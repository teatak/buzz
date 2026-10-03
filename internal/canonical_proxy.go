package buzz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/teatak/buzz/internal/protocol"
)

const defaultAnthropicMaxOutputTokens = 4096

func routeTargetsUseMixedProtocols(targets []RouteTarget) bool {
	if len(targets) < 2 {
		return false
	}
	protocolType := targets[0].ProviderType
	for _, target := range targets[1:] {
		if target.ProviderType != protocolType {
			return true
		}
	}
	return false
}

func directProtocolTargets(targets []RouteTarget, inbound string) []RouteTarget {
	out := make([]RouteTarget, 0, len(targets))
	for _, target := range targets {
		if target.ProviderType == inbound {
			out = append(out, target)
		}
	}
	return out
}

func (s *Server) proxyRawIfAvailable(
	w http.ResponseWriter,
	r *http.Request,
	body []byte,
	user AuthToken,
	model string,
	targets []RouteTarget,
	inbound string,
) bool {
	directTargets := directProtocolTargets(targets, inbound)
	if len(directTargets) == 0 {
		return false
	}
	s.proxyRaw(w, r, body, user, model, directTargets, inbound)
	return true
}

func (s *Server) proxyMixedCanonicalRoutes(
	w http.ResponseWriter,
	r *http.Request,
	rawBody []byte,
	canonicalReq protocol.CanonicalRequest,
	user AuthToken,
	model string,
	targets []RouteTarget,
	inbound string,
	includeUsage bool,
) {
	if !s.enforceUserQuota(w, inbound, user) {
		return
	}
	requests := make(map[RouteTarget]ProviderRequest, len(targets))
	preparedTargets := make([]RouteTarget, 0, len(targets))
	var preparationErr error
	for _, target := range targets {
		request, err := s.prepareCanonicalProviderRequest(r, rawBody, canonicalReq, user, model, inbound, target)
		if err != nil {
			if preparationErr == nil {
				preparationErr = err
			}
			continue
		}
		requests[target] = request
		preparedTargets = append(preparedTargets, target)
	}
	if len(preparedTargets) == 0 {
		if preparationErr == nil {
			preparationErr = errors.New("no compatible provider endpoint")
		}
		writeInboundError(w, inbound, http.StatusBadRequest, "invalid_request_error", preparationErr.Error())
		return
	}

	result := s.doProviderTargetLoop(r.Context(), user, model, preparedTargets, func(target RouteTarget) ProviderRequest {
		return requests[target]
	})
	if !result.OK {
		s.recordProviderResultUsage(user, model, result, providerResultStatus(result.Response))
		writeInboundRetryError(w, inbound, result.Response, result.Attempts, s.cfg.Retry.MaxAttempts, result.Chain)
		return
	}
	resp := result.Response
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw := drain(resp.Body, 64*1024)
		s.recordProviderResultUsage(user, model, result, resp.StatusCode)
		if result.Target.ProviderType == inbound {
			copyResponseHeaders(w.Header(), resp.Header)
			setCORS(w.Header())
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(raw)
			return
		}
		writeInboundUpstreamError(w, inbound, resp.StatusCode, raw)
		return
	}

	if result.Target.ProviderType == inbound {
		s.writeDirectMixedResult(w, resp, user, model, result)
		return
	}
	if isEventStream(resp) {
		if !canonicalReq.Stream {
			writeInboundError(w, inbound, http.StatusBadGateway, "upstream_error", "upstream returned a stream for a non-streaming request")
			s.recordProviderResultUsage(user, model, result, http.StatusBadGateway)
			return
		}
		writeConvertedHeaders(w, result)
		usage, streamErr := s.streamCanonicalResult(
			w,
			resp,
			r.Context(),
			user,
			inbound,
			result.Target.ProviderType,
			model,
			result.RequestID,
			result.StartedAt.Unix(),
			canonicalReq.Stream,
			includeUsage,
		)
		s.recordProviderResultUsage(user, model, result, streamResultStatus(streamErr), usage)
		return
	}
	if canonicalReq.Stream {
		writeInboundError(w, inbound, http.StatusBadGateway, "upstream_error", "upstream returned a non-stream response for a streaming request")
		s.recordProviderResultUsage(user, model, result, http.StatusBadGateway)
		return
	}

	raw := drain(resp.Body, 8*1024*1024)
	canonicalResp, err := canonicalResponseFromProvider(raw, result.Target.ProviderType, model, result)
	if err != nil {
		writeInboundError(w, inbound, http.StatusBadGateway, "upstream_error", err.Error())
		s.recordProviderResultUsage(user, model, result, http.StatusBadGateway)
		return
	}
	s.rememberToolSignatures(r.Context(), user, model, canonicalResp.ToolCalls)
	writeConvertedHeaders(w, result)
	writeCanonicalResponse(w, inbound, canonicalResp)
	s.recordProviderResultUsage(user, model, result, http.StatusOK, tokenUsageFromProviderBody(raw, result.Target.ProviderType))
}

