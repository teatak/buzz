package buzz

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teatak/buzz/internal/protocol"
)

func createGeminiRouteTestStore(t *testing.T, baseURL, publicModel, upstreamModel, keySecret string) (*Store, []APIKey) {
	t.Helper()

	store := openTestStore(t)
	provider, err := store.CreateProvider(ProviderRecord{
		Name: providerGemini,
		Endpoints: []ProviderEndpoint{{
			Protocol: providerGemini,
			BaseURL:  baseURL,
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.CreateProviderKey(ProviderKey{
		ProviderID: provider.ID,
		Name:       "ga-1",
		Secret:     keySecret,
		Enabled:    true,
	}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	model, err := store.CreateModel(Model{
		Name:    publicModel,
		Enabled: true,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.SaveModelRoute(ModelRoute{
		ModelID:          model.ID,
		ProviderID:       provider.ID,
		UpstreamProtocol: providerGemini,
		UpstreamModel:    upstreamModel,
		Enabled:          true,
		Weight:           1,
	}, nil); err != nil {
		store.Close()
		t.Fatal(err)
	}
	keys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, keys
}

func compactJSONStringForTest(t *testing.T, raw string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func compactJSONRawForTest(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestOpenAIErrorTypeAndCodeMapsUpstreamStatuses(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantType string
		wantCode string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantType: "authentication_error", wantCode: "invalid_api_key"},
		{name: "forbidden", status: http.StatusForbidden, wantType: "permission_error", wantCode: "permission_denied"},
		{name: "too many requests", status: http.StatusTooManyRequests, wantType: "rate_limit_error", wantCode: "rate_limit_exceeded"},
		{name: "server error", status: http.StatusBadGateway, wantType: "server_error", wantCode: "upstream_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotCode := openAIErrorTypeAndCode(tt.status, "upstream_error")
			if gotType != tt.wantType || gotCode != tt.wantCode {
				t.Fatalf("type/code = %s/%s, want %s/%s", gotType, gotCode, tt.wantType, tt.wantCode)
			}
		})
	}
}

func TestOpenAIUpstreamErrorMessageParsesProviderBodies(t *testing.T) {
	tests := []struct {
		name   string
		status int
		raw    string
		want   string
	}{
		{
			name:   "gemini error object",
			status: http.StatusTooManyRequests,
			raw:    `{"error":{"code":429,"message":"Resource has been exhausted.","status":"RESOURCE_EXHAUSTED"}}`,
			want:   "Resource has been exhausted. (RESOURCE_EXHAUSTED)",
		},
		{
			name:   "string error",
			status: http.StatusUnauthorized,
			raw:    `{"error":"unauthorized"}`,
			want:   "unauthorized",
		},
		{
			name:   "empty body",
			status: http.StatusBadGateway,
			raw:    ``,
			want:   "Bad Gateway",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openAIUpstreamErrorMessage(tt.status, []byte(tt.raw)); got != tt.want {
				t.Fatalf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateOpenAIChatParameterSupport(t *testing.T) {
	trueValue := true
	falseValue := false
	zero := 0
	one := 1
	two := 2
	topLogprobs := 3

	tests := []struct {
		name    string
		req     protocol.OpenAIChatRequest
		wantErr string
	}{
		{name: "default", req: protocol.OpenAIChatRequest{}},
		{name: "n one", req: protocol.OpenAIChatRequest{N: &one}},
		{name: "logprobs false", req: protocol.OpenAIChatRequest{Logprobs: &falseValue}},
		{name: "n zero", req: protocol.OpenAIChatRequest{N: &zero}, wantErr: "n must be at least 1"},
		{name: "n greater than one", req: protocol.OpenAIChatRequest{N: &two}, wantErr: "n greater than 1 is not supported"},
		{name: "logprobs true", req: protocol.OpenAIChatRequest{Logprobs: &trueValue}, wantErr: "logprobs is not supported"},
		{name: "top logprobs", req: protocol.OpenAIChatRequest{TopLogprobs: &topLogprobs}, wantErr: "top_logprobs is not supported"},
		{name: "presence penalty", req: protocol.OpenAIChatRequest{PresencePenalty: float64PtrForTest(0.2)}, wantErr: "presence_penalty is not supported"},
		{name: "frequency penalty", req: protocol.OpenAIChatRequest{FrequencyPenalty: float64PtrForTest(0.3)}, wantErr: "frequency_penalty is not supported"},
		{name: "logit bias", req: protocol.OpenAIChatRequest{LogitBias: json.RawMessage(`{"42":1}`)}, wantErr: "logit_bias is not supported"},
		{name: "seed", req: protocol.OpenAIChatRequest{Seed: int64PtrForTest(123)}, wantErr: "seed is not supported"},
		{name: "user", req: protocol.OpenAIChatRequest{User: "alice"}, wantErr: "user is not supported"},
		{name: "metadata", req: protocol.OpenAIChatRequest{Metadata: json.RawMessage(`{"trace":"ok"}`)}, wantErr: "metadata is not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenAIChatParameterSupport(tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestGeminiThinkingLevelForOpenAIReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		effort  *string
		model   string
		want    string
		wantErr string
	}{
		{name: "unset", model: "gemini-3-flash-preview"},
		{name: "blank", effort: stringPtrForTest(" "), model: "gemini-3-flash-preview"},
		{name: "flash minimal", effort: stringPtrForTest("minimal"), model: "gemini-3-flash-preview", want: "MINIMAL"},
		{name: "flash medium", effort: stringPtrForTest("medium"), model: "gemini-3-flash-preview", want: "MEDIUM"},
		{name: "pro low", effort: stringPtrForTest("low"), model: "gemini-3-pro-preview", want: "LOW"},
		{name: "pro high", effort: stringPtrForTest("high"), model: "gemini-3-pro-preview", want: "HIGH"},
		{name: "pro medium", effort: stringPtrForTest("medium"), model: "gemini-3-pro-preview", want: "MEDIUM"},
		{name: "pro minimal maps low", effort: stringPtrForTest("minimal"), model: "gemini-3-pro-preview", want: "LOW"},
		{name: "none", effort: stringPtrForTest("none"), model: "gemini-3-flash-preview", wantErr: "reasoning_effort none is not supported for Gemini 3 models"},
		{name: "xhigh", effort: stringPtrForTest("xhigh"), model: "gemini-3-flash-preview", wantErr: "reasoning_effort xhigh is not supported for Gemini models"},
		{name: "gemini 2.5", effort: stringPtrForTest("low"), model: "gemini-2.5-flash", wantErr: "reasoning_effort mapping is only supported for Gemini 3 models"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := geminiThinkingLevelForOpenAIReasoningEffort(tt.effort, tt.model)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("thinking level = %q, want nil", *got)
				}
				return
			}
			if got == nil || *got != tt.want {
				t.Fatalf("thinking level = %v, want %q", got, tt.want)
			}
		})
	}
}

func stringPtrForTest(value string) *string {
	return &value
}

func int64PtrForTest(value int64) *int64 {
	return &value
}

func float64PtrForTest(value float64) *float64 {
	return &value
}

func TestOpenAIRetryErrorUsesMappedRateLimitError(t *testing.T) {
	resp := errorResponse(http.StatusTooManyRequests, `{"error":{"code":429,"message":"Resource has been exhausted.","status":"RESOURCE_EXHAUSTED"}}`)
	rr := httptest.NewRecorder()

	writeOpenAIRetryError(rr, resp, 2, 2, []string{"m(k1):429", "m(k2):429"})

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Type != "rate_limit_error" || got.Error.Code != "rate_limit_exceeded" {
		t.Fatalf("error = %+v", got.Error)
	}
	if !strings.Contains(got.Error.Message, "Resource has been exhausted.") {
		t.Fatalf("message = %q", got.Error.Message)
	}
	if rr.Header().Get("X-Proxy-Debug") != "m(k1):429 -> m(k2):429" {
		t.Fatalf("X-Proxy-Debug = %q", rr.Header().Get("X-Proxy-Debug"))
	}
}

func TestOpenAIChatCompletionsRoutesToGemini(t *testing.T) {
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
				"cachedContentTokenCount": 1,
				"thoughtsTokenCount": 2
			}
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "hi"}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
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

	var got protocol.OpenAIChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message.Content == nil || *got.Choices[0].Message.Content != "hello from gemini" {
		t.Fatalf("content = %v", got.Choices[0].Message.Content)
	}
	if got.Usage == nil || got.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if got.Usage.PromptTokensDetails == nil || got.Usage.PromptTokensDetails.CachedTokens != 1 {
		t.Fatalf("prompt token details = %+v", got.Usage.PromptTokensDetails)
	}
	if got.Usage.CompletionTokensDetails == nil || got.Usage.CompletionTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("completion token details = %+v", got.Usage.CompletionTokensDetails)
	}
	usage, err := store.UsageSummary(UsageQuery{
		Model: "gemini-3.5-flash",
		From:  time.Now().Add(-time.Minute),
		To:    time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.Requests != 1 || usage.Errors != 0 {
		t.Fatalf("usage summary = %+v", usage)
	}
	if usage.ByKey["alice-key"] != 1 {
		t.Fatalf("usage by key = %+v", usage.ByKey)
	}
	if usage.PromptTokens != 3 || usage.CompletionTokens != 4 || usage.TotalTokens != 7 || usage.CachedTokens != 1 || usage.ReasoningTokens != 2 {
		t.Fatalf("usage tokens = %+v", usage)
	}

	var usageProviderName, usageProviderKeyName, usageModel, usageUpstreamModel string
	var usageStatus int
	var promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens int64
	var rawUsage string
	if err := store.db.QueryRow(`
		SELECT provider_name, provider_key_name, model, upstream_model, status, prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens, raw_usage
		FROM usage_logs
		LIMIT 1`,
	).Scan(&usageProviderName, &usageProviderKeyName, &usageModel, &usageUpstreamModel, &usageStatus, &promptTokens, &completionTokens, &totalTokens, &cachedTokens, &reasoningTokens, &rawUsage); err != nil {
		t.Fatal(err)
	}
	if usageProviderName != "gemini" || usageProviderKeyName != "ga-1" || usageModel != "gemini-3.5-flash" || usageUpstreamModel != "gemini-3.5-flash" || usageStatus != http.StatusOK {
		t.Fatalf("usage log = provider %q key %q model %q upstream %q status %d", usageProviderName, usageProviderKeyName, usageModel, usageUpstreamModel, usageStatus)
	}
	if promptTokens != 3 || completionTokens != 4 || totalTokens != 7 || cachedTokens != 1 || reasoningTokens != 2 {
		t.Fatalf("usage log tokens = prompt %d completion %d total %d cached %d reasoning %d", promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens)
	}
	if !strings.Contains(rawUsage, `"cachedContentTokenCount":1`) || !strings.Contains(rawUsage, `"thoughtsTokenCount":2`) {
		t.Fatalf("raw usage = %s", rawUsage)
	}
}

func TestOpenAIChatReasoningEffortRoutesToGeminiThinkingLevel(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "fast-model", "gemini-3-flash-preview", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "fast-model",
		"reasoning_effort": "medium",
		"messages": [{"role": "user", "content": "hi"}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamBody.GenerationConfig == nil || upstreamBody.GenerationConfig.ThinkingConfig == nil {
		t.Fatalf("generation config = %+v", upstreamBody.GenerationConfig)
	}
	if got := upstreamBody.GenerationConfig.ThinkingConfig.ThinkingLevel; got != "MEDIUM" {
		t.Fatalf("thinking level = %q", got)
	}
}

func TestOpenAIChatResponseFormatRoutesToGemini(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat string
		wantMimeType   string
		wantSchema     string
	}{
		{
			name:           "json object",
			responseFormat: `{"type":"json_object"}`,
			wantMimeType:   "application/json",
		},
		{
			name:           "json schema",
			responseFormat: `{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}`,
			wantMimeType:   "application/json",
			wantSchema:     `{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamBody protocol.GeminiGenerateRequest
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
					t.Fatal(err)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
					"candidates": [{
						"content": {"role": "model", "parts": [{"text": "ok"}]},
						"finishReason": "STOP"
					}]
				}`))
			}))
			defer upstream.Close()

			upstreamURL, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
			defer store.Close()
			srv := &Server{
				store:    store,
				upstream: upstreamURL,
				client:   upstream.Client(),
				providers: map[string]Provider{
					providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
				},
				authTokens: map[string]AuthToken{
					"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

			body := `{
				"model": "gemini-3.5-flash",
				"messages": [{"role": "user", "content": "hi"}],
				"response_format": ` + tt.responseFormat + `
			}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer bh_valid")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if upstreamBody.GenerationConfig == nil {
				t.Fatal("generationConfig is nil")
			}
			if got := upstreamBody.GenerationConfig.ResponseMimeType; got != tt.wantMimeType {
				t.Fatalf("responseMimeType = %q, want %q", got, tt.wantMimeType)
			}
			if tt.wantSchema == "" {
				if len(upstreamBody.GenerationConfig.ResponseJSONSchema) != 0 {
					t.Fatalf("responseJsonSchema = %s", upstreamBody.GenerationConfig.ResponseJSONSchema)
				}
				return
			}
			if got := compactJSONRawForTest(t, upstreamBody.GenerationConfig.ResponseJSONSchema); got != compactJSONStringForTest(t, tt.wantSchema) {
				t.Fatalf("responseJsonSchema = %s, want %s", got, tt.wantSchema)
			}
		})
	}
}

func TestOpenAIChatRejectsUnsupportedResponseFormat(t *testing.T) {
	_, err := protocol.OpenAIChatToCanonical(protocol.OpenAIChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []protocol.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hi"`),
		}},
		ResponseFormat: json.RawMessage(`{"type":"xml"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported response_format") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIChatTextPartsRouteToGemini(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hi "},
				{"type": "text", "text": "there"}
			]
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Contents) != 1 || len(upstreamBody.Contents[0].Parts) != 2 {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
	if upstreamBody.Contents[0].Parts[0].Text != "hi " || upstreamBody.Contents[0].Parts[1].Text != "there" {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
}

func TestOpenAIChatToolsRouteToGemini(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {
			"type": "object",
			"properties": {"filters": {"type": "object", "additionalProperties": {"type": "string"}}},
			"additionalProperties": false
		}}}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Tools) != 1 || len(upstreamBody.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %+v", upstreamBody.Tools)
	}
	gotTool := upstreamBody.Tools[0].FunctionDeclarations[0]
	if gotTool.Name != "search" {
		t.Fatalf("tool = %+v", gotTool)
	}
	if len(gotTool.Parameters) != 0 {
		t.Fatalf("legacy parameters must be empty: %s", gotTool.Parameters)
	}
	var parameters map[string]any
	if err := json.Unmarshal(gotTool.ParametersJSONSchema, &parameters); err != nil {
		t.Fatal(err)
	}
	if parameters["type"] != "object" || parameters["additionalProperties"] != false {
		t.Fatalf("parameters = %+v", parameters)
	}
	properties := parameters["properties"].(map[string]any)
	filters := properties["filters"].(map[string]any)
	if _, ok := filters["additionalProperties"]; !ok {
		t.Fatalf("nested additionalProperties was lost: %+v", filters)
	}
}

func TestOpenAIChatToolChoiceRoutesToGeminiToolConfig(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}],
		"tool_choice": {"type": "function", "function": {"name": "search"}}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamBody.ToolConfig == nil || upstreamBody.ToolConfig.FunctionCallingConfig == nil {
		t.Fatalf("tool config = %+v", upstreamBody.ToolConfig)
	}
	cfg := upstreamBody.ToolConfig.FunctionCallingConfig
	if cfg.Mode != "ANY" {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if len(cfg.AllowedFunctionNames) != 1 || cfg.AllowedFunctionNames[0] != "search" {
		t.Fatalf("allowed functions = %+v", cfg.AllowedFunctionNames)
	}
}

func TestOpenAIChatToolChoiceRejectsUnknownFunction(t *testing.T) {
	_, err := protocol.OpenAIChatToCanonical(protocol.OpenAIChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []protocol.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hi"`),
		}},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]`),
		ToolChoice: json.RawMessage(`{
			"type": "function",
			"function": {"name": "lookup"}
		}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unknown function") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIChatToolChoiceModes(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]`)
	for _, tt := range []struct {
		name string
		raw  json.RawMessage
		mode string
	}{
		{name: "none", raw: json.RawMessage(`"none"`), mode: "NONE"},
		{name: "required", raw: json.RawMessage(`"required"`), mode: "ANY"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := protocol.OpenAIChatToCanonical(protocol.OpenAIChatRequest{
				Model: "gemini-3.5-flash",
				Messages: []protocol.OpenAIMessage{{
					Role:    "user",
					Content: json.RawMessage(`"hi"`),
				}},
				Tools:      tools,
				ToolChoice: tt.raw,
			})
			if err != nil {
				t.Fatal(err)
			}
			if req.ToolChoice == nil || req.ToolChoice.Mode != tt.mode {
				t.Fatalf("tool choice = %+v", req.ToolChoice)
			}
		})
	}
}

