package session

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// RuntimeLeasePurpose describes why a process owns the Session-wide lease.
// Legacy rows use RuntimeLeasePurposeLegacyRun until all callers have migrated
// to an explicit purpose.
type RuntimeLeasePurpose string

const (
	RuntimeLeasePurposeLegacyRun RuntimeLeasePurpose = "run"
	RuntimeLeasePurposeAdmission RuntimeLeasePurpose = "admission"
	RuntimeLeasePurposeExecution RuntimeLeasePurpose = "execution"
	RuntimeLeasePurposeRecovery  RuntimeLeasePurpose = "recovery"
	RuntimeLeasePurposeMutation  RuntimeLeasePurpose = "mutation"
	RuntimeLeasePurposeFork      RuntimeLeasePurpose = "fork"
)

// RuntimeLeaseSnapshot is an immutable database view of one Session lease.
// TokenHash is an internal identity component used only for matching a
// Runtime-owned binding; callers must never accept it back as authorization.
type RuntimeLeaseSnapshot struct {
	SessionID       string
	OwnerInstanceID string
	OwnerPID        int
	OwnerKind       string
	TokenHash       string
	Epoch           int64
	RunID           string
	Purpose         RuntimeLeasePurpose
	State           string
	AcquiredAt      time.Time
	HeartbeatAt     time.Time
	ExpiresAt       time.Time
	UpdatedAt       time.Time
	Valid           bool
}

// SessionExecutionFacts contains the durable Run and lease rows observed from
// one SQLite read transaction and one SQLite clock sample. ActiveRuns normally
// contains at most one row; retaining the slice lets Runtime surface corrupted
// or legacy databases with multiple active rows as inconsistent.
type SessionExecutionFacts struct {
	DatabaseIdentity string
	SessionID        string
	SessionExists    bool
	DatabaseNow      time.Time
	ActiveRuns       []SessionRun
	Lease            *RuntimeLeaseSnapshot
	Recovery         *SessionRunRecovery
	RemoteRun        *ResponseRun
}

// ReadSessionExecutionFacts reads the durable execution facts for one Session
// from a single transaction. It deliberately does not consult process-local
// runtime state; internal/agentruntime combines this immutable view with its
// own registered execution bindings.
func ReadSessionExecutionFacts(sessionDir, sessionID string) (SessionExecutionFacts, error) {
	sessionID = strings.TrimSpace(sessionID)
	facts := SessionExecutionFacts{DatabaseIdentity: runtimeDatabaseIdentity(sessionDir), SessionID: sessionID}
	if sessionID == "" {
		return facts, fmt.Errorf("session ID is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return facts, err
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return facts, err
	}
	defer func() { _ = tx.Rollback() }()

	now, err := sqliteNow(tx)
	if err != nil {
		return facts, fmt.Errorf("read SQLite execution clock: %w", err)
	}
	facts.DatabaseNow = time.Unix(now, 0).UTC()
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)`, sessionID).Scan(&facts.SessionExists); err != nil {
		return facts, fmt.Errorf("inspect execution session: %w", err)
	}

	rows, err := tx.Query(`SELECT id, session_id, intent_id, retry_of, attempt, work_dir, source, model, mode, status, started_at, updated_at, finished_at, error, error_info_json, progress_json, usage_json, context_usage_json
		FROM session_runs WHERE session_id = ? AND status IN (`+nonTerminalSessionRunStatusSQL+`) ORDER BY started_at DESC`, sessionID)
	if err != nil {
		return facts, fmt.Errorf("read active session runs: %w", err)
	}
	for rows.Next() {
		run, scanErr := scanSessionRun(rows)
		if scanErr != nil {
			_ = rows.Close()
			return facts, fmt.Errorf("scan active session run: %w", scanErr)
		}
		facts.ActiveRuns = append(facts.ActiveRuns, *run)
	}
	if err := rows.Close(); err != nil {
		return facts, fmt.Errorf("close active session runs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return facts, fmt.Errorf("iterate active session runs: %w", err)
	}
	if len(facts.ActiveRuns) == 1 {
		activeRunID := facts.ActiveRuns[0].ID
		recovery, recoveryErr := readSessionRunRecoveryTx(tx, activeRunID)
		if recoveryErr != nil && recoveryErr != sql.ErrNoRows {
			return facts, fmt.Errorf("read session run recovery: %w", recoveryErr)
		}
		if recoveryErr == nil {
			facts.Recovery = recovery
		}
		remote, remoteErr := readLinkedResponseRunTx(tx, sessionID, activeRunID)
		if remoteErr != nil && remoteErr != sql.ErrNoRows {
			return facts, fmt.Errorf("read linked remote run: %w", remoteErr)
		}
		if remoteErr == nil {
			facts.RemoteRun = remote
		}
	}

	var lease RuntimeLeaseSnapshot
	var acquiredAt, heartbeatAt, expiresAt, updatedAt int64
	err = tx.QueryRow(`SELECT session_id, owner_instance_id, owner_pid, owner_kind, lease_token_hash, epoch, run_id, purpose, state, acquired_at, heartbeat_at, expires_at, updated_at
		FROM session_runtime_leases WHERE session_id = ?`, sessionID).Scan(
		&lease.SessionID, &lease.OwnerInstanceID, &lease.OwnerPID, &lease.OwnerKind, &lease.TokenHash,
		&lease.Epoch, &lease.RunID, &lease.Purpose, &lease.State,
		&acquiredAt, &heartbeatAt, &expiresAt, &updatedAt,
	)
	if err != nil && err != sql.ErrNoRows {
		return facts, fmt.Errorf("read session runtime lease: %w", err)
	}
	if err == nil {
		lease.AcquiredAt = time.Unix(acquiredAt, 0).UTC()
		lease.HeartbeatAt = time.Unix(heartbeatAt, 0).UTC()
		lease.ExpiresAt = time.Unix(expiresAt, 0).UTC()
		lease.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		lease.Valid = lease.State == "active" && expiresAt > now
		facts.Lease = &lease
	}

	if err := tx.Commit(); err != nil {
		return facts, err
	}
	return facts, nil
}

func readLinkedResponseRunTx(tx *sql.Tx, sessionID, runID string) (*ResponseRun, error) {
	var run ResponseRun
	var responseID, pollingURL sql.NullString
	var messageID, sequence sql.NullInt64
	var createdAt, updatedAt string
	err := tx.QueryRow(`SELECT id, session_id, local_run_id, local_turn_id, message_id, response_id, provider, api, state,
		polling_url, last_event_sequence, cancel_requested, created_at, updated_at
		FROM response_runs
		WHERE session_id = ? AND (local_turn_id = ? OR substr(local_turn_id, 1, length(?) + 1) = ? || ':')
		ORDER BY updated_at DESC, id DESC LIMIT 1`, sessionID, runID, runID, runID).Scan(
		&run.ID, &run.SessionID, &run.LocalRunID, &run.LocalTurnID, &messageID, &responseID, &run.Provider,
		&run.API, &run.State, &pollingURL, &sequence, &run.CancelRequested, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	run.ResponseID = responseID.String
	run.MessageID = nullableInt64Value(messageID)
	run.PollingURL = pollingURL.String
	run.LastEventSequence = nullableInt64Value(sequence)
	run.CreatedAt = parseSessionTimestamp(createdAt)
	run.UpdatedAt = parseSessionTimestamp(updatedAt)
	return &run, nil
}
