package buzz

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teatak/buzz/internal/protocol"
)

func TestResponsesStreamEncoderPreservesOutputIndexes(t *testing.T) {
	var buf bytes.Buffer
	encoder := newResponsesStreamEncoder(&buf, nil, "resp_1", 123, "model")
	for _, event := range []protocol.CanonicalStreamEvent{
		{Type: protocol.CanonicalStreamToolCallStart, Index: 7, CallID: "call_1", Name: "lookup"},
		{Type: protocol.CanonicalStreamToolArgumentsDelta, Index: 7, Delta: `{"q":"hello"}`},
		{Type: protocol.CanonicalStreamTextDelta, Delta: "done"},
		{Type: protocol.CanonicalStreamToolCallDone, Index: 7, CallID: "call_1", Name: "lookup", Arguments: `{"q":"hello"}`},
		{Type: protocol.CanonicalStreamUsage, Usage: protocol.CanonicalUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}},
		{Type: protocol.CanonicalStreamResponseDone, FinishReason: "tool_calls"},
	} {
		encoder.writeEvent(event)
	}

	events := decodeSSEEvents(t, buf.String())
	assertSSEOutputIndex(t, events, "response.function_call_arguments.delta", 0)
	assertSSEOutputIndex(t, events, "response.output_text.delta", 1)

	completed := findSSEEvent(t, events, "response.completed")
	var response protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(completed["response"], &response); err != nil {
		t.Fatalf("decode completed response: %v", err)
	}
	if len(response.Output) != 2 ||
		response.Output[0].Type != "function_call" ||
		response.Output[0].CallID != "call_1" ||
		response.Output[1].Type != "message" ||
		response.Output[1].Content[0].Text != "done" {
		t.Fatalf("completed output = %+v", response.Output)
	}
	if response.Usage == nil || response.Usage.TotalTokens != 6 {
		t.Fatalf("completed usage = %+v", response.Usage)
	}
}

func TestResponsesStreamDoneEventUsesProtocolSpecificField(t *testing.T) {
	tests := []struct {
		name       string
		event      protocol.CanonicalStreamEvent
		eventType  string
		wantField  string
		otherField string
	}{
		{
			name:       "text",
			event:      protocol.CanonicalStreamEvent{Type: protocol.CanonicalStreamTextDelta, Delta: "answer"},
			eventType:  "response.output_text.done",
			wantField:  "text",
			otherField: "refusal",
		},
		{
			name:       "refusal",
			event:      protocol.CanonicalStreamEvent{Type: protocol.CanonicalStreamRefusalDelta, Delta: "declined"},
			eventType:  "response.refusal.done",
			wantField:  "refusal",
			otherField: "text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			encoder := newResponsesStreamEncoder(&buf, nil, "resp_1", 123, "model")
			encoder.writeEvent(tt.event)
			encoder.writeEvent(protocol.CanonicalStreamEvent{
				Type:         protocol.CanonicalStreamResponseDone,
				FinishReason: "stop",
			})

			event := findSSEEvent(t, decodeSSEEvents(t, buf.String()), tt.eventType)
			if _, ok := event[tt.wantField]; !ok {
				t.Fatalf("%s missing field %q: %s", tt.eventType, tt.wantField, event)
			}
			if _, ok := event[tt.otherField]; ok {
				t.Fatalf("%s unexpectedly contains %q: %s", tt.eventType, tt.otherField, event)
			}
		})
	}
}

func TestAnthropicStreamEncoderWritesToolBlock(t *testing.T) {
	var buf bytes.Buffer
	encoder := newAnthropicStreamEncoder(&buf, nil, "msg_1", "model")
	for _, event := range []protocol.CanonicalStreamEvent{
		{Type: protocol.CanonicalStreamTextDelta, Delta: "checking"},
		{Type: protocol.CanonicalStreamToolCallStart, Index: 0, CallID: "call_1", Name: "lookup"},
		{Type: protocol.CanonicalStreamToolArgumentsDelta, Index: 0, Delta: `{"q":`},
		{Type: protocol.CanonicalStreamToolArgumentsDelta, Index: 0, Delta: `"hello"}`},
		{Type: protocol.CanonicalStreamToolCallDone, Index: 0, CallID: "call_1", Name: "lookup", Arguments: `{"q":"hello"}`},
		{Type: protocol.CanonicalStreamUsage, Usage: protocol.CanonicalUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}},
		{Type: protocol.CanonicalStreamResponseDone, FinishReason: "tool_calls"},
	} {
		encoder.writeEvent(event)
	}

	events := decodeSSEEvents(t, buf.String())
	wantTypes := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %s", len(events), len(wantTypes), buf.String())
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, events[index].Type, want)
		}
	}
	var toolDelta struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(events[5].Data, &toolDelta); err != nil {
		t.Fatalf("decode tool delta: %v", err)
	}
	if toolDelta.Index != 1 || toolDelta.Delta.Type != "input_json_delta" || toolDelta.Delta.PartialJSON != `{"q":"hello"}` {
		t.Fatalf("tool delta = %+v", toolDelta)
	}
	var messageDelta struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage protocol.AnthropicUsage `json:"usage"`
	}
	if err := json.Unmarshal(events[7].Data, &messageDelta); err != nil {
		t.Fatalf("decode message delta: %v", err)
	}
	if messageDelta.Delta.StopReason != "tool_use" || messageDelta.Usage.InputTokens != 4 || messageDelta.Usage.OutputTokens != 2 {
		t.Fatalf("message delta = %+v", messageDelta)
	}
}

