package buzzhive

import (
	"math"
	"net/http"
	"strings"
	"time"
)

type openAIModelsResponse struct {
	Object string              `json:"object"`
	Data   []openAIModelObject `json:"data"`
}

type openAIModelObject struct {
	catalogModel
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request, _ AuthToken) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if s.store == nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "store is not configured")
		return
	}
	models, err := s.store.Models()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", err.Error())
		return
	}
	data := make([]openAIModelObject, 0, len(models))
	for _, model := range models {
		if !model.Enabled {
			continue
		}
		data = append(data, publicModelMetadata(model))
	}
	writeJSON(w, http.StatusOK, openAIModelsResponse{Object: "list", Data: data})
}

// Public metadata comes solely from the saved model configuration. Catalog
// fields follow OpenRouter; Credits rates are not advertised as USD pricing.
func publicModelMetadata(model Model) openAIModelObject {
	m := openAIModelObject{catalogModel: catalogModel{
		ID: model.Name, Name: strings.TrimSpace(model.DisplayName), Description: strings.TrimSpace(model.Description),
		ContextLength: max(0, model.ContextWindow),
	}, Object: "model", OwnedBy: "buzzhive"}
	if created, err := time.Parse(time.RFC3339, model.CreatedAt); err == nil {
		m.Created = created.Unix()
	}
	if model.ContextWindow > 0 || model.MaxOutputTokens > 0 {
		m.TopProvider = &modelTopProvider{ContextLength: max(0, model.ContextWindow), MaxCompletionTokens: max(0, model.MaxOutputTokens)}
	}
	caps := savedModelCapabilities(model.Capabilities)
	_, visionKnown := caps["vision"]
	_, audioKnown := caps["audio_input"]
	// Publish the configured supported set once any flag in a group is known.
	// Unconfigured flags are not advertised; a wholly unknown group stays absent.
	if visionKnown || audioKnown {
		inputs := []string{"text"}
		if caps["vision"] {
			inputs = append(inputs, "image")
		}
		if caps["audio_input"] {
			inputs = append(inputs, "audio")
		}
		m.Architecture = &modelArchitecture{InputModalities: inputs, OutputModalities: []string{"text"}}
	}
	_, toolsKnown := caps["tools"]
	_, reasoningKnown := caps["reasoning"]
	_, schemaKnown := caps["json_schema"]
	if toolsKnown || reasoningKnown || schemaKnown {
		parameters := []string{}
		if caps["tools"] {
			parameters = append(parameters, "tools", "tool_choice")
		}
		if caps["reasoning"] {
			parameters = append(parameters, "reasoning")
		}
		if caps["json_schema"] {
			parameters = append(parameters, "response_format", "structured_outputs")
		}
		m.SupportedParameters = &parameters
	}
	m.CostMultiplier = calculateCostMultiplier(model)
	return m
}

func calculateCostMultiplier(model Model) *float64 {
	cost := model.QuotaCachedInputRate*0.64 + model.QuotaUncachedInputRate*0.16 + model.QuotaOutputRate*0.20
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return nil
	}
	if cost == 0 {
		var zero float64 = 0
		return &zero
	}
	raw := cost / 1000.0
	var val float64
	if raw < 1.0 {
		val = math.Round(raw*100) / 100
		if val == 0 && cost > 0 {
			val = 0.01
		}
	} else {
		val = math.Round(raw*10) / 10
	}
	return &val
}
