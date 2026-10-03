package buzz

import (
	"context"
	"strconv"
	"time"
)

func (s *Server) nextProviderKey(ctx context.Context, cooldownModel string, target RouteTarget) (APIKey, bool) {
	for {
		key, ok := s.keyState.NextFor(cooldownModel, target)
		if !ok {
			return APIKey{}, false
		}
		expiresAt, rpdLike, ok := s.redisKeyCooldown(ctx, target, key)
		if !ok {
			return key, true
		}
		s.keyState.MarkRedisExhausted(cooldownModel, key, expiresAt, rpdLike)
	}
}

func (s *Server) markProviderKeyExhausted(ctx context.Context, cooldownModel string, target RouteTarget, key APIKey) {
	expiresAt, rpdLike := s.keyState.MarkExhaustedUntil(cooldownModel, key)
	redisExpiresAt, redisRPDLike, err := s.runtimeCache.MarkKeyExhausted(ctx, keyCooldownStorageKey(target, key), s.keyState.cooldown, s.keyState.rpdCooldown)
	if err == nil {
		expiresAt = redisExpiresAt
		rpdLike = redisRPDLike
	}
	s.keyState.MarkRedisExhausted(cooldownModel, key, expiresAt, rpdLike)
}

func (s *Server) markProviderKeyHealthy(ctx context.Context, cooldownModel string, target RouteTarget, key APIKey) {
	s.keyState.MarkHealthy(cooldownModel, key)
	_ = s.runtimeCache.DeleteKeyCooldown(ctx, keyCooldownStorageKey(target, key))
}

func (s *Server) redisKeyCooldown(ctx context.Context, target RouteTarget, key APIKey) (time.Time, bool, bool) {
	expiresAt, rpdLike, err := s.runtimeCache.KeyCooldown(ctx, keyCooldownStorageKey(target, key))
	if err != nil {
		return time.Time{}, false, false
	}
	return expiresAt, rpdLike, true
}

func keyCooldownStorageKey(target RouteTarget, key APIKey) string {
	provider := stringID(target.ProviderID, target.ProviderName)
	if provider == "" {
		provider = stringID(key.ProviderID, key.ProviderName)
	}
	model := target.UpstreamModel
	keyID := stringID(key.ProviderKeyID, key.Name)
	if keyID == "" {
		keyID = stringID(key.ID, key.Name)
	}
	return provider + "::" + model + "::" + keyID
}

func stringID(id int64, fallback string) string {
	if id != 0 {
		return strconv.FormatInt(id, 10)
	}
	return fallback
}
