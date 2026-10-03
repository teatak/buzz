package buzz

import (
	"database/sql"
	"sort"
	"strings"
	"time"
)

func (s *Store) InsertUsageBatch(records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := s.prepareTx(tx, `
		INSERT INTO usage_logs (
			user_id,
			user_name,
			user_api_key_id,
			user_api_key_name,
			provider_id,
			provider_name,
			provider_key_id,
			provider_key_name,
			model,
			upstream_model,
			status,
			latency_ms,
			prompt_tokens,
			completion_tokens,
			total_tokens,
			cached_tokens,
			reasoning_tokens,
			quota_microcredits,
			raw_usage,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	hourlyStmt, err := s.prepareTx(tx, usageStatsUpsertSQL("usage_stats_hourly"))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer hourlyStmt.Close()
	dailyStmt, err := s.prepareTx(tx, usageStatsUpsertSQL("usage_stats_daily"))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer dailyStmt.Close()
	quotaStmt, err := s.prepareTx(tx, `
		INSERT INTO user_quota_usage (
			user_id,
			period_start,
			period_end,
			used_microcredits,
			updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, period_start) DO UPDATE SET
			period_end = excluded.period_end,
			used_microcredits = user_quota_usage.used_microcredits + excluded.used_microcredits,
			updated_at = excluded.updated_at
		RETURNING used_microcredits`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer quotaStmt.Close()
	lifetimeQuotaStmt, err := s.prepareTx(tx, `
		UPDATE users
		SET lifetime_quota_used_microcredits = lifetime_quota_used_microcredits + ?
		WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer lifetimeQuotaStmt.Close()

	for _, record := range records {
		createdAt := record.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		} else {
			createdAt = createdAt.UTC()
		}
		if _, err := stmt.Exec(
			record.UserID,
			record.UserName,
			record.UserAPIKeyID,
			record.UserAPIKeyName,
			record.ProviderID,
			record.ProviderName,
			record.ProviderKeyID,
			record.ProviderKeyName,
			record.Model,
			record.UpstreamModel,
			record.Status,
			record.LatencyMS,
			record.PromptTokens,
			record.CompletionTokens,
			record.TotalTokens,
			record.CachedTokens,
			record.ReasoningTokens,
			record.QuotaMicrocredits,
			record.RawUsage,
			createdAt,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertUsageStats(hourlyStmt, record, usageHourBucket(createdAt)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := insertUsageStats(dailyStmt, record, usageDayBucket(createdAt)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if record.UserID > 0 && record.QuotaMicrocredits > 0 && !record.QuotaPeriodStart.IsZero() {
			var periodEnd any
			if record.QuotaPeriodEnd != nil {
				periodEnd = record.QuotaPeriodEnd.UTC()
			}
			var usedMicrocredits int64
			if err := quotaStmt.QueryRow(
				record.UserID,
				record.QuotaPeriodStart.UTC(),
				periodEnd,
				record.QuotaMicrocredits,
				createdAt,
			).Scan(&usedMicrocredits); err != nil {
				_ = tx.Rollback()
				return err
			}
			if record.UsesLifetimeQuota {
				previousUsed := usedMicrocredits - record.QuotaMicrocredits
				lifetimeUsed := quotaOverage(usedMicrocredits, record.WeeklyQuotaLimitMicrocredits) - quotaOverage(previousUsed, record.WeeklyQuotaLimitMicrocredits)
				if lifetimeUsed > 0 {
					if _, err := lifetimeQuotaStmt.Exec(lifetimeUsed, record.UserID); err != nil {
						_ = tx.Rollback()
						return err
					}
				}
			}
		}
	}
	return tx.Commit()
}

func quotaOverage(used, limit int64) int64 {
	if used <= limit {
		return 0
	}
	return used - limit
}

func usageStatsUpsertSQL(table string) string {
	return `
		INSERT INTO ` + table + ` (
			bucket_start,
			user_id,
			user_name,
			user_api_key_id,
			user_api_key_name,
			model,
			requests,
			errors,
			latency_ms_sum,
			prompt_tokens_sum,
			completion_tokens_sum,
			total_tokens_sum,
			cached_tokens_sum,
			reasoning_tokens_sum
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, user_id, user_api_key_id, model) DO UPDATE SET
			user_name = excluded.user_name,
			user_api_key_name = excluded.user_api_key_name,
			requests = ` + table + `.requests + excluded.requests,
			errors = ` + table + `.errors + excluded.errors,
			latency_ms_sum = ` + table + `.latency_ms_sum + excluded.latency_ms_sum,
			prompt_tokens_sum = ` + table + `.prompt_tokens_sum + excluded.prompt_tokens_sum,
			completion_tokens_sum = ` + table + `.completion_tokens_sum + excluded.completion_tokens_sum,
			total_tokens_sum = ` + table + `.total_tokens_sum + excluded.total_tokens_sum,
			cached_tokens_sum = ` + table + `.cached_tokens_sum + excluded.cached_tokens_sum,
			reasoning_tokens_sum = ` + table + `.reasoning_tokens_sum + excluded.reasoning_tokens_sum`
}

func insertUsageStats(stmt *sql.Stmt, record UsageRecord, bucketStart time.Time) error {
	errors := 0
	if record.Status >= 400 {
		errors = 1
	}
	_, err := stmt.Exec(
		bucketStart,
		record.UserID,
		record.UserName,
		record.UserAPIKeyID,
		record.UserAPIKeyName,
		record.Model,
		1,
		errors,
		record.LatencyMS,
		record.PromptTokens,
		record.CompletionTokens,
		record.TotalTokens,
		record.CachedTokens,
		record.ReasoningTokens,
	)
	return err
}

func (s *Store) UsageSummary(query UsageQuery) (UsageSummary, error) {
	if table, bucketMinutes, ok := usageStatsSource(query); ok {
		return s.usageSummaryFromStats(query, table, bucketMinutes)
	}
	loc := usageQueryLocation(query)
	where, args := usageWhere(query)
	var summary UsageSummary
	var errors sql.NullInt64
	var avg sql.NullFloat64
	var promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens sql.NullInt64
	if err := s.queryRow(`
		SELECT
			COUNT(1),
			COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0),
			COALESCE(SUM(prompt_tokens), 0),
			COALESCE(SUM(completion_tokens), 0),
			COALESCE(SUM(total_tokens), 0),
			COALESCE(SUM(cached_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0)
		FROM usage_logs
		WHERE `+where,
		args...,
	).Scan(&summary.Requests, &errors, &avg, &promptTokens, &completionTokens, &totalTokens, &cachedTokens, &reasoningTokens); err != nil {
		return UsageSummary{}, err
	}
	summary.Errors = errors.Int64
	summary.AvgLatencyMS = avg.Float64
	summary.PromptTokens = promptTokens.Int64
	summary.CompletionTokens = completionTokens.Int64
	summary.TotalTokens = totalTokens.Int64
	summary.CachedTokens = cachedTokens.Int64
	summary.ReasoningTokens = reasoningTokens.Int64
	summary.TimeZone = loc.String()

	byKey, err := s.usageByKey(where, args)
	if err != nil {
		return UsageSummary{}, err
	}
	summary.ByKey = byKey

	summary.BucketMinutes = usageBucketMinutes(query.From, query.To)
	series, err := s.usageSeries(where, args, summary.BucketMinutes, loc)
	if err != nil {
		return UsageSummary{}, err
	}
	summary.Series = series
	return summary, nil
}

func (s *Store) usageSummaryFromStats(query UsageQuery, table string, bucketMinutes int) (UsageSummary, error) {
	loc := usageQueryLocation(query)
	where, args := usageStatsWhere(query, bucketMinutes)
	var summary UsageSummary
	var requests sql.NullInt64
	var errors sql.NullInt64
	var latencySum sql.NullInt64
	var promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens sql.NullInt64
	if err := s.queryRow(`
		SELECT
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(errors), 0),
			COALESCE(SUM(latency_ms_sum), 0),
			COALESCE(SUM(prompt_tokens_sum), 0),
			COALESCE(SUM(completion_tokens_sum), 0),
			COALESCE(SUM(total_tokens_sum), 0),
			COALESCE(SUM(cached_tokens_sum), 0),
			COALESCE(SUM(reasoning_tokens_sum), 0)
		FROM `+table+`
		WHERE `+where,
		args...,
	).Scan(&requests, &errors, &latencySum, &promptTokens, &completionTokens, &totalTokens, &cachedTokens, &reasoningTokens); err != nil {
		return UsageSummary{}, err
	}
	summary.Requests = requests.Int64
	summary.Errors = errors.Int64
	summary.PromptTokens = promptTokens.Int64
	summary.CompletionTokens = completionTokens.Int64
	summary.TotalTokens = totalTokens.Int64
	summary.CachedTokens = cachedTokens.Int64
	summary.ReasoningTokens = reasoningTokens.Int64
	if summary.Requests > 0 {
		summary.AvgLatencyMS = float64(latencySum.Int64) / float64(summary.Requests)
	}
	summary.TimeZone = loc.String()
	byKey, err := s.usageByKeyFromStats(table, where, args)
	if err != nil {
		return UsageSummary{}, err
	}
	summary.ByKey = byKey
	series, err := s.usageSeriesFromStats(table, where, args, bucketMinutes, loc)
	if err != nil {
		return UsageSummary{}, err
	}
	summary.Series = series
	summary.BucketMinutes = bucketMinutes
	return summary, nil
}

func (s *Store) usageByKey(where string, args []any) (map[string]int64, error) {
	rows, err := s.query(`
		SELECT user_api_key_name, COUNT(1)
		FROM usage_logs
		WHERE `+where+`
		GROUP BY user_api_key_name
		ORDER BY COUNT(1) DESC, user_api_key_name`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = "unknown"
		}
		out[name] = count
	}
	return out, rows.Err()
}

func (s *Store) usageByKeyFromStats(table, where string, args []any) (map[string]int64, error) {
	rows, err := s.query(`
		SELECT user_api_key_name, COALESCE(SUM(requests), 0)
		FROM `+table+`
		WHERE `+where+`
		GROUP BY user_api_key_name
		ORDER BY COALESCE(SUM(requests), 0) DESC, user_api_key_name`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var name string
		var count int64
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = "unknown"
		}
		out[name] = count
	}
	return out, rows.Err()
}

func (s *Store) usageSeriesFromStats(table, where string, args []any, bucketMinutes int, loc *time.Location) ([]UsagePoint, error) {
	rows, err := s.query(`
		SELECT
			bucket_start,
			COALESCE(SUM(requests), 0),
			COALESCE(SUM(errors), 0),
			COALESCE(SUM(latency_ms_sum), 0),
			COALESCE(SUM(prompt_tokens_sum), 0),
			COALESCE(SUM(completion_tokens_sum), 0),
			COALESCE(SUM(total_tokens_sum), 0),
			COALESCE(SUM(cached_tokens_sum), 0),
			COALESCE(SUM(reasoning_tokens_sum), 0)
		FROM `+table+`
		WHERE `+where+`
		GROUP BY bucket_start
		ORDER BY bucket_start`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make(map[string]usageBucketAgg)
	for rows.Next() {
		var bucketStart time.Time
		var latencySum int64
		var agg usageBucketAgg
		if err := rows.Scan(
			&bucketStart,
			&agg.requests,
			&agg.errors,
			&latencySum,
			&agg.promptTokens,
			&agg.completionTokens,
			&agg.totalTokens,
			&agg.cachedTokens,
			&agg.reasoningTokens,
		); err != nil {
			return nil, err
		}
		agg.latencySum = latencySum
		key := formatUsageBucket(floorUsageBucketInLocation(bucketStart, bucketMinutes, loc))
		existing := buckets[key]
		addUsageAgg(&existing, agg)
		buckets[key] = existing
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return usagePointsFromBuckets(buckets, bucketMinutes, loc)
}

func (s *Store) usageSeries(where string, args []any, bucketMinutes int, loc *time.Location) ([]UsagePoint, error) {
	rows, err := s.query(`
		SELECT
			created_at,
			status,
			latency_ms,
			prompt_tokens,
			completion_tokens,
			total_tokens,
			cached_tokens,
			reasoning_tokens
		FROM usage_logs
		WHERE `+where+`
		ORDER BY created_at`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	buckets := make(map[string]usageBucketAgg)
	for rows.Next() {
		var createdAt time.Time
		var status int
		var latency int64
		var promptTokens, completionTokens, totalTokens, cachedTokens, reasoningTokens int64
		if err := rows.Scan(&createdAt, &status, &latency, &promptTokens, &completionTokens, &totalTokens, &cachedTokens, &reasoningTokens); err != nil {
			return nil, err
		}
		key := formatUsageBucket(floorUsageBucketInLocation(createdAt, bucketMinutes, loc))
		agg := buckets[key]
		agg.requests++
		if status >= 400 {
			agg.errors++
		}
		agg.latencySum += latency
		agg.promptTokens += promptTokens
		agg.completionTokens += completionTokens
		agg.totalTokens += totalTokens
		agg.cachedTokens += cachedTokens
		agg.reasoningTokens += reasoningTokens
		buckets[key] = agg
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return usagePointsFromBuckets(buckets, bucketMinutes, loc)
}

func usageBucketMinutes(from, to time.Time) int {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return 1
	}
	span := to.Sub(from)
	switch {
	case span <= time.Hour:
		return 1
	default:
		return 5
	}
}

func usageStatsSource(query UsageQuery) (string, int, bool) {
	if query.From.IsZero() || query.To.IsZero() || !query.From.Before(query.To) {
		return "", 0, false
	}
	span := query.To.Sub(query.From)
	if span <= 5*time.Hour {
		return "", 0, false
	}
	if span <= 4*24*time.Hour {
		return "usage_stats_hourly", 60, true
	}
	return "usage_stats_daily", 1440, true
}

func usageStatsWhere(query UsageQuery, bucketMinutes int) (string, []any) {
	clauses := []string{"user_id = ?"}
	args := []any{query.UserID}
	if query.UserAPIKeyID != 0 {
		clauses = append(clauses, "user_api_key_id = ?")
		args = append(args, query.UserAPIKeyID)
	}
	if strings.TrimSpace(query.Model) != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, strings.TrimSpace(query.Model))
	}
	if !query.From.IsZero() {
		clauses = append(clauses, "bucket_start >= ?")
		args = append(args, floorUsageBucketInLocation(query.From, 60, time.UTC))
	}
	if !query.To.IsZero() {
		clauses = append(clauses, "bucket_start < ?")
		args = append(args, ceilUsageBucketInLocation(query.To, 60, time.UTC))
	}
	return strings.Join(clauses, " AND "), args
}

type usageBucketAgg struct {
	requests         int64
	errors           int64
	latencySum       int64
	promptTokens     int64
	completionTokens int64
	totalTokens      int64
	cachedTokens     int64
	reasoningTokens  int64
}

func usageQueryLocation(query UsageQuery) *time.Location {
	if query.Location != nil {
		return query.Location
	}
	return time.UTC
}

func addUsageAgg(dst *usageBucketAgg, src usageBucketAgg) {
	dst.requests += src.requests
	dst.errors += src.errors
	dst.latencySum += src.latencySum
	dst.promptTokens += src.promptTokens
	dst.completionTokens += src.completionTokens
	dst.totalTokens += src.totalTokens
	dst.cachedTokens += src.cachedTokens
	dst.reasoningTokens += src.reasoningTokens
}

func floorUsageBucketInLocation(value time.Time, bucketMinutes int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := value.In(loc)
	if bucketMinutes >= 1440 {
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
	}
	step := bucketMinutes
	if step < 1 {
		step = 1
	}
	local = local.Truncate(time.Minute)
	local = local.Add(-time.Duration(local.Minute()%step) * time.Minute)
	return local.UTC()
}

func ceilUsageBucketInLocation(value time.Time, bucketMinutes int, loc *time.Location) time.Time {
	floor := floorUsageBucketInLocation(value, bucketMinutes, loc)
	if value.UTC().Equal(floor) {
		return floor
	}
	step := bucketMinutes
	if step < 1 {
		step = 1
	}
	return floor.Add(time.Duration(step) * time.Minute)
}

func usagePointsFromBuckets(buckets map[string]usageBucketAgg, bucketMinutes int, loc *time.Location) ([]UsagePoint, error) {
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]UsagePoint, 0, len(keys))
	for _, key := range keys {
		bucket, err := parseStoredUsageTime(key)
		if err != nil {
			continue
		}
		out = append(out, usagePointFromBucket(bucket, bucketMinutes, loc, buckets[key]))
	}
	return out, nil
}

