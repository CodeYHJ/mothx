package agentruntime

import (
	"encoding/json"
	"testing"
)

func TestDecisionRecordKeepsProtocolNeutralPayload(t *testing.T) {
	request := DecisionRequest{ID: "approval-1", SessionID: "session-1", RunID: "run-1", Kind: DecisionApproval}
	record, err := NewDecisionRequestRecord(request, map[string]any{"tool": "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "pending" || record.ID != request.ID || record.Kind != request.Kind {
		t.Fatalf("record = %#v", record)
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["tool"] != "bash" {
		t.Fatalf("payload = %#v", payload)
	}

	resolved, err := NewDecisionResolutionRecord(request, DecisionResolution{ID: request.ID, Kind: request.Kind, Status: "resolved", Value: "approve_once"}, map[string]any{"action": "approve_once"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Status != "resolved" || resolved.Value != "approve_once" {
		t.Fatalf("resolved record = %#v", resolved)
	}
}
