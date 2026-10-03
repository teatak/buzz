package buzz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShouldDisableAPIKey(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		errorCode    string
		errorMessage string
		body         string
		want         bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: true},
		{name: "forbidden", status: http.StatusForbidden, want: true},
		{name: "invalid api key reason", status: http.StatusBadRequest, body: `{"error":{"status":"INVALID_ARGUMENT","message":"API key not valid. Please pass a valid API key.","details":[{"reason":"API_KEY_INVALID"}]}}`, want: true},
		{name: "invalid api key message", status: http.StatusBadRequest, errorMessage: "API key not valid. Please pass a valid API key.", want: true},
		{name: "ordinary bad request", status: http.StatusBadRequest, errorMessage: "invalid request body", want: false},
		{name: "too many requests", status: http.StatusTooManyRequests, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldDisableAPIKey(tt.status, tt.errorCode, tt.errorMessage, []byte(tt.body))
			if got != tt.want {
				t.Fatalf("shouldDisableAPIKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{name: "too many requests", status: http.StatusTooManyRequests, want: true},
		{name: "not found", status: http.StatusNotFound, want: false},
		{name: "bad request", status: http.StatusBadRequest, want: false},
		{name: "server error", status: http.StatusInternalServerError, want: true},
		{name: "bad gateway", status: http.StatusBadGateway, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetry(tt.status)
			if got != tt.want {
				t.Fatalf("shouldRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRepeated429AfterCooldownMarksRPDLike(t *testing.T) {
	key := APIKey{Name: "k1"}
	state := &KeyState{
		keys:         []APIKey{key},
		cooldown:     120 * time.Second,
		rpdCooldown:  time.Hour,
		exhausted:    make(map[string]time.Time),
		cooldownHits: make(map[string]int),
		rpdLike:      make(map[string]bool),
		errors:       make(map[string]KeyError),
	}

	state.MarkExhausted("gemini-3.5-flash", key)
	if got := state.SnapshotRPDLike(); len(got) != 0 {
		t.Fatalf("rpd_like after first 429 = %v, want empty", got)
	}

	state.mu.Lock()
	state.exhausted[cooldownKey("gemini-3.5-flash", key.Name)] = time.Now().Add(-time.Second)
	state.mu.Unlock()

	if _, ok := state.Next("gemini-3.5-flash"); !ok {
		t.Fatal("expected key to be available after cooldown")
	}
	state.MarkExhausted("gemini-3.5-flash", key)

	id := cooldownKey("gemini-3.5-flash", key.Name)
	if got := state.SnapshotRPDLike(); !got[id] {
		t.Fatalf("rpd_like[%q] = false, got %v", id, got)
	}

	state.mu.Lock()
	state.exhausted[id] = time.Now().Add(-time.Second)
	state.mu.Unlock()

	if _, ok := state.Next("gemini-3.5-flash"); !ok {
		t.Fatal("expected rpd-like key to be probed after long cooldown")
	}
	state.MarkExhausted("gemini-3.5-flash", key)

	state.mu.Lock()
	nextExpires := state.exhausted[id]
	state.mu.Unlock()
	if time.Until(nextExpires) < 59*time.Minute {
		t.Fatalf("rpd-like retry cooldown = %s, want about 1h", time.Until(nextExpires))
	}
	if got := state.SnapshotRPDLike(); !got[id] {
		t.Fatalf("rpd_like[%q] cleared after retry 429, got %v", id, got)
	}

	state.MarkHealthy("gemini-3.5-flash", key)
	if got := state.SnapshotRPDLike(); len(got) != 0 {
		t.Fatalf("rpd_like after 200 = %v, want empty", got)
	}
}

func TestAuthenticateAcceptsBearerQueryAndGeminiHeaderKeys(t *testing.T) {
	srv := &Server{
		authTokens: map[string]AuthToken{
			"bh_valid": {Name: "default", UserName: "admin", Valid: true},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-auto:generateContent?key=bh_valid", nil)
	req.Header.Set("Authorization", "Bearer stale")

	user, ok := srv.authenticate(req)
	if !ok {
		t.Fatal("expected query key to authenticate when bearer token is stale")
	}
	if user.Name != "default" {
		t.Fatalf("user.Name = %q, want default", user.Name)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-auto:generateContent", nil)
	req.Header.Set("Authorization", "Bearer stale")
	req.Header.Set("x-goog-api-key", "bh_valid")

	user, ok = srv.authenticate(req)
	if !ok {
		t.Fatal("expected x-goog-api-key to authenticate when bearer token is stale")
	}
	if user.Name != "default" {
		t.Fatalf("user.Name = %q, want default", user.Name)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer stale")
	req.Header.Set("x-api-key", "bh_valid")

	user, ok = srv.authenticate(req)
	if !ok {
		t.Fatal("expected x-api-key to authenticate when bearer token is stale")
	}
	if user.Name != "default" {
		t.Fatalf("user.Name = %q, want default", user.Name)
	}
}

func TestAutoModelIsRejected(t *testing.T) {
	srv := &Server{
		keyState: &KeyState{
			cooldown:  time.Minute,
			exhausted: make(map[string]time.Time),
			errors:    make(map[string]KeyError),
		},
		stats: Stats{
			Exhausted: make(map[string]string),
			KeyErrors: make(map[string]KeyError),
		},
	}
	srv.cfg.Retry.MaxAttempts = 8

	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/auto:generateContent", strings.NewReader("{}"))
	rr := httptest.NewRecorder()
	srv.proxy(rr, req, []byte("{}"), AuthToken{Name: "u", Valid: true}, "auto")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rr.Code, rr.Body.String())
	}
}
