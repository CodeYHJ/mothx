package agentruntime

import (
	"context"
	"fmt"
	"time"
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
	r.mu.Lock()
	runCopy := run
	r.durable = &runCopy
	r.durablePersisted = false
	r.startEvent = event
	r.mu.Unlock()
	if err := store.Create(run); err != nil {
		_ = r.FinishWithState(run.ID, RunStateFailed)
		return nil, fmt.Errorf("create durable run: %w", err)
	}
	r.mu.Lock()
	if r.activeLocked(run.ID) {
		r.durablePersisted = true
	}
	r.mu.Unlock()
	event.SessionID = run.SessionID
	event.RunID = run.ID
	if event.Status == "" {
		event.Status = string(RunStateRunning)
	}
	if _, err := r.RecordEvent(event); err != nil {
		message := "record run start event: " + err.Error()
		// The start event failure must not leave either an active in-memory run or
		// a running durable row. Try the canonical terminal path first; if the
		// same failing sink prevents that path from completing, force the
		// in-memory transition and finish the row as a best effort.
		if finishErr := r.FinishDurable(run.ID, RunStateFailed, message, RunEvent{
			SessionID: run.SessionID, RunID: run.ID, EventType: "failed",
			Source: run.Source, Status: string(RunStateFailed), Model: run.Model,
			Mode: run.Mode, Timestamp: time.Now(),
		}); finishErr != nil {
			_ = store.Finish(run.ID, RunStateFailed, message)
			r.transitionMu.Lock()
			_, _ = r.finishInMemory(run.ID, RunStateFailed, true)
			r.transitionMu.Unlock()
		}
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
	if r.runStore() == nil {
		return nil, fmt.Errorf("execution run store is not configured")
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
	r.durable = &DurableRun{ID: runID, Status: string(state)}
	r.durablePersisted = true
	r.mu.Unlock()
	return ctx, nil
}

// ReattachDurableRun restores an existing row with its full identity. The
// metadata is required so a later shutdown or FinishWithState can emit a
// valid terminal event without relying on adapter-local fallbacks.
func (r *ExecutionRuntime) ReattachDurableRun(parent context.Context, run DurableRun, state RunState, startEvent RunEvent) (context.Context, error) {
	if r == nil {
		return nil, fmt.Errorf("execution runtime is nil")
	}
	if run.ID == "" || run.SessionID == "" {
		return nil, fmt.Errorf("durable run ID and session ID are required")
	}
	if r.runStore() == nil {
		return nil, fmt.Errorf("execution run store is not configured")
	}
	if state == "" {
		state = RunStateRunning
	}
	if isTerminalRunState(state) {
		return nil, fmt.Errorf("cannot reattach terminal execution state: %s", state)
	}
	ctx, err := r.Begin(parent, run.ID)
	if err != nil {
		return nil, err
	}
	if run.Status == "" {
		run.Status = string(state)
	}
	if startEvent.SessionID == "" {
		startEvent.SessionID = run.SessionID
	}
	if startEvent.RunID == "" {
		startEvent.RunID = run.ID
	}
	if startEvent.Source == "" {
		startEvent.Source = run.Source
	}
	if startEvent.Model == "" {
		startEvent.Model = run.Model
	}
	if startEvent.Mode == "" {
		startEvent.Mode = run.Mode
	}
	r.mu.Lock()
	r.state = state
	r.durable = &run
	r.durablePersisted = true
	r.startEvent = startEvent
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
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	activeID, active := r.Active()
	if !active || activeID != runID {
		return fmt.Errorf("execution is not active: %s", runID)
	}
	store := r.runStore()
	if store == nil {
		return fmt.Errorf("execution run store is not configured")
	}
	r.mu.Lock()
	previous := r.state
	r.mu.Unlock()
	if err := store.Update(runID, state, message); err != nil {
		return fmt.Errorf("persist run update: %w", err)
	}
	r.mu.Lock()
	if r.activeLocked(runID) {
		r.state = state
	} else if r.state != state {
		r.mu.Unlock()
		return fmt.Errorf("execution ended while updating %s (previous state %s)", state, previous)
	}
	r.mu.Unlock()
	return nil
}

// CancelDurable requests cancellation and persists the canonical cancelling
// state. Final terminalization remains the responsibility of FinishDurable.
func (r *ExecutionRuntime) CancelDurable(message string) (bool, error) {
	store := r.runStore()
	if store == nil {
		return false, fmt.Errorf("execution run store is not configured")
	}
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	runID, active := r.Active()
	if !active {
		return false, nil
	}
	if err := store.Update(runID, RunStateCancelling, message); err != nil {
		return false, fmt.Errorf("persist run cancellation: %w", err)
	}
	if !r.Cancel() {
		return false, nil
	}
	return true, nil
}

// FinishDurable performs one canonical terminal transition, records its final
// event, and updates the durable run row. Persistence happens before releasing
// in-memory ownership so failed writes remain retryable and concurrent finishers
// cannot emit duplicate terminal events.
func (r *ExecutionRuntime) FinishDurable(runID string, state RunState, message string, event RunEvent) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	if !isTerminalRunState(state) {
		return fmt.Errorf("execution terminal state is invalid: %s", state)
	}
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	return r.finishDurableLocked(runID, state, message, event)
}

// finishDurableLocked is the single durable terminal transition owner. The
// caller must hold transitionMu; keeping the lock across event/store I/O
// prevents a competing adapter callback from selecting another terminal state.
func (r *ExecutionRuntime) finishDurableLocked(runID string, state RunState, message string, event RunEvent) error {
	r.mu.Lock()
	if !r.activeLocked(runID) {
		finished := r.finished && r.runID == runID
		terminalState, terminalErr := r.state, r.terminalErr
		r.mu.Unlock()
		if finished && terminalState == state && terminalErr == nil {
			return nil
		}
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.terminalEventSet && r.terminalState != state {
		selected := r.terminalState
		r.mu.Unlock()
		return fmt.Errorf("execution terminal state already selected: %s", selected)
	}
	r.mu.Unlock()

	// Normalize and persist the event before the row becomes terminal. The
	// event identity is retained across retries, so a transient row failure
	// does not append duplicate terminal events.
	event.RunID = runID
	if event.Status == "" {
		event.Status = string(state)
	}
	r.mu.Lock()
	if !r.terminalEventSet {
		durable := r.durable
		startEvent := r.startEvent
		if durable != nil {
			if event.SessionID == "" {
				event.SessionID = durable.SessionID
			}
			if event.Source == "" {
				event.Source = durable.Source
			}
			if event.Model == "" {
				event.Model = durable.Model
			}
			if event.Mode == "" {
				event.Mode = durable.Mode
			}
		}
		if event.SessionID == "" {
			event.SessionID = startEvent.SessionID
		}
		if event.Source == "" {
			event.Source = startEvent.Source
		}
		if event.Model == "" {
			event.Model = startEvent.Model
		}
		if event.Mode == "" {
			event.Mode = startEvent.Mode
		}
		r.terminalEvent = event
		r.terminalEventSet = true
		r.terminalState = state
		r.terminalMessage = message
	} else {
		event = r.terminalEvent
		if message != "" {
			r.terminalMessage = message
		}
	}
	r.terminalizing = true
	r.terminalErr = nil
	recorded := r.terminalEventRecorded
	r.mu.Unlock()

	if !recorded {
		if sink := r.eventSink(); sink != nil {
			id, err := sink.Record(event)
			if err != nil {
				r.finishTerminalAttempt(err)
				return fmt.Errorf("record run terminal event: %w", err)
			}
			r.mu.Lock()
			if r.terminalEvent.ID == "" {
				r.terminalEvent.ID = id
			}
			r.terminalEventRecorded = true
			r.mu.Unlock()
		} else {
			r.mu.Lock()
			r.terminalEventRecorded = true
			r.mu.Unlock()
		}
	}
	store := r.runStore()
	if store == nil {
		err := fmt.Errorf("execution run store is not configured")
		r.finishTerminalAttempt(err)
		return err
	}
	if err := store.Finish(runID, state, message); err != nil {
		r.finishTerminalAttempt(err)
		return fmt.Errorf("finish durable run: %w", err)
	}
	done, err := r.finishInMemory(runID, state, false)
	if err != nil {
		r.finishTerminalAttempt(err)
		return err
	}
	r.mu.Lock()
	r.terminalErr = nil
	r.terminalizing = false
	wait := r.terminalDone
	r.terminalDone = nil
	if wait != nil {
		close(wait)
	}
	r.mu.Unlock()
	r.closeDone(done)
	return nil
}

func (r *ExecutionRuntime) finishTerminalAttempt(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.terminalErr = err
	r.terminalizing = false
	wait := r.terminalDone
	r.terminalDone = nil
	if wait != nil {
		close(wait)
	}
	r.mu.Unlock()
}
