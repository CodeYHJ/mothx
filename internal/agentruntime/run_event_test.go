package agentruntime

import (
	"encoding/json"
	"testing"
)

type recordingRunEventSink struct{ events []RunEvent }

func (s *recordingRunEventSink) Record(ev RunEvent) (string, error) {
	s.events = append(s.events, ev)
	return "event-1", nil
}

func TestSessionRunEventSinkRecordJSON(t *testing.T) {
	sink := SessionRunEventSink{SessionDir: t.TempDir()}
	if _, err := sink.RecordJSON("session-1", "run-1", "started", "tui", "running", "model", "agent", map[string]string{"key": "value"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunEventCarriesProtocolNeutralData(t *testing.T) {
	data, err := json.Marshal(map[string]any{"decision": "approval-1"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &recordingRunEventSink{}
	if _, err := sink.Record(RunEvent{SessionID: "session-1", RunID: "run-1", EventType: "decision_pending", Data: data}); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 1 || string(sink.events[0].Data) != string(data) {
		t.Fatalf("events = %#v", sink.events)
	}
}
