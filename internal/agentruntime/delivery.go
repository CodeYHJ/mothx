package agentruntime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DeliveryCapability describes a transport's actual media behavior. It is a
// Runtime policy input, not a promise inferred from a platform's wire format.
type DeliveryCapability struct {
	Text      bool
	SendImage bool
	SendFile  bool
	SendVideo bool
}

// DeliveryIntentPlan freezes the run-level transport target and opaque reply
// context before the terminal transaction creates the durable outbox.
type DeliveryIntentPlan struct {
	ID               string
	SessionID        string
	RunID            string
	Platform         string
	TargetID         string
	ReplyMessageID   string
	TransportContext json.RawMessage
	Status           string
	CreatedAt        time.Time
}

// OrderedDeliveryOperationPlan is one deterministic outbox step. Provider
// state and lease fields are populated only by the delivery coordinator after
// terminal commit.
type OrderedDeliveryOperationPlan struct {
	ID             string
	OperationKey   string
	ArtifactID     string
	OperationKind  string
	Sequence       int
	DependsOn      string
	IdempotencyKey string
	PayloadDigest  string
	Status         string
	CreatedAt      time.Time
}

// DeliveryPlan is attached to the active DurableRun and persisted by its
// terminal transaction. Adapters may supply transport hooks but never write
// these rows directly.
type DeliveryPlan struct {
	Intent     DeliveryIntentPlan
	Operations []OrderedDeliveryOperationPlan
}

// DeliveryPlanRequest contains the canonical result and the transport target
// needed to build deterministic ordered operations before terminal commit.
type DeliveryPlanRequest struct {
	SessionID        string
	RunID            string
	Platform         string
	TargetID         string
	ReplyMessageID   string
	TransportContext json.RawMessage
	Caption          string
	Attachments      []SessionAttachment
	Capability       DeliveryCapability
	CreatedAt        time.Time
}

// DeliveryOperationText returns the frozen text payload for a durable text
// operation. Transport adapters use this Runtime-owned projection for both
// immediate delivery and recovery, so neither path reconstructs a caption or
// fallback from mutable adapter state.
func DeliveryOperationText(raw []byte, operationKind string) string {
	var payload struct {
		Caption  string `json:"caption"`
		Fallback string `json:"fallback"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if operationKind == "send_fallback_text" {
		return strings.TrimSpace(payload.Fallback)
	}
	return strings.TrimSpace(payload.Caption)
}

// PlanDelivery builds a deterministic run-level caption/upload/send/fallback
// sequence. It performs no persistence and no network I/O.
func PlanDelivery(request DeliveryPlanRequest) (DeliveryPlan, string, error) {
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.Platform = strings.TrimSpace(request.Platform)
	request.TargetID = strings.TrimSpace(request.TargetID)
	if request.SessionID == "" || request.RunID == "" || request.Platform == "" {
		return DeliveryPlan{}, "", fmt.Errorf("delivery session, Run, and platform are required")
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	intentID := stableDeliveryID("intent", request.SessionID, request.RunID, request.Platform, request.TargetID)
	plan := DeliveryPlan{Intent: DeliveryIntentPlan{
		ID: intentID, SessionID: request.SessionID, RunID: request.RunID,
		Platform: request.Platform, TargetID: request.TargetID, ReplyMessageID: strings.TrimSpace(request.ReplyMessageID),
		TransportContext: append(json.RawMessage(nil), request.TransportContext...), Status: "pending", CreatedAt: request.CreatedAt,
	}}
	sequence := 0
	previousID := ""
	appendOperationWithDependency := func(key, artifactID, kind, payload, dependsOn string, status ...string) string {
		sequence++
		operationID := stableDeliveryID("operation", intentID, key)
		operationStatus := "pending"
		if len(status) > 0 && status[0] != "" {
			operationStatus = status[0]
		}
		plan.Operations = append(plan.Operations, OrderedDeliveryOperationPlan{
			ID: operationID, OperationKey: key, ArtifactID: artifactID, OperationKind: kind,
			Sequence: sequence, DependsOn: dependsOn, IdempotencyKey: operationID,
			PayloadDigest: stableDeliveryDigest(payload), Status: operationStatus, CreatedAt: request.CreatedAt,
		})
		previousID = operationID
		return operationID
	}
	appendOperation := func(key, artifactID, kind, payload string, status ...string) string {
		return appendOperationWithDependency(key, artifactID, kind, payload, previousID, status...)
	}
	caption := strings.TrimSpace(request.Caption)
	captionID := ""
	if caption != "" && request.Capability.Text {
		captionID = appendOperation("caption", "", "send_text", caption)
	}
	var fallback []string
	for index, attachment := range request.Attachments {
		if attachment.SessionID != request.SessionID || attachment.RunID != request.RunID || attachment.ID == "" {
			return DeliveryPlan{}, "", fmt.Errorf("delivery attachment does not belong to Run")
		}
		native := (attachment.Kind == AttachmentImage && request.Capability.SendImage) ||
			(attachment.Kind == AttachmentFile && request.Capability.SendFile) ||
			(attachment.Kind == AttachmentVideo && request.Capability.SendVideo)
		if !native {
			name := strings.TrimSpace(attachment.Filename)
			if name == "" {
				name = string(attachment.Kind)
			}
			fallback = append(fallback, fmt.Sprintf("Generated %s %q is available in the MothX WebUI session; this channel cannot send media attachments.", attachment.Kind, name))
			continue
		}
		keyPrefix := fmt.Sprintf("artifact-%03d-%s", index+1, attachment.ID)
		uploadID := appendOperation(keyPrefix+"-upload", attachment.ID, "upload_artifact", attachment.SHA256)
		sequence++
		sendID := stableDeliveryID("operation", intentID, keyPrefix+"-send")
		plan.Operations = append(plan.Operations, OrderedDeliveryOperationPlan{
			ID: sendID, OperationKey: keyPrefix + "-send", ArtifactID: attachment.ID, OperationKind: "send_artifact",
			Sequence: sequence, DependsOn: uploadID, IdempotencyKey: sendID,
			PayloadDigest: stableDeliveryDigest(attachment.SHA256 + "\x00" + string(attachment.Kind)), Status: "pending", CreatedAt: request.CreatedAt,
		})
		previousID = sendID
	}
	fallbackText := strings.Join(fallback, "\n")
	if fallbackText != "" && request.Capability.Text {
		// A channel's immediate projection may combine the caption and fallback
		// into one text message. Keep fallback ordered after that caption while
		// avoiding a dependency on native media that the same message already
		// precedes; recovery can therefore replay fallback without waiting on a
		// failed optional artifact send.
		appendOperationWithDependency("fallback", "", "send_fallback_text", fallbackText, captionID)
	}
	if len(plan.Operations) == 0 {
		return DeliveryPlan{}, fallbackText, nil
	}
	return plan, fallbackText, nil
}

func stableDeliveryID(kind string, values ...string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(kind))
	for _, value := range values {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(value))
	}
	return "delivery_" + kind + "_" + fmt.Sprintf("%x", digest.Sum(nil)[:16])
}

func stableDeliveryDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest[:])
}