func (s *Server) prepareCanonicalProviderRequest(
	r *http.Request,
	rawBody []byte,
	canonicalReq protocol.CanonicalRequest,
	user AuthToken,
	model string,
	inbound string,
	target RouteTarget,
) (ProviderRequest, error) {
	if target.ProviderType == inbound {
		body := rawBody
		if inbound != providerGemini {
			body = rewriteOpenAIModel(rawBody, model, target.UpstreamModel)
		}
		return ProviderRequest{
			ProviderName:    target.ProviderName,
			InboundProtocol: inbound,
			Method:          r.Method,
			Path:            r.URL.Path,
			RawQuery:        r.URL.RawQuery,
			Headers:         r.Header,
			Body:            body,
			RequestedModel:  model,
			Model:           target.UpstreamModel,
		}, nil
	}

	targetReq := cloneCanonicalRequest(canonicalReq)
	targetReq.Model = target.UpstreamModel
	headers := cleanHeaders(r.Header)
	headers.Set("Content-Type", "application/json")
	request := ProviderRequest{
		ProviderName:    target.ProviderName,
		InboundProtocol: inbound,
		Method:          http.MethodPost,
		Headers:         headers,
		RequestedModel:  model,
		Model:           target.UpstreamModel,
	}
	var payload any
	switch target.ProviderType {
	case providerOpenAI:
		mapReasoningBudgetToOpenAIEffort(targetReq.Reasoning)
		converted, err := protocol.CanonicalToOpenAIChatRequest(targetReq)
		if err != nil {
			return ProviderRequest{}, err
		}
		request.Path = "/v1/chat/completions"
		payload = converted
	case providerOpenAIResponses:
		mapReasoningBudgetToOpenAIEffort(targetReq.Reasoning)
		converted, err := protocol.CanonicalToOpenAIResponsesRequest(targetReq)
		if err != nil {
			return ProviderRequest{}, err
		}
		request.Path = "/v1/responses"
		payload = converted
	case providerAnthropic:
		if targetReq.MaxOutputTokens == nil {
			maxOutputTokens := defaultAnthropicMaxOutputTokens
			if target.MaxOutputTokens > 0 {
				maxOutputTokens = int(target.MaxOutputTokens)
			}
			targetReq.MaxOutputTokens = &maxOutputTokens
		}
		converted, err := protocol.CanonicalToAnthropicMessagesRequest(targetReq)
		if err != nil {
			return ProviderRequest{}, err
		}
		request.Path = "/v1/messages"
		payload = converted
	case providerGemini:
		if inbound == providerOpenAI || inbound == providerOpenAIResponses {
			var effort *string
			if targetReq.Reasoning != nil && strings.TrimSpace(targetReq.Reasoning.Effort) != "" {
				effort = &targetReq.Reasoning.Effort
			}
			level, err := geminiThinkingLevelForOpenAIReasoningEffort(effort, target.UpstreamModel)
			if err != nil {
				return ProviderRequest{}, err
			}
			if level != nil {
				if targetReq.Reasoning == nil {
					targetReq.Reasoning = &protocol.CanonicalReasoning{}
				}
				targetReq.Reasoning.Effort = *level
			}
		}
		s.applyToolSignatures(r.Context(), user, model, &targetReq)
		converted, err := protocol.CanonicalToGeminiGenerateRequest(targetReq)
		if err != nil {
			return ProviderRequest{}, err
		}
		request.Action = "generateContent"
		if targetReq.Stream {
			request.Action = "streamGenerateContent"
		}
		payload = converted
	default:
		return ProviderRequest{}, errors.New("unsupported provider protocol " + target.ProviderType)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ProviderRequest{}, err
	}
	request.Body = body
	return request, nil
}

func mapReasoningBudgetToOpenAIEffort(reasoning *protocol.CanonicalReasoning) {
	if reasoning == nil || reasoning.BudgetTokens == nil {
		return
	}
	budget := *reasoning.BudgetTokens
	reasoning.BudgetTokens = nil
	if strings.TrimSpace(reasoning.Effort) != "" || budget <= 0 {
		return
	}
	switch {
	case budget <= 128:
		reasoning.Effort = "minimal"
	case budget <= 1024:
		reasoning.Effort = "low"
	case budget <= 8192:
		reasoning.Effort = "medium"
	default:
		reasoning.Effort = "high"
	}
}

