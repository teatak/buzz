package buzz

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/teatak/buzz/internal/protocol"
)

func writeSSEJSON(w io.Writer, flusher http.Flusher, event string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher != nil {
		flusher.Flush()
	}
}

type responsesStreamEncoder struct {
	w               io.Writer
	flusher         http.Flusher
	id              string
	created         int64
	model           string
	sequence        int
	nextOutputIndex int
	text            strings.Builder
	textIndex       int
	textOpen        bool
	textRefusal     bool
	reasoning       strings.Builder
	reasoningSig    strings.Builder
	reasoningIndex  int
	reasoningOpen   bool
	toolCalls       map[int]*responsesStreamToolState
	output          map[int]protocol.OpenAIResponseOutputItem
	usage           protocol.CanonicalUsage
	finished        bool
}

type responsesStreamToolState struct {
	call        protocol.CanonicalToolCall
	itemID      string
	outputIndex int
	done        bool
}

func newResponsesStreamEncoder(w io.Writer, flusher http.Flusher, id string, created int64, model string) *responsesStreamEncoder {
	encoder := &responsesStreamEncoder{
		w:              w,
		flusher:        flusher,
		id:             id,
		created:        created,
		model:          model,
		textIndex:      -1,
		reasoningIndex: -1,
		toolCalls:      make(map[int]*responsesStreamToolState),
		output:         make(map[int]protocol.OpenAIResponseOutputItem),
	}
	encoder.write("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         id,
			"object":     "response",
			"created_at": created,
			"status":     "in_progress",
			"model":      model,
			"output":     []any{},
		},
	})
	return encoder
}

func (e *responsesStreamEncoder) write(event string, value map[string]any) {
	value["sequence_number"] = e.sequence
	e.sequence++
	writeSSEJSON(e.w, e.flusher, event, value)
}

func (e *responsesStreamEncoder) writeEvent(event protocol.CanonicalStreamEvent) {
	if e.finished {
		return
	}
	switch event.Type {
	case protocol.CanonicalStreamTextDelta:
		e.writeTextDelta(event.Delta, false)
	case protocol.CanonicalStreamRefusalDelta:
		e.writeTextDelta(event.Delta, true)
	case protocol.CanonicalStreamReasoningDelta:
		e.writeReasoningDelta(event)
	case protocol.CanonicalStreamToolCallStart:
		e.writeToolStart(event)
	case protocol.CanonicalStreamToolArgumentsDelta:
		e.writeToolDelta(event)
	case protocol.CanonicalStreamToolCallDone:
		e.writeToolDone(event)
	case protocol.CanonicalStreamUsage:
		e.usage = event.Usage
	case protocol.CanonicalStreamResponseDone:
		e.finish(event.Status, event.FinishReason, e.usage)
	case protocol.CanonicalStreamError:
		if event.Error != nil {
			e.write("error", map[string]any{
				"type":    "error",
				"code":    event.Error.Code,
				"message": event.Error.Message,
				"param":   nil,
			})
		}
		e.finished = true
	}
}

func (e *responsesStreamEncoder) ensureText(refusal bool) {
	if e.textOpen {
		return
	}
	e.closeReasoning()
	e.textIndex = e.reserveOutputIndex()
	e.textOpen = true
	e.textRefusal = refusal
	part := map[string]any{
		"type":        "output_text",
		"text":        "",
		"annotations": []any{},
	}
	if refusal {
		part = map[string]any{
			"type":    "refusal",
			"refusal": "",
		}
	}
	e.write("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": e.textIndex,
		"item": map[string]any{
			"id":      e.id + "_msg",
			"type":    "message",
			"status":  "in_progress",
			"role":    "assistant",
			"content": []any{},
		},
	})
	e.write("response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       e.id + "_msg",
		"output_index":  e.textIndex,
		"content_index": 0,
		"part":          part,
	})
}

func (e *responsesStreamEncoder) ensureReasoning() {
	if e.reasoningOpen {
		return
	}
	e.reasoningIndex = e.reserveOutputIndex()
	e.reasoningOpen = true
	e.write("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": e.reasoningIndex,
		"item": map[string]any{
			"id":      e.id + "_reasoning",
			"type":    "reasoning",
			"status":  "in_progress",
			"summary": []any{},
		},
	})
	e.write("response.reasoning_summary_part.added", map[string]any{
		"type":          "response.reasoning_summary_part.added",
		"item_id":       e.id + "_reasoning",
		"output_index":  e.reasoningIndex,
		"summary_index": 0,
		"part": map[string]any{
			"type": "summary_text",
			"text": "",
		},
	})
}

func (e *responsesStreamEncoder) writeReasoningDelta(event protocol.CanonicalStreamEvent) {
	if event.Delta == "" && event.Signature == "" {
		return
	}
	e.ensureReasoning()
	if event.Delta != "" {
		e.reasoning.WriteString(event.Delta)
		e.write("response.reasoning_summary_text.delta", map[string]any{
			"type":          "response.reasoning_summary_text.delta",
			"item_id":       e.id + "_reasoning",
			"output_index":  e.reasoningIndex,
			"summary_index": 0,
			"delta":         event.Delta,
		})
	}
	if event.Signature != "" {
		e.reasoningSig.WriteString(event.Signature)
	}
}

