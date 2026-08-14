package agentruntime

import (
	"context"
	"fmt"
)

// BeginWithEvent starts a run and records its initial durable event through the
// configured sink. Event persistence remains best-effort for adapters that do
// not attach a sink; a configured sink error is returned to the caller.
func (r *ExecutionRuntime) BeginWithEvent(parent context.Context, runID string, event RunEvent) (context.Context, error) {
	if event.SessionID == "" {
		return nil, fmt.Errorf("run start event session ID is required")
	}
	ctx, err := r.Begin(parent, runID)
	if err != nil {
		return nil, err
	}
	event.RunID = runID
	if _, err := r.RecordEvent(event); err != nil {
		_ = r.FinishWithState(runID, RunStateFailed)
		return nil, fmt.Errorf("record run start event: %w", err)
	}
	return ctx, nil
}

// FinishWithEvent transitions a run to a terminal state and records its final
// durable event. The event is written only after the state transition succeeds.
func (r *ExecutionRuntime) FinishWithEvent(runID string, state RunState, event RunEvent) error {
	if event.SessionID == "" {
		return fmt.Errorf("run terminal event session ID is required")
	}
	if err := r.FinishWithState(runID, state); err != nil {
		return err
	}
	event.RunID = runID
	if _, err := r.RecordEvent(event); err != nil {
		return fmt.Errorf("record run terminal event: %w", err)
	}
	return nil
}
