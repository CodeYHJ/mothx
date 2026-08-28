package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/startvibecoding/mothx/internal/dao"
	"time"
)

// InputResourceEvent is the canonical lifecycle projection for one Runtime
// materialized resource. Transport references are intentionally absent.
type InputResourceEvent struct {
	ID         string
	SessionID  string
	ResourceID string
	RunID      string
	EventType  string
	Status     string
	Timestamp  time.Time
	Data       json.RawMessage
}

// AppendInputResourceEventTx records a resource lifecycle event in the caller's
// transaction. Deterministic event IDs make retries safe after an unknown
// commit result.
func AppendInputResourceEventTx(tx *dao.Tx, event InputResourceEvent) error {
	if tx == nil {
		return fmt.Errorf("input resource event transaction is nil")
	}
	if event.ID == "" || event.SessionID == "" || event.ResourceID == "" || event.EventType == "" {
		return fmt.Errorf("input resource event identity and type are required")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	data := event.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(`INSERT INTO input_resource_events
		(id, session_id, resource_id, run_id, event_type, status, timestamp, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`, event.ID, event.SessionID, event.ResourceID, event.RunID,
		event.EventType, event.Status, event.Timestamp.Format(time.RFC3339Nano), string(data))
	return err
}

// SaveInputResourceEvent appends a resource lifecycle event outside a larger
// transaction. Runtime admission paths should use AppendInputResourceEventTx.
func SaveInputResourceEvent(ctx context.Context, sessionDir string, event InputResourceEvent) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return WriteRootDatabase(ctx, sessionDir, func(tx *dao.Tx) error {
		return AppendInputResourceEventTx(tx, event)
	})
}

// ListInputResourceEvents returns resource lifecycle events in durable order.
func ListInputResourceEvents(ctx context.Context, sessionDir, sessionID string) ([]InputResourceEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" {
		return nil, nil
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, session_id, resource_id, run_id,
		event_type, status, timestamp, data FROM input_resource_events
		WHERE session_id = ? ORDER BY timestamp ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []InputResourceEvent
	for rows.Next() {
		var event InputResourceEvent
		var timestamp, data string
		if err := rows.Scan(&event.ID, &event.SessionID, &event.ResourceID, &event.RunID,
			&event.EventType, &event.Status, &timestamp, &data); err != nil {
			return nil, err
		}
		event.Timestamp, _ = time.Parse(time.RFC3339Nano, timestamp)
		event.Data = json.RawMessage(data)
		events = append(events, event)
	}
	return events, rows.Err()
}

// bindInputResourcesToRunTx attaches Runtime-prepared input resources while
// the intent, Run row, and start event are being admitted. A resource already
// attached to another attempt of the same immutable intent is reusable for a
// retry; ownership from a different intent is rejected.
func bindInputResourcesToRunTx(tx *dao.Tx, sessionID, runID, intentID string, resourceIDs []string) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	if sessionID == "" || runID == "" {
		return fmt.Errorf("input resource binding requires session and Run IDs")
	}
	seen := make(map[string]struct{}, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		if resourceID == "" {
			continue
		}
		if _, ok := seen[resourceID]; ok {
			continue
		}
		seen[resourceID] = struct{}{}

		var ownerRunID, status string
		err := tx.QueryRow(`SELECT run_id, status FROM input_resources WHERE id = ? AND session_id = ?`, resourceID, sessionID).Scan(&ownerRunID, &status)
		if err == dao.ErrNoRows {
			return fmt.Errorf("input resource %s does not belong to session", resourceID)
		}
		if err != nil {
			return err
		}
		if status == "missing" || status == "deleted" {
			return fmt.Errorf("input resource %s is not attachable (status %s)", resourceID, status)
		}
		if ownerRunID == "" || ownerRunID == runID {
			if _, err := tx.Exec(`UPDATE input_resources SET run_id = ?, status = 'attached' WHERE id = ? AND session_id = ?`, runID, resourceID, sessionID); err != nil {
				return err
			}
			if err := AppendInputResourceEventTx(tx, InputResourceEvent{
				ID:        "input-resource-" + resourceID + "-attached-" + runID,
				SessionID: sessionID, ResourceID: resourceID, RunID: runID,
				EventType: "input_resource_attached", Status: "attached",
				Data: json.RawMessage(`{"runId":"` + runID + `"}`),
			}); err != nil {
				return err
			}
			continue
		}

		// Retries reuse the original immutable input resource. There is no need
		// to overwrite its canonical owner just to represent another attempt.
		var ownerIntentID string
		err = tx.QueryRow(`SELECT intent_id FROM session_runs WHERE id = ? AND session_id = ?`, ownerRunID, sessionID).Scan(&ownerIntentID)
		if err != nil {
			return fmt.Errorf("resolve input resource %s owner: %w", resourceID, err)
		}
		if intentID == "" || ownerIntentID != intentID {
			return fmt.Errorf("input resource %s is already attached to another Run", resourceID)
		}
	}
	return nil
}
