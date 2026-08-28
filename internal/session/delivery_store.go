package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/startvibecoding/mothx/internal/dao"
	"sort"
	"strings"
	"time"
)

var (
	ErrDeliveryLeaseLost       = errors.New("delivery operation lease was lost")
	ErrDeliveryOperationBusy   = errors.New("delivery operation is leased or not ready")
	ErrDeliveryOperationAbsent = errors.New("delivery operation was not found")
)

const defaultDeliveryLease = 30 * time.Second

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
	return WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		return createDeliveryPlanTx(ctx, tx, plan)
	})
}

func createDeliveryPlanTx(ctx context.Context, tx *dao.Tx, plan DeliveryPlan) error {
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
	if err == dao.ErrNoRows {
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
		var nextAttemptAt, leaseExpiresAt dao.NullInt64
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

// ClaimDeliveryOperation atomically claims the next due operation. The lease
// epoch is incremented on every claim, so a delayed worker cannot overwrite a
// later retry after its lease expires.
func ClaimDeliveryOperation(ctx context.Context, sessionDir, operationID, owner string, now time.Time, lease time.Duration) (*DeliveryOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationID = strings.TrimSpace(operationID)
	owner = strings.TrimSpace(owner)
	if operationID == "" || owner == "" {
		return nil, fmt.Errorf("delivery operation ID and owner are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if lease <= 0 {
		lease = defaultDeliveryLease
	}
	claimedUntil := now.Add(lease)
	var operation *DeliveryOperation
	err := WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		var intentStatus string
		var dependsOn string
		if err := tx.QueryRowContext(ctx, `SELECT i.status, COALESCE(o.depends_on, '')
			FROM delivery_operations o JOIN delivery_intents i ON i.id = o.intent_id
			WHERE o.id = ?`, operationID).Scan(&intentStatus, &dependsOn); err != nil {
			if err == dao.ErrNoRows {
				return ErrDeliveryOperationAbsent
			}
			return err
		}
		if intentStatus == "delivered" || intentStatus == "failed" || intentStatus == "cancelled" {
			return ErrDeliveryOperationBusy
		}
		nowMillis := now.UnixMilli()
		leaseMillis := claimedUntil.UnixMilli()
		query := `UPDATE delivery_operations SET lease_owner = ?, lease_epoch = lease_epoch + 1,
			lease_expires_at = ?, attempt_count = attempt_count + 1, updated_at = ?
			WHERE id = ? AND status IN ('pending', 'uploading', 'sending', 'retry_wait')
			AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
			AND (lease_owner = '' OR lease_expires_at IS NULL OR lease_expires_at <= ?)
			AND (? = '' OR EXISTS (SELECT 1 FROM delivery_operations dependency
				WHERE dependency.id = ? AND dependency.intent_id = delivery_operations.intent_id
				AND dependency.status IN ('uploaded', 'delivered', 'unsupported')))`
		result, err := tx.ExecContext(ctx, query, owner, leaseMillis, now.Format(time.RFC3339Nano), operationID, nowMillis, nowMillis, dependsOn, dependsOn)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return ErrDeliveryOperationBusy
		}
		loaded, err := scanDeliveryOperation(ctx, tx, operationID)
		if err != nil {
			return err
		}
		operation = loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return operation, nil
}

// UpdateDeliveryOperation applies a fenced provider result. Terminal updates
// are idempotent when a retry repeats the same result after an unknown commit.
func UpdateDeliveryOperation(ctx context.Context, sessionDir, operationID, owner string, epoch int64, status, providerAssetID, providerMessageID string, providerState json.RawMessage, failureCode string, nextAttemptAt *time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	operationID = strings.TrimSpace(operationID)
	owner = strings.TrimSpace(owner)
	status = strings.TrimSpace(status)
	if operationID == "" || owner == "" || epoch <= 0 {
		return fmt.Errorf("delivery operation identity and lease are required")
	}
	if !validDeliveryOperationStatus(status) {
		return fmt.Errorf("invalid delivery operation status %q", status)
	}
	providerState = normalizedRunJSON(providerState)
	updatedAt := time.Now().UTC()
	var next any
	if nextAttemptAt != nil {
		next = nextAttemptAt.UnixMilli()
	}
	err := WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		if status == "retry_wait" {
			if nextAttemptAt == nil {
				return fmt.Errorf("retry_wait requires next attempt time")
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE delivery_operations SET status = ?,
			provider_asset_id = ?, provider_message_id = ?, provider_state = ?, failure_code = ?,
			next_attempt_at = ?, lease_owner = '', lease_expires_at = NULL, updated_at = ?
			WHERE id = ? AND lease_owner = ? AND lease_epoch = ?`, status, strings.TrimSpace(providerAssetID),
			strings.TrimSpace(providerMessageID), string(providerState), strings.TrimSpace(failureCode), next,
			updatedAt.Format(time.RFC3339Nano), operationID, owner, epoch)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			return refreshDeliveryIntentStatusTx(ctx, tx, operationID, updatedAt)
		}
		var currentStatus, currentAsset, currentMessage, currentFailure, currentState string
		if err := tx.QueryRowContext(ctx, `SELECT status, provider_asset_id, provider_message_id, failure_code, provider_state FROM delivery_operations WHERE id = ?`, operationID).Scan(&currentStatus, &currentAsset, &currentMessage, &currentFailure, &currentState); err != nil {
			if err == dao.ErrNoRows {
				return ErrDeliveryOperationAbsent
			}
			return err
		}
		if currentStatus == status && (status == "uploaded" || status == "delivered" || status == "unsupported" || status == "failed" || status == "uncertain") && currentAsset == strings.TrimSpace(providerAssetID) && currentMessage == strings.TrimSpace(providerMessageID) && currentFailure == strings.TrimSpace(failureCode) && strings.TrimSpace(currentState) == strings.TrimSpace(string(providerState)) {
			return nil
		}
		return ErrDeliveryLeaseLost
	})
	return err
}

// UpdateDeliveryOperationProgress persists an in-flight provider phase while
// retaining the current lease. A subsequent terminal update must use the same
// owner and epoch, so a stale worker remains fenced throughout upload/send.
func UpdateDeliveryOperationProgress(ctx context.Context, sessionDir, operationID, owner string, epoch int64, status, providerAssetID, providerMessageID string, providerState json.RawMessage, failureCode string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	operationID = strings.TrimSpace(operationID)
	owner = strings.TrimSpace(owner)
	status = strings.TrimSpace(status)
	if operationID == "" || owner == "" || epoch <= 0 {
		return fmt.Errorf("delivery operation identity and lease are required")
	}
	if status != "uploading" && status != "sending" {
		return fmt.Errorf("invalid in-flight delivery operation status %q", status)
	}
	providerState = normalizedRunJSON(providerState)
	now := time.Now().UTC()
	return WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE delivery_operations SET status = ?,
			provider_asset_id = ?, provider_message_id = ?, provider_state = ?, failure_code = ?,
			updated_at = ? WHERE id = ? AND lease_owner = ? AND lease_epoch = ?`, status,
			strings.TrimSpace(providerAssetID), strings.TrimSpace(providerMessageID), string(providerState),
			strings.TrimSpace(failureCode), now.Format(time.RFC3339Nano), operationID, owner, epoch)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return ErrDeliveryLeaseLost
		}
		return refreshDeliveryIntentStatusTx(ctx, tx, operationID, now)
	})
}

