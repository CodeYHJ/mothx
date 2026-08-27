package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestReadSessionExecutionFactsUsesCanonicalRunAndLease(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := mgr.InitWithID("execution-facts"); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquireExecutionAdmission(sessionDir, "execution-facts")
	if err != nil {
		t.Fatalf("acquire execution admission: %v", err)
	}
	defer guard.Release()
	now := time.Now().UTC()
	if err := SaveSessionRun(sessionDir, SessionRun{
		ID: "run-active", SessionID: "execution-facts", Status: "running", Source: "test",
		StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if binding := guard.Binding(); binding.Purpose != RuntimeLeasePurposeExecution || binding.RunID != "run-active" {
		t.Fatalf("bound admission = %+v, want run-active execution binding", binding)
	}

	facts, err := ReadSessionExecutionFacts(sessionDir, "execution-facts")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.SessionExists || facts.DatabaseNow.IsZero() {
		t.Fatalf("session facts = %+v, want existing session and database clock", facts)
	}
	if len(facts.ActiveRuns) != 1 || facts.ActiveRuns[0].ID != "run-active" {
		t.Fatalf("active runs = %+v, want run-active", facts.ActiveRuns)
	}
	if facts.Lease == nil {
		t.Fatal("execution lease missing from facts")
	}
	if facts.Lease.Purpose != RuntimeLeasePurposeExecution || facts.Lease.RunID != "run-active" || !facts.Lease.Valid {
		t.Fatalf("execution lease = %+v, want valid bound execution lease", facts.Lease)
	}
	binding := guard.Binding()
	if facts.Lease.OwnerInstanceID != binding.OwnerInstanceID || facts.Lease.Epoch != binding.Epoch || facts.Lease.TokenHash != binding.TokenHash {
		t.Fatalf("execution lease identity = %+v, want local lease identity", facts.Lease)
	}
}

func TestReadSessionExecutionFactsPreservesReleasedLeaseTombstone(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := mgr.InitWithID("execution-facts-released"); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireRuntimeLease(sessionDir, "execution-facts-released", string(RuntimeLeasePurposeMutation))
	if err != nil || lease == nil {
		t.Fatalf("acquire mutation lease = %v, lease=%v", err, lease)
	}
	lease.release()

	facts, err := ReadSessionExecutionFacts(sessionDir, "execution-facts-released")
	if err != nil {
		t.Fatal(err)
	}
	if facts.Lease == nil {
		t.Fatal("released lease tombstone missing from facts")
	}
	if facts.Lease.State != "released" || facts.Lease.Valid {
		t.Fatalf("released lease = %+v, want invalid released tombstone", facts.Lease)
	}
	if len(facts.ActiveRuns) != 0 {
		t.Fatalf("active runs = %+v, want none", facts.ActiveRuns)
	}
}

func TestReadSessionExecutionFactsReportsMissingSession(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := mgr.InitWithID("execution-facts-existing"); err != nil {
		t.Fatal(err)
	}

	facts, err := ReadSessionExecutionFacts(sessionDir, "execution-facts-missing")
	if err != nil {
		t.Fatal(err)
	}
	if facts.SessionExists || len(facts.ActiveRuns) != 0 || facts.Lease != nil {
		t.Fatalf("missing session facts = %+v, want no durable rows", facts)
	}
}

func TestCanonicalNonTerminalSessionRunStatuses(t *testing.T) {
	want := map[string]bool{
		"created": true, "queued": true, "running": true,
		"waiting_for_approval": true, "waiting_for_question": true,
		"cancelling": true, "terminalizing": true,
	}
	got := NonTerminalSessionRunStatuses()
	if len(got) != len(want) {
		t.Fatalf("non-terminal status count = %d, want %d", len(got), len(want))
	}
	for _, status := range got {
		if !want[status] || !IsNonTerminalSessionRunStatus(status) {
			t.Fatalf("unexpected non-terminal status %q", status)
		}
	}
	if IsNonTerminalSessionRunStatus("completed") || IsNonTerminalSessionRunStatus("failed") {
		t.Fatal("terminal status reported as non-terminal")
	}
}
