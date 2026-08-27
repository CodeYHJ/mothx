package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrConversationTurnNotOpen = errors.New("conversation turn is not open")

// ConversationTurn is the durable boundary index used by Session fork
// resolution. It is intentionally separate from SessionRun because a Run may
// execute tools or maintenance work without producing a conversation turn.
type ConversationTurn struct {
	ID        string
	SessionID string
	IntentID  string
	RunID     string
	Attempt   int
	Kind      string
	Status    string
	StartSeq  int64
	EndSeq    *int64
	StartedAt time.Time
	EndedAt   *time.Time
}

func normalizeTurnStatus(status string) string {
	if status == "" {
		return "open"
	}
	return status
}

func appendTurnEntryTx(tx *sql.Tx, sessionID string, entry any, parentID string) (int64, error) {
	return appendTurnEntryTxContext(context.Background(), tx, sessionID, entry, parentID)
}

func appendTurnEntryTxContext(ctx context.Context, tx *sql.Tx, sessionID string, entry any, parentID string) (int64, error) {
	id, typeName, _, timestamp := getEntryMetadata(entry)
	if id == "" || typeName == "" {
		return 0, fmt.Errorf("turn entry identity is required")
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal turn entry: %w", err)
	}
	var parent any
	if parentID != "" {
		parent = parentID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO entries (session_id, id, type, parent_id, timestamp, data)
		VALUES (?, ?, ?, ?, ?, ?)`, sessionID, id, typeName, parent, timestamp.Format(time.RFC3339Nano), string(data))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func currentLeafTx(tx *sql.Tx, sessionID string) (string, error) {
	return currentLeafTxContext(context.Background(), tx, sessionID)
}

func currentLeafTxContext(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	var leaf sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id FROM entries WHERE session_id = ? AND type <> ? ORDER BY seq DESC LIMIT 1`, sessionID, string(EntrySession)).Scan(&leaf)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return leaf.String, nil
}

// StartConversationTurn atomically writes turn/start and its boundary row.
func StartConversationTurn(sessionDir string, turn ConversationTurn) error {
	if turn.SessionID == "" || turn.ID == "" {
		return fmt.Errorf("conversation turn ID and session ID are required")
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now()
	}
	if turn.Kind == "" {
		turn.Kind = "conversation"
	}
	turn.Status = "open"
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateRuntimeLeaseTx(tx, sessionDir, turn.SessionID); err != nil {
		return err
	}
	if err := startConversationTurnTx(tx, turn); err != nil {
		return err
	}
	return tx.Commit()
}

