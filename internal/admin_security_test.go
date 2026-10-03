package buzz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAdminDataRedactsSensitiveDataForNonAdmin(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	userToken := createAdminRouteTestSession(t, srv, "alice", "user")

	if _, err := srv.store.CreateUserAPIKey(AuthToken{UserID: 1, Name: "alice-key", Token: "bh_alice_secret", Valid: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/data", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	srv.adminAPI.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var data AdminData
	if err := json.Unmarshal(rr.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Users) != 0 || len(data.Config.Tokens) != 0 {
		t.Fatalf("non-admin data leaked sensitive fields: %+v", data)
	}
	if len(data.UserAPIKeys) != 1 || data.UserAPIKeys[0].Token == "bh_alice_secret" {
		t.Fatalf("user api keys were not limited and masked: %+v", data.UserAPIKeys)
	}
}

func TestAdminOnlyRoutesRejectNonAdmin(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	userToken := createAdminRouteTestSession(t, srv, "bob", "user")

	for _, tt := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/api/config"},
		{http.MethodPost, "/admin/api/flush-exhausted"},
		{http.MethodGet, "/admin/api/users/1"},
		{http.MethodGet, "/admin/api/users/1/api-keys"},
		{http.MethodGet, "/admin/api/users/1/usage"},
		{http.MethodGet, "/admin/api/users/1/quota"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+userToken)
			srv.adminAPI.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403, body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestUserCanReadOwnQuota(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	user, err := srv.store.CreateAppUser("quota-self-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	user, err = srv.store.UpdateUserQuota(user.ID, 12, 3, user.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	token, err := srv.createSession(user)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var status UserQuotaStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.WeeklyQuotaCredits != 12 || status.LifetimeQuotaCredits != 3 || status.Unlimited {
		t.Fatalf("quota status = %+v", status)
	}
}

func TestUserCanReadEnabledModelOptions(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	userToken := createAdminRouteTestSession(t, srv, "model-options-user", "user")
	if _, err := srv.store.CreateModel(Model{Name: "enabled-option", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateModel(Model{Name: "disabled-option", Enabled: false}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/model-options", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var models []Model
	if err := json.Unmarshal(rr.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "enabled-option" {
		t.Fatalf("model options = %+v", models)
	}
}

func TestAdminCanManageAndInspectAnotherUsersAPIKeys(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "admin", "admin")
	target, err := srv.store.CreateAppUser("target", "password", "user")
	if err != nil {
		t.Fatal(err)
	}

	createBody := bytes.NewBufferString(`{"name":"managed-key"}`)
	createdRR := httptest.NewRecorder()
	createdReq := httptest.NewRequest(http.MethodPost, "/admin/api/users/"+strconv.FormatInt(target.ID, 10)+"/api-keys", createBody)
	createdReq.Header.Set("Authorization", "Bearer "+adminToken)
	createdReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(createdRR, createdReq)
	if createdRR.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createdRR.Code, createdRR.Body.String())
	}
	var created AuthToken
	if err := json.Unmarshal(createdRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.UserID != target.ID || !strings.HasPrefix(created.Token, "bh_") {
		t.Fatalf("created key = %+v", created)
	}

	if err := srv.store.InsertUsageBatch([]UsageRecord{{
		UserID:         target.ID,
		UserName:       target.Username,
		UserAPIKeyID:   created.ID,
		UserAPIKeyName: created.Name,
		Model:          "test-model",
		Status:         http.StatusOK,
		TotalTokens:    42,
		CreatedAt:      time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}

	keysRR := httptest.NewRecorder()
	keysReq := httptest.NewRequest(http.MethodGet, "/admin/api/users/"+strconv.FormatInt(target.ID, 10)+"/api-keys", nil)
	keysReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(keysRR, keysReq)
	if keysRR.Code != http.StatusOK {
		t.Fatalf("keys status = %d, body = %s", keysRR.Code, keysRR.Body.String())
	}
	var keys []UserAPIKeyDetails
	if err := json.Unmarshal(keysRR.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].UserID != target.ID || keys[0].Token == created.Token || keys[0].Requests != 1 || keys[0].TotalTokens != 42 {
		t.Fatalf("keys = %+v", keys)
	}

	other, err := srv.store.CreateAppUser("other", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	wrongOwnerRR := httptest.NewRecorder()
	wrongOwnerReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/users/"+strconv.FormatInt(other.ID, 10)+"/api-keys/"+strconv.FormatInt(created.ID, 10),
		bytes.NewBufferString(`{"valid":false}`),
	)
	wrongOwnerReq.Header.Set("Authorization", "Bearer "+adminToken)
	wrongOwnerReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(wrongOwnerRR, wrongOwnerReq)
	if wrongOwnerRR.Code != http.StatusNotFound {
		t.Fatalf("wrong owner status = %d, want 404, body = %s", wrongOwnerRR.Code, wrongOwnerRR.Body.String())
	}

	query := url.Values{}
	query.Set("from", time.Now().Add(-time.Hour).Format(time.RFC3339))
	query.Set("to", time.Now().Add(time.Hour).Format(time.RFC3339))
	usageRR := httptest.NewRecorder()
	usageReq := httptest.NewRequest(http.MethodGet, "/admin/api/users/"+strconv.FormatInt(target.ID, 10)+"/usage?"+query.Encode(), nil)
	usageReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(usageRR, usageReq)
	if usageRR.Code != http.StatusOK {
		t.Fatalf("usage status = %d, body = %s", usageRR.Code, usageRR.Body.String())
	}
	var usage UsageSummary
	if err := json.Unmarshal(usageRR.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.Requests != 1 || usage.TotalTokens != 42 {
		t.Fatalf("usage = %+v", usage)
	}

	selfRR := httptest.NewRecorder()
	selfReq := httptest.NewRequest(http.MethodGet, "/admin/api/user-api-keys", nil)
	selfReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(selfRR, selfReq)
	if selfRR.Code != http.StatusOK {
		t.Fatalf("self keys status = %d, body = %s", selfRR.Code, selfRR.Body.String())
	}
	var selfKeys []AuthToken
	if err := json.Unmarshal(selfRR.Body.Bytes(), &selfKeys); err != nil {
		t.Fatal(err)
	}
	if len(selfKeys) != 0 {
		t.Fatalf("admin self keys leaked target keys: %+v", selfKeys)
	}
}

func TestAdminCanUpdateAndResetUserQuota(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "quota-admin", "admin")
	target, err := srv.store.CreateAppUser("quota-target", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	key, err := srv.store.CreateUserAPIKey(AuthToken{UserID: target.ID, Name: "quota-key", Token: "bh_quota_target", Valid: true})
	if err != nil {
		t.Fatal(err)
	}

	path := "/admin/api/users/" + strconv.FormatInt(target.ID, 10) + "/quota"
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(`{"weekly_quota_credits":25,"lifetime_quota_credits":5}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var status UserQuotaStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.WeeklyQuotaCredits != 25 || status.LifetimeQuotaCredits != 5 || status.PeriodEnd.IsZero() || status.Unlimited {
		t.Fatalf("quota status = %+v", status)
	}
	runtimeKey, ok := srv.authTokens[key.Token]
	if !ok || runtimeKey.WeeklyQuotaCredits != 25 || runtimeKey.LifetimeQuotaCredits != 5 || runtimeKey.QuotaAnchor.IsZero() {
		t.Fatalf("runtime key = %+v, loaded = %v", runtimeKey, ok)
	}
	anchorBeforeReset := runtimeKey.QuotaAnchor

	resetRR := httptest.NewRecorder()
	resetReq := httptest.NewRequest(http.MethodPost, path+"/reset", nil)
	resetReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(resetRR, resetReq)
	if resetRR.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body = %s", resetRR.Code, resetRR.Body.String())
	}
	runtimeKey = srv.authTokens[key.Token]
	if !runtimeKey.QuotaAnchor.After(anchorBeforeReset) {
		t.Fatalf("quota anchor was not reset: %v <= %v", runtimeKey.QuotaAnchor, anchorBeforeReset)
	}

	missingRR := httptest.NewRecorder()
	missingReq := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(`{}`))
	missingReq.Header.Set("Authorization", "Bearer "+adminToken)
	missingReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(missingRR, missingReq)
	if missingRR.Code != http.StatusBadRequest {
		t.Fatalf("missing quota status = %d, body = %s", missingRR.Code, missingRR.Body.String())
	}
}

func TestAdminCanResetAllWeeklyQuotas(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "weekly-reset-admin", "admin")
	weekly, err := srv.store.CreateAppUser("weekly-api-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	lifetime, err := srv.store.CreateAppUser("lifetime-api-user", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	weekly, err = srv.store.UpdateUserQuota(weekly.ID, 10, 0, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.UpdateUserQuota(lifetime.ID, 0, 10, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	weeklyBefore, _ := srv.store.User(weekly.ID)
	lifetimeBefore, _ := srv.store.User(lifetime.ID)
	time.Sleep(time.Millisecond)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/quotas/weekly/reset", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	weeklyAfter, _ := srv.store.User(weekly.ID)
	lifetimeAfter, _ := srv.store.User(lifetime.ID)
	if !weeklyAfter.QuotaAnchor.After(weeklyBefore.QuotaAnchor) {
		t.Fatalf("weekly anchor was not reset: %v", weeklyAfter.QuotaAnchor)
	}
	if !lifetimeAfter.QuotaAnchor.Equal(lifetimeBefore.QuotaAnchor) {
		t.Fatalf("lifetime anchor changed: %v -> %v", lifetimeBefore.QuotaAnchor, lifetimeAfter.QuotaAnchor)
	}
}

func TestAdminCanManageUserAccessAndRevokeSessions(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "access-admin", "admin")
	target, err := srv.store.CreateAppUser("access-target", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	targetToken, err := srv.createSession(target)
	if err != nil {
		t.Fatal(err)
	}
	key, err := srv.store.CreateUserAPIKey(AuthToken{UserID: target.ID, Name: "access-key", Token: "bh_access_target", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.reloadRuntimeState(); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.authTokens[key.Token]; !ok {
		t.Fatal("target API key was not loaded")
	}

	disableRR := httptest.NewRecorder()
	disableReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/users/"+strconv.FormatInt(target.ID, 10),
		bytes.NewBufferString(`{"valid":false,"role":"user"}`),
	)
	disableReq.Header.Set("Authorization", "Bearer "+adminToken)
	disableReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(disableRR, disableReq)
	if disableRR.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body = %s", disableRR.Code, disableRR.Body.String())
	}

	sessionRR := httptest.NewRecorder()
	sessionReq := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+targetToken)
	srv.adminAPI.ServeHTTP(sessionRR, sessionReq)
	if sessionRR.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user session status = %d, want 401", sessionRR.Code)
	}
	if _, ok := srv.authTokens[key.Token]; ok {
		t.Fatal("disabled user's API key remained active")
	}

	enableRR := httptest.NewRecorder()
	enableReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/users/"+strconv.FormatInt(target.ID, 10),
		bytes.NewBufferString(`{"valid":true,"role":"admin"}`),
	)
	enableReq.Header.Set("Authorization", "Bearer "+adminToken)
	enableReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(enableRR, enableReq)
	if enableRR.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body = %s", enableRR.Code, enableRR.Body.String())
	}
	var updated AppUser
	if err := json.Unmarshal(enableRR.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Valid || updated.Role != "admin" {
		t.Fatalf("updated user = %+v", updated)
	}
	if _, ok := srv.authTokens[key.Token]; !ok {
		t.Fatal("re-enabled user's API key was not restored")
	}

	targetToken, err = srv.createSession(updated)
	if err != nil {
		t.Fatal(err)
	}
	revokeRR := httptest.NewRecorder()
	revokeReq := httptest.NewRequest(http.MethodPost, "/admin/api/users/"+strconv.FormatInt(target.ID, 10)+"/sessions/revoke", nil)
	revokeReq.Header.Set("Authorization", "Bearer "+adminToken)
	srv.adminAPI.ServeHTTP(revokeRR, revokeReq)
	if revokeRR.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, body = %s", revokeRR.Code, revokeRR.Body.String())
	}

	sessionRR = httptest.NewRecorder()
	sessionReq = httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
	sessionReq.Header.Set("Authorization", "Bearer "+targetToken)
	srv.adminAPI.ServeHTTP(sessionRR, sessionReq)
	if sessionRR.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d, want 401", sessionRR.Code)
	}
}

func TestAdminUserAccessProtectsCurrentAndLastAdministrator(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "protected-admin", "admin")
	admin, err := srv.store.VerifyPassword("protected-admin", "password")
	if err != nil {
		t.Fatal(err)
	}

	selfRR := httptest.NewRecorder()
	selfReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/api/users/"+strconv.FormatInt(admin.ID, 10),
		bytes.NewBufferString(`{"valid":false,"role":"user"}`),
	)
	selfReq.Header.Set("Authorization", "Bearer "+adminToken)
	selfReq.Header.Set("Content-Type", "application/json")
	srv.adminAPI.ServeHTTP(selfRR, selfReq)
	if selfRR.Code != http.StatusBadRequest {
		t.Fatalf("self update status = %d, want 400, body = %s", selfRR.Code, selfRR.Body.String())
	}

	actor, err := srv.store.CreateAppUser("non-admin-actor", "password", "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.UpdateAppUser(actor.ID, admin.ID, "user", true); err == nil || !strings.Contains(err.Error(), "last active administrator") {
		t.Fatalf("last administrator update error = %v", err)
	}
}

func TestPublicStatsAndFlushAreNotExposed(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	srv.keyState.MarkExhausted("gemini", APIKey{Name: "k1"})

	for _, path := range []string{"/stats", "/flush-exhausted"} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rr.Code)
		}
	}
	if len(srv.keyState.SnapshotExhausted()) == 0 {
		t.Fatal("public flush-exhausted cleared key cooldown state")
	}
}

func TestPublicAdminRouteWrongMethodDoesNotBypassAuth(t *testing.T) {
	srv := newAdminRouteTestServer(t)

	rr := httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin/api/login", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func newAdminRouteTestServer(t *testing.T) *Server {
	t.Helper()
	store := openTestStore(t)
	srv := &Server{
		store:         store,
		adminSessions: make(map[string]SessionUser),
		keyState: &KeyState{
			cooldown:  time.Minute,
			exhausted: make(map[string]time.Time),
			errors:    make(map[string]KeyError),
		},
		stats: Stats{
			StartedAt: time.Now(),
			Exhausted: make(map[string]string),
			KeyErrors: make(map[string]KeyError),
		},
	}
	srv.cfg.Upstream.Timeout = "10m"
	srv.cfg.Retry.CooldownSeconds = 60
	srv.adminAPI = srv.newAdminAPI()
	return srv
}

func createAdminRouteTestSession(t *testing.T, srv *Server, username, role string) string {
	t.Helper()
	user, err := srv.store.CreateAppUser(username, "password", role)
	if err != nil {
		t.Fatal(err)
	}
	token, err := srv.createSession(user)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAdminAPIReturnsVersion(t *testing.T) {
	srv := newAdminRouteTestServer(t)

	// Test setup-state returns version
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/setup-state", nil)
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("setup-state status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var setupResp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &setupResp); err != nil {
		t.Fatal(err)
	}
	if setupResp.Version != Version {
		t.Fatalf("setup-state version = %q, want %q", setupResp.Version, Version)
	}

	// Test session returns version
	userToken := createAdminRouteTestSession(t, srv, "versionuser", "user")
	rrSession := httptest.NewRecorder()
	reqSession := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
	reqSession.Header.Set("Authorization", "Bearer "+userToken)
	srv.adminAPI.ServeHTTP(rrSession, reqSession)
	if rrSession.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", rrSession.Code, rrSession.Body.String())
	}
	var sessionResp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rrSession.Body.Bytes(), &sessionResp); err != nil {
		t.Fatal(err)
	}
	if sessionResp.Version != Version {
		t.Fatalf("session version = %q, want %q", sessionResp.Version, Version)
	}
}
