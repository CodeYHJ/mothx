package tui

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/session"
)

// tuiRun adapts the Bubble Tea stream to the shared execution lifecycle. It
// intentionally does not own rendering or event translation.
type tuiRun struct {
	execution  *agentruntime.ExecutionRuntime
	decisions  *agentruntime.DecisionService
	sessionID  string
	id         string
	sessionDir string
	workDir    string
	model      string
	mode       string
}

func (r *tuiRun) registerDecision(id string, kind agentruntime.DecisionKind) error {
	if r == nil || r.decisions == nil {
		return nil
	}
	if err := r.decisions.Register(agentruntime.DecisionRequest{
		ID: id, RunID: r.id, SessionID: r.sessionID, Kind: kind,
	}); err != nil {
		return err
	}
	if err := r.persistDecision(id, kind, "pending", "", nil); err != nil {
		return err
	}
	return nil
}

func (r *tuiRun) persistDecision(id string, kind agentruntime.DecisionKind, status, value string, payload any) error {
	if r == nil || r.sessionDir == "" || r.sessionID == "" || r.id == "" {
		return nil
	}
	request := agentruntime.DecisionRequest{ID: id, RunID: r.id, SessionID: r.sessionID, Kind: kind}
	resolution := agentruntime.DecisionResolution{ID: id, Kind: kind, Status: status, Value: value}
	record, err := agentruntime.NewDecisionResolutionRecord(request, resolution, payload)
	if status == "pending" {
		record, err = agentruntime.NewDecisionRequestRecord(request, payload)
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": payload})
	if err != nil {
		return err
	}
	_, err = r.execution.RecordEvent(agentruntime.RunEvent{
		SessionID: r.sessionID, RunID: r.id, EventType: "decision_" + status,
		Source: "tui", Status: status, Timestamp: time.Now(), Data: data,
	})
	return err
}
func (r *tuiRun) resolveDecision(id string, kind agentruntime.DecisionKind, value string) error {
	if r == nil || r.decisions == nil {
		return nil
	}
	_, err := r.decisions.ResolveWith(agentruntime.DecisionResolution{
		ID: id, Kind: kind, Status: "resolved", Value: value,
	}, func(_ agentruntime.DecisionRequest) error {
		return r.persistDecision(id, kind, "resolved", value, map[string]any{"value": value})
	})
	return err
}

func (r *tuiRun) clearDecisions(status string) {
	if r == nil || r.decisions == nil {
		return
	}
	for _, request := range r.decisions.ClearRunWithValue(r.id, "") {
		r.persistDecision(request.ID, request.Kind, status, "", map[string]any{"reason": "TUI run ended before the decision was resolved"})
	}
}
func (r *tuiRun) start(parent context.Context, a *agent.Agent, input string) <-chan agent.Event {
	if parent == nil {
		parent = context.Background()
	}
	if r == nil || r.execution == nil || a == nil {
		return nil
	}
	var ctx context.Context
	var err error
	if r.sessionID != "" {
		startedAt := time.Now()
		r.execution.SetRunStore(agentruntime.RunStore{SessionDir: r.sessionDir})
		ctx, err = r.execution.BeginDurable(parent, agentruntime.DurableRun{
			ID: r.id, SessionID: r.sessionID, WorkDir: r.workDir, Source: "tui",
			Model: r.model, Mode: r.mode, Status: "running", StartedAt: startedAt,
		}, agentruntime.RunEvent{SessionID: r.sessionID, RunID: r.id, EventType: "started", Source: "tui", Status: "running", Model: r.model, Mode: r.mode, Timestamp: startedAt})
	} else {
		ctx, err = r.execution.Begin(parent, r.id)
	}
	if err != nil {
		return nil
	}
	r.execution.SetAgent(a)
	return a.Run(ctx, input)
}

func (r *tuiRun) waitForQuestion() error {
	if r == nil || r.execution == nil {
		return nil
	}
	return r.execution.WaitForQuestion(r.id)
}

func (r *tuiRun) waitForApproval() error {
	if r == nil || r.execution == nil {
		return nil
	}
	return r.execution.WaitForApproval(r.id)
}

func (r *tuiRun) resume() error {
	if r == nil || r.execution == nil {
		return nil
	}
	return r.execution.Resume(r.id)
}

func (r *tuiRun) cancel() bool {
	return r != nil && r.execution != nil && r.execution.Cancel()
}

func (r *tuiRun) finish(state agentruntime.RunState) {
	if r == nil || r.execution == nil {
		return
	}
	if r != nil {
		r.clearDecisions("cancelled")
	}
	if r.sessionID != "" {
		_ = r.execution.FinishDurable(r.id, state, "", agentruntime.RunEvent{SessionID: r.sessionID, RunID: r.id, EventType: "finished", Source: "tui", Status: string(state), Model: r.model, Mode: r.mode, Timestamp: time.Now()})
	} else {
		_ = r.execution.FinishWithState(r.id, state)
	}
}

func recoverTUIOrphanedDecisions(sessionDir, sessionID string) error {
	if sessionDir == "" || sessionID == "" {
		return nil
	}
	run, err := session.GetActiveSessionRun(sessionDir, sessionID)
	if err != nil || run == nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(run.Source), "tui") {
		return nil
	}
	events, err := session.ListSessionRunEvents(sessionDir, sessionID)
	if err != nil {
		return err
	}
	records := make([]agentruntime.DecisionRecord, 0)
	for _, event := range events {
		if event.RunID != run.ID || !strings.HasPrefix(event.EventType, "decision_") {
			continue
		}
		var envelope struct {
			Decision agentruntime.DecisionRecord `json:"decision"`
		}
		if json.Unmarshal(event.Data, &envelope) == nil && envelope.Decision.ID != "" {
			records = append(records, envelope.Decision)
		}
	}
	now := time.Now()
	for _, record := range agentruntime.ExpiredDecisions(records, now) {
		if err := persistRecoveredTUIDecision(sessionDir, run, record, "timed_out"); err != nil {
			return err
		}
	}
	for _, record := range agentruntime.ReplayDecisionsAt(records, now) {
		if err := persistRecoveredTUIDecision(sessionDir, run, record, "cancelled"); err != nil {
			return err
		}
	}
	_, err = agentruntime.RecoverOrphanedRuns(sessionDir, func(candidate session.SessionRun) agentruntime.RecoveryAction {
		if candidate.ID == run.ID {
			return agentruntime.RecoveryFailLocal
		}
		return agentruntime.RecoveryKeepRemote
	}, nil)
	return err
}

