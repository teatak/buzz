package buzz

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/teatak/cart/v3"
)

func (s *Server) newAdminAPI() http.Handler {
	app := cart.New()
	app.Use("/", cart.Recovery())
	app.NotFound = func(c *cart.Context) error {
		c.JSON(http.StatusNotFound, cart.H{"error": "not found"})
		return nil
	}
	app.Use("/admin/api", s.adminSessionMiddleware())

	api := app.Route("/admin/api")
	api.Route("/setup-state").GET(s.handleSetupState)
	api.Route("/setup").POST(s.handleSetup)
	api.Route("/login").POST(s.handleLogin)

	api.Route("/session").GET(s.handleSession)
	api.Route("/logout").POST(s.handleLogout)
	api.Route("/password").PUT(s.handlePassword)
	api.Route("/stats").GET(s.handleStats)
	api.Route("/usage").GET(s.handleUsage)
	api.Route("/quota").GET(s.handleOwnQuota)
	api.Route("/model-options").GET(s.handleModelOptions)
	api.Route("/data").GET(s.handleData)
	api.Route("/user-api-keys").
		GET(s.handleUserAPIKeysAdmin).
		POST(s.handleUserAPIKeysAdmin).
		PUT(s.handleUserAPIKeysAdmin).
		DELETE(s.handleUserAPIKeysAdmin)

	s.adminOnlyRoute(api, "/config").GET(s.handleConfig)
	s.adminOnlyRoute(api, "/users").GET(s.handleUsersAdmin).POST(s.handleUsersAdmin)
	s.adminOnlyRoute(api, "/users/:user_id").GET(s.handleUserAdmin).PUT(s.handleUserAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/quota").GET(s.handleUserQuotaAdmin).PUT(s.handleUserQuotaAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/quota/reset").POST(s.handleResetUserQuotaAdmin)
	s.adminOnlyRoute(api, "/quotas/weekly/reset").POST(s.handleResetWeeklyQuotasAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/sessions/revoke").POST(s.handleRevokeUserSessionsAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/api-keys").
		GET(s.handleUserAPIKeysForAdmin).
		POST(s.handleUserAPIKeysForAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/api-keys/:key_id").
		PUT(s.handleUserAPIKeyForAdmin).
		DELETE(s.handleUserAPIKeyForAdmin)
	s.adminOnlyRoute(api, "/users/:user_id/usage").GET(s.handleUserUsageAdmin)
	s.adminOnlyRoute(api, "/providers").
		GET(s.handleProvidersAdmin).
		POST(s.handleProvidersAdmin).
		PUT(s.handleProvidersAdmin).
		DELETE(s.handleProvidersAdmin)
	s.adminOnlyRoute(api, "/providers/:id/upstream-models").
		GET(s.handleProviderUpstreamModelsAdmin)
	s.adminOnlyRoute(api, "/provider-presets").
		GET(s.handleProviderPresetsAdmin).
		POST(s.handleProviderPresetsAdmin)
	s.adminOnlyRoute(api, "/provider-keys").
		GET(s.handleProviderKeysAdmin).
		POST(s.handleProviderKeysAdmin).
		PUT(s.handleProviderKeysAdmin).
		DELETE(s.handleProviderKeysAdmin)
	s.adminOnlyRoute(api, "/models").
		GET(s.handleModelsAdmin).
		POST(s.handleModelsAdmin).
		PUT(s.handleModelsAdmin).
		DELETE(s.handleModelsAdmin)
	s.adminOnlyRoute(api, "/model-presets").
		GET(s.handleModelPresetsAdmin).
		POST(s.handleModelPresetsAdmin)
	s.adminOnlyRoute(api, "/model-routes").
		GET(s.handleModelRoutesAdmin).
		POST(s.handleModelRoutesAdmin).
		PUT(s.handleModelRoutesAdmin).
		DELETE(s.handleModelRoutesAdmin)
	s.adminOnlyRoute(api, "/flush-exhausted").POST(s.handleFlushExhausted)

	return app
}

func (s *Server) adminOnlyRoute(r *cart.Router, path string) *cart.Router {
	return r.Route(path).Use("", s.adminOnlyMiddleware())
}

func (s *Server) adminSessionMiddleware() cart.Handler {
	return func(c *cart.Context, next cart.Next) {
		if isPublicAdminAPI(c.Request.Method, c.Request.URL.Path) {
			next()
			return
		}
		user, ok := s.authenticateAdmin(c.Request)
		if !ok {
			c.JSON(http.StatusUnauthorized, cart.H{"error": "unauthorized"})
			return
		}
		c.Set("admin_user", user)
		next()
	}
}

func isPublicAdminAPI(method, path string) bool {
	switch path {
	case "/admin/api/setup-state":
		return method == http.MethodGet
	case "/admin/api/setup", "/admin/api/login":
		return method == http.MethodPost
	default:
		return false
	}
}

func (s *Server) adminOnlyMiddleware() cart.Handler {
	return func(c *cart.Context, next cart.Next) {
		user := adminUser(c)
		if user.Role != "admin" {
			c.JSON(http.StatusForbidden, cart.H{"error": "forbidden"})
			return
		}
		next()
	}
}

func adminUser(c *cart.Context) AppUser {
	user, _ := c.MustGet("admin_user").(AppUser)
	return user
}

func jsonError(c *cart.Context, status int, err any) error {
	switch value := err.(type) {
	case error:
		c.JSON(status, cart.H{"error": value.Error()})
	case string:
		c.JSON(status, cart.H{"error": value})
	default:
		c.JSON(status, cart.H{"error": value})
	}
	return nil
}

func jsonOK(c *cart.Context, body any) error {
	c.JSON(http.StatusOK, body)
	return nil
}

func (s *Server) handleSetupState(c *cart.Context) error {
	required, err := s.store.SetupRequired()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"setup_required": required, "version": Version})
}

