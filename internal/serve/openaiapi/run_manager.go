package openaiapi

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/session"
)

type RunManager struct {
	mu         sync.RWMutex
	sessionDir string
	runs       map[string]*managedRun
	finalized  map[string]struct{}
}

type managedRun struct {
	id        string
	sessionID string
	cancel    context.CancelFunc
	subs      map[*runEventSubscription]struct{}
	hook      func(agent.Event)
	finalized sync.Once
}

type runEventSubscription struct {
	ch   chan agent.Event
	once sync.Once
}

func NewRunManager(sessionDir string) *RunManager {
	return &RunManager{sessionDir: sessionDir, runs: make(map[string]*managedRun), finalized: make(map[string]struct{})}
}

func (m *RunManager) Create(run session.SessionRun) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	if err := agentruntime.CreateDurableRun(m.sessionDir, agentruntime.DurableRun{
		ID: run.ID, SessionID: run.SessionID, WorkDir: run.WorkDir, Source: run.Source,
		Model: run.Model, Mode: run.Mode, Status: run.Status, StartedAt: run.StartedAt,
		FinishedAt: run.FinishedAt, Error: run.Error, Usage: run.Usage,
	}); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.finalized, run.ID)
	m.runs[run.ID] = &managedRun{id: run.ID, sessionID: run.SessionID, subs: make(map[*runEventSubscription]struct{})}
	m.mu.Unlock()
	return nil
}

