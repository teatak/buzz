package buzz

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func decodeCrossProtocolJSON(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func writeInboundError(w http.ResponseWriter, inbound string, status int, code, message string) {
	switch inbound {
	case providerAnthropic:
		writeAnthropicError(w, status, code, message)
	case providerGemini:
		writeGeminiError(w, status, message)
	default:
		writeOpenAIError(w, status, code, message)
	}
}

func writeAnthropicError(w http.ResponseWriter, status int, code, message string) {
	errorType := anthropicErrorType(status, code)
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errorType,
			"message": message,
		},
	})
}

func anthropicErrorType(status int, code string) string {
	switch code {
	case "invalid_request_error", "authentication_error", "permission_error",
		"not_found_error", "request_too_large", "rate_limit_error",
		"api_error", "overloaded_error":
		return code
	}

	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func writeGeminiError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"status":  geminiErrorStatus(status),
		},
	})
}

func geminiErrorStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ALREADY_EXISTS"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}

func canonicalStreamErrorHTTPStatus(code string) int {
	value := strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(value, "rate"), strings.Contains(value, "resource_exhausted"), value == "429":
		return http.StatusTooManyRequests
	case strings.Contains(value, "unauth"), strings.Contains(value, "authentication"), strings.Contains(value, "api_key"), value == "401":
		return http.StatusUnauthorized
	case strings.Contains(value, "permission"), value == "403":
		return http.StatusForbidden
	case strings.Contains(value, "not_found"), value == "404":
		return http.StatusNotFound
	case strings.Contains(value, "invalid"), value == "400":
		return http.StatusBadRequest
	case strings.Contains(value, "timeout"), strings.Contains(value, "deadline"), value == "408", value == "504":
		return http.StatusGatewayTimeout
	case strings.Contains(value, "unavailable"), strings.Contains(value, "overload"), value == "503", value == "529":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func writeInboundRetryError(w http.ResponseWriter, inbound string, resp *http.Response, attempts, maxAttempts int, chain []string) {
	status := http.StatusBadGateway
	raw := []byte{}
	if resp != nil {
		status = resp.StatusCode
		raw = drain(resp.Body, 64*1024)
		resp.Body.Close()
	}
	w.Header().Set("X-Proxy-Debug", strings.Join(chain, " -> "))
	message := openAIUpstreamErrorMessage(status, raw)
	writeInboundError(
		w,
		inbound,
		status,
		"upstream_error",
		fmt.Sprintf("upstream failed after %d/%d attempts: %s", attempts, maxAttempts, message),
	)
}

func writeInboundUpstreamError(w http.ResponseWriter, inbound string, status int, raw []byte) {
	writeInboundError(w, inbound, status, "upstream_error", openAIUpstreamErrorMessage(status, raw))
}