func (s *Server) handleSetup(c *cart.Context) error {
	var req LoginRequest
	if err := c.BindJSON(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	required, err := s.store.SetupRequired()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	if !required {
		return jsonError(c, http.StatusConflict, "setup already completed")
	}
	user, err := s.store.CreateInitialAdmin(req.Username, req.Password)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	s.reloadRuntimeNoResponse()
	token, err := s.createSession(user)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"token": token, "user": user})
}

func (s *Server) handleLogin(c *cart.Context) error {
	var req LoginRequest
	if err := c.BindJSON(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	user, err := s.store.VerifyPassword(req.Username, req.Password)
	if err != nil {
		return jsonError(c, http.StatusUnauthorized, "unauthorized")
	}
	token, err := s.createSession(user)
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"token": token, "user": user})
}

func (s *Server) handleSession(c *cart.Context) error {
	return jsonOK(c, cart.H{"user": adminUser(c), "version": Version})
}

func (s *Server) handleLogout(c *cart.Context) error {
	token := adminTokenFromRequest(c.Request)
	_ = s.deleteAdminSession(c.Request.Context(), token)
	return jsonOK(c, cart.H{"status": "ok"})
}

func (s *Server) handlePassword(c *cart.Context) error {
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.BindJSON(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	if err := s.store.ChangePassword(adminUser(c).ID, req.CurrentPassword, req.NewPassword); err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	return jsonOK(c, cart.H{"status": "ok"})
}

func (s *Server) handleStats(c *cart.Context) error {
	user := adminUser(c)
	if user.Role != "admin" {
		return jsonOK(c, s.userStats())
	}
	return jsonOK(c, s.statsSnapshot())
}

func (s *Server) handleOwnQuota(c *cart.Context) error {
	status, err := s.store.UserQuotaStatus(adminUser(c).ID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jsonError(c, http.StatusNotFound, "user not found")
		}
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, status)
}

func (s *Server) handleModelOptions(c *cart.Context) error {
	models, err := s.store.Models()
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	enabled := make([]Model, 0, len(models))
	for _, model := range models {
		if model.Enabled {
			enabled = append(enabled, model)
		}
	}
	return jsonOK(c, enabled)
}

func (s *Server) handleConfig(c *cart.Context) error {
	return jsonOK(c, s.adminConfig())
}

func (s *Server) handleData(c *cart.Context) error {
	data, status, err := s.adminData(adminUser(c))
	if err != nil {
		return jsonError(c, status, err)
	}
	return jsonOK(c, data)
}

func (s *Server) handleUsersAdmin(c *cart.Context) error {
	return s.handleUsers(c)
}

func (s *Server) handleUserAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	current, err := s.store.User(userID)
	if err != nil {
		return jsonError(c, http.StatusNotFound, "user not found")
	}
	if c.Request.Method == http.MethodGet {
		return jsonOK(c, current)
	}
	if c.Request.Method != http.MethodPut {
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}

	var req struct {
		Role  *string `json:"role"`
		Valid *bool   `json:"valid"`
	}
	if err := c.BindJSON(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	role := current.Role
	valid := current.Valid
	if req.Role != nil {
		role = *req.Role
	}
	if req.Valid != nil {
		valid = *req.Valid
	}
	updated, err := s.store.UpdateAppUser(adminUser(c).ID, userID, role, valid)
	if err != nil {
		return jsonError(c, http.StatusBadRequest, err)
	}
	if updated.Role != current.Role || updated.Valid != current.Valid {
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		if err := s.revokeAdminSessions(c.Request.Context(), userID); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
	}
	return jsonOK(c, updated)
}

func (s *Server) handleRevokeUserSessionsAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	if userID == adminUser(c).ID {
		return jsonError(c, http.StatusBadRequest, "cannot revoke your own sessions from user management")
	}
	if _, err := s.store.User(userID); err != nil {
		return jsonError(c, http.StatusNotFound, "user not found")
	}
	if err := s.revokeAdminSessions(c.Request.Context(), userID); err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"status": "ok"})
}

func (s *Server) handleUserQuotaAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	if c.Request.Method == http.MethodPut {
		var req struct {
			WeeklyQuotaCredits   *int64 `json:"weekly_quota_credits"`
			LifetimeQuotaCredits *int64 `json:"lifetime_quota_credits"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if req.WeeklyQuotaCredits == nil || req.LifetimeQuotaCredits == nil {
			return jsonError(c, http.StatusBadRequest, "weekly_quota_credits and lifetime_quota_credits are required")
		}
		if _, err := s.store.UpdateUserQuota(userID, *req.WeeklyQuotaCredits, *req.LifetimeQuotaCredits, time.Now().UTC()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return jsonError(c, http.StatusNotFound, "user not found")
			}
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
	} else if c.Request.Method != http.MethodGet {
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
	status, err := s.store.UserQuotaStatus(userID, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jsonError(c, http.StatusNotFound, "user not found")
		}
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, status)
}

func (s *Server) handleResetUserQuotaAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	if _, err := s.store.ResetUserQuota(userID, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jsonError(c, http.StatusNotFound, "user not found")
		}
		return jsonError(c, http.StatusInternalServerError, err)
	}
	if err := s.reloadRuntimeState(); err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	status, err := s.store.UserQuotaStatus(userID, time.Now().UTC())
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, status)
}

func (s *Server) handleResetWeeklyQuotasAdmin(c *cart.Context) error {
	count, err := s.store.ResetWeeklyQuotas(time.Now().UTC())
	if err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	if err := s.reloadRuntimeState(); err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"reset_count": count})
}

func (s *Server) handleUserAPIKeysForAdmin(c *cart.Context) error {
	userID, err := c.ParamInt64("user_id")
	if err != nil || userID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user id")
	}
	if _, err := s.store.User(userID); err != nil {
		return jsonError(c, http.StatusNotFound, "user not found")
	}

	switch c.Request.Method {
	case http.MethodGet:
		keys, err := s.store.UserAPIKeyDetails(userID)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		for i := range keys {
			keys[i].Token = maskSecret(keys[i].Token)
		}
		return jsonOK(c, keys)
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		created, err := s.store.CreateUserAPIKey(AuthToken{
			UserID: userID,
			Name:   strings.TrimSpace(req.Name),
			Token:  "bh_" + randomHex(24),
			Valid:  true,
		})
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, created)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleUserAPIKeyForAdmin(c *cart.Context) error {
	userID, userErr := c.ParamInt64("user_id")
	keyID, keyErr := c.ParamInt64("key_id")
	if userErr != nil || keyErr != nil || userID <= 0 || keyID <= 0 {
		return jsonError(c, http.StatusBadRequest, "invalid user or key id")
	}
	if _, err := s.store.UserAPIKey(keyID, userID); err != nil {
		return jsonError(c, http.StatusNotFound, "api key not found")
	}

	switch c.Request.Method {
	case http.MethodPut:
		var req struct {
			Valid bool `json:"valid"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.store.SetUserAPIKeyValid(keyID, userID, req.Valid); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
	case http.MethodDelete:
		if err := s.store.DeleteUserAPIKey(keyID, userID); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
	return s.reloadRuntime(c)
}

func (s *Server) handleUserAPIKeysAdmin(c *cart.Context) error {
	return s.handleUserAPIKeys(c, adminUser(c))
}

func (s *Server) handleProvidersAdmin(c *cart.Context) error {
	return s.handleProviders(c)
}

func (s *Server) handleProviderPresetsAdmin(c *cart.Context) error {
	return s.handleProviderPresets(c)
}

func (s *Server) handleProviderKeysAdmin(c *cart.Context) error {
	return s.handleProviderKeys(c)
}

func (s *Server) handleModelsAdmin(c *cart.Context) error {
	return s.handleModels(c)
}

func (s *Server) handleModelPresetsAdmin(c *cart.Context) error {
	return s.handleModelPresets(c)
}

func (s *Server) handleModelRoutesAdmin(c *cart.Context) error {
	return s.handleModelRoutes(c)
}

func (s *Server) handleFlushExhausted(c *cart.Context) error {
	s.keyState.Flush()
	s.refreshKeyStateStats()
	return jsonOK(c, cart.H{"status": "ok"})
}

func (s *Server) authenticate(r *http.Request) (AuthToken, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	if len(s.authTokens) == 0 {
		return AuthToken{Name: "local", UserName: "local", Valid: true}, true
	}

	var tokens []string
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		tokens = append(tokens, strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	}
	if key := r.URL.Query().Get("key"); key != "" {
		tokens = append(tokens, strings.TrimSpace(key))
	}
	if key := r.Header.Get("x-goog-api-key"); key != "" {
		tokens = append(tokens, strings.TrimSpace(key))
	}
	if key := r.Header.Get("x-api-key"); key != "" {
		tokens = append(tokens, strings.TrimSpace(key))
	}

	for _, token := range tokens {
		user, ok := s.authTokens[token]
		if ok && user.Valid {
			return user, true
		}
	}
	return AuthToken{}, false
}

func (s *Server) authenticateAdmin(r *http.Request) (AppUser, bool) {
	token := adminTokenFromRequest(r)
	if token == "" {
		return AppUser{}, false
	}
	sessionUser, ok := s.adminSessionByToken(r.Context(), token)
	if !ok {
		return AppUser{}, false
	}
	return sessionUser.User, true
}

func (s *Server) adminSessionByToken(ctx context.Context, token string) (SessionUser, bool) {
	if s.runtimeCache.Enabled() {
		sessionUser, err := s.runtimeCache.AdminSession(ctx, token)
		if err != nil {
			return SessionUser{}, false
		}
		sessionUser = s.renewAdminSessionIfNeeded(ctx, token, sessionUser)
		return sessionUser, true
	}

	s.runtimeMu.Lock()
	user, ok := s.adminSessions[token]
	s.runtimeMu.Unlock()
	if ok && user.User.Valid && time.Now().Before(user.ExpiresAt) {
		user = s.renewAdminSessionIfNeeded(ctx, token, user)
		return user, true
	}
	if ok {
		s.runtimeMu.Lock()
		delete(s.adminSessions, token)
		s.runtimeMu.Unlock()
	}
	return SessionUser{}, false
}

func adminTokenFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	return ""
}

