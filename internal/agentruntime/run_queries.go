package agentruntime

import (
	"context"

	"github.com/startvibecoding/mothx/internal/session"
)

// GetDurableRun loads one canonical Run row for inspection by an adapter.
// Durable lifecycle writes remain owned by ExecutionRuntime/RunStore; this
// read boundary keeps adapters from treating session storage as their own
// execution state store.
func GetDurableRun(ctx context.Context, sessionDir, runID string) (*session.SessionRun, error) {
	return session.GetSessionRunContext(ctx, sessionDir, runID)
}

// GetActiveDurableRun loads the canonical non-terminal Run for a Session.
// Callers that need ownership, submit, or cancellation decisions must use
// InspectSessionExecution instead, since an active row alone does not prove a
// local execution owner.
func GetActiveDurableRun(ctx context.Context, sessionDir, sessionID string) (*session.SessionRun, error) {
	return session.GetActiveSessionRunContext(ctx, sessionDir, sessionID)
}
