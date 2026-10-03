package buzz

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/teatak/buzz/internal/protocol"
)

func (s *Server) handleAnthropicPassthrough(w http.ResponseWriter, r *http.Request, body []byte, user AuthToken) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	var req protocol.AnthropicMessagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if req.Model == "" {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if isAutoModel(req.Model) {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "auto model routing has been removed")
		return
	}

	targets, err := s.resolveRouteTargets(req.Model)
	if err != nil {
		if errors.Is(err, errModelRouteNotFound) {
			writeAnthropicError(w, http.StatusNotFound, "not_found_error", err.Error())
			return
		}
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	targets = s.selectRouteTargets(req.Model, targets, anthropicProtocolPreference())
	if len(targets) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "selected upstream does not support Anthropic Messages")
		return
	}
	if protocol.ShouldPassthrough(providerAnthropic, targets[0].ProviderType) && !routeTargetsUseMixedProtocols(targets) {
		s.proxyRaw(w, r, body, user, req.Model, targets, providerAnthropic)
		return
	}
	if err := decodeCrossProtocolJSON(body, &req); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerAnthropic) {
			return
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if err := validateAnthropicCrossProtocolContent(body); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerAnthropic) {
			return
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	canonicalReq, err := protocol.AnthropicMessagesToCanonicalRequest(req)
	if err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerAnthropic) {
			return
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.proxyMixedCanonicalRoutes(w, r, body, canonicalReq, user, req.Model, targets, providerAnthropic, false)
}

type anthropicStrictMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func validateAnthropicCrossProtocolContent(body []byte) error {
	var request struct {
		Model         string                          `json:"model"`
		System        json.RawMessage                 `json:"system,omitempty"`
		Messages      []anthropicStrictMessage        `json:"messages"`
		MaxTokens     *int                            `json:"max_tokens,omitempty"`
		Temperature   *float64                        `json:"temperature,omitempty"`
		TopP          *float64                        `json:"top_p,omitempty"`
		StopSequences []string                        `json:"stop_sequences,omitempty"`
		Tools         []protocol.AnthropicTool        `json:"tools,omitempty"`
		ToolChoice    *protocol.AnthropicToolChoice   `json:"tool_choice,omitempty"`
		Thinking      *protocol.AnthropicThinking     `json:"thinking,omitempty"`
		OutputConfig  *protocol.AnthropicOutputConfig `json:"output_config,omitempty"`
		Stream        bool                            `json:"stream,omitempty"`
	}
	if err := decodeCrossProtocolJSON(body, &request); err != nil {
		return err
	}
	if len(request.System) > 0 {
		if err := validateAnthropicContentValue(request.System); err != nil {
			return err
		}
	}
	for _, message := range request.Messages {
		if err := validateAnthropicContentValue(message.Content); err != nil {
			return err
		}
	}
	return nil
}

func validateAnthropicContentValue(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return nil
	}
	var blocks []json.RawMessage
	if err := decodeCrossProtocolJSON(raw, &blocks); err != nil {
		return err
	}
	for _, block := range blocks {
		var content protocol.AnthropicContent
		if err := decodeCrossProtocolJSON(block, &content); err != nil {
			return err
		}
	}
	return nil
}

func anthropicProtocolPreference() []string {
	return []string{providerAnthropic, providerOpenAI, providerOpenAIResponses, providerGemini}
}
