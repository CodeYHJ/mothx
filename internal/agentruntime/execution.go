package agentruntime

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RunState is the adapter-neutral lifecycle state of an active execution.
type RunState string

const (
	RunStateCreated         RunState = "created"
	RunStateQueued          RunState = "queued"
	RunStateRunning         RunState = "running"
	RunStateWaitingApproval RunState = "waiting_for_approval"
	RunStateWaitingQuestion RunState = "waiting_for_question"
	RunStateCancelling      RunState = "cancelling"
	RunStateCompleted       RunState = "completed"
	RunStateIncomplete      RunState = "incomplete"
	RunStateFailed          RunState = "failed"
	RunStateCancelled       RunState = "cancelled"
	RunStateTimedOut        RunState = "timed_out"
)

// ExecutionRuntime owns the adapter-neutral active run state for one session.
// When configured with a DurableRunStore and RunEventSink it also owns the
// canonical durable transitions; adapters provide those storage implementations
// plus protocol event projection and any compatibility admission lock.
type ExecutionRuntime struct {
	// transitionMu serializes lifecycle operations that touch durable state. The
	// state mutex remains intentionally small so cancellation and Wait can make
	// progress while a store or event sink is doing I/O.
	transitionMu sync.Mutex
	mu           sync.Mutex
	runID        string
	startedAt    time.Time
	ctx          context.Context
	cancel       context.CancelFunc
	running      interface{ Abort() }
	state        RunState
	finished     bool
	events       RunEventSink
	store        DurableRunStore
	done         chan struct{}
	durable      *DurableRun
	// durablePersisted is distinct from durable metadata: BeginDurable sets
	// the metadata before Create, but only a successful Create means terminal
	// transitions must go through the durable store.
	durablePersisted      bool
	startEvent            RunEvent
	terminalizing         bool
	terminalDone          chan struct{}
	terminalErr           error
	terminalState         RunState
	terminalMessage       string
	terminalEvent         RunEvent
	terminalErrorInfo     ErrorInfo
	terminalEventSet      bool
	terminalEventRecorded bool
	facts                 executionFacts
}

// Begin starts one exclusive execution. The caller must finish the run exactly
// once, including when agent construction fails.
func (r *ExecutionRuntime) Begin(parent context.Context, runID string) (context.Context, error) {
	if r == nil {
		return nil, fmt.Errorf("execution runtime is nil")
	}
	if runID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	if parent == nil {
		parent = context.Background()
	}

	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil && !r.finished {
		return nil, fmt.Errorf("execution already active: %s", r.runID)
	}
	ctx, cancel := context.WithCancel(parent)
	r.done = make(chan struct{})
	r.runID = runID
	r.startedAt = time.Now()
	r.ctx = ctx
	r.cancel = cancel
	r.running = nil
	r.finished = false
	r.state = RunStateRunning
	r.durable = nil
	r.durablePersisted = false
	r.startEvent = RunEvent{}
	r.terminalizing = false
	r.terminalDone = nil
	r.terminalErr = nil
	r.terminalState = ""
	r.terminalMessage = ""
	r.terminalEvent = RunEvent{}
	r.terminalErrorInfo = ErrorInfo{}
	r.terminalEventSet = false
	r.terminalEventRecorded = false
	r.facts = executionFacts{phase: PhaseModel, sideEffects: SideEffectNone}
	return ctx, nil
}

// WaitForApproval transitions an active run into an approval wait.
func (r *ExecutionRuntime) WaitForApproval(runID string) error {
	return r.waitFor(runID, RunStateWaitingApproval)
}

// WaitForQuestion transitions an active run into a question wait.
func (r *ExecutionRuntime) WaitForQuestion(runID string) error {
	return r.waitFor(runID, RunStateWaitingQuestion)
}

func (r *ExecutionRuntime) waitFor(runID string, state RunState) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	r.mu.Lock()
	if !r.activeLocked(runID) {
		r.mu.Unlock()
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.state != RunStateRunning {
		current := r.state
		r.mu.Unlock()
		return fmt.Errorf("execution %s is not running: %s", runID, current)
	}
	previous := r.state
	r.state = state
	r.mu.Unlock()
	return r.persistNonTerminalTransition(runID, previous, state, string(state), "")
}

// Resume returns a run from an approval or question wait to active execution.
func (r *ExecutionRuntime) Resume(runID string) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	r.mu.Lock()
	if !r.activeLocked(runID) {
		r.mu.Unlock()
		return fmt.Errorf("execution is not active: %s", runID)
	}
	if r.state != RunStateWaitingApproval && r.state != RunStateWaitingQuestion {
		current := r.state
		r.mu.Unlock()
		return fmt.Errorf("execution %s is not waiting: %s", runID, current)
	}
	previous := r.state
	r.state = RunStateRunning
	r.mu.Unlock()
	return r.persistNonTerminalTransition(runID, previous, RunStateRunning, "resumed", "")
}

