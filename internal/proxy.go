package buzz

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/teatak/buzz/internal/protocol"
)

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, body []byte, user AuthToken, originalModel string) {
	if isAutoModel(originalModel) {
		writeGeminiError(w, http.StatusBadRequest, "auto model routing has been removed")
		return
	}

	model := originalModel
	targets, err := s.resolveRouteTargets(model)
	if err != nil {
		if errors.Is(err, errModelRouteNotFound) {
			writeGeminiError(w, http.StatusNotFound, "model route not found: "+model)
			return
		}
		writeGeminiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	actionModel, action, ok := parseGeminiModelAction(r.URL.Path)
	if ok && actionModel != "" {
		model = actionModel
	}
	targets = s.selectRouteTargets(model, targets, geminiProtocolPreference())
	if len(targets) == 0 {
		writeGeminiError(w, http.StatusBadRequest, "selected upstream does not support Gemini")
		return
	}
	target := targets[0]
	if protocol.ShouldPassthrough(providerGemini, target.ProviderType) && !routeTargetsUseMixedProtocols(targets) {
		s.proxyRaw(w, r, body, user, model, targets, providerGemini)
		return
	}
	if action != "generateContent" && action != "streamGenerateContent" {
		if s.proxyRawIfAvailable(w, r, body, user, model, targets, providerGemini) {
			return
		}
		writeGeminiError(w, http.StatusBadRequest, "unsupported Gemini action")
		return
	}
	var req protocol.GeminiGenerateRequest
	if err := decodeCrossProtocolJSON(body, &req); err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, model, targets, providerGemini) {
			return
		}
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	canonicalReq, err := protocol.GeminiGenerateToCanonicalRequest(req, model, action == "streamGenerateContent")
	if err != nil {
		if s.proxyRawIfAvailable(w, r, body, user, model, targets, providerGemini) {
			return
		}
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.proxyMixedCanonicalRoutes(w, r, body, canonicalReq, user, model, targets, providerGemini, false)
}

func geminiProtocolPreference() []string {
	return []string{providerGemini, providerOpenAI, providerOpenAIResponses, providerAnthropic}
}

func isAutoModel(model string) bool {
	return model == "auto" || model == "gemini-auto"
}

func parseGeminiModelAction(path string) (string, string, bool) {
	const prefix = "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest, err := url.PathUnescape(strings.TrimPrefix(path, prefix))
	if err != nil {
		return "", "", false
	}
	model, action, ok := strings.Cut(rest, ":")
	if !ok || strings.TrimSpace(model) == "" || strings.TrimSpace(action) == "" {
		return "", "", false
	}
	return model, action, true
}