func (e *responsesStreamEncoder) closeReasoning() {
	if !e.reasoningOpen {
		return
	}
	reasoning := e.reasoning.String()
	part := protocol.OpenAIResponseOutputPart{Type: "summary_text", Text: reasoning}
	e.write("response.reasoning_summary_text.done", map[string]any{
		"type":          "response.reasoning_summary_text.done",
		"item_id":       e.id + "_reasoning",
		"output_index":  e.reasoningIndex,
		"summary_index": 0,
		"text":          reasoning,
	})
	e.write("response.reasoning_summary_part.done", map[string]any{
		"type":          "response.reasoning_summary_part.done",
		"item_id":       e.id + "_reasoning",
		"output_index":  e.reasoningIndex,
		"summary_index": 0,
		"part":          part,
	})
	item := protocol.OpenAIResponseOutputItem{
		Type:             "reasoning",
		ID:               e.id + "_reasoning",
		Status:           "completed",
		Summary:          []protocol.OpenAIResponseOutputPart{part},
		EncryptedContent: e.reasoningSig.String(),
	}
	e.output[e.reasoningIndex] = item
	e.write("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": e.reasoningIndex,
		"item":         item,
	})
	e.reasoningOpen = false
}

func (e *responsesStreamEncoder) writeTextDelta(delta string, refusal bool) {
	if delta == "" {
		return
	}
	e.ensureText(refusal)
	e.text.WriteString(delta)
	eventType := "response.output_text.delta"
	if e.textRefusal {
		eventType = "response.refusal.delta"
	}
	e.write(eventType, map[string]any{
		"type":          eventType,
		"item_id":       e.id + "_msg",
		"output_index":  e.textIndex,
		"content_index": 0,
		"delta":         delta,
	})
}

func (e *responsesStreamEncoder) writeToolStart(event protocol.CanonicalStreamEvent) {
	if state, exists := e.toolCalls[event.Index]; exists {
		if state.call.ID == "" {
			state.call.ID = event.CallID
		}
		if state.call.Name == "" {
			state.call.Name = event.Name
		}
		if state.call.Signature == "" {
			state.call.Signature = event.Signature
		}
		return
	}
	e.closeReasoning()
	state := &responsesStreamToolState{
		call: protocol.CanonicalToolCall{
			ID:        event.CallID,
			Name:      event.Name,
			Signature: event.Signature,
		},
		itemID:      e.toolItemID(event.Index),
		outputIndex: e.reserveOutputIndex(),
	}
	e.toolCalls[event.Index] = state
	e.write("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": state.outputIndex,
		"item": map[string]any{
			"id":        state.itemID,
			"type":      "function_call",
			"status":    "in_progress",
			"call_id":   state.call.ID,
			"name":      state.call.Name,
			"arguments": "",
		},
	})
}

func (e *responsesStreamEncoder) writeToolDelta(event protocol.CanonicalStreamEvent) {
	state := e.toolCalls[event.Index]
	if state == nil {
		e.writeToolStart(protocol.CanonicalStreamEvent{
			Type:   protocol.CanonicalStreamToolCallStart,
			Index:  event.Index,
			CallID: event.CallID,
			Name:   event.Name,
		})
		state = e.toolCalls[event.Index]
	}
	if state.done || event.Delta == "" {
		return
	}
	state.call.Arguments += event.Delta
	e.write("response.function_call_arguments.delta", map[string]any{
		"type":         "response.function_call_arguments.delta",
		"item_id":      state.itemID,
		"output_index": state.outputIndex,
		"delta":        event.Delta,
	})
}

func (e *responsesStreamEncoder) writeToolDone(event protocol.CanonicalStreamEvent) {
	state := e.toolCalls[event.Index]
	if state == nil {
		e.writeToolStart(protocol.CanonicalStreamEvent{
			Type:      protocol.CanonicalStreamToolCallStart,
			Index:     event.Index,
			CallID:    event.CallID,
			Name:      event.Name,
			Signature: event.Signature,
		})
		state = e.toolCalls[event.Index]
	}
	if state.done {
		return
	}
	if state.call.ID == "" {
		state.call.ID = event.CallID
	}
	if state.call.Name == "" {
		state.call.Name = event.Name
	}
	if event.Arguments != "" {
		state.call.Arguments = event.Arguments
	}
	if event.Signature != "" {
		state.call.Signature = event.Signature
	}
	state.done = true
	e.write("response.function_call_arguments.done", map[string]any{
		"type":         "response.function_call_arguments.done",
		"item_id":      state.itemID,
		"output_index": state.outputIndex,
		"name":         state.call.Name,
		"arguments":    state.call.Arguments,
	})
	item := protocol.OpenAIResponseOutputItem{
		Type:      "function_call",
		ID:        state.itemID,
		Status:    "completed",
		CallID:    state.call.ID,
		Name:      state.call.Name,
		Arguments: state.call.Arguments,
	}
	e.output[state.outputIndex] = item
	e.write("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": state.outputIndex,
		"item":         item,
	})
}