func persistRecoveredTUIDecision(sessionDir string, run *session.SessionRun, pending agentruntime.DecisionRecord, status string) error {
	request := agentruntime.DecisionRequest{ID: pending.ID, SessionID: run.SessionID, RunID: run.ID, Kind: pending.Kind}
	resolution := agentruntime.DecisionResolution{ID: pending.ID, Kind: pending.Kind, Status: status}
	record, err := agentruntime.NewDecisionResolutionRecord(request, resolution, map[string]any{"reason": "TUI execution stack was not recoverable"})
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": map[string]any{"reason": "TUI execution stack was not recoverable"}})
	if err != nil {
		return err
	}
	_, err = (agentruntime.SessionRunEventSink{SessionDir: sessionDir}).Record(agentruntime.RunEvent{
		SessionID: run.SessionID, RunID: run.ID, EventType: "decision_" + status,
		Source: "tui", Status: status, Model: run.Model, Mode: run.Mode, Timestamp: time.Now(), Data: data,
	})
	return err
}

func newTUIRun(sessionIDs ...string) *tuiRun {
	sessionID, sessionDir := "", ""
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	if len(sessionIDs) > 1 {
		sessionDir = sessionIDs[1]
	}
	run := &tuiRun{
		execution:  &agentruntime.ExecutionRuntime{},
		decisions:  &agentruntime.DecisionService{},
		sessionID:  sessionID,
		id:         "tui_" + session.GenerateID(),
		sessionDir: sessionDir,
	}
	run.execution.SetEventSink(agentruntime.SessionRunEventSink{SessionDir: sessionDir})
	return run
}
