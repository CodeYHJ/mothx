package agentruntime

import "testing"

func TestReplayDecisionsPairsRequestAndResolution(t *testing.T) {
	pending := ReplayDecisions([]DecisionRecord{
		{ID: "approval-1", RunID: "run-1", Kind: DecisionApproval, Status: "pending"},
		{ID: "question-1", RunID: "run-1", Kind: DecisionQuestion, Status: "pending"},
		{ID: "approval-1", RunID: "run-1", Kind: DecisionApproval, Status: "resolved", Value: "approve_once"},
	})
	if len(pending) != 1 {
		t.Fatalf("pending = %#v", pending)
	}
	if record, ok := pending["question-1"]; !ok || record.Kind != DecisionQuestion {
		t.Fatalf("question pending = %#v", pending)
	}
}