func (s *Server) createSession(user AppUser) (string, error) {
	token := randomHex(32)
	issuedAt := time.Now()
	expiresAt := issuedAt.Add(adminSessionTTL)
	sessionUser := SessionUser{User: user, IssuedAt: issuedAt, ExpiresAt: expiresAt}
	if s.runtimeCache.Enabled() {
		if err := s.runtimeCache.SetAdminSession(context.Background(), token, sessionUser); err != nil {
			return "", err
		}
		return token, nil
	}
	s.runtimeMu.Lock()
	s.adminSessions[token] = sessionUser
	s.runtimeMu.Unlock()
	return token, nil
}

func (s *Server) renewAdminSessionIfNeeded(ctx context.Context, token string, sessionUser SessionUser) SessionUser {
	if time.Until(sessionUser.ExpiresAt) > adminSessionRenewBefore {
		return sessionUser
	}
	nextExpiresAt := time.Now().Add(adminSessionTTL)
	nextSessionUser := SessionUser{User: sessionUser.User, IssuedAt: sessionUser.IssuedAt, ExpiresAt: nextExpiresAt}
	if s.runtimeCache.Enabled() {
		if err := s.runtimeCache.SetAdminSession(ctx, token, nextSessionUser); err != nil {
			log.Printf("renew admin session: %v", err)
			return sessionUser
		}
		return nextSessionUser
	}
	s.runtimeMu.Lock()
	s.adminSessions[token] = nextSessionUser
	s.runtimeMu.Unlock()
	return nextSessionUser
}

func (s *Server) deleteAdminSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if s.runtimeCache.Enabled() {
		return s.runtimeCache.DeleteAdminSession(ctx, token)
	}
	s.runtimeMu.Lock()
	delete(s.adminSessions, token)
	s.runtimeMu.Unlock()
	return nil
}

