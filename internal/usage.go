package buzz

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/teatak/buzz/internal/protocol"
	"github.com/teatak/cart/v3"
)

type TokenUsage struct {
	PromptTokens      int64
	CompletionTokens  int64
	TotalTokens       int64
	CachedTokens      int64
	ReasoningTokens   int64
	QuotaMicrocredits int64
	RawUsage          string
}

func (u TokenUsage) IsZero() bool {
	return u.PromptTokens == 0 &&
		u.CompletionTokens == 0 &&
		u.TotalTokens == 0 &&
		u.CachedTokens == 0 &&
		u.ReasoningTokens == 0 &&
		strings.TrimSpace(u.RawUsage) == ""
}

func (s *Server) recordProviderResultUsage(user AuthToken, model string, result ProviderAttemptResult, status int, usages ...TokenUsage) {
	latency := time.Duration(0)
	if !result.StartedAt.IsZero() {
		latency = time.Since(result.StartedAt)
	}
	var usage TokenUsage
	if len(usages) > 0 {
		usage = usages[0]
	}
	usage.QuotaMicrocredits = quotaMicrocreditsForUsage(usage, result.Target)
	s.recordUsage(user, result.Key, model, result.Target.UpstreamModel, status, latency, usage)
}

func (s *Server) recordUsage(user AuthToken, key APIKey, model, upstreamModel string, status int, latency time.Duration, usage TokenUsage) {
	if status == 0 {
		status = http.StatusBadGateway
	}
	userName := strings.TrimSpace(user.UserName)
	if userName == "" {
		userName = strings.TrimSpace(user.Name)
	}
	if userName == "" {
		userName = "local"
	}
	latencyMS := latency.Milliseconds()
	if latencyMS < 0 {
		latencyMS = 0
	}

	now := time.Now().UTC()
	record := UsageRecord{
		UserID:            user.UserID,
		UserName:          userName,
		UserAPIKeyID:      user.ID,
		UserAPIKeyName:    user.Name,
		ProviderID:        key.ProviderID,
		ProviderName:      key.ProviderName,
		ProviderKeyID:     key.ProviderKeyID,
		ProviderKeyName:   key.Name,
		Model:             model,
		UpstreamModel:     upstreamModel,
		Status:            status,
		LatencyMS:         latencyMS,
		PromptTokens:      usage.PromptTokens,
		CompletionTokens:  usage.CompletionTokens,
		TotalTokens:       usage.TotalTokens,
		CachedTokens:      usage.CachedTokens,
		ReasoningTokens:   usage.ReasoningTokens,
		QuotaMicrocredits: usage.QuotaMicrocredits,
		RawUsage:          usage.RawUsage,
		CreatedAt:         now,
	}
	if user.UserID > 0 && !user.QuotaAnchor.IsZero() && (user.WeeklyQuotaCredits > 0 || user.LifetimeQuotaCredits > 0) {
		periodStart, periodEnd := weeklyQuotaPeriod(user.QuotaAnchor, now)
		record.QuotaPeriodStart = periodStart
		record.QuotaPeriodEnd = &periodEnd
		record.WeeklyQuotaLimitMicrocredits = user.WeeklyQuotaCredits * creditMicrocredits
		record.UsesLifetimeQuota = user.LifetimeQuotaCredits > 0
	}

	s.statsMu.Lock()
	if s.stats.ByUser == nil {
		s.stats.ByUser = make(map[string]int64)
	}
	if s.stats.ByKey == nil {
		s.stats.ByKey = make(map[string]int64)
	}
	s.stats.Requests++
	s.stats.ByUser[userName]++
	if record.UserAPIKeyName != "" {
		s.stats.ByKey[record.UserAPIKeyName]++
	}
	s.stats.LastUpdated = record.CreatedAt
	s.statsMu.Unlock()

	if s.usageCh == nil || user.WeeklyQuotaCredits > 0 || user.LifetimeQuotaCredits > 0 {
		if err := s.store.InsertUsageBatch([]UsageRecord{record}); err != nil {
			log.Printf("record usage: %v", err)
		}
		return
	}
	select {
	case s.usageCh <- record:
	default:
		log.Printf("usage queue full; dropping usage record")
	}
}

func quotaMicrocreditsForUsage(usage TokenUsage, target RouteTarget) int64 {
	uncachedInput := usage.PromptTokens - usage.CachedTokens
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	total := quotaComponent(0, uncachedInput, target.QuotaUncachedInputRate)
	total = quotaComponent(total, usage.CachedTokens, target.QuotaCachedInputRate)
	outputTokens := usage.CompletionTokens
	if totalOutput := usage.TotalTokens - usage.PromptTokens; totalOutput > outputTokens {
		outputTokens = totalOutput
	}
	return quotaComponent(total, outputTokens, target.QuotaOutputRate)
}

func quotaComponent(total, tokens int64, rate float64) int64 {
	if total < 0 || tokens <= 0 || rate <= 0 {
		return max(total, 0)
	}
	component := math.Round(float64(tokens) * rate)
	if math.IsInf(component, 0) || component >= float64(math.MaxInt64-total) {
		return math.MaxInt64
	}
	return total + int64(component)
}

