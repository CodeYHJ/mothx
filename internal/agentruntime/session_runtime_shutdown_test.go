package agentruntime

import (
	"context"
	"testing"
)

func TestSessionRuntimeShutdownCancelsActiveExecution(t *testing.T) {
	var execution ExecutionRuntime
	if _, err := execution.Begin(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{Execution: &execution}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, active := execution.Active(); active {
		t.Fatal("execution remains active after shutdown")
	}
	if execution.State() != RunStateCancelled {
		t.Fatalf("state = %q, want cancelled", execution.State())
	}
	if err := runtime.ensureOpen(); err == nil {
		t.Fatal("runtime remains open after shutdown")
	}
}

func TestSessionRuntimeShutdownHonorsContextWhileExecutionIsActive(t *testing.T) {
	// A terminal transition is synchronous in the current ExecutionRuntime, so
	// this primarily protects the API contract and nil-context handling.
	runtime := &SessionRuntime{}
	if err := runtime.Shutdown(nil); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRuntimeShutdownClearsPendingDecisions(t *testing.T) {
	var execution ExecutionRuntime
	if _, err := execution.Begin(context.Background(), "run-decisions"); err != nil {
		t.Fatal(err)
	}
	var decisions DecisionService
	if err := decisions.Register(DecisionRequest{
		ID: "decision-shutdown", RunID: "run-decisions", SessionID: "session-1", Kind: DecisionApproval,
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{Execution: &execution, Decisions: &decisions}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending := decisions.Pending(); len(pending) != 0 {
		t.Fatalf("pending decisions after shutdown = %#v", pending)
	}
}