func (e *responsesStreamEncoder) finish(status string, reason string, usage protocol.CanonicalUsage) {
	if e.finished {
		return
	}
	if !usage.IsZero() {
		e.usage = usage
	}
	refusal := ""
	if e.textRefusal {
		refusal = e.text.String()
	}
	canonical := protocol.CanonicalResponse{
		ID:           e.id,
		Created:      e.created,
		Model:        e.model,
		Role:         "assistant",
		Status:       status,
		Refusal:      refusal,
		FinishReason: reason,
		Usage:        e.usage,
	}
	responseStatus := protocol.CanonicalToOpenAIResponsesResponse(canonical).Status
	e.closeReasoning()
	if e.textOpen {
		text := e.text.String()
		eventType := "response.output_text.done"
		part := protocol.OpenAIResponseOutputPart{
			Type:        "output_text",
			Text:        text,
			Annotations: json.RawMessage(`[]`),
		}
		if e.textRefusal {
			eventType = "response.refusal.done"
			part = protocol.OpenAIResponseOutputPart{Type: "refusal", Refusal: text}
		}
		done := map[string]any{
			"type":          eventType,
			"item_id":       e.id + "_msg",
			"output_index":  e.textIndex,
			"content_index": 0,
		}
		if e.textRefusal {
			done["refusal"] = part.Refusal
		} else {
			done["text"] = part.Text
		}
		e.write(eventType, done)
		e.write("response.content_part.done", map[string]any{
			"type":          "response.content_part.done",
			"item_id":       e.id + "_msg",
			"output_index":  e.textIndex,
			"content_index": 0,
			"part":          part,
		})
		item := protocol.OpenAIResponseOutputItem{
			Type:    "message",
			ID:      e.id + "_msg",
			Status:  responseStatus,
			Role:    "assistant",
			Content: []protocol.OpenAIResponseOutputPart{part},
		}
		e.output[e.textIndex] = item
		e.write("response.output_item.done", map[string]any{
			"type":         "response.output_item.done",
			"output_index": e.textIndex,
			"item":         item,
		})
	}
	for _, index := range e.sortedToolIndexes() {
		state := e.toolCalls[index]
		if !state.done {
			e.writeToolDone(protocol.CanonicalStreamEvent{
				Index:     index,
				CallID:    state.call.ID,
				Name:      state.call.Name,
				Arguments: state.call.Arguments,
				Signature: state.call.Signature,
			})
		}
	}
	resp := protocol.CanonicalToOpenAIResponsesResponse(canonical)
	resp.Output = e.sortedOutput()
	e.finished = true
	eventType := "response.completed"
	if resp.Status == "incomplete" {
		eventType = "response.incomplete"
	}
	e.write(eventType, map[string]any{
		"type":     eventType,
		"response": resp,
	})
}

func (e *responsesStreamEncoder) toolItemID(index int) string {
	return fmt.Sprintf("%s_fc_%d", e.id, index)
}

func (e *responsesStreamEncoder) reserveOutputIndex() int {
	index := e.nextOutputIndex
	e.nextOutputIndex++
	return index
}

func (e *responsesStreamEncoder) sortedToolIndexes() []int {
	indexes := make([]int, 0, len(e.toolCalls))
	for index := range e.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func (e *responsesStreamEncoder) sortedOutput() []protocol.OpenAIResponseOutputItem {
	out := make([]protocol.OpenAIResponseOutputItem, 0, len(e.output))
	for index := 0; index < e.nextOutputIndex; index++ {
		if item, ok := e.output[index]; ok {
			out = append(out, item)
		}
	}
	return out
}

type anthropicStreamEncoder struct {
	w                  io.Writer
	flusher            http.Flusher
	nextIndex          int
	textIndex          int
	textOpen           bool
	reasoningIndex     int
	reasoningOpen      bool
	reasoningSignature strings.Builder
	refusal            strings.Builder
	toolCalls          map[int]protocol.CanonicalToolCall
	usage              protocol.CanonicalUsage
	finished           bool
}

func newAnthropicStreamEncoder(w io.Writer, flusher http.Flusher, id string, model string) *anthropicStreamEncoder {
	encoder := &anthropicStreamEncoder{
		w:         w,
		flusher:   flusher,
		toolCalls: make(map[int]protocol.CanonicalToolCall),
	}
	writeSSEJSON(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens": 0,
			},
		},
	})
	return encoder
}

func (e *anthropicStreamEncoder) writeEvent(event protocol.CanonicalStreamEvent) {
	if e.finished {
		return
	}
	switch event.Type {
	case protocol.CanonicalStreamTextDelta:
		e.writeTextDelta(event.Delta)
	case protocol.CanonicalStreamRefusalDelta:
		e.refusal.WriteString(event.Delta)
	case protocol.CanonicalStreamReasoningDelta:
		e.writeReasoningDelta(event)
	case protocol.CanonicalStreamToolCallStart:
		e.toolCalls[event.Index] = protocol.CanonicalToolCall{
			ID:        event.CallID,
			Name:      event.Name,
			Signature: event.Signature,
		}
	case protocol.CanonicalStreamToolArgumentsDelta:
		call := e.toolCalls[event.Index]
		call.Arguments += event.Delta
		e.toolCalls[event.Index] = call
	case protocol.CanonicalStreamToolCallDone:
		e.writeToolDone(event)
	case protocol.CanonicalStreamUsage:
		e.usage = event.Usage
	case protocol.CanonicalStreamResponseDone:
		e.finish(event.FinishReason, e.usage)
	case protocol.CanonicalStreamError:
		if event.Error != nil {
			writeSSEJSON(e.w, e.flusher, "error", map[string]any{
				"type": "error",
				"error": map[string]any{
					"type":    anthropicStreamErrorType(event.Error.Code),
					"message": event.Error.Message,
				},
			})
		}
		e.finished = true
	}
}

func (e *anthropicStreamEncoder) ensureText() {
	if e.textOpen {
		return
	}
	e.closeReasoning()
	e.textOpen = true
	e.textIndex = e.nextIndex
	e.nextIndex++
	writeSSEJSON(e.w, e.flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": e.textIndex,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})
}

func (e *anthropicStreamEncoder) ensureReasoning() {
	if e.reasoningOpen {
		return
	}
	e.closeText()
	e.reasoningSignature.Reset()
	e.reasoningOpen = true
	e.reasoningIndex = e.nextIndex
	e.nextIndex++
	writeSSEJSON(e.w, e.flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": e.reasoningIndex,
		"content_block": map[string]any{
			"type":      "thinking",
			"thinking":  "",
			"signature": "",
		},
	})
}

