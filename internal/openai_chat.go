package buzz

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/teatak/buzz/internal/protocol"
)

func (s *Server) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request, body []byte, user AuthToken) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	var req protocol.OpenAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if req.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if isAutoModel(req.Model) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "auto model routing has been removed")
		return
	}
	targets, err := s.resolveRouteTargets(req.Model)
	if err != nil {
		if errors.Is(err, errModelRouteNotFound) {
			writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
			return
		}
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	targets = s.selectRouteTargets(req.Model, targets, openAIChatProtocolPreference())
	if len(targets) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "unsupported_endpoint", "selected upstream does not support OpenAI Chat Completions")
		return
	}
	target := targets[0]
	if protocol.ShouldPassthrough(providerOpenAI, target.ProviderType) && !routeTargetsUseMixedProtocols(targets) {
		s.proxyRaw(w, r, body, user, req.Model, targets, providerOpenAI)
		return
	}
	if err := decodeCrossProtocolJSON(body, &req); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerOpenAI) {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if err := validateOpenAIChatParameterSupport(req); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerOpenAI) {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	canonicalReq, err := protocol.OpenAIChatToCanonical(req)
	if err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerOpenAI) {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	s.proxyMixedCanonicalRoutes(w, r, body, canonicalReq, user, req.Model, targets, providerOpenAI, includeUsage)
}

func validateOpenAIChatParameterSupport(req protocol.OpenAIChatRequest) error {
	if req.N != nil {
		if *req.N < 1 {
			return errors.New("n must be at least 1")
		}
		if *req.N > 1 {
			return errors.New("n greater than 1 is not supported")
		}
	}
	if req.Logprobs != nil && *req.Logprobs {
		return errors.New("logprobs is not supported")
	}
	if req.TopLogprobs != nil {
		return errors.New("top_logprobs is not supported")
	}
	if req.PresencePenalty != nil && *req.PresencePenalty != 0 {
		return errors.New("presence_penalty is not supported")
	}
	if req.FrequencyPenalty != nil && *req.FrequencyPenalty != 0 {
		return errors.New("frequency_penalty is not supported")
	}
	if rawJSONHasValue(req.LogitBias) {
		return errors.New("logit_bias is not supported")
	}
	if req.Seed != nil {
		return errors.New("seed is not supported")
	}
	if strings.TrimSpace(req.User) != "" {
		return errors.New("user is not supported")
	}
	if rawJSONHasValue(req.Metadata) {
		return errors.New("metadata is not supported")
	}
	switch stop := req.Stop.(type) {
	case nil, string:
	case []any:
		for _, item := range stop {
			if _, ok := item.(string); !ok {
				return errors.New("stop must be a string or an array of strings")
			}
		}
	default:
		return errors.New("stop must be a string or an array of strings")
	}
	return nil
}

func rawJSONHasValue(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != "{}"
}

func openAIChatProtocolPreference() []string {
	return []string{providerOpenAI, providerOpenAIResponses, providerAnthropic, providerGemini}
}

func geminiThinkingLevelForOpenAIReasoningEffort(effort *string, model string) (*string, error) {
	if effort == nil || strings.TrimSpace(*effort) == "" {
		return nil, nil
	}
	if !strings.Contains(strings.ToLower(model), "gemini-3") {
		return nil, errors.New("reasoning_effort mapping is only supported for Gemini 3 models")
	}
	value := strings.ToLower(strings.TrimSpace(*effort))
	switch value {
	case "low", "high":
		level := strings.ToUpper(value)
		return &level, nil
	case "medium":
		level := "MEDIUM"
		return &level, nil
	case "minimal":
		if strings.Contains(strings.ToLower(model), "flash") {
			level := "MINIMAL"
			return &level, nil
		}
		level := "LOW"
		return &level, nil
	case "none":
		return nil, errors.New("reasoning_effort none is not supported for Gemini 3 models")
	case "xhigh":
		return nil, errors.New("reasoning_effort xhigh is not supported for Gemini models")
	default:
		return nil, fmt.Errorf("unsupported reasoning_effort %q", value)
	}
}

