package openaiapi

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/session"
)

type RunManager struct {
	mu         sync.RWMutex
	sessionDir string
	runs       map[string]*managedRun
}

type managedRun struct {
	id        string
	sessionID string
	cancel    context.CancelFunc
	subs      map[*runEventSubscription]struct{}
	hook      func(agent.Event)
}

type runEventSubscription struct {
	ch   chan agent.Event
	once sync.Once
}

func NewRunManager(sessionDir string) *RunManager {
	return &RunManager{sessionDir: sessionDir, runs: make(map[string]*managedRun)}
}

func (m *RunManager) Create(run session.SessionRun) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	if err := session.SaveSessionRun(m.sessionDir, run); err != nil {
		return err
	}
	m.mu.Lock()
	m.runs[run.ID] = &managedRun{id: run.ID, sessionID: run.SessionID, subs: make(map[*runEventSubscription]struct{})}
	m.mu.Unlock()
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
	m.mu.RLock()
	run := m.runs[runID]
	var cancel context.CancelFunc
	if run != nil {
		cancel = run.cancel
	}
	m.mu.RUnlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (m *RunManager) Finish(runID, status, message string) error {
	if m == nil {
		return fmt.Errorf("run manager is nil")
	}
	if err := session.UpdateSessionRunStatus(m.sessionDir, runID, status, message, timePtr(time.Now())); err != nil {
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
func timePtr(value time.Time) *time.Time { return &value }

func (s *Server) GetRun(id string) (*session.SessionRun, error) {
	if s == nil || s.runManager == nil || id == "" {
		return nil, ErrSessionNotFound
	}
	run, err := s.runManager.Get(id)
	if err != nil || run == nil {
		return nil, ErrSessionNotFound
	}
	return run, nil
}
func (s *Server) CancelRun(id string) error {
	if s == nil || s.runManager == nil || id == "" {
		return ErrSessionNotFound
	}
	run, err := s.runManager.Get(id)
	if err != nil || run == nil {
		return ErrSessionNotFound
	}
	if !s.runManager.Cancel(id) {
		return ErrSessionNotFound
	}
	return session.UpdateSessionRunStatus(s.settings.GetSessionDir(), id, "cancelling", "run cancellation requested", nil)
}
