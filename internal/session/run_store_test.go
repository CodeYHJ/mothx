package session

import (
	"database/sql"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
)

func TestCreateSessionRunRejectsDuplicateAndStatusRollback(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("session-run-store"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	run := SessionRun{ID: "run-1", SessionID: mgr.GetHeader().ID, Status: "running", StartedAt: started}
	if err := CreateSessionRun(sessionDir, run); err != nil {
		t.Fatalf("CreateSessionRun: %v", err)
	}
	if err := CreateSessionRun(sessionDir, run); err == nil {
		t.Fatal("duplicate CreateSessionRun unexpectedly succeeded")
	}
	if err := UpdateSessionRunStatus(sessionDir, run.ID, "completed", "", &started); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := UpdateSessionRunStatus(sessionDir, run.ID, "running", "", nil); err == nil {
		t.Fatal("terminal-to-running transition unexpectedly succeeded")
	}
	if err := UpdateSessionRunStatus(sessionDir, run.ID, "completed", "", &started); err != nil {
		t.Fatalf("idempotent terminal transition: %v", err)
	}
	if _, err := GetSessionRun(sessionDir, "missing"); err != nil && err != sql.ErrNoRows {
		t.Fatalf("missing run lookup: %v", err)
	}
}

func TestUpdateSessionRunStatusAllowsWaitingResumeAndCancellation(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("session-run-state"); err != nil {
		t.Fatal(err)
	}
	run := SessionRun{ID: "run-1", SessionID: mgr.GetHeader().ID, Status: "queued", StartedAt: time.Now()}
	if err := CreateSessionRun(sessionDir, run); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"running", "waiting_for_approval", "running", "cancelling", "cancelled"} {
		if err := UpdateSessionRunStatus(sessionDir, run.ID, status, "", nil); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
}

func TestNextSessionRunAttemptUsesHighestExistingAttempt(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("session-run-attempts"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for _, run := range []SessionRun{
		{ID: "run-a", SessionID: mgr.GetHeader().ID, IntentID: "intent-a", Attempt: 1, Status: "failed", StartedAt: started, UpdatedAt: started, FinishedAt: &started},
		{ID: "run-b", SessionID: mgr.GetHeader().ID, IntentID: "intent-a", RetryOf: "run-a", Attempt: 2, Status: "failed", StartedAt: started, UpdatedAt: started, FinishedAt: &started},
	} {
		if err := CreateSessionRun(sessionDir, run); err != nil {
			t.Fatalf("create run %s: %v", run.ID, err)
		}
	}
	attempt, err := NextSessionRunAttempt(sessionDir, mgr.GetHeader().ID, "intent-a")
	if err != nil {
		t.Fatalf("next attempt: %v", err)
	}
	if attempt != 3 {
		t.Fatalf("attempt = %d, want 3", attempt)
	}
}

func TestFinishSessionRunAndConversationTurnCommitsAssistantIdempotently(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("assistant-terminal"); err != nil {
		t.Fatal(err)
	}
	release, ok := TryLockRuntime(sessionDir, "assistant-terminal")
	if !ok {
		t.Fatal("acquire runtime lease")
	}
	defer release()
	started := time.Now().UTC()
	if _, err := CreateSessionRunAndEventWithTurn(sessionDir,
		SessionRun{ID: "run-assistant", SessionID: "assistant-terminal", IntentID: "intent-assistant", Status: "running", StartedAt: started},
		SessionRunEvent{ID: "run-start-assistant", SessionID: "assistant-terminal", RunID: "run-assistant", EventType: "started", Status: "running", Timestamp: started},
		ConversationTurn{ID: "turn-assistant", SessionID: "assistant-terminal", IntentID: "intent-assistant", RunID: "run-assistant", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	assistant := provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "answer"}})
	planTime := started.Add(1500 * time.Millisecond)
	plan := &DeliveryPlan{Intent: DeliveryIntent{
		ID: "intent-delivery-assistant", SessionID: "assistant-terminal", RunID: "run-assistant",
		Platform: "wechat", TargetID: "chat", Status: "pending", CreatedAt: planTime, UpdatedAt: planTime,
	}, Operations: []DeliveryOperation{{
		ID: "op-delivery-assistant", OperationKey: "caption", OperationKind: "send_text", Sequence: 1,
		IdempotencyKey: "op-delivery-assistant", PayloadDigest: "sha256:caption", Status: "pending", CreatedAt: planTime, UpdatedAt: planTime,
	}}}
	terminalRun := SessionRun{
		ID: "run-assistant", SessionID: "assistant-terminal", Status: "completed",
		FinishedAt:       &[]time.Time{started.Add(2 * time.Second)}[0],
		AssistantEntryID: RunAssistantEntryID("run-assistant"), AssistantMessage: &assistant,
		DeliveryPlan: plan,
	}
	terminalEvent := SessionRunEvent{SessionID: "assistant-terminal", RunID: "run-assistant", EventType: "finished", Status: "completed", Timestamp: started.Add(2 * time.Second)}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := FinishSessionRunAndConversationTurn(sessionDir, terminalRun, terminalEvent, "turn-assistant", "completed", "stop"); err != nil {
			t.Fatalf("terminal attempt %d: %v", attempt+1, err)
		}
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	var assistantCount, terminalEventCount, turnEndCount, intentCount, operationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entries WHERE session_id = ? AND id = ? AND type = 'message'`, "assistant-terminal", RunAssistantEntryID("run-assistant")).Scan(&assistantCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_run_events WHERE run_id = ? AND id = ?`, "run-assistant", RunTerminalEventID("run-assistant", "finished")).Scan(&terminalEventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM entries WHERE session_id = ? AND type = 'turn_end'`, "assistant-terminal").Scan(&turnEndCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_intents WHERE id = ?`, "intent-delivery-assistant").Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_operations WHERE id = ?`, "op-delivery-assistant").Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if assistantCount != 1 || terminalEventCount != 1 || turnEndCount != 1 || intentCount != 1 || operationCount != 1 {
		t.Fatalf("idempotent terminal counts: assistant=%d event=%d turnEnd=%d intent=%d op=%d", assistantCount, terminalEventCount, turnEndCount, intentCount, operationCount)
	}
	conflicting := assistant
	conflicting.Content = "different terminal result"
	terminalRun.AssistantMessage = &conflicting
	if _, err := FinishSessionRunAndConversationTurn(sessionDir, terminalRun, terminalEvent, "turn-assistant", "completed", "stop"); err == nil {
		t.Fatal("conflicting terminal assistant message unexpectedly succeeded")
	}
}

func TestFinishSessionRunAndConversationTurnRollsBackInvalidDeliveryPlan(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("assistant-rollback"); err != nil {
		t.Fatal(err)
	}
	release, ok := TryLockRuntime(sessionDir, "assistant-rollback")
	if !ok {
		t.Fatal("acquire runtime lease")
	}
	defer release()
	started := time.Now().UTC()
	if _, err := CreateSessionRunAndEventWithTurn(sessionDir,
		SessionRun{ID: "run-rollback", SessionID: "assistant-rollback", IntentID: "intent-rollback", Status: "running", StartedAt: started},
		SessionRunEvent{ID: "run-start-rollback", SessionID: "assistant-rollback", RunID: "run-rollback", EventType: "started", Status: "running", Timestamp: started},
		ConversationTurn{ID: "turn-rollback", SessionID: "assistant-rollback", IntentID: "intent-rollback", RunID: "run-rollback", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	assistant := provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "must not commit"}})
	plan := &DeliveryPlan{Intent: DeliveryIntent{
		ID: "intent-rollback-delivery", SessionID: "assistant-rollback", RunID: "run-rollback", Platform: "wechat", TargetID: "chat", Status: "pending",
	}, Operations: []DeliveryOperation{{
		ID: "op-rollback", OperationKey: "caption", OperationKind: "send_text", Sequence: 1,
		ArtifactID: "missing-artifact", IdempotencyKey: "op-rollback", PayloadDigest: "sha256:rollback", Status: "pending",
	}}}
	_, err := FinishSessionRunAndConversationTurn(sessionDir, SessionRun{
		ID: "run-rollback", SessionID: "assistant-rollback", Status: "completed",
		AssistantEntryID: RunAssistantEntryID("run-rollback"), AssistantMessage: &assistant, DeliveryPlan: plan,
	}, SessionRunEvent{EventType: "finished", Status: "completed", Timestamp: started.Add(time.Second)}, "turn-rollback", "completed", "stop")
	if err == nil {
		t.Fatal("invalid delivery plan unexpectedly committed")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	var runStatus, turnStatus string
	var assistantCount, terminalEventCount, intentCount int
	if err := db.QueryRow(`SELECT status FROM session_runs WHERE id = ?`, "run-rollback").Scan(&runStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM conversation_turns WHERE id = ?`, "turn-rollback").Scan(&turnStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM entries WHERE id = ?`, RunAssistantEntryID("run-rollback")).Scan(&assistantCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_run_events WHERE id = ?`, RunTerminalEventID("run-rollback", "finished")).Scan(&terminalEventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM delivery_intents WHERE id = ?`, "intent-rollback-delivery").Scan(&intentCount); err != nil {
		t.Fatal(err)
	}
	if runStatus != "running" || turnStatus != "open" || assistantCount != 0 || terminalEventCount != 0 || intentCount != 0 {
		t.Fatalf("rollback state: run=%q turn=%q assistant=%d event=%d intent=%d", runStatus, turnStatus, assistantCount, terminalEventCount, intentCount)
	}
}
