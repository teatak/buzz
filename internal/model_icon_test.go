package buzz

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAdminModelIconCreateUpdateAndClear(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	token := createAdminRouteTestSession(t, srv, "icon-admin", "admin")
	write := func(method string, payload map[string]any) Model {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "/admin/api/models", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		srv.adminAPI.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s model: status %d, body %s", method, rr.Code, rr.Body.String())
		}
		var model Model
		if err := json.Unmarshal(rr.Body.Bytes(), &model); err != nil {
			t.Fatal(err)
		}
		return model
	}

	created := write(http.MethodPost, map[string]any{"name": "work-assistant", "icon": "deepseek"})
	if created.Icon != "deepseek" {
		t.Fatalf("created icon = %q", created.Icon)
	}
	updated := write(http.MethodPut, map[string]any{"id": created.ID, "name": "renamed", "icon": "claude"})
	if updated.Icon != "claude" || updated.Name != "renamed" {
		t.Fatalf("updated model = %+v", updated)
	}
	updated = write(http.MethodPut, map[string]any{"id": created.ID, "display_name": "Friendly name"})
	if updated.Icon != "claude" {
		t.Fatalf("omitted icon changed selection to %q", updated.Icon)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/api/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.adminAPI.ServeHTTP(rr, req)
	var models []Model
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &models) != nil || len(models) != 1 || models[0].Icon != "claude" {
		t.Fatalf("model list: status %d, body %s", rr.Code, rr.Body.String())
	}

	cleared := write(http.MethodPut, map[string]any{"id": created.ID, "icon": ""})
	stored, err := srv.store.Model(created.ID)
	if err != nil || cleared.Icon != "" || stored.Icon != "" {
		t.Fatalf("restore automatic: response %+v, stored %+v, error %v", cleared, stored, err)
	}
	created = write(http.MethodPost, map[string]any{"name": "automatic-model"})
	if created.Icon != "" {
		t.Fatalf("default icon = %q, want automatic", created.Icon)
	}
}

func TestModelIconMigrationPreservesModelsAndSurvivesReopen(t *testing.T) {
	store := openTestStoreWithSetup(t, func(db *sql.DB) {
		_, err := db.Exec(`CREATE TABLE models (
			id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '',
			context_window BIGINT NOT NULL DEFAULT 0, max_input_tokens BIGINT NOT NULL DEFAULT 0,
			max_output_tokens BIGINT NOT NULL DEFAULT 0,
			quota_uncached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			quota_cached_input_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			quota_output_rate NUMERIC(20,6) NOT NULL DEFAULT 1,
			capabilities TEXT NOT NULL DEFAULT '{}', selection_policy TEXT NOT NULL DEFAULT 'round_robin',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		INSERT INTO models (name, display_name, context_window, quota_output_rate)
		VALUES ('custom-legacy', 'Original model', 128000, 7)`)
		if err != nil {
			t.Fatal(err)
		}
	})
	models, err := store.Models()
	if err != nil || len(models) != 1 {
		t.Fatalf("migrated models = %+v, error %v", models, err)
	}
	model := models[0]
	if model.Icon != "" || model.DisplayName != "Original model" || model.ContextWindow != 128000 || model.QuotaOutputRate != 7 {
		t.Fatalf("migration changed existing model: %+v", model)
	}
	model.Icon = "deepseek"
	if _, err := store.UpdateModel(model); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := store.db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(DatabaseConfig{URL: databaseURLWithSearchPath(os.Getenv("BUZZ_TEST_DATABASE_URL"), schema)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	saved, err := reopened.Model(model.ID)
	if err != nil || saved.Icon != "deepseek" || saved.QuotaOutputRate != 7 {
		t.Fatalf("reopened model = %+v, error %v", saved, err)
	}
	if err := reopened.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	saved, err = reopened.Model(model.ID)
	if err != nil || saved.Icon != "deepseek" {
		t.Fatalf("repeated migration cleared icon: %+v, error %v", saved, err)
	}
}
