package session

import (
	"database/sql"
	"fmt"
)

// bindInputResourcesToRunTx attaches Runtime-prepared input resources while
// the intent, Run row, and start event are being admitted. A resource already
// attached to another attempt of the same immutable intent is reusable for a
// retry; ownership from a different intent is rejected.
func bindInputResourcesToRunTx(tx *sql.Tx, sessionID, runID, intentID string, resourceIDs []string) error {
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
		if err == sql.ErrNoRows {
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