// persistNonTerminalTransition keeps the durable row/event projection in
// lockstep with in-memory waiting/resume transitions. A storage failure rolls
// the state back while the run is still active so callers can retry safely.
func (r *ExecutionRuntime) persistNonTerminalTransition(runID string, previous, state RunState, eventType, message string) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.mu.Lock()
	store, sink := r.store, r.events
	durable := r.durable
	start := r.startEvent
	r.mu.Unlock()
	if durable == nil {
		return nil
	}
	if store != nil {
		if err := store.Update(runID, state, message); err != nil {
			r.mu.Lock()
			if r.activeLocked(runID) {
				r.state = previous
			}
			r.mu.Unlock()
			return fmt.Errorf("persist execution %s: %w", state, err)
		}
	}
	if sink != nil {
		event := RunEvent{RunID: runID, EventType: eventType, Status: string(state), Timestamp: time.Now()}
		if durable != nil {
			event.SessionID, event.Source, event.Model, event.Mode = durable.SessionID, durable.Source, durable.Model, durable.Mode
		}
		if event.SessionID == "" {
			event.SessionID = start.SessionID
		}
		if event.Source == "" {
			event.Source = start.Source
		}
		if event.Model == "" {
			event.Model = start.Model
		}
		if event.Mode == "" {
			event.Mode = start.Mode
		}
		if _, err := sink.Record(event); err != nil {
			var rollbackErr error
			if store != nil {
				rollbackErr = store.Update(runID, previous, "rollback after event persistence failure")
			}
			r.mu.Lock()
			if r.activeLocked(runID) {
				r.state = previous
			}
			r.mu.Unlock()
			if rollbackErr != nil {
				return fmt.Errorf("record execution %s event: %w (rollback failed: %v)", state, err, rollbackErr)
			}
			return fmt.Errorf("record execution %s event: %w", state, err)
		}
	}
	return nil
}

func (r *ExecutionRuntime) activeLocked(runID string) bool {
	return r.cancel != nil && !r.finished && (runID == "" || r.runID == runID)
}

// SetAgent associates the core agent so cancellation can unblock agent waits.
func (r *ExecutionRuntime) SetAgent(a interface{ Abort() }) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.activeLocked("") {
		r.mu.Unlock()
		return
	}
	r.running = a
	r.mu.Unlock()
}

// Cancel requests context cancellation and aborts the core agent if present.
func (r *ExecutionRuntime) Cancel() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	cancel := r.cancel
	a := r.running
	if cancel == nil || r.finished {
		r.mu.Unlock()
		return false
	}
	r.state = RunStateCancelling
	r.mu.Unlock()

	cancel()
	if a != nil {
		a.Abort()
	}
	return true
}

// SetRunStore attaches the canonical durable run store used by lifecycle
// helpers. Adapters should configure it once when assembling a session runtime.
func (r *ExecutionRuntime) SetRunStore(store DurableRunStore) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.store = store
	r.mu.Unlock()
}

// SetEventSink attaches the durable event sink used by adapter-neutral run
// lifecycle helpers. It does not emit an event by itself.
func (r *ExecutionRuntime) SetEventSink(sink RunEventSink) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = sink
	r.mu.Unlock()
}

// RecordEvent persists one adapter-neutral run event when a sink is attached.
func (r *ExecutionRuntime) runStore() DurableRunStore {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store
}

func (r *ExecutionRuntime) RecordEvent(ev RunEvent) (string, error) {
	if r == nil {
		return "", fmt.Errorf("execution runtime is nil")
	}
	r.mu.Lock()
	sink := r.events
	r.mu.Unlock()
	if sink == nil {
		return "", nil
	}
	return sink.Record(ev)
}

// Finish transitions the active run to completed. Callers that know the run
// failed, was cancelled, or timed out must use FinishWithState.
func (r *ExecutionRuntime) Finish(runID string) {
	_ = r.FinishWithState(runID, RunStateCompleted)
}

// FinishWithState transitions the active run to an explicit terminal state.
func (r *ExecutionRuntime) FinishWithState(runID string, state RunState) error {
	if r == nil {
		return fmt.Errorf("execution runtime is nil")
	}
	r.transitionMu.Lock()
	defer r.transitionMu.Unlock()
	r.mu.Lock()
	durable := r.activeLocked(runID) && r.durablePersisted && r.durable != nil
	var durableRun DurableRun
	var startEvent RunEvent
	if durable {
		durableRun = *r.durable
		startEvent = r.startEvent
	}
	r.mu.Unlock()
	if durable {
		return r.finishDurableLocked(runID, state, "", RunEvent{
			SessionID: durableRun.SessionID,
			RunID:     runID,
			EventType: "finished",
			Source:    durableRun.Source,
			Status:    string(state),
			Model:     durableRun.Model,
			Mode:      durableRun.Mode,
			Timestamp: time.Now(),
			Data:      startEvent.Data,
		})
	}
	_, err := r.finishInMemory(runID, state, true)
	return err
}

