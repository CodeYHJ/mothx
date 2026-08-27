package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const nonTerminalSessionRunStatusSQL = "'created', 'queued', 'running', 'waiting_for_approval', 'waiting_for_question', 'cancelling', 'terminalizing'"

// NonTerminalSessionRunStatuses returns the canonical durable statuses that
// keep a Session busy. Callers receive a copy so the shared definition cannot
// be mutated outside this package.
func NonTerminalSessionRunStatuses() []string {
	return []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling", "terminalizing"}
}

// IsNonTerminalSessionRunStatus reports whether a durable Run still requires
// execution, cancellation, or terminal persistence work.
func IsNonTerminalSessionRunStatus(status string) bool {
	switch status {
	case "created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling", "terminalizing":
		return true
	default:
		return false
	}
}

// SessionRun is the durable lifecycle record for one agent execution.
type SessionRun struct {
	ID           string
	SessionID    string
	IntentID     string
	RetryOf      string
	Attempt      int
	WorkDir      string
	Source       string
	Model        string
	Mode         string
	Status       string
	StartedAt    time.Time
	UpdatedAt    time.Time
	FinishedAt   *time.Time
	Error        string
	ErrorInfo    json.RawMessage
	Progress     json.RawMessage
	Usage        json.RawMessage
	ContextUsage json.RawMessage
}

