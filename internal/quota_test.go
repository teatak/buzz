package buzz

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWeeklyQuotaPeriodUsesUserCreationAnchor(t *testing.T) {
	anchor := time.Date(2026, time.July, 29, 3, 15, 0, 0, time.UTC)

	start, end := weeklyQuotaPeriod(anchor, anchor.Add(15*24*time.Hour))
	wantStart := anchor.Add(14 * 24 * time.Hour)
	if !start.Equal(wantStart) {
		t.Fatalf("start = %v, want %v", start, wantStart)
	}
	if !end.Equal(wantStart.Add(7 * 24 * time.Hour)) {
		t.Fatalf("end = %v", end)
	}

	boundaryStart, _ := weeklyQuotaPeriod(anchor, anchor.Add(7*24*time.Hour))
	if !boundaryStart.Equal(anchor.Add(7 * 24 * time.Hour)) {
		t.Fatalf("boundary start = %v", boundaryStart)
	}
}

func TestZeroWeeklyAndLifetimeQuotaIsUnlimited(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("unlimited-quota-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.UserQuotaStatus(user.ID, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Unlimited || status.WeeklyQuotaCredits != 0 || status.LifetimeQuotaCredits != 0 {
		t.Fatalf("quota status = %+v", status)
	}
	if !(&Server{store: store}).enforceUserQuota(httptest.NewRecorder(), providerOpenAI, AuthToken{UserID: user.ID}) {
		t.Fatal("zero weekly and lifetime quotas should be unlimited")
	}
}