func TestResponsesStreamEncoderWritesReasoning(t *testing.T) {
	var buf bytes.Buffer
	encoder := newResponsesStreamEncoder(&buf, nil, "resp_1", 123, "model")
	for _, event := range []protocol.CanonicalStreamEvent{
		{Type: protocol.CanonicalStreamReasoningDelta, Delta: "think"},
		{Type: protocol.CanonicalStreamReasoningDelta, Delta: "ing", Signature: "encrypted"},
		{Type: protocol.CanonicalStreamTextDelta, Delta: "answer"},
		{Type: protocol.CanonicalStreamResponseDone, FinishReason: "stop"},
	} {
		encoder.writeEvent(event)
	}

	events := decodeSSEEvents(t, buf.String())
	assertSSEOutputIndex(t, events, "response.reasoning_summary_text.delta", 0)
	assertSSEOutputIndex(t, events, "response.output_text.delta", 1)
	completed := findSSEEvent(t, events, "response.completed")
	var response protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(completed["response"], &response); err != nil {
		t.Fatalf("decode completed response: %v", err)
	}
	if len(response.Output) != 2 ||
		response.Output[0].Type != "reasoning" ||
		response.Output[0].Summary[0].Text != "thinking" ||
		response.Output[0].EncryptedContent != "encrypted" ||
		response.Output[1].Type != "message" {
		t.Fatalf("completed output = %+v", response.Output)
	}
}

