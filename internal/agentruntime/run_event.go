package agentruntime

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// RunEvent is the front-end-neutral durable representation of a run event.
// Adapters may keep their protocol payload in Data, but persistence is owned by
// this runtime boundary.
type RunEvent struct {
	ID        string
	SessionID string
	RunID     string
	EventType string
	Source    string
	Status    string
	Model     string
	Mode      string
	Timestamp time.Time
	Data      json.RawMessage
}

// RunEventSink persists run lifecycle events without exposing the storage
// implementation to adapters.
type RunEventSink interface {
	Record(RunEvent) (string, error)
}

// SessionRunEventSink stores events in the existing session_run_events table.
// It intentionally reuses the existing session persistence API and schema.
type SessionRunEventSink struct {
	SessionDir string
}

func (s SessionRunEventSink) Record(ev RunEvent) (string, error) {
	if ev.SessionID == "" || ev.RunID == "" || ev.EventType == "" {
		return "", fmt.Errorf("run event requires session ID, run ID, and event type")
	}
	return session.SaveSessionRunEvent(s.SessionDir, session.SessionRunEvent{
		ID: ev.ID, SessionID: ev.SessionID, RunID: ev.RunID,
		EventType: ev.EventType, Source: ev.Source, Status: ev.Status,
		Model: ev.Model, Mode: ev.Mode, Timestamp: ev.Timestamp, Data: ev.Data,
	})
}

func (s SessionRunEventSink) RecordJSON(sessionID, runID, eventType, source, status, model, mode string, data any) (string, error) {
	var raw json.RawMessage
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return "", err
		}
		raw = encoded
	}
	return s.Record(RunEvent{SessionID: sessionID, RunID: runID, EventType: eventType, Source: source, Status: status, Model: model, Mode: mode, Timestamp: time.Now(), Data: raw})
}
