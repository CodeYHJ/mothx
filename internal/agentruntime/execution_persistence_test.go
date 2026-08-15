package agentruntime

import (
	"context"
	"errors"
	"testing"
)

type recordingDurableRunStore struct {
	created  []DurableRun
	finished []struct {
		id      string
		state   RunState
		message string
	}
	createErr error
	updateErr error
	finishErr error
}

func (s *recordingDurableRunStore) Create(run DurableRun) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.created = append(s.created, run)
	return nil
}

func (s *recordingDurableRunStore) Update(id string, state RunState, message string) error {
	s.finished = append(s.finished, struct {
		id      string
		state   RunState
		message string
	}{id: id, state: state, message: message})
	return s.updateErr
}

func (s *recordingDurableRunStore) Finish(id string, state RunState, message string) error {
	s.finished = append(s.finished, struct {
		id      string
		state   RunState
		message string
	}{id: id, state: state, message: message})
	return s.finishErr
}

func TestExecutionRuntimeDurableLifecycle(t *testing.T) {
	store := &recordingDurableRunStore{}
	sink := &recordingRunEventSink{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	runtime.SetEventSink(sink)
	run := DurableRun{ID: "run-1", SessionID: "session-1", Status: "running"}
	if _, err := runtime.BeginDurable(context.Background(), run, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.FinishDurable(run.ID, RunStateCompleted, "", RunEvent{SessionID: run.SessionID, EventType: "finished"}); err != nil {
		t.Fatal(err)
	}
	if len(store.created) != 1 || len(store.finished) != 1 || store.finished[0].state != RunStateCompleted {
		t.Fatalf("store lifecycle = %#v %#v", store.created, store.finished)
	}
	if len(sink.events) != 2 || sink.events[0].RunID != run.ID || sink.events[1].Status != string(RunStateCompleted) {
		t.Fatalf("events = %#v", sink.events)
	}
}

func TestExecutionRuntimeUpdateDurablePersistsRunning(t *testing.T) {
	store := &recordingDurableRunStore{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-1", SessionID: "session-1", Status: "queued"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.UpdateDurable("run-1", RunStateRunning, "remote started"); err != nil {
		t.Fatal(err)
	}
	if runtime.State() != RunStateRunning || len(store.finished) != 1 || store.finished[0].state != RunStateRunning {
		t.Fatalf("runtime/store state = %q %#v", runtime.State(), store.finished)
	}
}

func TestExecutionRuntimeUpdateDurableRejectsTerminalState(t *testing.T) {
	var runtime ExecutionRuntime
	if err := runtime.UpdateDurable("run-1", RunStateCompleted, ""); err == nil {
		t.Fatal("terminal UpdateDurable unexpectedly succeeded")
	}
}

func TestExecutionRuntimeCancelDurablePersistsCancelling(t *testing.T) {
	store := &recordingDurableRunStore{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-1", SessionID: "session-1"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	cancelled, err := runtime.CancelDurable("requested")
	if err != nil || !cancelled {
		t.Fatalf("CancelDurable = %v, %v", cancelled, err)
	}
	if len(store.finished) != 1 || store.finished[0].state != RunStateCancelling {
		t.Fatalf("updates = %#v", store.finished)
	}
}

func TestExecutionRuntimeDurableBeginCompensatesCreateFailure(t *testing.T) {
	store := &recordingDurableRunStore{createErr: errors.New("write failed")}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-1", SessionID: "session-1"}, RunEvent{EventType: "started"}); err == nil {
		t.Fatal("BeginDurable succeeded")
	}
	if _, active := runtime.Active(); active {
		t.Fatal("runtime remained active after create failure")
	}
	if runtime.State() != RunStateFailed {
		t.Fatalf("state = %q, want failed", runtime.State())
	}
}

func TestExecutionRuntimeShutdownPersistsTerminalEventAndIsIdempotent(t *testing.T) {
	store := &recordingDurableRunStore{}
	sink := &recordingRunEventSink{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	runtime.SetEventSink(sink)
	run := DurableRun{
		ID: "run-shutdown", SessionID: "session-shutdown", Source: "webui",
		Model: "model-shutdown", Mode: "agent", Status: "running",
	}
	if _, err := runtime.BeginDurable(context.Background(), run, RunEvent{
		SessionID: run.SessionID, EventType: "started", Source: run.Source,
		Model: run.Model, Mode: run.Mode, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown("process stopped"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Shutdown("process stopped again"); err != nil {
		t.Fatal(err)
	}
	if runtime.State() != RunStateCancelled {
		t.Fatalf("state = %q, want cancelled", runtime.State())
	}
	if len(store.finished) != 1 || store.finished[0].state != RunStateCancelled {
		t.Fatalf("durable finishes = %#v", store.finished)
	}
	if len(sink.events) != 2 || sink.events[1].EventType != "finished" || sink.events[1].Status != string(RunStateCancelled) {
		t.Fatalf("events = %#v", sink.events)
	}
}