func (s *Server) revokeAdminSessions(ctx context.Context, userID int64) error {
	if s.runtimeCache.Enabled() {
		return s.runtimeCache.RevokeAdminSessions(ctx, userID)
	}
	s.runtimeMu.Lock()
	for token, session := range s.adminSessions {
		if session.User.ID == userID {
			delete(s.adminSessions, token)
		}
	}
	s.runtimeMu.Unlock()
	return nil
}

func (s *Server) serveAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.adminDir == "" {
		return false
	}
	if r.URL.Path == "/admin" || r.URL.Path == "/admin/" {
		indexPath := s.adminDir + "/index.html"
		if _, err := os.Stat(indexPath); err != nil {
			return false
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, indexPath)
		return true
	}
	if r.URL.Path == "/admin/favicon.svg" || r.URL.Path == "/favicon.svg" {
		faviconPath := s.adminDir + "/favicon.svg"
		if _, err := os.Stat(faviconPath); err != nil {
			return false
		}
		http.ServeFile(w, r, faviconPath)
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/admin/assets/") {
		filePath := strings.TrimPrefix(r.URL.Path, "/admin/")
		if _, err := os.Stat(s.adminDir + "/" + filePath); err != nil {
			return false
		}
		http.StripPrefix("/admin/", http.FileServer(http.Dir(s.adminDir))).ServeHTTP(w, r)
		return true
	}
	return false
}

func (s *Server) adminConfig() AdminConfig {
	users, _ := s.store.Users()

	tokens := make([]string, 0, len(users))
	for _, user := range users {
		if user.Valid {
			tokens = append(tokens, user.Username)
		}
	}
	return AdminConfig{
		Addr:            s.cfg.Server.Addr,
		UpstreamBaseURL: s.cfg.Upstream.BaseURL,
		Timeout:         s.cfg.Upstream.Timeout,
		MaxAttempts:     s.cfg.Retry.MaxAttempts,
		CooldownSeconds: s.cfg.Retry.CooldownSeconds,
		Tokens:          tokens,
	}
}

func (s *Server) adminData(actor AppUser) (AdminData, int, error) {
	if actor.Role != "admin" {
		userAPIKeys, err := s.store.UserAPIKeys()
		if err != nil {
			return AdminData{}, http.StatusInternalServerError, err
		}
		return AdminData{
			Config: AdminConfig{
				Timeout:         s.cfg.Upstream.Timeout,
				CooldownSeconds: s.cfg.Retry.CooldownSeconds,
			},
			UserAPIKeys: maskUsers(filterUserAPIKeys(userAPIKeys, actor)),
		}, http.StatusOK, nil
	}

	users, err := s.store.Users()
	if err != nil {
		return AdminData{}, http.StatusInternalServerError, err
	}
	userAPIKeys, err := s.store.UserAPIKeys()
	if err != nil {
		return AdminData{}, http.StatusInternalServerError, err
	}
	return AdminData{
		Config:      s.adminConfig(),
		Users:       users,
		UserAPIKeys: maskUsers(filterUserAPIKeys(userAPIKeys, actor)),
	}, http.StatusOK, nil
}

func (s *Server) handleUsers(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		users, err := s.store.Users()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, users)
	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if _, err := s.store.CreateAppUser(req.Username, req.Password, req.Role); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, cart.H{"status": "ok"})
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleUserAPIKeys(c *cart.Context, actor AppUser) error {
	switch c.Request.Method {
	case http.MethodGet:
		if id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64); id != 0 {
			key, err := s.store.UserAPIKey(id, actor.ID)
			if err != nil {
				return jsonError(c, http.StatusNotFound, "not found")
			}
			return jsonOK(c, key)
		}
		keys, err := s.store.UserAPIKeys()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, maskUsers(filterUserAPIKeys(keys, actor)))
	case http.MethodPost:
		var key AuthToken
		if err := c.BindJSON(&key); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		key.UserID = actor.ID
		key.Valid = true
		if key.Token == "" {
			key.Token = "bh_" + randomHex(24)
		}
		created, err := s.store.CreateUserAPIKey(key)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, created)
	case http.MethodPut:
		var key AuthToken
		if err := c.BindJSON(&key); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.store.SetUserAPIKeyValid(key.ID, actor.ID, key.Valid); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return s.reloadRuntime(c)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64)
		if err := s.store.DeleteUserAPIKey(id, actor.ID); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return s.reloadRuntime(c)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

type providerWriteRequest struct {
	ID        int64              `json:"id"`
	Name      string             `json:"name"`
	PresetID  string             `json:"preset_id"`
	Endpoints []ProviderEndpoint `json:"endpoints"`
	Enabled   *bool              `json:"enabled"`
}

