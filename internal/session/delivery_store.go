package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DeliveryIntent is the run-level durable outbox identity. TransportContext
// is opaque Runtime-owned state and must never be projected into prompts or
// ordinary logs.
type DeliveryIntent struct {
	ID               string
	SessionID        string
	RunID            string
	Platform         string
	TargetID         string
	ReplyMessageID   string
	TransportContext json.RawMessage
	Status           string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// DeliveryOperation is one ordered, independently recoverable outbox step.
type DeliveryOperation struct {
	ID                string
	IntentID          string
	OperationKey      string
	ArtifactID        string
	OperationKind     string
	Sequence          int
	DependsOn         string
	IdempotencyKey    string
	PayloadDigest     string
	Status            string
	ProviderAssetID   string
	ProviderMessageID string
	ProviderState     json.RawMessage
	AttemptCount      int
	NextAttemptAt     *time.Time
	FailureCode       string
	LeaseOwner        string
	LeaseEpoch        int64
	LeaseExpiresAt    *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DeliveryPlan is created in the same transaction that terminalizes its Run.
type DeliveryPlan struct {
	Intent     DeliveryIntent
	Operations []DeliveryOperation
}

// CreateDeliveryPlan persists a plan outside terminalization only for
// recovery reconciliation and focused tools. Normal execution must attach the
// plan to FinishSessionRunAndConversationTurn instead.
func CreateDeliveryPlan(ctx context.Context, sessionDir string, plan DeliveryPlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return WriteRootDatabase(ctx, sessionDir, func(tx *sql.Tx) error {
		return createDeliveryPlanTx(ctx, tx, plan)
	})
}

func createDeliveryPlanTx(ctx context.Context, tx *sql.Tx, plan DeliveryPlan) error {
	intent := plan.Intent
	intent.ID = strings.TrimSpace(intent.ID)
	intent.SessionID = strings.TrimSpace(intent.SessionID)
	intent.RunID = strings.TrimSpace(intent.RunID)
	intent.Platform = strings.TrimSpace(intent.Platform)
	if intent.ID == "" || intent.SessionID == "" || intent.RunID == "" || intent.Platform == "" {
		return fmt.Errorf("delivery intent identity, session, run, and platform are required")
	}
	if len(plan.Operations) == 0 {
		return fmt.Errorf("delivery intent requires at least one operation")
	}
	if intent.Status == "" {
		intent.Status = "pending"
	}
	if intent.Status != "pending" {
		return fmt.Errorf("new delivery intent status must be pending")
	}
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = time.Now().UTC()
	}
	if intent.UpdatedAt.IsZero() {
		intent.UpdatedAt = intent.CreatedAt
	}
	intent.TransportContext = normalizedRunJSON(intent.TransportContext)
	var runCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_runs WHERE id = ? AND session_id = ?`, intent.RunID, intent.SessionID).Scan(&runCount); err != nil {
		return err
	}
	if runCount != 1 {
		return fmt.Errorf("delivery Run %s does not belong to session", intent.RunID)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO delivery_intents
		(id, session_id, run_id, platform, target_id, reply_message_id, transport_context, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, platform, target_id) DO NOTHING`,
		intent.ID, intent.SessionID, intent.RunID, intent.Platform, intent.TargetID, intent.ReplyMessageID,
		string(intent.TransportContext), intent.Status, intent.CreatedAt.Format(time.RFC3339Nano), intent.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("create delivery intent: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var existing DeliveryIntent
		var transport, created, updated string
		if err := tx.QueryRowContext(ctx, `SELECT id, session_id, run_id, platform, target_id, reply_message_id,
			transport_context, status, created_at, updated_at FROM delivery_intents
			WHERE run_id = ? AND platform = ? AND target_id = ?`, intent.RunID, intent.Platform, intent.TargetID).Scan(
			&existing.ID, &existing.SessionID, &existing.RunID, &existing.Platform, &existing.TargetID,
			&existing.ReplyMessageID, &transport, &existing.Status, &created, &updated); err != nil {
			return err
		}
		if existing.ID != intent.ID || existing.SessionID != intent.SessionID || existing.ReplyMessageID != intent.ReplyMessageID ||
			strings.TrimSpace(transport) != strings.TrimSpace(string(intent.TransportContext)) {
			return fmt.Errorf("delivery intent conflicts with existing Run projection")
		}
	}

	operations := append([]DeliveryOperation(nil), plan.Operations...)
	sort.SliceStable(operations, func(i, j int) bool { return operations[i].Sequence < operations[j].Sequence })
	seenKeys := make(map[string]struct{}, len(operations))
	seenSequences := make(map[int]struct{}, len(operations))
	seenIDs := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		operation.IntentID = intent.ID
		operation.ID = strings.TrimSpace(operation.ID)
		operation.OperationKey = strings.TrimSpace(operation.OperationKey)
		operation.OperationKind = strings.TrimSpace(operation.OperationKind)
		operation.IdempotencyKey = strings.TrimSpace(operation.IdempotencyKey)
		operation.PayloadDigest = strings.TrimSpace(operation.PayloadDigest)
		if operation.ID == "" || operation.OperationKey == "" || operation.OperationKind == "" || operation.Sequence <= 0 || operation.IdempotencyKey == "" || operation.PayloadDigest == "" {
			return fmt.Errorf("delivery operation identity, key, kind, sequence, idempotency key, and digest are required")
		}
		if _, exists := seenKeys[operation.OperationKey]; exists {
			return fmt.Errorf("duplicate delivery operation key %q", operation.OperationKey)
		}
		if _, exists := seenSequences[operation.Sequence]; exists {
			return fmt.Errorf("duplicate delivery operation sequence %d", operation.Sequence)
		}
		if operation.DependsOn != "" {
			if _, exists := seenIDs[operation.DependsOn]; !exists {
				return fmt.Errorf("delivery operation %s depends on a missing or later operation", operation.ID)
			}
		}
		seenKeys[operation.OperationKey] = struct{}{}
		seenSequences[operation.Sequence] = struct{}{}
		seenIDs[operation.ID] = struct{}{}
		if operation.ArtifactID != "" {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_attachments WHERE id = ? AND session_id = ? AND run_id = ?`, operation.ArtifactID, intent.SessionID, intent.RunID).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("delivery artifact %s does not belong to Run", operation.ArtifactID)
			}
		}
		if operation.Status == "" {
			operation.Status = "pending"
		}
		if operation.Status != "pending" && operation.Status != "unsupported" {
			return fmt.Errorf("new delivery operation status must be pending or unsupported")
		}
		if operation.CreatedAt.IsZero() {
			operation.CreatedAt = intent.CreatedAt
		}
		if operation.UpdatedAt.IsZero() {
			operation.UpdatedAt = operation.CreatedAt
		}
		operation.ProviderState = normalizedRunJSON(operation.ProviderState)
		var artifactID, dependsOn any
		if operation.ArtifactID != "" {
			artifactID = operation.ArtifactID
		}
		if operation.DependsOn != "" {
			dependsOn = operation.DependsOn
		}
		insert, err := tx.ExecContext(ctx, `INSERT INTO delivery_operations
			(id, intent_id, operation_key, artifact_id, operation_kind, sequence, depends_on,
			 idempotency_key, payload_digest, status, provider_asset_id, provider_message_id,
			 provider_state, attempt_count, next_attempt_at, failure_code, lease_owner,
			 lease_epoch, lease_expires_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, 0, NULL, '', '', 0, NULL, ?, ?)
			ON CONFLICT(intent_id, operation_key) DO NOTHING`,
			operation.ID, intent.ID, operation.OperationKey, artifactID, operation.OperationKind,
			operation.Sequence, dependsOn, operation.IdempotencyKey, operation.PayloadDigest,
			operation.Status, string(operation.ProviderState), operation.CreatedAt.Format(time.RFC3339Nano), operation.UpdatedAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("create delivery operation %s: %w", operation.OperationKey, err)
		}
		if changed, _ := insert.RowsAffected(); changed == 0 {
			var existingID, existingArtifact, existingKind, existingDepends, existingIdempotency, existingDigest string
			var existingSequence int
			if err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(artifact_id, ''), operation_kind, sequence,
				COALESCE(depends_on, ''), idempotency_key, payload_digest FROM delivery_operations
				WHERE intent_id = ? AND operation_key = ?`, intent.ID, operation.OperationKey).Scan(
				&existingID, &existingArtifact, &existingKind, &existingSequence, &existingDepends,
				&existingIdempotency, &existingDigest); err != nil {
				return err
			}
			if existingID != operation.ID || existingArtifact != operation.ArtifactID || existingKind != operation.OperationKind ||
				existingSequence != operation.Sequence || existingDepends != operation.DependsOn ||
				existingIdempotency != operation.IdempotencyKey || existingDigest != operation.PayloadDigest {
				return fmt.Errorf("delivery operation %q conflicts with existing projection", operation.OperationKey)
			}
		}
	}
	return nil
}

