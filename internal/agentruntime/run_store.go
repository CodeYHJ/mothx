package agentruntime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// DurableRun is the adapter-neutral lifecycle row for one execution.
type DurableRun struct {
	ID           string
	SessionID    string
	IntentID     string
	RetryOf      string
	Attempt      int
	WorkDir      string
	Source       string
	Model        string
	Mode         string
	Status       string
	StartedAt    time.Time
	FinishedAt   *time.Time
	Error        string
	ErrorInfo    ErrorInfo
	Progress     RetryInfo
	Usage        json.RawMessage
	ContextUsage json.RawMessage
}

// DurableRunStore is the persistence boundary used by ExecutionRuntime to
// coordinate canonical run rows with in-memory lifecycle transitions.
type DurableRunStore interface {
	Create(DurableRun) error
	Update(string, RunState, string) error
	Finish(string, RunState, string) error
}

// DurableRunMetadataStore is an optional extension implemented by stores that
// persist structured recovery state. Keeping it optional preserves test and
// embedded adapters that only need lifecycle rows.
type DurableRunMetadataStore interface {
	UpdateErrorInfo(string, ErrorInfo) error
	UpdateProgress(string, RetryInfo) error
}

// DurableRunUsageStore is an optional metadata extension for providers that
// expose token/context usage before terminalization. Keeping it separate from
// DurableRunMetadataStore preserves embedded stores that predate usage rows.
type DurableRunUsageStore interface {
	UpdateUsage(string, json.RawMessage, json.RawMessage) error
}

// DurableIntentStore is the Runtime-owned persistence boundary for an
// accepted original request. Adapters keep their request decoding private but
// must not create a second intent store or lifecycle chain.
type DurableIntentStore interface {
	CreateIntentAndRun(ExecutionIntent, DurableRun) error
	GetIntent(string) (*ExecutionIntent, error)
}

// DurableIntentEventStore extends intent admission with an atomic started
// event. Stores that implement it prevent a process loss between the Run row
// and its first replay anchor.
type DurableIntentEventStore interface {
	DurableIntentStore
	CreateIntentAndRunWithEvent(ExecutionIntent, DurableRun, RunEvent) (string, error)
}

// DurableRunEventStore atomically admits a linked Run and its initial event.
// It is separate from DurableIntentEventStore because retries reuse an
// immutable intent that already exists.
type DurableRunEventStore interface {
	CreateRunWithEvent(DurableRun, RunEvent) (string, error)
}