// startConversationTurnTx is the shared transaction primitive used by both
// standalone turn admission and atomic durable Run admission. The caller owns
// lease validation and transaction commit/rollback.
func startConversationTurnTx(tx *sql.Tx, turn ConversationTurn) error {
	if turn.SessionID == "" || turn.ID == "" {
		return fmt.Errorf("conversation turn ID and session ID are required")
	}
	if turn.StartedAt.IsZero() {
		turn.StartedAt = time.Now()
	}
	if turn.Kind == "" {
		turn.Kind = "conversation"
	}
	turn.Status = "open"
	var existingIntent, existingStatus, existingRunID string
	existingErr := tx.QueryRow(`SELECT intent_id, status,
		COALESCE((SELECT json_extract(e.data, '$.runId') FROM entries e
			WHERE e.session_id = conversation_turns.session_id AND e.type = 'turn_start'
			AND json_extract(e.data, '$.turnId') = conversation_turns.id
			ORDER BY e.seq DESC LIMIT 1), '')
		FROM conversation_turns WHERE id = ? AND session_id = ?`, turn.ID, turn.SessionID).
		Scan(&existingIntent, &existingStatus, &existingRunID)
	if existingErr != nil && existingErr != sql.ErrNoRows {
		return existingErr
	}
	if existingErr == nil {
		if existingIntent != "" && turn.IntentID != "" && existingIntent != turn.IntentID {
			return fmt.Errorf("conversation turn %s belongs to another intent", turn.ID)
		}
		if existingStatus == "open" {
			if existingRunID == turn.RunID && turn.RunID != "" {
				return nil
			}
			return fmt.Errorf("conversation turn already open for session %s", turn.SessionID)
		}
	}
	var openCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM conversation_turns WHERE session_id = ? AND status = 'open'`, turn.SessionID).Scan(&openCount); err != nil {
		return err
	}
	if openCount != 0 {
		return fmt.Errorf("conversation turn already open for session %s", turn.SessionID)
	}
	parentID, err := currentLeafTx(tx, turn.SessionID)
	if err != nil {
		return err
	}
	entry := TurnStartEntry{EntryBase: EntryBase{Type: EntryTurnStart, ID: GenerateID(), ParentID: stringPtr(parentID), Timestamp: turn.StartedAt}, TurnID: turn.ID, IntentID: turn.IntentID, RunID: turn.RunID, Attempt: turn.Attempt}
	startSeq, err := appendTurnEntryTx(tx, turn.SessionID, entry, parentID)
	if err != nil {
		return err
	}
	if existingErr == nil {
		_, err = tx.Exec(`UPDATE conversation_turns SET intent_id = ?, status = 'open', end_seq = NULL, started_at = ?, ended_at = NULL WHERE id = ? AND session_id = ?`, turn.IntentID, turn.StartedAt.Format(time.RFC3339Nano), turn.ID, turn.SessionID)
	} else {
		_, err = tx.Exec(`INSERT INTO conversation_turns
			(id, session_id, intent_id, kind, status, start_seq, started_at)
			VALUES (?, ?, ?, ?, 'open', ?, ?)`, turn.ID, turn.SessionID, turn.IntentID, turn.Kind, startSeq, turn.StartedAt.Format(time.RFC3339Nano))
	}
	if err != nil {
		return err
	}
	return nil
}

// EndConversationTurn atomically writes turn/end and closes its boundary row.
func EndConversationTurn(sessionDir, sessionID, turnID, status, stopReason string, endedAt time.Time) error {
	if sessionID == "" || turnID == "" {
		return fmt.Errorf("conversation turn and session ID are required")
	}
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	status = normalizeTurnStatus(status)
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	var intentID, runID string
	if err := tx.QueryRow(`SELECT intent_id, COALESCE((SELECT json_extract(e.data, '$.runId') FROM entries e WHERE e.session_id = t.session_id AND e.type = 'turn_start' AND json_extract(e.data, '$.turnId') = t.id ORDER BY e.seq DESC LIMIT 1), '')
		FROM conversation_turns t WHERE t.id = ? AND t.session_id = ? AND t.status = 'open'`, turnID, sessionID).Scan(&intentID, &runID); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: %s", ErrConversationTurnNotOpen, turnID)
		}
		return err
	}
	parentID, err := currentLeafTx(tx, sessionID)
	if err != nil {
		return err
	}
	entry := TurnEndEntry{EntryBase: EntryBase{Type: EntryTurnEnd, ID: GenerateID(), ParentID: stringPtr(parentID), Timestamp: endedAt}, TurnID: turnID, IntentID: intentID, RunID: runID, Status: status, StopReason: stopReason}
	endSeq, err := appendTurnEntryTx(tx, sessionID, entry, parentID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE conversation_turns SET status = ?, end_seq = ?, ended_at = ? WHERE id = ? AND session_id = ? AND status = 'open'`, status, endSeq, endedAt.Format(time.RFC3339Nano), turnID, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func scanConversationTurn(scanner interface{ Scan(...any) error }) (*ConversationTurn, error) {
	var turn ConversationTurn
	var endedSeq sql.NullInt64
	var started, ended string
	if err := scanner.Scan(&turn.ID, &turn.SessionID, &turn.IntentID, &turn.Kind, &turn.Status, &turn.StartSeq, &endedSeq, &started, &ended); err != nil {
		return nil, err
	}
	if endedSeq.Valid {
		value := endedSeq.Int64
		turn.EndSeq = &value
	}
	turn.StartedAt = parseSessionTimestamp(started)
	if ended != "" {
		value := parseSessionTimestamp(ended)
		turn.EndedAt = &value
	}
	return &turn, nil
}

// ListConversationTurns returns boundary rows in transcript order.
func ListConversationTurns(sessionDir, sessionID string) ([]ConversationTurn, error) {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, intent_id, kind, status, start_seq, end_seq, started_at, COALESCE(ended_at, '')
		FROM conversation_turns WHERE session_id = ? ORDER BY start_seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []ConversationTurn
	for rows.Next() {
		turn, err := scanConversationTurn(rows)
		if err != nil {
			return nil, err
		}
		turns = append(turns, *turn)
	}
	return turns, rows.Err()
}
