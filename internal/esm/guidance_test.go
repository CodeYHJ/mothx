package esm

import (
	"context"
	"strings"
	"testing"
)

func TestStoreAddGuidanceStampsObjectiveVersion(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	obj, err := store.Create(ctx, sessionID, "finish the objective")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGuidance(ctx, sessionID, "focus on failing tests"); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingGuidance(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Guidance != "focus on failing tests" || pending[0].Status != "pending" {
		t.Fatalf("pending = %#v", pending)
	}
	if pending[0].ObjectiveVersion != formatTime(obj.UpdatedAt) {
		t.Fatalf("version = %q, want %q", pending[0].ObjectiveVersion, formatTime(obj.UpdatedAt))
	}
	if _, err := store.AddGuidance(ctx, sessionID, "   "); err == nil {
		t.Fatal("empty guidance should fail")
	}
	if _, err := store.AddGuidance(ctx, "missing-session", "text"); err == nil {
		t.Fatal("guidance without an objective should fail")
	}
}

func TestSupervisorInjectsAndConsumesGuidanceForWorker(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGuidance(ctx, sessionID, "prioritize the failing tests"); err != nil {
		t.Fatal(err)
	}

	adapter := &runtimeTestAdapter{
		responses: map[Role]string{RoleWorker: `{"status":"continue","summary":"progress","evidence":["inspection"],"remaining_work":["finish"],"blockers":[]}`},
		prompts:   map[Role]string{},
	}
	obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-guidance", rootForRuntimeTest(t), "yolo")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != StatusActive {
		t.Fatalf("status = %s, want active", obj.Status)
	}

	prompt := adapter.prompts[RoleWorker]
	if !strings.Contains(prompt, "User guidance queued for this objective") || !strings.Contains(prompt, "prioritize the failing tests") {
		t.Fatalf("worker prompt missing guidance:\n%s", prompt)
	}
	pending, err := store.PendingGuidance(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("guidance not consumed after successful role: %#v", pending)
	}
}

func TestSupervisorKeepsGuidanceWhenRoleFails(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGuidance(ctx, sessionID, "prioritize the failing tests"); err != nil {
		t.Fatal(err)
	}

	adapter := &runtimeTestAdapter{roleErr: context.Canceled, prompts: map[Role]string{}}
	if _, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-fail", rootForRuntimeTest(t), "yolo"); err == nil {
		t.Fatal("Run should surface the role failure")
	}
	pending, err := store.PendingGuidance(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("guidance should stay pending after a failed role: %#v", pending)
	}
}
