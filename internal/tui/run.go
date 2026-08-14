package tui

import (
	"context"
	"encoding/json"
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
	r.persistDecision(id, kind, "pending", "", nil)
	return nil
}

func (r *tuiRun) persistDecision(id string, kind agentruntime.DecisionKind, status, value string, payload any) {
	if r == nil || r.sessionDir == "" || r.sessionID == "" || r.id == "" {
		return
	}
	request := agentruntime.DecisionRequest{ID: id, RunID: r.id, SessionID: r.sessionID, Kind: kind}
	resolution := agentruntime.DecisionResolution{ID: id, Kind: kind, Status: status, Value: value}
	record, err := agentruntime.NewDecisionResolutionRecord(request, resolution, payload)
	if status == "pending" {
		record, err = agentruntime.NewDecisionRequestRecord(request, payload)
	}
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": payload})
	if err != nil {
		return
	}
	_, err = r.execution.RecordEvent(agentruntime.RunEvent{
		SessionID: r.sessionID, RunID: r.id, EventType: "decision_" + status,
		Source: "tui", Status: status, Timestamp: time.Now(), Data: data,
	})
	if err != nil {
		return
	}
}
func (r *tuiRun) resolveDecision(id string, kind agentruntime.DecisionKind, value string) error {
	if r == nil || r.decisions == nil {
		return nil
	}
	_, err := r.decisions.Resolve(agentruntime.DecisionResolution{
		ID: id, Kind: kind, Status: "resolved", Value: value,
	})
	if err == nil {
		r.persistDecision(id, kind, "resolved", value, map[string]any{"value": value})
	}
	return err
}

func (r *tuiRun) clearDecisions() {
	if r != nil && r.decisions != nil {
		r.decisions.ClearRun(r.id)
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
		if err = (agentruntime.RunStore{SessionDir: r.sessionDir}).Create(agentruntime.DurableRun{
			ID: r.id, SessionID: r.sessionID, WorkDir: r.workDir, Source: "tui",
			Model: r.model, Mode: r.mode, Status: "running", StartedAt: startedAt,
		}); err != nil {
			return nil
		}
		ctx, err = r.execution.BeginWithEvent(parent, r.id, agentruntime.RunEvent{SessionID: r.sessionID, RunID: r.id, EventType: "started", Source: "tui", Status: "running", Model: r.model, Mode: r.mode, Timestamp: startedAt})
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
		r.clearDecisions()
	}
	if r.sessionID != "" {
		_ = r.execution.FinishWithEvent(r.id, state, agentruntime.RunEvent{SessionID: r.sessionID, RunID: r.id, EventType: "finished", Source: "tui", Status: string(state), Model: r.model, Mode: r.mode, Timestamp: time.Now()})
		_ = (agentruntime.RunStore{SessionDir: r.sessionDir}).Finish(r.id, state, "")
	} else {
		_ = r.execution.FinishWithState(r.id, state)
	}
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