// RequeueDeliveryOperation releases a fenced lease for an explicit retry. It
// is used by recovery when a provider call did not yield a trustworthy result.
func RequeueDeliveryOperation(ctx context.Context, sessionDir, operationID, owner string, epoch int64, nextAttemptAt time.Time, failureCode string) error {
	return UpdateDeliveryOperation(ctx, sessionDir, operationID, owner, epoch, "retry_wait", "", "", nil, failureCode, &nextAttemptAt)
}

// RefreshDeliveryIntentStatus recomputes the intent aggregate after an
// operation result. An intent is delivered only when all operations are
// delivered/unsupported; failed and uncertain operations remain visible.
func RefreshDeliveryIntentStatus(ctx context.Context, sessionDir, intentID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		return refreshDeliveryIntentStatusByIDTx(ctx, tx, intentID, time.Now().UTC())
	})
}

// ListDueDeliveryOperations returns recoverable operations whose retry time is
// due or whose previous worker lease has expired. It is intentionally a read;
// callers must claim each row through ClaimDeliveryOperation before executing.
func ListDueDeliveryOperations(ctx context.Context, sessionDir string, now time.Time) ([]DeliveryOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM delivery_operations
		WHERE status IN ('pending', 'uploading', 'sending', 'retry_wait')
		AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		AND (lease_owner = '' OR lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY sequence ASC, created_at ASC`, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	var operationIDs []string
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			rows.Close()
			return nil, err
		}
		operationIDs = append(operationIDs, operationID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var operations []DeliveryOperation
	for _, operationID := range operationIDs {
		operation, err := GetDeliveryOperation(ctx, sessionDir, operationID)
		if err != nil {
			return nil, err
		}
		if operation != nil {
			operations = append(operations, *operation)
		}
	}
	return operations, nil
}

// GetDeliveryOperation loads one operation without exposing transport
// credentials or requiring callers to know its parent intent ID.
func GetDeliveryOperation(ctx context.Context, sessionDir, operationID string) (*DeliveryOperation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var operation DeliveryOperation
	var providerState, created, updated string
	var nextAttemptAt, leaseExpiresAt dao.NullInt64
	err = db.QueryRowContext(ctx, `SELECT id, intent_id, operation_key, COALESCE(artifact_id, ''), operation_kind,
		sequence, COALESCE(depends_on, ''), idempotency_key, payload_digest, status,
		provider_asset_id, provider_message_id, provider_state, attempt_count,
		next_attempt_at, failure_code, lease_owner, lease_epoch, lease_expires_at, created_at, updated_at
		FROM delivery_operations WHERE id = ?`, operationID).Scan(&operation.ID, &operation.IntentID, &operation.OperationKey, &operation.ArtifactID,
		&operation.OperationKind, &operation.Sequence, &operation.DependsOn, &operation.IdempotencyKey,
		&operation.PayloadDigest, &operation.Status, &operation.ProviderAssetID, &operation.ProviderMessageID,
		&providerState, &operation.AttemptCount, &nextAttemptAt, &operation.FailureCode, &operation.LeaseOwner,
		&operation.LeaseEpoch, &leaseExpiresAt, &created, &updated)
	if err == dao.ErrNoRows {
		return nil, ErrDeliveryOperationAbsent
	}
	if err != nil {
		return nil, err
	}
	operation.ProviderState = json.RawMessage(providerState)
	operation.CreatedAt, operation.UpdatedAt = parseSessionTimestamp(created), parseSessionTimestamp(updated)
	if nextAttemptAt.Valid {
		value := time.UnixMilli(nextAttemptAt.Int64)
		operation.NextAttemptAt = &value
	}
	if leaseExpiresAt.Valid {
		value := time.UnixMilli(leaseExpiresAt.Int64)
		operation.LeaseExpiresAt = &value
	}
	return &operation, nil
}

func refreshDeliveryIntentStatusTx(ctx context.Context, tx *dao.Tx, operationID string, now time.Time) error {
	var intentID string
	if err := tx.QueryRowContext(ctx, `SELECT intent_id FROM delivery_operations WHERE id = ?`, operationID).Scan(&intentID); err != nil {
		return err
	}
	return refreshDeliveryIntentStatusByIDTx(ctx, tx, intentID, now)
}

func refreshDeliveryIntentStatusByIDTx(ctx context.Context, tx *dao.Tx, intentID string, now time.Time) error {
	// A terminal prerequisite can never make its dependent operation valid.
	// Resolve that dependent in the same transaction so a permanently failed
	// upload or uncertain send cannot leave an orphaned pending operation.
	for {
		result, err := tx.ExecContext(ctx, `UPDATE delivery_operations
			SET status = CASE dependency.status WHEN 'uncertain' THEN 'uncertain' ELSE 'failed' END,
				failure_code = CASE dependency.status WHEN 'uncertain' THEN 'dependency_uncertain' ELSE 'dependency_failed' END,
				next_attempt_at = NULL, lease_owner = '', lease_expires_at = NULL, updated_at = ?
			FROM delivery_operations dependency
			WHERE delivery_operations.intent_id = ?
			  AND delivery_operations.depends_on = dependency.id
			  AND dependency.intent_id = delivery_operations.intent_id
			  AND dependency.status IN ('failed', 'uncertain')
			  AND delivery_operations.status IN ('pending', 'retry_wait')`, now.Format(time.RFC3339Nano), intentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			break
		}
	}
	var total, terminal, failed, uncertain int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN status IN ('uploaded', 'delivered', 'unsupported') THEN 1 ELSE 0 END), SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), SUM(CASE WHEN status = 'uncertain' THEN 1 ELSE 0 END) FROM delivery_operations WHERE intent_id = ?`, intentID).Scan(&total, &terminal, &failed, &uncertain); err != nil {
		return err
	}
	resolved := terminal + failed + uncertain
	status := "pending"
	switch {
	case total > 0 && terminal == total:
		status = "delivered"
	case failed > 0 && resolved == total:
		status = "failed"
	case uncertain > 0 && resolved == total:
		status = "uncertain"
	}
	_, err := tx.ExecContext(ctx, `UPDATE delivery_intents SET status = ?, updated_at = ? WHERE id = ?`, status, now.Format(time.RFC3339Nano), intentID)
	return err
}

