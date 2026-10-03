package buzz

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/teatak/buzz/internal/protocol"
)

func streamCanonicalAsOpenAIChat(
	w http.ResponseWriter,
	id string,
	created int64,
	model string,
	includeUsage bool,
	read func(func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error),
) (TokenUsage, error) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	roleWritten := false
	failed := false
	var streamErr error
	usage, readErr := read(func(event protocol.CanonicalStreamEvent) {
		if event.Type == protocol.CanonicalStreamError {
			if event.Error != nil {
				writeSSEJSON(w, flusher, "", map[string]any{
					"error": map[string]any{
						"message": event.Error.Message,
						"type":    "server_error",
						"code":    event.Error.Code,
					},
				})
			}
			failed = true
			streamErr = errors.New("upstream stream returned an error event")
			return
		}
		if failed {
			return
		}
		if !roleWritten && event.Type != protocol.CanonicalStreamResponseStart && event.Type != protocol.CanonicalStreamUsage {
			writeOpenAIStreamChunk(w, flusher, protocol.OpenAIChatRoleStreamChunk(id, created, model))
			roleWritten = true
		}
		if event.Type == protocol.CanonicalStreamMessageStart && roleWritten {
			return
		}
		if chunk, ok := protocol.CanonicalToOpenAIStreamChunk(event, id, created, model, includeUsage); ok {
			writeOpenAIStreamChunk(w, flusher, chunk)
		}
	})
	if readErr != nil && !failed {
		event := canonicalStreamReadError(readErr)
		if event.Error != nil {
			writeSSEJSON(w, flusher, "", map[string]any{
				"error": map[string]any{
					"message": event.Error.Message,
					"type":    "server_error",
					"code":    event.Error.Code,
				},
			})
		}
		failed = true
	}
	if failed {
		if readErr != nil {
			streamErr = readErr
		}
		return tokenUsageFromCanonical(usage), streamErr
	}
	if !roleWritten {
		writeOpenAIStreamChunk(w, flusher, protocol.OpenAIChatRoleStreamChunk(id, created, model))
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
	return tokenUsageFromCanonical(usage), nil
}

func streamResultStatus(err error) int {
	if err != nil {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

func isEventStream(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

func writeConvertedHeaders(w http.ResponseWriter, result ProviderAttemptResult) {
	w.Header().Set("X-Proxy-Debug", strings.Join(result.Chain, " -> "))
	w.Header().Set("X-Proxy-Key", result.Key.Name)
}
