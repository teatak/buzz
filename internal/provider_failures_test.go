package buzz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func failureTestServer(forward testProviderFunc) *Server {
	s := &Server{
		providers: map[string]Provider{"test": forward},
		keyState: &KeyState{
			keys:     []APIKey{{Name: "key", Key: "secret", ProviderName: "test"}},
			cooldown: time.Minute,
		},
	}
	s.cfg.Retry.MaxAttempts = 8
	return s
}

func TestProviderTransportFailuresReachClient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"connection refused", errors.New("proxyconnect tcp: dial tcp 192.168.1.2:7890: connect: connection refused"), 502, "connection refused"},
		{"timeout", context.DeadlineExceeded, 504, "context deadline exceeded"},
		{"dns", errors.New("lookup upstream.example: no such host"), 502, "no such host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := failureTestServer(func(context.Context, ProviderRequest, APIKey) (*http.Response, error) {
				return nil, &url.Error{Op: "Post", URL: "https://upstream.example/?key=secret", Err: tc.err}
			})
			target := RouteTarget{ProviderName: "test", UpstreamModel: "model"}
			result := s.doProviderAttemptLoop(context.Background(), AuthToken{}, "model", target, ProviderRequest{})
			w := httptest.NewRecorder()
			writeInboundRetryError(w, providerOpenAIResponses, result.Response, result.Attempts, 8, result.Chain)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String()+w.Header().Get("X-Proxy-Debug"), "secret") {
				t.Fatal("credential leaked into client error")
			}
		})
	}
}

func TestUnavailableFallbackPreservesAttemptFailure(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusTooManyRequests, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := failureTestServer(func(context.Context, ProviderRequest, APIKey) (*http.Response, error) {
				if status == http.StatusBadGateway {
					return nil, errors.New("connection refused")
				}
				return errorResponse(status, "upstream rejected request"), nil
			})
			targets := []RouteTarget{{ProviderName: "test", UpstreamModel: "a"}, {ProviderName: "test", ProviderID: 2, UpstreamModel: "b"}}
			s.keyState.keys[0].ProviderID = 1
			result := s.doProviderTargetLoop(context.Background(), AuthToken{}, "public", targets, func(RouteTarget) ProviderRequest { return ProviderRequest{} })
			defer result.Response.Body.Close()
			if result.Response.StatusCode != status || result.Attempts != 1 {
				t.Fatalf("status=%d attempts=%d", result.Response.StatusCode, result.Attempts)
			}
			if status == http.StatusForbidden && len(s.keyState.errors) != 0 {
				t.Fatalf("403 retained error state: %+v", s.keyState.errors)
			}
		})
	}
}

func TestUnavailableKeysReturnServiceUnavailable(t *testing.T) {
	s := failureTestServer(func(context.Context, ProviderRequest, APIKey) (*http.Response, error) {
		t.Fatal("must not forward without a key")
		return nil, nil
	})
	s.keyState.keys = nil
	result := s.doProviderAttemptLoop(context.Background(), AuthToken{}, "model", RouteTarget{ProviderName: "test"}, ProviderRequest{})
	defer result.Response.Body.Close()
	if result.Response.StatusCode != http.StatusServiceUnavailable || result.Attempts != 0 {
		t.Fatalf("status=%d attempts=%d", result.Response.StatusCode, result.Attempts)
	}
}

func TestForbiddenDoesNotDisableKeyAndFallsBackAcrossModels(t *testing.T) {
	s := failureTestServer(func(_ context.Context, req ProviderRequest, _ APIKey) (*http.Response, error) {
		if req.Model == "restricted" {
			return errorResponse(http.StatusForbidden, `{"error":{"message":"model access denied"}}`), nil
		}
		return errorResponse(http.StatusOK, `{"ok":true}`), nil
	})
	targets := []RouteTarget{{ProviderName: "test", UpstreamModel: "restricted"}, {ProviderName: "test", UpstreamModel: "allowed"}}
	result := s.doProviderTargetLoop(context.Background(), AuthToken{}, "public", targets, func(RouteTarget) ProviderRequest { return ProviderRequest{} })
	defer result.Response.Body.Close()
	if !result.OK || result.Response.StatusCode != 200 || result.Attempts != 2 {
		t.Fatalf("result = %+v", result)
	}
	if s.keyState.AvailableFor(targets[0]) != 1 {
		t.Fatal("403 disabled the key globally")
	}
	if len(s.keyState.errors) != 0 {
		t.Fatalf("403 retained error state: %+v", s.keyState.errors)
	}
}

func TestForbiddenFallsBackToAnotherKey(t *testing.T) {
	s := failureTestServer(func(_ context.Context, _ ProviderRequest, key APIKey) (*http.Response, error) {
		if key.Name == "key" {
			return errorResponse(http.StatusForbidden, "model access denied"), nil
		}
		return errorResponse(http.StatusOK, "ok"), nil
	})
	s.keyState.keys = append(s.keyState.keys, APIKey{Name: "other", ProviderName: "test"})
	result := s.doProviderAttemptLoop(context.Background(), AuthToken{}, "model", RouteTarget{ProviderName: "test"}, ProviderRequest{})
	defer result.Response.Body.Close()
	if !result.OK || result.Key.Name != "other" || result.Attempts != 2 || len(s.keyState.keys) != 2 {
		t.Fatalf("result = %+v, keys = %d", result, len(s.keyState.keys))
	}
}

func TestTransportFailureAfterHTTPFailureKeepsLatestCause(t *testing.T) {
	calls := 0
	s := failureTestServer(func(context.Context, ProviderRequest, APIKey) (*http.Response, error) {
		calls++
		if calls == 1 {
			return errorResponse(500, "earlier HTTP error"), nil
		}
		return nil, errors.New("connection refused")
	})
	s.keyState.keys = append(s.keyState.keys, APIKey{Name: "other", ProviderName: "test"})
	result := s.doProviderAttemptLoop(context.Background(), AuthToken{}, "model", RouteTarget{ProviderName: "test"}, ProviderRequest{})
	defer result.Response.Body.Close()
	if result.Response.StatusCode != 502 || !strings.Contains(string(drain(result.Response.Body, 1024)), "connection refused") {
		t.Fatal("latest transport failure was hidden")
	}
}
