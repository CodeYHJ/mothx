package openaiapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/esm"
)

func TestESMCoordinatorStopAllCancelsAndWaits(t *testing.T) {
	coordinator := newESMCoordinator()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	done := make(chan struct{})
	coordinator.mu.Lock()
	coordinator.running["session-1"] = cancelWorker
	coordinator.done["session-1"] = done
	coordinator.mu.Unlock()
	go func() {
		<-workerCtx.Done()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.stopAll(ctx); err != nil {
		t.Fatalf("stopAll: %v", err)
	}
	coordinator.mu.Lock()
	closed := coordinator.closed
	coordinator.mu.Unlock()
	if !closed {
		t.Fatal("stopAll did not close the coordinator")
	}
}

func TestESMCoordinatorStopCancelsAndWaits(t *testing.T) {
	coordinator := newESMCoordinator()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	done := make(chan struct{})
	coordinator.mu.Lock()
	coordinator.running["session-1"] = cancelWorker
	coordinator.done["session-1"] = done
	coordinator.mu.Unlock()
	go func() {
		<-workerCtx.Done()
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := coordinator.stop(ctx, "session-1"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestWebESMRuntimeAdapterHandlesClosedSessionRuntime(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	sess, err := srv.getOrCreateSession("webui-esm-closed-runtime", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("getOrCreateSession: %v", err)
	}
	if sess.Runtime == nil {
		t.Fatal("test session has no runtime")
	}
	sess.Runtime.Close()

	adapter := &webESMRuntimeAdapter{
		server: srv, sess: sess, workDir: sess.WorkDir,
		source: "webui", mode: "agent",
	}
	_, err = adapter.RunRole(context.Background(), esm.RoleRequest{
		SessionID: sess.ID, RunID: "closed-runtime-role", Role: esm.RoleWorker,
		WorkDir: sess.WorkDir, Mode: "agent", Prompt: "should fail cleanly",
	})
	if err == nil || !strings.Contains(err.Error(), "agent manager is unavailable") {
		t.Fatalf("RunRole error = %v, want unavailable manager error", err)
	}
}

func TestApplyESMWorkerContinueResetsCompletionRejectionStreak(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	ctx := context.Background()
	const sessionID = "webui-esm-worker-continue"
	store := srv.esmStore()
	if _, err := store.Create(ctx, sessionID, "finish migration"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 1; i <= esm.CompletionRejectionLimit; i++ {
		runID := fmt.Sprintf("run-%d", i)
		obj, err := store.UpdateFromModelForRun(ctx, sessionID, esm.StatusComplete, "worker evidence", runID)
		if err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
		if _, err := store.RejectCompletionCandidateForRun(ctx, sessionID, runID, "missing requirement", []string{"finish implementation"}); err != nil {
			t.Fatalf("reject %d: %v", i, err)
		}

		if !srv.applyESMWorker(ctx, store, obj, runID+"-continue", esm.RoleResult{
			Response:  `{"status":"continue","summary":"implemented missing requirement","evidence":["focused test passes"],"remaining_work":[],"blockers":[]}`,
			ToolCalls: 1,
			ToolError: map[string]bool{},
		}) {
			t.Fatalf("apply continue %d failed", i)
		}
		obj, err = store.Get(ctx, sessionID)
		if err != nil {
			t.Fatalf("Get after continue %d: %v", i, err)
		}
		if obj.Status != esm.StatusActive || obj.RejectionCount != 0 || obj.RejectionRunID != "" {
			t.Fatalf("continue %d did not reset rejection streak: %#v", i, obj)
		}
	}
}

func TestResolveESMRuntimePolicyDerivesUnattendedMode(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	sess, err := srv.getOrCreateSession("webui-esm-mode-policy", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("getOrCreateSession: %v", err)
	}
	if sess.Runtime == nil {
		t.Fatal("test session has no runtime")
	}

	for _, tt := range []struct{ session, want string }{
		{"agent", "yolo"},
		{"plan", "yolo"},
		{"yolo", "yolo"},
		{"os", "os"},
		{"", "yolo"},
	} {
		sess.Mode = tt.session
		_, mode, err := srv.resolveESMRuntimePolicy(sess)
		if err != nil {
			t.Fatalf("resolveESMRuntimePolicy(%q): %v", tt.session, err)
		}
		if mode != tt.want {
			t.Fatalf("resolveESMRuntimePolicy(%q) = %q, want %q", tt.session, mode, tt.want)
		}
	}
}