// Register adds an in-memory run entry without persisting the canonical row.
// ExecutionRuntime owns durable row creation for migrated lifecycle paths.
func (m *RunManager) Register(run session.SessionRun) error {
	if m == nil || run.ID == "" || run.SessionID == "" {
		return fmt.Errorf("run ID and session ID are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.finalized, run.ID)
	m.runs[run.ID] = &managedRun{id: run.ID, sessionID: run.SessionID, subs: make(map[*runEventSubscription]struct{})}
	return nil
}
func (m *RunManager) Attach(runID, sessionID string, cancel context.CancelFunc) error {
	if m == nil || runID == "" || sessionID == "" {
		return fmt.Errorf("run ID and session ID are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		run = &managedRun{id: runID, sessionID: sessionID, subs: make(map[*runEventSubscription]struct{})}
		m.runs[runID] = run
	}
	run.cancel = cancel
	return nil
}

func (m *RunManager) closeSubscribersLocked(run *managedRun) {
	if run == nil {
		return
	}
	for sub := range run.subs {
		close(sub.ch)
	}
	run.subs = make(map[*runEventSubscription]struct{})
}
func (m *RunManager) Start(runID string, events <-chan agent.Event) error {
	if m == nil || runID == "" || events == nil {
		return fmt.Errorf("run ID and event stream are required")
	}
	m.mu.RLock()
	_, ok := m.runs[runID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("run %q is not active", runID)
	}
	go func() {
		for ev := range events {
			m.Publish(runID, ev)
		}
		m.mu.Lock()
		if run := m.runs[runID]; run != nil {
			m.closeSubscribersLocked(run)
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *RunManager) Subscribe(runID string) (<-chan agent.Event, func(), error) {
	if m == nil {
		return nil, func() {}, fmt.Errorf("run manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		return nil, func() {}, fmt.Errorf("run %q is not active", runID)
	}
	sub := &runEventSubscription{ch: make(chan agent.Event, 128)}
	if run.subs == nil {
		run.subs = make(map[*runEventSubscription]struct{})
	}
	run.subs[sub] = struct{}{}
	cancel := func() {
		sub.once.Do(func() {
			m.mu.Lock()
			if current := m.runs[runID]; current != nil {
				delete(current.subs, sub)
			}
			m.mu.Unlock()
		})
	}
	return sub.ch, cancel, nil
}

func (m *RunManager) SetHook(runID string, hook func(agent.Event)) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[runID]
	if run == nil {
		return fmt.Errorf("run %q is not active", runID)
	}
	run.hook = hook
	return nil
}

func (m *RunManager) Publish(runID string, ev agent.Event) {
	if m == nil {
		return
	}
	m.mu.RLock()
	run := m.runs[runID]
	var hook func(agent.Event)
	if run != nil {
		hook = run.hook
	}
	m.mu.RUnlock()
	if hook != nil {
		hook(ev)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if run := m.runs[runID]; run != nil {
		for sub := range run.subs {
			select {
			case sub.ch <- ev:
			default:
			}
		}
	}
}

func (m *RunManager) Cancel(runID string) bool {
	if m == nil {
		return false
	}
	// Check if the run exists in the database first.
	run, err := session.GetSessionRun(m.sessionDir, runID)
	if err != nil || run == nil {
		return false
	}
	// If the run is already in a terminal state, don't cancel.
	if run.Status == "completed" || run.Status == "incomplete" || run.Status == "expired" || run.Status == "failed" || run.Status == "cancelled" {
		return false
	}
	m.mu.RLock()
	mr := m.runs[runID]
	var cancel context.CancelFunc
	if mr != nil {
		cancel = mr.cancel
	}
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	// Even if no in-memory cancel func, update the DB status.
	// This handles the case where the run exists only in DB (e.g. after server restart).
	_ = agentruntime.UpdateDurableRun(m.sessionDir, runID, agentruntime.RunStateCancelling, "run cancellation requested")
	return true
}

func (m *RunManager) Finish(runID, status, message string) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	if err := agentruntime.FinishDurableRun(m.sessionDir, runID, runStateFromStatus(status), message); err != nil {
		return err
	}
	m.mu.Lock()
	if run := m.runs[runID]; run != nil {
		m.closeSubscribersLocked(run)
		delete(m.runs, runID)
	}
	m.mu.Unlock()
	return nil
}

// FinalizeOnce executes fn exactly once for the given run, using sync.Once.
// Returns true if fn was executed, false if it was already called for this run
// or if the run does not exist in the memory map.
func (m *RunManager) FinalizeOnce(runID string, fn func()) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	finalized := false
	if m.finalized != nil {
		_, finalized = m.finalized[runID]
	}
	if finalized {
		m.mu.RUnlock()
		return false
	}
	run := m.runs[runID]
	m.mu.RUnlock()
	// If the run is not in memory (e.g. it was created by a different process
	// or the map was cleaned), create a temporary entry for idempotency.
	if run == nil {
		run = &managedRun{id: runID, subs: make(map[*runEventSubscription]struct{})}
		m.mu.Lock()
		if existing := m.runs[runID]; existing != nil {
			run = existing
		} else {
			m.runs[runID] = run
		}
		m.mu.Unlock()
	}
	called := false
	run.finalized.Do(func() {
		m.mu.Lock()
		if m.finalized == nil {
			m.finalized = make(map[string]struct{})
		}
		m.finalized[runID] = struct{}{}
		m.mu.Unlock()
		called = true
		fn()
	})
	return called
}

// RecoverOrphanedRuns scans the database for runs that are still in a non-terminal
// state after a server restart and marks them as failed. This must be called once
// during server startup.
func (m *RunManager) RecoverOrphanedRuns() error {
	return m.RecoverOrphanedRunsExcept(nil)
}

// RecoverOrphanedRunsExcept marks orphaned local executions as failed unless
// skip identifies a run whose lifecycle is owned by another durable runtime.
func (m *RunManager) RecoverOrphanedRunsExcept(skip func(session.SessionRun) bool) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	_, err := agentruntime.RecoverOrphanedRuns(m.sessionDir, func(run session.SessionRun) agentruntime.RecoveryAction {
		if skip != nil && skip(run) {
			return agentruntime.RecoveryKeepRemote
		}
		return agentruntime.RecoveryFailLocal
	}, nil)
	return err
}

func (m *RunManager) Get(runID string) (*session.SessionRun, error) {
	if m == nil {
		return nil, fmt.Errorf("run manager is nil")
	}
	return session.GetSessionRun(m.sessionDir, runID)
}
func (m *RunManager) Active(sessionID string) (*session.SessionRun, error) {
	if m == nil {
		return nil, fmt.Errorf("run manager is nil")
	}
	return session.GetActiveSessionRun(m.sessionDir, sessionID)
}
func runStateFromStatus(status string) agentruntime.RunState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return agentruntime.RunStateCompleted
	case "incomplete":
		return agentruntime.RunStateIncomplete
	case "cancelled", "canceled":
		return agentruntime.RunStateCancelled
	case "timed_out", "timeout", "expired":
		return agentruntime.RunStateTimedOut
	default:
		return agentruntime.RunStateFailed
	}
}

// timePtr remains a compatibility helper for callers constructing legacy
// SessionRun values; canonical writes still go through RunStore.
func timePtr(value time.Time) *time.Time { return &value }

func (s *Server) GetRun(id string) (*session.SessionRun, error) {
	if s == nil || s.settings == nil || id == "" {
		return nil, ErrSessionNotFound
	}
	var (
		run *session.SessionRun
		err error
	)
	if s.runManager != nil {
		run, err = s.runManager.Get(id)
	} else {
		run, err = session.GetSessionRun(s.settings.GetSessionDir(), id)
	}
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, ErrSessionNotFound
	}
	return run, nil
}
func (s *Server) CancelRun(id string) error {
	if s == nil || s.runManager == nil || id == "" {
		return ErrSessionNotFound
	}
	run, err := s.runManager.Get(id)
	if err != nil {
		return err
	}
	if run == nil {
		return ErrSessionNotFound
	}
	if sess := s.pool.GetForWorkDir(run.WorkDir, run.SessionID); sess != nil && sess.isDurableRun(id) && sess.Execution != nil {
		cancelled, cancelErr := sess.Execution.CancelDurable("run cancellation requested")
		if cancelErr != nil {
			return cancelErr
		}
		if cancelled {
			return nil
		}
		return ErrSessionNotFound
	}
	if !s.runManager.Cancel(id) {
		return ErrSessionNotFound
	}
	return agentruntime.UpdateDurableRun(s.settings.GetSessionDir(), id, agentruntime.RunStateCancelling, "run cancellation requested")
}

