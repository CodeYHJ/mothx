package agentruntime

import (
	"context"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestAnnotateDurableRunErrorOnlyTerminalizesEmptyErrors(t *testing.T) {
	sessionDir := t.TempDir()
	store := RunStore{SessionDir: sessionDir}
	now := time.Now()
	if err := store.Create(DurableRun{ID: "run-annotate", SessionID: "session-annotate", WorkDir: t.TempDir(), Source: "responses_background", Mode: "yolo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Active runs never accept an annotation; the lifecycle still owns them.
	applied, err := AnnotateDurableRunError(context.Background(), sessionDir, "run-annotate", "abandoned after interrupted tool execution")
	if err != nil || applied {
		t.Fatalf("annotate active run: applied=%v err=%v", applied, err)
	}

	if err := store.Finish("run-annotate", RunStateFailed, ""); err != nil {
		t.Fatal(err)
	}
	applied, err = AnnotateDurableRunError(context.Background(), sessionDir, "run-annotate", "abandoned after interrupted tool execution")
	if err != nil || !applied {
		t.Fatalf("annotate terminal run: applied=%v err=%v", applied, err)
	}
	run, err := session.GetSessionRun(sessionDir, "run-annotate")
	if err != nil || run == nil {
		t.Fatalf("load annotated run: %v", err)
	}
	if run.Status != "failed" || run.Error != "abandoned after interrupted tool execution" {
		t.Fatalf("annotated run = status %q error %q", run.Status, run.Error)
	}

	// A finalizer that already recorded a reason stays authoritative.
	applied, err = AnnotateDurableRunError(context.Background(), sessionDir, "run-annotate", "later reason")
	if err != nil || applied {
		t.Fatalf("second annotation: applied=%v err=%v", applied, err)
	}
	run, err = session.GetSessionRun(sessionDir, "run-annotate")
	if err != nil || run == nil {
		t.Fatalf("reload annotated run: %v", err)
	}
	if run.Error != "abandoned after interrupted tool execution" {
		t.Fatalf("annotation overwrote existing error: %q", run.Error)
	}

	applied, err = AnnotateDurableRunError(context.Background(), sessionDir, "missing-run", "reason")
	if err != nil || applied {
		t.Fatalf("missing run annotation: applied=%v err=%v", applied, err)
	}
}