func TestAnthropicStreamEncoderWritesReasoning(t *testing.T) {
	var buf bytes.Buffer
	encoder := newAnthropicStreamEncoder(&buf, nil, "msg_1", "model")
	for _, event := range []protocol.CanonicalStreamEvent{
		{Type: protocol.CanonicalStreamReasoningDelta, Delta: "thinking"},
		{Type: protocol.CanonicalStreamReasoningDelta, Signature: "sig"},
		{Type: protocol.CanonicalStreamTextDelta, Delta: "answer"},
		{Type: protocol.CanonicalStreamResponseDone, FinishReason: "stop"},
	} {
		encoder.writeEvent(event)
	}

	events := decodeSSEEvents(t, buf.String())
	wantTypes := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %s", len(events), len(wantTypes), buf.String())
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, events[index].Type, want)
		}
	}
	var signature struct {
		Delta struct {
			Type      string `json:"type"`
			Signature string `json:"signature"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(events[3].Data, &signature); err != nil {
		t.Fatal(err)
	}
	if signature.Delta.Type != "signature_delta" || signature.Delta.Signature != "sig" {
		t.Fatalf("signature event = %+v", signature)
	}
}

func TestAnthropicStreamEncoderWritesRefusalStopDetails(t *testing.T) {
	var buf bytes.Buffer
	encoder := newAnthropicStreamEncoder(&buf, nil, "msg_1", "model")
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:  protocol.CanonicalStreamRefusalDelta,
		Delta: "request declined",
	})
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:         protocol.CanonicalStreamResponseDone,
		FinishReason: "content_filter",
	})

	events := decodeSSEEvents(t, buf.String())
	messageDelta := findSSEEvent(t, events, "message_delta")
	var got struct {
		Delta struct {
			StopReason  string                         `json:"stop_reason"`
			StopDetails *protocol.AnthropicStopDetails `json:"stop_details"`
		} `json:"delta"`
	}
	raw, err := json.Marshal(messageDelta)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Delta.StopReason != "refusal" ||
		got.Delta.StopDetails == nil ||
		got.Delta.StopDetails.Explanation != "request declined" {
		t.Fatalf("message delta = %+v", got)
	}
}

func TestResponsesStreamEncoderCompletesRefusal(t *testing.T) {
	var buf bytes.Buffer
	encoder := newResponsesStreamEncoder(&buf, nil, "resp_1", 123, "model")
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:  protocol.CanonicalStreamRefusalDelta,
		Delta: "request declined",
	})
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:         protocol.CanonicalStreamResponseDone,
		FinishReason: "content_filter",
	})

	events := decodeSSEEvents(t, buf.String())
	completed := findSSEEvent(t, events, "response.completed")
	var response protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(completed["response"], &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "completed" ||
		response.IncompleteDetails != nil ||
		len(response.Output) != 1 ||
		response.Output[0].Content[0].Refusal != "request declined" {
		t.Fatalf("completed refusal = %+v", response)
	}
}

func TestResponsesStreamEncoderPreservesIncompleteRefusal(t *testing.T) {
	var buf bytes.Buffer
	encoder := newResponsesStreamEncoder(&buf, nil, "resp_1", 123, "model")
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:  protocol.CanonicalStreamRefusalDelta,
		Delta: "partial refusal",
	})
	encoder.writeEvent(protocol.CanonicalStreamEvent{
		Type:         protocol.CanonicalStreamResponseDone,
		Status:       "incomplete",
		FinishReason: "content_filter",
	})

	events := decodeSSEEvents(t, buf.String())
	incomplete := findSSEEvent(t, events, "response.incomplete")
	var response protocol.OpenAIResponsesResponse
	if err := json.Unmarshal(incomplete["response"], &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "incomplete" ||
		response.IncompleteDetails == nil ||
		response.IncompleteDetails.Reason != "content_filter" ||
		len(response.Output) != 1 ||
		response.Output[0].Status != "incomplete" {
		t.Fatalf("incomplete refusal = %+v", response)
	}
}

func TestReadOpenAIChatToolStreamAsCanonical(t *testing.T) {
	stream := strings.NewReader(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hello\"}"}}]}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
			`data: {"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n" +
			"data: [DONE]\n\n",
	)

	var events []protocol.CanonicalStreamEvent
	usage, err := readOpenAIChatStreamAsCanonical(stream, func(event protocol.CanonicalStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCanonicalToolStream(t, events, "call_1", "lookup", `{"q":"hello"}`)
	if usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	if events[len(events)-2].Type != protocol.CanonicalStreamUsage ||
		events[len(events)-1].Type != protocol.CanonicalStreamResponseDone {
		t.Fatalf("event order = %+v", events)
	}
}

func TestReadResponsesToolStreamAsCanonical(t *testing.T) {
	stream := strings.NewReader(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}` + "\n\n" +
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":"}` + "\n\n" +
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"hello\"}"}` + "\n\n" +
			`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"name":"lookup","arguments":"{\"q\":\"hello\"}"}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"model","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hello\"}"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}` + "\n\n",
	)

	var events []protocol.CanonicalStreamEvent
	usage, err := readResponsesStreamAsCanonical(stream, func(event protocol.CanonicalStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCanonicalToolStream(t, events, "call_1", "lookup", `{"q":"hello"}`)
	if usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestReadAnthropicToolStreamAsCanonical(t *testing.T) {
	stream := strings.NewReader(
		`data: {"type":"message_start","message":{"id":"msg_1","role":"assistant","model":"claude","usage":{"input_tokens":4}}}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}` + "\n\n" +
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"hello\"}"}}` + "\n\n" +
			`data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":2}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n",
	)

	var events []protocol.CanonicalStreamEvent
	usage, err := readAnthropicStreamAsCanonical(stream, func(event protocol.CanonicalStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}

	assertCanonicalToolStream(t, events, "call_1", "lookup", `{"q":"hello"}`)
	if usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", usage)
	}
	if events[len(events)-1].Type != protocol.CanonicalStreamResponseDone ||
		events[len(events)-1].FinishReason != "tool_calls" {
		t.Fatalf("last event = %+v", events[len(events)-1])
	}
}

func TestReadReasoningStreamsAsCanonical(t *testing.T) {
	tests := []struct {
		name string
		read func(func(protocol.CanonicalStreamEvent)) error
	}{
		{
			name: "responses",
			read: func(onEvent func(protocol.CanonicalStreamEvent)) error {
				_, err := readResponsesStreamAsCanonical(strings.NewReader(
					`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thinking"}`+"\n\n"+
						`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","encrypted_content":"encrypted","summary":[{"type":"summary_text","text":"thinking"}]}}`+"\n\n"+
						`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n",
				), onEvent)
				return err
			},
		},
		{
			name: "anthropic",
			read: func(onEvent func(protocol.CanonicalStreamEvent)) error {
				_, err := readAnthropicStreamAsCanonical(strings.NewReader(
					`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`+"\n\n"+
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking"}}`+"\n\n"+
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`+"\n\n"+
						`data: {"type":"content_block_stop","index":0}`+"\n\n"+
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{}}`+"\n\n"+
						`data: {"type":"message_stop"}`+"\n\n",
				), onEvent)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []protocol.CanonicalStreamEvent
			if err := tt.read(func(event protocol.CanonicalStreamEvent) {
				events = append(events, event)
			}); err != nil {
				t.Fatal(err)
			}
			var reasoning strings.Builder
			var signature strings.Builder
			for _, event := range events {
				if event.Type == protocol.CanonicalStreamReasoningDelta {
					reasoning.WriteString(event.Delta)
					signature.WriteString(event.Signature)
				}
			}
			if reasoning.String() != "thinking" {
				t.Fatalf("reasoning = %q, events = %+v", reasoning.String(), events)
			}
			wantSignature := "sig"
			if tt.name == "responses" {
				wantSignature = "encrypted"
			}
			if signature.String() != wantSignature {
				t.Fatalf("signature = %q, events = %+v", signature.String(), events)
			}
		})
	}
}