func tokenUsageFromOpenAIResponseBody(raw []byte) TokenUsage {
	var envelope struct {
		Usage    json.RawMessage `json:"usage"`
		Response struct {
			Usage json.RawMessage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return TokenUsage{}
	}
	usageRaw := envelope.Usage
	if isEmptyRawJSON(usageRaw) {
		usageRaw = envelope.Response.Usage
	}
	if isEmptyRawJSON(usageRaw) {
		return TokenUsage{}
	}
	var usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		TotalTokens         int64 `json:"total_tokens"`
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	}
	_ = json.Unmarshal(usageRaw, &usage)
	if usage.PromptTokens == 0 {
		usage.PromptTokens = usage.InputTokens
	}
	if usage.CompletionTokens == 0 {
		usage.CompletionTokens = usage.OutputTokens
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	if usage.PromptTokensDetails.CachedTokens == 0 {
		usage.PromptTokensDetails.CachedTokens = usage.InputTokensDetails.CachedTokens
	}
	if usage.CompletionTokensDetails.ReasoningTokens == 0 {
		usage.CompletionTokensDetails.ReasoningTokens = usage.OutputTokensDetails.ReasoningTokens
	}
	return TokenUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
		RawUsage:         compactRawJSON(usageRaw),
	}
}

func tokenUsageFromGeminiResponseBody(raw []byte, resp protocol.GeminiGenerateResponse) TokenUsage {
	var envelope struct {
		UsageMetadata json.RawMessage `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || isEmptyRawJSON(envelope.UsageMetadata) {
		return TokenUsage{}
	}
	return TokenUsage{
		PromptTokens:     int64(resp.UsageMetadata.PromptTokenCount),
		CompletionTokens: int64(resp.UsageMetadata.CandidatesTokenCount),
		TotalTokens:      int64(resp.UsageMetadata.TotalTokenCount),
		CachedTokens:     int64(resp.UsageMetadata.CachedContentTokenCount),
		ReasoningTokens:  int64(resp.UsageMetadata.ThoughtsTokenCount),
		RawUsage:         compactRawJSON(envelope.UsageMetadata),
	}
}

func isEmptyRawJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func compactRawJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return buf.String()
}

func (s *Server) usageWriter() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	batch := make([]UsageRecord, 0, 64)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.store.InsertUsageBatch(batch); err != nil {
			log.Printf("record usage batch: %v", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case record := <-s.usageCh:
			batch = append(batch, record)
			if len(batch) >= 64 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Server) handleUsage(c *cart.Context) error {
	return s.handleUsageForUser(c, adminUser(c).ID)
}

func (s *Server) handleUserUsageAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	if _, err := s.store.User(userID); err != nil {
		return jsonError(c, http.StatusNotFound, "user not found")
	}
	return s.handleUsageForUser(c, userID)
}

func (s *Server) handleUsageForUser(c *cart.Context, userID int64) error {
	loc, err := usageLocationFromRequest(c.Request)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	from, to, err := parseUsageRange(c.Request, loc)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	keyID, err := parseUsageKeyID(c.Request)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	usage, err := s.store.UsageSummary(UsageQuery{
		UserID:       userID,
		UserAPIKeyID: keyID,
		Model:        strings.TrimSpace(c.Request.URL.Query().Get("model")),
		From:         from,
		To:           to,
		Location:     loc,
	})
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, usage)
}

func parseUsageRange(r *http.Request, loc *time.Location) (time.Time, time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	from, err := parseUsageTime(r.URL.Query().Get("from"), loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parseUsageTime(r.URL.Query().Get("to"), loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.IsZero() && to.IsZero() {
		from = time.Now().In(loc)
		from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
		to = from.Add(24 * time.Hour)
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return time.Time{}, time.Time{}, strconv.ErrSyntax
	}
	return from, to, nil
}

func usageLocationFromRequest(r *http.Request) (*time.Location, error) {
	name := strings.TrimSpace(r.URL.Query().Get("tz"))
	if name == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

func parseUsageTime(value string, loc *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	if loc == nil {
		loc = time.UTC
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, strconv.ErrSyntax
}

func parseUsageKeyID(r *http.Request) (int64, error) {
	keyID := strings.TrimSpace(r.URL.Query().Get("key_id"))
	if keyID == "" || keyID == "all" {
		return 0, nil
	}
	return strconv.ParseInt(keyID, 10, 64)
}

func providerResultStatus(resp *http.Response) int {
	if resp == nil {
		return http.StatusBadGateway
	}
	return resp.StatusCode
}

func (s *Server) statsSnapshot() Stats {
	s.refreshKeyStateStats()
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return s.stats
}

func (s *Server) userStats() Stats {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	return Stats{
		StartedAt:   s.stats.StartedAt,
		Requests:    s.stats.Requests,
		ByUser:      map[string]int64{},
		ByKey:       s.stats.ByKey,
		Exhausted:   map[string]string{},
		RPDLike:     map[string]bool{},
		KeyErrors:   map[string]KeyError{},
		LastUpdated: s.stats.LastUpdated,
	}
}