func TestOpenAIChatGeminiFunctionCallToOpenAIToolCall(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{
						"functionCall": {
							"name": "search",
							"args": {"query": "hello"}
						}
					}]
				},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got protocol.OpenAIChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %+v", got.Choices[0].FinishReason)
	}
	message := got.Choices[0].Message
	if message == nil || message.Content != nil || len(message.ToolCalls) != 1 {
		t.Fatalf("message = %+v", message)
	}
	call := message.ToolCalls[0]
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if call.Type != "function" || call.Function.Name != "search" || args["query"] != "hello" {
		t.Fatalf("tool call = %+v", call)
	}
}

func TestOpenAIChatReplaysGeminiThoughtSignature(t *testing.T) {
	var calls int
	var secondBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if calls == 1 {
			w.Write([]byte(`{
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [{
							"thoughtSignature": "sig-abc",
							"functionCall": {
								"name": "search",
								"args": {"query": "hello"}
							}
						}]
					},
					"finishReason": "STOP"
				}]
			}`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&secondBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	firstBody := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(firstBody))
	firstReq.Header.Set("Authorization", "Bearer bh_valid")
	firstRR := httptest.NewRecorder()
	srv.ServeHTTP(firstRR, firstReq)
	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstRR.Code, firstRR.Body.String())
	}
	var firstResp protocol.OpenAIChatResponse
	if err := json.Unmarshal(firstRR.Body.Bytes(), &firstResp); err != nil {
		t.Fatal(err)
	}
	callID := firstResp.Choices[0].Message.ToolCalls[0].ID

	secondBodyJSON := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "tool_calls": [{
				"id": "` + callID + `",
				"type": "function",
				"function": {"name": "search", "arguments": "{\"query\":\"hello\"}"}
			}]},
			{"role": "tool", "tool_call_id": "` + callID + `", "content": "{\"result\":\"ok\"}"}
		],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(secondBodyJSON))
	secondReq.Header.Set("Authorization", "Bearer bh_valid")
	secondRR := httptest.NewRecorder()
	srv.ServeHTTP(secondRR, secondReq)
	if secondRR.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", secondRR.Code, secondRR.Body.String())
	}
	if len(secondBody.Contents) < 2 || len(secondBody.Contents[1].Parts) == 0 || secondBody.Contents[1].Parts[0].FunctionCall == nil {
		t.Fatalf("second body = %+v", secondBody)
	}
	if got := secondBody.Contents[1].Parts[0].ThoughtSignature; got != "sig-abc" {
		t.Fatalf("thought signature = %q", got)
	}
}

