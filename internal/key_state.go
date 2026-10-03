package buzz

import (
	"log"
	mathrand "math/rand"
	"strings"
	"time"
)

func (k *KeyState) Next(model string) (APIKey, bool) {
	return k.NextFor(model, RouteTarget{})
}

func (k *KeyState) NextFor(model string, target RouteTarget) (APIKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	now := time.Now()
	for i := 0; i < len(k.keys); i++ {
		idx := (k.next + i) % len(k.keys)
		key := k.keys[idx]
		if !keyMatchesTarget(key, target) {
			continue
		}
		id := cooldownKey(model, key.Name)
		expires, cooling := k.exhausted[id]
		if cooling && now.Before(expires) {
			continue
		}
		if cooling {
			delete(k.exhausted, id)
		}
		k.next = (idx + 1) % len(k.keys)
		return key, true
	}
	return APIKey{}, false
}

func (k *KeyState) AvailableFor(target RouteTarget) int {
	k.mu.Lock()
	defer k.mu.Unlock()

	count := 0
	for _, key := range k.keys {
		if keyMatchesTarget(key, target) {
			count++
		}
	}
	return count
}

func keyMatchesTarget(key APIKey, target RouteTarget) bool {
	if target.ProviderName != "" && key.ProviderName != "" && key.ProviderName != target.ProviderName {
		return false
	}
	if target.ProviderID != 0 && key.ProviderID != 0 && key.ProviderID != target.ProviderID {
		return false
	}
	return true
}

func (k *KeyState) MarkExhausted(model string, key APIKey) {
	k.MarkExhaustedUntil(model, key)
}

func (k *KeyState) MarkExhaustedUntil(model string, key APIKey) (time.Time, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	id := cooldownKey(model, key.Name)
	k.ensureMaps()
	expiresAt, rpdLike := k.nextCooldownLocked(id)
	if rpdLike {
		k.rpdLike[id] = true
	}
	k.exhausted[id] = expiresAt
	delete(k.errors, id)
	return expiresAt, rpdLike
}

func (k *KeyState) MarkRedisExhausted(model string, key APIKey, expiresAt time.Time, rpdLike bool) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if time.Now().After(expiresAt) {
		return
	}
	id := cooldownKey(model, key.Name)
	k.ensureMaps()
	k.exhausted[id] = expiresAt
	if rpdLike {
		k.rpdLike[id] = true
	}
	delete(k.errors, id)
}

func (k *KeyState) nextCooldownLocked(id string) (time.Time, bool) {
	k.cooldownHits[id]++
	cooldown := k.cooldown
	rpdLike := false
	if k.cooldownHits[id] >= 2 {
		cooldown = k.rpdCooldown
		if cooldown <= 0 {
			cooldown = time.Hour
		}
		rpdLike = true
	}
	return time.Now().Add(cooldown + time.Duration(mathrand.Intn(500))*time.Millisecond), rpdLike
}

func (k *KeyState) MarkError(model string, key APIKey, status int, message string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensureMaps()
	if len(message) > 500 {
		message = message[:500]
	}
	k.errors[cooldownKey(model, key.Name)] = KeyError{
		Key:       key.Name,
		Model:     model,
		Status:    status,
		Message:   strings.TrimSpace(message),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
}

func (k *KeyState) Remove(key APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensureMaps()
	nextKeys := k.keys[:0]
	for _, item := range k.keys {
		if !sameAPIKey(item, key) {
			nextKeys = append(nextKeys, item)
		}
	}
	k.keys = nextKeys
	if len(k.keys) == 0 {
		k.next = 0
	} else if k.next >= len(k.keys) {
		k.next = k.next % len(k.keys)
	}
	suffix := "::" + key.Name
	for item := range k.exhausted {
		if strings.HasSuffix(item, suffix) {
			delete(k.exhausted, item)
		}
	}
	for item := range k.cooldownHits {
		if strings.HasSuffix(item, suffix) {
			delete(k.cooldownHits, item)
		}
	}
	for item := range k.rpdLike {
		if strings.HasSuffix(item, suffix) {
			delete(k.rpdLike, item)
		}
	}
}

func sameAPIKey(a, b APIKey) bool {
	if a.ProviderKeyID != 0 && b.ProviderKeyID != 0 {
		return a.ProviderKeyID == b.ProviderKeyID
	}
	return a.ID == b.ID
}

func (k *KeyState) ClearError(key APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ensureMaps()
	for id, item := range k.errors {
		if item.Key == key.Name {
			delete(k.errors, id)
		}
	}
}

func (k *KeyState) MarkHealthy(model string, key APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	id := cooldownKey(model, key.Name)
	k.ensureMaps()
	delete(k.exhausted, id)
	delete(k.cooldownHits, id)
	delete(k.rpdLike, id)
	for errorID, item := range k.errors {
		if item.Key == key.Name {
			delete(k.errors, errorID)
		}
	}
}

func (k *KeyState) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.exhausted = make(map[string]time.Time)
	k.cooldownHits = make(map[string]int)
	k.rpdLike = make(map[string]bool)
}

func (k *KeyState) Replace(keys []APIKey) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = keys
	k.next = 0
	k.exhausted = make(map[string]time.Time)
	k.cooldownHits = make(map[string]int)
	k.rpdLike = make(map[string]bool)
	k.errors = make(map[string]KeyError)
}

func (k *KeyState) SnapshotExhausted() map[string]string {
	k.mu.Lock()
	defer k.mu.Unlock()

	now := time.Now()
	out := make(map[string]string)
	for key, expires := range k.exhausted {
		if now.After(expires) {
			delete(k.exhausted, key)
			continue
		}
		out[key] = expires.Format(time.RFC3339)
	}
	return out
}

func (k *KeyState) SnapshotErrors() map[string]KeyError {
	k.mu.Lock()
	defer k.mu.Unlock()

	out := make(map[string]KeyError, len(k.errors))
	for key, item := range k.errors {
		out[key] = item
	}
	return out
}

func (k *KeyState) SnapshotRPDLike() map[string]bool {
	k.mu.Lock()
	defer k.mu.Unlock()

	out := make(map[string]bool)
	for key, value := range k.rpdLike {
		if value {
			out[key] = true
		}
	}
	return out
}

func (s *Server) refreshKeyStateStats() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	s.stats.Exhausted = s.keyState.SnapshotExhausted()
	s.stats.RPDLike = s.keyState.SnapshotRPDLike()
	s.stats.KeyErrors = s.keyState.SnapshotErrors()
}

func (s *Server) disableAPIKey(key APIKey, status int, errorCode, errorMessage string, errorBody []byte) {
	id := key.ProviderKeyID
	if id == 0 {
		id = key.ID
	}
	err := s.store.DisableProviderKey(id, status, errorCode, errorMessage, string(errorBody))
	if err != nil {
		log.Printf("disable api key %s after %d %s: %v", key.Name, status, errorCode, err)
		return
	}
	s.keyState.Remove(key)
	log.Printf("disabled api key %s after %d %s", key.Name, status, errorCode)
}
func cooldownKey(model, keyName string) string {
	return model + "::" + keyName
}

func (k *KeyState) ensureMaps() {
	if k.exhausted == nil {
		k.exhausted = make(map[string]time.Time)
	}
	if k.cooldownHits == nil {
		k.cooldownHits = make(map[string]int)
	}
	if k.rpdLike == nil {
		k.rpdLike = make(map[string]bool)
	}
	if k.errors == nil {
		k.errors = make(map[string]KeyError)
	}
}
