package buzz

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var errRuntimeCacheMiss = errors.New("runtime cache miss")

type RuntimeCache struct {
	client *redis.Client
}

type adminSessionCacheValue struct {
	User      AppUser `json:"user"`
	IssuedAt  string  `json:"issued_at,omitempty"`
	ExpiresAt string  `json:"expires_at"`
}

type routeSessionCacheValue struct {
	ModelRouteID int64  `json:"model_route_id"`
	ExpiresAt    string `json:"expires_at"`
}

type keyCooldownCacheValue struct {
	ExpiresAt string `json:"expires_at"`
	RPDLike   bool   `json:"rpd_like"`
	Hits      int    `json:"hits"`
}

func OpenRuntimeCache(cfg RedisConfig) (*RuntimeCache, error) {
	if cfg.URL == "" && cfg.Addr == "" {
		return nil, nil
	}

	var (
		opts *redis.Options
		err  error
	)
	if cfg.URL != "" {
		opts, err = redis.ParseURL(cfg.URL)
		if err != nil {
			return nil, err
		}
	} else {
		opts = &redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       cfg.DB,
		}
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return &RuntimeCache{client: client}, nil
}

func (c *RuntimeCache) Enabled() bool {
	return c != nil && c.client != nil
}

func (c *RuntimeCache) Close() error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Close()
}

func (c *RuntimeCache) AdminSession(ctx context.Context, token string) (SessionUser, error) {
	if !c.Enabled() {
		return SessionUser{}, errRuntimeCacheMiss
	}
	raw, err := c.client.Get(ctx, adminSessionKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return SessionUser{}, errRuntimeCacheMiss
	}
	if err != nil {
		return SessionUser{}, err
	}

	var value adminSessionCacheValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return SessionUser{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, value.ExpiresAt)
	if err != nil {
		return SessionUser{}, err
	}
	if !value.User.Valid || time.Now().After(expiresAt) {
		_ = c.DeleteAdminSession(ctx, token)
		return SessionUser{}, errRuntimeCacheMiss
	}
	legacySession := value.IssuedAt == ""
	issuedAt := expiresAt.Add(-adminSessionTTL)
	if !legacySession {
		issuedAt, err = time.Parse(time.RFC3339Nano, value.IssuedAt)
		if err != nil {
			return SessionUser{}, err
		}
	}
	if revokedAt, err := c.adminSessionsRevokedAt(ctx, value.User.ID); err == nil && !issuedAt.After(revokedAt) {
		_ = c.DeleteAdminSession(ctx, token)
		return SessionUser{}, errRuntimeCacheMiss
	} else if err != nil && !errors.Is(err, errRuntimeCacheMiss) {
		return SessionUser{}, err
	}

	// Sessions written before per-user indexes existed are registered once on read.
	if legacySession {
		hash := sessionHash(token)
		pipe := c.client.Pipeline()
		pipe.SAdd(ctx, adminUserSessionsKey(value.User.ID), hash)
		pipe.Expire(ctx, adminUserSessionsKey(value.User.ID), adminSessionTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return SessionUser{}, err
		}
	}
	return SessionUser{User: value.User, IssuedAt: issuedAt, ExpiresAt: expiresAt}, nil
}

func (c *RuntimeCache) SetAdminSession(ctx context.Context, token string, sessionUser SessionUser) error {
	if !c.Enabled() {
		return nil
	}
	ttl := time.Until(sessionUser.ExpiresAt)
	if ttl <= 0 {
		return c.DeleteAdminSession(ctx, token)
	}
	if sessionUser.IssuedAt.IsZero() {
		sessionUser.IssuedAt = time.Now()
	}
	raw, err := json.Marshal(adminSessionCacheValue{
		User:      sessionUser.User,
		IssuedAt:  sessionUser.IssuedAt.Format(time.RFC3339Nano),
		ExpiresAt: sessionUser.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	hash := sessionHash(token)
	pipe := c.client.TxPipeline()
	pipe.Set(ctx, adminSessionKeyFromHash(hash), raw, ttl)
	pipe.SAdd(ctx, adminUserSessionsKey(sessionUser.User.ID), hash)
	pipe.Expire(ctx, adminUserSessionsKey(sessionUser.User.ID), adminSessionTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *RuntimeCache) DeleteAdminSession(ctx context.Context, token string) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Del(ctx, adminSessionKey(token)).Err()
}

func adminSessionKey(token string) string {
	return adminSessionKeyFromHash(sessionHash(token))
}

func adminSessionKeyFromHash(hash string) string {
	return "buzz:admin-session:" + hash
}

func adminUserSessionsKey(userID int64) string {
	return "buzz:admin-user-sessions:" + strconv.FormatInt(userID, 10)
}

func adminSessionsRevokedKey(userID int64) string {
	return "buzz:admin-sessions-revoked:" + strconv.FormatInt(userID, 10)
}

func (c *RuntimeCache) adminSessionsRevokedAt(ctx context.Context, userID int64) (time.Time, error) {
	value, err := c.client.Get(ctx, adminSessionsRevokedKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, errRuntimeCacheMiss
	}
	if err != nil {
		return time.Time{}, err
	}
	revokedAt, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return revokedAt, nil
}

func (c *RuntimeCache) RevokeAdminSessions(ctx context.Context, userID int64) error {
	if !c.Enabled() {
		return nil
	}
	hashes, err := c.client.SMembers(ctx, adminUserSessionsKey(userID)).Result()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(hashes)+1)
	for _, hash := range hashes {
		keys = append(keys, adminSessionKeyFromHash(hash))
	}
	keys = append(keys, adminUserSessionsKey(userID))

	pipe := c.client.TxPipeline()
	pipe.Del(ctx, keys...)
	pipe.Set(ctx, adminSessionsRevokedKey(userID), time.Now().UTC().Format(time.RFC3339Nano), adminSessionTTL)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *RuntimeCache) RouteSession(ctx context.Context, key string) (RouteSession, error) {
	if !c.Enabled() {
		return RouteSession{}, errRuntimeCacheMiss
	}
	raw, err := c.client.Get(ctx, routeSessionKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return RouteSession{}, errRuntimeCacheMiss
	}
	if err != nil {
		return RouteSession{}, err
	}

	var value routeSessionCacheValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return RouteSession{}, err
	}
	expiresAt, err := time.Parse(time.RFC3339, value.ExpiresAt)
	if err != nil {
		return RouteSession{}, err
	}
	if time.Now().After(expiresAt) {
		_ = c.DeleteRouteSession(ctx, key)
		return RouteSession{}, errRuntimeCacheMiss
	}
	return RouteSession{
		ModelRouteID: value.ModelRouteID,
		ExpiresAt:    expiresAt,
	}, nil
}

