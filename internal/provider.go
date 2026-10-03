package buzz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	providerAuto            = "auto"
	providerGemini          = "gemini"
	providerOpenAI          = "openai"
	providerOpenAIResponses = "openai-responses"
	providerAnthropic       = "anthropic"
	providerOllama          = "ollama"
)

var errModelRouteNotFound = errors.New("model route not found")

type Provider interface {
	Forward(ctx context.Context, req ProviderRequest, key APIKey) (*http.Response, error)
}

type ProviderRequest struct {
	ProviderName    string
	InboundProtocol string
	Method          string
	Path            string
	RawQuery        string
	Headers         http.Header
	Body            []byte
	RequestedModel  string
	Model           string
	Action          string
	MaxAttempts     int
}

type ProviderAttemptResult struct {
	Response  *http.Response
	Key       APIKey
	Target    RouteTarget
	Attempts  int
	Chain     []string
	RequestID string
	StartedAt time.Time
	OK        bool
}

type GeminiProvider struct {
	baseURL *url.URL
	client  *http.Client
}

type OpenAIProvider struct {
	baseURL *url.URL
	client  *http.Client
}

func NewGeminiProvider(baseURL *url.URL, client *http.Client) GeminiProvider {
	return GeminiProvider{baseURL: baseURL, client: client}
}

func NewOpenAIProvider(baseURL *url.URL, client *http.Client) OpenAIProvider {
	return OpenAIProvider{baseURL: baseURL, client: client}
}

func newProviderRegistry(records []ProviderRecord, fallbackGeminiBase *url.URL, client *http.Client) (map[string]Provider, error) {
	out := make(map[string]Provider)
	if len(records) == 0 {
		out[providerRuntimeKey(providerGemini, providerGemini)] = NewGeminiProvider(fallbackGeminiBase, client)
		return out, nil
	}
	for _, record := range records {
		for _, endpoint := range record.Endpoints {
			if !endpoint.Enabled {
				continue
			}
			proto := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
			baseURL := strings.TrimSpace(endpoint.BaseURL)
			if baseURL == "" && record.Name == providerGemini && proto == providerGemini {
				baseURL = fallbackGeminiBase.String()
			}
			if proto == "" || baseURL == "" {
				continue
			}
			parsed, err := url.Parse(baseURL)
			if err != nil {
				return nil, err
			}
			switch proto {
			case providerGemini:
				out[providerRuntimeKey(record.Name, proto)] = NewGeminiProvider(parsed, client)
			case providerOpenAI, providerOpenAIResponses:
				out[providerRuntimeKey(record.Name, proto)] = NewOpenAIProvider(parsed, client)
			case providerAnthropic:
				out[providerRuntimeKey(record.Name, proto)] = NewAnthropicProvider(parsed, client)
			}
		}
	}
	if out[providerRuntimeKey(providerGemini, providerGemini)] == nil {
		out[providerRuntimeKey(providerGemini, providerGemini)] = NewGeminiProvider(fallbackGeminiBase, client)
	}
	return out, nil
}

func providerRuntimeKey(providerName string, protocol string) string {
	if protocol == "" {
		return providerName
	}
	return providerName + ":" + protocol
}

func (s *Server) resolveRouteTarget(publicModel string) (RouteTarget, error) {
	targets, err := s.resolveRouteTargets(publicModel)
	if err != nil {
		return RouteTarget{}, err
	}
	return targets[0], nil
}

func (s *Server) resolveRouteTargets(publicModel string) ([]RouteTarget, error) {
	if s.store == nil {
		return nil, fmt.Errorf("%w: %s", errModelRouteNotFound, publicModel)
	}
	targets, ok, err := s.store.ResolveModelRoutes(publicModel)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", errModelRouteNotFound, publicModel)
	}
	return targets, nil
}

