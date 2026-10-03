package buzz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teatak/buzz/internal/protocol"
)

func TestAnthropicPassthrough(t *testing.T) {
	var upstreamPath string
	var upstreamMethod string
	var upstreamApiKey string
	var upstreamVersion string
	var upstreamBody []byte

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamMethod = r.Method
		upstreamApiKey = r.Header.Get("x-api-key")
		upstreamVersion = r.Header.Get("anthropic-version")
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		upstreamBody = buf[:n]

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","content":[{"type":"text","text":"hello from anthropic passthrough"}]}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t)
	defer store.Close()

	now := storeNow()
	providerID, err := store.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"claude-provider", "anthropic", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_endpoints (provider_id, protocol, base_url, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		providerID, providerAnthropic, upstream.URL, now, now,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.exec(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, '', ?, ?)`,
		providerID, "claude-key-1", "mock-anthropic-secret-key", "key1", now, now,
	); err != nil {
		t.Fatal(err)
	}

	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"claude-3-5-sonnet", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.exec(
		`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
		modelID, providerID, providerAnthropic, "claude-3-5-sonnet-upstream", now, now,
	); err != nil {
		t.Fatal(err)
	}

	providerRecords, err := store.EnabledProviders()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := newProviderRegistry(providerRecords, upstreamURL, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		store:     store,
		upstream:  upstreamURL,
		client:    upstream.Client(),
		providers: providers,
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "user-key-1", UserName: "user1", Valid: true},
		},
		keyState: &KeyState{
			keys:         keys,
			cooldown:     time.Minute,
			rpdCooldown:  time.Hour,
			exhausted:    make(map[string]time.Time),
			cooldownHits: make(map[string]int),
			rpdLike:      make(map[string]bool),
			errors:       make(map[string]KeyError),
		},
		stats: Stats{
			StartedAt: time.Now(),
			Exhausted: make(map[string]string),
			RPDLike:   make(map[string]bool),
			KeyErrors: make(map[string]KeyError),
		},
	}

	body := `{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/messages" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamMethod != http.MethodPost {
		t.Fatalf("upstream method = %q", upstreamMethod)
	}
	if upstreamApiKey != "mock-anthropic-secret-key" {
		t.Fatalf("upstream api key = %q", upstreamApiKey)
	}
	if upstreamVersion != "2023-06-01" {
		t.Fatalf("upstream version = %q", upstreamVersion)
	}

	var reqBody map[string]any
	if err := json.Unmarshal(upstreamBody, &reqBody); err != nil {
		t.Fatal(err)
	}
	// Upstream model rewritten correctly
	if reqBody["model"] != "claude-3-5-sonnet-upstream" {
		t.Fatalf("upstream model = %q", reqBody["model"])
	}

	var respBody map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &respBody); err != nil {
		t.Fatal(err)
	}
	if respBody["id"] != "msg_123" {
		t.Fatalf("response id = %q", respBody["id"])
	}
}

func TestAnthropicRoutesToOpenAIChat(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIChatRequest
	var upstreamAuthorization string
	var upstreamAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamAuthorization = r.Header.Get("Authorization")
		upstreamAPIKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"id":"chatcmpl-upstream",
			"object":"chat.completion",
			"created":123,
			"model":"gpt-upstream",
			"choices":[{"message":{"role":"assistant","content":"hello from chat"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":7,
				"completion_tokens":5,
				"total_tokens":12,
				"prompt_tokens_details":{"cached_tokens":2}
			}
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	defer store.Close()
	provider, err := store.CreateProvider(ProviderRecord{
		Name: "openrouter",
		Endpoints: []ProviderEndpoint{{
			Protocol: providerOpenAI,
			BaseURL:  upstream.URL + "/v1",
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProviderKey(ProviderKey{ProviderID: provider.ID, Name: "or-main", Secret: "sk-secret", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(Model{Name: "claude-public-chat", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveModelRoute(ModelRoute{ModelID: model.ID, ProviderID: provider.ID, UpstreamProtocol: providerOpenAI, UpstreamModel: "gpt-upstream", Enabled: true, Weight: 1}, nil); err != nil {
		t.Fatal(err)
	}
	providerRecords, err := store.EnabledProviders()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := newProviderRegistry(providerRecords, upstreamURL, upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		store:     store,
		upstream:  upstreamURL,
		client:    upstream.Client(),
		providers: providers,
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "user-key-1", UserName: "user1", Valid: true},
		},
		keyState: &KeyState{
			keys:         keys,
			cooldown:     time.Minute,
			rpdCooldown:  time.Hour,
			exhausted:    make(map[string]time.Time),
			cooldownHits: make(map[string]int),
			rpdLike:      make(map[string]bool),
			errors:       make(map[string]KeyError),
		},
		stats: Stats{
			StartedAt: time.Now(),
			Exhausted: make(map[string]string),
			RPDLike:   make(map[string]bool),
			KeyErrors: make(map[string]KeyError),
		},
	}
	srv.cfg.Retry.MaxAttempts = 2

	body := `{"model":"claude-public-chat","system":"be brief","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("x-api-key", "bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamAuthorization != "Bearer sk-secret" || upstreamAPIKey != "" {
		t.Fatalf("upstream auth = %q, x-api-key = %q", upstreamAuthorization, upstreamAPIKey)
	}
	if upstreamBody.Model != "gpt-upstream" || upstreamBody.MaxOutputTokens == nil || *upstreamBody.MaxOutputTokens != 64 {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}
	if len(upstreamBody.Messages) != 2 || string(upstreamBody.Messages[0].Content) != `"be brief"` || string(upstreamBody.Messages[1].Content) != `"hi"` {
		t.Fatalf("messages = %+v", upstreamBody.Messages)
	}
	var got protocol.AnthropicMessagesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "message" || got.Model != "claude-public-chat" || got.Content[0].Text != "hello from chat" {
		t.Fatalf("response = %+v", got)
	}
	if got.Usage.InputTokens != 5 || got.Usage.OutputTokens != 5 || got.Usage.CacheReadInputTokens != 2 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestAnthropicRoutesToGemini(t *testing.T) {
	var upstreamPath string
	var upstreamKey string
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamKey = r.URL.Query().Get("key")
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "hello from gemini"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 3,
				"candidatesTokenCount": 4,
				"totalTokenCount": 7,
				"cachedContentTokenCount": 1
			}
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "claude-public-gemini", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "user-key-1", UserName: "user1", Valid: true},
		},
		keyState: &KeyState{
			keys:         keys,
			cooldown:     time.Minute,
			rpdCooldown:  time.Hour,
			exhausted:    make(map[string]time.Time),
			cooldownHits: make(map[string]int),
			rpdLike:      make(map[string]bool),
			errors:       make(map[string]KeyError),
		},
		stats: Stats{
			StartedAt: time.Now(),
			Exhausted: make(map[string]string),
			RPDLike:   make(map[string]bool),
			KeyErrors: make(map[string]KeyError),
		},
	}
	srv.cfg.Retry.MaxAttempts = 2

	body := `{"model":"claude-public-gemini","system":"be brief","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1beta/models/gemini-3.5-flash:generateContent" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamKey != "AIza-secret" {
		t.Fatalf("upstream key = %q", upstreamKey)
	}
	if upstreamBody.SystemInstruction == nil || upstreamBody.SystemInstruction.Parts[0].Text != "be brief" {
		t.Fatalf("system instruction = %+v", upstreamBody.SystemInstruction)
	}
	if len(upstreamBody.Contents) != 1 || upstreamBody.Contents[0].Parts[0].Text != "hi" {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
	var got protocol.AnthropicMessagesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "message" || got.Model != "claude-public-gemini" || got.Content[0].Text != "hello from gemini" {
		t.Fatalf("response = %+v", got)
	}
	if got.Usage.InputTokens != 2 || got.Usage.OutputTokens != 4 || got.Usage.CacheReadInputTokens != 1 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}