func (e *anthropicStreamEncoder) writeReasoningDelta(event protocol.CanonicalStreamEvent) {
	if event.Delta == "" && event.Signature == "" {
		return
	}
	e.ensureReasoning()
	if event.Delta != "" {
		writeSSEJSON(e.w, e.flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.reasoningIndex,
			"delta": map[string]any{
				"type":     "thinking_delta",
				"thinking": event.Delta,
			},
		})
	}
	if event.Signature != "" {
		e.reasoningSignature.WriteString(event.Signature)
	}
}

func (e *anthropicStreamEncoder) closeReasoning() {
	if !e.reasoningOpen {
		return
	}
	if signature := e.reasoningSignature.String(); signature != "" {
		writeSSEJSON(e.w, e.flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.reasoningIndex,
			"delta": map[string]any{
				"type":      "signature_delta",
				"signature": signature,
			},
		})
	}
	writeSSEJSON(e.w, e.flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": e.reasoningIndex,
	})
	e.reasoningOpen = false
}

func (e *anthropicStreamEncoder) writeTextDelta(delta string) {
	if delta == "" {
		return
	}
	e.ensureText()
	writeSSEJSON(e.w, e.flusher, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": e.textIndex,
		"delta": map[string]any{
			"type": "text_delta",
			"text": delta,
		},
	})
}

func (e *anthropicStreamEncoder) closeText() {
	if !e.textOpen {
		return
	}
	writeSSEJSON(e.w, e.flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": e.textIndex,
	})
	e.textOpen = false
}

func (e *anthropicStreamEncoder) writeToolDone(event protocol.CanonicalStreamEvent) {
	e.closeReasoning()
	e.closeText()
	call := e.toolCalls[event.Index]
	if call.ID == "" {
		call.ID = event.CallID
	}
	if call.Name == "" {
		call.Name = event.Name
	}
	if event.Arguments != "" {
		call.Arguments = event.Arguments
	}
	e.toolCalls[event.Index] = call
	index := e.nextIndex
	e.nextIndex++
	writeSSEJSON(e.w, e.flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": index,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": map[string]any{},
		},
	})
	writeSSEJSON(e.w, e.flusher, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": call.Arguments,
		},
	})
	writeSSEJSON(e.w, e.flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
}

func (e *anthropicStreamEncoder) finish(reason string, usage protocol.CanonicalUsage) {
	if e.finished {
		return
	}
	e.finished = true
	e.closeReasoning()
	e.closeText()
	if !usage.IsZero() {
		e.usage = usage
	}
	inputTokens := e.usage.PromptTokens - e.usage.CachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	stopReason := protocol.CanonicalFinishReasonToAnthropic(reason)
	delta := map[string]any{
		"stop_reason":   stopReason,
		"stop_sequence": nil,
	}
	if stopReason == "refusal" {
		explanation := e.refusal.String()
		if explanation == "" {
			explanation = "The request was declined"
		}
		delta["stop_details"] = protocol.AnthropicStopDetails{
			Type:        "refusal",
			Explanation: explanation,
		}
	}
	writeSSEJSON(e.w, e.flusher, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": delta,
		"usage": map[string]any{
			"input_tokens":                inputTokens,
			"output_tokens":               e.usage.CompletionTokens,
			"cache_read_input_tokens":     e.usage.CachedTokens,
			"cache_creation_input_tokens": 0,
		},
	})
	writeSSEJSON(e.w, e.flusher, "message_stop", map[string]any{
		"type": "message_stop",
	})
}

func writeGeminiStreamEvent(w io.Writer, flusher http.Flusher, event protocol.CanonicalStreamEvent) {
	if event.Type == protocol.CanonicalStreamError && event.Error != nil {
		status := canonicalStreamErrorHTTPStatus(event.Error.Code)
		writeSSEJSON(w, flusher, "", map[string]any{
			"error": map[string]any{
				"code":    status,
				"message": event.Error.Message,
				"status":  geminiErrorStatus(status),
			},
		})
		return
	}
	resp, ok := protocol.CanonicalStreamEventToGeminiGenerateResponse(event)
	if !ok {
		return
	}
	writeSSEJSON(w, flusher, "", resp)
}

type geminiStreamEncoder struct {
	w         io.Writer
	flusher   http.Flusher
	toolCalls []protocol.CanonicalStreamEvent
}

func newGeminiStreamEncoder(w io.Writer, flusher http.Flusher) *geminiStreamEncoder {
	return &geminiStreamEncoder{w: w, flusher: flusher}
}

func (e *geminiStreamEncoder) writeEvent(event protocol.CanonicalStreamEvent) {
	switch event.Type {
	case protocol.CanonicalStreamToolCallDone:
		e.toolCalls = append(e.toolCalls, event)
		return
	case protocol.CanonicalStreamResponseDone:
		if len(e.toolCalls) > 0 {
			sort.SliceStable(e.toolCalls, func(i, j int) bool {
				return e.toolCalls[i].Index < e.toolCalls[j].Index
			})
			toolCalls := make([]protocol.CanonicalToolCall, 0, len(e.toolCalls))
			for _, call := range e.toolCalls {
				toolCalls = append(toolCalls, protocol.CanonicalToolCall{
					ID:        call.CallID,
					Name:      call.Name,
					Arguments: call.Arguments,
					Signature: call.Signature,
				})
			}
			writeSSEJSON(e.w, e.flusher, "", protocol.CanonicalToGeminiGenerateResponse(
				protocol.CanonicalResponse{
					Role:         "assistant",
					ToolCalls:    toolCalls,
					FinishReason: event.FinishReason,
				},
			))
			e.toolCalls = nil
			return
		}
	}
	writeGeminiStreamEvent(e.w, e.flusher, event)
}

