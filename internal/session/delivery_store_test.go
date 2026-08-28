package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func deliveryFixture(t *testing.T) (string, string) {
	t.Helper()
	sessionDir := t.TempDir()
	mgr := New(t.TempDir(), sessionDir)
	if err := mgr.InitWithID("delivery-session"); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	if err := CreateSessionRun(sessionDir, SessionRun{ID: "delivery-run", SessionID: mgr.GetHeader().ID, Status: "completed", StartedAt: started, UpdatedAt: started, FinishedAt: &started}); err != nil {
		t.Fatal(err)
	}
	return sessionDir, mgr.GetHeader().ID
}

func createDeliveryFixturePlan(t *testing.T, sessionDir, sessionID string) DeliveryPlan {
	t.Helper()
	now := time.Now().UTC()
	plan := DeliveryPlan{Intent: DeliveryIntent{ID: "delivery-intent", SessionID: sessionID, RunID: "delivery-run", Platform: "wechat", TargetID: "chat", Status: "pending", CreatedAt: now, UpdatedAt: now, TransportContext: json.RawMessage(`{"caption":"hello"}`)}, Operations: []DeliveryOperation{
		{ID: "delivery-op-caption", OperationKey: "caption", OperationKind: "send_text", Sequence: 1, IdempotencyKey: "delivery-op-caption", PayloadDigest: "sha256:caption", Status: "pending", CreatedAt: now, UpdatedAt: now},
		{ID: "delivery-op-file", OperationKey: "file", OperationKind: "send_artifact", Sequence: 2, DependsOn: "delivery-op-caption", IdempotencyKey: "delivery-op-file", PayloadDigest: "sha256:file", Status: "pending", CreatedAt: now, UpdatedAt: now},
	}}
	if err := CreateDeliveryPlan(context.Background(), sessionDir, plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDeliveryClaimFencesExpiredWorkerAndHonorsDependency(t *testing.T) {
	sessionDir, sessionID := deliveryFixture(t)
	plan := createDeliveryFixturePlan(t, sessionDir, sessionID)
	now := time.Now().UTC()
	if _, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[1].ID, "worker-a", now, time.Minute); !errors.Is(err, ErrDeliveryOperationBusy) {
		t.Fatalf("dependency claim error = %v, want busy", err)
	}
	claimed, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.LeaseOwner != "worker-a" || claimed.LeaseEpoch != 1 || claimed.AttemptCount != 1 {
		t.Fatalf("claim = %#v", claimed)
	}
	if _, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-b", now, time.Minute); !errors.Is(err, ErrDeliveryOperationBusy) {
		t.Fatalf("second claim error = %v, want busy", err)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, claimed.ID, "worker-b", claimed.LeaseEpoch, "delivered", "", "msg-a", nil, "", nil); !errors.Is(err, ErrDeliveryLeaseLost) {
		t.Fatalf("wrong owner update = %v, want lease lost", err)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, claimed.ID, "worker-a", claimed.LeaseEpoch, "delivered", "", "msg-a", nil, "", nil); err != nil {
		t.Fatal(err)
	}
	dependent, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[1].ID, "worker-b", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if dependent.LeaseEpoch != 1 {
		t.Fatalf("dependent claim epoch = %d", dependent.LeaseEpoch)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, dependent.ID, "worker-b", dependent.LeaseEpoch, "retry_wait", "", "", nil, "timeout", ptrTime(now.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := QueryRootDatabase(sessionDir, func(db *sql.DB) error {
		return db.QueryRow(`SELECT status FROM delivery_intents WHERE id = ?`, plan.Intent.ID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("intent status = %q, want pending", status)
	}
}

func TestDeliveryClaimCanRecoverExpiredLease(t *testing.T) {
	sessionDir, sessionID := deliveryFixture(t)
	plan := createDeliveryFixturePlan(t, sessionDir, sessionID)
	first, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", time.Now().UTC().Add(-time.Minute), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-b", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if second.LeaseEpoch != first.LeaseEpoch+1 || second.LeaseOwner != "worker-b" {
		t.Fatalf("recovered claim = %#v, first = %#v", second, first)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", first.LeaseEpoch, "delivered", "", "stale", nil, "", nil); !errors.Is(err, ErrDeliveryLeaseLost) {
		t.Fatalf("stale worker update = %v, want lease lost", err)
	}
}

func TestUploadedPhaseCountsAsTerminalAfterDependentSend(t *testing.T) {
	sessionDir, sessionID := deliveryFixture(t)
	plan := createDeliveryFixturePlan(t, sessionDir, sessionID)
	now := time.Now().UTC()
	upload, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state := json.RawMessage(`{"provider_asset_id":"asset-1"}`)
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, upload.ID, "worker-a", upload.LeaseEpoch, "uploaded", "asset-1", "", state, "", nil); err != nil {
		t.Fatal(err)
	}
	send, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[1].ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, send.ID, "worker-a", send.LeaseEpoch, "delivered", "asset-1", "message-1", state, "", nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := GetDeliveryPlan(t.Context(), sessionDir, plan.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Intent.Status != "delivered" {
		t.Fatalf("intent status = %q, want delivered", loaded.Intent.Status)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, upload.ID, "worker-a", upload.LeaseEpoch, "uploaded", "asset-1", "", state, "", nil); err != nil {
		t.Fatalf("idempotent uploaded update = %v", err)
	}
}

func TestDeliveryFailureCascadesToDependentOperation(t *testing.T) {
	sessionDir, sessionID := deliveryFixture(t)
	plan := createDeliveryFixturePlan(t, sessionDir, sessionID)
	now := time.Now().UTC()
	upload, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, upload.ID, "worker-a", upload.LeaseEpoch, "failed", "", "", nil, "provider_rejected", nil); err != nil {
		t.Fatal(err)
	}
	dependent, err := GetDeliveryOperation(t.Context(), sessionDir, plan.Operations[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if dependent.Status != "failed" || dependent.FailureCode != "dependency_failed" {
		t.Fatalf("dependent after failed prerequisite = %#v", dependent)
	}
	intent, err := GetDeliveryPlan(t.Context(), sessionDir, plan.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Intent.Status != "failed" {
		t.Fatalf("intent after dependency failure = %q, want failed", intent.Intent.Status)
	}
}

func TestUncertainDeliveryCascadesUncertainDependent(t *testing.T) {
	sessionDir, sessionID := deliveryFixture(t)
	plan := createDeliveryFixturePlan(t, sessionDir, sessionID)
	now := time.Now().UTC()
	upload, err := ClaimDeliveryOperation(t.Context(), sessionDir, plan.Operations[0].ID, "worker-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateDeliveryOperation(t.Context(), sessionDir, upload.ID, "worker-a", upload.LeaseEpoch, "uncertain", "", "", nil, "provider_timeout", nil); err != nil {
		t.Fatal(err)
	}
	dependent, err := GetDeliveryOperation(t.Context(), sessionDir, plan.Operations[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if dependent.Status != "uncertain" || dependent.FailureCode != "dependency_uncertain" {
		t.Fatalf("dependent after uncertain prerequisite = %#v", dependent)
	}
	intent, err := GetDeliveryPlan(t.Context(), sessionDir, plan.Intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Intent.Status != "uncertain" {
		t.Fatalf("intent after uncertain dependency = %q, want uncertain", intent.Intent.Status)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