func (s *Server) proxyRaw(w http.ResponseWriter, r *http.Request, body []byte, user AuthToken, model string, targets []RouteTarget, inbound string) {
	if !s.enforceUserQuota(w, inbound, user) {
		return
	}
	reqDiag := openAIDiagnosticRequest{}
	if isOpenAIProviderType(inbound) {
		reqDiag = openAIDiagnosticRequestFromBody(body)
	}
	result := s.doProviderTargetLoop(r.Context(), user, model, targets, func(target RouteTarget) ProviderRequest {
		return ProviderRequest{
			ProviderName:    target.ProviderName,
			InboundProtocol: inbound,
			Method:          r.Method,
			Path:            r.URL.Path,
			RawQuery:        r.URL.RawQuery,
			Headers:         r.Header,
			Body:            rewriteOpenAIModel(body, model, target.UpstreamModel),
			RequestedModel:  model,
			Model:           target.UpstreamModel,
		}
	})
	if !result.OK {
		s.recordProviderResultUsage(user, model, result, providerResultStatus(result.Response))
		if isOpenAIProviderType(inbound) {
			logOpenAIDiagnostic(result, model, reqDiag, providerResultStatus(result.Response), "", nil)
		}
		writeInboundRetryError(w, inbound, result.Response, result.Attempts, s.cfg.Retry.MaxAttempts, result.Chain)
		return
	}
	resp := result.Response
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && isOpenAIProviderType(inbound) {
		logOpenAIDiagnostic(result, model, reqDiag, resp.StatusCode, "", nil)
	}
	usage := TokenUsage{}
	if !reqDiag.Stream && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		raw := drain(resp.Body, 8*1024*1024)
		usage = tokenUsageFromProviderBody(raw, result.Target.ProviderType)
		if isOpenAIProviderType(inbound) {
			logOpenAIDiagnosticResponse(result, model, reqDiag, resp.StatusCode, raw)
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Proxy-Debug", strings.Join(result.Chain, " -> "))
	w.Header().Set("X-Proxy-Key", result.Key.Name)
	w.WriteHeader(resp.StatusCode)
	if isEventStream(resp) {
		usage = copyProviderStreamResponseBody(w, resp.Body, result.Target.ProviderType)
	} else {
		_ = copyResponseBody(w, resp.Body)
	}
	s.recordProviderResultUsage(user, model, result, resp.StatusCode, usage)
}

func copyProviderStreamResponseBody(w http.ResponseWriter, r io.Reader, providerType string) TokenUsage {
	reader := bufio.NewReader(r)
	flusher, _ := w.(http.Flusher)
	usage := TokenUsage{}
	var anthropicUsage protocol.AnthropicUsage
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return usage
			}
			if flusher != nil {
				flusher.Flush()
			}
			switch providerType {
			case providerAnthropic:
				if next, ok := anthropicUsageFromStreamLine(line); ok {
					anthropicUsage = mergeRawAnthropicUsage(anthropicUsage, next)
					usage = tokenUsageFromAnthropicUsage(anthropicUsage)
				}
			case providerGemini:
				if chunkUsage := tokenUsageFromGeminiStreamLine(line); !chunkUsage.IsZero() {
					usage = chunkUsage
				}
			default:
				if chunkUsage := tokenUsageFromOpenAIStreamLine(line); !chunkUsage.IsZero() {
					usage = chunkUsage
				}
			}
		}
		if err != nil {
			return usage
		}
	}
}

func streamDataPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	return payload
}

func tokenUsageFromOpenAIStreamLine(line []byte) TokenUsage {
	payload := streamDataPayload(line)
	if len(payload) == 0 {
		return TokenUsage{}
	}
	return tokenUsageFromOpenAIResponseBody(payload)
}

func tokenUsageFromGeminiStreamLine(line []byte) TokenUsage {
	payload := streamDataPayload(line)
	if len(payload) == 0 {
		return TokenUsage{}
	}
	var response protocol.GeminiGenerateResponse
	if json.Unmarshal(payload, &response) != nil {
		return TokenUsage{}
	}
	return tokenUsageFromGeminiResponseBody(payload, response)
}

func anthropicUsageFromStreamLine(line []byte) (protocol.AnthropicUsage, bool) {
	payload := streamDataPayload(line)
	if len(payload) == 0 {
		return protocol.AnthropicUsage{}, false
	}
	var event struct {
		Message struct {
			Usage protocol.AnthropicUsage `json:"usage"`
		} `json:"message"`
		Usage protocol.AnthropicUsage `json:"usage"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return protocol.AnthropicUsage{}, false
	}
	usage := event.Usage
	if usage == (protocol.AnthropicUsage{}) {
		usage = event.Message.Usage
	}
	return usage, usage != (protocol.AnthropicUsage{})
}

func mergeRawAnthropicUsage(current protocol.AnthropicUsage, next protocol.AnthropicUsage) protocol.AnthropicUsage {
	if next.InputTokens != 0 {
		current.InputTokens = next.InputTokens
	}
	if next.OutputTokens != 0 {
		current.OutputTokens = next.OutputTokens
	}
	if next.CacheCreationInputTokens != 0 {
		current.CacheCreationInputTokens = next.CacheCreationInputTokens
	}
	if next.CacheReadInputTokens != 0 {
		current.CacheReadInputTokens = next.CacheReadInputTokens
	}
	if next.OutputTokensDetails != nil {
		current.OutputTokensDetails = next.OutputTokensDetails
	}
	return current
}

func tokenUsageFromAnthropicUsage(usage protocol.AnthropicUsage) TokenUsage {
	raw, _ := json.Marshal(usage)
	promptTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	reasoningTokens := 0
	if usage.OutputTokensDetails != nil {
		reasoningTokens = usage.OutputTokensDetails.ThinkingTokens
	}
	return TokenUsage{
		PromptTokens:     int64(promptTokens),
		CompletionTokens: int64(usage.OutputTokens),
		TotalTokens:      int64(promptTokens + usage.OutputTokens),
		CachedTokens:     int64(usage.CacheReadInputTokens),
		ReasoningTokens:  int64(reasoningTokens),
		RawUsage:         compactRawJSON(raw),
	}
}

func isOpenAIProviderType(providerType string) bool {
	t := strings.ToLower(providerType)
	return t == "openai" || t == "openai-responses"
}

func rewriteOpenAIModel(body []byte, publicModel, upstreamModel string) []byte {
	if upstreamModel == "" || upstreamModel == publicModel {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = upstreamModel
	nextBody, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return nextBody
}

type openAIDiagnosticRequest struct {
	MaxTokens           *int `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
	Stream              bool `json:"stream,omitempty"`
}

func openAIDiagnosticRequestFromBody(body []byte) openAIDiagnosticRequest {
	var req openAIDiagnosticRequest
	_ = json.Unmarshal(body, &req)
	return req
}

func logOpenAIDiagnosticResponse(result ProviderAttemptResult, publicModel string, req openAIDiagnosticRequest, status int, raw []byte) {
	var resp struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Choices) == 0 || resp.Choices[0].FinishReason == nil {
		return
	}
	if *resp.Choices[0].FinishReason != "length" {
		return
	}
	var completionTokens *int
	if resp.Usage != nil {
		completionTokens = &resp.Usage.CompletionTokens
	}
	logOpenAIDiagnostic(result, publicModel, req, status, "length", completionTokens)
}

