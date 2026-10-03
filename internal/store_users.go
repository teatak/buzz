package buzz

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Store) AuthTokens() (map[string]AuthToken, error) {
	rows, err := s.query(`
		SELECT k.id, k.user_id, u.username, k.name, k.token, k.valid, u.weekly_quota_credits, u.lifetime_quota_credits, u.quota_anchor_at
		FROM user_api_keys k
		JOIN users u ON u.id = k.user_id
		WHERE k.valid = 1 AND u.valid = 1
		ORDER BY k.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]AuthToken)
	for rows.Next() {
		var user AuthToken
		var valid int
		if err := rows.Scan(&user.ID, &user.UserID, &user.UserName, &user.Name, &user.Token, &valid, &user.WeeklyQuotaCredits, &user.LifetimeQuotaCredits, &user.QuotaAnchor); err != nil {
			return nil, err
		}
		user.Valid = valid != 0
		out[user.Token] = user
	}
	return out, rows.Err()
}

func (s *Store) Users() ([]AppUser, error) {
	rows, err := s.query(`SELECT id, username, role, valid, weekly_quota_credits, lifetime_quota_credits, quota_anchor_at, created_at FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AppUser
	for rows.Next() {
		var user AppUser
		var valid int
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &valid, &user.WeeklyQuotaCredits, &user.LifetimeQuotaCredits, &user.QuotaAnchor, &user.CreatedAt); err != nil {
			return nil, err
		}
		user.Valid = valid != 0
		out = append(out, user)
	}
	return out, rows.Err()
}

func (s *Store) User(id int64) (AppUser, error) {
	var user AppUser
	var valid int
	if err := s.queryRow(`SELECT id, username, role, valid, weekly_quota_credits, lifetime_quota_credits, quota_anchor_at, created_at FROM users WHERE id = ?`, id).
		Scan(&user.ID, &user.Username, &user.Role, &valid, &user.WeeklyQuotaCredits, &user.LifetimeQuotaCredits, &user.QuotaAnchor, &user.CreatedAt); err != nil {
		return AppUser{}, err
	}
	user.Valid = valid != 0
	return user, nil
}

func (s *Store) UserAPIKeys() ([]AuthToken, error) {
	rows, err := s.query(`SELECT id, user_id, name, token, valid FROM user_api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuthToken
	for rows.Next() {
		var key AuthToken
		var valid int
		if err := rows.Scan(&key.ID, &key.UserID, &key.Name, &key.Token, &valid); err != nil {
			return nil, err
		}
		key.Valid = valid != 0
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Store) UserAPIKeyDetails(userID int64) ([]UserAPIKeyDetails, error) {
	rows, err := s.query(`
		SELECT k.id, k.user_id, k.name, k.token, k.valid, k.created_at,
			COALESCE(stats.requests, 0), COALESCE(stats.total_tokens, 0), last_used.created_at
		FROM user_api_keys k
		LEFT JOIN (
			SELECT user_api_key_id, SUM(requests) AS requests, SUM(total_tokens_sum) AS total_tokens
			FROM usage_stats_daily
			WHERE user_id = ?
			GROUP BY user_api_key_id
		) stats ON stats.user_api_key_id = k.id
		LEFT JOIN LATERAL (
			SELECT created_at
			FROM usage_logs
			WHERE user_api_key_id = k.id AND user_id = k.user_id
			ORDER BY created_at DESC
			LIMIT 1
		) last_used ON TRUE
		WHERE k.user_id = ?
		ORDER BY k.id`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]UserAPIKeyDetails, 0)
	for rows.Next() {
		var key UserAPIKeyDetails
		var valid int
		var lastUsed sql.NullTime
		if err := rows.Scan(
			&key.ID,
			&key.UserID,
			&key.Name,
			&key.Token,
			&valid,
			&key.CreatedAt,
			&key.Requests,
			&key.TotalTokens,
			&lastUsed,
		); err != nil {
			return nil, err
		}
		key.Valid = valid != 0
		if lastUsed.Valid {
			value := lastUsed.Time
			key.LastUsedAt = &value
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Store) UserAPIKey(id, userID int64) (AuthToken, error) {
	var key AuthToken
	var valid int
	err := s.queryRow(`SELECT id, user_id, name, token, valid FROM user_api_keys WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&key.ID, &key.UserID, &key.Name, &key.Token, &valid)
	if err != nil {
		return AuthToken{}, err
	}
	key.Valid = valid != 0
	return key, nil
}

func (s *Store) CreateAppUser(username, password, role string) (AppUser, error) {
	username = strings.TrimSpace(username)
	role = strings.ToLower(strings.TrimSpace(role))
	if username == "" || password == "" {
		return AppUser{}, errors.New("username and password are required")
	}
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "admin" {
		return AppUser{}, errors.New("role must be user or admin")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return AppUser{}, err
	}
	now := storeNow()
	id, err := s.insertReturningID(`INSERT INTO users (username, password_hash, role, valid, quota_anchor_at, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?)`, username, string(hash), role, now, now, now)
	if err != nil {
		return AppUser{}, err
	}
	return AppUser{ID: id, Username: username, Role: role, Valid: true, QuotaAnchor: now, CreatedAt: now}, nil
}

