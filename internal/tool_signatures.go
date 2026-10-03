package buzz

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/teatak/buzz/internal/protocol"
)

const (
	toolSignatureTTL        = 24 * time.Hour
	maxToolSignatureEntries = 4096
)

func (s *Server) rememberToolSignatures(ctx context.Context, user AuthToken, model string, toolCalls []protocol.CanonicalToolCall) {
	if len(toolCalls) == 0 {
		return
	}
	s.toolSigMu.Lock()
	if s.toolSigs == nil {
		s.toolSigs = make(map[string]toolSignatureEntry)
	}
	now := time.Now()
	s.purgeExpiredToolSignatures(now)
	scope := toolSignatureScope(user, model)
	entries := make(map[string]string, len(toolCalls)*2)
	for _, call := range toolCalls {
		if call.Signature == "" {
			continue
		}
		if id := strings.TrimSpace(call.ID); id != "" {
			key := scope + toolSignatureIDKey(id)
			s.toolSigs[key] = newToolSignatureEntry(call.Signature, now)
			entries[key] = call.Signature
		}
		if name := strings.TrimSpace(call.Name); name != "" {
			key := scope + toolSignatureFunctionKey(name, call.Arguments)
			s.toolSigs[key] = newToolSignatureEntry(call.Signature, now)
			entries[key] = call.Signature
		}
	}
	s.trimToolSignatures()
	s.toolSigMu.Unlock()

	for key, signature := range entries {
		_ = s.runtimeCache.SetToolSignature(ctx, key, signature, toolSignatureTTL)
	}
}

func (s *Server) applyToolSignatures(ctx context.Context, user AuthToken, model string, req *protocol.CanonicalRequest) {
	scope := toolSignatureScope(user, model)
	for messageIndex := range req.Messages {
		for partIndex := range req.Messages[messageIndex].Parts {
			part := &req.Messages[messageIndex].Parts[partIndex]
			if part.Type != "tool_call" || part.Signature != "" {
				continue
			}
			if id := strings.TrimSpace(part.ToolCallID); id != "" {
				if signature := s.toolSignature(ctx, scope+toolSignatureIDKey(id)); signature != "" {
					part.Signature = signature
					continue
				}
			}
			if name := strings.TrimSpace(part.Name); name != "" {
				part.Signature = s.toolSignature(ctx, scope+toolSignatureFunctionKey(name, string(part.Arguments)))
			}
		}
	}
}

func (s *Server) toolSignature(ctx context.Context, key string) string {
	now := time.Now()
	s.toolSigMu.Lock()
	s.purgeExpiredToolSignatures(now)
	entry, ok := s.toolSigs[key]
	s.toolSigMu.Unlock()
	if ok {
		return entry.signature
	}

	signature, err := s.runtimeCache.ToolSignature(ctx, key)
	if err != nil || signature == "" {
		return ""
	}
	s.toolSigMu.Lock()
	if s.toolSigs == nil {
		s.toolSigs = make(map[string]toolSignatureEntry)
	}
	s.toolSigs[key] = newToolSignatureEntry(signature, now)
	s.trimToolSignatures()
	s.toolSigMu.Unlock()
	return signature
}

func newToolSignatureEntry(signature string, now time.Time) toolSignatureEntry {
	return toolSignatureEntry{
		signature: signature,
		expiresAt: now.Add(toolSignatureTTL),
		updatedAt: now,
	}
}

func (s *Server) purgeExpiredToolSignatures(now time.Time) {
	for key, entry := range s.toolSigs {
		if !entry.expiresAt.After(now) {
			delete(s.toolSigs, key)
		}
	}
}

func (s *Server) trimToolSignatures() {
	for len(s.toolSigs) > maxToolSignatureEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range s.toolSigs {
			if oldestKey == "" || entry.updatedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.updatedAt
			}
		}
		delete(s.toolSigs, oldestKey)
	}
}

func toolSignatureScope(user AuthToken, model string) string {
	return routeSessionStorageKey(user, strings.TrimSpace(model)) + "::"
}

func toolSignatureIDKey(id string) string {
	return "id:" + strings.TrimSpace(id)
}

func toolSignatureFunctionKey(name, args string) string {
	return "fn:" + strings.TrimSpace(name) + ":" + normalizeToolSignatureArgs(args)
}

func normalizeToolSignatureArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}
