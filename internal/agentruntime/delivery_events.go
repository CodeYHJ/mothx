package agentruntime

import (
	"encoding/json"
	"time"
)

// DeliveryPendingData returns the compatibility payload used by existing
// channel recovery while keeping delivery semantics at the Runtime boundary.
func DeliveryPendingData(responseRunID, responseID, state, assistantEntryID string, extra map[string]any) map[string]any {
	data := map[string]any{
		"responseRunId":          responseRunID,
		"responseId":             responseID,
		"state":                  state,
		"channelDeliveryPending": true,
		"assistantEntryId":       assistantEntryID,
	}
	for key, value := range extra {
		data[key] = value
	}
	return data
}

func NewDeliveryPendingEvent(sessionID, runID, source, status, model, mode string, data any) RunEvent {
	raw, _ := json.Marshal(data)
	return RunEvent{
		SessionID: sessionID, RunID: runID, EventType: "finished",
		Source: source, Status: status, Model: model, Mode: mode,
		Timestamp: time.Now(), Data: raw,
	}
}
