package agentruntime

import (
	"encoding/json"
	"time"
)

// DecisionRecord is the protocol-neutral durable projection of a pending or
// resolved Approval/Question. Adapters may persist their legacy payload beside
// this record while migrating; the record itself must not contain agent/runtime
// pointers or protocol-specific response channels.
type DecisionRecord struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	RunID     string          `json:"runId"`
	Kind      DecisionKind    `json:"kind"`
	Status    string          `json:"status"`
	Value     string          `json:"value,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"createdAt,omitempty"`
	ExpiresAt time.Time       `json:"expiresAt,omitempty"`
}

func NewDecisionRequestRecord(request DecisionRequest, payload any) (DecisionRecord, error) {
	return NewDecisionRequestRecordWithDeadline(request, payload, time.Time{})
}

func NewDecisionRequestRecordWithDeadline(request DecisionRequest, payload any, expiresAt time.Time) (DecisionRecord, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return DecisionRecord{}, err
	}
	return DecisionRecord{
		ID: request.ID, SessionID: request.SessionID, RunID: request.RunID,
		Kind: request.Kind, Status: "pending", Payload: raw, CreatedAt: time.Now(), ExpiresAt: expiresAt,
	}, nil
}

func NewDecisionResolutionRecord(request DecisionRequest, resolution DecisionResolution, payload any) (DecisionRecord, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return DecisionRecord{}, err
	}
	return DecisionRecord{
		ID: request.ID, SessionID: request.SessionID, RunID: request.RunID,
		Kind: request.Kind, Status: resolution.Status, Value: resolution.Value, Payload: raw, CreatedAt: time.Now(),
	}, nil
}