func cloneCanonicalRequest(req protocol.CanonicalRequest) protocol.CanonicalRequest {
	out := req
	if req.Reasoning != nil {
		reasoning := *req.Reasoning
		out.Reasoning = &reasoning
	}
	out.Messages = make([]protocol.CanonicalMessage, len(req.Messages))
	for index, message := range req.Messages {
		out.Messages[index] = message
		out.Messages[index].Parts = append([]protocol.CanonicalPart(nil), message.Parts...)
	}
	return out
}

func (s *Server) writeDirectMixedResult(
	w http.ResponseWriter,
	resp *http.Response,
	user AuthToken,
	model string,
	result ProviderAttemptResult,
) {
	copyResponseHeaders(w.Header(), resp.Header)
	setCORS(w.Header())
	w.Header().Set("X-Proxy-Debug", strings.Join(result.Chain, " -> "))
	w.Header().Set("X-Proxy-Key", result.Key.Name)
	w.WriteHeader(resp.StatusCode)
	var usage TokenUsage
	if isEventStream(resp) {
		usage = copyProviderStreamResponseBody(w, resp.Body, result.Target.ProviderType)
	} else {
		raw := drain(resp.Body, 8*1024*1024)
		usage = tokenUsageFromProviderBody(raw, result.Target.ProviderType)
		_, _ = io.Copy(w, bytes.NewReader(raw))
	}
	s.recordProviderResultUsage(user, model, result, resp.StatusCode, usage)
}

func tokenUsageFromProviderBody(raw []byte, providerType string) TokenUsage {
	switch providerType {
	case providerGemini:
		var response protocol.GeminiGenerateResponse
		if json.Unmarshal(raw, &response) == nil {
			return tokenUsageFromGeminiResponseBody(raw, response)
		}
	case providerAnthropic:
		var response protocol.AnthropicMessagesResponse
		if json.Unmarshal(raw, &response) == nil {
			canonical, err := protocol.AnthropicMessagesResponseToCanonical(response)
			if err == nil {
				usage := tokenUsageFromCanonical(canonical.Usage)
				var envelope struct {
					Usage json.RawMessage `json:"usage"`
				}
				if json.Unmarshal(raw, &envelope) == nil && !isEmptyRawJSON(envelope.Usage) {
					usage.RawUsage = compactRawJSON(envelope.Usage)
				}
				return usage
			}
		}
	default:
		return tokenUsageFromOpenAIResponseBody(raw)
	}
	return TokenUsage{}
}

func canonicalResponseFromProvider(
	raw []byte,
	providerType string,
	model string,
	result ProviderAttemptResult,
) (protocol.CanonicalResponse, error) {
	var response protocol.CanonicalResponse
	switch providerType {
	case providerOpenAI:
		var upstream protocol.OpenAIChatResponse
		if err := json.Unmarshal(raw, &upstream); err != nil {
			return protocol.CanonicalResponse{}, err
		}
		response = protocol.OpenAIChatResponseToCanonical(upstream)
	case providerOpenAIResponses:
		var upstream protocol.OpenAIResponsesResponse
		if err := json.Unmarshal(raw, &upstream); err != nil {
			return protocol.CanonicalResponse{}, err
		}
		var err error
		response, err = protocol.OpenAIResponsesResponseToCanonical(upstream)
		if err != nil {
			return protocol.CanonicalResponse{}, err
		}
	case providerAnthropic:
		var upstream protocol.AnthropicMessagesResponse
		if err := json.Unmarshal(raw, &upstream); err != nil {
			return protocol.CanonicalResponse{}, err
		}
		var err error
		response, err = protocol.AnthropicMessagesResponseToCanonical(upstream)
		if err != nil {
			return protocol.CanonicalResponse{}, err
		}
	case providerGemini:
		var upstream protocol.GeminiGenerateResponse
		if err := json.Unmarshal(raw, &upstream); err != nil {
			return protocol.CanonicalResponse{}, err
		}
		response = protocol.GeminiToCanonicalResponse(
			upstream,
			model,
			"resp_"+result.RequestID,
			result.StartedAt.Unix(),
			result.RequestID,
		)
	default:
		return protocol.CanonicalResponse{}, errors.New("unsupported provider protocol " + providerType)
	}
	response.ID = canonicalResponseID(response.ID, result.RequestID)
	response.Created = result.StartedAt.Unix()
	response.Model = model
	if response.Role == "" {
		response.Role = "assistant"
	}
	return response, nil
}