func (s *Server) selectRouteTargets(publicModel string, targets []RouteTarget, protocolPreference []string) []RouteTarget {
	if len(targets) == 0 {
		return nil
	}
	rank := make(map[string]int, len(protocolPreference))
	for index, protocol := range protocolPreference {
		rank[protocol] = index
	}
	selected := make(map[int64]RouteTarget)
	selectedRank := make(map[int64]int)
	order := make([]int64, 0, len(targets))
	for _, target := range targets {
		targetRank, supported := rank[target.ProviderType]
		if !supported {
			continue
		}
		if target.RouteProtocol != providerAuto {
			selected[target.ID] = target
			order = append(order, target.ID)
			continue
		}
		currentRank, exists := selectedRank[target.ID]
		if exists && currentRank <= targetRank {
			continue
		}
		if !exists {
			order = append(order, target.ID)
		}
		selected[target.ID] = target
		selectedRank[target.ID] = targetRank
	}
	out := make([]RouteTarget, 0, len(order))
	for _, routeID := range order {
		out = append(out, selected[routeID])
	}
	return s.rotateRouteTargets(publicModel, out)
}

func (s *Server) rotateRouteTargets(publicModel string, targets []RouteTarget) []RouteTarget {
	if len(targets) <= 1 {
		return targets
	}
	if targets[0].SelectionPolicy == "weighted" {
		return s.rotateWeightedRouteTargets(publicModel, targets)
	}
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.routeNext == nil {
		s.routeNext = make(map[string]int)
	}
	start := s.routeNext[publicModel] % len(targets)
	s.routeNext[publicModel] = (start + 1) % len(targets)
	out := make([]RouteTarget, 0, len(targets))
	out = append(out, targets[start:]...)
	out = append(out, targets[:start]...)
	return out
}

func (s *Server) rotateWeightedRouteTargets(publicModel string, targets []RouteTarget) []RouteTarget {
	weighted := expandWeightedTargets(targets)
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.routeNext == nil {
		s.routeNext = make(map[string]int)
	}
	start := s.routeNext[publicModel] % len(weighted)
	s.routeNext[publicModel] = (start + 1) % len(weighted)
	selected := weighted[start]
	selectedIndex := 0
	for i, target := range targets {
		if target.ID == selected.ID {
			selectedIndex = i
			break
		}
	}
	out := make([]RouteTarget, 0, len(targets))
	out = append(out, targets[selectedIndex:]...)
	out = append(out, targets[:selectedIndex]...)
	return out
}

func expandWeightedTargets(targets []RouteTarget) []RouteTarget {
	out := make([]RouteTarget, 0, len(targets))
	for _, target := range targets {
		weight := target.Weight
		if weight <= 0 {
			weight = 1
		}
		if weight > 100 {
			weight = 100
		}
		for i := 0; i < weight; i++ {
			out = append(out, target)
		}
	}
	return out
}