func TestReadStreamErrorsAsCanonical(t *testing.T) {
	tests := []struct {
		name string
		read func(func(protocol.CanonicalStreamEvent)) error
	}{
		{
			name: "openai chat",
			read: func(onEvent func(protocol.CanonicalStreamEvent)) error {
				_, err := readOpenAIChatStreamAsCanonical(strings.NewReader(
					`data: {"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"slow down"}}`+"\n\n",
				), onEvent)
				return err
			},
		},
		{
			name: "gemini",
			read: func(onEvent func(protocol.CanonicalStreamEvent)) error {
				_, err := readGeminiStreamAsCanonical(strings.NewReader(
					`data: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"slow down"}}`+"\n\n",
				), "request-id", onEvent)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *protocol.CanonicalStreamErrorData
			err := tt.read(func(event protocol.CanonicalStreamEvent) {
				if event.Type == protocol.CanonicalStreamError {
					got = event.Error
				}
			})
			if err == nil {
				t.Fatal("expected stream error")
			}
			if got == nil || got.Message != "slow down" {
				t.Fatalf("error = %+v", got)
			}
		})
	}
}

func TestStreamErrorEncodersUseInboundShape(t *testing.T) {
	event := protocol.CanonicalStreamEvent{
		Type: protocol.CanonicalStreamError,
		Error: &protocol.CanonicalStreamErrorData{
			Code:    "RESOURCE_EXHAUSTED",
			Message: "slow down",
		},
	}

	var anthropic bytes.Buffer
	newAnthropicStreamEncoder(&anthropic, nil, "msg_1", "model").writeEvent(event)
	anthropicEvents := decodeSSEEvents(t, anthropic.String())
	if anthropicEvents[len(anthropicEvents)-1].Type != "error" ||
		!strings.Contains(string(anthropicEvents[len(anthropicEvents)-1].Data), `"type":"rate_limit_error"`) {
		t.Fatalf("anthropic stream = %s", anthropic.String())
	}

	var gemini bytes.Buffer
	writeGeminiStreamEvent(&gemini, nil, event)
	if !strings.Contains(gemini.String(), `"code":429`) ||
		!strings.Contains(gemini.String(), `"status":"RESOURCE_EXHAUSTED"`) ||
		!strings.Contains(gemini.String(), `"message":"slow down"`) {
		t.Fatalf("gemini stream = %s", gemini.String())
	}

	openAI := httptest.NewRecorder()
	streamCanonicalAsOpenAIChat(openAI, "chatcmpl_1", 1, "model", false, func(emit func(protocol.CanonicalStreamEvent)) (protocol.CanonicalUsage, error) {
		emit(event)
		return protocol.CanonicalUsage{}, nil
	})
	if !strings.Contains(openAI.Body.String(), `"error"`) ||
		!strings.Contains(openAI.Body.String(), `"message":"slow down"`) ||
		strings.Contains(openAI.Body.String(), "[DONE]") {
		t.Fatalf("openai stream = %s", openAI.Body.String())
	}
}

