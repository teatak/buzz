-- Dev/test token usage seed for PostgreSQL.
-- Run from the project root:
--   docker compose exec -T postgres psql -U buzz -d buzz < scripts/seed-token-usage.postgres.sql
--
-- This script is idempotent for its own demo rows. It removes rows marked with
-- "token_usage_demo" and rebuilds usage aggregate tables from usage_logs.

BEGIN;

DELETE FROM usage_logs
WHERE raw_usage LIKE '%"seed":"token_usage_demo"%';

WITH
demo_user AS (
	SELECT
		COALESCE(
			(SELECT id FROM users WHERE role = 'admin' ORDER BY id LIMIT 1),
			(SELECT id FROM users ORDER BY id LIMIT 1),
			1
		) AS user_id,
		COALESCE(
			(SELECT username FROM users WHERE role = 'admin' ORDER BY id LIMIT 1),
			(SELECT username FROM users ORDER BY id LIMIT 1),
			'admin'
		) AS user_name
),
demo_key AS (
	SELECT
		COALESCE(
			(SELECT id FROM user_api_keys WHERE user_id = (SELECT user_id FROM demo_user) ORDER BY id LIMIT 1),
			0
		) AS user_api_key_id,
		COALESCE(
			(SELECT name FROM user_api_keys WHERE user_id = (SELECT user_id FROM demo_user) ORDER BY id LIMIT 1),
			'demo-key'
		) AS user_api_key_name
),
demo_provider AS (
	SELECT
		COALESCE((SELECT id FROM providers WHERE enabled = 1 ORDER BY id LIMIT 1), 0) AS provider_id,
		COALESCE((SELECT name FROM providers WHERE enabled = 1 ORDER BY id LIMIT 1), 'demo-provider') AS provider_name
),
demo_provider_key AS (
	SELECT
		COALESCE((SELECT id FROM provider_keys WHERE enabled = 1 ORDER BY id LIMIT 1), 0) AS provider_key_id,
		COALESCE((SELECT name FROM provider_keys WHERE enabled = 1 ORDER BY id LIMIT 1), 'demo-provider-key') AS provider_key_name
),
demo_points AS (
	SELECT
		n,
		CASE n % 4
			WHEN 0 THEN 'gemini-3.5-flash'
			WHEN 1 THEN 'gpt-5.5'
			WHEN 2 THEN 'mimo-v2.5'
			ELSE 'deepseek-v4-flash'
		END AS model,
		CASE WHEN n % 17 = 0 THEN 429 ELSE 200 END AS status,
		800 + ((n * 137) % 3600) AS prompt_tokens,
		CASE WHEN n % 3 = 0 THEN 180 + ((n * 41) % 1200) ELSE 0 END AS cached_tokens,
		260 + ((n * 73) % 1400) AS output_text_tokens,
		CASE WHEN n % 5 = 0 THEN 120 + ((n * 29) % 600) ELSE 0 END AS reasoning_tokens,
		120 + ((n * 19) % 900) AS latency_ms,
		(now() AT TIME ZONE 'UTC') - ((47 - n) * INTERVAL '30 minutes') AS created_at
	FROM generate_series(0, 47) AS n
)
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
	raw_usage,
	created_at
)
SELECT
	u.user_id,
	u.user_name,
	k.user_api_key_id,
	k.user_api_key_name,
	p.provider_id,
	p.provider_name,
	pk.provider_key_id,
	pk.provider_key_name,
	point.model,
	point.model,
	point.status,
	point.latency_ms,
	CASE WHEN point.status >= 400 THEN 0 ELSE point.prompt_tokens END,
	CASE WHEN point.status >= 400 THEN 0 ELSE point.output_text_tokens + point.reasoning_tokens END,
	CASE WHEN point.status >= 400 THEN 0 ELSE point.prompt_tokens + point.output_text_tokens + point.reasoning_tokens END,
	CASE WHEN point.status >= 400 THEN 0 ELSE point.cached_tokens END,
	CASE WHEN point.status >= 400 THEN 0 ELSE point.reasoning_tokens END,
	'{"seed":"token_usage_demo"}',
	to_char(point.created_at, 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
FROM demo_points point
CROSS JOIN demo_user u
CROSS JOIN demo_key k
CROSS JOIN demo_provider p
CROSS JOIN demo_provider_key pk;

TRUNCATE TABLE usage_stats_hourly, usage_stats_daily;

INSERT INTO usage_stats_hourly (
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
)
SELECT
	to_char(date_trunc('hour', created_at::timestamptz AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	user_id,
	user_name,
	user_api_key_id,
	user_api_key_name,
	model,
	COUNT(1),
	COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(latency_ms), 0),
	COALESCE(SUM(prompt_tokens), 0),
	COALESCE(SUM(completion_tokens), 0),
	COALESCE(SUM(total_tokens), 0),
	COALESCE(SUM(cached_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0)
FROM usage_logs
GROUP BY 1, user_id, user_name, user_api_key_id, user_api_key_name, model;

INSERT INTO usage_stats_daily (
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
)
SELECT
	to_char(date_trunc('day', created_at::timestamptz AT TIME ZONE 'UTC'), 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	user_id,
	user_name,
	user_api_key_id,
	user_api_key_name,
	model,
	COUNT(1),
	COALESCE(SUM(CASE WHEN status >= 400 THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(latency_ms), 0),
	COALESCE(SUM(prompt_tokens), 0),
	COALESCE(SUM(completion_tokens), 0),
	COALESCE(SUM(total_tokens), 0),
	COALESCE(SUM(cached_tokens), 0),
	COALESCE(SUM(reasoning_tokens), 0)
FROM usage_logs
GROUP BY 1, user_id, user_name, user_api_key_id, user_api_key_name, model;

COMMIT;
