package buzzhive

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teatak/buzzhive/internal/protocol"
)

func createRouteTestServer(t *testing.T, proto string, baseURL string, publicModel string, upstreamModel string, keySecret string, client *http.Client) (*Server, *Store) {
	t.Helper()
	upstreamURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t)
	provider, err := store.CreateProvider(ProviderRecord{
		Name: proto + "-provider",
		Endpoints: []ProviderEndpoint{{
			Protocol: proto,
			BaseURL:  baseURL,
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProviderKey(ProviderKey{ProviderID: provider.ID, Name: proto + "-key", Secret: keySecret, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	model, err := store.CreateModel(Model{Name: publicModel, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveModelRoute(ModelRoute{ModelID: model.ID, ProviderID: provider.ID, UpstreamProtocol: proto, UpstreamModel: upstreamModel, Enabled: true, Weight: 1}, nil); err != nil {
		t.Fatal(err)
	}
	providerRecords, err := store.EnabledProviders()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := newProviderRegistry(providerRecords, upstreamURL, client)
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
		client:    client,
		providers: providers,
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "user-key", UserName: "user1", Valid: true},
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
	return srv, store
}

func TestCrossProtocolUpstreamErrorsUseInboundShape(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		assert func(*testing.T, []byte)
	}{
		{
			name: "anthropic",
			path: "/v1/messages",
			body: `{"model":"anthropic-error-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			assert: func(t *testing.T, body []byte) {
				t.Helper()
				var got struct {
					Type  string `json:"type"`
					Error struct {
						Type string `json:"type"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if got.Type != "error" || got.Error.Type != "rate_limit_error" {
					t.Fatalf("response = %+v", got)
				}
			},
		},
		{
			name: "gemini",
			path: "/v1beta/models/gemini-error-model:generateContent",
			body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			assert: func(t *testing.T, body []byte) {
				t.Helper()
				var got struct {
					Error struct {
						Code   int    `json:"code"`
						Status string `json:"status"`
					} `json:"error"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if got.Error.Code != http.StatusTooManyRequests || got.Error.Status != "RESOURCE_EXHAUSTED" {
					t.Fatalf("response = %+v", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			}))
			defer upstream.Close()

			publicModel := tt.name + "-error-model"
			srv, store := createRouteTestServer(t, providerOpenAI, upstream.URL+"/v1", publicModel, "upstream-model", "sk-secret", upstream.Client())
			defer store.Close()

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer bh_valid")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			tt.assert(t, rr.Body.Bytes())
		})
	}
}

func TestCrossProtocolTextStreamMatrix(t *testing.T) {
	protocols := []string{
		providerOpenAI,
		providerOpenAIResponses,
		providerAnthropic,
		providerGemini,
	}
	for _, inbound := range protocols {
		for _, outbound := range protocols {
			if inbound == outbound {
				continue
			}
			name := inbound + "_to_" + outbound
			t.Run(name, func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeProtocolTextStream(t, w, outbound)
				}))
				defer upstream.Close()

				baseURL := upstream.URL
				if outbound == providerOpenAI || outbound == providerOpenAIResponses {
					baseURL += "/v1"
				}
				publicModel := "stream-" + name
				srv, store := createRouteTestServer(t, outbound, baseURL, publicModel, "upstream-model", "secret", upstream.Client())
				defer store.Close()

				path, body := protocolStreamRequest(inbound, publicModel)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer bh_valid")
				rr := httptest.NewRecorder()

				srv.ServeHTTP(rr, req)

				if rr.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), "hello") {
					t.Fatalf("stream does not contain text delta: %s", rr.Body.String())
				}
				switch inbound {
				case providerOpenAI:
					if !strings.Contains(rr.Body.String(), `"object":"chat.completion.chunk"`) ||
						!strings.Contains(rr.Body.String(), "data: [DONE]") {
						t.Fatalf("invalid OpenAI Chat stream: %s", rr.Body.String())
					}
				case providerOpenAIResponses:
					if !strings.Contains(rr.Body.String(), "event: response.output_text.delta") ||
						!strings.Contains(rr.Body.String(), "event: response.completed") {
						t.Fatalf("invalid Responses stream: %s", rr.Body.String())
					}
				case providerAnthropic:
					if !strings.Contains(rr.Body.String(), "event: content_block_delta") ||
						!strings.Contains(rr.Body.String(), "event: message_stop") {
						t.Fatalf("invalid Anthropic stream: %s", rr.Body.String())
					}
				case providerGemini:
					if !strings.Contains(rr.Body.String(), `"candidates"`) {
						t.Fatalf("invalid Gemini stream: %s", rr.Body.String())
					}
				}
			})
		}
	}
}

func TestCrossProtocolStreamRejectsNonStreamUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"not streamed"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer upstream.Close()

	srv, store := createRouteTestServer(
		t,
		providerAnthropic,
		upstream.URL,
		"chat-stream-mismatch",
		"claude-upstream",
		"sk-ant",
		upstream.Client(),
	)
	defer store.Close()

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"chat-stream-mismatch","stream":true,"max_completion_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	)
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway ||
		!strings.Contains(rr.Body.String(), "upstream returned a non-stream response") {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func protocolStreamRequest(inbound string, model string) (string, string) {
	switch inbound {
	case providerOpenAI:
		return "/v1/chat/completions", fmt.Sprintf(
			`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
	case providerOpenAIResponses:
		return "/v1/responses", fmt.Sprintf(
			`{"model":%q,"stream":true,"input":"hi"}`,
			model,
		)
	case providerAnthropic:
		return "/v1/messages", fmt.Sprintf(
			`{"model":%q,"stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			model,
		)
	default:
		return "/v1beta/models/" + model + ":streamGenerateContent",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	}
}

func writeProtocolTextStream(t *testing.T, w http.ResponseWriter, outbound string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	switch outbound {
	case providerOpenAI:
		fmt.Fprint(w,
			`data: {"id":"chatcmpl-upstream","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-upstream","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{"content":"hello"}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-upstream","object":"chat.completion.chunk","created":1,"model":"upstream-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
				"data: [DONE]\n\n",
		)
	case providerOpenAIResponses:
		fmt.Fprint(w,
			`event: response.created`+"\n"+
				`data: {"type":"response.created","response":{"id":"resp_upstream","object":"response","created_at":1,"status":"in_progress","model":"upstream-model","output":[]}}`+"\n\n"+
				`event: response.output_text.delta`+"\n"+
				`data: {"type":"response.output_text.delta","output_index":0,"delta":"hello"}`+"\n\n"+
				`event: response.completed`+"\n"+
				`data: {"type":"response.completed","response":{"id":"resp_upstream","object":"response","created_at":1,"status":"completed","model":"upstream-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n",
		)
	case providerAnthropic:
		fmt.Fprint(w,
			`event: message_start`+"\n"+
				`data: {"type":"message_start","message":{"id":"msg_upstream","type":"message","role":"assistant","model":"upstream-model","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`+"\n\n"+
				`event: content_block_start`+"\n"+
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
				`event: content_block_delta`+"\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`+"\n\n"+
				`event: content_block_stop`+"\n"+
				`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				`event: message_delta`+"\n"+
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`+"\n\n"+
				`event: message_stop`+"\n"+
				`data: {"type":"message_stop"}`+"\n\n",
		)
	case providerGemini:
		fmt.Fprint(w,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`+"\n\n",
		)
	}
}

func TestGeminiRoutesToOpenAIChat(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIChatRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"chatcmpl-1","created":123,"model":"gpt-upstream","choices":[{"message":{"role":"assistant","content":"hello chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAI, upstream.URL+"/v1", "gemini-public-chat", "gpt-upstream", "sk-secret", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-public-chat:generateContent", strings.NewReader(`{
		"system_instruction":{"parts":[{"text":"be brief"}]},
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"generationConfig":{"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}
	}`))
	req.Header.Set("x-goog-api-key", "bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamBody.Model != "gpt-upstream" ||
		len(upstreamBody.Messages) != 2 ||
		upstreamBody.Messages[0].Role != "system" ||
		string(upstreamBody.Messages[0].Content) != `"be brief"` ||
		upstreamBody.ReasoningEffort != nil {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}
	var got protocol.GeminiGenerateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Candidates[0].Content.Parts[0].Text != "hello chat" || got.UsageMetadata.TotalTokenCount != 5 {
		t.Fatalf("response = %+v", got)
	}
}

func TestGeminiStreamRoutesOpenAIToolCallBackToGemini(t *testing.T) {
	var upstreamBody protocol.OpenAIChatRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-upstream","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_capture","type":"function","function":{"name":"builtin_app_load","arguments":"{\"app_id\":\"cap"}},{"index":1,"id":"call_canvas","type":"function","function":{"name":"builtin_app_load","arguments":"{\"app_id\":"}}]}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ture\"}"}},{"index":1,"function":{"arguments":"\"canvas\"}"}}]}}]}`+"\n\n"+
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-upstream","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n"+
				`data: {"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1,"model":"gpt-upstream","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`+"\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAI, upstream.URL+"/v1", "gemini-public-tool", "gpt-upstream", "sk-secret", upstream.Client())
	defer store.Close()

	body := `{
		"contents":[{"role":"user","parts":[{"text":"take a photo"}]}],
		"tools":[{"functionDeclarations":[{
			"name":"builtin_app_load",
			"description":"Load an application",
			"parameters":{"type":"object","properties":{"app_id":{"type":"string"}}}
		}]}],
		"generationConfig":{"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}
	}`
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1beta/models/gemini-public-tool:streamGenerateContent?alt=sse",
		strings.NewReader(body),
	)
	req.Header.Set("x-goog-api-key", "bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var upstreamTools []protocol.OpenAITool
	if err := json.Unmarshal(upstreamBody.Tools, &upstreamTools); err != nil {
		t.Fatalf("decode upstream tools: %v", err)
	}
	if !upstreamBody.Stream ||
		upstreamBody.ReasoningEffort != nil ||
		len(upstreamTools) != 1 ||
		upstreamTools[0].Function.Name != "builtin_app_load" {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}

	var gotCalls []*protocol.GeminiFunctionCall
	gotFinish := ""
	toolFrameCount := 0
	for _, event := range decodeSSEEvents(t, rr.Body.String()) {
		var frame protocol.GeminiGenerateResponse
		if err := json.Unmarshal(event.Data, &frame); err != nil {
			t.Fatalf("decode Gemini frame: %v, body = %s", err, rr.Body.String())
		}
		for _, candidate := range frame.Candidates {
			if candidate.FinishReason != "" {
				gotFinish = candidate.FinishReason
			}
			frameCalls := make([]*protocol.GeminiFunctionCall, 0, len(candidate.Content.Parts))
			for _, part := range candidate.Content.Parts {
				if part.FunctionCall != nil {
					frameCalls = append(frameCalls, part.FunctionCall)
				}
			}
			if len(frameCalls) > 0 {
				toolFrameCount++
				gotCalls = append(gotCalls, frameCalls...)
				if candidate.FinishReason != "STOP" {
					t.Fatalf("tool frame finish reason = %q, body = %s", candidate.FinishReason, rr.Body.String())
				}
			}
		}
	}
	if toolFrameCount != 1 || len(gotCalls) != 2 {
		t.Fatalf("tool frames = %d, function calls = %+v, body = %s", toolFrameCount, gotCalls, rr.Body.String())
	}
	if gotCalls[0].ID != "call_capture" ||
		gotCalls[0].Name != "builtin_app_load" ||
		string(gotCalls[0].Args) != `{"app_id":"capture"}` {
		t.Fatalf("first function call = %+v, body = %s", gotCalls[0], rr.Body.String())
	}
	if gotCalls[1].ID != "call_canvas" ||
		gotCalls[1].Name != "builtin_app_load" ||
		string(gotCalls[1].Args) != `{"app_id":"canvas"}` {
		t.Fatalf("second function call = %+v, body = %s", gotCalls[1], rr.Body.String())
	}
	if gotFinish != "STOP" {
		t.Fatalf("finish reason = %q, body = %s", gotFinish, rr.Body.String())
	}
}

func TestGeminiRoutesToAnthropic(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.AnthropicMessagesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"hello anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":6}}`))
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerAnthropic, upstream.URL, "gemini-public-anthropic", "claude-upstream", "sk-ant", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-public-anthropic:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/messages" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamBody.Model != "claude-upstream" || len(upstreamBody.Messages) != 1 {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}
	if upstreamBody.MaxTokens == nil || *upstreamBody.MaxTokens != defaultAnthropicMaxOutputTokens {
		t.Fatalf("max tokens = %v", upstreamBody.MaxTokens)
	}
	var got protocol.GeminiGenerateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Candidates[0].Content.Parts[0].Text != "hello anthropic" || got.UsageMetadata.TotalTokenCount != 10 {
		t.Fatalf("response = %+v", got)
	}
}

func TestGeminiRoutesToOpenAIResponses(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIResponsesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"resp-upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello responses"}]}],"usage":{"input_tokens":2,"output_tokens":8,"total_tokens":10}}`))
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAIResponses, upstream.URL+"/v1", "gemini-public-responses", "resp-upstream", "sk-resp", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-public-responses:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/responses" {
		t.Fatalf("upstream path = %q", upstreamPath)
	}
	if upstreamBody.Model != "resp-upstream" {
		t.Fatalf("upstream body = %+v", upstreamBody)
	}
	var got protocol.GeminiGenerateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Candidates[0].Content.Parts[0].Text != "hello responses" || got.UsageMetadata.TotalTokenCount != 10 {
		t.Fatalf("response = %+v", got)
	}
}

func TestOpenAIChatRoutesToOpenAIResponses(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIResponsesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"resp-upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"from responses"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAIResponses, upstream.URL+"/v1", "chat-public-responses", "resp-upstream", "sk-resp", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat-public-responses","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/responses" || upstreamBody.Model != "resp-upstream" {
		t.Fatalf("upstream path = %q, body = %+v", upstreamPath, upstreamBody)
	}
	var got protocol.OpenAIChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message == nil || got.Choices[0].Message.Content == nil ||
		*got.Choices[0].Message.Content != "from responses" ||
		got.Usage == nil || got.Usage.TotalTokens != 5 {
		t.Fatalf("response = %+v", got)
	}
}

func TestOpenAIChatRoutesToAnthropic(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.AnthropicMessagesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"from anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":6}}`)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerAnthropic, upstream.URL, "chat-public-anthropic", "claude-upstream", "sk-ant", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"chat-public-anthropic","max_completion_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/messages" || upstreamBody.Model != "claude-upstream" ||
		upstreamBody.MaxTokens == nil || *upstreamBody.MaxTokens != 64 {
		t.Fatalf("upstream path = %q, body = %+v", upstreamPath, upstreamBody)
	}
	var got protocol.OpenAIChatResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message == nil || got.Choices[0].Message.Content == nil ||
		*got.Choices[0].Message.Content != "from anthropic" ||
		got.Usage == nil || got.Usage.TotalTokens != 10 {
		t.Fatalf("response = %+v", got)
	}
}

func TestOpenAIResponsesRoutesToOpenAICompatible(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIChatRequest
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_1","object":"chat.completion","created":123,"model":"deepseek-chat","choices":[{"index":0,"message":{"role":"assistant","content":"from deepseek"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAI, upstream.URL+"/v1", "responses-public-chat", "deepseek-chat", "sk-secret", upstream.Client())
	defer store.Close()

	body := `{
		"model":"responses-public-chat",
		"input":[
			{
				"type":"reasoning",
				"format":"openai-responses-v1",
				"id":"rs_previous",
				"status":"completed",
				"summary":[{"type":"summary_text","text":"previous reasoning"}],
				"encrypted_content":"encrypted"
			},
			{
				"type":"message",
				"id":"msg_previous",
				"role":"assistant",
				"status":"completed",
				"phase":"final_answer",
				"content":[{
					"type":"output_text",
					"text":"previous answer",
					"annotations":[],
					"logprobs":[]
				}]
			},
			{
				"type":"message",
				"role":"user",
				"status":"completed",
				"content":[{"type":"input_text","text":"hi"}]
			}
		],
		"background":false,
		"store":false,
		"include":["reasoning.encrypted_content"],
		"metadata":{"trace_id":"test"},
		"parallel_tool_calls":true,
		"max_tool_calls":4,
		"prompt_cache_key":"cache-key",
		"prompt_cache_options":{"mode":"implicit","ttl":"30m"},
		"prompt_cache_retention":"in_memory",
		"reasoning":{"effort":"low","summary":"auto"},
		"safety_identifier":"user-hash",
		"service_tier":"auto",
		"stream_options":{"include_obfuscation":false},
		"text":{"verbosity":"medium"},
		"truncation":"disabled",
		"user":"legacy-user"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/chat/completions" || upstreamBody.Model != "deepseek-chat" ||
		len(upstreamBody.Messages) != 2 || upstreamBody.ReasoningEffort == nil ||
		*upstreamBody.ReasoningEffort != "low" ||
		upstreamBody.Messages[0].ReasoningContent != "previous reasoning" ||
		string(upstreamBody.Messages[0].Content) != `"previous answer"` {
		t.Fatalf("upstream path = %q, body = %+v", upstreamPath, upstreamBody)
	}
	var got protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Output) != 1 || got.Output[0].Content[0].Text != "from deepseek" ||
		got.Usage == nil || got.Usage.TotalTokens != 5 {
		t.Fatalf("response = %+v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"responses-public-chat","input":"hi","store":true}`,
	))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "store=true cannot be represented") {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestOpenAIResponsesRoutesToAnthropic(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.AnthropicMessagesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-upstream","content":[{"type":"text","text":"anthropic response"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerAnthropic, upstream.URL, "responses-public-anthropic", "claude-upstream", "sk-ant", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses-public-anthropic","input":"hi","max_output_tokens":64}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/messages" || upstreamBody.Model != "claude-upstream" {
		t.Fatalf("upstream path = %q, body = %+v", upstreamPath, upstreamBody)
	}
	var got protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Output) != 1 || got.Output[0].Content[0].Text != "anthropic response" ||
		got.Usage == nil || got.Usage.TotalTokens != 12 {
		t.Fatalf("response = %+v", got)
	}
}

func TestAnthropicRoutesToOpenAIResponses(t *testing.T) {
	var upstreamPath string
	var upstreamBody protocol.OpenAIResponsesRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&upstreamBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"resp-upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"responses result"}]}],"usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}`)
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAIResponses, upstream.URL+"/v1", "anthropic-public-responses", "resp-upstream", "sk-resp", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"anthropic-public-responses","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if upstreamPath != "/v1/responses" || upstreamBody.Model != "resp-upstream" {
		t.Fatalf("upstream path = %q, body = %+v", upstreamPath, upstreamBody)
	}
	var got protocol.AnthropicMessagesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "responses result" ||
		got.Usage.InputTokens != 3 || got.Usage.OutputTokens != 4 {
		t.Fatalf("response = %+v", got)
	}
}

func TestOpenAIChatToolStreamRoutesToOpenAIResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":123,"status":"in_progress","model":"resp-upstream","output":[]}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":\"hello\"}"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"name":"lookup","arguments":"{\"q\":\"hello\"}"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"resp-upstream","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hello\"}"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`+"\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAIResponses, upstream.URL+"/v1", "chat-stream-responses", "resp-upstream", "sk-resp", upstream.Client())
	defer store.Close()

	body := serveCrossProtocolRequest(t, srv, "/v1/chat/completions", `{"model":"chat-stream-responses","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{`"id":"call_1"`, `"name":"lookup"`, `\"q\":\"hello\"`, `"finish_reason":"tool_calls"`, `"total_tokens":6`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
}

func TestOpenAIChatToolStreamRoutesToAnthropic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-upstream","usage":{"input_tokens":4}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"hello\"}"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerAnthropic, upstream.URL, "chat-stream-anthropic", "claude-upstream", "sk-ant", upstream.Client())
	defer store.Close()

	body := serveCrossProtocolRequest(t, srv, "/v1/chat/completions", `{"model":"chat-stream-anthropic","stream":true,"max_completion_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{`"id":"call_1"`, `"name":"lookup"`, `\"q\":\"hello\"`, `"finish_reason":"tool_calls"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
}

func TestOpenAIResponsesToolStreamRoutesToAnthropic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude-upstream","usage":{"input_tokens":4}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"hello\"}"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerAnthropic, upstream.URL, "responses-stream-anthropic", "claude-upstream", "sk-ant", upstream.Client())
	defer store.Close()

	body := serveCrossProtocolRequest(t, srv, "/v1/responses", `{"model":"responses-stream-anthropic","input":"hi","max_output_tokens":64,"stream":true}`)
	for _, want := range []string{"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed", `"call_id":"call_1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
}

func TestAnthropicToolStreamRoutesToOpenAIResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","created_at":123,"status":"in_progress","model":"resp-upstream","output":[]}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":\"hello\"}"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"name":"lookup","arguments":"{\"q\":\"hello\"}"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":123,"status":"completed","model":"resp-upstream","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hello\"}"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}`+"\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAIResponses, upstream.URL+"/v1", "anthropic-stream-responses", "resp-upstream", "sk-resp", upstream.Client())
	defer store.Close()

	body := serveCrossProtocolRequest(t, srv, "/v1/messages", `{"model":"anthropic-stream-responses","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{"message_start", `"type":"tool_use"`, `"id":"call_1"`, `"name":"lookup"`, `"partial_json":"{\"q\":\"hello\"}"`, `"stop_reason":"tool_use"`, "message_stop"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q: %s", want, body)
		}
	}
}

func TestOpenAIResponsesStreamRoutesToGemini(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hello "}]},"finishReason":""}]}`+"\n\n")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"stream"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}`+"\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerGemini, upstream.URL, "responses-stream-gemini", "gemini-upstream", "AIza-secret", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses-stream-gemini","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "hello ") || !strings.Contains(body, "stream") || !strings.Contains(body, "response.completed") {
		t.Fatalf("stream body = %s", body)
	}
}

func serveCrossProtocolRequest(t *testing.T, srv *Server, path string, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return rr.Body.String()
}

func TestAnthropicStreamRoutesToOpenAIChat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"hello "}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"stream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	srv, store := createRouteTestServer(t, providerOpenAI, upstream.URL+"/v1", "anthropic-stream-chat", "gpt-upstream", "sk-secret", upstream.Client())
	defer store.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"anthropic-stream-chat","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer bh_valid")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "message_start") || !strings.Contains(body, "content_block_delta") || !strings.Contains(body, "hello ") || !strings.Contains(body, "stream") || !strings.Contains(body, "message_stop") {
		t.Fatalf("stream body = %s", body)
	}
}

func TestReadAnthropicStreamUsageMergesMessageStart(t *testing.T) {
	stream := strings.NewReader(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":7}}}` + "\n\n" +
			`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)
	var events []protocol.CanonicalStreamEvent
	usage, err := readAnthropicStreamAsCanonical(stream, func(event protocol.CanonicalStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}

	if usage.PromptTokens != 32 || usage.CompletionTokens != 15 || usage.TotalTokens != 47 || usage.CachedTokens != 7 {
		t.Fatalf("usage = %+v", usage)
	}
	if len(events) != 5 ||
		events[0].Type != protocol.CanonicalStreamResponseStart ||
		events[1].Type != protocol.CanonicalStreamMessageStart ||
		events[2].Type != protocol.CanonicalStreamTextDelta ||
		events[2].Delta != "hello" ||
		events[3].Type != protocol.CanonicalStreamUsage ||
		events[4].Type != protocol.CanonicalStreamResponseDone ||
		events[4].FinishReason != "stop" {
		t.Fatalf("events = %+v", events)
	}
}
