package agentruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
)

// ExecutionRuntime owns the adapter-neutral active run state for one session.
// Adapters remain responsible for persistence, event encoding, and admission
// locks; this type owns the cancel/abort/terminal transition itself.
type ExecutionRuntime struct {
	mu        sync.Mutex
	runID     string
	startedAt time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	running   *agent.Agent
	finished  bool
}

// Begin starts one exclusive execution. The caller must call Finish exactly
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
	return ctx, nil
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
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	if a != nil {
		a.Abort()
	}
	return true
}

// Finish transitions the active run to terminal state and clears ownership.
func (r *ExecutionRuntime) Finish(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if runID != "" && r.runID != runID {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.cancel = nil
	r.ctx = nil
	r.running = nil
	r.finished = true
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
