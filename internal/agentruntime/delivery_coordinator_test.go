package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestDeliveryCoordinatorReconcilesDueOperationAndBoundsRetries(t *testing.T) {
	sessionDir, sessionID := deliveryCoordinatorFixture(t)
	now := time.Now().UTC()
	plan := session.DeliveryPlan{Intent: session.DeliveryIntent{ID: "coord-intent", SessionID: sessionID, RunID: "coord-run", Platform: "wechat", TargetID: "chat", Status: "pending", CreatedAt: now, UpdatedAt: now}, Operations: []session.DeliveryOperation{{ID: "coord-op", IntentID: "coord-intent", OperationKey: "caption", OperationKind: "send_text", Sequence: 1, IdempotencyKey: "coord-op", PayloadDigest: "sha256:x", Status: "pending", CreatedAt: now, UpdatedAt: now}}}
	if err := session.CreateDeliveryPlan(t.Context(), sessionDir, plan); err != nil {
		t.Fatal(err)
	}
	coordinator := NewDeliveryCoordinator(sessionDir, "coord-worker")
	processed, err := coordinator.ReconcileDue(t.Context(), now, func(context.Context, session.DeliveryOperation) (DeliveryResult, error) {
		return DeliveryResult{}, errors.New("provider unavailable")
	})
	if err != nil || processed != 1 {
		t.Fatalf("reconcile = %d, %v", processed, err)
	}
	op, err := session.GetDeliveryOperation(t.Context(), sessionDir, "coord-op")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "retry_wait" || op.AttemptCount != 1 || op.FailureCode != "transport_error" || op.NextAttemptAt == nil {
		t.Fatalf("operation after retry = %#v", op)
	}
}

