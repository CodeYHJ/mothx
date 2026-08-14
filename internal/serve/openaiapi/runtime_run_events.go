package openaiapi

import (
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/session"
)

// runtimeRunEventSink persists canonical events and mirrors them to WebUI
// projections. Persistence remains Runtime-owned while publication remains an
// adapter concern.
func (s *Server) runtimeRunEventSink(sess *APISession) agentruntime.RunEventSink {
	return agentruntime.RunEventSinkFunc(func(ev agentruntime.RunEvent) (string, error) {
		id, err := (agentruntime.SessionRunEventSink{SessionDir: s.settings.GetSessionDir()}).Record(ev)
		if err != nil {
			return "", err
		}
		entry := sessionRunEventToEntry(session.SessionRunEvent{
			ID: id, SessionID: ev.SessionID, RunID: ev.RunID, EventType: ev.EventType,
			Source: ev.Source, Status: ev.Status, Model: ev.Model, Mode: ev.Mode,
			Timestamp: ev.Timestamp, Data: ev.Data,
		}, 0)
		s.publishSessionStreamEvent(sess.ID, "run_event", entry)
		s.getEventBroker().PublishRunEvent(sess.ID, ev.RunID, entry)
		return id, nil
	})
}
