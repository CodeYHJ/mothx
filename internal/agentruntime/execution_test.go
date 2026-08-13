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
	if _, err := runtime.Begin(context.Background(), "run-2"); err != nil {
		t.Fatalf("Begin after Finish: %v", err)
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
