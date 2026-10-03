package buzz

import (
	"context"
	"strconv"
	"time"
)

const routeSessionTTL = 30 * time.Minute

func (s *Server) preferRouteSessionTarget(ctx context.Context, user AuthToken, publicModel string, targets []RouteTarget) []RouteTarget {
	if len(targets) <= 1 {
		return targets
	}
	session, ok := s.routeSession(ctx, user, publicModel)
	if !ok || session.ModelRouteID == 0 {
		return targets
	}
	for idx, target := range targets {
		if target.ID != session.ModelRouteID {
			continue
		}
		if idx == 0 {
			return targets
		}
		out := make([]RouteTarget, 0, len(targets))
		out = append(out, target)
		out = append(out, targets[:idx]...)
		out = append(out, targets[idx+1:]...)
		return out
	}
	s.deleteRouteSession(ctx, user, publicModel)
	return targets
}

func (s *Server) rememberRouteSession(ctx context.Context, user AuthToken, publicModel string, target RouteTarget) {
	if target.ID == 0 {
		return
	}
	key := routeSessionStorageKey(user, publicModel)
	session := RouteSession{
		ModelRouteID: target.ID,
		ExpiresAt:    time.Now().Add(routeSessionTTL),
	}

	s.routeMu.Lock()
	if s.routeSessions == nil {
		s.routeSessions = make(map[string]RouteSession)
	}
	s.routeSessions[key] = session
	s.routeMu.Unlock()

	_ = s.runtimeCache.SetRouteSession(ctx, key, session)
}

func (s *Server) routeSession(ctx context.Context, user AuthToken, publicModel string) (RouteSession, bool) {
	key := routeSessionStorageKey(user, publicModel)
	if session, err := s.runtimeCache.RouteSession(ctx, key); err == nil {
		return session, true
	}

	now := time.Now()
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	session, ok := s.routeSessions[key]
	if !ok {
		return RouteSession{}, false
	}
	if now.After(session.ExpiresAt) {
		delete(s.routeSessions, key)
		return RouteSession{}, false
	}
	return session, true
}

func (s *Server) deleteRouteSession(ctx context.Context, user AuthToken, publicModel string) {
	key := routeSessionStorageKey(user, publicModel)
	s.routeMu.Lock()
	delete(s.routeSessions, key)
	s.routeMu.Unlock()
	_ = s.runtimeCache.DeleteRouteSession(ctx, key)
}

func routeSessionStorageKey(user AuthToken, publicModel string) string {
	identity := "local"
	if user.ID != 0 {
		identity = "key:" + strconv.FormatInt(user.ID, 10)
	} else if user.Name != "" {
		identity = "name:" + user.Name
	} else if user.UserName != "" {
		identity = "user:" + user.UserName
	}
	return identity + "::" + publicModel
}
