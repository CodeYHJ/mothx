package agentruntime

import (
	"errors"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestRecoverOrphanedRunsFailsLocalAndKeepsRemote(t *testing.T) {
	sessionDir := t.TempDir()
	store := RunStore{SessionDir: sessionDir}
	now := time.Now()
	initRecoveryTestSession(t, sessionDir, "session-1")
	initRecoveryTestSession(t, sessionDir, "session-2")
	for _, run := range []DurableRun{
		{ID: "local", SessionID: "session-1", Source: "acp", Status: "running", StartedAt: now},
		{ID: "remote", SessionID: "session-2", Source: "responses_background", Status: "running", StartedAt: now},
	} {
		if err := store.Create(run); err != nil {
			t.Fatal(err)
		}
	}
	var cleaned []string
	result, err := RecoverOrphanedRuns(sessionDir, func(run session.SessionRun) RecoveryAction {
		if run.ID == "remote" {
			return RecoveryKeepRemote
		}
		return RecoveryFailLocal
	}, func(run session.SessionRun) error {
		cleaned = append(cleaned, run.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 1 || result.Failed[0].ID != "local" || len(result.Kept) != 1 || result.Kept[0].ID != "remote" {
		t.Fatalf("result = %#v", result)
	}
	if len(cleaned) != 1 || cleaned[0] != "local" {
		t.Fatalf("cleaned = %#v", cleaned)
	}
	local, _ := session.GetSessionRun(sessionDir, "local")
	remote, _ := session.GetSessionRun(sessionDir, "remote")
	if local.Status != "failed" || remote.Status != "running" {
		t.Fatalf("local=%#v remote=%#v", local, remote)
	}
}

func TestRecoverOrphanedSessionRunForAdmissionFailsOnlyLocalRun(t *testing.T) {
	sessionDir := t.TempDir()
	store := RunStore{SessionDir: sessionDir}
	now := time.Now()
	initRecoveryTestSession(t, sessionDir, "session-local")
	initRecoveryTestSession(t, sessionDir, "session-remote")
	if err := store.Create(DurableRun{ID: "stale-local", SessionID: "session-local", Source: "wechat", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(DurableRun{ID: "remote", SessionID: "session-remote", Source: "responses_background", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}

	local, err := RecoverOrphanedSessionRun(sessionDir, "session-local", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Failed) != 1 || local.Failed[0].ID != "stale-local" || len(local.Kept) != 0 {
		t.Fatalf("local recovery = %#v", local)
	}
	recovered, err := session.GetSessionRun(sessionDir, "stale-local")
	if err != nil || recovered == nil || recovered.Status != "failed" {
		t.Fatalf("recovered local run = %#v, err=%v", recovered, err)
	}

	remote, err := RecoverOrphanedSessionRun(sessionDir, "session-remote", func(run session.SessionRun) RecoveryAction {
		return RecoveryKeepRemote
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.Kept) != 1 || remote.Kept[0].ID != "remote" || len(remote.Failed) != 0 {
		t.Fatalf("remote recovery = %#v", remote)
	}
	stillActive, err := session.GetSessionRun(sessionDir, "remote")
	if err != nil || stillActive == nil || stillActive.Status != "running" {
		t.Fatalf("remote run = %#v, err=%v", stillActive, err)
	}
}

func TestRecoverOrphanedRunsSkipsValidExecutionLease(t *testing.T) {
	sessionDir := t.TempDir()
	initRecoveryTestSession(t, sessionDir, "session-owned")
	guard, err := session.AcquireExecutionAdmission(sessionDir, "session-owned")
	if err != nil {
		t.Fatal(err)
	}
	store := RunStore{SessionDir: sessionDir}
	if err := store.Create(DurableRun{ID: "owned", SessionID: "session-owned", Source: "acp", Status: "running", StartedAt: time.Now()}); err != nil {
		guard.Release()
		t.Fatal(err)
	}

	result, err := RecoverOrphanedRuns(sessionDir, nil, nil)
	if err != nil {
		guard.Release()
		t.Fatal(err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].ID != "owned" || len(result.Failed) != 0 {
		guard.Release()
		t.Fatalf("recovery with live owner = %#v", result)
	}
	active, err := session.GetSessionRun(sessionDir, "owned")
	if err != nil || active == nil || active.Status != "running" {
		guard.Release()
		t.Fatalf("owned run = %#v, err=%v", active, err)
	}

	guard.Release()
	result, err = RecoverOrphanedRuns(sessionDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 1 || result.Failed[0].ID != "owned" {
		t.Fatalf("recovery after release = %#v", result)
	}
}

func TestDefaultRunRecoveryPolicyDoesNotTrustSourceAlone(t *testing.T) {
	if got := DefaultRunRecoveryPolicy(session.SessionRun{Source: "responses_background"}); got != RecoveryFailLocal {
		t.Fatalf("default policy = %q, want %q", got, RecoveryFailLocal)
	}
}

func TestRecoverOrphanedRunsKeepsVerifiedRemoteRecord(t *testing.T) {
	sessionDir := t.TempDir()
	initRecoveryTestSession(t, sessionDir, "session-remote-record")
	now := time.Now()
	if err := (RunStore{SessionDir: sessionDir}).Create(DurableRun{
		ID: "remote-parent", SessionID: "session-remote-record", Source: "responses_background", Status: "running", StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveResponseRun(sessionDir, session.ResponseRun{
		SessionID: "session-remote-record", LocalRunID: "remote-provider-run", LocalTurnID: "remote-parent",
		ResponseID: "resp-remote", Provider: "openai", API: "openai-responses", State: "queued",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := RecoverOrphanedRuns(sessionDir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Kept) != 1 || result.Kept[0].ID != "remote-parent" || len(result.Failed) != 0 {
		t.Fatalf("verified remote recovery = %#v", result)
	}
	recovery, err := session.GetSessionRunRecovery(sessionDir, "remote-parent")
	if err != nil || recovery == nil || recovery.State != session.SessionRunRecoveryDetached {
		t.Fatalf("remote recovery record = %#v, err=%v", recovery, err)
	}
}

func TestRecoveryFailureIsDurableAndRetryable(t *testing.T) {
	sessionDir := t.TempDir()
	initRecoveryTestSession(t, sessionDir, "session-retry")
	store := RunStore{SessionDir: sessionDir}
	if err := store.Create(DurableRun{ID: "retry", SessionID: "session-retry", Source: "tui", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("decision cleanup unavailable")
	if _, err := RecoverOrphanedRuns(sessionDir, nil, func(session.SessionRun) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("recovery error = %v, want %v", err, wantErr)
	}
	recovery, err := session.GetSessionRunRecovery(sessionDir, "retry")
	if err != nil {
		t.Fatal(err)
	}
	if recovery == nil || recovery.State != session.SessionRunRecoveryFailed || recovery.Attempt != 1 || recovery.LastError != wantErr.Error() || recovery.NextRetryAt == nil {
		t.Fatalf("failed recovery record = %#v", recovery)
	}
	snapshot, err := InspectSessionExecution(sessionDir, "session-retry")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != SessionExecutionRecoveryFailed || snapshot.Running || !snapshot.Busy || snapshot.RecoveryAttempt != 1 {
		t.Fatalf("failed recovery snapshot = %#v", snapshot)
	}

	result, err := RecoverOrphanedSessionRun(sessionDir, "session-retry", nil, nil)
	if err != nil || len(result.Failed) != 1 {
		t.Fatalf("retry result = %#v, err=%v", result, err)
	}
	recovery, err = session.GetSessionRunRecovery(sessionDir, "retry")
	if err != nil || recovery == nil || recovery.State != session.SessionRunRecoveryComplete || recovery.Attempt != 2 || recovery.CompletedAt == nil {
		t.Fatalf("completed recovery record = %#v, err=%v", recovery, err)
	}
}

func initRecoveryTestSession(t *testing.T, sessionDir, id string) {
	t.Helper()
	manager := session.New(t.TempDir(), sessionDir)
	if err := manager.InitWithID(id); err != nil {
		t.Fatalf("create recovery test session %s: %v", id, err)
	}
}
