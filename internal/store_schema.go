package buzz

func (s *Store) EnsureSchema() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range s.schemaStatements() {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) schemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user',
			valid INTEGER NOT NULL DEFAULT 1,
			weekly_quota_credits BIGINT NOT NULL DEFAULT 0,
			lifetime_quota_credits BIGINT NOT NULL DEFAULT 0,
			lifetime_quota_used_microcredits BIGINT NOT NULL DEFAULT 0,
			quota_anchor_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS weekly_quota_credits BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS lifetime_quota_credits BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS lifetime_quota_used_microcredits BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS quota_anchor_at TIMESTAMPTZ`,
		`UPDATE users SET quota_anchor_at = created_at WHERE quota_anchor_at IS NULL`,
		`ALTER TABLE users ALTER COLUMN quota_anchor_at SET NOT NULL`,
		`CREATE TABLE IF NOT EXISTS user_api_keys (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			name TEXT NOT NULL,
			token TEXT NOT NULL UNIQUE,
			valid INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS providers (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			preset_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE providers DROP COLUMN IF EXISTS type`,
		`ALTER TABLE providers DROP COLUMN IF EXISTS vendor`,
		`ALTER TABLE providers DROP COLUMN IF EXISTS supports_responses`,
		`CREATE TABLE IF NOT EXISTS provider_endpoints (
			id BIGSERIAL PRIMARY KEY,
			provider_id BIGINT NOT NULL,
			protocol TEXT NOT NULL,
			base_url TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(provider_id, protocol),
			FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS provider_keys (
			id BIGSERIAL PRIMARY KEY,
			provider_id BIGINT NOT NULL,
			name TEXT NOT NULL,
			secret TEXT NOT NULL,
			secret_hint TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			priority INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 1,
			labels TEXT NOT NULL DEFAULT '',
			disabled_status INTEGER NOT NULL DEFAULT 0,
			disabled_error_code TEXT NOT NULL DEFAULT '',
			disabled_error_message TEXT NOT NULL DEFAULT '',
			disabled_error_body TEXT NOT NULL DEFAULT '',
			disabled_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(provider_id, name),
			FOREIGN KEY (provider_id) REFERENCES providers(id)
		)`,
		`ALTER TABLE provider_keys DROP COLUMN IF EXISTS provider_account_id`,
		`CREATE TABLE IF NOT EXISTS models (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			icon TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			context_window BIGINT NOT NULL DEFAULT 0,
			max_input_tokens BIGINT NOT NULL DEFAULT 0,
			max_output_tokens BIGINT NOT NULL DEFAULT 0,
			quota_uncached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			quota_cached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			quota_output_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			capabilities TEXT NOT NULL DEFAULT '{}',
			selection_policy TEXT NOT NULL DEFAULT 'round_robin',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS quota_uncached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS quota_cached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1`,
		`ALTER TABLE models ADD COLUMN IF NOT EXISTS quota_output_rate NUMERIC(20,6) NOT NULL DEFAULT 1`,
		`CREATE TABLE IF NOT EXISTS model_routes (
			id BIGSERIAL PRIMARY KEY,
			model_id BIGINT NOT NULL,
			provider_id BIGINT NOT NULL,
			upstream_protocol TEXT NOT NULL DEFAULT 'auto',
			upstream_model TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			priority INTEGER NOT NULL DEFAULT 0,
			weight INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			FOREIGN KEY (model_id) REFERENCES models(id),
			FOREIGN KEY (provider_id) REFERENCES providers(id)
		)`,
		`ALTER TABLE model_routes ADD COLUMN IF NOT EXISTS upstream_protocol TEXT NOT NULL DEFAULT 'auto'`,
		`ALTER TABLE model_routes DROP COLUMN IF EXISTS provider_endpoint_id`,
		`ALTER TABLE model_routes DROP COLUMN IF EXISTS quota_family`,
		`CREATE TABLE IF NOT EXISTS usage_logs (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL DEFAULT 0,
			user_name TEXT NOT NULL DEFAULT '',
			user_api_key_id BIGINT NOT NULL DEFAULT 0,
			user_api_key_name TEXT NOT NULL DEFAULT '',
			provider_id BIGINT NOT NULL DEFAULT 0,
			provider_name TEXT NOT NULL DEFAULT '',
			provider_key_id BIGINT NOT NULL DEFAULT 0,
			provider_key_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			upstream_model TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL DEFAULT 0,
			latency_ms BIGINT NOT NULL DEFAULT 0,
			prompt_tokens BIGINT NOT NULL DEFAULT 0,
			completion_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens BIGINT NOT NULL DEFAULT 0,
			quota_microcredits BIGINT NOT NULL DEFAULT 0,
			raw_usage TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS quota_microcredits BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS user_quota_usage (
			user_id BIGINT NOT NULL,
			period_start TIMESTAMPTZ NOT NULL,
			period_end TIMESTAMPTZ,
			used_microcredits BIGINT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, period_start),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`ALTER TABLE user_quota_usage ALTER COLUMN period_end DROP NOT NULL`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'quota_credits'
			) THEN
				IF EXISTS (
					SELECT 1 FROM information_schema.columns
					WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'quota_cycle'
				) THEN
					UPDATE users SET
						weekly_quota_credits = CASE WHEN quota_cycle = 'weekly' THEN quota_credits ELSE weekly_quota_credits END,
						lifetime_quota_credits = CASE WHEN quota_cycle = 'lifetime' THEN quota_credits ELSE lifetime_quota_credits END;
					UPDATE users u SET lifetime_quota_used_microcredits = GREATEST(
						u.lifetime_quota_used_microcredits,
						COALESCE((
							SELECT q.used_microcredits
							FROM user_quota_usage q
							WHERE q.user_id = u.id AND q.period_start = u.quota_anchor_at
						), 0)
					) WHERE u.quota_cycle = 'lifetime';
				ELSE
					UPDATE users SET weekly_quota_credits = quota_credits;
				END IF;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'extra_quota_credits'
			) THEN
				UPDATE users SET lifetime_quota_credits = lifetime_quota_credits + extra_quota_credits;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'users' AND column_name = 'extra_quota_used_microcredits'
			) THEN
				UPDATE users SET lifetime_quota_used_microcredits = GREATEST(lifetime_quota_used_microcredits, extra_quota_used_microcredits);
			END IF;
		END $$`,
		`ALTER TABLE users DROP COLUMN IF EXISTS quota_credits`,
		`ALTER TABLE users DROP COLUMN IF EXISTS extra_quota_credits`,
		`ALTER TABLE users DROP COLUMN IF EXISTS extra_quota_used_microcredits`,
		`ALTER TABLE users DROP COLUMN IF EXISTS quota_cycle`,
		`CREATE TABLE IF NOT EXISTS usage_stats_hourly (
			bucket_start TIMESTAMPTZ NOT NULL,
			user_id BIGINT NOT NULL DEFAULT 0,
			user_name TEXT NOT NULL DEFAULT '',
			user_api_key_id BIGINT NOT NULL DEFAULT 0,
			user_api_key_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			requests BIGINT NOT NULL DEFAULT 0,
			errors BIGINT NOT NULL DEFAULT 0,
			latency_ms_sum BIGINT NOT NULL DEFAULT 0,
			prompt_tokens_sum BIGINT NOT NULL DEFAULT 0,
			completion_tokens_sum BIGINT NOT NULL DEFAULT 0,
			total_tokens_sum BIGINT NOT NULL DEFAULT 0,
			cached_tokens_sum BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens_sum BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket_start, user_id, user_api_key_id, model)
		)`,
		`CREATE TABLE IF NOT EXISTS usage_stats_daily (
			bucket_start TIMESTAMPTZ NOT NULL,
			user_id BIGINT NOT NULL DEFAULT 0,
			user_name TEXT NOT NULL DEFAULT '',
			user_api_key_id BIGINT NOT NULL DEFAULT 0,
			user_api_key_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			requests BIGINT NOT NULL DEFAULT 0,
			errors BIGINT NOT NULL DEFAULT 0,
			latency_ms_sum BIGINT NOT NULL DEFAULT 0,
			prompt_tokens_sum BIGINT NOT NULL DEFAULT 0,
			completion_tokens_sum BIGINT NOT NULL DEFAULT 0,
			total_tokens_sum BIGINT NOT NULL DEFAULT 0,
			cached_tokens_sum BIGINT NOT NULL DEFAULT 0,
			reasoning_tokens_sum BIGINT NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket_start, user_id, user_api_key_id, model)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_provider_keys_provider_id ON provider_keys(provider_id)`,
		`CREATE INDEX IF NOT EXISTS idx_provider_endpoints_provider_id ON provider_endpoints(provider_id)`,
		`CREATE INDEX IF NOT EXISTS idx_model_routes_model_id ON model_routes(model_id)`,
		`CREATE INDEX IF NOT EXISTS idx_model_routes_provider_id ON model_routes(provider_id)`,
		`CREATE INDEX IF NOT EXISTS idx_model_routes_provider_protocol ON model_routes(provider_id, upstream_protocol)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_logs_user_created ON usage_logs(user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_logs_key_created ON usage_logs(user_api_key_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_user_quota_usage_period ON user_quota_usage(user_id, period_end)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_user_bucket ON usage_stats_hourly(user_id, bucket_start)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_stats_hourly_key_bucket ON usage_stats_hourly(user_api_key_id, bucket_start)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_user_bucket ON usage_stats_daily(user_id, bucket_start)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_stats_daily_key_bucket ON usage_stats_daily(user_api_key_id, bucket_start)`,
		`ALTER TABLE providers DROP COLUMN IF EXISTS base_url`,
		`ALTER TABLE providers DROP COLUMN IF EXISTS protocols`,
	}
}