func TestOpenAIChatReplaysGeminiThoughtSignatureByFunctionArguments(t *testing.T) {
	srv := &Server{}
	user := AuthToken{ID: 7, UserID: 11}
	srv.rememberToolSignatures(context.Background(), user, "public-model", []protocol.CanonicalToolCall{{
		ID:        "call_old",
		Name:      "search",
		Arguments: `{"b":2,"a":1}`,
		Signature: "sig-abc",
	}})
	req := &protocol.CanonicalRequest{
		Messages: []protocol.CanonicalMessage{{
			Role: "assistant",
			Parts: []protocol.CanonicalPart{{
				Type:       "tool_call",
				ToolCallID: "call_new",
				Name:       "search",
				Arguments:  json.RawMessage(`{"a":1,"b":2}`),
			}},
		}},
	}

	srv.applyToolSignatures(context.Background(), user, "public-model", req)

	if got := req.Messages[0].Parts[0].Signature; got != "sig-abc" {
		t.Fatalf("thought signature = %q", got)
	}

	req.Messages[0].Parts[0].Signature = ""
	srv.applyToolSignatures(context.Background(), AuthToken{ID: 8, UserID: 11}, "public-model", req)
	if got := req.Messages[0].Parts[0].Signature; got != "" {
		t.Fatalf("signature leaked across API keys: %q", got)
	}
}

