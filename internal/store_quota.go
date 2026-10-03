package buzz

import (
	"database/sql"
	"errors"
	"math"
	"time"
)

const (
	creditMicrocredits int64 = 1_000_000
	maxQuotaCredits          = math.MaxInt64 / creditMicrocredits
)

func (s *Store) UserQuotaStatus(userID int64, at time.Time) (UserQuotaStatus, error) {
	var weeklyQuotaCredits int64
	var lifetimeQuotaCredits int64
	var lifetimeUsedMicrocredits int64
	var anchor time.Time
	if err := s.queryRow(
		`SELECT weekly_quota_credits, lifetime_quota_credits, lifetime_quota_used_microcredits, quota_anchor_at FROM users WHERE id = ?`,
		userID,
	).Scan(&weeklyQuotaCredits, &lifetimeQuotaCredits, &lifetimeUsedMicrocredits, &anchor); err != nil {
		return UserQuotaStatus{}, err
	}

	periodStart, periodEnd := weeklyQuotaPeriod(anchor, at)
	var periodUsedMicrocredits int64
	err := s.queryRow(
		`SELECT used_microcredits FROM user_quota_usage WHERE user_id = ? AND period_start = ?`,
		userID,
		periodStart,
	).Scan(&periodUsedMicrocredits)
	if err != nil && err != sql.ErrNoRows {
		return UserQuotaStatus{}, err
	}

	weeklyLimitMicrocredits := weeklyQuotaCredits * creditMicrocredits
	weeklyUsedMicrocredits := min(periodUsedMicrocredits, weeklyLimitMicrocredits)
	weeklyRemainingMicrocredits := weeklyLimitMicrocredits - weeklyUsedMicrocredits
	lifetimeLimitMicrocredits := lifetimeQuotaCredits * creditMicrocredits
	lifetimeRemainingMicrocredits := lifetimeLimitMicrocredits - lifetimeUsedMicrocredits
	if lifetimeRemainingMicrocredits < 0 {
		lifetimeRemainingMicrocredits = 0
	}
	return UserQuotaStatus{
		WeeklyQuotaCredits:            weeklyQuotaCredits,
		WeeklyUsedMicrocredits:        weeklyUsedMicrocredits,
		WeeklyRemainingMicrocredits:   weeklyRemainingMicrocredits,
		LifetimeQuotaCredits:          lifetimeQuotaCredits,
		LifetimeUsedMicrocredits:      lifetimeUsedMicrocredits,
		LifetimeRemainingMicrocredits: lifetimeRemainingMicrocredits,
		PeriodStart:                   periodStart,
		PeriodEnd:                     periodEnd,
		Unlimited:                     weeklyQuotaCredits == 0 && lifetimeQuotaCredits == 0,
	}, nil
}

func weeklyQuotaPeriod(anchor, at time.Time) (time.Time, time.Time) {
	anchor = anchor.UTC()
	at = at.UTC()
	const period = 7 * 24 * time.Hour
	if at.Before(anchor) {
		return anchor, anchor.Add(period)
	}
	start := anchor.Add(time.Duration(at.Sub(anchor)/period) * period)
	return start, start.Add(period)
}

func (s *Store) ResetUserQuota(userID int64, at time.Time) (AppUser, error) {
	if userID <= 0 {
		return AppUser{}, errors.New("user_id is required")
	}
	if at.IsZero() {
		at = storeNow()
	}
	result, err := s.exec(
		`UPDATE users SET quota_anchor_at = ?, updated_at = ? WHERE id = ?`,
		at.UTC(),
		storeNow(),
		userID,
	)
	if err != nil {
		return AppUser{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return AppUser{}, err
	} else if affected == 0 {
		return AppUser{}, sql.ErrNoRows
	}
	return s.User(userID)
}

func (s *Store) ResetWeeklyQuotas(at time.Time) (int64, error) {
	if at.IsZero() {
		at = storeNow()
	}
	result, err := s.exec(
		`UPDATE users SET quota_anchor_at = ?, updated_at = ? WHERE weekly_quota_credits > 0`,
		at.UTC(),
		storeNow(),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
