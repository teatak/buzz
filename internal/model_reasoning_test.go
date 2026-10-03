package buzz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teatak/buzz/internal/protocol"
)

func TestMimoReasoningAtProviderBoundary(t *testing.T) {
	for _, wireProtocol := range []string{providerOpenAI, providerOpenAIResponses, providerAnthropic} {
		for _, model := range []string{"mimo-v2.5", "mimo-v2.5-pro", "mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.6-pro-ultraspeed", "mimo-v3-flash", "xiaomi/mimo-v2.6-flash", "xiaomimimo/mimo-v2.6-pro"} {
			for _, effort := range []string{"none", "low", "medium", "high", "xhigh", "max"} {
				t.Run(wireProtocol+"/"+model+"/"+effort, func(t *testing.T) {
					field, nested := reasoningField(wireProtocol)
					value := fmt.Sprintf("%q", effort)
					if nested {
						value = fmt.Sprintf(`{"effort":%q,"extra":9007199254740993}`, effort)
					}
					body := fmt.Sprintf(`{"model":%q,%q:%s,"extra":9007199254740993}`, model, field, value)
					request := ProviderRequest{Body: []byte(body), RequestedModel: "public-alias"}
					got := captureReasoningProviderRequest(t, wireProtocol, model, request)
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(got.Body, &payload); err != nil {
						t.Fatal(err)
					}
					if string(payload["extra"]) != "9007199254740993" {
						t.Fatal("unknown fields lost precision")
					}
					container := payload
					if nested {
						container = nil
						if err := json.Unmarshal(payload[field], &container); err != nil {
							t.Fatal(err)
						}
						field = "effort"
						if string(container["extra"]) != "9007199254740993" {
							t.Fatal("other reasoning settings changed")
						}
					}
					want := effort
					if effort == "xhigh" || effort == "max" {
						want = "high"
					}
					if string(container[field]) != fmt.Sprintf("%q", want) {
						t.Fatalf("wire effort = %s, want %q", container[field], want)
					}
					if string(request.Body) != body || got.Model != model || got.RequestedModel != "public-alias" {
						t.Fatal("mapping mutated the source request or model identity")
					}
				})
			}
		}
	}
}

func TestMimoReasoningPreservesUnrelatedRequests(t *testing.T) {
	for _, model := range []string{"gpt-5.5", "mimo-custom", "mimo-vnext", "mimo-v2.5-tts", "mimo-v2.5-asr", "custom/mimo-v2.6-flash"} {
		body := `{"reasoning_effort":"max"}`
		got := captureReasoningProviderRequest(t, providerOpenAI, model, ProviderRequest{Body: []byte(body), RequestedModel: "mimo-v2.6-flash"})
		if string(got.Body) != body {
			t.Fatalf("%s: public name must not determine reasoning: %s", model, got.Body)
		}
	}
	for _, wireProtocol := range []string{providerOpenAI, providerOpenAIResponses, providerAnthropic, providerGemini} {
		for _, body := range []string{`{ "messages": [] }`, `{"reasoning_effort":"auto","reasoning":null,"output_config":null}`, `{"reasoning_effort":123,"reasoning":{"summary":"auto"},"output_config":{"format":{"type":"json_schema"}}}`} {
			got := captureReasoningProviderRequest(t, wireProtocol, "mimo-v2.6-flash", ProviderRequest{Body: []byte(body)})
			if string(got.Body) != body {
				t.Fatalf("%s: absent/non-product effort changed: %s", wireProtocol, got.Body)
			}
		}
	}
}

func TestMimoReasoningAfterProtocolConversion(t *testing.T) {
	canonical := protocol.CanonicalRequest{
		Model: "public-alias", Reasoning: &protocol.CanonicalReasoning{Effort: "max"},
		Messages: []protocol.CanonicalMessage{{Role: "user", Parts: []protocol.CanonicalPart{{Type: "text", Text: "hello"}}}},
	}
	for _, wireProtocol := range []string{providerOpenAIResponses, providerAnthropic} {
		target := RouteTarget{ProviderName: "upstream", ProviderType: wireProtocol, UpstreamModel: "mimo-v2.6-pro"}
		request, err := (&Server{}).prepareCanonicalProviderRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil), nil, canonical, AuthToken{}, "public-alias", providerOpenAI, target)
		if err != nil {
			t.Fatal(err)
		}
		got := captureReasoningProviderRequest(t, wireProtocol, target.UpstreamModel, request)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(got.Body, &body); err != nil {
			t.Fatal(err)
		}
		field, _ := reasoningField(wireProtocol)
		var reasoning struct{ Effort string }
		if err := json.Unmarshal(body[field], &reasoning); err != nil || reasoning.Effort != "high" {
			t.Fatalf("%s converted effort = %s, error %v", wireProtocol, body[field], err)
		}
		if canonical.Reasoning.Effort != "max" {
			t.Fatal("source canonical request mutated")
		}
	}
}

func reasoningField(wireProtocol string) (string, bool) {
	switch wireProtocol {
	case providerOpenAIResponses:
		return "reasoning", true
	case providerAnthropic:
		return "output_config", true
	default:
		return "reasoning_effort", false
	}
}

func captureReasoningProviderRequest(t *testing.T, wireProtocol, model string, request ProviderRequest) ProviderRequest {
	t.Helper()
	var got ProviderRequest
	srv := &Server{
		providers: map[string]Provider{providerRuntimeKey("upstream", wireProtocol): testProviderFunc(func(_ context.Context, req ProviderRequest, _ APIKey) (*http.Response, error) {
			got = req
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		})},
		keyState: &KeyState{keys: []APIKey{{Name: "test", Key: "fixture", ProviderName: "upstream"}}},
	}
	result := srv.doProviderAttemptLoop(context.Background(), AuthToken{}, request.RequestedModel,
		RouteTarget{ProviderName: "upstream", ProviderType: wireProtocol, UpstreamModel: model}, request)
	if !result.OK || result.Attempts != 1 {
		t.Fatalf("request not forwarded: %+v", result)
	}
	result.Response.Body.Close()
	return got
}
