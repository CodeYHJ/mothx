package agentruntime

import (
	"context"
	"testing"
)

func TestExecutionRuntimeExclusiveBeginAndFinish(t *testing.T) {
	var runtime ExecutionRuntime
	ctx, err := runtime.Begin(context.Background(), "run-1")
	if err != nil || ctx == nil {
		t.Fatalf("Begin: ctx=%v err=%v", ctx, err)
	}
	if _, err := runtime.Begin(context.Background(), "run-2"); err == nil {
		t.Fatal("second Begin succeeded while run is active")
	}
	if id, active := runtime.Active(); !active || id != "run-1" {
		t.Fatalf("Active() = %q, %v", id, active)
	}
	if !runtime.Cancel() {
		t.Fatal("Cancel returned false for active run")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Cancel did not cancel run context")
	}
	runtime.Finish("run-1")
	if _, active := runtime.Active(); active {
		t.Fatal("run stayed active after Finish")
	}
	if got := runtime.State(); got != RunStateCompleted {
		t.Fatalf("State() after Finish = %q, want %q", got, RunStateCompleted)
	}
	if _, err := runtime.Begin(context.Background(), "run-2"); err != nil {
		t.Fatalf("Begin after Finish: %v", err)
	}
}

func TestExecutionRuntimeCancelNeedsExplicitTerminalState(t *testing.T) {
	var runtime ExecutionRuntime
	ctx, err := runtime.Begin(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !runtime.Cancel() {
		t.Fatal("Cancel returned false")
	}
	if got := runtime.State(); got != RunStateCancelling {
		t.Fatalf("State() after Cancel = %q, want %q", got, RunStateCancelling)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Cancel did not cancel context")
	}
	if err := runtime.FinishWithState("run-1", RunStateCancelled); err != nil {
		t.Fatalf("FinishWithState: %v", err)
	}
	if got := runtime.State(); got != RunStateCancelled {
		t.Fatalf("terminal State() = %q, want %q", got, RunStateCancelled)
	}
}

func TestExecutionRuntimeWaitAndResume(t *testing.T) {
	var runtime ExecutionRuntime
	if _, err := runtime.Begin(context.Background(), "run-1"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := runtime.State(); got != RunStateRunning {
		t.Fatalf("initial State() = %q", got)
	}
	if err := runtime.WaitForApproval("run-1"); err != nil {
		t.Fatalf("WaitForApproval: %v", err)
	}
	if got := runtime.State(); got != RunStateWaitingApproval {
		t.Fatalf("approval State() = %q", got)
	}
	if err := runtime.Resume("run-1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := runtime.State(); got != RunStateRunning {
		t.Fatalf("resumed State() = %q", got)
	}
	if err := runtime.WaitForQuestion("other"); err == nil {
		t.Fatal("WaitForQuestion accepted a different run ID")
	}
	runtime.Finish("run-1")
}

func TestExecutionRuntimeExplicitTerminalStates(t *testing.T) {
	var runtime ExecutionRuntime
	if _, err := runtime.Begin(context.Background(), "run-1"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := runtime.FinishWithState("run-1", RunStateFailed); err != nil {
		t.Fatalf("FinishWithState: %v", err)
	}
	if got := runtime.State(); got != RunStateFailed {
		t.Fatalf("State() = %q, want %q", got, RunStateFailed)
	}
	if err := runtime.FinishWithState("run-1", RunStateCompleted); err == nil {
		t.Fatal("FinishWithState succeeded after the run was already terminal")
	}
}

func TestExecutionRuntimeFinishIgnoresDifferentRun(t *testing.T) {
	var runtime ExecutionRuntime
	if _, err := runtime.Begin(context.Background(), "run-1"); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	runtime.Finish("other")
	if id, active := runtime.Active(); !active || id != "run-1" {
		t.Fatalf("wrong Finish cleared active run: %q, %v", id, active)
	}
}
