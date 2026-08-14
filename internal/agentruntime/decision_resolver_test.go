package agentruntime

import "testing"

func TestDecisionServiceBindAndClearRunWithValue(t *testing.T) {
	var service DecisionService
	if err := service.Register(DecisionRequest{ID: "question-1", RunID: "run-1", Kind: DecisionQuestion}); err != nil {
		t.Fatal(err)
	}
	var resolved string
	if err := service.Bind("question-1", func(value string) error {
		resolved = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(DecisionResolution{ID: "question-1", Kind: DecisionQuestion, Status: "resolved", Value: "yes"}); err != nil {
		t.Fatal(err)
	}
	if resolved != "yes" {
		t.Fatalf("resolver value = %q, want yes", resolved)
	}

	if err := service.Register(DecisionRequest{ID: "approval-1", RunID: "run-1", Kind: DecisionApproval}); err != nil {
		t.Fatal(err)
	}
	resolved = ""
	if err := service.Bind("approval-1", func(value string) error {
		resolved = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cleared := service.ClearRunWithValue("run-1", "cancelled")
	if len(cleared) != 1 || cleared[0].ID != "approval-1" {
		t.Fatalf("cleared = %#v", cleared)
	}
	if resolved != "cancelled" {
		t.Fatalf("clear resolver value = %q, want cancelled", resolved)
	}
	if pending := service.Pending(); len(pending) != 0 {
		t.Fatalf("pending after clear = %#v", pending)
	}
}
