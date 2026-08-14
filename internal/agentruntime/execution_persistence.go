package agentruntime

import (
	"context"
	"fmt"
)

// BeginDurable starts an exclusive in-memory execution, creates its canonical
// durable run row, and records the initial event. Failures are compensated so a
// partially-started run is not left active or persisted as running.
func (r *ExecutionRuntime) BeginDurable(parent context.Context, run DurableRun, event RunEvent) (context.Context, error) {
	if run.ID == "" || run.SessionID == "" {
		return nil, fmt.Errorf("durable run ID and session ID are required")
	}
	store := r.runStore()
	if store == nil {
		return nil, fmt.Errorf("execution run store is not configured")
	}
	ctx, err := r.Begin(parent, run.ID)
	if err != nil {
		return nil, err
	}
	if err := store.Create(run); err != nil {
		_ = r.FinishWithState(run.ID, RunStateFailed)
		return nil, fmt.Errorf("create durable run: %w", err)
	}
	event.SessionID = run.SessionID
	event.RunID = run.ID
	if event.Status == "" {
		event.Status = string(RunStateRunning)
	}
	if _, err := r.RecordEvent(event); err != nil {
		_ = r.FinishWithState(run.ID, RunStateFailed)
		_ = store.Finish(run.ID, RunStateFailed, "record run start event: "+err.Error())
		return nil, fmt.Errorf("record run start event: %w", err)
	}
	return ctx, nil
}

// ReattachDurable restores in-memory ownership of an already persisted,
// non-terminal Run without creating a duplicate canonical row.
func (r *ExecutionRuntime) ReattachDurable(parent context.Context, runID string, state RunState) (context.Context, error) {
	if r == nil {
		return nil, fmt.Errorf("execution runtime is nil")
	}
	if runID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	if state == "" {
		state = RunStateRunning
	}
	if isTerminalRunState(state) {
		return nil, fmt.Errorf("cannot reattach terminal execution state: %s", state)
	}
	ctx, err := r.Begin(parent, runID)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
	return ctx, nil
}

// UpdateDurable persists a non-terminal state for the active canonical run.
func (r *ExecutionRuntime) UpdateDurable(runID string, state RunState, message string) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	if isTerminalRunState(state) || state == "" {
		return fmt.Errorf("execution non-terminal state is invalid: %s", state)
	}
	activeID, active := r.Active()
	if !active || activeID != runID {
		return fmt.Errorf("execution is not active: %s", runID)
	}
	store := r.runStore()
	if store == nil {
		return fmt.Errorf("execution run store is not configured")
	}
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
	return store.Update(runID, state, message)
}

// CancelDurable requests cancellation and persists the canonical cancelling
// state. Final terminalization remains the responsibility of FinishDurable.
func (r *ExecutionRuntime) CancelDurable(message string) (bool, error) {
	store := r.runStore()
	if store == nil {
		return false, fmt.Errorf("execution run store is not configured")
	}
	runID, active := r.Active()
	if !active || !r.Cancel() {
		return false, nil
	}
	if err := store.Update(runID, RunStateCancelling, message); err != nil {
		return true, fmt.Errorf("persist run cancellation: %w", err)
	}
	return true, nil
}

// FinishDurable performs one canonical terminal transition, records its final
// event, and updates the durable run row. The in-memory transition happens
// first so cancellation and admission are released even if persistence fails.
func (r *ExecutionRuntime) FinishDurable(runID string, state RunState, message string, event RunEvent) error {
	store := r.runStore()
	if store == nil {
		return fmt.Errorf("execution run store is not configured")
	}
	if err := r.FinishWithState(runID, state); err != nil {
		return err
	}
	event.RunID = runID
	if event.Status == "" {
		event.Status = string(state)
	}
	var eventErr error
	if _, err := r.RecordEvent(event); err != nil {
		eventErr = fmt.Errorf("record run terminal event: %w", err)
	}
	if err := store.Finish(runID, state, message); err != nil {
		if eventErr != nil {
			return fmt.Errorf("%v; finish durable run: %w", eventErr, err)
		}
		return fmt.Errorf("finish durable run: %w", err)
	}
	return eventErr
}
