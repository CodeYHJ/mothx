package openaiapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/startvibecoding/mothx/internal/esm"
)

func TestApplyESMWorkerContinueResetsCompletionRejectionStreak(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	ctx := context.Background()
	const sessionID = "webui-esm-worker-continue"
	store := srv.esmStore()
	if _, err := store.Create(ctx, sessionID, "finish migration", nil); err != nil {
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