func TestDeliveryCoordinatorPreservesUncertainResult(t *testing.T) {
	sessionDir, sessionID := deliveryCoordinatorFixture(t)
	now := time.Now().UTC()
	plan := session.DeliveryPlan{Intent: session.DeliveryIntent{ID: "uncertain-intent", SessionID: sessionID, RunID: "coord-run", Platform: "wechat", TargetID: "chat", Status: "pending", CreatedAt: now, UpdatedAt: now}, Operations: []session.DeliveryOperation{{ID: "uncertain-op", IntentID: "uncertain-intent", OperationKey: "caption", OperationKind: "send_text", Sequence: 1, IdempotencyKey: "uncertain-op", PayloadDigest: "sha256:x", Status: "pending", CreatedAt: now, UpdatedAt: now}}}
	if err := session.CreateDeliveryPlan(t.Context(), sessionDir, plan); err != nil {
		t.Fatal(err)
	}
	coordinator := NewDeliveryCoordinator(sessionDir, "uncertain-worker")
	if _, err := coordinator.ReconcileDue(t.Context(), now, func(context.Context, session.DeliveryOperation) (DeliveryResult, error) {
		return DeliveryResult{Status: "uncertain", FailureCode: "provider_timeout"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	op, err := session.GetDeliveryOperation(t.Context(), sessionDir, "uncertain-op")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "uncertain" || op.FailureCode != "provider_timeout" {
		t.Fatalf("uncertain operation = %#v", op)
	}
}

func TestDeliveryCoordinatorAppliesRetryLimitToProviderRetryResult(t *testing.T) {
	sessionDir, sessionID := deliveryCoordinatorFixture(t)
	now := time.Now().UTC()
	plan := session.DeliveryPlan{Intent: session.DeliveryIntent{ID: "retry-limit-intent", SessionID: sessionID, RunID: "coord-run", Platform: "feishu", TargetID: "chat", Status: "pending", CreatedAt: now, UpdatedAt: now}, Operations: []session.DeliveryOperation{{ID: "retry-limit-op", IntentID: "retry-limit-intent", OperationKey: "upload", OperationKind: "upload_artifact", Sequence: 1, IdempotencyKey: "retry-limit-op", PayloadDigest: "sha256:x", Status: "pending", ProviderAssetID: "asset-before", ProviderState: []byte(`{"checkpoint":"before"}`), CreatedAt: now, UpdatedAt: now}}}
	if err := session.CreateDeliveryPlan(t.Context(), sessionDir, plan); err != nil {
		t.Fatal(err)
	}
	coordinator := NewDeliveryCoordinator(sessionDir, "retry-limit-worker")
	coordinator.MaxRetries = 1
	processed, err := coordinator.ReconcileDue(t.Context(), now, func(context.Context, session.DeliveryOperation) (DeliveryResult, error) {
		return DeliveryResult{Status: "retry_wait"}, nil
	})
	if err != nil || processed != 1 {
		t.Fatalf("reconcile = %d, %v", processed, err)
	}
	op, err := session.GetDeliveryOperation(t.Context(), sessionDir, "retry-limit-op")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "failed" || op.FailureCode != "delivery_retries_exhausted" || string(op.ProviderState) != `{"checkpoint":"before"}` {
		t.Fatalf("retry-limited operation = %#v", op)
	}
}

func TestDeliveryCoordinatorPreservesCheckpointOnExecutorError(t *testing.T) {
	sessionDir, sessionID := deliveryCoordinatorFixture(t)
	now := time.Now().UTC()
	plan := session.DeliveryPlan{Intent: session.DeliveryIntent{ID: "error-checkpoint-intent", SessionID: sessionID, RunID: "coord-run", Platform: "wechat", TargetID: "chat", Status: "pending", CreatedAt: now, UpdatedAt: now}, Operations: []session.DeliveryOperation{{ID: "error-checkpoint-op", IntentID: "error-checkpoint-intent", OperationKey: "upload", OperationKind: "upload_artifact", Sequence: 1, IdempotencyKey: "error-checkpoint-op", PayloadDigest: "sha256:x", Status: "pending", ProviderAssetID: "asset-before", ProviderState: []byte(`{"checkpoint":"before"}`), CreatedAt: now, UpdatedAt: now}}}
	if err := session.CreateDeliveryPlan(t.Context(), sessionDir, plan); err != nil {
		t.Fatal(err)
	}
	coordinator := NewDeliveryCoordinator(sessionDir, "error-checkpoint-worker")
	if _, err := coordinator.ReconcileDue(t.Context(), now, func(context.Context, session.DeliveryOperation) (DeliveryResult, error) {
		return DeliveryResult{ProviderAssetID: "asset-after", ProviderState: []byte(`{"checkpoint":"after"}`)}, errors.New("provider response lost")
	}); err != nil {
		t.Fatal(err)
	}
	op, err := session.GetDeliveryOperation(t.Context(), sessionDir, "error-checkpoint-op")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "retry_wait" || op.ProviderAssetID != "asset-after" || string(op.ProviderState) != `{"checkpoint":"after"}` {
		t.Fatalf("error checkpoint = %#v", op)
	}
}

func TestPlanDeliveryFallbackStaysAfterCaptionWhenMediaAlsoExists(t *testing.T) {
	now := time.Now().UTC()
	plan, fallback, err := PlanDelivery(DeliveryPlanRequest{
		SessionID: "session", RunID: "run", Platform: "feishu", TargetID: "chat", Caption: "summary",
		CreatedAt: now, Capability: DeliveryCapability{Text: true, SendImage: true},
		Attachments: []SessionAttachment{
			{ID: "native-image", SessionID: "session", RunID: "run", Kind: AttachmentImage, Filename: "screen.png", SHA256: "image-hash"},
			{ID: "unsupported-audio", SessionID: "session", RunID: "run", Kind: AttachmentAudio, Filename: "voice.amr", SHA256: "audio-hash"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fallback == "" || len(plan.Operations) != 4 {
		t.Fatalf("plan operations = %#v, fallback=%q", plan.Operations, fallback)
	}
	captionID := plan.Operations[0].ID
	if plan.Operations[0].OperationKind != "send_text" || plan.Operations[3].OperationKind != "send_fallback_text" {
		t.Fatalf("operation kinds = %#v", plan.Operations)
	}
	if plan.Operations[3].DependsOn != captionID {
		t.Fatalf("fallback dependency = %q, want caption %q", plan.Operations[3].DependsOn, captionID)
	}
}

func deliveryCoordinatorFixture(t *testing.T) (string, string) {
	t.Helper()
	sessionDir := t.TempDir()
	mgr := session.New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("coord-session"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := session.CreateSessionRun(sessionDir, session.SessionRun{ID: "coord-run", SessionID: mgr.GetHeader().ID, Status: "completed", StartedAt: now, UpdatedAt: now, FinishedAt: &now}); err != nil {
		t.Fatal(err)
	}
	return sessionDir, mgr.GetHeader().ID
}
