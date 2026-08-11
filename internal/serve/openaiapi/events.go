package openaiapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

type capabilitySnapshot struct {
	Mode         string
	DelegateMode bool
	MultiAgent   bool
	Workflows    bool
	WebSearch    bool
	Browser      bool
	A2AMaster    bool
}

func newRunID() string {
	return "run_" + session.GenerateID()
}

// ErrIdempotencyKeyConflict means a caller reused a key for a different
// request. Silently returning the first run would be unsafe for side effects.
var ErrIdempotencyKeyConflict = errors.New("idempotency key was already used with a different request")

// requestFingerprint returns a stable, non-sensitive digest for the request
// fields selected by the caller. Only the digest is persisted in run events.
func requestFingerprint(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("sha256:%x", digest[:])
}

func findIdempotentRun(sessionDir, sessionID, key, fingerprint string) (*session.SessionRun, error) {
	key = strings.TrimSpace(key)
	if sessionDir == "" || sessionID == "" || key == "" {
		return nil, nil
	}
	events, err := session.ListSessionRunEvents(sessionDir, sessionID)
	if err != nil {
		return nil, err
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.EventType != "started" || len(ev.Data) == 0 {
			continue
		}
		var data struct {
			IdempotencyKey string `json:"idempotencyKey"`
			Fingerprint    string `json:"requestFingerprint"`
		}
		if json.Unmarshal(ev.Data, &data) != nil || data.IdempotencyKey != key {
			continue
		}
		// Events written before request fingerprints existed remain reusable for
		// availability. New events reject an accidental key collision.
		if data.Fingerprint != "" && fingerprint != "" && data.Fingerprint != fingerprint {
			return nil, ErrIdempotencyKeyConflict
		}
		run, err := session.GetSessionRun(sessionDir, ev.RunID)
		if err != nil {
			return nil, err
		}
		if run != nil {
			return run, nil
		}
		// Test and embedded runtimes may publish run events without a
		// RunManager. Return enough durable identity for idempotent callers to
		// receive the original run instead of creating a duplicate.
		return &session.SessionRun{
			ID: ev.RunID, SessionID: ev.SessionID, Source: ev.Source,
			Model: ev.Model, Mode: ev.Mode, Status: ev.Status,
			StartedAt: ev.Timestamp, UpdatedAt: ev.Timestamp,
		}, nil
	}
	return nil, nil
}

func capabilitySnapshotFromSession(sess *APISession) capabilitySnapshot {
	if sess == nil {
		return capabilitySnapshot{}
	}
	return capabilitySnapshot{
		Mode:         sess.Mode,
		DelegateMode: sess.DelegateMode,
		MultiAgent:   sess.MultiAgent,
		Workflows:    sess.Workflows,
		WebSearch:    sess.WebSearch,
		Browser:      sess.Browser,
		A2AMaster:    sess.A2AMaster,
	}
}

func (c capabilitySnapshot) values() map[string]string {
	return map[string]string{
		"mode":         c.Mode,
		"delegateMode": strconv.FormatBool(c.DelegateMode),
		"multiAgent":   strconv.FormatBool(c.MultiAgent),
		"workflows":    strconv.FormatBool(c.Workflows),
		"webSearch":    strconv.FormatBool(c.WebSearch),
		"browser":      strconv.FormatBool(c.Browser),
		"a2aMaster":    strconv.FormatBool(c.A2AMaster),
	}
}

func (s *Server) persistSessionCapabilitiesWithEvents(sess *APISession, before capabilitySnapshot, source, actor, runID string, data map[string]any) error {
	if err := s.persistSessionCapabilities(sess); err != nil {
		return err
	}
	return s.recordSessionCapabilityChanges(sess, before, source, actor, runID, data)
}

