package openaiapi

import (
	"encoding/json"

	"github.com/startvibecoding/mothx/internal/agentruntime"
)

// recordDecisionEvent persists the protocol-neutral DecisionRecord alongside
// the legacy payload. The legacy fields remain the compatibility contract for
// existing replay clients; the neutral record is the migration source for
// future cross-entry recovery.
func (s *Server) recordDecisionEvent(sess *APISession, request agentruntime.DecisionRequest, resolution *agentruntime.DecisionResolution, eventType, status, source, mode string, payload any) error {
	if sess == nil {
		return nil
	}
	var (
		record agentruntime.DecisionRecord
		err    error
	)
	if resolution == nil {
		record, err = agentruntime.NewDecisionRequestRecord(request, payload)
	} else {
		record, err = agentruntime.NewDecisionResolutionRecord(request, *resolution, payload)
	}
	if err != nil {
		return err
	}
	data := map[string]any{
		"decision": record,
		"payload":  payload,
	}
	// Preserve the legacy event shape used by existing recovery/replay code.
	// New consumers can use decision/payload; old consumers continue to find
	// approval/question and resolution at the top level.
	switch request.Kind {
	case agentruntime.DecisionApproval:
		if eventType == "approval_requested" {
			data["approval"] = payload
		} else {
			mergeDecisionPayload(data, payload)
		}
	case agentruntime.DecisionQuestion:
		if eventType == "question_requested" {
			data["question"] = payload
		} else {
			mergeDecisionPayload(data, payload)
		}
	}
	return s.recordSessionRunEvent(sess, request.RunID, eventType, status, source, "", mode, data)
}

func mergeDecisionPayload(data map[string]any, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	var values map[string]any
	if json.Unmarshal(raw, &values) == nil {
		for field, value := range values {
			data[field] = value
		}
	}
}