// FinalizeRun is the unified, idempotent finalizer for any run exit path.
// It must be called exactly once per run: from the handler defer, from stop/cancel,
// or from the RunExecutor completion path.
//
// It performs:
//  1. Mark run as terminalizing in APISession
//  2. Clear pending approvals for this run
//  3. Finish run in APISession (release in-memory state)
//  4. Update RunManager persistent state
//  5. Publish final runtime snapshot
//  6. Publish stream done event
func (s *Server) FinalizeRun(sess *APISession, runID, status, errMsg string) {
	if s == nil || sess == nil || runID == "" {
		return
	}
	// Use sync.Once to ensure the finalization logic runs at most once per run.
	if s.runManager != nil {
		s.runManager.FinalizeOnce(runID, func() {
			s.finalizeRunInternal(sess, runID, status, errMsg)
		})
	} else {
		s.finalizeRunInternal(sess, runID, status, errMsg)
	}
}

func (s *Server) finalizeRunInternal(sess *APISession, runID, status, errMsg string) {
	// 1. Mark terminalizing
	sess.markRunTerminalizing(runID)
	// 2. Clear pending approvals
	s.clearSessionApprovalsForRun(sess, runID, "cancelled", "run ended before the approval was resolved")
	// 3. Release in-memory run state
	sess.finishRun(runID)
	// 4. Persist terminal state
	if s.runManager != nil && !sess.isDurableRun(runID) {
		_ = s.runManager.Finish(runID, status, errMsg)
	}
	if sess.isDurableRun(runID) {
		sess.clearDurableRun(runID)
	}
	// 5. Publish final runtime snapshot
	s.publishSessionRuntime(sess)
	// 6. Publish stream done
	s.publishSessionStreamDone(sess.ID, runID, status)
	// Notify integrations only after the run and its terminal event are persisted.
	s.mu.RLock()
	observer := s.runComplete
	s.mu.RUnlock()
	if observer != nil {
		observer(sess.ID, runID, status, errMsg)
	}
}