func (r *ExecutionRuntime) finishInMemory(runID string, state RunState, closeDone bool) (chan struct{}, error) {
	if r == nil {
		return nil, fmt.Errorf("execution runtime is nil")
	}
	if !isTerminalRunState(state) {
		return nil, fmt.Errorf("execution terminal state is invalid: %s", state)
	}
	r.mu.Lock()
	if !r.activeLocked(runID) {
		r.mu.Unlock()
		return nil, fmt.Errorf("execution is not active: %s", runID)
	}
	done := r.done
	if r.cancel != nil {
		r.cancel()
	}
	r.state = state
	r.cancel = nil
	r.ctx = nil
	r.running = nil
	r.finished = true
	if closeDone && done != nil {
		close(done)
		r.done = nil
	}
	r.mu.Unlock()
	return done, nil
}

func (r *ExecutionRuntime) closeDone(done chan struct{}) {
	if r == nil || done == nil {
		return
	}
	r.mu.Lock()
	if r.done == done {
		close(done)
		r.done = nil
	}
	r.mu.Unlock()
}

func isTerminalRunState(state RunState) bool {
	switch state {
	case RunStateCompleted, RunStateIncomplete, RunStateFailed, RunStateCancelled, RunStateTimedOut:
		return true
	default:
		return false
	}
}

// Shutdown requests cancellation and waits for the active execution to
// terminalize. If an Agent loop is bound, cancellation only requests
// termination; the owner of that loop must perform the terminal transition.
// Executions without a bound Agent are terminalized synchronously, which
// covers runs restored for process cleanup before their adapter loop exists.
func (r *ExecutionRuntime) Shutdown(message string) error {
	return r.ShutdownContext(context.Background(), message)
}

// ShutdownContext is the context-bounded form of Shutdown. It is the boundary
// SessionRuntime uses before releasing MCP and other shared resources.
func (r *ExecutionRuntime) ShutdownContext(ctx context.Context, message string) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runID, active := r.Active()
	if !active {
		return nil
	}
	r.mu.Lock()
	hasRunner := r.running != nil
	r.mu.Unlock()
	if !hasRunner {
		r.transitionMu.Lock()
		// Re-check after acquiring the lifecycle lock: a loop may have been
		// attached while the caller was taking the snapshot above.
		r.mu.Lock()
		if !r.activeLocked(runID) {
			r.mu.Unlock()
			return nil
		}
		if r.running != nil {
			r.mu.Unlock()
			r.transitionMu.Unlock()
			return r.shutdownLoopOwned(ctx, runID, message)
		}
		r.mu.Unlock()
		if err := r.persistShutdownTerminalLocked(runID, message); err != nil {
			r.transitionMu.Unlock()
			return err
		}
		done, err := r.finishInMemory(runID, RunStateCancelled, false)
		if err != nil {
			r.transitionMu.Unlock()
			return err
		}
		r.closeDone(done)
		r.transitionMu.Unlock()
		return nil
	}
	return r.shutdownLoopOwned(ctx, runID, message)
}

func (r *ExecutionRuntime) shutdownLoopOwned(ctx context.Context, runID, message string) error {
	if !r.Cancel() {
		if _, active := r.Active(); !active {
			return nil
		}
	}
	var updateErr error
	// A terminal durable transition owns the row while it is writing its
	// terminal event. Avoid waiting on that I/O here; cancellation and the
	// caller's context-bounded Wait must remain responsive.
	r.mu.Lock()
	terminalizing := r.terminalizing
	r.mu.Unlock()
	if terminalizing {
		return r.Wait(ctx)
	}
	if !r.transitionMu.TryLock() {
		return r.Wait(ctx)
	}
	r.mu.Lock()
	stillActive := r.activeLocked(runID)
	terminalizing = r.terminalizing
	r.mu.Unlock()
	if !stillActive {
		r.transitionMu.Unlock()
		return nil
	}
	if store := r.runStore(); store != nil {
		if !terminalizing {
			if err := store.Update(runID, RunStateCancelling, message); err != nil {
				updateErr = fmt.Errorf("persist run cancellation: %w", err)
			}
		}
	}
	r.transitionMu.Unlock()
	if err := r.Wait(ctx); err != nil {
		if updateErr != nil {
			return fmt.Errorf("%v; wait for execution shutdown: %w", updateErr, err)
		}
		return err
	}
	return updateErr
}