func TestStreamReadersRejectMalformedAndTruncatedInput(t *testing.T) {
	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "OpenAI Chat malformed JSON",
			read: func() error {
				_, err := readOpenAIChatStreamAsCanonical(strings.NewReader("data: {\n\n"), func(protocol.CanonicalStreamEvent) {})
				return err
			},
		},
		{
			name: "Responses truncated",
			read: func() error {
				_, err := readResponsesStreamAsCanonical(strings.NewReader(
					`data: {"type":"response.output_text.delta","delta":"partial"}`+"\n\n",
				), func(protocol.CanonicalStreamEvent) {})
				return err
			},
		},
		{
			name: "Gemini truncated",
			read: func() error {
				_, err := readGeminiStreamAsCanonical(strings.NewReader(
					`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`+"\n\n",
				), "request-id", func(protocol.CanonicalStreamEvent) {})
				return err
			},
		},
		{
			name: "Anthropic truncated",
			read: func() error {
				_, err := readAnthropicStreamAsCanonical(strings.NewReader(
					`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`+"\n\n",
				), func(protocol.CanonicalStreamEvent) {})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.read(); err == nil {
				t.Fatal("expected stream error")
			}
		})
	}
}

func TestReadResponsesIncompleteAndRefusalStream(t *testing.T) {
	stream := strings.NewReader(
		`data: {"type":"response.refusal.delta","output_index":0,"delta":"cannot comply"}` + "\n\n" +
			`data: {"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}` + "\n\n",
	)
	var events []protocol.CanonicalStreamEvent
	_, err := readResponsesStreamAsCanonical(stream, func(event protocol.CanonicalStreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].Type != protocol.CanonicalStreamRefusalDelta ||
		events[0].Delta != "cannot comply" ||
		events[1].Type != protocol.CanonicalStreamResponseDone ||
		events[1].Status != "incomplete" ||
		events[1].FinishReason != "content_filter" {
		t.Fatalf("events = %+v", events)
	}
}

func assertCanonicalToolStream(t *testing.T, events []protocol.CanonicalStreamEvent, callID, name, arguments string) {
	t.Helper()
	var start *protocol.CanonicalStreamEvent
	var deltas strings.Builder
	var done *protocol.CanonicalStreamEvent
	for i := range events {
		event := &events[i]
		switch event.Type {
		case protocol.CanonicalStreamToolCallStart:
			if start != nil {
				t.Fatalf("duplicate tool start: %+v", events)
			}
			start = event
		case protocol.CanonicalStreamToolArgumentsDelta:
			deltas.WriteString(event.Delta)
		case protocol.CanonicalStreamToolCallDone:
			if done != nil {
				t.Fatalf("duplicate tool done: %+v", events)
			}
			done = event
		}
	}
	if start == nil || start.CallID != callID || start.Name != name {
		t.Fatalf("tool start = %+v, events = %+v", start, events)
	}
	if deltas.String() != arguments {
		t.Fatalf("tool argument deltas = %q, want %q", deltas.String(), arguments)
	}
	if done == nil || done.CallID != callID || done.Name != name || done.Arguments != arguments {
		t.Fatalf("tool done = %+v, events = %+v", done, events)
	}
}

type decodedSSEEvent struct {
	Type string
	Data json.RawMessage
}

func decodeSSEEvents(t *testing.T, stream string) []decodedSSEEvent {
	t.Helper()
	blocks := strings.Split(strings.TrimSpace(stream), "\n\n")
	events := make([]decodedSSEEvent, 0, len(blocks))
	for _, block := range blocks {
		var event decodedSSEEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				event.Type = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				event.Data = json.RawMessage(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if event.Type == "" {
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(event.Data, &envelope); err != nil {
				t.Fatalf("decode event envelope: %v", err)
			}
			event.Type = envelope.Type
		}
		events = append(events, event)
	}
	return events
}

func findSSEEvent(t *testing.T, events []decodedSSEEvent, eventType string) map[string]json.RawMessage {
	t.Helper()
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(event.Data, &value); err != nil {
			t.Fatalf("decode %s: %v", eventType, err)
		}
		return value
	}
	t.Fatalf("missing SSE event %q", eventType)
	return nil
}

func assertSSEOutputIndex(t *testing.T, events []decodedSSEEvent, eventType string, want int) {
	t.Helper()
	event := findSSEEvent(t, events, eventType)
	var got int
	if err := json.Unmarshal(event["output_index"], &got); err != nil {
		t.Fatalf("decode %s output_index: %v", eventType, err)
	}
	if got != want {
		t.Fatalf("%s output_index = %d, want %d", eventType, got, want)
	}
}
