package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SessionRun is the durable lifecycle record for one agent execution.
type SessionRun struct {
	ID         string
	SessionID  string
	WorkDir    string
	Source     string
	Model      string
	Mode       string
	Status     string
	StartedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
	Error      string
	Usage      json.RawMessage
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
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	_, err = db.Exec(`INSERT INTO session_runs
		(id, session_id, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, usage_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		status=excluded.status, updated_at=excluded.updated_at, finished_at=excluded.finished_at,
		error=excluded.error, usage_json=excluded.usage_json`,
		run.ID, run.SessionID, run.WorkDir, run.Source, run.Model, run.Mode, run.Status,
		run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), finished, run.Error, string(run.Usage))
	return err
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
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	var finished any
	if run.FinishedAt != nil {
		finished = run.FinishedAt.Format(time.RFC3339Nano)
	}
	_, err = db.Exec(`INSERT INTO session_runs
		(id, session_id, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, usage_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.SessionID, run.WorkDir, run.Source, run.Model, run.Mode, run.Status,
		run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), finished, run.Error, string(run.Usage))
	return err
}

func scanSessionRun(scanner interface{ Scan(...any) error }) (*SessionRun, error) {
	var run SessionRun
	var started, updated, usage string
	var finished sql.NullString
	if err := scanner.Scan(&run.ID, &run.SessionID, &run.WorkDir, &run.Source, &run.Model, &run.Mode, &run.Status, &started, &updated, &finished, &run.Error, &usage); err != nil {
		return nil, err
	}
	run.StartedAt = parseSessionTimestamp(started)
	run.UpdatedAt = parseSessionTimestamp(updated)
	if finished.Valid && finished.String != "" {
		value := parseSessionTimestamp(finished.String)
		run.FinishedAt = &value
	}
	run.Usage = json.RawMessage(usage)
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
	run, err := scanSessionRun(db.QueryRow(`SELECT id, session_id, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, usage_json FROM session_runs WHERE id = ?`, runID))
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
	err = db.QueryRow(`SELECT id FROM session_runs WHERE session_id = ? AND status IN ('created', 'queued', 'running', 'cancelling', 'terminalizing') ORDER BY started_at DESC LIMIT 1`, sessionID).Scan(&runID)
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
	rows, err := db.Query(`SELECT id, session_id, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, usage_json FROM session_runs WHERE session_id = ? ORDER BY started_at DESC LIMIT ?`, sessionID, limit)
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
	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		current, getErr := GetSessionRun(sessionDir, runID)
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
	return nil
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
	result, err := db.Exec(`UPDATE session_runs SET status = ?, updated_at = ?, finished_at = NULL, error = ?
		WHERE id = ? AND status IN ('completed', 'incomplete', 'expired', 'failed', 'cancelled', 'canceled', 'timed_out')`,
		status, time.Now().Format(time.RFC3339Nano), message, runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		current, getErr := GetSessionRun(sessionDir, runID)
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
	return nil
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
	rows, err := db.Query(`SELECT id, session_id, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, usage_json FROM session_runs WHERE status IN ('created', 'queued', 'running', 'cancelling', 'terminalizing') ORDER BY started_at ASC`)
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
