package buzz

import (
	"testing"
	"time"
)

func TestUsageStatsRollupAndSummary(t *testing.T) {
	store := openTestStore(t)

	loc, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 5, 9, 13, 0, 0, loc)
	records := []UsageRecord{
		{UserID: 1, UserName: "alice", UserAPIKeyID: 10, UserAPIKeyName: "k1", Model: "mimo-v2.5", Status: 200, LatencyMS: 100, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedTokens: 1, RawUsage: `{"prompt_tokens":10}`, CreatedAt: base},
		{UserID: 1, UserName: "alice", UserAPIKeyID: 10, UserAPIKeyName: "k1", Model: "mimo-v2.5", Status: 500, LatencyMS: 300, PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24, CachedTokens: 2, ReasoningTokens: 1, RawUsage: `{"prompt_tokens":20}`, CreatedAt: base.Add(27 * time.Minute)},
		{UserID: 1, UserName: "alice", UserAPIKeyID: 11, UserAPIKeyName: "k2", Model: "mimo-v2.5", Status: 200, LatencyMS: 500, PromptTokens: 30, CompletionTokens: 6, TotalTokens: 36, CachedTokens: 3, ReasoningTokens: 2, RawUsage: `{"prompt_tokens":30}`, CreatedAt: base.Add(57 * time.Minute)},
	}
	if err := store.InsertUsageBatch(records); err != nil {
		t.Fatal(err)
	}

	var hourlyRows int
	if err := store.queryRow(`SELECT COUNT(1) FROM usage_stats_hourly`).Scan(&hourlyRows); err != nil {
		t.Fatal(err)
	}
	if hourlyRows != 2 {
		t.Fatalf("hourly rows = %d, want 2", hourlyRows)
	}

	shortSummary, err := store.UsageSummary(UsageQuery{
		UserID:   1,
		From:     time.Date(2026, 6, 5, 9, 0, 0, 0, loc),
		To:       time.Date(2026, 6, 5, 10, 0, 0, 0, loc),
		Location: loc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if shortSummary.BucketMinutes != 1 || shortSummary.Requests != 2 || shortSummary.Errors != 1 {
		t.Fatalf("short summary = %+v, want 1m logs with 2 requests and 1 error", shortSummary)
	}
	if shortSummary.PromptTokens != 30 || shortSummary.CompletionTokens != 6 || shortSummary.TotalTokens != 36 || shortSummary.CachedTokens != 3 || shortSummary.ReasoningTokens != 1 {
		t.Fatalf("short summary tokens = %+v", shortSummary)
	}

	midSummary, err := store.UsageSummary(UsageQuery{
		UserID:   1,
		From:     time.Date(2026, 6, 5, 9, 0, 0, 0, loc),
		To:       time.Date(2026, 6, 5, 12, 0, 0, 0, loc),
		Location: loc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if midSummary.BucketMinutes != 5 || midSummary.Requests != 3 || midSummary.Errors != 1 {
		t.Fatalf("mid summary = %+v, want 5m logs with 3 requests and 1 error", midSummary)
	}

	longSummary, err := store.UsageSummary(UsageQuery{
		UserID:   1,
		From:     time.Date(2026, 6, 5, 9, 0, 0, 0, loc),
		To:       time.Date(2026, 6, 5, 15, 0, 0, 0, loc),
		Location: loc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if longSummary.BucketMinutes != 60 || longSummary.Requests != 3 || longSummary.Errors != 1 {
		t.Fatalf("long summary = %+v, want hourly stats with 3 requests and 1 error", longSummary)
	}
	if longSummary.PromptTokens != 60 || longSummary.CompletionTokens != 12 || longSummary.TotalTokens != 72 || longSummary.CachedTokens != 6 || longSummary.ReasoningTokens != 3 {
		t.Fatalf("long summary tokens = %+v", longSummary)
	}
	if got := longSummary.ByKey["k1"]; got != 2 {
		t.Fatalf("k1 usage = %d, want 2", got)
	}
	if len(longSummary.Series) != 2 {
		t.Fatalf("series len = %d, want 2: %+v", len(longSummary.Series), longSummary.Series)
	}
	if longSummary.TimeZone != "Asia/Singapore" {
		t.Fatalf("timezone = %q, want Asia/Singapore", longSummary.TimeZone)
	}
	if longSummary.Series[0].Date != "2026-06-05T01:00:00Z" || longSummary.Series[0].Label != "2026/06/05 09:00" || longSummary.Series[0].Requests != 2 || longSummary.Series[0].Errors != 1 {
		t.Fatalf("first hourly point = %+v", longSummary.Series[0])
	}
	if longSummary.Series[0].PromptTokens != 30 || longSummary.Series[0].CompletionTokens != 6 || longSummary.Series[0].TotalTokens != 36 || longSummary.Series[0].CachedTokens != 3 || longSummary.Series[0].ReasoningTokens != 1 {
		t.Fatalf("first hourly point tokens = %+v", longSummary.Series[0])
	}

	fourDaySummary, err := store.UsageSummary(UsageQuery{
		UserID:   1,
		From:     time.Date(2026, 6, 5, 0, 0, 0, 0, loc),
		To:       time.Date(2026, 6, 9, 0, 0, 0, 0, loc),
		Location: loc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fourDaySummary.BucketMinutes != 60 || fourDaySummary.Requests != 3 || fourDaySummary.Errors != 1 {
		t.Fatalf("four day summary = %+v, want hourly stats with 3 requests and 1 error", fourDaySummary)
	}

	dailySummary, err := store.UsageSummary(UsageQuery{
		UserID:   1,
		From:     time.Date(2026, 6, 5, 0, 0, 0, 0, loc),
		To:       time.Date(2026, 6, 10, 0, 0, 0, 0, loc),
		Location: loc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dailySummary.BucketMinutes != 1440 || dailySummary.Requests != 3 || dailySummary.Errors != 1 {
		t.Fatalf("daily summary = %+v, want daily stats with 3 requests and 1 error", dailySummary)
	}
	if dailySummary.PromptTokens != 60 || dailySummary.CompletionTokens != 12 || dailySummary.TotalTokens != 72 || dailySummary.CachedTokens != 6 || dailySummary.ReasoningTokens != 3 {
		t.Fatalf("daily summary tokens = %+v", dailySummary)
	}
	if len(dailySummary.Series) != 1 || dailySummary.Series[0].Date != "2026-06-04T16:00:00Z" || dailySummary.Series[0].Label != "2026/06/05" {
		t.Fatalf("daily series = %+v, want one daily point", dailySummary.Series)
	}
}