func SaveSessionRun(sessionDir string, run SessionRun) error {
	if run.ID == "" || run.SessionID == "" {
		return fmt.Errorf("session run ID and session ID are required")
	}
	if run.Status == "" {
		return fmt.Errorf("session run status is required")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.StartedAt
	}
	if len(run.Usage) == 0 {
		run.Usage = json.RawMessage(`{}`)
	}
	if len(run.ContextUsage) == 0 {
		run.ContextUsage = json.RawMessage(`{}`)
	}
	if run.Attempt <= 0 {
		run.Attempt = 1
	}
	run.ErrorInfo = normalizedRunJSON(run.ErrorInfo)
	run.Progress = normalizedRunJSON(run.Progress)
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateRuntimeLeaseTx(tx, sessionDir, run.SessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO session_runs
		(id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		status=excluded.status, updated_at=excluded.updated_at, finished_at=excluded.finished_at,
		error=excluded.error, error_info_json=excluded.error_info_json, progress_json=excluded.progress_json, usage_json=excluded.usage_json, context_usage_json=excluded.context_usage_json`,
		run.ID, run.SessionID, run.IntentID, run.RetryOf, run.Attempt, run.WorkDir, run.Source, run.Model, run.Mode, run.Status,
		run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), finished, run.Error, string(run.ErrorInfo), string(run.Progress), string(run.Usage), string(run.ContextUsage))
	if err != nil {
		return err
	}
	var boundLease *runtimeLease
	if IsNonTerminalSessionRunStatus(run.Status) {
		boundLease, err = bindRuntimeLeaseToRunTx(tx, sessionDir, run.SessionID, run.ID)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	markRuntimeLeaseBound(boundLease, run.ID)
	return nil
}

// CreateSessionRun inserts one canonical run row. Unlike SaveSessionRun, this
// method never overwrites an existing identity; Runtime-owned lifecycle code
// must treat duplicate run IDs as an admission error.
func CreateSessionRun(sessionDir string, run SessionRun) error {
	if run.ID == "" || run.SessionID == "" {
		return fmt.Errorf("session run ID and session ID are required")
	}
	if run.Status == "" {
		return fmt.Errorf("session run status is required")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.StartedAt
	}
	if len(run.Usage) == 0 {
		run.Usage = json.RawMessage(`{}`)
	}
	if len(run.ContextUsage) == 0 {
		run.ContextUsage = json.RawMessage(`{}`)
	}
	if run.Attempt <= 0 {
		run.Attempt = 1
	}
	run.ErrorInfo = normalizedRunJSON(run.ErrorInfo)
	run.Progress = normalizedRunJSON(run.Progress)
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateRuntimeLeaseTx(tx, sessionDir, run.SessionID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO session_runs
		(id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SessionID, run.IntentID, run.RetryOf, run.Attempt, run.WorkDir, run.Source, run.Model, run.Mode, run.Status,
		run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), finished, run.Error, string(run.ErrorInfo), string(run.Progress), string(run.Usage), string(run.ContextUsage))
	if err != nil {
		return err
	}
	var boundLease *runtimeLease
	if IsNonTerminalSessionRunStatus(run.Status) {
		boundLease, err = bindRuntimeLeaseToRunTx(tx, sessionDir, run.SessionID, run.ID)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	markRuntimeLeaseBound(boundLease, run.ID)
	return nil
}

// CreateSessionRunAndEvent atomically inserts a new canonical Run and its
// first event. Retry attempts use this path so a process loss cannot leave a
// durable attempt without a replay anchor.
func CreateSessionRunAndEvent(sessionDir string, run SessionRun, event SessionRunEvent) (string, error) {
	return createSessionRunAndEvent(sessionDir, run, event, nil)
}

// CreateSessionRunAndEventWithTurn atomically admits a Run, its first event,
// and a conversation turn boundary when the Run produces transcript output.
func CreateSessionRunAndEventWithTurn(sessionDir string, run SessionRun, event SessionRunEvent, turn ConversationTurn) (string, error) {
	return createSessionRunAndEvent(sessionDir, run, event, &turn)
}

// FinishSessionRunAndConversationTurn atomically closes a conversation turn,
// its Run row, and the terminal Run event. A missing turn is tolerated for
// recovery/idempotent retries because an Agent may already have emitted the
// boundary before Runtime terminalization.
func FinishSessionRunAndConversationTurn(sessionDir string, run SessionRun, event SessionRunEvent, turnID, turnStatus, stopReason string) (string, error) {
	if run.ID == "" || run.SessionID == "" || run.Status == "" {
		return "", fmt.Errorf("session run identity and terminal status are required")
	}
	if event.EventType == "" {
		return "", fmt.Errorf("session run event type is required")
	}
	if event.ID == "" {
		event.ID = GenerateID()
	}
	event.SessionID, event.RunID = run.SessionID, run.ID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.Status == "" {
		event.Status = run.Status
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return "", err
	}
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, run.SessionID); err != nil {
		return "", err
	}
	allowed := allowedRunPredecessors(run.Status)
	args := make([]any, 0, len(allowed)+5)
	finished := any(nil)
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	args = append(args, run.Status, time.Now().Format(time.RFC3339Nano), finished, run.Error, run.ID)
	placeholders := make([]string, 0, len(allowed))
	for _, predecessor := range allowed {
		placeholders = append(placeholders, "?")
		args = append(args, predecessor)
	}
	result, err := tx.Exec(`UPDATE session_runs SET status = ?, updated_at = ?, finished_at = ?, error = ? WHERE id = ? AND status IN (`+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		var current string
		if err := tx.QueryRow(`SELECT status FROM session_runs WHERE id = ?`, run.ID).Scan(&current); err != nil {
			return "", err
		}
		if current != run.Status {
			return "", fmt.Errorf("invalid session run transition %q -> %q", current, run.Status)
		}
	}
	if _, err := tx.Exec(`INSERT INTO session_run_events
		(id, session_id, run_id, event_type, source, status, model, mode, timestamp, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.SessionID, event.RunID, event.EventType,
		event.Source, event.Status, event.Model, event.Mode, event.Timestamp.Format(time.RFC3339Nano), string(normalizedRunJSON(event.Data))); err != nil {
		return "", err
	}
	if turnID != "" {
		var intentID, runFromEntry string
		err := tx.QueryRow(`SELECT intent_id, COALESCE((SELECT json_extract(e.data, '$.runId') FROM entries e WHERE e.session_id = t.session_id AND e.type = 'turn_start' AND json_extract(e.data, '$.turnId') = t.id ORDER BY e.seq DESC LIMIT 1), '') FROM conversation_turns t WHERE t.id = ? AND t.session_id = ? AND t.status = 'open'`, turnID, run.SessionID).Scan(&intentID, &runFromEntry)
		if err == nil {
			parentID, err := currentLeafTx(tx, run.SessionID)
			if err != nil {
				return "", err
			}
			entry := TurnEndEntry{EntryBase: EntryBase{Type: EntryTurnEnd, ID: GenerateID(), ParentID: stringPtr(parentID), Timestamp: event.Timestamp}, TurnID: turnID, IntentID: intentID, RunID: runFromEntry, Status: turnStatus, StopReason: stopReason}
			endSeq, err := appendTurnEntryTx(tx, run.SessionID, entry, parentID)
			if err != nil {
				return "", err
			}
			if _, err := tx.Exec(`UPDATE conversation_turns SET status = ?, end_seq = ?, ended_at = ? WHERE id = ? AND session_id = ? AND status = 'open'`, turnStatus, endSeq, event.Timestamp.Format(time.RFC3339Nano), turnID, run.SessionID); err != nil {
				return "", err
			}
		} else if err != sql.ErrNoRows {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return event.ID, nil
}

func createSessionRunAndEvent(sessionDir string, run SessionRun, event SessionRunEvent, turn *ConversationTurn) (string, error) {
	if run.ID == "" || run.SessionID == "" {
		return "", fmt.Errorf("session run ID and session ID are required")
	}
	if run.Status == "" {
		return "", fmt.Errorf("session run status is required")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.StartedAt
	}
	if len(run.Usage) == 0 {
		run.Usage = json.RawMessage(`{}`)
	}
	if len(run.ContextUsage) == 0 {
		run.ContextUsage = json.RawMessage(`{}`)
	}
	if run.Attempt <= 0 {
		run.Attempt = 1
	}
	run.ErrorInfo = normalizedRunJSON(run.ErrorInfo)
	run.Progress = normalizedRunJSON(run.Progress)
	if event.EventType == "" {
		return "", fmt.Errorf("session run event type is required")
	}
	if event.ID == "" {
		event.ID = GenerateID()
	}
	if event.SessionID == "" {
		event.SessionID = run.SessionID
	}
	if event.RunID == "" {
		event.RunID = run.ID
	}
	if event.SessionID != run.SessionID || event.RunID != run.ID {
		return "", fmt.Errorf("session run event identity does not match run")
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = run.StartedAt
	}
	if event.Status == "" {
		event.Status = run.Status
	}
	if event.Source == "" {
		event.Source = run.Source
	}
	if event.Model == "" {
		event.Model = run.Model
	}
	if event.Mode == "" {
		event.Mode = run.Mode
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return "", err
	}
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, run.SessionID); err != nil {
		return "", err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	if _, err := tx.Exec(`INSERT INTO session_runs
		(id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SessionID, run.IntentID, run.RetryOf, run.Attempt, run.WorkDir, run.Source, run.Model, run.Mode, run.Status,
		run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), finished, run.Error,
		string(run.ErrorInfo), string(run.Progress), string(run.Usage), string(run.ContextUsage)); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO session_run_events
		(id, session_id, run_id, event_type, source, status, model, mode, timestamp, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.SessionID, event.RunID,
		event.EventType, event.Source, event.Status, event.Model, event.Mode,
		event.Timestamp.Format(time.RFC3339Nano), string(normalizedRunJSON(event.Data))); err != nil {
		return "", err
	}
	if turn != nil {
		if turn.SessionID != run.SessionID || (turn.RunID != "" && turn.RunID != run.ID) {
			return "", fmt.Errorf("conversation turn identity does not match run")
		}
		if turn.RunID == "" {
			turn.RunID = run.ID
		}
		if turn.IntentID == "" {
			turn.IntentID = run.IntentID
		}
		if err := startConversationTurnTx(tx, *turn); err != nil {
			return "", err
		}
	}
	boundLease, err := bindRuntimeLeaseToRunTx(tx, sessionDir, run.SessionID, run.ID)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	markRuntimeLeaseBound(boundLease, run.ID)
	return event.ID, nil
}

func scanSessionRun(scanner interface{ Scan(...any) error }) (*SessionRun, error) {
	var run SessionRun
	var started, updated, errorInfo, progress, usage, contextUsage string
	var finished sql.NullString
	if err := scanner.Scan(&run.ID, &run.SessionID, &run.IntentID, &run.RetryOf, &run.Attempt, &run.WorkDir, &run.Source, &run.Model, &run.Mode, &run.Status, &started, &updated, &finished, &run.Error, &errorInfo, &progress, &usage, &contextUsage); err != nil {
		return nil, err
	}
	run.StartedAt = parseSessionTimestamp(started)
	run.UpdatedAt = parseSessionTimestamp(updated)
	if finished.Valid && finished.String != "" {
		value := parseSessionTimestamp(finished.String)
		run.FinishedAt = &value
	}
	run.Usage = json.RawMessage(usage)
	run.ErrorInfo = json.RawMessage(errorInfo)
	run.Progress = json.RawMessage(progress)
	run.ContextUsage = json.RawMessage(contextUsage)
	return &run, nil
}

func GetSessionRun(sessionDir, runID string) (*SessionRun, error) {
	if runID == "" {
		return nil, fmt.Errorf("run ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	run, err := scanSessionRun(db.QueryRow(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json FROM session_runs WHERE id = ?`, runID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return run, err
}

func GetActiveSessionRun(sessionDir, sessionID string) (*SessionRun, error) {
	if sessionID == "" {
		return nil, nil
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var runID string
	err = db.QueryRow(`SELECT id FROM session_runs WHERE session_id = ? AND status IN (`+nonTerminalSessionRunStatusSQL+`) ORDER BY started_at DESC LIMIT 1`, sessionID).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetSessionRun(sessionDir, runID)
}

func ListSessionRuns(sessionDir, sessionID string, limit int) ([]SessionRun, error) {
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json FROM session_runs WHERE session_id = ? ORDER BY started_at DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionRun
	for rows.Next() {
		run, err := scanSessionRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *run)
	}
	return result, rows.Err()
}

// NextSessionRunAttempt returns the next ordered user-visible attempt for an
// ExecutionIntent. Callers must hold their Runtime admission lock while using
// the returned value and creating the Run, so two retry commands cannot select
// the same attempt number.
func NextSessionRunAttempt(sessionDir, sessionID, intentID string) (int, error) {
	if sessionID == "" || intentID == "" {
		return 0, fmt.Errorf("session ID and execution intent ID are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return 0, err
	}
	var attempt int
	if err := db.QueryRow(`SELECT COALESCE(MAX(attempt), 0) + 1 FROM session_runs WHERE session_id = ? AND intent_id = ?`, sessionID, intentID).Scan(&attempt); err != nil {
		return 0, err
	}
	if attempt < 2 {
		attempt = 2
	}
	return attempt, nil
}

// LatestSessionRunForIntent returns the highest-attempt Run in an immutable
// intent chain. Retry admission uses it to prevent two callers from retrying
// an older terminal attempt after a newer attempt already exists.
func LatestSessionRunForIntent(sessionDir, sessionID, intentID string) (*SessionRun, error) {
	if sessionID == "" || intentID == "" {
		return nil, fmt.Errorf("session ID and execution intent ID are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var runID string
	err = db.QueryRow(`SELECT id FROM session_runs WHERE session_id = ? AND intent_id = ? ORDER BY attempt DESC, started_at DESC LIMIT 1`, sessionID, intentID).Scan(&runID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetSessionRun(sessionDir, runID)
}

func UpdateSessionRunStatus(sessionDir, runID, status, message string, finishedAt *time.Time) error {
	if runID == "" || status == "" {
		return fmt.Errorf("run ID and status are required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var finished any
	if finishedAt != nil {
		finished = finishedAt.Format(time.RFC3339Nano)
	}
	allowed := allowedRunPredecessors(status)
	args := make([]any, 0, len(allowed)+5)
	args = append(args, status, time.Now().Format(time.RFC3339Nano), finished, message, runID)
	placeholders := make([]string, 0, len(allowed))
	for _, predecessor := range allowed {
		placeholders = append(placeholders, "?")
		args = append(args, predecessor)
	}
	query := `UPDATE session_runs SET status = ?, updated_at = ?, finished_at = ?, error = ? WHERE id = ? AND status IN (` + strings.Join(placeholders, ",") + `)`
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_runs WHERE id = ?`, runID).Scan(&sessionID); err != nil {
		return err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		current, getErr := scanSessionRun(tx.QueryRow(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json FROM session_runs WHERE id = ?`, runID))
		if getErr == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		if getErr != nil {
			return getErr
		}
		if current == nil {
			return sql.ErrNoRows
		}
		if current.Status == status {
			return nil
		}
		return fmt.Errorf("invalid session run transition %q -> %q", current.Status, status)
	}
	return tx.Commit()
}

// UpdateSessionRunErrorInfo stores the structured terminal/recovery error
// independently of the compatibility Error summary column.
func UpdateSessionRunErrorInfo(sessionDir, runID string, info json.RawMessage) error {
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_runs WHERE id = ?`, runID).Scan(&sessionID); err != nil {
		return err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE session_runs SET error_info_json = ?, updated_at = ? WHERE id = ?`, string(normalizedRunJSON(info)), time.Now().Format(time.RFC3339Nano), runID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateSessionRunProgress persists the latest non-terminal retry/recovery
// projection. Terminal callers should clear it with an empty object.
func UpdateSessionRunProgress(sessionDir, runID string, progress json.RawMessage) error {
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_runs WHERE id = ?`, runID).Scan(&sessionID); err != nil {
		return err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE session_runs SET progress_json = ?, updated_at = ? WHERE id = ?`, string(normalizedRunJSON(progress)), time.Now().Format(time.RFC3339Nano), runID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateSessionRunUsage persists token and context-window usage independently
// from terminalization so reconnects can inspect partial or recovered runs.
func UpdateSessionRunUsage(sessionDir, runID string, usage, contextUsage json.RawMessage) error {
	if runID == "" {
		return fmt.Errorf("run ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_runs WHERE id = ?`, runID).Scan(&sessionID); err != nil {
		return err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE session_runs SET usage_json = ?, context_usage_json = ?, updated_at = ? WHERE id = ?`,
		string(normalizedRunJSON(usage)), string(normalizedRunJSON(contextUsage)), time.Now().Format(time.RFC3339Nano), runID); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizedRunJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 || !json.Valid(value) {
		return json.RawMessage(`{}`)
	}
	return value
}

// ReopenSessionRun is an explicit recovery transition for a terminal run whose
// provider task can be resumed. Normal lifecycle callers must use
// UpdateSessionRunStatus, which rejects terminal-to-active regressions.
func ReopenSessionRun(sessionDir, runID, status, message string) error {
	if runID == "" || status == "" {
		return fmt.Errorf("session run ID and status are required")
	}
	if status != "created" && status != "queued" && status != "running" {
		return fmt.Errorf("invalid reopened session run status: %s", status)
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sessionID string
	if err := tx.QueryRow(`SELECT session_id FROM session_runs WHERE id = ?`, runID).Scan(&sessionID); err != nil {
		return err
	}
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE session_runs SET status = ?, updated_at = ?, finished_at = NULL, error = ?
		WHERE id = ? AND status IN ('completed', 'incomplete', 'expired', 'failed', 'cancelled', 'canceled', 'timed_out')`,
		status, time.Now().Format(time.RFC3339Nano), message, runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		current, getErr := scanSessionRun(tx.QueryRow(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json FROM session_runs WHERE id = ?`, runID))
		if getErr == sql.ErrNoRows {
			return sql.ErrNoRows
		}
		if getErr != nil {
			return getErr
		}
		if current == nil {
			return sql.ErrNoRows
		}
		if current.Status == status {
			return nil
		}
		return fmt.Errorf("session run %s is not terminal and cannot be reopened from %q", runID, current.Status)
	}
	return tx.Commit()
}

func allowedRunPredecessors(status string) []string {
	switch status {
	case "created":
		return []string{"created"}
	case "queued":
		return []string{"created", "queued"}
	case "running":
		return []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question"}
	case "waiting_for_approval", "waiting_for_question":
		return []string{"running", status}
	case "cancelling":
		return []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling"}
	case "terminalizing":
		return []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling", "terminalizing"}
	case "completed", "incomplete", "failed", "cancelled", "canceled", "timed_out", "expired":
		return []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling", "terminalizing", status}
	default:
		return []string{status}
	}
}

// ListOrphanedSessionRuns returns all runs that are in a non-terminal state.
// This is used during server startup to recover runs that were active when
// the previous server instance stopped.
func ListOrphanedSessionRuns(sessionDir string) ([]SessionRun, error) {
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json FROM session_runs WHERE status IN (` + nonTerminalSessionRunStatusSQL + `) ORDER BY started_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionRun
	for rows.Next() {
		run, err := scanSessionRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *run)
	}
	return result, rows.Err()
}
