package buzz

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return false
	}
	return true
}

func maskSecret(value string) string {
	if len(value) <= 10 {
		return strings.Repeat("*", len(value))
	}
	return value[:6] + strings.Repeat("*", len(value)-10) + value[len(value)-4:]
}

func randomHex(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	const alphabet = "0123456789abcdef"
	out := make([]byte, len(buf)*2)
	for i, b := range buf {
		out[i*2] = alphabet[b>>4]
		out[i*2+1] = alphabet[b&0x0f]
	}
	return string(out)
}

func parseModel(path string, fallback string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "models" && i+1 < len(parts) {
			model := parts[i+1]
			if idx := strings.IndexByte(model, ':'); idx >= 0 {
				return model[:idx]
			}
			return model
		}
	}
	return fallback
}

func parseIDs(value string) []int64 {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func cleanHeaders(in http.Header) http.Header {
	out := in.Clone()
	for _, h := range []string{
		"Authorization",
		"Connection",
		"Content-Length",
		"Cf-Connecting-Ip",
		"Cf-Ipcountry",
		"Cf-Ray",
		"Cf-Visitor",
		"Host",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"X-Api-Key",
		"X-Forwarded-For",
		"X-Goog-Api-Key",
		"X-Real-Ip",
	} {
		out.Del(h)
	}
	return out
}

func copyResponseHeaders(dst, src http.Header) {
	for k, values := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		dst.Del(k)
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func copyResponseBody(w http.ResponseWriter, r io.Reader) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func setCORS(h http.Header) {
	for k, v := range corsHeaders {
		h.Set(k, v)
	}
}

func shouldRetry(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func shouldDisableAPIKey(status int, errorCode, errorMessage string, body []byte) bool {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	text := strings.ToUpper(errorCode + "\n" + errorMessage + "\n" + string(body))
	return strings.Contains(text, "API_KEY_INVALID") || strings.Contains(text, "API KEY NOT VALID")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	setCORS(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func drain(r io.Reader, max int64) []byte {
	if r == nil {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(r, max))
	return data
}

func parseUpstreamError(body []byte) (string, string) {
	var payload struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if len(body) == 0 || json.Unmarshal(body, &payload) != nil {
		return "", ""
	}
	return payload.Error.Status, payload.Error.Message
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
