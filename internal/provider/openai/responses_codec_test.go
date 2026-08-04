package openai

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readResponsesFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "responses", name))
	if err != nil {
		t.Fatalf("read Responses fixture %q: %v", name, err)
	}
	return data
}

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

func TestResponsesProtocolFixtures(t *testing.T) {
	t.Run("custom tool SSE", func(t *testing.T) {
		n := newResponsesNormalizer()
		err := decodeResponsesSSE(strings.NewReader(string(readResponsesFixture(t, "custom_tool_call.sse"))), func(frame responsesSSEFrame) error {
			var event responsesSSEEvent
			if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
				return err
			}
			return n.apply(event, []byte(frame.Data))
		})
		if err != nil {
			t.Fatalf("decode custom fixture: %v", err)
		}
		calls := n.toolCalls()
		if len(calls) != 1 || calls[0].Kind != "custom" || calls[0].ID != "call_fixture_1" || calls[0].Input != "echo fixture" {
			t.Fatalf("custom fixture calls = %#v", calls)
		}
	})

	t.Run("hosted output items", func(t *testing.T) {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(readResponsesFixture(t, "hosted_items.json"), &rawItems); err != nil {
			t.Fatalf("decode hosted fixture: %v", err)
		}
		n := newResponsesNormalizer()
		for index, raw := range rawItems {
			item, err := decodeResponsesOutputItem(raw, index)
			if err != nil {
				t.Fatalf("decode hosted item %d: %v", index, err)
			}
			n.upsertDecodedItem(item)
		}
		attachments := n.attachments()
		if len(attachments) != 4 || attachments[0].ProviderRef != "file_fixture_1" || attachments[1].ProviderRef != "container_fixture_1" || attachments[2].URL != "https://files.example.test/plot.png" || attachments[3].ProviderRef != "image_fixture_1" {
			t.Fatalf("hosted fixture attachments = %#v", attachments)
		}
	})

	t.Run("computer use rejected", func(t *testing.T) {
		raw := readResponsesFixture(t, "computer_use_item.json")
		var event responsesSSEEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode computer fixture: %v", err)
		}
		n := newResponsesNormalizer()
		if err := n.apply(event, raw); err != nil {
			t.Fatalf("apply computer fixture: %v", err)
		}
		if err := n.unsupportedError(); err == nil || !strings.Contains(err.Error(), "computer use") {
			t.Fatalf("computer fixture error = %v", err)
		}
		if len(n.response.Items) != 1 || strings.Contains(string(n.response.Items[0].Canonical), "redact-me") {
			t.Fatalf("computer fixture archive = %#v", n.response.Items)
		}
	})

	t.Run("incomplete terminal", func(t *testing.T) {
		n := newResponsesNormalizer()
		err := decodeResponsesSSE(strings.NewReader(string(readResponsesFixture(t, "incomplete_terminal.sse"))), func(frame responsesSSEFrame) error {
			var event responsesSSEEvent
			if err := json.Unmarshal([]byte(frame.Data), &event); err != nil {
				return err
			}
			return n.apply(event, []byte(frame.Data))
		})
		if err != nil {
			t.Fatalf("decode incomplete fixture: %v", err)
		}
		if n.response.ID != "resp_incomplete_fixture" || n.response.Status != "incomplete" || n.response.PreviousResponseID != "resp_previous_fixture" || n.response.ConversationID != "conv_fixture" || n.response.IncompleteReason != "max_output_tokens" {
			t.Fatalf("incomplete fixture response = %#v", n.response)
		}
		if len(n.response.Items) != 1 || n.response.Items[0].ID != "msg_incomplete_1" {
			t.Fatalf("incomplete fixture items = %#v", n.response.Items)
		}
	})
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

