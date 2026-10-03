package buzz

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMimoPresets(t *testing.T) {
	for _, preset := range modelPresets() {
		if strings.HasPrefix(preset.ID, "mimo-") && (preset.Family != "MiMo" || !strings.HasPrefix(preset.DisplayName, "MiMo ")) {
			t.Errorf("inconsistent display name: %s", preset.DisplayName)
		}
	}
	for _, variant := range []string{"flash", "pro"} {
		preset, found := findModelPreset("mimo-v2.6-" + variant)
		if !found || preset.Family != "MiMo" || preset.ContextWindow != 1000000 || preset.MaxInputTokens != 1000000 || preset.MaxOutputTokens != 131072 {
			t.Fatalf("incomplete V2.6 preset: %+v", preset)
		}
		var capabilities map[string]bool
		if err := json.Unmarshal([]byte(preset.Capabilities), &capabilities); err != nil {
			t.Fatal(err)
		}
		for _, capability := range []string{"vision", "audio_input", "tools", "reasoning"} {
			if !capabilities[capability] {
				t.Errorf("%s missing %s", preset.ID, capability)
			}
		}
	}
}

func TestOfficialPresetBrandNames(t *testing.T) {
	for id, name := range map[string]string{"mimo": "MiMo", "mimo-plan": "MiMo Plan", "deepseek": "DeepSeek"} {
		preset, found := findProviderPreset(id)
		if !found || preset.Name != name || preset.Provider().Name != name {
			t.Errorf("provider %s: %+v", id, preset)
		}
	}
	for _, preset := range modelPresets() {
		if strings.HasPrefix(preset.ID, "deepseek-") && (preset.Family != "DeepSeek" || !strings.HasPrefix(preset.DisplayName, "DeepSeek ")) {
			t.Errorf("inconsistent DeepSeek preset: %+v", preset)
		}
	}
}
