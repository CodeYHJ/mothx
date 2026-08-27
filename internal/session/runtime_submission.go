package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrRuntimeSubmissionExists = errors.New("runtime submission already exists")
var ErrRuntimeSubmissionConflict = errors.New("runtime submission key conflicts with another request")

// RuntimeSubmission is the durable admission identity for one original or
// retry submission. Only a digest of the transport key is persisted.
type RuntimeSubmission struct {
	ID                 string
	SessionID          string
	Scope              string
	KeyHash            string
	RequestFingerprint string
	IntentID           string
	RunID              string
	CreatedAt          time.Time
}

// RuntimeSubmissionError carries the canonical Run identity selected by a
// competing or replayed admission.
type RuntimeSubmissionError struct {
	Existing RuntimeSubmission
	Conflict bool
}

func (e *RuntimeSubmissionError) Error() string {
	if e == nil {
		return "runtime submission admission failed"
	}
	if e.Conflict {
		return fmt.Sprintf("%v: existing Run %s", ErrRuntimeSubmissionConflict, e.Existing.RunID)
	}
	return fmt.Sprintf("%v: Run %s", ErrRuntimeSubmissionExists, e.Existing.RunID)
}

func (e *RuntimeSubmissionError) Unwrap() error {
	if e != nil && e.Conflict {
		return ErrRuntimeSubmissionConflict
	}
	return ErrRuntimeSubmissionExists
}

func reserveRuntimeSubmissionTx(tx *sql.Tx, run SessionRun) error {
	keyHash := strings.TrimSpace(run.SubmissionKeyHash)
	if keyHash == "" {
		return nil
	}
	scope := strings.TrimSpace(run.SubmissionScope)
	if scope == "" {
		return fmt.Errorf("runtime submission scope is required")
	}
	fingerprint := strings.TrimSpace(run.SubmissionFingerprint)
	existing, err := getRuntimeSubmissionTx(tx, run.SessionID, scope, keyHash)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		return &RuntimeSubmissionError{Existing: existing, Conflict: existing.RequestFingerprint != "" && fingerprint != "" && existing.RequestFingerprint != fingerprint}
	}
	createdAt := run.StartedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err = tx.Exec(`INSERT INTO runtime_submissions
		(id, session_id, scope, key_hash, request_fingerprint, intent_id, run_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, GenerateID(), run.SessionID, scope, keyHash,
		fingerprint, run.IntentID, run.ID, createdAt.Format(time.RFC3339Nano))
	if err == nil {
		return nil
	}
	// A concurrent process can win the unique constraint after our initial
	// lookup. Resolve the durable winner before returning the typed result.
	existing, lookupErr := getRuntimeSubmissionTx(tx, run.SessionID, scope, keyHash)
	if lookupErr == nil {
		return &RuntimeSubmissionError{Existing: existing, Conflict: existing.RequestFingerprint != "" && fingerprint != "" && existing.RequestFingerprint != fingerprint}
	}
	return err
}

func getRuntimeSubmissionTx(tx *sql.Tx, sessionID, scope, keyHash string) (RuntimeSubmission, error) {
	var submission RuntimeSubmission
	var createdAt string
	err := tx.QueryRow(`SELECT id, session_id, scope, key_hash, request_fingerprint, intent_id, run_id, created_at
		FROM runtime_submissions WHERE session_id = ? AND scope = ? AND key_hash = ?`, sessionID, scope, keyHash).Scan(
		&submission.ID, &submission.SessionID, &submission.Scope, &submission.KeyHash,
		&submission.RequestFingerprint, &submission.IntentID, &submission.RunID, &createdAt)
	if err != nil {
		return RuntimeSubmission{}, err
	}
	submission.CreatedAt = parseSessionTimestamp(createdAt)
	return submission, nil
}

func GetRuntimeSubmission(ctx context.Context, sessionDir, sessionID, scope, keyHash string) (*RuntimeSubmission, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" || strings.TrimSpace(scope) == "" || strings.TrimSpace(keyHash) == "" {
		return nil, nil
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	var submission RuntimeSubmission
	var createdAt string
	err = db.QueryRowContext(ctx, `SELECT id, session_id, scope, key_hash, request_fingerprint, intent_id, run_id, created_at
		FROM runtime_submissions WHERE session_id = ? AND scope = ? AND key_hash = ?`, sessionID, scope, keyHash).Scan(
		&submission.ID, &submission.SessionID, &submission.Scope, &submission.KeyHash,
		&submission.RequestFingerprint, &submission.IntentID, &submission.RunID, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	submission.CreatedAt = parseSessionTimestamp(createdAt)
	return &submission, nil
}
