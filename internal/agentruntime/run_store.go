package agentruntime

import (
	"fmt"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// DurableRun is the adapter-neutral lifecycle row for one execution.
type DurableRun struct {
	ID        string
	SessionID string
	WorkDir   string
	Source    string
	Model     string
	Mode      string
	Status    string
	StartedAt time.Time
}

// DurableRunStore is the persistence boundary used by ExecutionRuntime to
// coordinate canonical run rows with in-memory lifecycle transitions.
type DurableRunStore interface {
	Create(DurableRun) error
	Update(string, RunState, string) error
	Finish(string, RunState, string) error
}

// RunStore persists the canonical run row alongside RunEvent records. It
// reuses the existing session_runs schema so startup recovery can discover runs
// from every adapter.
type RunStore struct {
	SessionDir string
}

func (s RunStore) Create(run DurableRun) error {
	if run.ID == "" || run.SessionID == "" {
		return fmt.Errorf("durable run ID and session ID are required")
	}
	if run.Status == "" {
		run.Status = string(RunStateRunning)
	}
	return session.SaveSessionRun(s.SessionDir, session.SessionRun{
		ID: run.ID, SessionID: run.SessionID, WorkDir: run.WorkDir,
		Source: run.Source, Model: run.Model, Mode: run.Mode, Status: run.Status,
		StartedAt: run.StartedAt, UpdatedAt: run.StartedAt,
	})
}

func (s RunStore) Update(runID string, state RunState, message string) error {
	if runID == "" || state == "" {
		return fmt.Errorf("durable run ID and state are required")
	}
	return session.UpdateSessionRunStatus(s.SessionDir, runID, durableRunStatus(state), message, nil)
}

func (s RunStore) Finish(runID string, state RunState, message string) error {
	if !isTerminalRunState(state) {
		return fmt.Errorf("durable run terminal state is invalid: %s", state)
	}
	finishedAt := time.Now()
	return session.UpdateSessionRunStatus(s.SessionDir, runID, durableRunStatus(state), message, &finishedAt)
}

func durableRunStatus(state RunState) string {
	if state == RunStateCancelled {
		return "cancelled"
	}
	return string(state)
}
