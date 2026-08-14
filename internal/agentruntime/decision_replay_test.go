package agentruntime

import (
	"testing"
	"time"
)

func TestReplayDecisionsOmitsExpiredPending(t *testing.T) {
	now := time.Now()
	records := []DecisionRecord{
		{ID: "expired", RunID: "run-1", Kind: DecisionApproval, Status: "pending", ExpiresAt: now.Add(-time.Second)},
		{ID: "active", RunID: "run-1", Kind: DecisionQuestion, Status: "pending", ExpiresAt: now.Add(time.Minute)},
	}
	pending := ReplayDecisionsAt(records, now)
	if len(pending) != 1 || pending["active"].ID != "active" {
		t.Fatalf("pending = %#v", pending)
	}
	expired := ExpiredDecisions(records, now)
	if len(expired) != 1 || expired[0].ID != "expired" {
		t.Fatalf("expired = %#v", expired)
	}
}

func TestExpiredDecisionsHonorsLaterResolution(t *testing.T) {
	now := time.Now()
	expired := ExpiredDecisions([]DecisionRecord{
		{ID: "approval-1", RunID: "run-1", Kind: DecisionApproval, Status: "pending", ExpiresAt: now.Add(-time.Second)},
		{ID: "approval-1", RunID: "run-1", Kind: DecisionApproval, Status: "resolved"},
	}, now)
	if len(expired) != 0 {
		t.Fatalf("resolved decision reported expired: %#v", expired)
	}
}

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