func TestOpenAIChatToolResultConvertsToGeminiFunctionResponse(t *testing.T) {
	req := protocol.OpenAIChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []protocol.OpenAIMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"what is the weather"`),
			},
			{
				Role:    "assistant",
				Content: json.RawMessage(`null`),
				ToolCalls: []protocol.OpenAIToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: protocol.OpenAIToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"city":"Paris"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    json.RawMessage(`"{\"temperature\":21}"`),
			},
		},
	}

	canonicalReq, err := protocol.OpenAIChatToCanonical(req)
	if err != nil {
		t.Fatal(err)
	}
	geminiReq, err := protocol.CanonicalToGeminiGenerateRequest(canonicalReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(geminiReq.Contents) != 3 {
		t.Fatalf("contents = %+v", geminiReq.Contents)
	}
	if geminiReq.Contents[1].Role != "model" || len(geminiReq.Contents[1].Parts) != 1 {
		t.Fatalf("assistant content = %+v", geminiReq.Contents[1])
	}
	call := geminiReq.Contents[1].Parts[0].FunctionCall
	if call == nil || call.Name != "get_weather" {
		t.Fatalf("function call = %+v", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil {
		t.Fatal(err)
	}
	if args["city"] != "Paris" {
		t.Fatalf("args = %+v", args)
	}

	if geminiReq.Contents[2].Role != "user" || len(geminiReq.Contents[2].Parts) != 1 {
		t.Fatalf("tool content = %+v", geminiReq.Contents[2])
	}
	response := geminiReq.Contents[2].Parts[0].FunctionResponse
	if response == nil || response.Name != "get_weather" {
		t.Fatalf("function response = %+v", response)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Response, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["temperature"] != float64(21) {
		t.Fatalf("response = %+v", payload)
	}
}

func TestOpenAIChatToolResultRoutesToGemini(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "sunny"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "weather"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Paris\"}"
					}
				}]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "{\"temperature\":21}"
			}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"parameters": {"type": "object"}
			}
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Contents) != 3 {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
	if upstreamBody.Contents[1].Role != "model" || upstreamBody.Contents[1].Parts[0].FunctionCall == nil {
		t.Fatalf("assistant content = %+v", upstreamBody.Contents[1])
	}
	if upstreamBody.Contents[2].Role != "user" || upstreamBody.Contents[2].Parts[0].FunctionResponse == nil {
		t.Fatalf("tool content = %+v", upstreamBody.Contents[2])
	}
	response := upstreamBody.Contents[2].Parts[0].FunctionResponse
	if response.Name != "get_weather" {
		t.Fatalf("function response = %+v", response)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Response, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["temperature"] != float64(21) {
		t.Fatalf("response = %+v", payload)
	}

	var got protocol.OpenAIChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message.Content == nil || *got.Choices[0].Message.Content != "sunny" {
		t.Fatalf("content = %+v", got.Choices[0].Message)
	}
}

func TestOpenAIChatRejectsUnknownToolCallID(t *testing.T) {
	_, err := protocol.OpenAIChatToCanonical(protocol.OpenAIChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []protocol.OpenAIMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
			{Role: "tool", ToolCallID: "missing", Content: json.RawMessage(`"ok"`)},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown tool_call_id") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIChatToolsStreamTranslatesGeminiFunctionCall(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{"query":"hello"}}}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Tools) != 1 || len(upstreamBody.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %+v", upstreamBody.Tools)
	}
	var toolCall protocol.OpenAIToolCall
	var arguments strings.Builder
	var finishReason string
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk protocol.OpenAIChatResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta != nil && len(choice.Delta.ToolCalls) > 0 {
			call := choice.Delta.ToolCalls[0]
			if call.ID != "" {
				toolCall = call
			}
			arguments.WriteString(call.Function.Arguments)
		}
		if choice.FinishReason != nil {
			finishReason = *choice.FinishReason
		}
	}
	if toolCall.ID == "" {
		t.Fatalf("stream missing tool call chunk: %s", rr.Body.String())
	}
	if toolCall.Index == nil || *toolCall.Index != 0 {
		t.Fatalf("tool call index = %v", toolCall.Index)
	}
	if toolCall.Type != "function" || toolCall.Function.Name != "search" {
		t.Fatalf("tool call = %+v", toolCall)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(arguments.String()), &args); err != nil {
		t.Fatal(err)
	}
	if args["query"] != "hello" {
		t.Fatalf("arguments = %+v", args)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", finishReason)
	}
}

func TestOpenAIChatToolsStreamStoresThoughtSignatureForNextTurn(t *testing.T) {
	var upstreamBodies []protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstreamBody protocol.GeminiGenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		upstreamBodies = append(upstreamBodies, upstreamBody)

		if len(upstreamBodies) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search","args":{"query":"hello"}},"thoughtSignature":"sig-stream"}]},"finishReason":"STOP"}]}` + "\n\n"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "done"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	firstBody := `{
		"model": "gemini-3.5-flash",
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`
	firstReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(firstBody))
	firstReq.Header.Set("Authorization", "Bearer bh_valid")
	firstRR := httptest.NewRecorder()

	srv.ServeHTTP(firstRR, firstReq)

	if firstRR.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstRR.Code, firstRR.Body.String())
	}

	var streamedCall protocol.OpenAIToolCall
	for _, line := range strings.Split(firstRR.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk protocol.OpenAIChatResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil && len(chunk.Choices[0].Delta.ToolCalls) > 0 {
			streamedCall = chunk.Choices[0].Delta.ToolCalls[0]
			break
		}
	}
	if streamedCall.ID == "" {
		t.Fatalf("stream missing tool call id: %s", firstRR.Body.String())
	}

	secondBody := fmt.Sprintf(`{
		"model": "gemini-3.5-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": %q,
					"type": "function",
					"function": {
						"name": "search",
						"arguments": "{\"query\":\"hello\"}"
					}
				}]
			},
			{"role": "tool", "tool_call_id": %q, "content": "{\"result\":\"world\"}"}
		],
		"tools": [{"type": "function", "function": {"name": "search", "parameters": {"type": "object"}}}]
	}`, streamedCall.ID, streamedCall.ID)
	secondReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(secondBody))
	secondReq.Header.Set("Authorization", "Bearer bh_valid")
	secondRR := httptest.NewRecorder()

	srv.ServeHTTP(secondRR, secondReq)

	if secondRR.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", secondRR.Code, secondRR.Body.String())
	}
	if len(upstreamBodies) != 2 {
		t.Fatalf("upstream request count = %d", len(upstreamBodies))
	}
	if got := upstreamBodies[1].Contents[1].Parts[0].ThoughtSignature; got != "sig-stream" {
		t.Fatalf("thought signature = %q", got)
	}
}

