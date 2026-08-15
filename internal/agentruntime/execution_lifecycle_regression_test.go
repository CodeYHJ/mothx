package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// retryRunEventSink lets lifecycle tests distinguish a failed persistence
// attempt from a successful retry without relying on a database.
type retryRunEventSink struct {
	mu     sync.Mutex
	fail   bool
	events []RunEvent
}

func (s *retryRunEventSink) Record(event RunEvent) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		s.fail = false
		return "", errors.New("event sink unavailable")
	}
	s.events = append(s.events, event)
	return "event-retry", nil
}

func (s *retryRunEventSink) Events() []RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunEvent(nil), s.events...)
}

type retryDurableRunStore struct {
	mu       sync.Mutex
	created  []DurableRun
	updates  []RunState
	finishes []RunState
	failNext bool
}

func (s *retryDurableRunStore) Create(run DurableRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, run)
	return nil
}

func (s *retryDurableRunStore) Update(_ string, state RunState, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates = append(s.updates, state)
	return nil
}

func (s *retryDurableRunStore) Finish(_ string, state RunState, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishes = append(s.finishes, state)
	if s.failNext {
		s.failNext = false
		return errors.New("run store unavailable")
	}
	return nil
}

func (s *retryDurableRunStore) FinishStates() []RunState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RunState(nil), s.finishes...)
}

func TestExecutionRuntimeFinishDurableRetriesEventFailure(t *testing.T) {
	store := &retryDurableRunStore{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-event-retry", SessionID: "session-1"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	sink := &retryRunEventSink{fail: true}
	runtime.SetEventSink(sink)

	terminal := RunEvent{SessionID: "session-1", EventType: "finished"}
	if err := runtime.FinishDurable("run-event-retry", RunStateCompleted, "", terminal); err == nil {
		t.Fatal("first FinishDurable unexpectedly succeeded")
	}
	if _, active := runtime.Active(); !active {
		t.Fatal("event persistence failure released the active run")
	}
	if err := runtime.FinishDurable("run-event-retry", RunStateCompleted, "", terminal); err != nil {
		t.Fatalf("retry FinishDurable: %v", err)
	}
	if _, active := runtime.Active(); active {
		t.Fatal("successful retry left the run active")
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("terminal events = %d, want one successful event", got)
	}
	if got := store.FinishStates(); len(got) != 1 || got[0] != RunStateCompleted {
		t.Fatalf("store finishes = %#v, want one completed finish", got)
	}
}

func TestExecutionRuntimeFinishDurableRetriesStoreFailureWithoutDuplicateEvent(t *testing.T) {
	store := &retryDurableRunStore{failNext: true}
	sink := &retryRunEventSink{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-store-retry", SessionID: "session-1"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	runtime.SetEventSink(sink)
	terminal := RunEvent{SessionID: "session-1", EventType: "finished"}
	if err := runtime.FinishDurable("run-store-retry", RunStateFailed, "first attempt", terminal); err == nil {
		t.Fatal("first FinishDurable unexpectedly succeeded")
	}
	if err := runtime.FinishDurable("run-store-retry", RunStateFailed, "retry", terminal); err != nil {
		t.Fatalf("retry FinishDurable: %v", err)
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("terminal events = %d, want one event across retries", got)
	}
	if got := store.FinishStates(); len(got) != 2 || got[0] != RunStateFailed || got[1] != RunStateFailed {
		t.Fatalf("store finishes = %#v, want failed retry pair", got)
	}
}

func TestExecutionRuntimeShutdownRetriesStoreFailureWithoutDuplicateEvent(t *testing.T) {
	store := &retryDurableRunStore{failNext: true}
	sink := &retryRunEventSink{}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-shutdown-retry", SessionID: "session-1"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	runtime.SetEventSink(sink)
	if err := runtime.Shutdown("first attempt"); err == nil {
		t.Fatal("first Shutdown unexpectedly succeeded")
	}
	if _, active := runtime.Active(); !active {
		t.Fatal("failed shutdown released the active run")
	}
	if err := runtime.Shutdown("retry"); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	if _, active := runtime.Active(); active {
		t.Fatal("successful shutdown retry left the run active")
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("shutdown terminal events = %d, want one event", got)
	}
}

type blockingRunEventSink struct {
	entered chan struct{}
	release chan struct{}
	events  []RunEvent
}

func (s *blockingRunEventSink) Record(event RunEvent) (string, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	s.events = append(s.events, event)
	return "event-blocking", nil
}

func TestExecutionRuntimeSerializesConcurrentTerminalTransitions(t *testing.T) {
	store := &retryDurableRunStore{}
	sink := &blockingRunEventSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
	var runtime ExecutionRuntime
	runtime.SetRunStore(store)
	if _, err := runtime.BeginDurable(context.Background(), DurableRun{ID: "run-terminal-race", SessionID: "session-1"}, RunEvent{EventType: "started"}); err != nil {
		t.Fatal(err)
	}
	runtime.SetEventSink(sink)

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runtime.FinishDurable("run-terminal-race", RunStateCompleted, "", RunEvent{SessionID: "session-1", EventType: "finished"})
	}()
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("terminal event sink was not entered")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runtime.FinishWithState("run-terminal-race", RunStateFailed)
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("conflicting terminal transition completed while first was blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(sink.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first terminal transition: %v", err)
	}
	if err := <-secondDone; err == nil {
		t.Fatal("conflicting terminal transition unexpectedly succeeded")
	}
	if got := runtime.State(); got != RunStateCompleted {
		t.Fatalf("state = %q, want completed", got)
	}
}

type lifecycleAborter struct {
	once    sync.Once
	aborted chan struct{}
}

func (a *lifecycleAborter) Abort() {
	a.once.Do(func() { close(a.aborted) })
}

func TestExecutionRuntimeShutdownAndWaitCompleteTogether(t *testing.T) {
	var runtime ExecutionRuntime
	ctx, err := runtime.Begin(context.Background(), "run-shutdown-wait-race")
	if err != nil {
		t.Fatal(err)
	}
	aborter := &lifecycleAborter{aborted: make(chan struct{})}
	runtime.SetAgent(aborter)
	loopDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		_ = runtime.FinishWithState("run-shutdown-wait-race", RunStateCancelled)
		close(loopDone)
	}()

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- runtime.Shutdown("concurrent shutdown") }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- runtime.Wait(context.Background()) }()
	select {
	case <-aborter.aborted:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not abort the bound agent")
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-waitDone; err != nil {
		t.Fatalf("Wait: %v", err)
	}
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("agent loop did not observe terminal transition")
	}
}
