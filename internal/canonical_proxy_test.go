package buzz

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teatak/buzz/internal/protocol"
)

func TestStreamCanonicalResultRemembersGeminiToolSignature(t *testing.T) {
	srv := &Server{}
	user := AuthToken{ID: 7, UserID: 11}
	upstream := `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"hello"}},"thoughtSignature":"thought-sig"}]},"finishReason":"STOP"}]}` + "\n\n"
	resp := &http.Response{
		Body:   io.NopCloser(strings.NewReader(upstream)),
		Header: make(http.Header),
	}
	recorder := httptest.NewRecorder()
	_, err := srv.streamCanonicalResult(
		recorder,
		resp,
		context.Background(),
		user,
		providerOpenAI,
		providerGemini,
		"public-model",
		"request-id",
		123,
		true,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	req := &protocol.CanonicalRequest{
		Messages: []protocol.CanonicalMessage{{
			Role: "assistant",
			Parts: []protocol.CanonicalPart{{
				Type:       "tool_call",
				ToolCallID: "different-call-id",
				Name:       "lookup",
				Arguments:  json.RawMessage(`{"q":"hello"}`),
			}},
		}},
	}
	srv.applyToolSignatures(context.Background(), user, "public-model", req)
	if got := req.Messages[0].Parts[0].Signature; got != "thought-sig" {
		t.Fatalf("thought signature = %q", got)
	}
}

func TestStreamCanonicalResultDoesNotDuplicateGeminiErrors(t *testing.T) {
	srv := &Server{}
	upstream := `data: {"error":{"code":429,"message":"limited","status":"RESOURCE_EXHAUSTED"}}` + "\n\n"
	resp := &http.Response{
		Body:   io.NopCloser(strings.NewReader(upstream)),
		Header: make(http.Header),
	}
	recorder := httptest.NewRecorder()
	_, err := srv.streamCanonicalResult(
		recorder,
		resp,
		context.Background(),
		AuthToken{},
		providerGemini,
		providerGemini,
		"public-model",
		"request-id",
		123,
		true,
		false,
	)
	if err == nil {
		t.Fatal("expected stream error")
	}
	if got := strings.Count(recorder.Body.String(), `"error"`); got != 1 {
		t.Fatalf("error event count = %d, body = %s", got, recorder.Body.String())
	}
}

func TestPrepareGeminiRequestMapsReasoningPerTargetModel(t *testing.T) {
	srv := &Server{}
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	canonical := protocol.CanonicalRequest{
		Model:     "public",
		Reasoning: &protocol.CanonicalReasoning{Effort: "minimal"},
		Messages: []protocol.CanonicalMessage{{
			Role:  "user",
			Parts: []protocol.CanonicalPart{{Type: "text", Text: "hello"}},
		}},
	}
	tests := []struct {
		model string
		want  string
	}{
		{model: "gemini-3-flash", want: "MINIMAL"},
		{model: "gemini-3-pro", want: "LOW"},
	}
	for _, tt := range tests {
		prepared, err := srv.prepareCanonicalProviderRequest(
			request,
			nil,
			canonical,
			AuthToken{},
			"public",
			providerOpenAI,
			RouteTarget{ProviderName: "gemini", ProviderType: providerGemini, UpstreamModel: tt.model},
		)
		if err != nil {
			t.Fatal(err)
		}
		var got protocol.GeminiGenerateRequest
		if err := json.Unmarshal(prepared.Body, &got); err != nil {
			t.Fatal(err)
		}
		if got.GenerationConfig == nil ||
			got.GenerationConfig.ThinkingConfig == nil ||
			got.GenerationConfig.ThinkingConfig.ThinkingLevel != tt.want ||
			got.GenerationConfig.ThinkingConfig.IncludeThoughts == nil ||
			!*got.GenerationConfig.ThinkingConfig.IncludeThoughts {
			t.Fatalf("%s thinking config = %+v", tt.model, got.GenerationConfig)
		}
	}
}

func TestMapReasoningBudgetToOpenAIEffort(t *testing.T) {
	tests := []struct {
		budget int
		want   string
	}{
		{budget: -1, want: ""},
		{budget: 0, want: ""},
		{budget: 128, want: "minimal"},
		{budget: 1024, want: "low"},
		{budget: 8192, want: "medium"},
		{budget: 24576, want: "high"},
	}
	for _, tt := range tests {
		budget := tt.budget
		reasoning := &protocol.CanonicalReasoning{BudgetTokens: &budget}
		mapReasoningBudgetToOpenAIEffort(reasoning)
		if reasoning.BudgetTokens != nil || reasoning.Effort != tt.want {
			t.Fatalf("budget %d mapped to %+v, want effort %q", tt.budget, reasoning, tt.want)
		}
	}
}

func TestDirectProtocolTargetsPreserveRouteOrder(t *testing.T) {
	targets := []RouteTarget{
		{ID: 1, ProviderType: providerAnthropic},
		{ID: 2, ProviderType: providerOpenAI},
		{ID: 3, ProviderType: providerAnthropic},
	}
	direct := directProtocolTargets(targets, providerAnthropic)
	if len(direct) != 2 || direct[0].ID != 1 || direct[1].ID != 3 {
		t.Fatalf("direct targets = %+v", direct)
	}
}

func TestStreamResultStatus(t *testing.T) {
	if got := streamResultStatus(nil); got != 200 {
		t.Fatalf("success status = %d", got)
	}
	if got := streamResultStatus(assertionError("truncated")); got != 502 {
		t.Fatalf("failure status = %d", got)
	}
}

func TestCopyProviderStreamResponseBodyCapturesNativeUsage(t *testing.T) {
	tests := []struct {
		name         string
		providerType string
		stream       string
		want         TokenUsage
	}{
		{
			name:         "Anthropic",
			providerType: providerAnthropic,
			stream: `data: {"type":"message_start","message":{"usage":{"input_tokens":25,"cache_creation_input_tokens":3,"cache_read_input_tokens":7}}}` + "\n\n" +
				`data: {"type":"message_delta","usage":{"output_tokens":15,"output_tokens_details":{"thinking_tokens":4}}}` + "\n\n",
			want: TokenUsage{
				PromptTokens:     35,
				CompletionTokens: 15,
				TotalTokens:      50,
				CachedTokens:     7,
				ReasoningTokens:  4,
			},
		},
		{
			name:         "Gemini",
			providerType: providerGemini,
			stream:       `data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":8,"totalTokenCount":28,"cachedContentTokenCount":6,"thoughtsTokenCount":3}}` + "\n\n",
			want: TokenUsage{
				PromptTokens:     20,
				CompletionTokens: 8,
				TotalTokens:      28,
				CachedTokens:     6,
				ReasoningTokens:  3,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			got := copyProviderStreamResponseBody(recorder, strings.NewReader(tt.stream), tt.providerType)
			if recorder.Body.String() != tt.stream {
				t.Fatalf("copied stream = %q", recorder.Body.String())
			}
			got.RawUsage = ""
			if got != tt.want {
				t.Fatalf("usage = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStreamDataPayload(t *testing.T) {
	if got := streamDataPayload([]byte("data: {\"ok\":true}\n")); !bytes.Equal(got, []byte(`{"ok":true}`)) {
		t.Fatalf("payload = %q", got)
	}
	if got := streamDataPayload([]byte("data: [DONE]\n")); got != nil {
		t.Fatalf("done payload = %q", got)
	}
}

type assertionError string

func (e assertionError) Error() string {
	return string(e)
}