func (p GeminiProvider) Forward(ctx context.Context, req ProviderRequest, key APIKey) (*http.Response, error) {
	upstream := *p.baseURL
	method := req.Method
	if method == "" {
		method = http.MethodPost
	}

	if req.Action != "" {
		upstream.Path = strings.TrimRight(upstream.Path, "/") + "/v1beta/models/" + req.Model + ":" + req.Action
		q := upstream.Query()
		q.Set("key", key.Key)
		if req.Action == "streamGenerateContent" {
			q.Set("alt", "sse")
		}
		upstream.RawQuery = q.Encode()
	} else {
		upstream.Path = req.Path
		if req.RequestedModel != "" && req.Model != "" && req.RequestedModel != req.Model {
			upstream.Path = strings.Replace(upstream.Path, "/models/"+req.RequestedModel, "/models/"+req.Model, 1)
		}
		upstream.RawQuery = req.RawQuery
		q := upstream.Query()
		q.Set("key", key.Key)
		q.Del("model")
		upstream.RawQuery = q.Encode()
	}

	providerCtx, cancel := context.WithCancel(ctx)
	httpReq, err := http.NewRequestWithContext(providerCtx, method, upstream.String(), bytes.NewReader(req.Body))
	if err != nil {
		cancel()
		return nil, err
	}
	if req.Action == "" {
		httpReq.Header = cleanHeaders(req.Headers)
	} else {
		httpReq.Header.Set("Content-Type", "application/json")
		if req.Action == "streamGenerateContent" {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func (p OpenAIProvider) Forward(ctx context.Context, req ProviderRequest, key APIKey) (*http.Response, error) {
	upstream := *p.baseURL
	upstream.Path = providerRequestPath(upstream.Path, req.Path)
	upstream.RawQuery = req.RawQuery
	q := upstream.Query()
	q.Del("key")
	upstream.RawQuery = q.Encode()

	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	providerCtx, cancel := context.WithCancel(ctx)
	httpReq, err := http.NewRequestWithContext(providerCtx, method, upstream.String(), bytes.NewReader(req.Body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header = cleanHeaders(req.Headers)
	httpReq.Header.Del("Authorization")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Del("x-goog-api-key")
	httpReq.Header.Set("Authorization", "Bearer "+key.Key)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type AnthropicProvider struct {
	baseURL *url.URL
	client  *http.Client
}

func NewAnthropicProvider(baseURL *url.URL, client *http.Client) AnthropicProvider {
	return AnthropicProvider{baseURL: baseURL, client: client}
}

func (p AnthropicProvider) Forward(ctx context.Context, req ProviderRequest, key APIKey) (*http.Response, error) {
	upstream := *p.baseURL
	upstream.Path = providerRequestPath(upstream.Path, req.Path)
	upstream.RawQuery = req.RawQuery
	q := upstream.Query()
	q.Del("key")
	upstream.RawQuery = q.Encode()

	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	providerCtx, cancel := context.WithCancel(ctx)
	httpReq, err := http.NewRequestWithContext(providerCtx, method, upstream.String(), bytes.NewReader(req.Body))
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header = cleanHeaders(req.Headers)
	httpReq.Header.Del("Authorization")
	httpReq.Header.Set("Accept-Encoding", "identity")
	httpReq.Header.Del("x-goog-api-key")
	httpReq.Header.Set("x-api-key", key.Key)
	if httpReq.Header.Get("anthropic-version") == "" {
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

func providerRequestPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return requestPath
	}
	basePath = strings.TrimRight(basePath, "/")
	if requestPath == "" || requestPath == "/" {
		return basePath
	}
	suffix := "/" + strings.TrimLeft(requestPath, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(suffix, "/v1/") {
		suffix = strings.TrimPrefix(suffix, "/v1")
	}
	return basePath + suffix
}

func (s *Server) doProviderAttemptLoop(ctx context.Context, user AuthToken, model string, target RouteTarget, req ProviderRequest) ProviderAttemptResult {
	maxAttempts := s.cfg.Retry.MaxAttempts
	if req.MaxAttempts > 0 {
		maxAttempts = req.MaxAttempts
	}
	if s.keyState != nil {
		keyCount := s.keyState.AvailableFor(target)
		if keyCount > 0 && keyCount < maxAttempts {
			maxAttempts = keyCount
		}
	}
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	requestID := randomHex(8)
	startedAt := time.Now()
	var chain []string
	var lastResp *http.Response
	var lastErrBody []byte
	var lastKey APIKey
	attempts := 0

	if req.ProviderName == "" {
		req.ProviderName = target.ProviderName
	}
	if req.Model == "" {
		req.Model = target.UpstreamModel
	}
	req.Body = normalizeModelReasoning(target.ProviderType, req.Model, req.Body)
	s.runtimeMu.Lock()
	provider := s.providers[providerRuntimeKey(req.ProviderName, target.ProviderType)]
	if provider == nil {
		provider = s.providers[req.ProviderName]
	}
	s.runtimeMu.Unlock()
	if provider == nil {
		return ProviderAttemptResult{
			Response:  errorResponse(http.StatusBadGateway, "provider not found"),
			Target:    target,
			Attempts:  attempts,
			Chain:     append(chain, req.ProviderName+":provider-not-found"),
			RequestID: requestID,
			StartedAt: startedAt,
		}
	}

	cooldownModel := target.CooldownModel()
	if cooldownModel == "" {
		cooldownModel = model
	}
	for attempts < maxAttempts {
		key, ok := s.nextProviderKey(ctx, cooldownModel, target)
		if !ok {
			chain = append(chain, cooldownModel+":all-keys-cooling")
			break
		}
		lastKey = key

		attempts++
		resp, err := provider.Forward(ctx, req, key)
		if err != nil {
			chain = append(chain, fmt.Sprintf("%s(%s):%s", model, key.Name, err.Error()))
			s.keyState.MarkError(cooldownModel, key, 0, err.Error())
			s.refreshKeyStateStats()
			continue
		}

		var errorCode, errorMessage string
		disableKey := false
		if resp.StatusCode >= 400 {
			lastResp = resp
			lastErrBody = drain(resp.Body, 64*1024)
			errorCode, errorMessage = parseUpstreamError(lastErrBody)
			disableKey = shouldDisableAPIKey(resp.StatusCode, errorCode, errorMessage, lastErrBody)
			if resp.StatusCode == http.StatusTooManyRequests {
				s.markProviderKeyExhausted(ctx, cooldownModel, target, key)
			} else if disableKey {
				s.keyState.MarkError(cooldownModel, key, resp.StatusCode, string(lastErrBody))
				s.disableAPIKey(key, resp.StatusCode, errorCode, errorMessage, lastErrBody)
			} else {
				s.keyState.MarkError(cooldownModel, key, resp.StatusCode, string(lastErrBody))
			}
			s.refreshKeyStateStats()
		}

		if shouldRetry(resp.StatusCode) {
			resp.Body.Close()
			chain = append(chain, fmt.Sprintf("%s(%s):%d", model, key.Name, resp.StatusCode))
			continue
		}
		if disableKey {
			resp.Body.Close()
			chain = append(chain, fmt.Sprintf("%s(%s):disabled:%d", model, key.Name, resp.StatusCode))
			continue
		}
		if resp.StatusCode >= 400 {
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(lastErrBody))
		} else {
			s.markProviderKeyHealthy(ctx, cooldownModel, target, key)
			s.refreshKeyStateStats()
		}
		return ProviderAttemptResult{
			Response:  resp,
			Key:       key,
			Target:    target,
			Attempts:  attempts,
			Chain:     chain,
			RequestID: requestID,
			StartedAt: startedAt,
			OK:        true,
		}
	}

	if lastResp == nil {
		lastResp = errorResponse(http.StatusTooManyRequests, "")
	} else {
		lastResp.Body = io.NopCloser(bytes.NewReader(lastErrBody))
	}
	return ProviderAttemptResult{
		Response:  lastResp,
		Key:       lastKey,
		Target:    target,
		Attempts:  attempts,
		Chain:     chain,
		RequestID: requestID,
		StartedAt: startedAt,
	}
}

func (s *Server) doProviderTargetLoop(ctx context.Context, user AuthToken, model string, targets []RouteTarget, buildReq func(RouteTarget) ProviderRequest) ProviderAttemptResult {
	maxAttempts := s.cfg.Retry.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	targets = s.preferRouteSessionTarget(ctx, user, model, targets)
	var combined ProviderAttemptResult
	var chain []string
	attempts := 0
	for _, target := range targets {
		remaining := maxAttempts - attempts
		if remaining <= 0 {
			break
		}
		req := buildReq(target)
		req.MaxAttempts = remaining
		result := s.doProviderAttemptLoop(ctx, user, model, target, req)
		attempts += result.Attempts
		chain = append(chain, result.Chain...)
		result.Attempts = attempts
		result.Chain = chain
		combined = result
		if result.OK {
			if result.Response != nil && result.Response.StatusCode < 400 {
				s.rememberRouteSession(ctx, user, model, result.Target)
			}
			return result
		}
		if result.Attempts >= maxAttempts {
			break
		}
	}
	if combined.Response == nil {
		combined.Response = errorResponse(http.StatusTooManyRequests, "")
	}
	combined.Attempts = attempts
	combined.Chain = chain
	return combined
}

func errorResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
