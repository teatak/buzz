package buzzhive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeUpstreamModelMetadata(t *testing.T) {
	ptrFloat := func(f float64) *float64 { return &f }
	for _, tc := range []struct {
		name, protocol, body string
		want                 []upstreamModel
	}{
		{"OpenRouter", providerOpenAI, `{"data":[{"id":" vendor/next ","name":"Next","context_length":65536,"architecture":{"input_modalities":["text","image"]},"supported_parameters":["tools","reasoning","structured_outputs"],"top_provider":{"max_completion_tokens":8192}},{"id":"text","architecture":{"input_modalities":["text"]},"supported_parameters":[]}]}`, []upstreamModel{
			{ID: "vendor/next", Name: "Next", ModelMetadata: ModelMetadata{ContextWindow: 65536, MaxOutputTokens: 8192, Capabilities: map[string]bool{"vision": true, "audio_input": false, "tools": true, "reasoning": true, "json_schema": true}}},
			{ID: "text", ModelMetadata: ModelMetadata{Capabilities: map[string]bool{"vision": false, "audio_input": false, "tools": false, "reasoning": false, "json_schema": false}}},
		}},
		{"OpenRouterWithPricing", providerOpenAI, `{"data":[{"id":"openrouter/claude-3.5-sonnet","name":"Claude 3.5 Sonnet","context_length":200000,"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003"}},{"id":"openrouter/numeric-pricing","pricing":{"prompt":0.0000025,"completion":0.00001}}]}`, []upstreamModel{
			{ID: "openrouter/claude-3.5-sonnet", Name: "Claude 3.5 Sonnet", ModelMetadata: ModelMetadata{ContextWindow: 200000, QuotaUncachedInputRate: ptrFloat(3000), QuotaOutputRate: ptrFloat(15000), QuotaCachedInputRate: ptrFloat(300), Capabilities: map[string]bool{}}},
			{ID: "openrouter/numeric-pricing", ModelMetadata: ModelMetadata{QuotaUncachedInputRate: ptrFloat(2500), QuotaOutputRate: ptrFloat(10000), Capabilities: map[string]bool{}}},
		}},
		{"Responses", providerOpenAIResponses, `{"data":[{"id":"audio","architecture":{"input_modalities":["text","audio"]},"supported_parameters":["response_format"]}]}`, []upstreamModel{{ID: "audio", ModelMetadata: ModelMetadata{Capabilities: map[string]bool{"vision": false, "audio_input": true, "tools": false, "reasoning": false, "json_schema": false}}}}},
		{"Unknown", providerOpenAI, `{"data":[{"id":"new-model","context_length":-1,"architecture":{"input_modalities":null},"supported_parameters":null,"top_provider":{"max_completion_tokens":0}},{"id":"new-model"},{"id":" "}]}`, []upstreamModel{{ID: "new-model", ModelMetadata: ModelMetadata{Capabilities: map[string]bool{}}}}},
		{"Claude", providerAnthropic, `{"data":[{"id":"claude-custom","display_name":"Custom","max_input_tokens":100000,"max_tokens":5000,"capabilities":{"image_input":{"supported":false},"thinking":{"supported":true}}}]}`, []upstreamModel{{ID: "claude-custom", Name: "Custom", ModelMetadata: ModelMetadata{ContextWindow: 100000, MaxInputTokens: 100000, MaxOutputTokens: 5000, Capabilities: map[string]bool{"vision": false, "reasoning": true}}}}},
		{"Gemini", providerGemini, `{"models":[{"name":"models/custom","displayName":"Custom","inputTokenLimit":30000,"outputTokenLimit":2000,"supportedGenerationMethods":["generateContent"],"thinking":false},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}]}`, []upstreamModel{{ID: "custom", Name: "Custom", ModelMetadata: ModelMetadata{ContextWindow: 30000, MaxInputTokens: 30000, MaxOutputTokens: 2000, Capabilities: map[string]bool{"reasoning": false}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeUpstreamModels(strings.NewReader(tc.body), tc.protocol)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, error %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestCatalogUsesOnlyExactPresetsAndKeepsRemoteFalse(t *testing.T) {
	models, err := decodeUpstreamModels(strings.NewReader(`{"data":[{"id":"deepseek-flash"},{"id":"deepseek-flash","context_length":99},{"id":"vendor/deepseek-flash"},{"id":"deepseek-v4-pro","context_length":60000,"supported_parameters":[]}]}`), providerOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	preset, _ := findModelPreset("deepseek-flash")
	if len(models) != 3 || models[0].ContextWindow != preset.ContextWindow || models[0].MaxOutputTokens != preset.MaxOutputTokens || models[1].ContextWindow != 0 || len(models[1].Capabilities) != 0 {
		t.Fatalf("models = %+v", models)
	}
	if models[2].ContextWindow != 60000 || models[2].Capabilities["tools"] || models[2].Capabilities["reasoning"] {
		t.Fatalf("remote false/limits overwritten: %+v", models[2])
	}
}

func TestRouteCatalogImportAndPublicRoundTrip(t *testing.T) {
	srv := newAdminRouteTestServer(t)
	token := createAdminRouteTestSession(t, srv, "catalog-admin", "admin")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Errorf("unexpected discovery request: %s", r.URL.Path)
		}
		w.Write([]byte(`{"data":[{"id":"vendor/next","name":"Remote Name","context_length":65536,"pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003"},"architecture":{"input_modalities":["text","image"]},"supported_parameters":["tools","reasoning"],"top_provider":{"max_completion_tokens":8192}}]}`))
	}))
	defer upstream.Close()
	provider, err := srv.store.CreateProvider(ProviderRecord{Name: "Catalog", Enabled: true, Endpoints: []ProviderEndpoint{{Protocol: providerOpenAI, BaseURL: upstream.URL + "/api/v1", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.CreateProviderKey(ProviderKey{ProviderID: provider.ID, Name: "test", Secret: "fixture-secret", Enabled: true, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	model, err := srv.store.CreateModel(Model{Name: "public-alias", DisplayName: "Local Name", Icon: "deepseek", Description: "Local description", ContextWindow: 1000, MaxOutputTokens: 200, Capabilities: `{"stream":true,"vision":false,"audio_input":true,"tools":false,"reasoning":false,"json_schema":true}`, QuotaUncachedInputRate: 42, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		srv.adminAPI.ServeHTTP(rr, req)
		return rr
	}
	rr := call(http.MethodGet, fmt.Sprintf("/admin/api/providers/%d/upstream-models?protocol=auto", provider.ID), nil)
	var candidates []upstreamModel
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &candidates) != nil || len(candidates) != 1 {
		t.Fatalf("discovery: %d %s", rr.Code, rr.Body.String())
	}
	body := map[string]any{"model_id": model.ID, "provider_id": provider.ID, "upstream_model": candidates[0].ID, "model_metadata": candidates[0].ModelMetadata}
	rr = call(http.MethodPost, "/admin/api/model-routes", body)
	var route ModelRoute
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &route) != nil {
		t.Fatalf("save: %d %s", rr.Code, rr.Body.String())
	}
	saved, _ := srv.store.Model(model.ID)
	if saved.Name != model.Name || saved.DisplayName != model.DisplayName || saved.Icon != model.Icon || saved.Description != model.Description || saved.QuotaUncachedInputRate != 3000 || saved.QuotaOutputRate != 15000 || saved.QuotaCachedInputRate != 300 || saved.ContextWindow != 65536 || saved.MaxOutputTokens != 8192 {
		t.Fatalf("saved model = %+v", saved)
	}
	// Exercise the actual public JSON, then parse it like another client/proxy.
	public := httptest.NewRecorder()
	srv.handleOpenAIModels(public, httptest.NewRequest(http.MethodGet, "/v1/models", nil), AuthToken{})
	roundtrip, err := decodeUpstreamModels(bytes.NewReader(public.Body.Bytes()), providerOpenAI)
	if err != nil || len(roundtrip) != 1 || roundtrip[0].ContextWindow != 65536 || roundtrip[0].MaxOutputTokens != 8192 || !roundtrip[0].Capabilities["vision"] || roundtrip[0].Capabilities["audio_input"] || roundtrip[0].Capabilities["json_schema"] || roundtrip[0].QuotaUncachedInputRate == nil || *roundtrip[0].QuotaUncachedInputRate != 3000 || roundtrip[0].QuotaOutputRate == nil || *roundtrip[0].QuotaOutputRate != 15000 || roundtrip[0].QuotaCachedInputRate == nil || *roundtrip[0].QuotaCachedInputRate != 300 {
		t.Fatalf("public roundtrip: %s / %v", public.Body.String(), err)
	}
	// A second route without opting in must leave the shared model unchanged.
	delete(body, "model_metadata")
	body["upstream_model"] = "other"
	rr = call(http.MethodPost, "/admin/api/model-routes", body)
	if rr.Code != 200 {
		t.Fatalf("second route: %s", rr.Body.String())
	}
	after, _ := srv.store.Model(model.ID)
	if !reflect.DeepEqual(saved, after) {
		t.Fatal("route without import changed model")
	}
	// Editing an existing route can explicitly apply partial metadata, including false.
	body["id"] = route.ID
	body["model_metadata"] = ModelMetadata{MaxOutputTokens: 4096, Capabilities: map[string]bool{"tools": false}}
	rr = call(http.MethodPut, "/admin/api/model-routes", body)
	if rr.Code != 200 {
		t.Fatalf("edit route: %s", rr.Body.String())
	}
	after, _ = srv.store.Model(model.ID)
	if after.ContextWindow != 65536 || after.MaxOutputTokens != 4096 || savedModelCapabilities(after.Capabilities)["tools"] || !savedModelCapabilities(after.Capabilities)["vision"] {
		t.Fatalf("partial import = %+v", after)
	}
	// A model update failure must roll back the newly inserted route as well.
	if _, err := srv.store.exec(`CREATE FUNCTION reject_metadata() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END; $$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.exec(`CREATE TRIGGER reject_metadata BEFORE UPDATE ON models FOR EACH ROW EXECUTE FUNCTION reject_metadata()`); err != nil {
		t.Fatal(err)
	}
	delete(body, "id")
	body["upstream_model"] = "must-rollback"
	rr = call(http.MethodPost, "/admin/api/model-routes", body)
	if rr.Code == 200 {
		t.Fatal("expected failed metadata update")
	}
	routes, _ := srv.store.ModelRoutes(model.ID)
	if len(routes) != 2 {
		t.Fatalf("route did not roll back: %+v", routes)
	}
	final, _ := srv.store.Model(model.ID)
	if !reflect.DeepEqual(after, final) {
		t.Fatal("failed save changed model")
	}
}
