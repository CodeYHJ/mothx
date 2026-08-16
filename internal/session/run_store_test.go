package session

import (
	"database/sql"
	"testing"
	"time"
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
