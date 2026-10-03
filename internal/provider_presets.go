package buzz

type ProviderPresetEndpoint struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
}

type ProviderPreset struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Endpoints   []ProviderPresetEndpoint `json:"endpoints"`
	Description string                   `json:"description"`
}

func providerPresets() []ProviderPreset {
	return []ProviderPreset{
		{ID: "gemini", Name: "Google Gemini", Endpoints: []ProviderPresetEndpoint{{Protocol: "gemini", BaseURL: "https://generativelanguage.googleapis.com"}}, Description: "Google Gemini native API."},
		{ID: "openai", Name: "OpenAI", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://api.openai.com/v1"}, {Protocol: "openai-responses", BaseURL: "https://api.openai.com/v1"}}, Description: "OpenAI official API."},
		{ID: "anthropic", Name: "Anthropic Claude", Endpoints: []ProviderPresetEndpoint{{Protocol: "anthropic", BaseURL: "https://api.anthropic.com"}}, Description: "Anthropic native Messages API."},
		{ID: "mimo", Name: "MiMo", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://api.xiaomimimo.com/v1"}, {Protocol: "anthropic", BaseURL: "https://api.xiaomimimo.com/anthropic"}}, Description: "Xiaomi MiMo OpenAI- and Anthropic-compatible endpoints."},
		{ID: "mimo-plan", Name: "MiMo Plan", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1"}, {Protocol: "anthropic", BaseURL: "https://token-plan-cn.xiaomimimo.com/anthropic"}}, Description: "Xiaomi MiMo subscription endpoints."},
		{ID: "deepseek", Name: "DeepSeek", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://api.deepseek.com"}, {Protocol: "openai-responses", BaseURL: "https://api.deepseek.com"}}, Description: "DeepSeek Chat Completions and Responses endpoints."},
		{ID: "qwen", Name: "Qwen", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1"}}, Description: "Alibaba Bailian Qwen OpenAI-compatible endpoint."},
		{ID: "moonshot", Name: "Kimi", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://api.moonshot.cn/v1"}}, Description: "Moonshot Kimi OpenAI-compatible endpoint."},
		{ID: "zhipu", Name: "GLM", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4"}}, Description: "Zhipu GLM OpenAI-compatible endpoint."},
		{ID: "openrouter", Name: "OpenRouter", Endpoints: []ProviderPresetEndpoint{{Protocol: "openai", BaseURL: "https://openrouter.ai/api/v1"}}, Description: "OpenRouter OpenAI-compatible router."},
	}
}

func findProviderPreset(id string) (ProviderPreset, bool) {
	for _, preset := range providerPresets() {
		if preset.ID == id {
			return preset, true
		}
	}
	return ProviderPreset{}, false
}

func (p ProviderPreset) Provider() ProviderRecord {
	endpoints := make([]ProviderEndpoint, 0, len(p.Endpoints))
	for _, endpoint := range p.Endpoints {
		endpoints = append(endpoints, ProviderEndpoint{
			Protocol: endpoint.Protocol,
			BaseURL:  endpoint.BaseURL,
			Enabled:  true,
		})
	}
	return ProviderRecord{
		Name:      p.Name,
		PresetID:  p.ID,
		Endpoints: endpoints,
		Enabled:   true,
	}
}
