package openai

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeResponsesSSESupportsFieldsMultilineDataAndDone(t *testing.T) {
	input := "event: response.output_text.delta\r\n" +
		"id: evt_1\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"hello\"}\r\n" +
		"\r\n" +
		": keep-alive\r\n" +
		"data: [DONE]\r\n"

	var frames []responsesSSEFrame
	err := decodeResponsesSSE(strings.NewReader(input), func(frame responsesSSEFrame) error {
		frames = append(frames, frame)
		return nil
	})
	if err != nil {
		t.Fatalf("decodeResponsesSSE() error = %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames))
	}
	if frames[0].Sequence != 1 || frames[0].Event != "response.output_text.delta" || frames[0].ID != "evt_1" {
		t.Fatalf("first frame = %#v", frames[0])
	}
	if frames[0].Data != `{"type":"response.output_text.delta",
"delta":"hello"}` {
		t.Fatalf("multiline data = %q", frames[0].Data)
	}
	if frames[1].Sequence != 2 || frames[1].Data != "[DONE]" {
		t.Fatalf("done frame = %#v", frames[1])
	}
}

func TestDecodeResponsesSSEAcceptsLineDelimitedGatewayEvents(t *testing.T) {
	input := "data: {\"type\":\"response.created\"}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n" +
		"data: [DONE]\n"

	var types []string
	err := decodeResponsesSSE(strings.NewReader(input), func(frame responsesSSEFrame) error {
		types = append(types, frame.Data)
		return nil
	})
	if err != nil {
		t.Fatalf("decodeResponsesSSE() error = %v", err)
	}
	if len(types) != 3 {
		t.Fatalf("frame count = %d, want 3", len(types))
	}
}

func TestResponsesNormalizerInterleavesFunctionArgumentsByItemIdentity(t *testing.T) {
	n := newResponsesNormalizer()
	events := []responsesSSEEvent{
		{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &responsesOutputItem{ID: "item_a", Type: "function_call", CallID: "call_a", Name: "read"},
		},
		{
			Type:        "response.output_item.added",
			OutputIndex: 1,
			Item:        &responsesOutputItem{ID: "item_b", Type: "function_call", CallID: "call_b", Name: "write"},
		},
		{Type: "response.function_call_arguments.delta", ItemID: "item_a", OutputIndex: 0, Delta: `{"path":`},
		{Type: "response.function_call_arguments.delta", ItemID: "item_b", OutputIndex: 1, Delta: `{"path":`},
		{Type: "response.function_call_arguments.delta", ItemID: "item_a", OutputIndex: 0, Delta: `"a"}`},
		{Type: "response.function_call_arguments.delta", ItemID: "item_b", OutputIndex: 1, Delta: `"b"}`},
		{
			Type:        "response.output_item.done",
			OutputIndex: 0,
			Item:        &responsesOutputItem{ID: "item_a", Type: "function_call", CallID: "call_a", Name: "read"},
		},
		{
			Type:        "response.output_item.done",
			OutputIndex: 1,
			Item:        &responsesOutputItem{ID: "item_b", Type: "function_call", CallID: "call_b", Name: "write"},
		},
	}
	for _, event := range events {
		if err := n.apply(event, []byte(`{"type":"`+event.Type+`"}`)); err != nil {
			t.Fatalf("apply(%q) error = %v", event.Type, err)
		}
	}

	calls := n.toolCalls()
	if len(calls) != 2 {
		t.Fatalf("tool call count = %d, want 2", len(calls))
	}
	if calls[0].ID != "call_a" || string(calls[0].Arguments) != `{"path":"a"}` {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].ID != "call_b" || string(calls[1].Arguments) != `{"path":"b"}` {
		t.Fatalf("second call = %#v", calls[1])
	}
}

func TestResponsesNormalizerPreservesUnknownItemWithSanitizedCanonicalJSON(t *testing.T) {
	n := newResponsesNormalizer()
	event := responsesSSEEvent{
		Type:        "response.output_item.done",
		OutputIndex: 3,
		Item:        &responsesOutputItem{ID: "future_1", Type: "future_item", Status: "completed"},
	}
	raw := []byte(`{"type":"response.output_item.done","output_index":3,"item":{"id":"future_1","type":"future_item","secret":"do-not-store"}}`)
	if err := n.apply(event, raw); err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	if n.response.UnknownItems != 1 {
		t.Fatalf("unknown item count = %d, want 1", n.response.UnknownItems)
	}
	if len(n.response.Items) != 1 {
		t.Fatalf("item count = %d, want 1", len(n.response.Items))
	}
	canonical := string(n.response.Items[0].Canonical)
	if strings.Contains(canonical, "do-not-store") || !strings.Contains(canonical, "[REDACTED]") {
		t.Fatalf("canonical item = %s, expected redacted secret", canonical)
	}
}

func TestDecodeResponsesSSEReportsMalformedEventSequence(t *testing.T) {
	errWant := errors.New("stop")
	err := decodeResponsesSSE(strings.NewReader("data: {not-json}\n"), func(frame responsesSSEFrame) error {
		return errWant
	})
	if !errors.Is(err, errWant) {
		t.Fatalf("decode error = %v, want callback error", err)
	}
}