func canonicalStreamReadError(err error) protocol.CanonicalStreamEvent {
	return protocol.CanonicalStreamEvent{
		Type: protocol.CanonicalStreamError,
		Error: &protocol.CanonicalStreamErrorData{
			Code:    "upstream_stream_error",
			Message: err.Error(),
		},
	}
}

func readOpenAIChatStreamAsCanonical(r io.Reader, onEvent func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var usage protocol.CanonicalUsage
	tools := newCanonicalStreamToolTracker()
	var done *protocol.CanonicalStreamEvent
	responseStarted := false
	terminated := false
	var streamErr error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			terminated = true
			break
		}
		var errorEvent struct {
			Error *struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &errorEvent) == nil && errorEvent.Error != nil {
			code := firstNonEmptyStreamValue(errorEvent.Error.Code, errorEvent.Error.Type)
			onEvent(protocol.CanonicalStreamEvent{
				Type: protocol.CanonicalStreamError,
				Error: &protocol.CanonicalStreamErrorData{
					Code:    code,
					Message: errorEvent.Error.Message,
				},
			})
			streamErr = fmt.Errorf("OpenAI Chat stream error %s: %s", code, errorEvent.Error.Message)
			terminated = true
			break
		}
		var chunk protocol.OpenAIChatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return usage, fmt.Errorf("decode OpenAI Chat stream event: %w", err)
		}
		if !responseStarted {
			onEvent(protocol.CanonicalStreamEvent{
				Type:    protocol.CanonicalStreamResponseStart,
				ID:      chunk.ID,
				Created: chunk.Created,
				Model:   chunk.Model,
			})
			responseStarted = true
		}
		for _, event := range protocol.OpenAIChatStreamChunkToCanonical(chunk) {
			switch event.Type {
			case protocol.CanonicalStreamToolCallStart:
				if stable, ok := tools.start(event); ok {
					onEvent(stable)
				}
			case protocol.CanonicalStreamToolArgumentsDelta:
				onEvent(tools.delta(event))
			case protocol.CanonicalStreamUsage:
				usage = event.Usage
				onEvent(event)
			case protocol.CanonicalStreamResponseDone:
				for _, toolEvent := range tools.doneAll() {
					onEvent(toolEvent)
				}
				next := event
				done = &next
			default:
				onEvent(event)
			}
		}
	}
	for _, toolEvent := range tools.doneAll() {
		onEvent(toolEvent)
	}
	if done != nil {
		onEvent(*done)
		terminated = true
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read OpenAI Chat stream: %w", err)
	}
	if streamErr != nil {
		return usage, streamErr
	}
	if !terminated {
		return usage, io.ErrUnexpectedEOF
	}
	return usage, nil
}

func readGeminiStreamAsCanonical(r io.Reader, requestID string, onEvent func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var usage protocol.CanonicalUsage
	toolOffset := 0
	terminated := false
	var streamErr error
	onEvent(protocol.CanonicalStreamEvent{
		Type: protocol.CanonicalStreamResponseStart,
		ID:   requestID,
	})
	onEvent(protocol.CanonicalStreamEvent{
		Type: protocol.CanonicalStreamMessageStart,
		Role: "assistant",
	})
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			terminated = true
			break
		}
		var errorEvent struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &errorEvent) == nil && errorEvent.Error != nil {
			code := errorEvent.Error.Status
			if code == "" {
				code = fmt.Sprintf("%d", errorEvent.Error.Code)
			}
			onEvent(protocol.CanonicalStreamEvent{
				Type: protocol.CanonicalStreamError,
				Error: &protocol.CanonicalStreamErrorData{
					Code:    code,
					Message: errorEvent.Error.Message,
				},
			})
			streamErr = fmt.Errorf("Gemini stream error %s: %s", code, errorEvent.Error.Message)
			terminated = true
			break
		}
		var resp protocol.GeminiGenerateResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return usage, fmt.Errorf("decode Gemini stream event: %w", err)
		}
		for _, event := range protocol.GeminiToCanonicalStreamEvents(resp, requestID, toolOffset) {
			if event.Type == protocol.CanonicalStreamUsage {
				usage = event.Usage
			}
			if event.Type == protocol.CanonicalStreamToolCallDone {
				toolOffset++
			}
			if event.Type == protocol.CanonicalStreamResponseDone && toolOffset > 0 && event.FinishReason == "stop" {
				event.FinishReason = "tool_calls"
			}
			if event.Type == protocol.CanonicalStreamResponseDone {
				terminated = true
			}
			onEvent(event)
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Gemini stream: %w", err)
	}
	if streamErr != nil {
		return usage, streamErr
	}
	if !terminated {
		return usage, io.ErrUnexpectedEOF
	}
	return usage, nil
}