func TestWeeklyQuotaRejectsAfterExhaustionWithoutLifetimeQuota(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("weekly-quota-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserQuota(user.ID, 2, 0, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	usedAt := user.QuotaAnchor.Add(time.Hour)
	periodStart, periodEnd := weeklyQuotaPeriod(user.QuotaAnchor, usedAt)
	if err := store.InsertUsageBatch([]UsageRecord{
		{UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 750_000, WeeklyQuotaLimitMicrocredits: 2_000_000, QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt},
		{UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 500_000, WeeklyQuotaLimitMicrocredits: 2_000_000, QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	status, err := store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != 1_250_000 || status.WeeklyRemainingMicrocredits != 750_000 || status.Unlimited {
		t.Fatalf("quota status = %+v", status)
	}
	server := &Server{store: store}
	userToken := AuthToken{UserID: user.ID, WeeklyQuotaCredits: 2}
	if !server.enforceUserQuota(httptest.NewRecorder(), providerOpenAI, userToken) {
		t.Fatal("remaining weekly quota should allow requests")
	}
	if err := store.InsertUsageBatch([]UsageRecord{{
		UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 750_000,
		WeeklyQuotaLimitMicrocredits: 2_000_000, QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt.Add(2 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if server.enforceUserQuota(recorder, providerOpenAI, userToken) {
		t.Fatal("exhausted weekly quota should reject without lifetime quota")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestWeeklyQuotaFallsBackToLifetimeQuota(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("weekly-lifetime-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserQuota(user.ID, 1, 1, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	usedAt := user.QuotaAnchor.Add(time.Hour)
	periodStart, periodEnd := weeklyQuotaPeriod(user.QuotaAnchor, usedAt)
	record := func(amount int64, at time.Time) UsageRecord {
		return UsageRecord{
			UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: amount,
			WeeklyQuotaLimitMicrocredits: creditMicrocredits, UsesLifetimeQuota: true,
			QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: at,
		}
	}

	if err := store.InsertUsageBatch([]UsageRecord{record(750_000, usedAt)}); err != nil {
		t.Fatal(err)
	}
	status, err := store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != 750_000 || status.LifetimeUsedMicrocredits != 0 {
		t.Fatalf("lifetime quota used before weekly quota: %+v", status)
	}

	if err := store.InsertUsageBatch([]UsageRecord{record(500_000, usedAt.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	status, err = store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != creditMicrocredits || status.LifetimeUsedMicrocredits != 250_000 || status.LifetimeRemainingMicrocredits != 750_000 {
		t.Fatalf("weekly to lifetime fallback = %+v", status)
	}
	server := &Server{store: store}
	userToken := AuthToken{UserID: user.ID, WeeklyQuotaCredits: 1, LifetimeQuotaCredits: 1}
	if !server.enforceUserQuota(httptest.NewRecorder(), providerOpenAI, userToken) {
		t.Fatal("remaining lifetime quota should allow requests")
	}

	if err := store.InsertUsageBatch([]UsageRecord{record(800_000, usedAt.Add(2*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	status, err = store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.LifetimeUsedMicrocredits != 1_050_000 || status.LifetimeRemainingMicrocredits != 0 {
		t.Fatalf("exhausted lifetime quota status = %+v", status)
	}
	if server.enforceUserQuota(httptest.NewRecorder(), providerOpenAI, userToken) {
		t.Fatal("exhausted weekly and lifetime quotas should reject requests")
	}

	resetAt := usedAt.Add(3 * time.Minute)
	if _, err := store.ResetUserQuota(user.ID, resetAt); err != nil {
		t.Fatal(err)
	}
	status, err = store.UserQuotaStatus(user.ID, resetAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != 0 || status.WeeklyRemainingMicrocredits != creditMicrocredits || status.LifetimeUsedMicrocredits != 1_050_000 {
		t.Fatalf("weekly reset should preserve lifetime usage: %+v", status)
	}
	if !server.enforceUserQuota(httptest.NewRecorder(), providerOpenAI, userToken) {
		t.Fatal("restored weekly quota should allow requests even when lifetime quota is exhausted")
	}
}

func TestLifetimeOnlyQuotaStartsImmediately(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("lifetime-only-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserQuota(user.ID, 0, 1, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	usedAt := user.QuotaAnchor.Add(time.Hour)
	periodStart, periodEnd := weeklyQuotaPeriod(user.QuotaAnchor, usedAt)
	if err := store.InsertUsageBatch([]UsageRecord{{
		UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 1_200_000,
		UsesLifetimeQuota: true, QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt,
	}}); err != nil {
		t.Fatal(err)
	}
	status, err := store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != 0 || status.LifetimeUsedMicrocredits != 1_200_000 || status.LifetimeRemainingMicrocredits != 0 {
		t.Fatalf("lifetime-only quota status = %+v", status)
	}
	if (&Server{store: store}).enforceUserQuota(httptest.NewRecorder(), providerOpenAI, AuthToken{UserID: user.ID, LifetimeQuotaCredits: 1}) {
		t.Fatal("exhausted lifetime-only quota should reject requests")
	}
}

func TestResetWeeklyQuotasLeavesLifetimeOnlyUsersUnchanged(t *testing.T) {
	store := openTestStore(t)
	weekly, err := store.CreateAppUser("weekly-reset-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	weekly, err = store.UpdateUserQuota(weekly.ID, 10, 0, weekly.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := store.CreateAppUser("lifetime-reset-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err = store.UpdateUserQuota(lifetime.ID, 0, 10, lifetime.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	resetAt := weekly.CreatedAt.Add(2 * time.Hour)
	if _, err := store.ResetWeeklyQuotas(resetAt); err != nil {
		t.Fatal(err)
	}
	weeklyAfter, _ := store.User(weekly.ID)
	lifetimeAfter, _ := store.User(lifetime.ID)
	if !weeklyAfter.QuotaAnchor.Equal(resetAt) {
		t.Fatalf("weekly anchor = %v", weeklyAfter.QuotaAnchor)
	}
	if !lifetimeAfter.QuotaAnchor.Equal(lifetime.QuotaAnchor) {
		t.Fatalf("lifetime-only anchor changed from %v to %v", lifetime.QuotaAnchor, lifetimeAfter.QuotaAnchor)
	}
}

func TestNaturalWeeklyRolloverPreservesLifetimeUsage(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("weekly-rollover-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserQuota(user.ID, 1, 2, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	usedAt := user.QuotaAnchor.Add(time.Hour)
	periodStart, periodEnd := weeklyQuotaPeriod(user.QuotaAnchor, usedAt)
	if err := store.InsertUsageBatch([]UsageRecord{{
		UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 1_250_000,
		WeeklyQuotaLimitMicrocredits: creditMicrocredits, UsesLifetimeQuota: true,
		QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt,
	}}); err != nil {
		t.Fatal(err)
	}

	status, err := store.UserQuotaStatus(user.ID, user.QuotaAnchor.Add(8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if status.WeeklyUsedMicrocredits != 0 || status.WeeklyRemainingMicrocredits != creditMicrocredits || status.LifetimeUsedMicrocredits != 250_000 {
		t.Fatalf("quota after natural weekly rollover = %+v", status)
	}
}

func TestUpdatingLifetimeQuotaPreservesWeeklyCycleAndLifetimeUsage(t *testing.T) {
	store := openTestStore(t)
	user, err := store.CreateAppUser("lifetime-update-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUserQuota(user.ID, 1, 1, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	anchor := user.QuotaAnchor
	usedAt := anchor.Add(time.Hour)
	periodStart, periodEnd := weeklyQuotaPeriod(anchor, usedAt)
	if err := store.InsertUsageBatch([]UsageRecord{{
		UserID: user.ID, Status: http.StatusOK, QuotaMicrocredits: 1_250_000,
		WeeklyQuotaLimitMicrocredits: creditMicrocredits, UsesLifetimeQuota: true,
		QuotaPeriodStart: periodStart, QuotaPeriodEnd: &periodEnd, CreatedAt: usedAt,
	}}); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateUserQuota(user.ID, 1, 3, usedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.QuotaAnchor.Equal(anchor) {
		t.Fatalf("lifetime quota update changed weekly anchor: %v -> %v", anchor, updated.QuotaAnchor)
	}
	status, err := store.UserQuotaStatus(user.ID, usedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status.LifetimeQuotaCredits != 3 || status.LifetimeUsedMicrocredits != 250_000 || status.LifetimeRemainingMicrocredits != 2_750_000 {
		t.Fatalf("quota after lifetime total update = %+v", status)
	}
}

func TestQuotaMicrocreditsForUsage(t *testing.T) {
	got := quotaMicrocreditsForUsage(TokenUsage{
		PromptTokens:     100,
		CachedTokens:     40,
		CompletionTokens: 20,
		TotalTokens:      130,
		ReasoningTokens:  10,
	}, RouteTarget{
		QuotaUncachedInputRate: 1.5,
		QuotaCachedInputRate:   0.5,
		QuotaOutputRate:        3,
	})
	if got != 200 {
		t.Fatalf("quota microcredits = %d, want 200", got)
	}
}
