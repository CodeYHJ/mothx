package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
)

func TestTUIRunLifecycle(t *testing.T) {
	run := newTUIRun()
	if run == nil || run.id == "" {
		t.Fatal("newTUIRun returned an incomplete run")
	}

	ctx, err := run.execution.Begin(context.Background(), run.id)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if ctx == nil {
		t.Fatal("Begin() returned nil context")
	}
	if got := run.execution.State(); got != agentruntime.RunStateRunning {
		t.Fatalf("State() = %q, want running", got)
	}

	run.finish(agentruntime.RunStateFailed)
	if got := run.execution.State(); got != agentruntime.RunStateFailed {
		t.Fatalf("State() = %q, want failed", got)
	}
	if err := run.execution.FinishWithState(run.id, agentruntime.RunStateCompleted); err == nil {
		t.Fatal("FinishWithState() after terminal state should fail")
	}
}

func TestTUIRunApprovalLifecycle(t *testing.T) {
	run := newTUIRun()
	if _, err := run.execution.Begin(context.Background(), run.id); err != nil {
		t.Fatal(err)
	}
	if err := run.waitForApproval(); err != nil {
		t.Fatalf("waitForApproval() error = %v", err)
	}
	if got := run.execution.State(); got != agentruntime.RunStateWaitingApproval {
		t.Fatalf("State() = %q, want waiting_for_approval", got)
	}
	if err := run.resume(); err != nil {
		t.Fatalf("resume() error = %v", err)
	}
	if got := run.execution.State(); got != agentruntime.RunStateRunning {
		t.Fatalf("State() = %q, want running", got)
	}
}

func TestTUIRunQuestionLifecycle(t *testing.T) {
	run := newTUIRun()
	if _, err := run.execution.Begin(context.Background(), run.id); err != nil {
		t.Fatal(err)
	}
	if err := run.waitForQuestion(); err != nil {
		t.Fatalf("waitForQuestion() error = %v", err)
	}
	if got := run.execution.State(); got != agentruntime.RunStateWaitingQuestion {
		t.Fatalf("State() = %q, want waiting_for_question", got)
	}
	if err := run.resume(); err != nil {
		t.Fatalf("resume() error = %v", err)
	}
	if got := run.execution.State(); got != agentruntime.RunStateRunning {
		t.Fatalf("State() = %q, want running", got)
	}
}
func TestTUIRunDecisionService(t *testing.T) {
	run := newTUIRun("session-1")
	if _, err := run.execution.Begin(context.Background(), run.id); err != nil {
		t.Fatal(err)
	}
	if err := run.registerDecision("approval-1", agentruntime.DecisionApproval); err != nil {
		t.Fatalf("register approval: %v", err)
	}
	if err := run.waitForApproval(); err != nil {
		t.Fatal(err)
	}
	if err := run.resolveDecision("approval-1", agentruntime.DecisionApproval, "true"); err != nil {
		t.Fatalf("resolve approval: %v", err)
	}
	if err := run.resume(); err != nil {
		t.Fatalf("resume after approval: %v", err)
	}
	if got := run.execution.State(); got != agentruntime.RunStateRunning {
		t.Fatalf("State() after approval = %q, want running", got)
	}

	if err := run.registerDecision("question-1", agentruntime.DecisionQuestion); err != nil {
		t.Fatalf("register question: %v", err)
	}
	if got := len(run.decisions.Pending()); got != 1 {
		t.Fatalf("pending decisions = %d, want 1", got)
	}
	run.clearDecisions("cancelled")
	if got := len(run.decisions.Pending()); got != 0 {
		t.Fatalf("pending decisions after clear = %d, want 0", got)
	}
}
func TestTUIRunCancel(t *testing.T) {
	run := newTUIRun()
	if _, err := run.execution.Begin(context.Background(), run.id); err != nil {
		t.Fatal(err)
	}
	if !run.cancel() {
		t.Fatal("cancel() = false, want true")
	}
	if err := run.execution.FinishWithState(run.id, agentruntime.RunStateCancelled); err != nil {
		// Cancellation remains an explicit terminal transition even if the
		// underlying context has already been cancelled.
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FinishWithState() error = %v", err)
		}
	}
}

var _ *agent.Agent
