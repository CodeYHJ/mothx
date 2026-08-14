package agentruntime

import "testing"

func TestDecisionServiceRehydrateIsSortedAndIdempotent(t *testing.T) {
	var service DecisionService
	requests, err := service.Rehydrate([]DecisionRecord{
		{ID: "z", SessionID: "s", RunID: "r", Kind: DecisionQuestion, Status: "pending"},
		{ID: "a", SessionID: "s", RunID: "r", Kind: DecisionApproval, Status: "pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ID != "a" || requests[1].ID != "z" {
		t.Fatalf("requests = %#v", requests)
	}
	again, err := service.Rehydrate([]DecisionRecord{
		{ID: "a", SessionID: "s", RunID: "r", Kind: DecisionApproval, Status: "pending"},
	})
	if err != nil || len(again) != 1 || again[0].ID != "a" {
		t.Fatalf("idempotent rehydrate = %#v, %v", again, err)
	}
}

func TestDecisionServiceRehydrateRejectsConflict(t *testing.T) {
	var service DecisionService
	if _, err := service.Rehydrate([]DecisionRecord{{ID: "d", SessionID: "s", RunID: "r1", Kind: DecisionApproval, Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rehydrate([]DecisionRecord{{ID: "d", SessionID: "s", RunID: "r2", Kind: DecisionApproval, Status: "pending"}}); err == nil {
		t.Fatal("conflicting rehydration unexpectedly succeeded")
	}
}
