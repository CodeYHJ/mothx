package channels

import (
	"encoding/json"
	"log"
	"time"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/session"
)

func (d *Dispatcher) persistChannelDecision(sess *ChannelSession, id string, kind agentruntime.DecisionKind, status, value string, payload map[string]any) {
	if d == nil || sess == nil || id == "" || sess.ID == "" || sess.runID == "" {
		return
	}
	request := agentruntime.DecisionRequest{ID: id, SessionID: sess.ID, RunID: sess.runID, Kind: kind}
	resolution := agentruntime.DecisionResolution{ID: id, Kind: kind, Status: status, Value: value}
	record, err := agentruntime.NewDecisionResolutionRecord(request, resolution, payload)
	if err != nil {
		log.Printf("[channels] encode decision %s: %v", id, err)
		return
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": payload})
	if err != nil {
		return
	}
	if _, err := session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
		SessionID: sess.ID, RunID: sess.runID, EventType: "decision_" + status,
		Source: "channel:" + sess.Platform, Status: status, Mode: sess.Mode,
		Timestamp: time.Now(), Data: data,
	}); err != nil {
		log.Printf("[channels] save decision %s: %v", id, err)
	}
}

func (d *Dispatcher) persistChannelDecisionRequest(sess *ChannelSession, id string, kind agentruntime.DecisionKind, payload map[string]any) {
	d.persistChannelDecisionRequestWithDeadline(sess, id, kind, payload, time.Time{})
}

func (d *Dispatcher) persistChannelDecisionRequestWithDeadline(sess *ChannelSession, id string, kind agentruntime.DecisionKind, payload map[string]any, expiresAt time.Time) {
	if d == nil || sess == nil || id == "" || sess.ID == "" || sess.runID == "" {
		return
	}
	request := agentruntime.DecisionRequest{ID: id, SessionID: sess.ID, RunID: sess.runID, Kind: kind}
	record, err := agentruntime.NewDecisionRequestRecordWithDeadline(request, payload, expiresAt)
	if err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": payload})
	if err != nil {
		return
	}
	if _, err := session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
		SessionID: sess.ID, RunID: sess.runID, EventType: "decision_requested",
		Source: "channel:" + sess.Platform, Status: "pending", Mode: sess.Mode,
		Timestamp: time.Now(), Data: data,
	}); err != nil {
		log.Printf("[channels] save decision request %s: %v", id, err)
	}
}