func anthropicStreamErrorType(code string) string {
	value := strings.ToLower(strings.TrimSpace(code))
	switch {
	case strings.Contains(value, "rate") || strings.Contains(value, "resource_exhausted"):
		return "rate_limit_error"
	case strings.Contains(value, "auth") || strings.Contains(value, "api_key"):
		return "authentication_error"
	case strings.Contains(value, "permission"):
		return "permission_error"
	case strings.Contains(value, "not_found"):
		return "not_found_error"
	case strings.Contains(value, "invalid"):
		return "invalid_request_error"
	case strings.Contains(value, "overload") || strings.Contains(value, "unavailable"):
		return "overloaded_error"
	default:
		return "api_error"
	}
}

func readResponsesStreamAsCanonical(r io.Reader, onEvent func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var usage protocol.CanonicalUsage
	tools := newCanonicalStreamToolTracker()
	reasoningSeen := false
	textSeen := false
	refusalSeen := false
	terminated := false
responsesStream:
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type        string                            `json:"type"`
			Delta       string                            `json:"delta"`
			Arguments   string                            `json:"arguments"`
			Name        string                            `json:"name"`
			ItemID      string                            `json:"item_id"`
			OutputIndex int                               `json:"output_index"`
			Item        protocol.OpenAIResponseOutputItem `json:"item"`
			Response    protocol.OpenAIResponsesResponse  `json:"response"`
			Code        string                            `json:"code"`
			Message     string                            `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return usage, fmt.Errorf("decode Responses stream event: %w", err)
		}
		switch event.Type {
		case "response.created":
			onEvent(protocol.CanonicalStreamEvent{
				Type:    protocol.CanonicalStreamResponseStart,
				ID:      event.Response.ID,
				Created: event.Response.CreatedAt,
				Model:   event.Response.Model,
			})
		case "response.output_text.delta":
			textSeen = true
			onEvent(protocol.CanonicalStreamEvent{
				Type:  protocol.CanonicalStreamTextDelta,
				Index: event.OutputIndex,
				Delta: event.Delta,
			})
		case "response.refusal.delta":
			refusalSeen = true
			onEvent(protocol.CanonicalStreamEvent{
				Type:  protocol.CanonicalStreamRefusalDelta,
				Index: event.OutputIndex,
				Delta: event.Delta,
			})
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoningSeen = true
			onEvent(protocol.CanonicalStreamEvent{
				Type:  protocol.CanonicalStreamReasoningDelta,
				Index: event.OutputIndex,
				Delta: event.Delta,
			})
		case "response.output_item.added":
			if event.Item.Type == "message" {
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamMessageStart,
					Index: event.OutputIndex,
					Role:  event.Item.Role,
				})
			} else if event.Item.Type == "function_call" {
				if stable, ok := tools.start(protocol.CanonicalStreamEvent{
					Type:   protocol.CanonicalStreamToolCallStart,
					Index:  event.OutputIndex,
					CallID: firstNonEmptyStreamValue(event.Item.CallID, event.Item.ID, event.ItemID),
					Name:   event.Item.Name,
				}); ok {
					onEvent(stable)
				}
			}
		case "response.function_call_arguments.delta":
			onEvent(tools.delta(protocol.CanonicalStreamEvent{
				Type:   protocol.CanonicalStreamToolArgumentsDelta,
				Index:  event.OutputIndex,
				CallID: event.ItemID,
				Delta:  event.Delta,
			}))
		case "response.function_call_arguments.done":
			if done, ok := tools.done(protocol.CanonicalStreamEvent{
				Type:      protocol.CanonicalStreamToolCallDone,
				Index:     event.OutputIndex,
				CallID:    event.ItemID,
				Name:      event.Name,
				Arguments: event.Arguments,
			}); ok {
				onEvent(done)
			}
		case "response.output_item.done":
			if event.Item.Type == "reasoning" {
				if !reasoningSeen {
					canonical, err := protocol.OpenAIResponsesResponseToCanonical(protocol.OpenAIResponsesResponse{
						Output: []protocol.OpenAIResponseOutputItem{event.Item},
					})
					if err != nil {
						return usage, err
					}
					if canonical.Reasoning != "" || canonical.Signature != "" {
						onEvent(protocol.CanonicalStreamEvent{
							Type:      protocol.CanonicalStreamReasoningDelta,
							Index:     event.OutputIndex,
							Delta:     canonical.Reasoning,
							Signature: canonical.Signature,
						})
						reasoningSeen = true
					}
				} else if event.Item.EncryptedContent != "" {
					onEvent(protocol.CanonicalStreamEvent{
						Type:      protocol.CanonicalStreamReasoningDelta,
						Index:     event.OutputIndex,
						Signature: event.Item.EncryptedContent,
					})
				}
			} else if event.Item.Type == "function_call" {
				if done, ok := tools.done(protocol.CanonicalStreamEvent{
					Type:      protocol.CanonicalStreamToolCallDone,
					Index:     event.OutputIndex,
					CallID:    firstNonEmptyStreamValue(event.Item.CallID, event.Item.ID, event.ItemID),
					Name:      event.Item.Name,
					Arguments: event.Item.Arguments,
				}); ok {
					onEvent(done)
				}
			}
		case "response.completed", "response.incomplete":
			canonical, err := protocol.OpenAIResponsesResponseToCanonical(event.Response)
			if err != nil {
				return usage, err
			}
			if canonical.Text != "" && !textSeen {
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamTextDelta,
					Delta: canonical.Text,
				})
				textSeen = true
			}
			if canonical.Refusal != "" && !refusalSeen {
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamRefusalDelta,
					Delta: canonical.Refusal,
				})
				refusalSeen = true
			}
			for _, call := range canonical.ToolCalls {
				index, ok := tools.indexForCallID(call.ID)
				if !ok {
					index = len(tools.order)
				}
				if stable, ok := tools.start(protocol.CanonicalStreamEvent{
					Type:   protocol.CanonicalStreamToolCallStart,
					Index:  index,
					CallID: call.ID,
					Name:   call.Name,
				}); ok {
					onEvent(stable)
				}
				if done, ok := tools.done(protocol.CanonicalStreamEvent{
					Type:      protocol.CanonicalStreamToolCallDone,
					Index:     index,
					CallID:    call.ID,
					Name:      call.Name,
					Arguments: call.Arguments,
					Signature: call.Signature,
				}); ok {
					onEvent(done)
				}
			}
			for _, toolEvent := range tools.doneAll() {
				onEvent(toolEvent)
			}
			usage = canonical.Usage
			if !usage.IsZero() {
				onEvent(protocol.CanonicalStreamEvent{Type: protocol.CanonicalStreamUsage, Usage: usage})
			}
			onEvent(protocol.CanonicalStreamEvent{
				Type:         protocol.CanonicalStreamResponseDone,
				Status:       canonical.Status,
				FinishReason: canonical.FinishReason,
			})
			terminated = true
			break responsesStream
		case "response.failed":
			terminated = true
			_, err := protocol.OpenAIResponsesResponseToCanonical(event.Response)
			if err == nil {
				err = fmt.Errorf("Responses stream failed")
			}
			return usage, err
		case "error":
			onEvent(protocol.CanonicalStreamEvent{
				Type: protocol.CanonicalStreamError,
				Error: &protocol.CanonicalStreamErrorData{
					Code:    event.Code,
					Message: event.Message,
				},
			})
			return usage, fmt.Errorf("Responses stream error %s: %s", event.Code, event.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Responses stream: %w", err)
	}
	if !terminated {
		return usage, io.ErrUnexpectedEOF
	}
	return usage, nil
}

func readAnthropicStreamAsCanonical(r io.Reader, onEvent func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var usage protocol.CanonicalUsage
	tools := newCanonicalStreamToolTracker()
	responseDone := false
	messageDeltaSeen := false
	finishReason := ""
anthropicStream:
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string                  `json:"id"`
				Model string                  `json:"model"`
				Role  string                  `json:"role"`
				Usage protocol.AnthropicUsage `json:"usage"`
			} `json:"message"`
			ContentBlock protocol.AnthropicContent `json:"content_block"`
			Delta        struct {
				Type        string                         `json:"type"`
				Text        string                         `json:"text"`
				Thinking    string                         `json:"thinking"`
				Signature   string                         `json:"signature"`
				PartialJSON string                         `json:"partial_json"`
				StopReason  string                         `json:"stop_reason"`
				StopDetails *protocol.AnthropicStopDetails `json:"stop_details"`
			} `json:"delta"`
			Usage protocol.AnthropicUsage `json:"usage"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return usage, fmt.Errorf("decode Anthropic stream event: %w", err)
		}
		switch event.Type {
		case "message_start":
			usage = mergeAnthropicStreamUsage(usage, event.Message.Usage)
			onEvent(protocol.CanonicalStreamEvent{
				Type:  protocol.CanonicalStreamResponseStart,
				ID:    event.Message.ID,
				Model: event.Message.Model,
			})
			onEvent(protocol.CanonicalStreamEvent{
				Type: protocol.CanonicalStreamMessageStart,
				Role: event.Message.Role,
			})
		case "content_block_start":
			if event.ContentBlock.Type == "tool_use" {
				if stable, ok := tools.start(protocol.CanonicalStreamEvent{
					Type:   protocol.CanonicalStreamToolCallStart,
					Index:  event.Index,
					CallID: event.ContentBlock.ID,
					Name:   event.ContentBlock.Name,
				}); ok {
					onEvent(stable)
				}
			}
		case "content_block_delta":
			switch event.Delta.Type {
			case "text_delta":
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamTextDelta,
					Index: event.Index,
					Delta: event.Delta.Text,
				})
			case "thinking_delta":
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamReasoningDelta,
					Index: event.Index,
					Delta: event.Delta.Thinking,
				})
			case "signature_delta":
				onEvent(protocol.CanonicalStreamEvent{
					Type:      protocol.CanonicalStreamReasoningDelta,
					Index:     event.Index,
					Signature: event.Delta.Signature,
				})
			case "input_json_delta":
				onEvent(tools.delta(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamToolArgumentsDelta,
					Index: event.Index,
					Delta: event.Delta.PartialJSON,
				}))
			}
		case "content_block_stop":
			if done, ok := tools.done(protocol.CanonicalStreamEvent{
				Type:  protocol.CanonicalStreamToolCallDone,
				Index: event.Index,
			}); ok {
				onEvent(done)
			}
		case "message_delta":
			messageDeltaSeen = true
			finishReason = protocol.AnthropicStopReasonToCanonical(event.Delta.StopReason)
			usage = mergeAnthropicStreamUsage(usage, event.Usage)
			for _, toolEvent := range tools.doneAll() {
				onEvent(toolEvent)
			}
			if !usage.IsZero() {
				onEvent(protocol.CanonicalStreamEvent{Type: protocol.CanonicalStreamUsage, Usage: usage})
			}
			if event.Delta.StopReason == "refusal" {
				refusal := "Anthropic refused the request"
				if event.Delta.StopDetails != nil {
					refusal = firstNonEmptyStreamValue(
						event.Delta.StopDetails.Explanation,
						event.Delta.StopDetails.Category,
						refusal,
					)
				}
				onEvent(protocol.CanonicalStreamEvent{
					Type:  protocol.CanonicalStreamRefusalDelta,
					Delta: refusal,
				})
			}
		case "message_stop":
			if !messageDeltaSeen {
				return usage, errors.New("Anthropic message_stop arrived before message_delta")
			}
			onEvent(protocol.CanonicalStreamEvent{
				Type:         protocol.CanonicalStreamResponseDone,
				FinishReason: finishReason,
			})
			responseDone = true
			break anthropicStream
		case "error":
			onEvent(protocol.CanonicalStreamEvent{
				Type: protocol.CanonicalStreamError,
				Error: &protocol.CanonicalStreamErrorData{
					Code:    event.Error.Type,
					Message: event.Error.Message,
				},
			})
			return usage, fmt.Errorf("Anthropic stream error %s: %s", event.Error.Type, event.Error.Message)
		}
	}
	for _, toolEvent := range tools.doneAll() {
		onEvent(toolEvent)
	}
	if !responseDone && !usage.IsZero() {
		onEvent(protocol.CanonicalStreamEvent{Type: protocol.CanonicalStreamUsage, Usage: usage})
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("read Anthropic stream: %w", err)
	}
	if !responseDone {
		return usage, io.ErrUnexpectedEOF
	}
	return usage, nil
}