// persistShutdownTerminalLocked records the terminal event and durable row for
// a run that had no loop owner available to perform its normal FinishDurable
// transition. The caller must hold transitionMu. It intentionally leaves the
// in-memory run active when persistence fails so a later shutdown can retry.
func (r *ExecutionRuntime) persistShutdownTerminalLocked(runID, message string) error {
	r.mu.Lock()
	durable := r.durable
	startEvent := r.startEvent
	if durable == nil {
		r.mu.Unlock()
		return nil
	}
	durableRun := *durable
	facts := r.facts
	if r.terminalEventSet && r.terminalState != RunStateCancelled {
		state := r.terminalState
		r.mu.Unlock()
		return fmt.Errorf("execution terminal state already selected: %s", state)
	}
	terminalInfo := r.terminalErrorInfo
	if !r.terminalEventSet {
		event := RunEvent{
			SessionID: durableRun.SessionID,
			RunID:     runID,
			EventType: "finished",
			Source:    durableRun.Source,
			Status:    string(RunStateCancelled),
			Model:     durableRun.Model,
			Mode:      durableRun.Mode,
			Timestamp: time.Now(),
		}
		if event.SessionID == "" {
			event.SessionID = startEvent.SessionID
		}
		if event.Source == "" {
			event.Source = startEvent.Source
		}
		if event.Model == "" {
			event.Model = startEvent.Model
		}
		if event.Mode == "" {
			event.Mode = startEvent.Mode
		}
		terminalInfo = terminalErrorInfoFor(RunStateCancelled, message, facts, durableRun)
		message = terminalInfo.Message
		event.Data = withTerminalErrorInfo(withRunAttemptData(event.Data, durableRun), terminalInfo)
		r.terminalEvent = event
		r.terminalEventSet = true
		r.terminalState = RunStateCancelled
		r.terminalMessage = message
		r.terminalErrorInfo = terminalInfo
		r.facts.lastError = terminalInfo
	} else if terminalInfo.Code != "" {
		message = terminalInfo.Message
		r.terminalMessage = message
		r.terminalEvent.Data = withTerminalErrorInfo(r.terminalEvent.Data, terminalInfo)
	} else {
		terminalInfo = terminalErrorInfoFor(RunStateCancelled, message, facts, durableRun)
		message = terminalInfo.Message
		r.terminalMessage = message
		r.terminalErrorInfo = terminalInfo
		r.facts.lastError = terminalInfo
		r.terminalEvent.Data = withTerminalErrorInfo(r.terminalEvent.Data, terminalInfo)
	}
	event := r.terminalEvent
	r.terminalizing = true
	r.terminalErr = nil
	store := r.store
	recorded := r.terminalEventRecorded
	r.mu.Unlock()

	if !recorded {
		if sink := r.eventSink(); sink != nil {
			id, err := sink.Record(event)
			if err != nil {
				r.finishTerminalAttempt(fmt.Errorf("record shutdown terminal event: %w", err))
				return fmt.Errorf("record shutdown terminal event: %w", err)
			}
			r.mu.Lock()
			if r.terminalEvent.ID == "" {
				r.terminalEvent.ID = id
			}
			r.terminalEventRecorded = true
			r.mu.Unlock()
		} else {
			// A nil sink is a valid best-effort configuration. Mark it recorded so
			// a later retry cannot manufacture an event after the run is terminal.
			r.mu.Lock()
			r.terminalEventRecorded = true
			r.mu.Unlock()
		}
	}
	if terminalInfo.Code != "" {
		if err := r.persistErrorInfo(durableRun, terminalInfo); err != nil {
			r.finishTerminalAttempt(err)
			return fmt.Errorf("persist shutdown terminal error: %w", err)
		}
	}
	if err := r.clearRetryProgress(durableRun); err != nil {
		r.finishTerminalAttempt(err)
		return fmt.Errorf("clear shutdown retry progress: %w", err)
	}
	if store == nil {
		err := fmt.Errorf("execution run store is not configured")
		r.finishTerminalAttempt(err)
		return err
	}
	if err := store.Finish(runID, RunStateCancelled, message); err != nil {
		r.finishTerminalAttempt(fmt.Errorf("finish shutdown durable run: %w", err))
		return fmt.Errorf("finish shutdown durable run: %w", err)
	}
	r.mu.Lock()
	r.terminalizing = false
	r.terminalErr = nil
	r.mu.Unlock()
	return nil
}

func (r *ExecutionRuntime) eventSink() RunEventSink {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events
}

// Wait waits for the active execution to reach a terminal state.
func (r *ExecutionRuntime) Wait(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State reports the current run state. It returns the last terminal state when
// idle after a run has finished, and an empty state for a zero-value runtime.
func (r *ExecutionRuntime) State() RunState {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Active reports the current run ID and whether a run is active.
func (r *ExecutionRuntime) Active() (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runID, r.cancel != nil && !r.finished
}