func (s *Store) UpdateAppUser(actorID, userID int64, role string, valid bool) (AppUser, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if actorID <= 0 || userID <= 0 {
		return AppUser{}, errors.New("actor_id and user_id are required")
	}
	if role != "user" && role != "admin" {
		return AppUser{}, errors.New("role must be user or admin")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return AppUser{}, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`LOCK TABLE users IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return AppUser{}, err
	}

	var current AppUser
	var currentValid int
	if err := tx.QueryRow(s.rebind(`SELECT id, username, role, valid, weekly_quota_credits, lifetime_quota_credits, quota_anchor_at, created_at FROM users WHERE id = ?`), userID).
		Scan(&current.ID, &current.Username, &current.Role, &currentValid, &current.WeeklyQuotaCredits, &current.LifetimeQuotaCredits, &current.QuotaAnchor, &current.CreatedAt); err != nil {
		return AppUser{}, err
	}
	current.Valid = currentValid != 0

	if actorID == userID && (role != current.Role || valid != current.Valid) {
		return AppUser{}, errors.New("cannot change your own role or status")
	}
	if current.Role == "admin" && current.Valid && (role != "admin" || !valid) {
		var activeAdmins int
		if err := tx.QueryRow(`SELECT COUNT(1) FROM users WHERE role = 'admin' AND valid = 1`).Scan(&activeAdmins); err != nil {
			return AppUser{}, err
		}
		if activeAdmins <= 1 {
			return AppUser{}, errors.New("cannot remove the last active administrator")
		}
	}

	if _, err := tx.Exec(
		s.rebind(`UPDATE users SET role = ?, valid = ?, updated_at = ? WHERE id = ?`),
		role,
		boolInt(valid),
		storeNow(),
		userID,
	); err != nil {
		return AppUser{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppUser{}, err
	}
	current.Role = role
	current.Valid = valid
	return current, nil
}

func (s *Store) UpdateUserQuota(userID, weeklyQuotaCredits, lifetimeQuotaCredits int64, at time.Time) (AppUser, error) {
	if userID <= 0 {
		return AppUser{}, errors.New("user_id is required")
	}
	if weeklyQuotaCredits < 0 || weeklyQuotaCredits > maxQuotaCredits {
		return AppUser{}, errors.New("weekly_quota_credits is out of range")
	}
	if lifetimeQuotaCredits < 0 || lifetimeQuotaCredits > maxQuotaCredits {
		return AppUser{}, errors.New("lifetime_quota_credits is out of range")
	}
	current, err := s.User(userID)
	if err != nil {
		return AppUser{}, err
	}
	if at.IsZero() {
		at = storeNow()
	}
	anchor := current.QuotaAnchor
	if current.WeeklyQuotaCredits != weeklyQuotaCredits {
		anchor = at.UTC()
	}
	result, err := s.exec(
		`UPDATE users SET weekly_quota_credits = ?, lifetime_quota_credits = ?, quota_anchor_at = ?, updated_at = ? WHERE id = ?`,
		weeklyQuotaCredits,
		lifetimeQuotaCredits,
		anchor,
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

func (s *Store) CreateInitialAdmin(username, password string) (AppUser, error) {
	if required, err := s.SetupRequired(); err != nil {
		return AppUser{}, err
	} else if !required {
		return AppUser{}, errors.New("setup already completed")
	}
	return s.CreateAppUser(username, password, "admin")
}

func (s *Store) CreateUserAPIKey(key AuthToken) (AuthToken, error) {
	if key.UserID == 0 || key.Token == "" {
		return AuthToken{}, errors.New("user_id and token are required")
	}
	if key.Name == "" {
		key.Name = "user-key-" + randomToken(5)
	}
	now := storeNow()
	id, err := s.insertReturningID(`INSERT INTO user_api_keys (user_id, name, token, valid, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, key.UserID, key.Name, key.Token, boolInt(key.Valid), now, now)
	if err != nil {
		return AuthToken{}, err
	}
	key.ID = id
	return key, nil
}

func (s *Store) SetUserAPIKeyValid(id, userID int64, valid bool) error {
	if id == 0 || userID == 0 {
		return errors.New("id and user_id are required")
	}
	_, err := s.exec(`UPDATE user_api_keys SET valid = ?, updated_at = ? WHERE id = ? AND user_id = ?`, boolInt(valid), storeNow(), id, userID)
	return err
}

func (s *Store) DeleteUserAPIKey(id, userID int64) error {
	if id == 0 || userID == 0 {
		return errors.New("id and user_id are required")
	}
	_, err := s.exec(`DELETE FROM user_api_keys WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

func (s *Store) VerifyPassword(username, password string) (AppUser, error) {
	var user AppUser
	var hash string
	var valid int
	err := s.queryRow(`SELECT id, username, password_hash, role, valid, weekly_quota_credits, lifetime_quota_credits, quota_anchor_at, created_at FROM users WHERE username = ?`, username).
		Scan(&user.ID, &user.Username, &hash, &user.Role, &valid, &user.WeeklyQuotaCredits, &user.LifetimeQuotaCredits, &user.QuotaAnchor, &user.CreatedAt)
	if err != nil {
		return AppUser{}, err
	}
	if valid == 0 {
		return AppUser{}, errors.New("user disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return AppUser{}, err
	}
	user.Valid = true
	return user, nil
}

func (s *Store) ChangePassword(userID int64, currentPassword, nextPassword string) error {
	if userID == 0 || currentPassword == "" || nextPassword == "" {
		return errors.New("current_password and new_password are required")
	}
	var hash string
	if err := s.queryRow(`SELECT password_hash FROM users WHERE id = ? AND valid = 1`, userID).Scan(&hash); err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(currentPassword)); err != nil {
		return errors.New("current password is incorrect")
	}
	nextHash, err := bcrypt.GenerateFromPassword([]byte(nextPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.exec(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, string(nextHash), storeNow(), userID)
	return err
}

func (s *Store) SetupRequired() (bool, error) {
	var count int
	if err := s.queryRow(`SELECT COUNT(1) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}
