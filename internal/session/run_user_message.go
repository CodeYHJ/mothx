package session

import (
	"database/sql"
	"fmt"
	"time"
)

// RunUserEntryID is the deterministic transcript identity for a Run's
// admitted user message. Retries do not create another user entry.
func RunUserEntryID(runID string) string {
	if runID == "" {
		return ""
	}
	return "run-user-" + runID
}

func appendRunUserMessageTx(tx *sql.Tx, run SessionRun) error {
	if run.UserMessage == nil {
		return nil
	}
	if run.SessionID == "" || run.ID == "" {
		return fmt.Errorf("session run identity is required for user entry")
	}
	message := *run.UserMessage
	if message.SystemInjected {
		return fmt.Errorf("runtime-admitted user entry cannot be system injected")
	}
	if message.Role == "" {
		message.Role = "user"
	}
	if message.Role != "user" {
		return fmt.Errorf("runtime-admitted entry must have user role")
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = run.StartedAt
		if message.Timestamp.IsZero() {
			message.Timestamp = time.Now()
		}
	}
	entryID := run.UserEntryID
	if entryID == "" {
		entryID = RunUserEntryID(run.ID)
	}
	parentID, err := currentLeafTx(tx, run.SessionID)
	if err != nil {
		return err
	}
	entry := MessageEntry{
		EntryBase: EntryBase{Type: EntryMessage, ID: entryID, ParentID: stringPtr(parentID), Timestamp: message.Timestamp},
		Message:   message,
	}
	if _, err := appendTurnEntryTx(tx, run.SessionID, entry, parentID); err != nil {
		return fmt.Errorf("append runtime user entry: %w", err)
	}
	return nil
}