func validDeliveryOperationStatus(status string) bool {
	switch status {
	case "pending", "uploading", "uploaded", "sending", "retry_wait", "delivered", "unsupported", "failed", "uncertain":
		return true
	default:
		return false
	}
}

func scanDeliveryOperation(ctx context.Context, tx *dao.Tx, operationID string) (*DeliveryOperation, error) {
	var operation DeliveryOperation
	var providerState, created, updated string
	var nextAttemptAt, leaseExpiresAt dao.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id, intent_id, operation_key, COALESCE(artifact_id, ''), operation_kind,
		sequence, COALESCE(depends_on, ''), idempotency_key, payload_digest, status,
		provider_asset_id, provider_message_id, provider_state, attempt_count,
		next_attempt_at, failure_code, lease_owner, lease_epoch, lease_expires_at, created_at, updated_at
		FROM delivery_operations WHERE id = ?`, operationID).Scan(&operation.ID, &operation.IntentID, &operation.OperationKey, &operation.ArtifactID,
		&operation.OperationKind, &operation.Sequence, &operation.DependsOn, &operation.IdempotencyKey,
		&operation.PayloadDigest, &operation.Status, &operation.ProviderAssetID, &operation.ProviderMessageID,
		&providerState, &operation.AttemptCount, &nextAttemptAt, &operation.FailureCode, &operation.LeaseOwner,
		&operation.LeaseEpoch, &leaseExpiresAt, &created, &updated)
	if err != nil {
		return nil, err
	}
	operation.ProviderState = json.RawMessage(providerState)
	operation.CreatedAt, operation.UpdatedAt = parseSessionTimestamp(created), parseSessionTimestamp(updated)
	if nextAttemptAt.Valid {
		value := time.UnixMilli(nextAttemptAt.Int64)
		operation.NextAttemptAt = &value
	}
	if leaseExpiresAt.Valid {
		value := time.UnixMilli(leaseExpiresAt.Int64)
		operation.LeaseExpiresAt = &value
	}
	return &operation, nil
}
