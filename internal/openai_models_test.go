package buzzhive

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPublicModelMetadataKeepsUnknownFieldsAbsent(t *testing.T) {
	for _, caps := range []string{"", "{}", "null", "invalid", `{"vision":"false"}`, `{"vision":null,"tools":"false"}`, `{"stream":true}`} {
		m := publicModelMetadata(Model{Name: "unknown", Capabilities: caps})
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"context_length", "max_input_tokens", "max_output_tokens", "capabilities", "architecture", "supported_parameters", "top_provider", "pricing"} {
			if strings.Contains(string(b), field) {
				t.Fatalf("invented %s in %s", field, b)
			}
		}
	}
}

func TestPublicModelMetadataKnownDisabledCapabilities(t *testing.T) {
	m := publicModelMetadata(Model{Name: "text", Capabilities: `{"vision":false,"audio_input":false,"tools":false,"reasoning":false,"json_schema":false}`})
	if m.Architecture == nil || !reflect.DeepEqual(m.Architecture.InputModalities, []string{"text"}) || m.SupportedParameters == nil || len(*m.SupportedParameters) != 0 {
		t.Fatalf("explicit disabled lost: %+v", m)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"supported_parameters":[]`) {
		t.Fatalf("empty list not preserved: %s", b)
	}
}

func TestPublicModelMetadataPartialCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name       string
		caps       string
		inputs     []string
		parameters []string
	}{
		{"vision and disabled tools", `{"vision":true,"tools":false}`, []string{"text", "image"}, []string{}},
		{"disabled vision", `{"vision":false}`, []string{"text"}, nil},
		{"audio", `{"audio_input":true}`, []string{"text", "audio"}, nil},
		{"tools", `{"tools":true,"reasoning":false,"json_schema":"true"}`, nil, []string{"tools", "tool_choice"}},
		{"disabled tools", `{"tools":false}`, nil, []string{}},
		{"reasoning", `{"reasoning":true}`, nil, []string{"reasoning"}},
		{"structured outputs", `{"json_schema":true}`, nil, []string{"response_format", "structured_outputs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := publicModelMetadata(Model{Name: "custom-partial", Capabilities: tc.caps})
			encoded, err := json.Marshal(openAIModelsResponse{Object: "list", Data: []openAIModelObject{model}})
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Data []struct {
					Architecture *modelArchitecture `json:"architecture"`
					Parameters   []string           `json:"supported_parameters"`
				} `json:"data"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			item := wire.Data[0]
			if tc.inputs == nil {
				if item.Architecture != nil {
					t.Fatalf("unknown modalities advertised: %s", encoded)
				}
			} else if item.Architecture == nil || !reflect.DeepEqual(item.Architecture.InputModalities, tc.inputs) || !reflect.DeepEqual(item.Architecture.OutputModalities, []string{"text"}) {
				t.Fatalf("configured modalities lost: %s", encoded)
			}
			if !reflect.DeepEqual(item.Parameters, tc.parameters) {
				t.Fatalf("parameters = %#v, want %#v: %s", item.Parameters, tc.parameters, encoded)
			}
			// A downstream OpenRouter-compatible importer must retain each saved flag,
			// including false when the public supported set is an explicit empty list.
			imported, err := decodeUpstreamModels(strings.NewReader(string(encoded)), "openai")
			if err != nil || len(imported) != 1 {
				t.Fatalf("decode public catalog: %v, %+v", err, imported)
			}
			for capability, want := range savedModelCapabilities(tc.caps) {
				if got, known := imported[0].Capabilities[capability]; !known || got != want {
					t.Errorf("round trip %s = %v (known=%v), want %v", capability, got, known, want)
				}
			}
		})
	}
}

func TestPublicModelMetadataCostMultiplier(t *testing.T) {
	cases := []struct {
		name     string
		model    Model
		wantMult *float64
	}{
		{
			name: "deepseek 0.08x",
			model: Model{
				Name:                   "deepseek-flash",
				QuotaCachedInputRate:   3,
				QuotaUncachedInputRate: 140,
				QuotaOutputRate:        280,
			},
			wantMult: float64Ptr(0.08),
		},
		{
			name: "flagship 3.2x",
			model: Model{
				Name:                   "gpt-4o",
				QuotaCachedInputRate:   1250,
				QuotaUncachedInputRate: 2500,
				QuotaOutputRate:        10000,
			},
			wantMult: float64Ptr(3.2),
		},
		{
			name: "zero rate 0x",
			model: Model{
				Name: "free",
			},
			wantMult: float64Ptr(0),
		},
		{
			name: "micro non-zero rate clamps to 0.01",
			model: Model{
				Name:                   "cheap",
				QuotaUncachedInputRate: 5,
			},
			wantMult: float64Ptr(0.01),
		},
		{
			name: "negative rate omitted",
			model: Model{
				Name:                   "invalid",
				QuotaUncachedInputRate: -1,
			},
			wantMult: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := publicModelMetadata(tc.model)
			if tc.wantMult == nil {
				if m.CostMultiplier != nil {
					t.Fatalf("CostMultiplier = %v, want nil", *m.CostMultiplier)
				}
			} else {
				if m.CostMultiplier == nil || *m.CostMultiplier != *tc.wantMult {
					t.Fatalf("CostMultiplier = %v, want %v", m.CostMultiplier, *tc.wantMult)
				}
			}

			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantMult != nil {
				if !strings.Contains(string(b), `"cost_multiplier":`) {
					t.Fatalf("cost_multiplier missing in json: %s", string(b))
				}
			} else {
				if strings.Contains(string(b), `"cost_multiplier"`) {
					t.Fatalf("cost_multiplier should be omitted: %s", string(b))
				}
			}
		})
	}
}

func float64Ptr(v float64) *float64 {
	return &v
}

