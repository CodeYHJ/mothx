package agentruntime

import (
	"context"
	"testing"
)

func TestExecutionRuntimeBeginAndFinishWithEvents(t *testing.T) {
	sink := &recordingRunEventSink{}
	var runtime ExecutionRuntime
	runtime.SetEventSink(sink)
	if _, err := runtime.BeginWithEvent(context.Background(), "run-1", RunEvent{SessionID: "session-1", EventType: "started", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.FinishWithEvent("run-1", RunStateCompleted, RunEvent{SessionID: "session-1", EventType: "finished", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].EventType != "started" || sink.events[1].EventType != "finished" {
		t.Fatalf("events = %#v", sink.events)
	}
	if sink.events[0].RunID != "run-1" || sink.events[1].RunID != "run-1" {
		t.Fatalf("run IDs = %#v", sink.events)
	}
}
