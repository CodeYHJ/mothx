package agentruntime

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestRuntimeShutdownAcrossProcessBoundary(t *testing.T) {
	sessionDir := t.TempDir()
	runRuntimeProcessHelper(t, "shutdown", sessionDir, "process-shutdown-session", "process-shutdown-run")
	run, err := session.GetSessionRun(sessionDir, "process-shutdown-run")
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "cancelled" {
		t.Fatalf("run after child shutdown = %#v", run)
	}
}

func TestRuntimeOrphanRecoveryAcrossProcessBoundary(t *testing.T) {
	sessionDir := t.TempDir()
	runRuntimeProcessHelper(t, "orphan", sessionDir, "process-orphan-session", "process-orphan-run")
	before, err := session.GetSessionRun(sessionDir, "process-orphan-run")
	if err != nil || before == nil || before.Status != "running" {
		t.Fatalf("run before recovery = %#v, err=%v", before, err)
	}
	if _, err := RecoverOrphanedRuns(sessionDir, func(run session.SessionRun) RecoveryAction {
		return RecoveryFailLocal
	}, nil); err != nil {
		t.Fatal(err)
	}
	after, err := session.GetSessionRun(sessionDir, "process-orphan-run")
	if err != nil || after == nil || after.Status != "failed" {
		t.Fatalf("run after recovery = %#v, err=%v", after, err)
	}
	events, err := session.ListSessionRunEvents(sessionDir, "process-orphan-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].EventType != "started" || events[1].EventType != "recovered" || events[1].Status != "failed" {
		t.Fatalf("recovery events = %#v", events)
	}
}

func runRuntimeProcessHelper(t *testing.T, action, sessionDir, sessionID, runID string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeProcessHelper$")
	cmd.Env = append(os.Environ(),
		"MOTHX_RUNTIME_PROCESS_HELPER=1",
		"MOTHX_RUNTIME_PROCESS_ACTION="+action,
		"MOTHX_RUNTIME_PROCESS_SESSION_DIR="+sessionDir,
		"MOTHX_RUNTIME_PROCESS_SESSION_ID="+sessionID,
		"MOTHX_RUNTIME_PROCESS_RUN_ID="+runID,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("process helper: %v\n%s", err, output)
	}
}

func TestRuntimeProcessHelper(t *testing.T) {
	if os.Getenv("MOTHX_RUNTIME_PROCESS_HELPER") != "1" {
		return
	}
	sessionDir := os.Getenv("MOTHX_RUNTIME_PROCESS_SESSION_DIR")
	sessionID := os.Getenv("MOTHX_RUNTIME_PROCESS_SESSION_ID")
	runID := os.Getenv("MOTHX_RUNTIME_PROCESS_RUN_ID")
	manager := session.New(t.TempDir(), sessionDir)
	if err := manager.InitWithID(sessionID); err != nil {
		t.Fatal(err)
	}
	execution := &ExecutionRuntime{}
	execution.SetRunStore(RunStore{SessionDir: sessionDir})
	execution.SetEventSink(SessionRunEventSink{SessionDir: sessionDir})
	runtime := &SessionRuntime{ID: sessionID, Source: SourceWebUI, WorkDir: manager.GetHeader().Cwd, Manager: manager, Execution: execution}
	if _, err := execution.BeginDurable(context.Background(), DurableRun{
		ID: runID, SessionID: sessionID, WorkDir: manager.GetHeader().Cwd,
		Source: "webui", Model: "process-model", Mode: "agent", Status: "running", StartedAt: time.Now(),
	}, RunEvent{SessionID: sessionID, RunID: runID, EventType: "started", Source: "webui", Status: "running", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MOTHX_RUNTIME_PROCESS_ACTION") == "shutdown" {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
