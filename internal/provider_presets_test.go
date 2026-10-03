package buzz

import "testing"

func TestMimoPresetsDefineProtocolSpecificEndpoints(t *testing.T) {
	tests := []struct {
		id               string
		openAIBaseURL    string
		anthropicBaseURL string
	}{
		{
			id:               "mimo",
			openAIBaseURL:    "https://api.xiaomimimo.com/v1",
			anthropicBaseURL: "https://api.xiaomimimo.com/anthropic",
		},
		{
			id:               "mimo-plan",
			openAIBaseURL:    "https://token-plan-cn.xiaomimimo.com/v1",
			anthropicBaseURL: "https://token-plan-cn.xiaomimimo.com/anthropic",
		},
	}

	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			preset, ok := findProviderPreset(test.id)
			if !ok {
				t.Fatalf("preset %q not found", test.id)
			}
			provider := preset.Provider()
			got := make(map[string]string, len(provider.Endpoints))
			for _, endpoint := range provider.Endpoints {
				got[endpoint.Protocol] = endpoint.BaseURL
			}
			if got[providerOpenAI] != test.openAIBaseURL {
				t.Fatalf("OpenAI endpoint = %q, want %q", got[providerOpenAI], test.openAIBaseURL)
			}
			if got[providerAnthropic] != test.anthropicBaseURL {
				t.Fatalf("Anthropic endpoint = %q, want %q", got[providerAnthropic], test.anthropicBaseURL)
			}
		})
	}
}

func TestDeepSeekPresetAutoRoutesToMatchingProtocol(t *testing.T) {
	preset, ok := findProviderPreset("deepseek")
	if !ok {
		t.Fatal("DeepSeek preset not found")
	}
	provider := preset.Provider()
	if !provider.Enabled || len(provider.Endpoints) != 2 {
		t.Fatalf("DeepSeek provider = %+v", provider)
	}
	targets := make([]RouteTarget, 0, len(provider.Endpoints))
	for _, endpoint := range provider.Endpoints {
		if !endpoint.Enabled || endpoint.BaseURL != "https://api.deepseek.com" {
			t.Fatalf("DeepSeek endpoint = %+v", endpoint)
		}
		targets = append(targets, RouteTarget{
			ID: 1, ProviderName: provider.Name, ProviderType: endpoint.Protocol,
			RouteProtocol: providerAuto, UpstreamModel: "deepseek-flash",
		})
	}
	for _, test := range []struct {
		protocol   string
		preference []string
	}{
		{providerOpenAI, openAIChatProtocolPreference()},
		{providerOpenAIResponses, openAIResponsesProtocolPreference()},
	} {
		t.Run(test.protocol, func(t *testing.T) {
			srv := &Server{}
			selected := srv.selectRouteTargets("deepseek-flash", targets, test.preference)
			if len(selected) != 1 || selected[0].ProviderType != test.protocol {
				t.Fatalf("auto route = %+v, want %s passthrough", selected, test.protocol)
			}
		})
	}
}