func mergeAnthropicStreamUsage(current protocol.CanonicalUsage, next protocol.AnthropicUsage) protocol.CanonicalUsage {
	if next.InputTokens != 0 || next.CacheCreationInputTokens != 0 || next.CacheReadInputTokens != 0 {
		current.PromptTokens = next.InputTokens +
			next.CacheCreationInputTokens +
			next.CacheReadInputTokens
	}
	if next.OutputTokens != 0 {
		current.CompletionTokens = next.OutputTokens
	}
	if next.CacheReadInputTokens != 0 {
		current.CachedTokens = next.CacheReadInputTokens
	}
	if next.OutputTokensDetails != nil && next.OutputTokensDetails.ThinkingTokens != 0 {
		current.ReasoningTokens = next.OutputTokensDetails.ThinkingTokens
	}
	current.TotalTokens = current.PromptTokens + current.CompletionTokens
	return current
}

type canonicalStreamToolState struct {
	index     int
	callID    string
	name      string
	signature string
	arguments strings.Builder
	done      bool
}

type canonicalStreamToolTracker struct {
	byIndex map[int]*canonicalStreamToolState
	order   []int
}

func newCanonicalStreamToolTracker() *canonicalStreamToolTracker {
	return &canonicalStreamToolTracker{byIndex: make(map[int]*canonicalStreamToolState)}
}