// GetDeliveryPlan loads one intent and its operations in execution order.
func GetDeliveryPlan(ctx context.Context, sessionDir, intentID string) (*DeliveryPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var plan DeliveryPlan
	var transport, created, updated string
	err = db.QueryRowContext(ctx, `SELECT id, session_id, run_id, platform, target_id, reply_message_id,
		transport_context, status, created_at, updated_at FROM delivery_intents WHERE id = ?`, intentID).Scan(
		&plan.Intent.ID, &plan.Intent.SessionID, &plan.Intent.RunID, &plan.Intent.Platform,
		&plan.Intent.TargetID, &plan.Intent.ReplyMessageID, &transport, &plan.Intent.Status, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan.Intent.TransportContext = json.RawMessage(transport)
	plan.Intent.CreatedAt, plan.Intent.UpdatedAt = parseSessionTimestamp(created), parseSessionTimestamp(updated)
	rows, err := db.QueryContext(ctx, `SELECT id, intent_id, operation_key, COALESCE(artifact_id, ''), operation_kind,
		sequence, COALESCE(depends_on, ''), idempotency_key, payload_digest, status,
		provider_asset_id, provider_message_id, provider_state, attempt_count,
		next_attempt_at, failure_code, lease_owner, lease_epoch, lease_expires_at, created_at, updated_at
		FROM delivery_operations WHERE intent_id = ? ORDER BY sequence ASC`, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var operation DeliveryOperation
		var providerState, opCreated, opUpdated string
		var nextAttemptAt, leaseExpiresAt sql.NullInt64
		if err := rows.Scan(&operation.ID, &operation.IntentID, &operation.OperationKey, &operation.ArtifactID,
			&operation.OperationKind, &operation.Sequence, &operation.DependsOn, &operation.IdempotencyKey,
			&operation.PayloadDigest, &operation.Status, &operation.ProviderAssetID, &operation.ProviderMessageID,
			&providerState, &operation.AttemptCount, &nextAttemptAt, &operation.FailureCode, &operation.LeaseOwner,
			&operation.LeaseEpoch, &leaseExpiresAt, &opCreated, &opUpdated); err != nil {
			return nil, err
		}
		operation.ProviderState = json.RawMessage(providerState)
		operation.CreatedAt, operation.UpdatedAt = parseSessionTimestamp(opCreated), parseSessionTimestamp(opUpdated)
		if nextAttemptAt.Valid {
			value := time.UnixMilli(nextAttemptAt.Int64)
			operation.NextAttemptAt = &value
		}
		if leaseExpiresAt.Valid {
			value := time.UnixMilli(leaseExpiresAt.Int64)
			operation.LeaseExpiresAt = &value
		}
		plan.Operations = append(plan.Operations, operation)
	}
	return &plan, rows.Err()
}