func usagePointFromBucket(bucket time.Time, bucketMinutes int, loc *time.Location, agg usageBucketAgg) UsagePoint {
	point := UsagePoint{
		Date:             formatUsageBucket(bucket),
		Label:            formatUsageLabel(bucket, bucketMinutes, loc),
		Tooltip:          formatUsageTooltip(bucket, bucketMinutes, loc),
		Requests:         agg.requests,
		Errors:           agg.errors,
		PromptTokens:     agg.promptTokens,
		CompletionTokens: agg.completionTokens,
		TotalTokens:      agg.totalTokens,
		CachedTokens:     agg.cachedTokens,
		ReasoningTokens:  agg.reasoningTokens,
	}
	if agg.requests > 0 {
		point.AvgLatencyMS = float64(agg.latencySum) / float64(agg.requests)
	}
	return point
}

func formatUsageLabel(bucket time.Time, bucketMinutes int, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	local := bucket.In(loc)
	if bucketMinutes >= 1440 {
		return local.Format("2006/01/02")
	}
	return local.Format("2006/01/02 15:04")
}

func formatUsageTooltip(bucket time.Time, bucketMinutes int, loc *time.Location) string {
	return formatUsageLabel(bucket, bucketMinutes, loc)
}

func formatUsageBucket(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatStoredUsageTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func usageHourBucket(value time.Time) time.Time {
	return floorUsageBucketInLocation(value, 60, time.UTC)
}

func usageDayBucket(value time.Time) time.Time {
	utc := value.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func parseStoredUsageTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func usageWhere(query UsageQuery) (string, []any) {
	clauses := []string{"user_id = ?"}
	args := []any{query.UserID}
	if query.UserAPIKeyID != 0 {
		clauses = append(clauses, "user_api_key_id = ?")
		args = append(args, query.UserAPIKeyID)
	}
	if strings.TrimSpace(query.Model) != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, strings.TrimSpace(query.Model))
	}
	if !query.From.IsZero() {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, query.From.UTC())
	}
	if !query.To.IsZero() {
		clauses = append(clauses, "created_at < ?")
		args = append(args, query.To.UTC())
	}
	return strings.Join(clauses, " AND "), args
}
