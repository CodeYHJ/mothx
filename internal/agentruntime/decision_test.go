package agentruntime

import "testing"

func TestDecisionServiceFirstResponseWins(t *testing.T) {
	var service DecisionService
	request := DecisionRequest{ID: "approval-1", RunID: "run-1", SessionID: "session-1", Kind: DecisionApproval}
	if err := service.Register(request); err != nil {
		t.Fatal(err)
	}
	got, err := service.Resolve(DecisionResolution{ID: request.ID, Kind: DecisionApproval, Status: "resolved", Value: "approve_once"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != request.ID || got.RunID != request.RunID || got.SessionID != request.SessionID || got.Kind != request.Kind {
		t.Fatalf("resolved request = %#v, want %#v", got, request)
	}
	if _, err := service.Resolve(DecisionResolution{ID: request.ID, Kind: DecisionApproval}); err == nil {
		t.Fatal("second resolution unexpectedly succeeded")
	}
}

func TestDecisionServiceRejectsDuplicateAndClearRun(t *testing.T) {
	var service DecisionService
	request := DecisionRequest{ID: "question-1", RunID: "run-1", Kind: DecisionQuestion}
	if err := service.Register(request); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(request); err == nil {
		t.Fatal("duplicate decision unexpectedly registered")
	}
	cleared := service.ClearRun(request.RunID)
	if len(cleared) != 1 || cleared[0].ID != request.ID || cleared[0].RunID != request.RunID || cleared[0].Kind != request.Kind {
		t.Fatalf("cleared = %#v, want %#v", cleared, []DecisionRequest{request})
	}
	if pending := service.Pending(); len(pending) != 0 {
		t.Fatalf("pending decisions remain: %#v", pending)
	}
}
