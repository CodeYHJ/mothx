package session

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type SessionRunRecoveryState string

const (
	SessionRunRecoveryRunning  SessionRunRecoveryState = "recovering"
	SessionRunRecoveryFailed   SessionRunRecoveryState = "failed"
	SessionRunRecoveryComplete SessionRunRecoveryState = "completed"
	SessionRunRecoveryDetached SessionRunRecoveryState = "detached_remote"
)

// SessionRunRecovery is the durable diagnostic and retry state for orphan
// reconciliation. It never grants ownership; the recovery lease remains the
// sole authority for changing a Run.
type SessionRunRecovery struct {
	RunID              string
	SessionID          string
	State              SessionRunRecoveryState
	TriggerSource      string
	ReasonCode         string
	Attempt            int
	PreviousLeaseEpoch int64
	LastError          string
	NextRetryAt        *time.Time
	StartedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

// BeginSessionRunRecovery records an attempt while verifying the exact
// purpose=recovery lease and target Run in the same transaction.
func BeginSessionRunRecovery(sessionDir, sessionID, runID, triggerSource, reasonCode string, previousLeaseEpoch int64) (*SessionRunRecovery, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("session recovery identity is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := validateRuntimeLeaseBindingTx(tx, sessionDir, sessionID, runID, RuntimeLeasePurposeRecovery); err != nil {
		return nil, err
	}
	now, err := sqliteNow(tx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO session_run_recoveries
		(run_id, session_id, state, trigger_source, reason_code, attempt, previous_lease_epoch, last_error, next_retry_at, started_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, '', NULL, ?, ?, NULL)
		ON CONFLICT(run_id) DO UPDATE SET
			session_id = excluded.session_id,
			state = excluded.state,
			trigger_source = excluded.trigger_source,
			reason_code = excluded.reason_code,
			attempt = session_run_recoveries.attempt + 1,
			previous_lease_epoch = excluded.previous_lease_epoch,
			last_error = '',
			next_retry_at = NULL,
			started_at = excluded.started_at,
			updated_at = excluded.updated_at,
			completed_at = NULL`,
		runID, sessionID, SessionRunRecoveryRunning, triggerSource, reasonCode, previousLeaseEpoch, now, now); err != nil {
		return nil, err
	}
	recovery, err := readSessionRunRecoveryTx(tx, runID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recovery, nil
}

// MarkSessionRunRecoveryFailed persists a retryable failure under the same
// fenced recovery owner. A zero nextRetryAt means retry as soon as a Runtime
// coordinator observes the row again.
func MarkSessionRunRecoveryFailed(sessionDir, sessionID, runID, message string, nextRetryAt time.Time) error {
	return updateSessionRunRecovery(sessionDir, sessionID, runID, SessionRunRecoveryFailed, message, nextRetryAt)
}

// MarkSessionRunRecoveryDetached records that a canonical remote record was
// retained. The response record, not this marker, remains the evidence used to
// decide whether the provider execution is still recoverable.
func MarkSessionRunRecoveryDetached(sessionDir, sessionID, runID string) error {
	return updateSessionRunRecovery(sessionDir, sessionID, runID, SessionRunRecoveryDetached, "", time.Time{})
}

// MarkSessionRunRecoveryComplete records successful fenced convergence.
func MarkSessionRunRecoveryComplete(sessionDir, sessionID, runID string) error {
	return updateSessionRunRecovery(sessionDir, sessionID, runID, SessionRunRecoveryComplete, "", time.Time{})
}

// ConvergeSessionRunRecovery atomically records pending Decision resolutions,
// closes every open ConversationTurn owned by the Run, writes the terminal Run
// event and state, and completes the recovery record. The exact local
// purpose=recovery lease is revalidated inside the transaction so a stale
// recovery worker cannot commit after another owner takes over.
func ConvergeSessionRunRecovery(sessionDir string, run SessionRun, terminalEvent SessionRunEvent, decisionEvents []SessionRunEvent, turnStatus, stopReason string) error {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.SessionID) == "" || strings.TrimSpace(run.Status) == "" {
		return fmt.Errorf("recovered run identity and terminal status are required")
	}
	if IsNonTerminalSessionRunStatus(run.Status) {
		return fmt.Errorf("recovered run status must be terminal: %s", run.Status)
	}
	if terminalEvent.ID == "" || terminalEvent.EventType == "" {
		return fmt.Errorf("recovery terminal event identity and type are required")
	}
	terminalEvent.SessionID = run.SessionID
	terminalEvent.RunID = run.ID
	if terminalEvent.Status == "" {
		terminalEvent.Status = run.Status
	}
	if terminalEvent.Timestamp.IsZero() {
		terminalEvent.Timestamp = time.Now()
	}
	if run.FinishedAt == nil {
		finishedAt := terminalEvent.Timestamp
		run.FinishedAt = &finishedAt
	}
	if turnStatus == "" {
		turnStatus = run.Status
	}
	turnStatus = normalizeTurnStatus(turnStatus)

	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := validateRuntimeLeaseBindingTx(tx, sessionDir, run.SessionID, run.ID, RuntimeLeasePurposeRecovery); err != nil {
		return err
	}

	allowed := allowedRunPredecessors(run.Status)
	args := make([]any, 0, len(allowed)+5)
	args = append(args, run.Status, terminalEvent.Timestamp.Format(time.RFC3339Nano), run.FinishedAt.Format(time.RFC3339Nano), run.Error, run.ID)
	placeholders := make([]string, 0, len(allowed))
	for _, predecessor := range allowed {
		placeholders = append(placeholders, "?")
		args = append(args, predecessor)
	}
	result, err := tx.Exec(`UPDATE session_runs SET status = ?, updated_at = ?, finished_at = ?, error = ?
		WHERE id = ? AND status IN (`+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return fmt.Errorf("recovery target run is no longer active: %s", run.ID)
	}

	insertEvent := func(event SessionRunEvent) error {
		if event.ID == "" || event.EventType == "" {
			return fmt.Errorf("recovery event identity and type are required")
		}
		if event.SessionID == "" {
			event.SessionID = run.SessionID
		}
		if event.RunID == "" {
			event.RunID = run.ID
		}
		if event.SessionID != run.SessionID || event.RunID != run.ID {
			return fmt.Errorf("recovery event identity does not match run")
		}
		if event.Timestamp.IsZero() {
			event.Timestamp = terminalEvent.Timestamp
		}
		_, err := tx.Exec(`INSERT INTO session_run_events
			(id, session_id, run_id, event_type, source, status, model, mode, timestamp, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.SessionID, event.RunID, event.EventType,
			event.Source, event.Status, event.Model, event.Mode, event.Timestamp.Format(time.RFC3339Nano), string(normalizedRunJSON(event.Data)))
		return err
	}
	for _, event := range decisionEvents {
		if err := insertEvent(event); err != nil {
			return err
		}
	}
	if err := insertEvent(terminalEvent); err != nil {
		return err
	}

	type openTurn struct {
		id, intentID, runID string
	}
	rows, err := tx.Query(`SELECT t.id, t.intent_id,
		COALESCE((SELECT json_extract(e.data, '$.runId') FROM entries e
			WHERE e.session_id = t.session_id AND e.type = 'turn_start'
			AND json_extract(e.data, '$.turnId') = t.id ORDER BY e.seq DESC LIMIT 1), '')
		FROM conversation_turns t WHERE t.session_id = ? AND t.status = 'open' ORDER BY t.start_seq`, run.SessionID)
	if err != nil {
		return err
	}
	var openTurns []openTurn
	for rows.Next() {
		var turn openTurn
		if err := rows.Scan(&turn.id, &turn.intentID, &turn.runID); err != nil {
			_ = rows.Close()
			return err
		}
		if turn.runID == run.ID || (turn.runID == "" && run.IntentID != "" && turn.intentID == run.IntentID) {
			openTurns = append(openTurns, turn)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, turn := range openTurns {
		parentID, err := currentLeafTx(tx, run.SessionID)
		if err != nil {
			return err
		}
		entry := TurnEndEntry{
			EntryBase: EntryBase{Type: EntryTurnEnd, ID: GenerateID(), ParentID: stringPtr(parentID), Timestamp: terminalEvent.Timestamp},
			TurnID:    turn.id, IntentID: turn.intentID, RunID: run.ID, Status: turnStatus, StopReason: stopReason,
		}
		endSeq, err := appendTurnEntryTx(tx, run.SessionID, entry, parentID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE conversation_turns SET status = ?, end_seq = ?, ended_at = ?
			WHERE id = ? AND session_id = ? AND status = 'open'`, turnStatus, endSeq,
			terminalEvent.Timestamp.Format(time.RFC3339Nano), turn.id, run.SessionID); err != nil {
			return err
		}
	}

	result, err = tx.Exec(`UPDATE session_run_recoveries SET state = ?, last_error = '', next_retry_at = NULL,
		updated_at = ?, completed_at = ? WHERE run_id = ? AND session_id = ?`, SessionRunRecoveryComplete,
		terminalEvent.Timestamp.Unix(), terminalEvent.Timestamp.Unix(), run.ID, run.SessionID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return fmt.Errorf("session recovery record not found: %s", run.ID)
	}
	return tx.Commit()
}

func updateSessionRunRecovery(sessionDir, sessionID, runID string, state SessionRunRecoveryState, message string, nextRetryAt time.Time) error {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := validateRuntimeLeaseBindingTx(tx, sessionDir, sessionID, runID, RuntimeLeasePurposeRecovery); err != nil {
		return err
	}
	now, err := sqliteNow(tx)
	if err != nil {
		return err
	}
	var next any
	if !nextRetryAt.IsZero() {
		next = nextRetryAt.Unix()
	}
	var completed any
	if state == SessionRunRecoveryComplete {
		completed = now
	}
	result, err := tx.Exec(`UPDATE session_run_recoveries SET state = ?, last_error = ?, next_retry_at = ?, updated_at = ?, completed_at = ?
		WHERE run_id = ? AND session_id = ?`, state, message, next, now, completed, runID, sessionID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// GetSessionRunRecovery returns the last durable recovery disposition for a
// Run. A missing row is represented by (nil, nil).
func GetSessionRunRecovery(sessionDir, runID string) (*SessionRunRecovery, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	recovery, err := readSessionRunRecoveryTx(db, runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return recovery, err
}

type recoveryRowScanner interface {
	Scan(...any) error
}

type recoveryQueryer interface {
	QueryRow(string, ...any) *sql.Row
}

func readSessionRunRecoveryTx(queryer recoveryQueryer, runID string) (*SessionRunRecovery, error) {
	return scanSessionRunRecovery(queryer.QueryRow(`SELECT run_id, session_id, state, trigger_source, reason_code, attempt,
		previous_lease_epoch, last_error, next_retry_at, started_at, updated_at, completed_at
		FROM session_run_recoveries WHERE run_id = ?`, runID))
}

func scanSessionRunRecovery(scanner recoveryRowScanner) (*SessionRunRecovery, error) {
	var recovery SessionRunRecovery
	var nextRetryAt, completedAt sql.NullInt64
	var startedAt, updatedAt int64
	if err := scanner.Scan(&recovery.RunID, &recovery.SessionID, &recovery.State, &recovery.TriggerSource,
		&recovery.ReasonCode, &recovery.Attempt, &recovery.PreviousLeaseEpoch, &recovery.LastError,
		&nextRetryAt, &startedAt, &updatedAt, &completedAt); err != nil {
		return nil, err
	}
	recovery.StartedAt = time.Unix(startedAt, 0).UTC()
	recovery.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if nextRetryAt.Valid {
		value := time.Unix(nextRetryAt.Int64, 0).UTC()
		recovery.NextRetryAt = &value
	}
	if completedAt.Valid {
		value := time.Unix(completedAt.Int64, 0).UTC()
		recovery.CompletedAt = &value
	}
	return &recovery, nil
}