// ExecutionIntent is the durable, adapter-neutral accepted request. The
// request and policy snapshots remain opaque at this boundary; their owner
// rehydrates them only through the shared Runtime execution path.
type ExecutionIntent = session.ExecutionIntent

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
	return session.CreateSessionRun(s.SessionDir, session.SessionRun{
		ID: run.ID, SessionID: run.SessionID, IntentID: run.IntentID, RetryOf: run.RetryOf, Attempt: run.Attempt, WorkDir: run.WorkDir,
		Source: run.Source, Model: run.Model, Mode: run.Mode, Status: run.Status,
		StartedAt: run.StartedAt, UpdatedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Error: run.Error, ErrorInfo: marshalErrorInfo(run.ErrorInfo), Progress: marshalRetryInfo(run.Progress), Usage: run.Usage,
		ContextUsage: run.ContextUsage,
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

func (s RunStore) UpdateErrorInfo(runID string, info ErrorInfo) error {
	if runID == "" {
		return fmt.Errorf("durable run ID is required")
	}
	return session.UpdateSessionRunErrorInfo(s.SessionDir, runID, marshalErrorInfo(info))
}

func (s RunStore) UpdateProgress(runID string, progress RetryInfo) error {
	if runID == "" {
		return fmt.Errorf("durable run ID is required")
	}
	return session.UpdateSessionRunProgress(s.SessionDir, runID, marshalRetryInfo(progress))
}

func (s RunStore) UpdateUsage(runID string, usage, contextUsage json.RawMessage) error {
	if runID == "" {
		return fmt.Errorf("durable run ID is required")
	}
	return session.UpdateSessionRunUsage(s.SessionDir, runID, usage, contextUsage)
}

func (s RunStore) CreateIntentAndRun(intent ExecutionIntent, run DurableRun) error {
	if run.ID == "" || run.SessionID == "" {
		return fmt.Errorf("durable run ID and session ID are required")
	}
	if run.Status == "" {
		run.Status = string(RunStateRunning)
	}
	if run.IntentID == "" {
		run.IntentID = intent.ID
	}
	return session.CreateExecutionIntentAndSessionRun(s.SessionDir, intent, session.SessionRun{
		ID: run.ID, SessionID: run.SessionID, IntentID: run.IntentID, RetryOf: run.RetryOf, Attempt: run.Attempt,
		WorkDir: run.WorkDir, Source: run.Source, Model: run.Model, Mode: run.Mode, Status: run.Status,
		StartedAt: run.StartedAt, UpdatedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Error: run.Error, ErrorInfo: marshalErrorInfo(run.ErrorInfo), Progress: marshalRetryInfo(run.Progress), Usage: run.Usage, ContextUsage: run.ContextUsage,
	})
}

func (s RunStore) CreateIntentAndRunWithEvent(intent ExecutionIntent, run DurableRun, event RunEvent) (string, error) {
	if run.ID == "" || run.SessionID == "" {
		return "", fmt.Errorf("durable run ID and session ID are required")
	}
	if run.Status == "" {
		run.Status = string(RunStateRunning)
	}
	if run.IntentID == "" {
		run.IntentID = intent.ID
	}
	return session.CreateExecutionIntentAndSessionRunEvent(s.SessionDir, intent, session.SessionRun{
		ID: run.ID, SessionID: run.SessionID, IntentID: run.IntentID, RetryOf: run.RetryOf, Attempt: run.Attempt,
		WorkDir: run.WorkDir, Source: run.Source, Model: run.Model, Mode: run.Mode, Status: run.Status,
		StartedAt: run.StartedAt, UpdatedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Error: run.Error, ErrorInfo: marshalErrorInfo(run.ErrorInfo), Progress: marshalRetryInfo(run.Progress), Usage: run.Usage, ContextUsage: run.ContextUsage,
	}, sessionRunEventFromRuntime(event))
}

func (s RunStore) CreateRunWithEvent(run DurableRun, event RunEvent) (string, error) {
	if run.ID == "" || run.SessionID == "" {
		return "", fmt.Errorf("durable run ID and session ID are required")
	}
	if run.Status == "" {
		run.Status = string(RunStateRunning)
	}
	return session.CreateSessionRunAndEvent(s.SessionDir, session.SessionRun{
		ID: run.ID, SessionID: run.SessionID, IntentID: run.IntentID, RetryOf: run.RetryOf, Attempt: run.Attempt,
		WorkDir: run.WorkDir, Source: run.Source, Model: run.Model, Mode: run.Mode, Status: run.Status,
		StartedAt: run.StartedAt, UpdatedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Error: run.Error, ErrorInfo: marshalErrorInfo(run.ErrorInfo), Progress: marshalRetryInfo(run.Progress), Usage: run.Usage, ContextUsage: run.ContextUsage,
	}, sessionRunEventFromRuntime(event))
}

func sessionRunEventFromRuntime(event RunEvent) session.SessionRunEvent {
	return session.SessionRunEvent{
		ID: event.ID, SessionID: event.SessionID, RunID: event.RunID, EventType: event.EventType,
		Source: event.Source, Status: event.Status, Model: event.Model, Mode: event.Mode,
		Timestamp: event.Timestamp, Data: event.Data,
	}
}

func (s RunStore) GetIntent(intentID string) (*ExecutionIntent, error) {
	return session.GetExecutionIntent(s.SessionDir, intentID)
}

// Reopen explicitly reactivates a terminal run for provider recovery. It is
// intentionally separate from Update so ordinary lifecycle transitions remain
// monotonic.
func (s RunStore) Reopen(runID string, state RunState, message string) error {
	if state != RunStateCreated && state != RunStateQueued && state != RunStateRunning {
		return fmt.Errorf("durable run recovery state is invalid: %s", state)
	}
	return session.ReopenSessionRun(s.SessionDir, runID, durableRunStatus(state), message)
}

func durableRunStatus(state RunState) string {
	if state == RunStateCancelled {
		return "cancelled"
	}
	return string(state)
}

func marshalErrorInfo(info ErrorInfo) json.RawMessage {
	if info == (ErrorInfo{}) {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func marshalRetryInfo(info RetryInfo) json.RawMessage {
	if info == (RetryInfo{}) {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}
