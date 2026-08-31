package agentruntime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestBeforeToolExecuteFenceRejectsExpiredLease(t *testing.T) {
	sessionDir := t.TempDir()
	manager := session.New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := manager.InitWithID("fence-session"); err != nil {
		t.Fatal(err)
	}
	guard, err := session.AcquireExecutionAdmission(sessionDir, "fence-session")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()

	execution := &ExecutionRuntime{}
	execution.SetRunStore(RunStore{SessionDir: sessionDir})
	now := time.Now()
	if _, err := execution.BeginDurable(context.Background(), DurableRun{
		ID: "fence-run", SessionID: "fence-session", Status: string(RunStateRunning), StartedAt: now,
	}, RunEvent{SessionID: "fence-session", RunID: "fence-run", EventType: "started", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{ID: "fence-session", Manager: manager, Execution: execution}
	hook := beforeToolExecuteForRuntime(runtime)
	if decision := hook(agent.BeforeToolExecuteContext{
		RunID: "fence-run", SideEffecting: true, ExecutionContext: context.Background(),
	}); decision != nil {
		t.Fatalf("fresh lease unexpectedly blocked: %#v", decision)
	}

	db, err := session.OpenRootDB(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bun().Exec(`UPDATE session_runtime_leases SET expires_at = CAST(strftime('%s','now') AS INTEGER) - 1 WHERE session_id = ?`, "fence-session"); err != nil {
		t.Fatal(err)
	}
	if decision := hook(agent.BeforeToolExecuteContext{
		RunID: "fence-run", SideEffecting: true, ExecutionContext: context.Background(),
	}); decision == nil || !decision.Block {
		t.Fatalf("expired lease decision = %#v, want blocked", decision)
	}
	_ = db.Close()
	_ = execution.FinishDurable("fence-run", RunStateFailed, "lease expired", RunEvent{SessionID: "fence-session", RunID: "fence-run", EventType: "failed", Status: "failed", Timestamp: time.Now()})
}
