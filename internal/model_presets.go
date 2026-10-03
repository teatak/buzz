package buzz

import (
	"encoding/json"
	"strings"
)

type ModelPreset struct {
	ID              string `json:"id"`
	Family          string `json:"family"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
	ContextWindow   int64  `json:"context_window"`
	MaxInputTokens  int64  `json:"max_input_tokens"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	Capabilities    string `json:"capabilities"`
	SelectionPolicy string `json:"selection_policy"`
}

// Creation templates only; saved models remain authoritative.
// Verified on 2026-09-14; sources and protocol limits: docs/model-presets.zh-CN.md.
func modelPresets() []ModelPreset {
	capText := modelPresetCapabilities(false, false)
	capVision := modelPresetCapabilities(true, false)
	capMultimodal := modelPresetCapabilities(true, true)

	return []ModelPreset{
		{ID: "gemini-3.8-flash", Family: "Gemini", Name: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Description: "Google multimodal model for coding and agent workflows.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 65536, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},
		{ID: "gemini-3.5-flash-lite", Family: "Gemini", Name: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash-Lite", Description: "Google low-latency multimodal model.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 65536, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},
		{ID: "gemini-3.1-pro-preview", Family: "Gemini", Name: "gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro Preview", Description: "Google pro multimodal preview model.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 65536, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},

		{ID: "gpt-6-astra", Family: "OpenAI", Name: "gpt-6-astra", DisplayName: "GPT 6 Astra", Description: "OpenAI flagship reasoning model. Tool calling requires Responses; omit temperature and top_p.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-6.1-sol", Family: "OpenAI", Name: "gpt-6.1-sol", DisplayName: "GPT 6.1 Sol", Description: "OpenAI flagship coding and reasoning multimodal model.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-6-luna", Family: "OpenAI", Name: "gpt-6-luna", DisplayName: "GPT 6 Luna", Description: "OpenAI fast low-latency multimodal model.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-5.6-luna", Family: "OpenAI", Name: "gpt-5.6-luna", DisplayName: "GPT 5.6 Luna", Description: "OpenAI multimodal model.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-5.5", Family: "OpenAI", Name: "gpt-5.5", DisplayName: "GPT 5.5", Description: "OpenAI multimodal model.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-5.4", Family: "OpenAI", Name: "gpt-5.4", DisplayName: "GPT 5.4", Description: "OpenAI multimodal model.", ContextWindow: 1050000, MaxInputTokens: 1050000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-5.4-mini", Family: "OpenAI", Name: "gpt-5.4-mini", DisplayName: "GPT 5.4 Mini", Description: "OpenAI mini multimodal model.", ContextWindow: 400000, MaxInputTokens: 400000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "gpt-5.4-nano", Family: "OpenAI", Name: "gpt-5.4-nano", DisplayName: "GPT 5.4 Nano", Description: "OpenAI nano multimodal model.", ContextWindow: 400000, MaxInputTokens: 400000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},

		{ID: "claude-fable-5.1", Family: "Anthropic", Name: "claude-fable-5.1", DisplayName: "Claude Fable 5.1", Description: "Anthropic model for demanding reasoning and long-running agents.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "claude-opus-5.5", Family: "Anthropic", Name: "claude-opus-5.5", DisplayName: "Claude Opus 5.5", Description: "Anthropic flagship reasoning and complex agent model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "claude-opus-5", Family: "Anthropic", Name: "claude-opus-5", DisplayName: "Claude Opus 5", Description: "Anthropic advanced coding and reasoning model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "claude-sonnet-5.5", Family: "Anthropic", Name: "claude-sonnet-5.5", DisplayName: "Claude Sonnet 5.5", Description: "Anthropic balanced coding and reasoning model with adaptive thinking.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "claude-haiku-4-5", Family: "Anthropic", Name: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", Description: "Anthropic fast model.", ContextWindow: 200000, MaxInputTokens: 200000, MaxOutputTokens: 64000, Capabilities: capVision, SelectionPolicy: "round_robin"},

		// V2.6 verified 2026-09-22; creation templates do not overwrite saved models.
		{ID: "mimo-v2.6-flash", Family: "MiMo", Name: "mimo-v2.6-flash", DisplayName: "MiMo V2.6 Flash", Description: "Xiaomi multimodal model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 131072, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},
		{ID: "mimo-v2.6-pro", Family: "MiMo", Name: "mimo-v2.6-pro", DisplayName: "MiMo V2.6 Pro", Description: "Xiaomi flagship multimodal reasoning model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 131072, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},
		{ID: "mimo-v2.5", Family: "MiMo", Name: "mimo-v2.5", DisplayName: "MiMo V2.5", Description: "Xiaomi multimodal model.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 131072, Capabilities: capMultimodal, SelectionPolicy: "round_robin"},
		{ID: "mimo-v2.5-pro", Family: "MiMo", Name: "mimo-v2.5-pro", DisplayName: "MiMo V2.5 Pro", Description: "Xiaomi text model.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 131072, Capabilities: capText, SelectionPolicy: "round_robin"},

		{ID: "deepseek-flash", Family: "DeepSeek", Name: "deepseek-flash", DisplayName: "DeepSeek Flash", Description: "DeepSeek V4.1 Flash with vision and Responses support.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 384000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "deepseek-v4-pro", Family: "DeepSeek", Name: "deepseek-v4-pro", DisplayName: "DeepSeek V4 Pro", Description: "DeepSeek V4 Pro text reasoning model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 384000, Capabilities: capText, SelectionPolicy: "round_robin"},

		{ID: "qwen3.8-flash", Family: "Qwen", Name: "qwen3.8-flash", DisplayName: "Qwen 3.8 Flash", Description: "Qwen fast text and vision model.", ContextWindow: 1000000, MaxInputTokens: 991808, MaxOutputTokens: 131072, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "qwen3.8-max", Family: "Qwen", Name: "qwen3.8-max", DisplayName: "Qwen 3.8 Max", Description: "Qwen vision and reasoning flagship model.", ContextWindow: 1000000, MaxInputTokens: 991808, MaxOutputTokens: 131072, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "qwen3.7-plus", Family: "Qwen", Name: "qwen3.7-plus", DisplayName: "Qwen 3.7 Plus", Description: "Qwen text and vision model.", ContextWindow: 1000000, MaxInputTokens: 991808, MaxOutputTokens: 131072, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "kimi-k3", Family: "Moonshot", Name: "kimi-k3", DisplayName: "Kimi K3", Description: "Kimi flagship vision model; output shares the context budget.", ContextWindow: 1048576, MaxInputTokens: 1048576, MaxOutputTokens: 1048576, Capabilities: capVision, SelectionPolicy: "round_robin"},
		// K2.7 documentation specifies a default output budget, not a hard maximum.
		// Leave that metadata unset until the official limit is published.
		{ID: "kimi-k2.7-code", Family: "Moonshot", Name: "kimi-k2.7-code", DisplayName: "Kimi K2.7 Code", Description: "Kimi coding and vision model with always-on reasoning.", ContextWindow: 262144, MaxInputTokens: 262144, MaxOutputTokens: 0, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "kimi-k2.6", Family: "Moonshot", Name: "kimi-k2.6", DisplayName: "Kimi K2.6", Description: "Kimi general-purpose vision model; output shares the context budget.", ContextWindow: 262144, MaxInputTokens: 262144, MaxOutputTokens: 262144, Capabilities: capVision, SelectionPolicy: "round_robin"},

		{ID: "glm-5.3-flash", Family: "Zhipu", Name: "glm-5.3-flash", DisplayName: "GLM 5.3 Flash", Description: "Zhipu fast vision model with always-on reasoning.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capVision, SelectionPolicy: "round_robin"},
		{ID: "glm-5.3", Family: "Zhipu", Name: "glm-5.3", DisplayName: "GLM 5.3", Description: "Zhipu flagship text model with always-on reasoning.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capText, SelectionPolicy: "round_robin"},
		{ID: "glm-5.1", Family: "Zhipu", Name: "glm-5.1", DisplayName: "GLM 5.1", Description: "Zhipu text reasoning model.", ContextWindow: 200000, MaxInputTokens: 200000, MaxOutputTokens: 128000, Capabilities: capText, SelectionPolicy: "round_robin"},
		{ID: "glm-5.2", Family: "Zhipu", Name: "glm-5.2", DisplayName: "GLM 5.2", Description: "Zhipu text model.", ContextWindow: 1000000, MaxInputTokens: 1000000, MaxOutputTokens: 128000, Capabilities: capText, SelectionPolicy: "round_robin"},
	}
}

func findModelPreset(id string) (ModelPreset, bool) {
	normalized := strings.ReplaceAll(id, ".", "-")
	for _, preset := range modelPresets() {
		if preset.ID == id || preset.Name == id {
			return preset, true
		}
		if strings.ReplaceAll(preset.ID, ".", "-") == normalized {
			return preset, true
		}
	}
	return ModelPreset{}, false
}

func (p ModelPreset) Model() Model {
	return Model{
		Name:                   p.Name,
		DisplayName:            p.DisplayName,
		Description:            p.Description,
		ContextWindow:          p.ContextWindow,
		MaxInputTokens:         p.MaxInputTokens,
		MaxOutputTokens:        p.MaxOutputTokens,
		Capabilities:           p.Capabilities,
		SelectionPolicy:        p.SelectionPolicy,
		QuotaUncachedInputRate: 1,
		QuotaCachedInputRate:   1,
		QuotaOutputRate:        1,
		Enabled:                true,
	}
}

func modelPresetCapabilities(vision, audioInput bool) string {
	data, err := json.MarshalIndent(map[string]any{
		"tools":       true,
		"vision":      vision,
		"reasoning":   true,
		"audio_input": audioInput,
	}, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}