type providerKeyWriteRequest struct {
	ID         int64    `json:"id"`
	ProviderID int64    `json:"provider_id"`
	Name       string   `json:"name"`
	Secret     string   `json:"secret"`
	Secrets    []string `json:"secrets"`
	Enabled    *bool    `json:"enabled"`
	Priority   *int     `json:"priority"`
	Weight     *int     `json:"weight"`
	Labels     string   `json:"labels"`
}

type modelWriteRequest struct {
	ID                     int64    `json:"id"`
	Name                   string   `json:"name"`
	DisplayName            string   `json:"display_name"`
	Icon                   *string  `json:"icon"`
	Description            string   `json:"description"`
	ContextWindow          int64    `json:"context_window"`
	MaxInputTokens         int64    `json:"max_input_tokens"`
	MaxOutputTokens        int64    `json:"max_output_tokens"`
	QuotaUncachedInputRate *float64 `json:"quota_uncached_input_rate"`
	QuotaCachedInputRate   *float64 `json:"quota_cached_input_rate"`
	QuotaOutputRate        *float64 `json:"quota_output_rate"`
	Capabilities           string   `json:"capabilities"`
	SelectionPolicy        string   `json:"selection_policy"`
	Enabled                *bool    `json:"enabled"`
}

type modelRouteWriteRequest struct {
	ModelMetadata    *ModelMetadata `json:"model_metadata"`
	ID               int64          `json:"id"`
	ModelID          int64          `json:"model_id"`
	ProviderID       int64          `json:"provider_id"`
	UpstreamProtocol string         `json:"upstream_protocol"`
	UpstreamModel    string         `json:"upstream_model"`
	Enabled          *bool          `json:"enabled"`
	Priority         *int           `json:"priority"`
	Weight           *int           `json:"weight"`
}

