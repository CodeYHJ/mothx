package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// DeliveryResult is the transport-neutral outcome reported by an adapter
// after it has used a claimed operation. Unknown provider outcomes must use
// Uncertain so recovery never blindly duplicates a possibly delivered message.
type DeliveryResult struct {
	Status            string
	ProviderAssetID   string
	ProviderMessageID string
	ProviderState     json.RawMessage
	FailureCode       string
	NextAttemptAt     *time.Time
}

// DeliveryExecutor performs one platform operation after the Runtime has
// claimed it. It may upload/send through a platform SDK but cannot write
// delivery rows directly.
type DeliveryExecutor func(context.Context, session.DeliveryOperation) (DeliveryResult, error)

// DeliveryCoordinator is the Runtime-owned claim/fence/retry boundary for
// durable delivery outbox operations.
type DeliveryCoordinator struct {
	SessionDir string
	Owner      string
	Lease      time.Duration
	MaxRetries int
}

func NewDeliveryCoordinator(sessionDir, owner string) *DeliveryCoordinator {
	if strings.TrimSpace(owner) == "" {
		owner = "delivery-worker-" + session.GenerateID()
	}
	return &DeliveryCoordinator{SessionDir: sessionDir, Owner: owner, Lease: 30 * time.Second, MaxRetries: 5}
}

func (c *DeliveryCoordinator) Claim(ctx context.Context, operationID string, now time.Time) (*session.DeliveryOperation, error) {
	if c == nil || strings.TrimSpace(c.SessionDir) == "" {
		return nil, fmt.Errorf("delivery coordinator is not configured")
	}
	return session.ClaimDeliveryOperation(ctx, c.SessionDir, operationID, c.Owner, now, c.Lease)
}

func (c *DeliveryCoordinator) Complete(ctx context.Context, operation *session.DeliveryOperation, result DeliveryResult) error {
	if c == nil || operation == nil {
		return fmt.Errorf("delivery operation is required")
	}
	if result.Status == "" {
		return fmt.Errorf("delivery result status is required")
	}
	if result.Status == "retry_wait" && result.NextAttemptAt == nil {
		next := time.Now().UTC().Add(deliveryRetryDelay(operation.AttemptCount))
		result.NextAttemptAt = &next
	}
	return session.UpdateDeliveryOperation(ctx, c.SessionDir, operation.ID, c.Owner, operation.LeaseEpoch, result.Status, result.ProviderAssetID, result.ProviderMessageID, result.ProviderState, result.FailureCode, result.NextAttemptAt)
}

// Progress checkpoints an in-flight provider phase without releasing its
// lease. Complete must later use the same operation owner and epoch.
func (c *DeliveryCoordinator) Progress(ctx context.Context, operation *session.DeliveryOperation, result DeliveryResult) error {
	if c == nil || operation == nil {
		return fmt.Errorf("delivery operation is required")
	}
	if result.Status == "" {
		return fmt.Errorf("delivery progress status is required")
	}
	return session.UpdateDeliveryOperationProgress(ctx, c.SessionDir, operation.ID, c.Owner, operation.LeaseEpoch, result.Status, result.ProviderAssetID, result.ProviderMessageID, result.ProviderState, result.FailureCode)
}

// ReconcileDue claims and executes all currently due operations. Errors from a
// transport are converted to bounded retry_wait; callers can explicitly return
// DeliveryResult{Status:"uncertain"} when the provider result is ambiguous.
func (c *DeliveryCoordinator) ReconcileDue(ctx context.Context, now time.Time, execute DeliveryExecutor) (int, error) {
	if c == nil || execute == nil {
		return 0, fmt.Errorf("delivery coordinator and executor are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operations, err := session.ListDueDeliveryOperations(ctx, c.SessionDir, now)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, candidate := range operations {
		operation, claimErr := c.Claim(ctx, candidate.ID, now)
		if claimErr != nil {
			if errors.Is(claimErr, session.ErrDeliveryOperationBusy) {
				continue
			}
			return processed, claimErr
		}
		result, execErr := execute(ctx, *operation)
		if execErr != nil {
			// Preserve any checkpoint returned alongside an error. A provider
			// adapter may have completed an upload before discovering that the
			// response was unusable, and that state is still valuable to recovery.
			if result.ProviderAssetID == "" {
				result.ProviderAssetID = operation.ProviderAssetID
			}
			if len(result.ProviderState) == 0 {
				result.ProviderState = append([]byte(nil), operation.ProviderState...)
			}
			result.Status = "retry_wait"
			result.FailureCode = "transport_error"
			result.NextAttemptAt = ptrDeliveryTime(now.Add(deliveryRetryDelay(operation.AttemptCount)))
		}
		if result.Status == "retry_wait" && c.MaxRetries > 0 && operation.AttemptCount >= c.MaxRetries {
			result.Status = "failed"
			result.NextAttemptAt = nil
			result.FailureCode = "delivery_retries_exhausted"
		}
		if result.ProviderAssetID == "" {
			result.ProviderAssetID = operation.ProviderAssetID
		}
		if len(result.ProviderState) == 0 {
			result.ProviderState = append([]byte(nil), operation.ProviderState...)
		}
		if result.Status == "retry_wait" && result.NextAttemptAt == nil {
			result.NextAttemptAt = ptrDeliveryTime(now.Add(deliveryRetryDelay(operation.AttemptCount)))
		}
		if completeErr := c.Complete(ctx, operation, result); completeErr != nil {
			if errors.Is(completeErr, session.ErrDeliveryLeaseLost) {
				continue
			}
			return processed, completeErr
		}
		processed++
	}
	return processed, nil
}

func deliveryRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

func ptrDeliveryTime(value time.Time) *time.Time { return &value }
