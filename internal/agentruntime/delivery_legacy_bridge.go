package agentruntime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// This file is a named migration bridge for schema-30 attachment_deliveries.
// The schema-34 delivery plan is authoritative for all new Runtime callers.
// Remove this file after external embedders have migrated to DeliveryPlan and
// the schema-30 table can be retired.

// AttachmentDelivery is the legacy schema-30 delivery record retained for
// embedders that have not yet adopted the durable delivery plan.
//
// Deprecated: use DeliveryPlan and DeliveryCoordinator.
type AttachmentDelivery struct {
	ID                string
	AttachmentID      string
	RunID             string
	Platform          string
	TargetID          string
	Status            string
	ProviderMessageID string
	FailureCode       string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DeliveryOperation is the legacy schema-30 projection for one artifact.
//
// Deprecated: use session.DeliveryOperation through the Runtime coordinator.
type DeliveryOperation struct {
	Attachment SessionAttachment
	Delivery   AttachmentDelivery
}

// DeliveryProjection keeps the legacy native/fallback projection shape for
// compatibility with older embedders.
//
// Deprecated: use DeliveryPlan and the messaging projection callbacks.
type DeliveryProjection struct {
	Operations   []DeliveryOperation
	FallbackText string
}

// ProjectDeliveries is the schema-30 compatibility bridge. New production
// paths must call PlanDelivery and persist its result in the terminal
// transaction instead.
//
// Deprecated: use PlanDelivery.
func (s *AttachmentService) ProjectDeliveries(ctx context.Context, attachments []SessionAttachment, platform, targetID string, capability DeliveryCapability) (DeliveryProjection, error) {
	projection := DeliveryProjection{}
	for _, attachment := range attachments {
		native := (attachment.Kind == AttachmentImage && capability.SendImage) ||
			(attachment.Kind == AttachmentFile && capability.SendFile) ||
			(attachment.Kind == AttachmentVideo && capability.SendVideo)
		delivery, err := s.BeginDelivery(ctx, attachment, platform, targetID)
		if err != nil {
			return DeliveryProjection{}, err
		}
		if native {
			projection.Operations = append(projection.Operations, DeliveryOperation{Attachment: attachment, Delivery: delivery})
			continue
		}
		if err := s.FinishDelivery(ctx, delivery.ID, "unsupported", "", "platform_media_unsupported"); err != nil {
			return DeliveryProjection{}, err
		}
		name := strings.TrimSpace(attachment.Filename)
		if name == "" {
			name = string(attachment.Kind)
		}
		if projection.FallbackText != "" {
			projection.FallbackText += "\n"
		}
		projection.FallbackText += fmt.Sprintf("Generated %s %q is available in the MothX WebUI session; this channel cannot send media attachments.", attachment.Kind, name)
	}
	return projection, nil
}

// BeginDelivery writes one schema-30 compatibility row.
//
// Deprecated: use the Runtime terminal transaction with DeliveryPlan.
func (s *AttachmentService) BeginDelivery(ctx context.Context, attachment SessionAttachment, platform, targetID string) (AttachmentDelivery, error) {
	if s == nil {
		return AttachmentDelivery{}, fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if attachment.ID == "" || attachment.SessionID == "" || attachment.RunID == "" {
		return AttachmentDelivery{}, fmt.Errorf("attachment identity and run ID are required for delivery")
	}
	if strings.TrimSpace(platform) == "" {
		return AttachmentDelivery{}, fmt.Errorf("delivery platform is required")
	}
	now := time.Now().UTC()
	record := AttachmentDelivery{
		ID: session.GenerateID(), AttachmentID: attachment.ID, RunID: attachment.RunID,
		Platform: strings.TrimSpace(platform), TargetID: strings.TrimSpace(targetID),
		Status: "pending", CreatedAt: now, UpdatedAt: now,
	}
	err := session.WriteRootDatabase(ctx, s.sessionDir, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM session_attachments WHERE id = ? AND session_id = ?`, attachment.ID, attachment.SessionID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("attachment %s does not belong to session", attachment.ID)
		}
		_, err := tx.Exec(`INSERT INTO attachment_deliveries
			(id, attachment_id, run_id, platform, target_id, status, provider_message_id, failure_code, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
			record.ID, record.AttachmentID, record.RunID, record.Platform, record.TargetID,
			record.Status, record.CreatedAt.Format(time.RFC3339Nano), record.UpdatedAt.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return AttachmentDelivery{}, fmt.Errorf("create attachment delivery: %w", err)
	}
	return record, nil
}

// FinishDelivery terminalizes one schema-30 compatibility row.
//
// Deprecated: use the Runtime coordinator's fenced operation update.
func (s *AttachmentService) FinishDelivery(ctx context.Context, deliveryID, status, providerMessageID, failureCode string) error {
	if s == nil {
		return fmt.Errorf("attachment service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(deliveryID) == "" {
		return fmt.Errorf("delivery ID is required")
	}
	switch status {
	case "delivered", "failed", "unsupported":
	default:
		return fmt.Errorf("invalid delivery status %q", status)
	}
	return session.WriteRootDatabase(ctx, s.sessionDir, func(tx *sql.Tx) error {
		result, err := tx.Exec(`UPDATE attachment_deliveries
			SET status = ?, provider_message_id = ?, failure_code = ?, updated_at = ?
			WHERE id = ? AND status = 'pending'`, status, providerMessageID, failureCode, time.Now().UTC().Format(time.RFC3339Nano), deliveryID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("pending delivery %s not found", deliveryID)
		}
		return nil
	})
}
