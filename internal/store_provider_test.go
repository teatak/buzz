package buzz

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestProviderAPIKeysUseProviderTables(t *testing.T) {
	store := openTestStore(t)

	provider, err := store.CreateProvider(ProviderRecord{
		Name: providerGemini,
		Endpoints: []ProviderEndpoint{{
			Protocol: providerGemini,
			BaseURL:  "https://gemini.example.test",
			Enabled:  true,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	createdKey, err := store.CreateProviderKey(ProviderKey{
		ProviderID: provider.ID,
		Name:       "k01-alpha",
		Secret:     "AIza-secret-alpha",
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	keys, err := store.ProviderAPIKeys(providerGemini)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("provider keys = %d, want 1", len(keys))
	}
	key := keys[0]
	if key.ProviderName != providerGemini || key.ProviderKeyID != createdKey.ID {
		t.Fatalf("provider metadata = %+v", key)
	}
	if key.Name != "k01-alpha" || key.Key != "AIza-secret-alpha" {
		t.Fatalf("provider key = %+v", key)
	}

	if err := store.DisableProviderKey(key.ID, 403, "PERMISSION_DENIED", "disabled", "body"); err != nil {
		t.Fatal(err)
	}
	keys, err = store.ProviderAPIKeys(providerGemini)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("enabled provider keys = %d, want 0", len(keys))
	}

	var status int
	if err := store.queryRow(`SELECT disabled_status FROM provider_keys WHERE name = ?`, "k01-alpha").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != 403 {
		t.Fatalf("disabled_status = %d, want 403", status)
	}
}

func TestDisableProviderKey(t *testing.T) {
	store := openTestStore(t)

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	providerID, err := store.insertReturningID(
		`INSERT INTO providers (name, preset_id, enabled, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
		"openrouter", "openrouter", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.exec(
		`INSERT INTO provider_endpoints (provider_id, protocol, base_url, enabled, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)`,
		providerID, providerOpenAI, "https://openrouter.example.test/v1", now, now,
	); err != nil {
		t.Fatal(err)
	}
	keyID, err := store.insertReturningID(
		`INSERT INTO provider_keys (provider_id, name, secret, secret_hint, enabled, priority, weight, labels, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 0, 1, '', ?, ?)`,
		providerID, "or-main", "sk-secret", "cret", now, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.DisableProviderKey(keyID, 401, "unauthorized", "bad key", "body"); err != nil {
		t.Fatal(err)
	}
	keys, err := store.RuntimeProviderAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("enabled keys = %d, want 0", len(keys))
	}
	var status int
	if err := store.queryRow(`SELECT disabled_status FROM provider_keys WHERE id = ?`, keyID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != 401 {
		t.Fatalf("disabled_status = %d, want 401", status)
	}
}

func TestProviderManagementCRUD(t *testing.T) {
	store := openTestStore(t)

	provider, err := store.CreateProvider(ProviderRecord{
		Name: "openrouter",
		Endpoints: []ProviderEndpoint{
			{
				Protocol: providerOpenAI,
				BaseURL:  "https://openrouter.example.test/v1",
				Enabled:  true,
			},
			{
				Protocol: providerAnthropic,
				BaseURL:  "https://openrouter.example.test/anthropic",
				Enabled:  true,
			},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateProviderKey(ProviderKey{
		ProviderID: provider.ID,
		Secret:     "sk-test-secret",
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if key.Secret == "sk-test-secret" || key.SecretHint != "cret" {
		t.Fatalf("key masking/hint = %+v", key)
	}

	model, err := store.CreateModel(Model{
		Name:                   "mimo-v2.5",
		QuotaUncachedInputRate: 1.25,
		QuotaCachedInputRate:   0.1,
		QuotaOutputRate:        3.5,
		Enabled:                true,
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := store.SaveModelRoute(ModelRoute{
		ModelID:          model.ID,
		ProviderID:       provider.ID,
		UpstreamProtocol: providerAuto,
		UpstreamModel:    "mimo/v2.5",
		Enabled:          true,
		Weight:           2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if route.ProviderName != "openrouter" || route.UpstreamProtocol != providerAuto {
		t.Fatalf("route provider metadata = %+v", route)
	}

	targets, ok, err := store.ResolveModelRoutes("mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(targets) != 2 || targets[0].RouteProtocol != providerAuto || targets[1].RouteProtocol != providerAuto || targets[0].UpstreamModel != "mimo/v2.5" {
		t.Fatalf("resolved targets = %+v, ok=%v", targets, ok)
	}
	if targets[0].QuotaUncachedInputRate != 1.25 || targets[0].QuotaCachedInputRate != 0.1 || targets[0].QuotaOutputRate != 3.5 {
		t.Fatalf("resolved quota rates = %+v", targets[0])
	}

	endpointID := provider.Endpoints[0].ID
	provider.Endpoints[0].BaseURL = "https://openrouter.example.test/proxy/v1"
	provider, err = store.UpdateProvider(provider)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Endpoints[0].ID != endpointID {
		t.Fatalf("provider endpoint id = %d, want %d", provider.Endpoints[0].ID, endpointID)
	}
	route, err = store.ModelRoute(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if route.ProviderID != provider.ID || route.UpstreamProtocol != providerAuto {
		t.Fatalf("route after provider update = %+v", route)
	}
	if _, err := store.SaveModelRoute(ModelRoute{
		ModelID:          model.ID,
		ProviderID:       provider.ID,
		UpstreamProtocol: providerGemini,
		UpstreamModel:    "unsupported",
		Enabled:          true,
	}, nil); err == nil {
		t.Fatal("expected unsupported provider protocol to be rejected")
	}

	if err := store.DeleteProvider(provider.ID); err != nil {
		t.Fatal(err)
	}
	targets, ok, err = store.ResolveModelRoutes("mimo-v2.5")
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(targets) != 0 {
		t.Fatalf("targets after provider delete = %+v, ok=%v", targets, ok)
	}
}

func TestProviderEndpointDisabledIsPreservedAndExcludedFromRoutes(t *testing.T) {
	store := openTestStore(t)

	provider, err := store.CreateProvider(ProviderRecord{
		Name: "disabled-endpoint-provider",
		Endpoints: []ProviderEndpoint{{
			Protocol: providerOpenAI,
			BaseURL:  "https://disabled.example.test/v1",
			Enabled:  false,
		}},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.Endpoints) != 1 || provider.Endpoints[0].Enabled {
		t.Fatalf("provider endpoints = %+v", provider.Endpoints)
	}

	model, err := store.CreateModel(Model{Name: "disabled-route-model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveModelRoute(ModelRoute{
		ModelID:          model.ID,
		ProviderID:       provider.ID,
		UpstreamProtocol: providerOpenAI,
		UpstreamModel:    "upstream-model",
		Enabled:          true,
		Weight:           1,
	}, nil); err != nil {
		t.Fatal(err)
	}

	targets, ok, err := store.ResolveModelRoutes("disabled-route-model")
	if err != nil {
		t.Fatal(err)
	}
	if ok || len(targets) != 0 {
		t.Fatalf("resolved disabled endpoint targets = %+v, ok=%v", targets, ok)
	}
}

func TestAdminModelUpdateAllowsEmptyDisplayName(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "admin", "admin")

	model, err := srv.store.CreateModel(Model{
		Name:            "owl-alpha",
		DisplayName:     "Owl Alpha",
		ContextWindow:   1050000,
		MaxInputTokens:  1050000,
		MaxOutputTokens: 262100,
		Capabilities:    `{"stream":true}`,
		SelectionPolicy: "round_robin",
		Enabled:         true,
	})
	if err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"id":` + strconv.FormatInt(model.ID, 10) + `,"name":"owl-alpha","display_name":"","description":"","context_window":1050000,"max_input_tokens":1050000,"max_output_tokens":262100,"capabilities":"{\"stream\":true}","selection_policy":"round_robin","enabled":true}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/models", body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var updated Model
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "" {
		t.Fatalf("display_name = %q, want empty", updated.DisplayName)
	}
}

func TestAdminModelCreateAllowsEmptyDisplayName(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "admin", "admin")

	body := bytes.NewBufferString(`{"name":"owl-beta","display_name":"","description":"","context_window":1050000,"max_input_tokens":1050000,"max_output_tokens":262100,"capabilities":"{\"stream\":true}","selection_policy":"round_robin","enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/models", body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var created Model
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.DisplayName != "" {
		t.Fatalf("display_name = %q, want empty", created.DisplayName)
	}
}

func TestAdminProviderRoutesCreateRuntimeProvider(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	adminToken := createAdminRouteTestSession(t, srv, "admin", "admin")

	body := bytes.NewBufferString(`{"name":"openai","endpoints":[{"protocol":"openai","base_url":"https://api.openai.example/v1","enabled":true}],"enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/api/providers", body)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr := httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var provider ProviderRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}
	if provider.ID == 0 || provider.Name != "openai" {
		t.Fatalf("provider = %+v", provider)
	}
	if _, ok := srv.providers[providerRuntimeKey("openai", providerOpenAI)]; !ok {
		t.Fatalf("runtime providers = %+v, want openai", srv.providers)
	}

	updateBody := bytes.NewBufferString(`{"id":` + strconv.FormatInt(provider.ID, 10) + `,"name":"openai","preset_id":"","endpoints":[{"protocol":"openai","base_url":"https://api.openai.example/v2","enabled":true}],"enabled":true}`)
	req = httptest.NewRequest(http.MethodPut, "/admin/api/providers", updateBody)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rr = httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var updated ProviderRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != provider.ID || len(updated.Endpoints) != 1 || updated.Endpoints[0].BaseURL != "https://api.openai.example/v2" {
		t.Fatalf("updated provider = %+v, want id %d and v2 base URL", updated, provider.ID)
	}
	if updated.PresetID != "" {
		t.Fatalf("preset_id = %q, want empty", updated.PresetID)
	}
	providers, err := srv.store.Providers()
	if err != nil {
		t.Fatal(err)
	}
	openaiCount := 0
	for _, item := range providers {
		if item.Name == "openai" {
			openaiCount++
		}
	}
	if openaiCount != 1 {
		t.Fatalf("openai providers = %d, want 1 after update; providers = %+v", openaiCount, providers)
	}
}
