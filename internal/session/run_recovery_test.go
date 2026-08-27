package session

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionRunRecoveryRequiresFencedRecoveryLease(t *testing.T) {
	sessionDir := t.TempDir()
	manager := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := manager.InitWithID("recovery-record"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := SaveSessionRun(sessionDir, SessionRun{
		ID: "run-record", SessionID: "recovery-record", Status: "running", StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginSessionRunRecovery(sessionDir, "recovery-record", "run-record", "startup", "owner_lost", 3); err == nil {
		t.Fatal("recovery record succeeded without a recovery lease")
	}

	guard, err := AcquireRecovery(sessionDir, "recovery-record", "run-record")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := BeginSessionRunRecovery(sessionDir, "recovery-record", "run-record", "startup", "owner_lost", 3)
	if err != nil {
		guard.Release()
		t.Fatal(err)
	}
	if recovery.Attempt != 1 || recovery.State != SessionRunRecoveryRunning || recovery.PreviousLeaseEpoch != 3 {
		guard.Release()
		t.Fatalf("first recovery = %#v", recovery)
	}
	nextRetry := time.Now().Add(time.Minute)
	if err := MarkSessionRunRecoveryFailed(sessionDir, "recovery-record", "run-record", "database busy", nextRetry); err != nil {
		guard.Release()
		t.Fatal(err)
	}
	guard.Release()

	facts, err := ReadSessionExecutionFacts(sessionDir, "recovery-record")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Recovery == nil || facts.Recovery.State != SessionRunRecoveryFailed || facts.Recovery.LastError != "database busy" || facts.Recovery.NextRetryAt == nil {
		t.Fatalf("recovery facts = %#v", facts.Recovery)
	}

	guard, err = AcquireRecovery(sessionDir, "recovery-record", "run-record")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	recovery, err = BeginSessionRunRecovery(sessionDir, "recovery-record", "run-record", "periodic", "owner_lost", guard.Binding().Epoch-1)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Attempt != 2 || recovery.State != SessionRunRecoveryRunning || recovery.LastError != "" || recovery.NextRetryAt != nil {
		t.Fatalf("retried recovery = %#v", recovery)
	}
	if err := MarkSessionRunRecoveryComplete(sessionDir, "recovery-record", "run-record"); err != nil {
		t.Fatal(err)
	}
	recovery, err = GetSessionRunRecovery(sessionDir, "run-record")
	if err != nil || recovery == nil || recovery.State != SessionRunRecoveryComplete || recovery.CompletedAt == nil {
		t.Fatalf("completed recovery = %#v, err=%v", recovery, err)
	}
}

func TestConvergeSessionRunRecoveryAtomicallyClosesRunTurnDecisionsAndRecovery(t *testing.T) {
	sessionDir := t.TempDir()
	manager := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := manager.InitWithID("recovery-converge"); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquireExecutionAdmission(sessionDir, manager.GetHeader().ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	run := SessionRun{
		ID: "run-converge", SessionID: manager.GetHeader().ID, IntentID: "intent-converge",
		Status: "running", StartedAt: now, UpdatedAt: now,
	}
	if _, err := CreateSessionRunAndEventWithTurn(sessionDir, run, SessionRunEvent{
		ID: "event-started", EventType: "started", Timestamp: now,
	}, ConversationTurn{
		ID: "turn-converge", SessionID: run.SessionID, IntentID: run.IntentID, RunID: run.ID, StartedAt: now,
	}); err != nil {
		guard.Release()
		t.Fatal(err)
	}
	decisionData := json.RawMessage(`{"decision":{"id":"approval-1","sessionId":"recovery-converge","runId":"run-converge","kind":"approval","status":"pending"}}`)
	if _, err := SaveSessionRunEvent(sessionDir, SessionRunEvent{
		ID: "decision-request", SessionID: run.SessionID, RunID: run.ID, EventType: "approval_requested",
		Status: "pending", Timestamp: now, Data: decisionData,
	}); err != nil {
		guard.Release()
		t.Fatal(err)
	}
	guard.Release()

	recoveryGuard, err := AcquireRecovery(sessionDir, run.SessionID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryGuard.Release()
	if _, err := BeginSessionRunRecovery(sessionDir, run.SessionID, run.ID, "user_stop", "cancelled_by_user_after_owner_loss", 1); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now()
	run.Status = "cancelled"
	run.Error = "owner lost"
	run.FinishedAt = &finishedAt
	resolutionData := json.RawMessage(`{"decision":{"id":"approval-1","sessionId":"recovery-converge","runId":"run-converge","kind":"approval","status":"cancelled","value":"deny_once"}}`)
	if err := ConvergeSessionRunRecovery(sessionDir, run, SessionRunEvent{
		ID: "event-recovered", EventType: "recovered", Status: "cancelled", Timestamp: finishedAt,
	}, []SessionRunEvent{{
		ID: "decision-resolution", EventType: "approval_resolved", Status: "cancelled", Timestamp: finishedAt, Data: resolutionData,
	}}, "cancelled", "cancelled_by_user_after_owner_loss"); err != nil {
		t.Fatal(err)
	}

	stored, err := GetSessionRun(sessionDir, run.ID)
	if err != nil || stored == nil || stored.Status != "cancelled" {
		t.Fatalf("terminal run = %+v err=%v", stored, err)
	}
	turns, err := ListConversationTurns(sessionDir, run.SessionID)
	if err != nil || len(turns) != 1 || turns[0].Status != "cancelled" || turns[0].EndSeq == nil {
		t.Fatalf("closed turns = %+v err=%v", turns, err)
	}
	events, err := ListSessionRunEvents(sessionDir, run.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	seenResolution, seenTerminal := false, false
	for _, event := range events {
		seenResolution = seenResolution || event.ID == "decision-resolution"
		seenTerminal = seenTerminal || event.ID == "event-recovered"
	}
	if !seenResolution || !seenTerminal {
		t.Fatalf("recovery events missing: %+v", events)
	}
	recovery, err := GetSessionRunRecovery(sessionDir, run.ID)
	if err != nil || recovery == nil || recovery.State != SessionRunRecoveryComplete {
		t.Fatalf("recovery = %+v err=%v", recovery, err)
	}
}
