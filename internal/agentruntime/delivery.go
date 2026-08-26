package agentruntime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

// AttachmentDelivery is the canonical durable projection state for one
// platform delivery attempt. Platform adapters execute transport calls but do
// not own this lifecycle or create their own delivery table.
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

// DeliveryCapability describes a transport's actual media behavior. It is a
// Runtime policy input, not a promise inferred from a platform's wire format.
type DeliveryCapability struct {
	Text      bool
	SendImage bool
	SendFile  bool
}

// DeliveryOperation is a Runtime-planned execution for one private artifact.
// Thin transport adapters open and send it, then use Delivery.ID to report the
// terminal outcome through FinishDelivery.
type DeliveryOperation struct {
	Attachment SessionAttachment
	Delivery   AttachmentDelivery
}

// DeliveryProjection keeps native media operations and textual fallbacks in
// one shared policy result. A platform adapter may serialize it differently,
// but cannot substitute a local path, URL, or adapter-owned delivery state.
type DeliveryProjection struct {
	Operations   []DeliveryOperation
	FallbackText string
}

// ProjectDeliveries records pending native operations and terminal unsupported
// states exactly once. WeChat's fixed text-only policy therefore receives the
// same canonical artifacts as Feishu without pretending it can send media.
func (s *AttachmentService) ProjectDeliveries(ctx context.Context, attachments []SessionAttachment, platform, targetID string, capability DeliveryCapability) (DeliveryProjection, error) {
	projection := DeliveryProjection{}
	for _, attachment := range attachments {
		native := (attachment.Kind == AttachmentImage && capability.SendImage) || (attachment.Kind == AttachmentFile && capability.SendFile)
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

// BeginDelivery persists a pending operation before a transport attempts an
// upload/send. It is intentionally idempotency-neutral: platform-specific
// retries keep the same record ID and must transition it through FinishDelivery.
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

// FinishDelivery terminalizes an existing delivery operation. The adapter
// supplies only its opaque platform message ID or a stable failure code; it
// never gets a writable handle to Runtime persistence.
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
