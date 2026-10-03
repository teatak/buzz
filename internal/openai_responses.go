package buzz

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/teatak/buzz/internal/protocol"
)

func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request, body []byte, user AuthToken) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req protocol.OpenAIResponsesRequest
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
	targets = s.selectRouteTargets(req.Model, targets, openAIResponsesProtocolPreference())
	if len(targets) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "unsupported_endpoint", "selected upstream does not support OpenAI Responses")
		return
	}
	if protocol.ShouldPassthrough(providerOpenAIResponses, targets[0].ProviderType) && !routeTargetsUseMixedProtocols(targets) {
		s.proxyRaw(w, r, body, user, req.Model, targets, providerOpenAIResponses)
		return
	}
	if err := decodeCrossProtocolJSON(body, &req); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerOpenAIResponses) {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	canonicalReq, err := protocol.OpenAIResponsesToCanonicalRequest(req)
	if err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, req.Model, targets, providerOpenAIResponses) {
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	s.proxyMixedCanonicalRoutes(w, r, body, canonicalReq, user, req.Model, targets, providerOpenAIResponses, false)
}

func openAIResponsesProtocolPreference() []string {
	return []string{providerOpenAIResponses, providerOpenAI, providerAnthropic, providerGemini}
}