func TestOpenAIChatImageDataURLPartRoutesToGemini(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "describe"},
				{"type": "image_url", "image_url": {"url": "data:image/png;base64,aGVsbG8="}}
			]
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Contents) != 1 || len(upstreamBody.Contents[0].Parts) != 2 {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
	imagePart := upstreamBody.Contents[0].Parts[1]
	if imagePart.InlineData == nil || imagePart.InlineData.MimeType != "image/png" || imagePart.InlineData.Data != "aGVsbG8=" {
		t.Fatalf("image part = %+v", imagePart)
	}
}

func TestOpenAIChatInputAudioPartRoutesToGemini(t *testing.T) {
	var upstreamBody protocol.GeminiGenerateRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model": "gemini-3.5-flash",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "transcribe"},
				{"type": "input_audio", "input_audio": {"data": "aGVsbG8=", "format": "wav"}}
			]
		}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamBody.Contents) != 1 || len(upstreamBody.Contents[0].Parts) != 2 {
		t.Fatalf("contents = %+v", upstreamBody.Contents)
	}
	audioPart := upstreamBody.Contents[0].Parts[1]
	if audioPart.InlineData == nil || audioPart.InlineData.MimeType != "audio/wav" || audioPart.InlineData.Data != "aGVsbG8=" {
		t.Fatalf("audio part = %+v", audioPart)
	}
}

