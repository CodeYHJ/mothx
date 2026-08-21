package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestACPReplayPendingDecisionProjection(t *testing.T) {
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	mgr := session.New(t.TempDir(), settings.GetSessionDir())
	if err := mgr.Init(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	s := &server{settings: settings, w: &output, pending: make(map[string]chan json.RawMessage), sessions: make(map[string]*sessionRuntime)}
	rt := &sessionRuntime{id: mgr.GetHeader().ID, mgr: mgr, decisions: &agentruntime.DecisionService{}, execution: &agentruntime.ExecutionRuntime{}}
	if _, err := rt.execution.Begin(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	s.sessions[rt.id] = rt
	request := agentruntime.DecisionRequest{ID: "question-1", SessionID: rt.id, RunID: "run-1", Kind: agentruntime.DecisionQuestion}
	if err := rt.decisions.Register(request); err != nil {
		t.Fatal(err)
	}
	record, err := agentruntime.NewDecisionRequestRecordWithDeadline(request, questionRequest{SessionID: rt.id, Question: "continue?", Options: []string{"yes"}}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"decision": record})
	if _, err := session.SaveSessionRunEvent(settings.GetSessionDir(), session.SessionRunEvent{SessionID: rt.id, RunID: "run-1", EventType: "decision_pending", Source: "acp", Status: "pending", Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := s.replayPendingDecisionRequests(rt.id); err != nil {
		t.Fatal(err)
	}
	var notification map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &notification); err != nil {
		t.Fatal(err)
	}
	if notification["method"] != "_mothx/request_question" {
		t.Fatalf("notification = %#v", notification)
	}
}

func TestACPReplayPendingStandardElicitationProjection(t *testing.T) {
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	mgr := session.New(t.TempDir(), settings.GetSessionDir())
	if err := mgr.Init(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	s := &server{
		settings: settings, w: &output, pending: make(map[string]chan json.RawMessage), sessions: make(map[string]*sessionRuntime),
		clientCaps: clientCapabilities{Elicitation: &clientElicitationCapabilities{Form: &struct{}{}}},
	}
	rt := &sessionRuntime{id: mgr.GetHeader().ID, mgr: mgr, decisions: &agentruntime.DecisionService{}, execution: &agentruntime.ExecutionRuntime{}}
	if _, err := rt.execution.Begin(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	s.sessions[rt.id] = rt
	request := agentruntime.DecisionRequest{ID: "question-standard-1", SessionID: rt.id, RunID: "run-1", Kind: agentruntime.DecisionQuestion}
	if err := rt.decisions.Register(request); err != nil {
		t.Fatal(err)
	}
	record, err := agentruntime.NewDecisionRequestRecordWithDeadline(request, questionRequest{
		SessionID: rt.id, Question: "continue?", Options: []string{"yes"}, Protocol: acpElicitationFormProtocol,
	}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"decision": record})
	if _, err := session.SaveSessionRunEvent(settings.GetSessionDir(), session.SessionRunEvent{
		SessionID: rt.id, RunID: "run-1", EventType: "decision_pending", Source: "acp", Status: "pending", Data: data,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.replayPendingDecisionRequests(rt.id); err != nil {
		t.Fatal(err)
	}
	var notification map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &notification); err != nil {
		t.Fatal(err)
	}
	if notification["method"] != "elicitation/create" {
		t.Fatalf("notification = %#v", notification)
	}
}

func TestACPReplayPendingDecisionProjectionSkipsResolved(t *testing.T) {
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	mgr := session.New(t.TempDir(), settings.GetSessionDir())
	if err := mgr.Init(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	s := &server{settings: settings, w: &output, pending: make(map[string]chan json.RawMessage), sessions: make(map[string]*sessionRuntime)}
	rt := &sessionRuntime{id: mgr.GetHeader().ID, mgr: mgr, decisions: &agentruntime.DecisionService{}, execution: &agentruntime.ExecutionRuntime{}}
	if _, err := rt.execution.Begin(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	s.sessions[rt.id] = rt
	request := agentruntime.DecisionRequest{ID: "approval-1", SessionID: rt.id, RunID: "run-1", Kind: agentruntime.DecisionApproval}
	_ = rt.decisions.Register(request)
	pending, _ := agentruntime.NewDecisionRequestRecord(request, requestPermissionRequest{SessionID: rt.id})
	resolved, _ := agentruntime.NewDecisionResolutionRecord(request, agentruntime.DecisionResolution{ID: request.ID, Kind: request.Kind, Status: "resolved"}, nil)
	for _, record := range []agentruntime.DecisionRecord{pending, resolved} {
		data, _ := json.Marshal(map[string]any{"decision": record})
		_, _ = session.SaveSessionRunEvent(settings.GetSessionDir(), session.SessionRunEvent{SessionID: rt.id, RunID: "run-1", EventType: "decision_" + record.Status, Source: "acp", Status: record.Status, Data: data})
	}
	if err := s.replayPendingDecisionRequests(rt.id); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("resolved decision replayed: %s", output.String())
	}
}
