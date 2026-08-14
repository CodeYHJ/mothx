package agentruntime

import (
	"encoding/json"
	"testing"
)

func TestDeliveryPendingDataPreservesCompatibilityFields(t *testing.T) {
	data := DeliveryPendingData("response-run", "response-id", "completed", "entry-1", map[string]any{"usage": "ok"})
	if data["channelDeliveryPending"] != true || data["assistantEntryId"] != "entry-1" {
		t.Fatalf("data = %#v", data)
	}
	if data["responseRunId"] != "response-run" || data["responseId"] != "response-id" || data["state"] != "completed" || data["usage"] != "ok" {
		t.Fatalf("data = %#v", data)
	}
}

func TestNewDeliveryPendingEventReplaysAsPending(t *testing.T) {
	data := DeliveryPendingData("response-run", "response-id", "completed", "entry-1", nil)
	event := NewDeliveryPendingEvent("session-1", "run-1", "channel:wechat", "completed", "model", "yolo", data)
	var decoded map[string]any
	if err := json.Unmarshal(event.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	pending := ReplayDeliveriesFromRunEvents([]RunEvent{event})
	if len(pending) != 1 || pending["run-1"].AssistantEntry != "entry-1" {
		t.Fatalf("pending = %#v", pending)
	}
}