func canonicalResponseID(current string, requestID string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return "resp_" + requestID
}

func writeCanonicalResponse(w http.ResponseWriter, inbound string, response protocol.CanonicalResponse) {
	switch inbound {
	case providerOpenAI:
		if !strings.HasPrefix(response.ID, "chatcmpl-") {
			response.ID = "chatcmpl-" + strings.TrimPrefix(response.ID, "resp_")
		}
		writeJSON(w, http.StatusOK, protocol.CanonicalToOpenAIChatResponse(response))
	case providerOpenAIResponses:
		if !strings.HasPrefix(response.ID, "resp_") {
			response.ID = "resp_" + response.ID
		}
		writeJSON(w, http.StatusOK, protocol.CanonicalToOpenAIResponsesResponse(response))
	case providerAnthropic:
		if !strings.HasPrefix(response.ID, "msg_") {
			response.ID = "msg_" + response.ID
		}
		writeJSON(w, http.StatusOK, protocol.CanonicalToAnthropicMessagesResponse(response))
	case providerGemini:
		writeJSON(w, http.StatusOK, protocol.CanonicalToGeminiGenerateResponse(response))
	}
}

func (s *Server) streamCanonicalResult(
	w http.ResponseWriter,
	resp *http.Response,
	ctx context.Context,
	user AuthToken,
	inbound string,
	outbound string,
	model string,
	requestID string,
	created int64,
	stream bool,
	includeUsage bool,
) (TokenUsage, error) {
	if !stream {
		return TokenUsage{}, errors.New("upstream returned a stream for a non-streaming request")
	}
	read := func(emit func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
		rememberingEmit := func(event protocol.CanonicalStreamEvent) {
			if event.Type == protocol.CanonicalStreamToolCallDone && event.Signature != "" {
				s.rememberToolSignatures(ctx, user, model, []protocol.CanonicalToolCall{{
					ID:        event.CallID,
					Name:      event.Name,
					Arguments: event.Arguments,
					Signature: event.Signature,
				}})
			}
			emit(event)
		}
		switch outbound {
		case providerOpenAI:
			return readOpenAIChatStreamAsCanonical(resp.Body, rememberingEmit)
		case providerOpenAIResponses:
			return readResponsesStreamAsCanonical(resp.Body, rememberingEmit)
		case providerAnthropic:
			return readAnthropicStreamAsCanonical(resp.Body, rememberingEmit)
		case providerGemini:
			return readGeminiStreamAsCanonical(resp.Body, requestID, rememberingEmit)
		default:
			return protocol.CanonicalUsage{}, errors.New("unsupported provider stream protocol " + outbound)
		}
	}

	switch inbound {
	case providerOpenAI:
		return streamCanonicalAsOpenAIChat(
			w,
			"chatcmpl-"+requestID,
			created,
			model,
			includeUsage,
			read,
		)
	case providerOpenAIResponses:
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		encoder := newResponsesStreamEncoder(w, flusher, "resp_"+requestID, created, model)
		usage, err := read(encoder.writeEvent)
		if err != nil {
			encoder.writeEvent(canonicalStreamReadError(err))
		} else {
			encoder.finish("", "stop", usage)
		}
		return tokenUsageFromCanonical(usage), err
	case providerAnthropic:
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		encoder := newAnthropicStreamEncoder(w, flusher, "msg_"+requestID, model)
		usage, err := read(encoder.writeEvent)
		if err != nil {
			encoder.writeEvent(canonicalStreamReadError(err))
		} else {
			encoder.finish("stop", usage)
		}
		return tokenUsageFromCanonical(usage), err
	case providerGemini:
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		failed := false
		encoder := newGeminiStreamEncoder(w, flusher)
		usage, err := read(func(event protocol.CanonicalStreamEvent) {
			if event.Type == protocol.CanonicalStreamError {
				failed = true
			}
			encoder.writeEvent(event)
		})
		if err != nil && !failed {
			encoder.writeEvent(canonicalStreamReadError(err))
		}
		return tokenUsageFromCanonical(usage), err
	default:
		return TokenUsage{}, errors.New("unsupported inbound protocol " + inbound)
	}
}

func tokenUsageFromCanonical(usage protocol.CanonicalUsage) TokenUsage {
	return TokenUsage{
		PromptTokens:     int64(usage.PromptTokens),
		CompletionTokens: int64(usage.CompletionTokens),
		TotalTokens:      int64(usage.TotalTokens),
		CachedTokens:     int64(usage.CachedTokens),
		ReasoningTokens:  int64(usage.ReasoningTokens),
	}
}