func logOpenAIDiagnostic(result ProviderAttemptResult, publicModel string, req openAIDiagnosticRequest, status int, finishReason string, completionTokens *int) {
	if status < 400 && finishReason != "length" {
		return
	}
	log.Printf(
		"openai diagnostic request_id=%s model=%s provider=%s upstream_model=%s status=%d finish_reason=%s max_tokens=%s max_completion_tokens=%s completion_tokens=%s attempts=%d chain=%s",
		result.RequestID,
		publicModel,
		result.Target.ProviderName,
		result.Target.UpstreamModel,
		status,
		finishReason,
		intPtrString(req.MaxTokens),
		intPtrString(req.MaxCompletionTokens),
		intPtrString(completionTokens),
		result.Attempts,
		strings.Join(result.Chain, " -> "),
	)
}

func intPtrString(value *int) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%d", *value)
}

func writeOpenAIRetryError(w http.ResponseWriter, resp *http.Response, attempts, maxAttempts int, chain []string) {
	status := http.StatusTooManyRequests
	raw := []byte{}
	if resp != nil {
		status = resp.StatusCode
		raw = drain(resp.Body, 64*1024)
	}
	w.Header().Set("X-Proxy-Debug", strings.Join(chain, " -> "))
	message := openAIUpstreamErrorMessage(status, raw)
	writeOpenAIError(w, status, "upstream_error", fmt.Sprintf("upstream failed after %d/%d attempts: %s", attempts, maxAttempts, message))
}

func writeOpenAIUpstreamError(w http.ResponseWriter, status int, raw []byte) {
	writeOpenAIError(w, status, "upstream_error", openAIUpstreamErrorMessage(status, raw))
}

func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	errorType, errorCode := openAIErrorTypeAndCode(status, code)
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
			"code":    errorCode,
		},
	})
}

func openAIErrorTypeAndCode(status int, code string) (string, string) {
	if code != "" && code != "upstream_error" {
		switch code {
		case "method_not_allowed", "model_not_found":
			return "invalid_request_error", code
		default:
			return code, code
		}
	}

	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "invalid_request_error", "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error", "invalid_api_key"
	case http.StatusForbidden:
		return "permission_error", "permission_denied"
	case http.StatusNotFound:
		return "invalid_request_error", "not_found"
	case http.StatusRequestTimeout:
		return "timeout_error", "timeout"
	case http.StatusConflict:
		return "conflict_error", "conflict"
	case http.StatusTooManyRequests:
		return "rate_limit_error", "rate_limit_exceeded"
	default:
		if status >= 500 {
			return "server_error", "upstream_error"
		}
		return "upstream_error", "upstream_error"
	}
}

func openAIUpstreamErrorMessage(status int, raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		if statusText := http.StatusText(status); statusText != "" {
			return statusText
		}
		return "upstream error"
	}

	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil && len(payload.Error) > 0 {
		var message string
		if json.Unmarshal(payload.Error, &message) == nil && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}

		var detail struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		}
		if json.Unmarshal(payload.Error, &detail) == nil {
			message = strings.TrimSpace(detail.Message)
			statusName := strings.TrimSpace(detail.Status)
			switch {
			case message != "" && statusName != "" && !strings.Contains(message, statusName):
				return message + " (" + statusName + ")"
			case message != "":
				return message
			case statusName != "":
				return statusName
			}
		}
	}

	return text
}

func writeOpenAIStreamChunk(w io.Writer, flusher http.Flusher, chunk protocol.OpenAIChatResponse) {
	data, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}