func TestOpenAIChatRejectsRemoteImageURLPart(t *testing.T) {
	_, err := protocol.OpenAIChatToCanonical(protocol.OpenAIChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []protocol.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]`),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "data URL image") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIChatRejectsUnconfiguredModelRoute(t *testing.T) {
	store := openTestStore(t)

	srv := &Server{store: store}
	body := `{"model":"missing-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "model_not_found") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestOpenAIChatUsesModelRouteUpstreamModel(t *testing.T) {
	var upstreamPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "target ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	provider, err := store.CreateProvider(ProviderRecord{
		Name: providerGemini,
		Endpoints: []ProviderEndpoint{{
			Protocol: providerGemini,
			BaseURL:  upstream.URL,
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProviderKey(ProviderKey{
		ProviderID: provider.ID,
		Name:       "k01-alpha",
		Secret:     "AIza-secret",
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	providerID := provider.ID
	now := storeNow()
	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"public-model", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
		modelID, providerID, providerGemini, "gemini-upstream", now, now,
	); err != nil {
		t.Fatal(err)
	}
	keys, err := store.ProviderAPIKeys(providerGemini)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1beta/models/gemini-upstream:generateContent" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
}

func TestOpenAIChatStreamTranslatesGeminiSSE(t *testing.T) {
	var upstreamPath string
	var upstreamAlt string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamAlt = r.URL.Query().Get("alt")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n"))
		w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\"}]}\n\n"))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{"model":"gemini-3.5-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1beta/models/gemini-3.5-flash:streamGenerateContent" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamAlt != "sse" {
		t.Fatalf("alt = %q", upstreamAlt)
	}
	bodyText := rr.Body.String()
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"hel"`,
		`"content":"lo"`,
		`"finish_reason":"stop"`,
		"data: [DONE]",
	} {
		if !strings.Contains(bodyText, want) {
			t.Fatalf("stream missing %q: %s", want, bodyText)
		}
	}
	if strings.Contains(bodyText, `"usage"`) {
		t.Fatalf("stream should not include usage without stream_options.include_usage: %s", bodyText)
	}
}

func TestOpenAIChatStreamIncludesUsageWhenRequested(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5,"cachedContentTokenCount":1,"thoughtsTokenCount":1}}` + "\n\n"))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "gemini-3.5-flash", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{"model":"gemini-3.5-flash","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	bodyText := rr.Body.String()
	for _, want := range []string{
		`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5`,
		`"prompt_tokens_details":{"cached_tokens":1}`,
		`"completion_tokens_details":{"reasoning_tokens":1}`,
		`"content":"ok"`,
		"data: [DONE]",
	} {
		if !strings.Contains(bodyText, want) {
			t.Fatalf("stream missing %q: %s", want, bodyText)
		}
	}
}

func TestOpenAIChatPassesThroughOpenAICompatibleProvider(t *testing.T) {
	var upstreamPath string
	var upstreamRawQuery string
	var upstreamAuth string
	var upstreamXGoog string
	var upstreamBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamRawQuery = r.URL.RawQuery
		upstreamAuth = r.Header.Get("Authorization")
		upstreamXGoog = r.Header.Get("x-goog-api-key")
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"chatcmpl-upstream","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)

	now := storeNow()
	providerID, err := store.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"openrouter", "openrouter", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_endpoints (provider_id, protocol, base_url, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		providerID, providerOpenAI, upstream.URL+"/v1", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, '', ?, ?)`,
		providerID, "or-main", "sk-secret", "cret", now, now,
	); err != nil {
		t.Fatal(err)
	}
	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"public-gpt", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
		modelID, providerID, providerOpenAI, "gpt-upstream", now, now,
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
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	body := `{
		"model":"public-gpt",
		"n":2,
		"future_option":{"enabled":true},
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?key=client-query&trace=1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	req.Header.Set("x-goog-api-key", "should-not-forward")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if strings.Contains(upstreamRawQuery, "key=") {
		t.Fatalf("upstream query forwarded client key: %q", upstreamRawQuery)
	}
	if !strings.Contains(upstreamRawQuery, "trace=1") {
		t.Fatalf("upstream query = %q", upstreamRawQuery)
	}
	if upstreamAuth != "Bearer sk-secret" {
		t.Fatalf("upstream auth = %q", upstreamAuth)
	}
	if upstreamXGoog != "" {
		t.Fatalf("upstream x-goog-api-key = %q", upstreamXGoog)
	}
	if upstreamBody["model"] != "gpt-upstream" {
		t.Fatalf("upstream model = %v", upstreamBody["model"])
	}
	if tools, ok := upstreamBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("upstream tools = %#v", upstreamBody["tools"])
	}
	if upstreamBody["n"] != float64(2) || upstreamBody["future_option"] == nil {
		t.Fatalf("passthrough fields = %#v", upstreamBody)
	}
	if !strings.Contains(rr.Body.String(), "chatcmpl-upstream") {
		t.Fatalf("response = %s", rr.Body.String())
	}
}

func TestOpenAIResponsesPassesThroughOpenAICompatibleProvider(t *testing.T) {
	var upstreamPath string
	var upstreamBody map[string]any
	var upstreamAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		upstreamAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resp-upstream","object":"response","output":[],"usage":{"input_tokens":7,"output_tokens":5,"total_tokens":12,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)

	now := storeNow()
	providerID, err := store.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"openrouter", "openrouter", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_endpoints (provider_id, protocol, base_url, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		providerID, providerOpenAIResponses, upstream.URL+"/v1", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, '', ?, ?)`,
		providerID, "or-main", "sk-secret", "cret", now, now,
	); err != nil {
		t.Fatal(err)
	}
	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"public-response", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
		modelID, providerID, providerOpenAIResponses, "openai/gpt-oss-120b", now, now,
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
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-response","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamAuth != "Bearer sk-secret" {
		t.Fatalf("upstream auth = %q", upstreamAuth)
	}
	if upstreamBody["model"] != "openai/gpt-oss-120b" {
		t.Fatalf("upstream model = %v", upstreamBody["model"])
	}
	if !strings.Contains(rr.Body.String(), "resp-upstream") {
		t.Fatalf("response = %s", rr.Body.String())
	}

	var promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens int64
	if err := store.queryRow(`
		SELECT prompt_tokens, completion_tokens, total_tokens, cached_tokens, reasoning_tokens
		FROM usage_logs
		WHERE model = ?`,
		"public-response",
	).Scan(&promptTokens, &completionTokens, &totalTokens, &cachedTokens, &reasoningTokens); err != nil {
		t.Fatal(err)
	}
	if promptTokens != 7 || completionTokens != 5 || totalTokens != 12 || cachedTokens != 2 || reasoningTokens != 3 {
		t.Fatalf("usage = prompt:%d completion:%d total:%d cached:%d reasoning:%d", promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens)
	}
}

func TestOpenAIResponsesRoutesToOpenAIChat(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIChatRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
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
				"prompt_tokens_details":{"cached_tokens":2},
				"completion_tokens_details":{"reasoning_tokens":3}
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
	model, err := store.CreateModel(Model{Name: "public-response-chat", Enabled: true})
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
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-response-chat","instructions":"be brief","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamBody.Model != "gpt-upstream" || len(upstreamBody.Messages) != 2 {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}
	var got protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "response" || got.Model != "public-response-chat" || got.Output[0].Content[0].Text != "hello from chat" {
		t.Fatalf("response = %+v", got)
	}
	if got.Usage == nil || got.Usage.InputTokens != 7 || got.Usage.InputTokensDetails.CachedTokens != 2 || got.Usage.OutputTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestOpenAIResponsesRoutesToGemini(t *testing.T) {
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
				"cachedContentTokenCount": 1,
				"thoughtsTokenCount": 2
			}
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, keys := createGeminiRouteTestStore(t, upstream.URL, "public-response-gemini", "gemini-3.5-flash", "AIza-secret")
	defer store.Close()
	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"public-response-gemini","instructions":"be brief","input":"hi"}`))
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
	var got protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "response" || got.Model != "public-response-gemini" || got.Output[0].Content[0].Text != "hello from gemini" {
		t.Fatalf("response = %+v", got)
	}
	if got.Usage == nil || got.Usage.InputTokens != 3 || got.Usage.InputTokensDetails.CachedTokens != 1 || got.Usage.OutputTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v", got.Usage)
	}
}

