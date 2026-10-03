package buzz

import (
	"encoding/json"
	"regexp"
)

// Versioned Mimo reasoning models, not arbitrary aliases or ASR/TTS models.
var mimoReasoningModel = regexp.MustCompile(`(?i)^(?:xiaomi/|xiaomimimo/)?mimo-v[0-9]+(?:\.[0-9]+)*(?:-(?:flash|pro)(?:-ultraspeed)?)?$`)

// Normalize only at the upstream boundary, after alias resolution and protocol
// conversion. Never change the public model, stored settings, or unknown fields.
// Mimo maps xhigh/max to high; older compatible endpoints reject those aliases.
// https://mimo.mi.com/docs/en-US/api/chat/responses (2026-09-22)
func normalizeModelReasoning(protocol, model string, body []byte) []byte {
	if !mimoReasoningModel.MatchString(model) {
		return body
	}
	field := "reasoning_effort"
	switch protocol {
	case providerOpenAI:
	case providerOpenAIResponses:
		field = "reasoning"
	case providerAnthropic:
		field = "output_config"
	default:
		return body
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	container, key := payload, field
	if field != "reasoning_effort" {
		// Use a separate map so decoding does not mutate the top-level payload.
		container = nil
		if json.Unmarshal(payload[field], &container) != nil {
			return body
		}
		key = "effort"
	}
	var effort string
	if json.Unmarshal(container[key], &effort) != nil || (effort != "xhigh" && effort != "max") {
		return body
	}
	container[key] = json.RawMessage(`"high"`)
	// All RawMessages came from valid JSON or the literal above.
	if field != "reasoning_effort" {
		payload[field], _ = json.Marshal(container)
	}
	encoded, _ := json.Marshal(payload)
	return encoded
}
