package buzz

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func newServer(cfg Config) (*Server, error) {
	upstream, err := url.Parse(cfg.Upstream.BaseURL)
	if err != nil {
		return nil, err
	}
	timeout, err := time.ParseDuration(cfg.Upstream.Timeout)
	if err != nil {
		return nil, err
	}

	store, err := OpenStore(cfg.Database)
	if err != nil {
		return nil, err
	}
	runtimeCache, err := OpenRuntimeCache(cfg.Redis)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	authTokens, err := store.AuthTokens()
	if err != nil {
		_ = runtimeCache.Close()
		_ = store.Close()
		return nil, err
	}
	apiKeys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		_ = runtimeCache.Close()
		_ = store.Close()
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 100
	transport.IdleConnTimeout = 90 * time.Second
	transport.DialContext = (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext

	client := &http.Client{Timeout: timeout, Transport: transport}
	providerRecords, err := store.EnabledProviders()
	if err != nil {
		_ = runtimeCache.Close()
		_ = store.Close()
		return nil, err
	}
	providers, err := newProviderRegistry(providerRecords, upstream, client)
	if err != nil {
		_ = runtimeCache.Close()
		_ = store.Close()
		return nil, err
	}

	srv := &Server{
		cfg:           cfg,
		store:         store,
		runtimeCache:  runtimeCache,
		upstream:      upstream,
		client:        client,
		providers:     providers,
		authTokens:    authTokens,
		adminSessions: make(map[string]SessionUser),
		routeNext:     make(map[string]int),
		routeSessions: make(map[string]RouteSession),
		toolSigs:      make(map[string]toolSignatureEntry),
		usageCh:       make(chan UsageRecord, 1024),
		keyState: &KeyState{
			keys:         apiKeys,
			cooldown:     time.Duration(cfg.Retry.CooldownSeconds) * time.Second,
			rpdCooldown:  time.Hour,
			exhausted:    make(map[string]time.Time),
			cooldownHits: make(map[string]int),
			rpdLike:      make(map[string]bool),
			errors:       make(map[string]KeyError),
		},
		stats: Stats{
			StartedAt: time.Now(),
			ByUser:    make(map[string]int64),
			ByKey:     make(map[string]int64),
			Exhausted: make(map[string]string),
			RPDLike:   make(map[string]bool),
			KeyErrors: make(map[string]KeyError),
		},
	}
	srv.adminAPI = srv.newAdminAPI()
	go srv.usageWriter()
	return srv, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.URL.Path == "/" {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}

	if r.URL.Path == "/admin" ||
		r.URL.Path == "/admin/" ||
		r.URL.Path == "/admin/favicon.svg" ||
		r.URL.Path == "/favicon.svg" ||
		strings.HasPrefix(r.URL.Path, "/admin/assets/") {
		if s.serveAdmin(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/admin/api/") {
		s.adminAPI.ServeHTTP(w, r)
		return
	}

	switch r.URL.Path {
	case "/health":
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	case "/stats":
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	case "/flush-exhausted":
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	user, ok := s.authenticate(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/v1/") {
		switch r.URL.Path {
		case "/v1/chat/completions":
			body, ok := readRequestBody(w, r)
			if !ok {
				return
			}
			s.handleOpenAIChatCompletions(w, r, body, user)
		case "/v1/messages", "/v1/complete":
			body, ok := readRequestBody(w, r)
			if !ok {
				return
			}
			s.handleAnthropicPassthrough(w, r, body, user)
		case "/v1/responses":
			body, ok := readRequestBody(w, r)
			if !ok {
				return
			}
			s.handleOpenAIResponses(w, r, body, user)
		case "/v1/models":
			s.handleOpenAIModels(w, r, user)
		default:
			writeOpenAIError(w, http.StatusNotFound, "not_found", "OpenAI endpoint not found")
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		model, _, ok := parseGeminiModelAction(r.URL.Path)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Gemini endpoint not found"})
			return
		}
		body, ok := readRequestBody(w, r)
		if !ok {
			return
		}
		s.proxy(w, r, body, user, model)
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, true
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	return body, true
}