func (s *Server) recordSessionCapabilityChanges(sess *APISession, before capabilitySnapshot, source, actor, runID string, data map[string]any) error {
	if s == nil || s.settings == nil || sess == nil || sess.ID == "" {
		return nil
	}
	after := capabilitySnapshotFromSession(sess)
	beforeValues := before.values()
	afterValues := after.values()
	eventData := rawEventData(data)
	for _, capability := range []string{"mode", "delegateMode", "multiAgent", "workflows", "webSearch", "browser", "a2aMaster"} {
		oldValue := beforeValues[capability]
		newValue := afterValues[capability]
		if oldValue == newValue {
			continue
		}
		ev := session.SessionCapabilityEvent{
			SessionID:  sess.ID,
			RunID:      runID,
			EventType:  "changed",
			Source:     source,
			Actor:      actor,
			Capability: capability,
			OldValue:   oldValue,
			NewValue:   newValue,
			Timestamp:  time.Now(),
			Data:       eventData,
		}
		id, err := session.SaveSessionCapabilityEvent(s.settings.GetSessionDir(), ev)
		if err != nil {
			return fmt.Errorf("save capability event: %w", err)
		}
		ev.ID = id
		s.publishSessionStreamEvent(sess.ID, "capability_event", sessionCapabilityEventToEntry(ev, 0))
		s.getEventBroker().PublishCapabilityEvent(sess.ID, runID, sessionCapabilityEventToEntry(ev, 0))
	}
	return nil
}

func (s *Server) recordSessionRunEvent(sess *APISession, runID, eventType, status, source, modelID, mode string, data map[string]any) error {
	if s == nil || s.settings == nil || sess == nil || sess.ID == "" || runID == "" {
		return nil
	}
	ev := session.SessionRunEvent{
		SessionID: sess.ID,
		RunID:     runID,
		EventType: eventType,
		Source:    source,
		Status:    status,
		Model:     modelID,
		Mode:      mode,
		Timestamp: time.Now(),
		Data:      rawEventData(data),
	}
	id, err := session.SaveSessionRunEvent(s.settings.GetSessionDir(), ev)
	if err != nil {
		return fmt.Errorf("save run event: %w", err)
	}
	ev.ID = id
	s.publishSessionStreamEvent(sess.ID, "run_event", sessionRunEventToEntry(ev, 0))
	s.getEventBroker().PublishRunEvent(sess.ID, runID, sessionRunEventToEntry(ev, 0))
	return nil
}

func rawEventData(data map[string]any) json.RawMessage {
	if len(data) == 0 {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return raw
}

// safeHostedItemRunData keeps durable hosted lifecycle events useful for
// reconnects without persisting arbitrary provider metadata. Canonical output
// remains in the provider archive, where its dedicated redaction/size policy
// applies.
func safeHostedItemRunData(item *provider.HostedItem) map[string]any {
	if item == nil {
		return nil
	}
	data := map[string]any{
		"id": boundedHostedString(item.ID), "type": boundedHostedString(item.Type), "status": boundedHostedString(item.Status), "outputIndex": item.OutputIndex,
	}
	allowed := map[string]struct{}{
		"annotationType": {}, "title": {}, "start_index": {}, "end_index": {},
		"score": {}, "responseItemId": {}, "responseItemType": {}, "status": {}, "tool": {},
	}
	metadata := make(map[string]any)
	for key, value := range item.Metadata {
		if _, ok := allowed[key]; !ok {
			continue
		}
		switch value.(type) {
		case string:
			metadata[key] = boundedHostedString(value.(string))
		case bool, float64, float32, int, int64, uint, uint64, nil:
			metadata[key] = value
		}
	}
	if len(metadata) > 0 {
		data["metadata"] = metadata
	}
	return data
}

const maxHostedRunString = 512

func boundedHostedString(value string) string {
	if len(value) <= maxHostedRunString {
		return value
	}
	return value[:maxHostedRunString] + "..."
}

func runEventTypeForStatus(status string) string {
	switch status {
	case "failed":
		return "failed"
	case "canceled":
		return "canceled"
	default:
		return "finished"
	}
}

// IsSuccessfulRunStatus reports only a fully completed Responses run.
func IsSuccessfulRunStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "completed"
}

// IsIncompleteRunStatus reports a run that produced a partial result without
// completing its objective.
func IsIncompleteRunStatus(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	return status == "incomplete"
}

func usageEventData(usage CompletionUsage, errMsg string) map[string]any {
	data := map[string]any{
		"usage": map[string]any{
			"prompt_tokens":      usage.PromptTokens,
			"completion_tokens":  usage.CompletionTokens,
			"total_tokens":       usage.TotalTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
	}
	if errMsg != "" {
		data["error"] = errMsg
	}
	return data
}
