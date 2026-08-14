package agentruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
)

// RunState is the adapter-neutral lifecycle state of an active execution.
type RunState string

const (
	RunStateCreated         RunState = "created"
	RunStateRunning         RunState = "running"
	RunStateWaitingApproval RunState = "waiting_for_approval"
	RunStateWaitingQuestion RunState = "waiting_for_question"
	RunStateCancelling      RunState = "cancelling"
	RunStateCompleted       RunState = "completed"
	RunStateFailed          RunState = "failed"
	RunStateCancelled       RunState = "cancelled"
	RunStateTimedOut        RunState = "timed_out"
)

// ExecutionRuntime owns the adapter-neutral active run state for one session.
// Adapters remain responsible for persistence, event encoding, and admission
// locks; this type owns cancellation and terminal transitions.
type ExecutionRuntime struct {
	mu        sync.Mutex
	runID     string
	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	running   *agent.Agent
	state     RunState
	finished  bool
	events    RunEventSink
}

// Begin starts one exclusive execution. The caller must finish the run exactly
// once, including when agent construction fails.
func (r *ExecutionRuntime) Begin(parent context.Context, runID string) (context.Context, error) {
	if r == nil {
		return nil, fmt.Errorf("execution runtime is nil")
	}
	if runID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	if parent == nil {
		parent = context.Background()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil && !r.finished {
		return nil, fmt.Errorf("execution already active: %s", r.runID)
	}
	ctx, cancel := context.WithCancel(parent)
	r.runID = runID
	r.startedAt = time.Now()
	r.ctx = ctx
	r.cancel = cancel
	r.running = nil
	r.finished = false
	r.state = RunStateRunning
	return ctx, nil
}

// WaitForApproval transitions an active run into an approval wait.
func (r *ExecutionRuntime) WaitForApproval(runID string) error {
	return r.waitFor(runID, RunStateWaitingApproval)
}

// WaitForQuestion transitions an active run into a question wait.
func (r *ExecutionRuntime) WaitForQuestion(runID string) error {
	return r.waitFor(runID, RunStateWaitingQuestion)
}

func (r *ExecutionRuntime) waitFor(runID string, state RunState) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.activeLocked(runID) {
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.state != RunStateRunning {
		return fmt.Errorf("execution %s is not running: %s", runID, r.state)
	}
	r.state = state
	return nil
}

// Resume returns a run from an approval or question wait to active execution.
func (r *ExecutionRuntime) Resume(runID string) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.activeLocked(runID) {
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.state != RunStateWaitingApproval && r.state != RunStateWaitingQuestion {
		return fmt.Errorf("execution %s is not waiting: %s", runID, r.state)
	}
	r.state = RunStateRunning
	return nil
}

func (r *ExecutionRuntime) activeLocked(runID string) bool {
	return r.cancel != nil && !r.finished && (runID == "" || r.runID == runID)
}

// SetAgent associates the core agent so cancellation can unblock agent waits.
func (r *ExecutionRuntime) SetAgent(a *agent.Agent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.running = a
	r.mu.Unlock()
}

// Cancel requests context cancellation and aborts the core agent if present.
func (r *ExecutionRuntime) Cancel() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	cancel := r.cancel
	a := r.running
	if cancel == nil || r.finished {
		r.mu.Unlock()
		return false
	}
	r.state = RunStateCancelling
	r.mu.Unlock()

	cancel()
	if a != nil {
		a.Abort()
	}
	return true
}

// SetEventSink attaches the durable event sink used by adapter-neutral run
// lifecycle helpers. It does not emit an event by itself.
func (r *ExecutionRuntime) SetEventSink(sink RunEventSink) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = sink
	r.mu.Unlock()
}

// RecordEvent persists one adapter-neutral run event when a sink is attached.
func (r *ExecutionRuntime) RecordEvent(ev RunEvent) (string, error) {
	if r == nil {
		return "", fmt.Errorf("execution runtime is nil")
	}
	r.mu.Lock()
	sink := r.events
	r.mu.Unlock()
	if sink == nil {
		return "", nil
	}
	return sink.Record(ev)
}

// Finish transitions the active run to completed. Callers that know the run
// failed, was cancelled, or timed out must use FinishWithState.
func (r *ExecutionRuntime) Finish(runID string) {
	_ = r.FinishWithState(runID, RunStateCompleted)
}

// FinishWithState transitions the active run to an explicit terminal state.
func (r *ExecutionRuntime) FinishWithState(runID string, state RunState) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	if !isTerminalRunState(state) {
		return fmt.Errorf("execution terminal state is invalid: %s", state)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.activeLocked(runID) {
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.state = state
	r.cancel = nil
	r.ctx = nil
	r.running = nil
	r.finished = true
	return nil
}

func isTerminalRunState(state RunState) bool {
	switch state {
	case RunStateCompleted, RunStateFailed, RunStateCancelled, RunStateTimedOut:
		return true
	default:
		return false
	}
}

// State reports the current run state. It returns the last terminal state when
// idle after a run has finished, and an empty state for a zero-value runtime.
func (r *ExecutionRuntime) State() RunState {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Active reports the current run ID and whether a run is active.
func (r *ExecutionRuntime) Active() (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runID, r.cancel != nil && !r.finished
}