func (s *Server) handleProviders(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		providers, err := s.store.Providers()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, providers)
	case http.MethodPost:
		var req providerWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		created, err := s.store.CreateProvider(ProviderRecord{
			Name:      req.Name,
			PresetID:  req.PresetID,
			Endpoints: req.Endpoints,
			Enabled:   boolWithDefault(req.Enabled, true),
		})
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, created)
	case http.MethodPut:
		var req providerWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		existing, err := s.store.Provider(req.ID)
		if err != nil {
			return jsonError(c, http.StatusNotFound, "not found")
		}
		if req.Name != "" {
			existing.Name = req.Name
		}
		existing.PresetID = req.PresetID
		if req.Endpoints != nil {
			existing.Endpoints = req.Endpoints
		}
		if req.Enabled != nil {
			existing.Enabled = *req.Enabled
		}
		updated, err := s.store.UpdateProvider(existing)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, updated)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64)
		if err := s.store.DeleteProvider(id); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return s.reloadRuntime(c)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProviderPresets(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		return jsonOK(c, providerPresets())
	case http.MethodPost:
		var req struct {
			ID string `json:"id"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		preset, ok := findProviderPreset(req.ID)
		if !ok {
			return jsonError(c, http.StatusNotFound, "preset not found")
		}
		providers, err := s.store.Providers()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		for _, provider := range providers {
			if strings.EqualFold(provider.Name, preset.Name) {
				return jsonError(c, http.StatusConflict, "provider already exists")
			}
		}
		created, err := s.store.CreateProvider(preset.Provider())
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, created)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleProviderKeys(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		query := c.Request.URL.Query()
		reveal := query.Get("reveal") == "1"
		if id, _ := strconv.ParseInt(query.Get("id"), 10, 64); id != 0 {
			key, err := s.store.ProviderKey(id, reveal)
			if err != nil {
				return jsonError(c, http.StatusNotFound, "not found")
			}
			return jsonOK(c, key)
		}
		providerID, _ := strconv.ParseInt(query.Get("provider_id"), 10, 64)
		keys, err := s.store.ProviderKeys(providerID, reveal)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, keys)
	case http.MethodPost:
		var req providerKeyWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		secrets := compactSecrets(append([]string{req.Secret}, req.Secrets...))
		if len(secrets) == 0 {
			return jsonError(c, http.StatusBadRequest, "secret is required")
		}
		created := make([]ProviderKey, 0, len(secrets))
		for _, secret := range secrets {
			name := req.Name
			if len(secrets) > 1 {
				name = ""
			}
			key := ProviderKey{
				ProviderID: req.ProviderID,
				Name:       name,
				Secret:     secret,
				Enabled:    boolWithDefault(req.Enabled, true),
				Priority:   intWithDefault(req.Priority, 0),
				Weight:     intWithDefault(req.Weight, 1),
				Labels:     req.Labels,
			}
			item, err := s.store.CreateProviderKey(key)
			if err != nil {
				return jsonError(c, http.StatusBadRequest, err)
			}
			created = append(created, item)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		if len(created) == 1 {
			return jsonOK(c, created[0])
		}
		return jsonOK(c, created)
	case http.MethodPut:
		var req providerKeyWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		key, err := s.store.ProviderKey(req.ID, false)
		if err != nil {
			return jsonError(c, http.StatusNotFound, "not found")
		}
		if req.ProviderID != 0 {
			key.ProviderID = req.ProviderID
		}
		if req.Name != "" {
			key.Name = req.Name
		}
		key.Secret = req.Secret
		if req.Enabled != nil {
			key.Enabled = *req.Enabled
		}
		if req.Priority != nil {
			key.Priority = *req.Priority
		}
		if req.Weight != nil {
			key.Weight = *req.Weight
		}
		key.Labels = req.Labels
		updated, err := s.store.UpdateProviderKey(key)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		if err := s.reloadRuntimeState(); err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, updated)
	case http.MethodDelete:
		ids := parseIDs(c.Request.URL.Query().Get("ids"))
		if len(ids) == 0 {
			if id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64); id != 0 {
				ids = append(ids, id)
			}
		}
		if err := s.store.DeleteProviderKeys(ids); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return s.reloadRuntime(c)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModels(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		models, err := s.store.Models()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, models)
	case http.MethodPost:
		var req modelWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		icon := ""
		if req.Icon != nil {
			icon = *req.Icon
		}
		created, err := s.store.CreateModel(Model{
			Name:                   req.Name,
			DisplayName:            req.DisplayName,
			Icon:                   icon,
			Description:            req.Description,
			ContextWindow:          req.ContextWindow,
			MaxInputTokens:         req.MaxInputTokens,
			MaxOutputTokens:        req.MaxOutputTokens,
			QuotaUncachedInputRate: float64WithDefault(req.QuotaUncachedInputRate, 1),
			QuotaCachedInputRate:   float64WithDefault(req.QuotaCachedInputRate, 1),
			QuotaOutputRate:        float64WithDefault(req.QuotaOutputRate, 1),
			Capabilities:           req.Capabilities,
			SelectionPolicy:        req.SelectionPolicy,
			Enabled:                boolWithDefault(req.Enabled, true),
		})
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, created)
	case http.MethodPut:
		var req modelWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		model, err := s.store.Model(req.ID)
		if err != nil {
			return jsonError(c, http.StatusNotFound, "not found")
		}
		if req.Name != "" {
			model.Name = req.Name
		}
		model.DisplayName = req.DisplayName
		if req.Icon != nil {
			model.Icon = *req.Icon
		}
		model.Description = req.Description
		model.ContextWindow = req.ContextWindow
		model.MaxInputTokens = req.MaxInputTokens
		model.MaxOutputTokens = req.MaxOutputTokens
		if req.QuotaUncachedInputRate != nil {
			model.QuotaUncachedInputRate = *req.QuotaUncachedInputRate
		}
		if req.QuotaCachedInputRate != nil {
			model.QuotaCachedInputRate = *req.QuotaCachedInputRate
		}
		if req.QuotaOutputRate != nil {
			model.QuotaOutputRate = *req.QuotaOutputRate
		}
		if req.Capabilities != "" {
			model.Capabilities = req.Capabilities
		}
		if req.SelectionPolicy != "" {
			model.SelectionPolicy = req.SelectionPolicy
		}
		if req.Enabled != nil {
			model.Enabled = *req.Enabled
		}
		updated, err := s.store.UpdateModel(model)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, updated)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64)
		if err := s.store.DeleteModel(id); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, cart.H{"status": "ok"})
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModelPresets(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		return jsonOK(c, modelPresets())
	case http.MethodPost:
		var req struct {
			ID  string   `json:"id"`
			IDs []string `json:"ids"`
		}
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		ids := req.IDs
		if req.ID != "" {
			ids = append(ids, req.ID)
		}
		if len(ids) == 0 {
			return jsonError(c, http.StatusBadRequest, "preset id required")
		}
		models, err := s.store.Models()
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		existing := make(map[string]bool, len(models))
		for _, model := range models {
			existing[model.Name] = true
		}
		created := make([]Model, 0, len(ids))
		for _, id := range ids {
			preset, ok := findModelPreset(id)
			if !ok {
				return jsonError(c, http.StatusNotFound, "preset not found")
			}
			if existing[preset.Name] {
				continue
			}
			model, err := s.store.CreateModel(preset.Model())
			if err != nil {
				return jsonError(c, http.StatusBadRequest, err)
			}
			existing[model.Name] = true
			created = append(created, model)
		}
		return jsonOK(c, created)
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleModelRoutes(c *cart.Context) error {
	switch c.Request.Method {
	case http.MethodGet:
		modelID, _ := strconv.ParseInt(c.Request.URL.Query().Get("model_id"), 10, 64)
		routes, err := s.store.ModelRoutes(modelID)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, err)
		}
		return jsonOK(c, routes)
	case http.MethodPost:
		var req modelRouteWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		created, err := s.store.SaveModelRoute(ModelRoute{
			ModelID:          req.ModelID,
			ProviderID:       req.ProviderID,
			UpstreamProtocol: req.UpstreamProtocol,
			UpstreamModel:    req.UpstreamModel,
			Enabled:          boolWithDefault(req.Enabled, true),
			Priority:         intWithDefault(req.Priority, 0),
			Weight:           intWithDefault(req.Weight, 1),
		}, req.ModelMetadata)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, created)
	case http.MethodPut:
		var req modelRouteWriteRequest
		if err := c.BindJSON(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		route, err := s.store.ModelRoute(req.ID)
		if err != nil {
			return jsonError(c, http.StatusNotFound, "not found")
		}
		if req.ModelID != 0 {
			route.ModelID = req.ModelID
		}
		if req.ProviderID != 0 {
			route.ProviderID = req.ProviderID
		}
		if req.UpstreamProtocol != "" {
			route.UpstreamProtocol = req.UpstreamProtocol
		}
		if req.UpstreamModel != "" {
			route.UpstreamModel = req.UpstreamModel
		}

		if req.Enabled != nil {
			route.Enabled = *req.Enabled
		}
		if req.Priority != nil {
			route.Priority = *req.Priority
		}
		if req.Weight != nil {
			route.Weight = *req.Weight
		}
		updated, err := s.store.SaveModelRoute(route, req.ModelMetadata)
		if err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, updated)
	case http.MethodDelete:
		id, _ := strconv.ParseInt(c.Request.URL.Query().Get("id"), 10, 64)
		if err := s.store.DeleteModelRoute(id); err != nil {
			return jsonError(c, http.StatusBadRequest, err)
		}
		return jsonOK(c, cart.H{"status": "ok"})
	default:
		return jsonError(c, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func boolWithDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func intWithDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func float64WithDefault(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func compactSecrets(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func (s *Server) reloadRuntime(c *cart.Context) error {
	if err := s.reloadRuntimeState(); err != nil {
		return jsonError(c, http.StatusInternalServerError, err)
	}
	return jsonOK(c, cart.H{"status": "ok"})
}

func (s *Server) reloadRuntimeState() error {
	tokens, providerRecords, keys, err := s.store.ReloadRuntime()
	if err != nil {
		return err
	}
	providers, err := newProviderRegistry(providerRecords, s.upstream, s.client)
	if err != nil {
		return err
	}
	s.runtimeMu.Lock()
	s.authTokens = tokens
	s.providers = providers
	s.runtimeMu.Unlock()
	s.keyState.Replace(keys)
	s.refreshKeyStateStats()
	return nil
}

func (s *Server) reloadRuntimeNoResponse() {
	tokens, providerRecords, keys, err := s.store.ReloadRuntime()
	if err != nil {
		log.Printf("reload runtime: %v", err)
		return
	}
	providers, err := newProviderRegistry(providerRecords, s.upstream, s.client)
	if err != nil {
		log.Printf("reload runtime providers: %v", err)
		return
	}
	s.runtimeMu.Lock()
	s.authTokens = tokens
	s.providers = providers
	s.runtimeMu.Unlock()
	s.keyState.Replace(keys)
}

func maskUsers(users []AuthToken) []AuthToken {
	out := make([]AuthToken, 0, len(users))
	for _, user := range users {
		out = append(out, maskUser(user))
	}
	return out
}

func filterUserAPIKeys(keys []AuthToken, actor AppUser) []AuthToken {
	if actor.Role == "admin" && actor.ID == 0 {
		return keys
	}
	out := make([]AuthToken, 0, len(keys))
	for _, key := range keys {
		if key.UserID == actor.ID {
			out = append(out, key)
		}
	}
	return out
}

func maskUser(user AuthToken) AuthToken {
	user.Token = maskSecret(user.Token)
	return user
}