func (c *RuntimeCache) SetRouteSession(ctx context.Context, key string, session RouteSession) error {
	if !c.Enabled() {
		return nil
	}
	ttl := time.Until(session.ExpiresAt)
	if ttl <= 0 {
		return c.DeleteRouteSession(ctx, key)
	}
	raw, err := json.Marshal(routeSessionCacheValue{
		ModelRouteID: session.ModelRouteID,
		ExpiresAt:    session.ExpiresAt.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, routeSessionKey(key), raw, ttl).Err()
}

func (c *RuntimeCache) DeleteRouteSession(ctx context.Context, key string) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Del(ctx, routeSessionKey(key)).Err()
}

func routeSessionKey(key string) string {
	return "buzz:route-session:" + sessionHash(key)
}

func (c *RuntimeCache) ToolSignature(ctx context.Context, key string) (string, error) {
	if !c.Enabled() {
		return "", errRuntimeCacheMiss
	}
	signature, err := c.client.Get(ctx, toolSignatureCacheKey(key)).Result()
	if errors.Is(err, redis.Nil) {
		return "", errRuntimeCacheMiss
	}
	if err != nil {
		return "", err
	}
	return signature, nil
}

func (c *RuntimeCache) SetToolSignature(ctx context.Context, key, signature string, ttl time.Duration) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Set(ctx, toolSignatureCacheKey(key), signature, ttl).Err()
}

func toolSignatureCacheKey(key string) string {
	return "buzz:tool-signature:" + sessionHash(key)
}

func (c *RuntimeCache) KeyCooldown(ctx context.Context, key string) (time.Time, bool, error) {
	if !c.Enabled() {
		return time.Time{}, false, errRuntimeCacheMiss
	}
	raw, err := c.client.Get(ctx, keyCooldownKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, false, errRuntimeCacheMiss
	}
	if err != nil {
		return time.Time{}, false, err
	}

	var value keyCooldownCacheValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}, false, err
	}
	expiresAt, err := time.Parse(time.RFC3339, value.ExpiresAt)
	if err != nil {
		return time.Time{}, false, err
	}
	if time.Now().After(expiresAt) {
		return time.Time{}, false, errRuntimeCacheMiss
	}
	return expiresAt, value.RPDLike, nil
}

func (c *RuntimeCache) MarkKeyExhausted(ctx context.Context, key string, cooldown, rpdCooldown time.Duration) (time.Time, bool, error) {
	if !c.Enabled() {
		return time.Time{}, false, errRuntimeCacheMiss
	}
	if cooldown <= 0 {
		cooldown = time.Minute
	}
	if rpdCooldown <= 0 {
		rpdCooldown = time.Hour
	}

	var (
		hits    int
		rpdLike bool
	)
	if raw, err := c.client.Get(ctx, keyCooldownKey(key)).Bytes(); err == nil {
		var existing keyCooldownCacheValue
		if json.Unmarshal(raw, &existing) == nil {
			hits = existing.Hits
			rpdLike = existing.RPDLike
		}
	} else if err != nil && !errors.Is(err, redis.Nil) {
		return time.Time{}, false, err
	}

	hits++
	rpdLike = rpdLike || hits >= 2
	nextCooldown := cooldown
	if rpdLike {
		nextCooldown = rpdCooldown
	}
	expiresAt := time.Now().Add(nextCooldown)
	raw, err := json.Marshal(keyCooldownCacheValue{
		ExpiresAt: expiresAt.Format(time.RFC3339),
		RPDLike:   rpdLike,
		Hits:      hits,
	})
	if err != nil {
		return time.Time{}, false, err
	}
	return expiresAt, rpdLike, c.client.Set(ctx, keyCooldownKey(key), raw, keyCooldownRetention(nextCooldown, rpdCooldown)).Err()
}

func (c *RuntimeCache) DeleteKeyCooldown(ctx context.Context, key string) error {
	if !c.Enabled() {
		return nil
	}
	return c.client.Del(ctx, keyCooldownKey(key)).Err()
}

func keyCooldownKey(key string) string {
	return "buzz:key-cooldown:" + sessionHash(key)
}

func keyCooldownRetention(cooldown, rpdCooldown time.Duration) time.Duration {
	retention := rpdCooldown
	if retention < cooldown {
		retention = cooldown
	}
	return retention + 5*time.Minute
}