func (t *canonicalStreamToolTracker) ensure(event protocol.CanonicalStreamEvent) (*canonicalStreamToolState, bool) {
	state, exists := t.byIndex[event.Index]
	if !exists {
		state = &canonicalStreamToolState{index: event.Index}
		t.byIndex[event.Index] = state
		t.order = append(t.order, event.Index)
	}
	if state.callID == "" && event.CallID != "" {
		state.callID = event.CallID
	}
	if state.name == "" && event.Name != "" {
		state.name = event.Name
	}
	if event.Signature != "" {
		state.signature = event.Signature
	}
	return state, !exists
}

func (t *canonicalStreamToolTracker) indexForCallID(callID string) (int, bool) {
	if callID == "" {
		return 0, false
	}
	for _, index := range t.order {
		if t.byIndex[index].callID == callID {
			return index, true
		}
	}
	return 0, false
}

func (t *canonicalStreamToolTracker) start(event protocol.CanonicalStreamEvent) (protocol.CanonicalStreamEvent, bool) {
	state, created := t.ensure(event)
	if !created {
		return protocol.CanonicalStreamEvent{}, false
	}
	return protocol.CanonicalStreamEvent{
		Type:      protocol.CanonicalStreamToolCallStart,
		Index:     state.index,
		CallID:    state.callID,
		Name:      state.name,
		Signature: state.signature,
	}, true
}

func (t *canonicalStreamToolTracker) delta(event protocol.CanonicalStreamEvent) protocol.CanonicalStreamEvent {
	state, _ := t.ensure(event)
	state.arguments.WriteString(event.Delta)
	return protocol.CanonicalStreamEvent{
		Type:   protocol.CanonicalStreamToolArgumentsDelta,
		Index:  state.index,
		CallID: state.callID,
		Name:   state.name,
		Delta:  event.Delta,
	}
}

func (t *canonicalStreamToolTracker) done(event protocol.CanonicalStreamEvent) (protocol.CanonicalStreamEvent, bool) {
	state, _ := t.ensure(event)
	if state.done {
		return protocol.CanonicalStreamEvent{}, false
	}
	if event.Arguments != "" {
		state.arguments.Reset()
		state.arguments.WriteString(event.Arguments)
	}
	state.done = true
	return protocol.CanonicalStreamEvent{
		Type:      protocol.CanonicalStreamToolCallDone,
		Index:     state.index,
		CallID:    state.callID,
		Name:      state.name,
		Arguments: state.arguments.String(),
		Signature: state.signature,
	}, true
}

func (t *canonicalStreamToolTracker) doneAll() []protocol.CanonicalStreamEvent {
	out := make([]protocol.CanonicalStreamEvent, 0, len(t.order))
	for _, index := range t.order {
		event, ok := t.done(protocol.CanonicalStreamEvent{
			Type:  protocol.CanonicalStreamToolCallDone,
			Index: index,
		})
		if ok {
			out = append(out, event)
		}
	}
	return out
}

func firstNonEmptyStreamValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