func TestOpenAICompatibleStreamPassThroughFlushesChunks(t *testing.T) {
	var releaseSecondOnce sync.Once
	releaseSecond := make(chan struct{})
	defer releaseSecondOnce.Do(func() { close(releaseSecond) })
	var upstreamAcceptEncoding string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAcceptEncoding = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-releaseSecond
		fmt.Fprint(w, `data: {"id":"chatcmpl-upstream","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":13,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":5},"completion_tokens_details":{"reasoning_tokens":7}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)

	now := storeNow()
	providerID, err := store.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"openrouter", "openrouter", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_endpoints (provider_id, protocol, base_url, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		providerID, providerOpenAI, upstream.URL, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, '', ?, ?)`,
		providerID, "or-main", "sk-secret", "cret", now, now,
	); err != nil {
		t.Fatal(err)
	}
	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"public-gpt", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
		modelID, providerID, providerOpenAI, "gpt-upstream", now, now,
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
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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

	proxy := httptest.NewServer(srv)
	defer proxy.Close()
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{
		"model":"public-gpt",
		"stream":true,
		"messages":[{"role":"user","content":"hi"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer bh_valid")
	req.Header.Set("Content-Type", "application/json")
	resp, err := proxy.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if upstreamAcceptEncoding != "identity" {
		t.Fatalf("upstream Accept-Encoding = %q, want identity", upstreamAcceptEncoding)
	}

	reader := bufio.NewReader(resp.Body)
	firstLine := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		firstLine <- line
	}()

	select {
	case line := <-firstLine:
		if line != "data: first\n" {
			t.Fatalf("first line = %q", line)
		}
	case err := <-readErr:
		t.Fatal(err)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first stream chunk was buffered until the upstream response completed")
	}

	releaseSecondOnce.Do(func() { close(releaseSecond) })
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rest), `"usage":{"prompt_tokens":11`) {
		t.Fatalf("stream rest missing usage: %s", string(rest))
	}
	summary, err := store.UsageSummary(UsageQuery{
		Model: "public-gpt",
		From:  time.Now().Add(-time.Hour),
		To:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.PromptTokens != 11 || summary.CompletionTokens != 13 || summary.TotalTokens != 24 || summary.CachedTokens != 5 || summary.ReasoningTokens != 7 {
		t.Fatalf("usage summary = %+v", summary)
	}
}

func TestOpenAIModelsListsEnabledModels(t *testing.T) {
	store := openTestStore(t)

	if _, err := store.CreateModel(Model{Name: "enabled-model", DisplayName: "Saved Name", ContextWindow: 65536, MaxInputTokens: 60000, MaxOutputTokens: 8192, Capabilities: `{"vision":false,"audio_input":false,"tools":true,"reasoning":false,"json_schema":false}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateModel(Model{Name: "disabled-model", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{
		store: store,
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
		},
	}

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	unauthorizedRR := httptest.NewRecorder()
	srv.ServeHTTP(unauthorizedRR, unauthorizedReq)
	if unauthorizedRR.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", unauthorizedRR.Code, unauthorizedRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got openAIModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "enabled-model" {
		t.Fatalf("models = %+v", got.Data)
	}
	m := got.Data[0]
	if m.Name != "Saved Name" || m.ContextLength != 65536 || m.TopProvider == nil || m.TopProvider.MaxCompletionTokens != 8192 || m.Architecture == nil || len(m.Architecture.InputModalities) != 1 || m.Architecture.InputModalities[0] != "text" || m.SupportedParameters == nil || len(*m.SupportedParameters) != 2 || (*m.SupportedParameters)[0] != "tools" {
		t.Fatalf("saved metadata lost: %+v", m)
	}
}

func TestNonOpenAIEndpointIsNotExposed(t *testing.T) {
	store := openTestStore(t)

	srv := &Server{store: store}
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/missing-model:generateContent", strings.NewReader(`{"contents":[]}`))
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not found") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestOpenAIChatSwitchesModelRoutesWhenRouteKeysCooling(t *testing.T) {
	var upstreamPaths []string
	var upstreamKeys []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPaths = append(upstreamPaths, r.URL.Path)
		upstreamKey := r.URL.Query().Get("key")
		upstreamKeys = append(upstreamKeys, upstreamKey)
		if strings.Contains(r.URL.Path, "/gemini-a:") {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "route ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	provider, err := store.CreateProvider(ProviderRecord{
		Name: providerGemini,
		Endpoints: []ProviderEndpoint{{
			Protocol: providerGemini,
			BaseURL:  upstream.URL,
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []ProviderKey{
		{ProviderID: provider.ID, Name: "k01-alpha", Secret: "AIza-a", Enabled: true},
		{ProviderID: provider.ID, Name: "k02-alpha", Secret: "AIza-b", Enabled: true},
	} {
		if _, err := store.CreateProviderKey(key); err != nil {
			t.Fatal(err)
		}
	}
	providerID := provider.ID
	now := storeNow()
	modelID, err := store.insertReturningID(
		`INSERT INTO models (name, selection_policy, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"public-model", "round_robin", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		model string
	}{
		{"gemini-a"},
		{"gemini-b"},
	} {
		if _, err := store.exec(
			`INSERT INTO model_routes (model_id, provider_id, upstream_protocol, upstream_model, enabled, priority, weight, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, ?, ?)`,
			modelID, providerID, providerGemini, target.model, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		store:    store,
		upstream: upstreamURL,
		client:   upstream.Client(),
		providers: map[string]Provider{
			providerGemini: NewGeminiProvider(upstreamURL, upstream.Client()),
		},
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "alice-key", UserName: "alice", Valid: true},
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
	srv.cfg.Retry.MaxAttempts = 4

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(upstreamPaths) != 3 {
		t.Fatalf("upstream calls = %d, paths = %v keys = %v", len(upstreamPaths), upstreamPaths, upstreamKeys)
	}
	if upstreamPaths[0] != "/v1beta/models/gemini-a:generateContent" || upstreamPaths[1] != "/v1beta/models/gemini-a:generateContent" || upstreamPaths[2] != "/v1beta/models/gemini-b:generateContent" {
		t.Fatalf("upstream paths = %v", upstreamPaths)
	}
	if upstreamKeys[0] != "AIza-a" || upstreamKeys[1] != "AIza-b" || upstreamKeys[2] == "" {
		t.Fatalf("upstream keys = %v", upstreamKeys)
	}
}