func TestResponsesNormalizerCollectsCustomToolInput(t *testing.T) {
	n := newResponsesNormalizer()
	events := []responsesSSEEvent{
		{
			Type:        "response.output_item.added",
			OutputIndex: 0,
			Item:        &responsesOutputItem{ID: "custom_1", Type: "custom_tool_call", CallID: "call_custom", Name: "shell_script"},
		},
		{Type: "response.custom_tool_call_input.delta", ItemID: "custom_1", OutputIndex: 0, Delta: "echo "},
		{Type: "response.custom_tool_call_input.done", ItemID: "custom_1", OutputIndex: 0, Input: "echo hello"},
	}
	for _, event := range events {
		if err := n.apply(event, []byte(`{"type":"`+event.Type+`"}`)); err != nil {
			t.Fatalf("apply(%q) error = %v", event.Type, err)
		}
	}
	calls := n.toolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.ID != "call_custom" || call.Kind != "custom" || call.Input != "echo hello" || string(call.Arguments) != `{"input":"echo hello"}` {
		t.Fatalf("custom call = %#v", call)
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

func TestResponsesNormalizerRejectsComputerUseItem(t *testing.T) {
	n := newResponsesNormalizer()
	event := responsesSSEEvent{
		Type:        "response.output_item.done",
		OutputIndex: 0,
		Item:        &responsesOutputItem{ID: "computer_1", Type: "computer_call", Status: "completed"},
	}
	raw := []byte(`{"type":"response.output_item.done","item":{"id":"computer_1","type":"computer_call","action":{"type":"screenshot","secret":"redact-me"}}}`)
	if err := n.apply(event, raw); err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	if err := n.unsupportedError(); err == nil || !strings.Contains(err.Error(), "computer use is not supported") {
		t.Fatalf("unsupported error = %v", err)
	}
	if n.response.UnknownItems != 1 || len(n.response.Items) != 1 {
		t.Fatalf("response state = unknown=%d items=%d", n.response.UnknownItems, len(n.response.Items))
	}
	if metadata := n.metadata(); metadata["computerUseRejected"] != true {
		t.Fatalf("metadata = %#v, want computerUseRejected", metadata)
	}
	if strings.Contains(string(n.response.Items[0].Canonical), "redact-me") {
		t.Fatalf("canonical item leaked sensitive payload: %s", n.response.Items[0].Canonical)
	}
}

func TestResponsesNormalizerExtractsSafeHostedToolAttachments(t *testing.T) {
	n := newResponsesNormalizer()
	events := []struct {
		event responsesSSEEvent
		raw   string
	}{
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 0, Item: &responsesOutputItem{ID: "msg_1", Type: "message", Status: "completed"}},
			raw:   `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[{"type":"url_citation","title":"OpenAI","url":"https://openai.com","start_index":2,"end_index":8}]}]}}`,
		},
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 1, Item: &responsesOutputItem{ID: "file_1", Type: "code_interpreter_call", Status: "completed"}},
			raw:   `{"type":"response.output_item.done","output_index":1,"item":{"id":"file_1","type":"code_interpreter_call","status":"completed","container_id":"container_123","result":{"type":"container_file_citation","file_id":"file_123","filename":"report.csv"}}}`,
		},
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 2, Item: &responsesOutputItem{ID: "mcp_1", Type: "mcp_call"}},
			raw:   `{"type":"response.output_item.done","output_index":2,"item":{"id":"mcp_1","type":"mcp_call","server_url":"https://private.example/mcp","result":{"type":"image","url":"https://untrusted.example/payload.png"}}}`,
		},
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 3, Item: &responsesOutputItem{ID: "image_1", Type: "image_generation_call"}},
			raw:   `{"type":"response.output_item.done","output_index":3,"item":{"id":"image_1","type":"image_generation_call","result":"aW1n"}}`,
		},
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 4, Item: &responsesOutputItem{ID: "search_1", Type: "file_search_call", Status: "completed"}},
			raw:   `{"type":"response.output_item.done","output_index":4,"item":{"id":"search_1","type":"file_search_call","status":"completed","results":[{"file_id":"file_search_1","filename":"guide.md","score":0.92,"text":"matching content"}]}}`,
		},
		{
			event: responsesSSEEvent{Type: "response.output_item.done", OutputIndex: 5, Item: &responsesOutputItem{ID: "unsafe_1", Type: "message", Status: "completed"}},
			raw:   `{"type":"response.output_item.done","output_index":5,"item":{"id":"unsafe_1","type":"message","status":"completed","content":[{"type":"output_text","annotations":[{"type":"url_citation","title":"unsafe","url":"javascript:alert(1)"}]}]}}`,
		},
	}
	for _, entry := range events {
		if err := n.apply(entry.event, []byte(entry.raw)); err != nil {
			t.Fatalf("apply attachment event: %v", err)
		}
	}
	attachments := n.attachments()
	if len(attachments) != 5 {
		t.Fatalf("attachments = %#v, want citation, container, file, generated image, and file-search result", attachments)
	}
	if attachments[0].Kind != "citation" || attachments[0].URL != "https://openai.com" {
		t.Fatalf("citation = %#v", attachments[0])
	}
	if attachments[0].Metadata["responseItemId"] != "msg_1" || attachments[0].Metadata["responseItemType"] != "message" || attachments[0].Metadata["status"] != "completed" || attachments[0].Metadata["start_index"] != float64(2) || attachments[0].Metadata["end_index"] != float64(8) {
		t.Fatalf("citation provenance = %#v", attachments[0].Metadata)
	}
	if attachments[1].Kind != "artifact" || attachments[1].ProviderRef != "container_123" || attachments[1].Metadata["tool"] != "code_interpreter" {
		t.Fatalf("container attachment = %#v", attachments[1])
	}
	if attachments[2].Kind != "file" || attachments[2].ProviderRef != "file_123" || attachments[2].Metadata["responseItemId"] != "file_1" {
		t.Fatalf("file attachment = %#v", attachments[2])
	}
	if attachments[3].Kind != "image" || attachments[3].ProviderRef != "image_1" || attachments[3].Metadata["encodedBytes"] != 4 {
		t.Fatalf("generated image attachment = %#v", attachments[2])
	}
	if attachments[4].Kind != "file" || attachments[4].ProviderRef != "file_search_1" || attachments[4].Metadata["responseItemId"] != "search_1" || attachments[4].Metadata["responseItemType"] != "file_search_call" || attachments[4].Metadata["score"] != 0.92 {
		t.Fatalf("file search attachment = %#v", attachments[4])
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
