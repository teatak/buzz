package buzz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteInboundErrorUsesAnthropicShape(t *testing.T) {
	rr := httptest.NewRecorder()

	writeInboundUpstreamError(
		rr,
		providerAnthropic,
		http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limited"}}`),
	)

	var got struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusTooManyRequests || got.Type != "error" || got.Error.Type != "rate_limit_error" {
		t.Fatalf("status = %d, response = %+v", rr.Code, got)
	}
	if got.Error.Message != "rate limited" {
		t.Fatalf("message = %q", got.Error.Message)
	}
}

func TestWriteInboundErrorUsesGeminiShape(t *testing.T) {
	rr := httptest.NewRecorder()

	writeInboundUpstreamError(
		rr,
		providerGemini,
		http.StatusTooManyRequests,
		[]byte(`{"error":{"message":"rate limited"}}`),
	)

	var got struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusTooManyRequests || got.Error.Code != http.StatusTooManyRequests || got.Error.Status != "RESOURCE_EXHAUSTED" {
		t.Fatalf("status = %d, response = %+v", rr.Code, got)
	}
	if got.Error.Message != "rate limited" {
		t.Fatalf("message = %q", got.Error.Message)
	}
}

func TestDecodeCrossProtocolJSONRejectsUnknownFields(t *testing.T) {
	var target struct {
		Model string `json:"model"`
	}

	err := decodeCrossProtocolJSON([]byte(`{"model":"m","future_field":true}`), &target)

	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateAnthropicCrossProtocolContentRejectsUnknownBlockFields(t *testing.T) {
	err := validateAnthropicCrossProtocolContent([]byte(
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi","future_field":true}]}]}`,
	))

	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}
