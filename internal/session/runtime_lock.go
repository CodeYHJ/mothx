package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	runtimeLeaseTTL       = 15 * time.Second
	runtimeHeartbeatEvery = 3 * time.Second
	runtimeHeartbeatRetry = 2 * time.Second
)

var (
	ErrRuntimeLeaseBusy         = errors.New("session runtime lease is held by another process")
	ErrRuntimeLeaseLost         = errors.New("session runtime lease was lost")
	ErrRuntimeSessionNotFound   = errors.New("session runtime lease requires an existing session")
	ErrSessionRunActive         = errors.New("session has an active durable run")
	ErrSessionRecoveryRequired  = errors.New("session has an active durable run that requires reconciliation")
	ErrSessionRecoveryNotNeeded = errors.New("session has no active durable run to recover")
	ErrRuntimeLeaseRunMismatch  = errors.New("session runtime lease run does not match the active durable run")
	ErrRuntimeLeasePurpose      = errors.New("session runtime lease purpose does not allow this operation")
)

var runtimeProcess = struct {
	sync.Once
	id string
}{}

func runtimeOwnerID() string {
	runtimeProcess.Do(func() {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			runtimeProcess.id = fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
			return
		}
		runtimeProcess.id = fmt.Sprintf("pid-%d-%s", os.Getpid(), hex.EncodeToString(nonce[:]))
	})
	return runtimeProcess.id
}

var runtimeLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

var sessionDataLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

var activeRuntimeLeases = struct {
	sync.Mutex
	leases map[string]*runtimeLease
}{leases: make(map[string]*runtimeLease)}

func runtimeLockKey(sessionDir, sessionID string) string {
	clean := filepath.Clean(sessionDir)
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}
	return clean + "\x00" + sessionID
}

type runtimeLease struct {
	bindingMu  sync.RWMutex
	sessionDir string
	sessionID  string
	ownerID    string
	purpose    string
	runID      string
	tokenHash  string
	epoch      int64
	stop       chan struct{}
	lost       chan struct{}
	stopOnce   sync.Once
	lostOnce   sync.Once
	// refs includes the caller-owned RuntimeLeaseGuard plus any Runtime-owned
	// execution retention. The durable lease is released only after the last
	// reference drops, so an adapter cannot accidentally revoke authority while
	// terminal persistence is still retrying in the shared Runtime.
	refs     int
	released bool
}

type runtimeLeaseAcquireMode uint8

const (
	runtimeLeaseAcquireLegacy runtimeLeaseAcquireMode = iota
	runtimeLeaseAcquireNoActiveRun
	runtimeLeaseAcquireRecovery
)

type runtimeLeaseAcquireOptions struct {
	purpose             RuntimeLeasePurpose
	runID               string
	mode                runtimeLeaseAcquireMode
	allowMissingSession bool
}

func rememberRuntimeLease(lease *runtimeLease) {
	if lease == nil {
		return
	}
	activeRuntimeLeases.Lock()
	activeRuntimeLeases.leases[runtimeLockKey(lease.sessionDir, lease.sessionID)] = lease
	activeRuntimeLeases.Unlock()
}

func forgetRuntimeLease(lease *runtimeLease) {
	if lease == nil {
		return
	}
	activeRuntimeLeases.Lock()
	key := runtimeLockKey(lease.sessionDir, lease.sessionID)
	if current := activeRuntimeLeases.leases[key]; current == lease {
		delete(activeRuntimeLeases.leases, key)
	}
	activeRuntimeLeases.Unlock()
}

// RuntimeLeaseLost returns the loss signal for the current process lease. It
// is intentionally read-only; callers use it to cancel work while every
// durable write still performs its own epoch/token fence check.
func RuntimeLeaseLost(sessionDir, sessionID string) <-chan struct{} {
	activeRuntimeLeases.Lock()
	lease := activeRuntimeLeases.leases[runtimeLockKey(sessionDir, sessionID)]
	activeRuntimeLeases.Unlock()
	if lease == nil {
		return nil
	}
	return lease.lost
}

func newLeaseTokenHash() string {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	hash := sha256.Sum256(token[:])
	return hex.EncodeToString(hash[:])
}

func sqliteNow(tx *sql.Tx) (int64, error) {
	return sqliteNowContext(tx, context.Background())
}

func sqliteNowContext(tx *sql.Tx, ctx context.Context) (int64, error) {
	var now int64
	if err := tx.QueryRowContext(ctx, `SELECT CAST(strftime('%s','now') AS INTEGER)`).Scan(&now); err != nil {
		return 0, err
	}
	return now, nil
}

func acquireRuntimeLease(sessionDir, sessionID, purpose string) (*runtimeLease, error) {
	return acquireRuntimeLeaseWithOptions(sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose: RuntimeLeasePurpose(purpose), allowMissingSession: true,
	})
}

func acquireRuntimeLeaseWithOptions(sessionDir, sessionID string, options runtimeLeaseAcquireOptions) (*runtimeLease, error) {
	return acquireRuntimeLeaseWithOptionsContext(context.Background(), sessionDir, sessionID, options)
}

func acquireRuntimeLeaseWithOptionsContext(ctx context.Context, sessionDir, sessionID string, options runtimeLeaseAcquireOptions) (*runtimeLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionID == "" {
		return nil, ErrRuntimeLeaseBusy
	}
	if sessionDir == "" {
		// Test and embedded in-memory adapters may intentionally omit a session
		// root. Production adapters resolve Settings.GetSessionDir before
		// admission, so only this compatibility path remains process-local.
		if options.allowMissingSession {
			return nil, nil
		}
		return nil, fmt.Errorf("session runtime directory is required")
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now, err := sqliteNowContext(tx, ctx)
	if err != nil {
		return nil, err
	}
	var sessionExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&sessionExists); err == sql.ErrNoRows {
		if options.allowMissingSession {
			// Compatibility for legacy callers that reserve a client-side ID before
			// the first durable Session insert. New explicit acquisition APIs reject
			// this process-local-only path.
			return nil, nil
		}
		return nil, ErrRuntimeSessionNotFound
	} else if err != nil {
		return nil, err
	}
	expires := now + int64(runtimeLeaseTTL/time.Second)
	ownerID := runtimeOwnerID()
	tokenHash := newLeaseTokenHash()
	purpose := string(options.purpose)
	lease := &runtimeLease{sessionDir: sessionDir, sessionID: sessionID, ownerID: ownerID, purpose: purpose, runID: options.runID, tokenHash: tokenHash, stop: make(chan struct{}), lost: make(chan struct{}), refs: 1}

	var currentOwner, currentToken, currentPurpose, currentState string
	var currentEpoch, currentExpiry int64
	err = tx.QueryRowContext(ctx, `SELECT owner_instance_id, lease_token_hash, epoch, purpose, state, expires_at
		FROM session_runtime_leases WHERE session_id = ?`, sessionID).
		Scan(&currentOwner, &currentToken, &currentEpoch, &currentPurpose, &currentState, &currentExpiry)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil && currentState == "active" && currentExpiry > now {
		return nil, ErrRuntimeLeaseBusy
	}
	if options.mode != runtimeLeaseAcquireLegacy {
		activeRunIDs, activeErr := activeSessionRunIDsTxContext(ctx, tx, sessionID)
		if activeErr != nil {
			return nil, activeErr
		}
		switch options.mode {
		case runtimeLeaseAcquireNoActiveRun:
			if len(activeRunIDs) != 0 {
				if options.purpose == RuntimeLeasePurposeAdmission {
					return nil, ErrSessionRecoveryRequired
				}
				return nil, ErrSessionRunActive
			}
		case runtimeLeaseAcquireRecovery:
			if len(activeRunIDs) == 0 {
				return nil, ErrSessionRecoveryNotNeeded
			}
			if len(activeRunIDs) != 1 || activeRunIDs[0] != options.runID {
				return nil, ErrRuntimeLeaseRunMismatch
			}
		}
	}
	switch {
	case err == sql.ErrNoRows:
		lease.epoch = 1
		_, err = tx.ExecContext(ctx, `INSERT INTO session_runtime_leases
			(session_id, owner_instance_id, owner_pid, owner_kind, lease_token_hash, epoch, run_id, purpose, state, acquired_at, heartbeat_at, expires_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
			sessionID, ownerID, os.Getpid(), "process", tokenHash, lease.epoch, options.runID, purpose, now, now, expires, now)
	default:
		lease.epoch = currentEpoch + 1
		result, updateErr := tx.ExecContext(ctx, `UPDATE session_runtime_leases SET
			owner_instance_id = ?, owner_pid = ?, owner_kind = ?, lease_token_hash = ?, epoch = ?, run_id = ?, purpose = ?, state = 'active', acquired_at = ?, heartbeat_at = ?, expires_at = ?, updated_at = ?
			WHERE session_id = ? AND epoch = ? AND (state != 'active' OR expires_at <= ?)`,
			ownerID, os.Getpid(), "process", tokenHash, lease.epoch, options.runID, purpose, now, now, expires, now,
			sessionID, currentEpoch, now)
		if updateErr != nil {
			return nil, updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return nil, countErr
		}
		if count != 1 {
			return nil, ErrRuntimeLeaseBusy
		}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rememberRuntimeLease(lease)
	go leaseHeartbeat(lease)
	publishRuntimeLeaseNotification(RuntimeLeaseNotification{
		Type: "acquired", SessionID: lease.sessionID, Origin: lease.purpose, OwnerInstanceID: lease.ownerID, Epoch: lease.epoch, ExpiresAt: expires,
	})
	return lease, nil
}

func activeSessionRunIDsTx(tx *sql.Tx, sessionID string) ([]string, error) {
	return activeSessionRunIDsTxContext(context.Background(), tx, sessionID)
}

func activeSessionRunIDsTxContext(ctx context.Context, tx *sql.Tx, sessionID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM session_runs WHERE session_id = ? AND status IN (`+nonTerminalSessionRunStatusSQL+`) ORDER BY started_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func leaseHeartbeat(lease *runtimeLease) {
	ticker := time.NewTicker(runtimeHeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !renewRuntimeLease(lease) {
				markRuntimeLeaseLost(lease)
				return
			}
		case <-lease.stop:
			return
		}
	}
}

// renewRuntimeLease retries transient SQLite failures for a bounded interval.
// Continuing an Agent after that interval would permit external side effects
// after the lease can no longer be proven live, so the caller must cancel it.
func renewRuntimeLease(lease *runtimeLease) bool {
	deadline := time.Now().Add(runtimeHeartbeatRetry)
	for {
		db, err := OpenRootDB(lease.sessionDir)
		if err == nil {
			result, updateErr := db.Exec(`UPDATE session_runtime_leases SET
				heartbeat_at = CAST(strftime('%s','now') AS INTEGER),
				expires_at = CAST(strftime('%s','now') AS INTEGER) + ?,
				updated_at = CAST(strftime('%s','now') AS INTEGER)
				WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ?
				AND state = 'active'
				AND expires_at > CAST(strftime('%s','now') AS INTEGER)`,
				int64(runtimeLeaseTTL/time.Second), lease.sessionID, lease.ownerID, lease.epoch, lease.tokenHash)
			if updateErr == nil {
				count, countErr := result.RowsAffected()
				if countErr == nil && count == 1 {
					return true
				}
				if countErr == nil && count == 0 {
					return false
				}
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-lease.stop:
			return true
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func markRuntimeLeaseLost(lease *runtimeLease) {
	if lease == nil {
		return
	}
	lease.bindingMu.Lock()
	if lease.released {
		lease.bindingMu.Unlock()
		return
	}
	lease.released = true
	lease.refs = 0
	purpose := lease.purpose
	ownerID := lease.ownerID
	epoch := lease.epoch
	sessionID := lease.sessionID
	lease.bindingMu.Unlock()
	forgetRuntimeLease(lease)
	lease.lostOnce.Do(func() {
		close(lease.lost)
		publishRuntimeLeaseNotification(RuntimeLeaseNotification{
			Type: "lost", SessionID: sessionID, Origin: purpose, OwnerInstanceID: ownerID, Epoch: epoch,
		})
	})
}

func (lease *runtimeLease) release() {
	if lease == nil {
		return
	}
	lease.bindingMu.Lock()
	if lease.released {
		lease.bindingMu.Unlock()
		return
	}
	if lease.refs > 1 {
		lease.refs--
		lease.bindingMu.Unlock()
		return
	}
	lease.refs = 0
	lease.released = true
	sessionID := lease.sessionID
	ownerID := lease.ownerID
	epoch := lease.epoch
	tokenHash := lease.tokenHash
	purpose := lease.purpose
	lease.bindingMu.Unlock()
	lease.stopOnce.Do(func() { close(lease.stop) })
	forgetRuntimeLease(lease)
	// A voluntary release ends this process's authority just as decisively as
	// heartbeat loss. ExecutionRuntime watches this signal to retire a local
	// registration that could not persist its terminal transition.
	defer lease.lostOnce.Do(func() { close(lease.lost) })
	db, err := OpenRootDB(lease.sessionDir)
	if err != nil {
		return
	}
	// Keep a released tombstone. Removing the row would make a delayed write
	// from an old owner indistinguishable from a legacy cold write after the
	// new owner has finished and released its lease.
	result, _ := db.Exec(`UPDATE session_runtime_leases SET
		state = 'released',
		expires_at = CAST(strftime('%s','now') AS INTEGER),
		heartbeat_at = CAST(strftime('%s','now') AS INTEGER),
		updated_at = CAST(strftime('%s','now') AS INTEGER)
		WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ? AND state = 'active'`,
		sessionID, ownerID, epoch, tokenHash)
	if result != nil {
		if count, err := result.RowsAffected(); err == nil && count == 1 {
			publishRuntimeLeaseNotification(RuntimeLeaseNotification{
				Type: "released", SessionID: sessionID, Origin: purpose, OwnerInstanceID: ownerID, Epoch: epoch,
			})
		}
	}
}

// bindRuntimeLeaseToRunTx transitions the caller's admission/legacy lease to
// an execution lease in the same transaction that creates the durable Run.
// A missing lease row remains a temporary compatibility path for embedded
// stores; once a row exists, exact owner/token/epoch fencing is mandatory.
func bindRuntimeLeaseToRunTx(tx *sql.Tx, sessionDir, sessionID, runID string) (*runtimeLease, error) {
	if tx == nil || sessionID == "" || runID == "" {
		return nil, fmt.Errorf("runtime lease binding requires transaction, session ID, and run ID")
	}
	activeRuntimeLeases.Lock()
	lease := activeRuntimeLeases.leases[runtimeLockKey(sessionDir, sessionID)]
	activeRuntimeLeases.Unlock()
	if lease == nil {
		var exists int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM session_runtime_leases WHERE session_id = ?)`, sessionID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, nil
		}
		return nil, ErrRuntimeLeaseLost
	}
	lease.bindingMu.RLock()
	existingRunID := lease.runID
	purpose := lease.purpose
	lease.bindingMu.RUnlock()
	if purpose != string(RuntimeLeasePurposeAdmission) && purpose != string(RuntimeLeasePurposeLegacyRun) && purpose != string(RuntimeLeasePurposeExecution) && purpose != string(RuntimeLeasePurposeRecovery) {
		return nil, ErrRuntimeLeasePurpose
	}
	if existingRunID != "" && existingRunID != runID {
		return nil, ErrRuntimeLeaseRunMismatch
	}
	result, err := tx.Exec(`UPDATE session_runtime_leases SET
		run_id = ?, purpose = ?, updated_at = CAST(strftime('%s','now') AS INTEGER)
		WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ?
		AND state = 'active' AND expires_at > CAST(strftime('%s','now') AS INTEGER)
		AND purpose IN (?, ?, ?, ?) AND (run_id = '' OR run_id = ?)`,
		runID, string(RuntimeLeasePurposeExecution), sessionID, lease.ownerID, lease.epoch, lease.tokenHash,
		string(RuntimeLeasePurposeAdmission), string(RuntimeLeasePurposeLegacyRun), string(RuntimeLeasePurposeExecution), string(RuntimeLeasePurposeRecovery), runID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrRuntimeLeaseLost
	}
	return lease, nil
}

func markRuntimeLeaseBound(lease *runtimeLease, runID string) {
	if lease == nil {
		return
	}
	lease.bindingMu.Lock()
	lease.runID = runID
	lease.purpose = string(RuntimeLeasePurposeExecution)
	lease.bindingMu.Unlock()
}

// BindRuntimeLeaseToExistingRun promotes a recovery/legacy lease to execution
// only while the expected Run is still the Session's sole non-terminal Run.
// Reattach paths call this before registering an in-memory execution.
func BindRuntimeLeaseToExistingRun(sessionDir, sessionID, runID string) (RuntimeLeaseBinding, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(runID) == "" {
		return RuntimeLeaseBinding{}, ErrRuntimeLeaseRunMismatch
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return RuntimeLeaseBinding{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return RuntimeLeaseBinding{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateRuntimeLeaseTx(tx, sessionDir, sessionID); err != nil {
		return RuntimeLeaseBinding{}, err
	}
	activeRunIDs, err := activeSessionRunIDsTx(tx, sessionID)
	if err != nil {
		return RuntimeLeaseBinding{}, err
	}
	if len(activeRunIDs) != 1 || activeRunIDs[0] != runID {
		return RuntimeLeaseBinding{}, ErrRuntimeLeaseRunMismatch
	}
	lease, err := bindRuntimeLeaseToRunTx(tx, sessionDir, sessionID, runID)
	if err != nil {
		return RuntimeLeaseBinding{}, err
	}
	if lease == nil {
		return RuntimeLeaseBinding{}, nil
	}
	if err := tx.Commit(); err != nil {
		return RuntimeLeaseBinding{}, err
	}
	markRuntimeLeaseBound(lease, runID)
	binding, ok := CurrentRuntimeLeaseBinding(sessionDir, sessionID)
	if !ok {
		return RuntimeLeaseBinding{}, ErrRuntimeLeaseLost
	}
	return binding, nil
}

// validateRuntimeLeaseTx fences transcript writes from a stale process. A
// session with no lease is a cold/manual mutation; once a lease row exists,
// only the current owner and epoch may append entries.
func validateRuntimeLeaseTx(tx *sql.Tx, sessionDir, sessionID string) error {
	return validateRuntimeLeaseTxContext(context.Background(), tx, sessionDir, sessionID)
}

func validateRuntimeLeaseTxContext(ctx context.Context, tx *sql.Tx, sessionDir, sessionID string) error {
	var ownerID, tokenHash, state string
	var epoch, expiresAt int64
	err := tx.QueryRowContext(ctx, `SELECT owner_instance_id, lease_token_hash, epoch, state, expires_at FROM session_runtime_leases WHERE session_id = ?`, sessionID).
		Scan(&ownerID, &tokenHash, &epoch, &state, &expiresAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var now int64
	if err := tx.QueryRowContext(ctx, `SELECT CAST(strftime('%s','now') AS INTEGER)`).Scan(&now); err != nil {
		return err
	}
	activeRuntimeLeases.Lock()
	lease := activeRuntimeLeases.leases[runtimeLockKey(sessionDir, sessionID)]
	activeRuntimeLeases.Unlock()
	if state != "active" || lease == nil || lease.ownerID != ownerID || lease.tokenHash != tokenHash || lease.epoch != epoch || expiresAt <= now {
		return ErrRuntimeLeaseLost
	}
	return nil
}

// validateRuntimeLeaseBindingTx verifies that the current process owns the
// exact purpose/run binding required by a control operation. Adapter input is
// never used as lease authority; expected identity comes from the process-local
// lease handle and is checked again against SQLite under the caller's tx.
func validateRuntimeLeaseBindingTx(tx *sql.Tx, sessionDir, sessionID, runID string, purpose RuntimeLeasePurpose) (RuntimeLeaseBinding, error) {
	return validateRuntimeLeaseBindingTxContext(context.Background(), tx, sessionDir, sessionID, runID, purpose)
}

func validateRuntimeLeaseBindingTxContext(ctx context.Context, tx *sql.Tx, sessionDir, sessionID, runID string, purpose RuntimeLeasePurpose) (RuntimeLeaseBinding, error) {
	if err := validateRuntimeLeaseTxContext(ctx, tx, sessionDir, sessionID); err != nil {
		return RuntimeLeaseBinding{}, err
	}
	binding, ok := CurrentRuntimeLeaseBinding(sessionDir, sessionID)
	if !ok {
		return RuntimeLeaseBinding{}, ErrRuntimeLeaseLost
	}
	var persistedRunID, persistedPurpose string
	if err := tx.QueryRowContext(ctx, `SELECT run_id, purpose FROM session_runtime_leases
		WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ?
		AND state = 'active' AND expires_at > CAST(strftime('%s','now') AS INTEGER)`,
		sessionID, binding.OwnerInstanceID, binding.Epoch, binding.TokenHash).Scan(&persistedRunID, &persistedPurpose); err != nil {
		if err == sql.ErrNoRows {
			return RuntimeLeaseBinding{}, ErrRuntimeLeaseLost
		}
		return RuntimeLeaseBinding{}, err
	}
	if RuntimeLeasePurpose(persistedPurpose) != purpose || binding.Purpose != purpose {
		return RuntimeLeaseBinding{}, ErrRuntimeLeasePurpose
	}
	if persistedRunID != runID || binding.RunID != runID {
		return RuntimeLeaseBinding{}, ErrRuntimeLeaseRunMismatch
	}
	return binding, nil
}

// ValidateRuntimeLeaseContext rechecks the process-owned lease binding in a
// fresh SQLite transaction. It is the final Runtime fence before a side
// effect; callers must not treat client-provided identity as authority.
func ValidateRuntimeLeaseContext(ctx context.Context, sessionDir, sessionID, runID string, purpose RuntimeLeasePurpose) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(sessionDir) == "" {
		return ErrRuntimeLeaseLost
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := validateRuntimeLeaseBindingTxContext(ctx, tx, sessionDir, sessionID, runID, purpose); err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM session_runs WHERE id = ? AND session_id = ?`, runID, sessionID).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return ErrRuntimeLeaseRunMismatch
		}
		return err
	}
	if isTerminalSessionRunStatus(status) {
		return ErrRuntimeLeaseLost
	}
	return nil
}

// ValidateRuntimeLease is the context-free compatibility wrapper.
func ValidateRuntimeLease(sessionDir, sessionID, runID string, purpose RuntimeLeasePurpose) error {
	return ValidateRuntimeLeaseContext(context.Background(), sessionDir, sessionID, runID, purpose)
}

func isTerminalSessionRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "incomplete", "failed", "cancelled", "canceled", "timed_out":
		return true
	default:
		return false
	}
}

// RuntimeLeaseBinding is the Runtime-owned identity of an acquired Session
// lease. It is safe to expose to trusted in-process code for diagnostics and
// matching, but must never be accepted from an adapter or client as authority.
type RuntimeLeaseBinding struct {
	DatabaseIdentity string
	SessionID        string
	RunID            string
	OwnerInstanceID  string
	TokenHash        string
	Epoch            int64
	Purpose          RuntimeLeasePurpose
}

// CurrentRuntimeLeaseBinding returns the lease identity held by this process.
// It is intended for internal/agentruntime registration and diagnostics only;
// durable control operations must still revalidate the database row.
func CurrentRuntimeLeaseBinding(sessionDir, sessionID string) (RuntimeLeaseBinding, bool) {
	activeRuntimeLeases.Lock()
	lease := activeRuntimeLeases.leases[runtimeLockKey(sessionDir, sessionID)]
	activeRuntimeLeases.Unlock()
	if lease == nil {
		return RuntimeLeaseBinding{}, false
	}
	lease.bindingMu.RLock()
	runID := lease.runID
	purpose := lease.purpose
	lease.bindingMu.RUnlock()
	return RuntimeLeaseBinding{
		DatabaseIdentity: runtimeDatabaseIdentity(lease.sessionDir),
		SessionID:        lease.sessionID,
		RunID:            runID,
		OwnerInstanceID:  lease.ownerID,
		TokenHash:        lease.tokenHash,
		Epoch:            lease.epoch,
		Purpose:          RuntimeLeasePurpose(purpose),
	}, true
}

// RetainRuntimeLease adds a Runtime-owned reference to the current execution
// lease. The caller-owned RuntimeLeaseGuard may be released independently; the
// durable lease remains active until the returned release function is called.
// This is intentionally an in-process handoff primitive, never a client-facing
// authorization mechanism.
func RetainRuntimeLease(sessionDir, sessionID, runID string) (RuntimeLeaseBinding, func(), bool, error) {
	if strings.TrimSpace(sessionDir) == "" {
		return RuntimeLeaseBinding{}, nil, false, nil
	}
	activeRuntimeLeases.Lock()
	lease := activeRuntimeLeases.leases[runtimeLockKey(sessionDir, sessionID)]
	activeRuntimeLeases.Unlock()
	if lease == nil {
		// A missing local lease is retained as a compatibility path for embedded
		// stores that predate Runtime admission.
		return RuntimeLeaseBinding{}, nil, false, nil
	}
	lease.bindingMu.Lock()
	if lease.released {
		lease.bindingMu.Unlock()
		return RuntimeLeaseBinding{}, nil, false, ErrRuntimeLeaseLost
	}
	if lease.purpose != string(RuntimeLeasePurposeExecution) {
		lease.bindingMu.Unlock()
		return RuntimeLeaseBinding{}, nil, false, ErrRuntimeLeasePurpose
	}
	if lease.runID != runID {
		lease.bindingMu.Unlock()
		return RuntimeLeaseBinding{}, nil, false, ErrRuntimeLeaseRunMismatch
	}
	lease.refs++
	binding := RuntimeLeaseBinding{
		DatabaseIdentity: runtimeDatabaseIdentity(lease.sessionDir),
		SessionID:        lease.sessionID,
		RunID:            lease.runID,
		OwnerInstanceID:  lease.ownerID,
		TokenHash:        lease.tokenHash,
		Epoch:            lease.epoch,
		Purpose:          RuntimeLeasePurpose(lease.purpose),
	}
	lease.bindingMu.Unlock()
	var once sync.Once
	return binding, func() { once.Do(lease.release) }, true, nil
}

// RuntimeLeaseGuard owns both the process-local mutex and the durable SQLite
// lease. Release is idempotent and must be called after every successful
// acquisition.
type RuntimeLeaseGuard struct {
	lease  *runtimeLease
	unlock func()
	once   sync.Once
}

// RuntimeLeaseGroup owns an ordered set of Session leases acquired for one
// multi-Session mutation.
type RuntimeLeaseGroup struct {
	guards []*RuntimeLeaseGuard
	once   sync.Once
}

// Release relinquishes grouped leases in reverse acquisition order.
func (g *RuntimeLeaseGroup) Release() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		for i := len(g.guards) - 1; i >= 0; i-- {
			g.guards[i].Release()
		}
	})
}

// Release relinquishes the durable lease and then the process-local mutex.
func (g *RuntimeLeaseGuard) Release() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		if g.lease != nil {
			g.lease.release()
		}
		if g.unlock != nil {
			g.unlock()
		}
	})
}

// Lost reports when the durable lease can no longer be renewed.
func (g *RuntimeLeaseGuard) Lost() <-chan struct{} {
	if g == nil || g.lease == nil {
		return nil
	}
	return g.lease.lost
}

// Binding returns the exact identity acquired by this process.
func (g *RuntimeLeaseGuard) Binding() RuntimeLeaseBinding {
	if g == nil || g.lease == nil {
		return RuntimeLeaseBinding{}
	}
	g.lease.bindingMu.RLock()
	runID := g.lease.runID
	purpose := g.lease.purpose
	g.lease.bindingMu.RUnlock()
	return RuntimeLeaseBinding{
		DatabaseIdentity: runtimeDatabaseIdentity(g.lease.sessionDir),
		SessionID:        g.lease.sessionID,
		RunID:            runID,
		OwnerInstanceID:  g.lease.ownerID,
		TokenHash:        g.lease.tokenHash,
		Epoch:            g.lease.epoch,
		Purpose:          RuntimeLeasePurpose(purpose),
	}
}

func runtimeDatabaseIdentity(sessionDir string) string {
	path := rootDBPath(sessionDir)
	if absolute, err := filepath.Abs(filepath.Clean(path)); err == nil {
		return absolute
	}
	return filepath.Clean(path)
}

// RuntimeDatabaseIdentity returns the normalized SQLite identity used to
// scope process-local execution and recovery registries.
func RuntimeDatabaseIdentity(sessionDir string) string {
	return runtimeDatabaseIdentity(sessionDir)
}

func acquireRuntimeLeaseGuard(sessionDir, sessionID string, options runtimeLeaseAcquireOptions) (*RuntimeLeaseGuard, error) {
	return acquireRuntimeLeaseGuardContext(context.Background(), sessionDir, sessionID, options)
}

func acquireRuntimeLeaseGuardContext(ctx context.Context, sessionDir, sessionID string, options runtimeLeaseAcquireOptions) (*RuntimeLeaseGuard, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, ErrRuntimeLeaseBusy
	}
	key := runtimeLockKey(sessionDir, sessionID)
	runtimeLocks.Lock()
	lock := runtimeLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		runtimeLocks.locks[key] = lock
	}
	runtimeLocks.Unlock()
	if !lock.TryLock() {
		return nil, ErrRuntimeLeaseBusy
	}
	lease, err := acquireRuntimeLeaseWithOptionsContext(ctx, sessionDir, sessionID, options)
	if err != nil {
		lock.Unlock()
		return nil, err
	}
	return &RuntimeLeaseGuard{lease: lease, unlock: lock.Unlock}, nil
}

// AcquireExecutionAdmission reserves an existing idle Session for a new Run.
// The durable admission transaction must subsequently bind the new run ID and
// transition this same lease to purpose=execution.
func AcquireExecutionAdmission(sessionDir, sessionID string) (*RuntimeLeaseGuard, error) {
	return acquireRuntimeLeaseGuard(sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose:             RuntimeLeasePurposeAdmission,
		mode:                runtimeLeaseAcquireNoActiveRun,
		allowMissingSession: strings.TrimSpace(sessionDir) == "",
	})
}

// AcquireMutation reserves an idle Session for a short non-execution change.
func AcquireMutation(sessionDir, sessionID string) (*RuntimeLeaseGuard, error) {
	return acquireRuntimeLeaseGuard(sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose: RuntimeLeasePurposeMutation,
		mode:    runtimeLeaseAcquireNoActiveRun,
		// Embedded/test adapters may not have a shared root. Keep this
		// process-local no-op explicit and isolated; production callers resolve
		// a non-empty Settings.SessionDir before admission.
		allowMissingSession: strings.TrimSpace(sessionDir) == "",
	})
}

// AcquireMutations reserves multiple idle Sessions in stable order so a
// cross-Session mutation cannot deadlock another caller taking the same set.
func AcquireMutations(sessionDir string, sessionIDs []string) (*RuntimeLeaseGroup, error) {
	ids := append([]string(nil), sessionIDs...)
	sort.Strings(ids)
	ordered := ids[:0]
	for _, id := range ids {
		if id == "" || (len(ordered) > 0 && ordered[len(ordered)-1] == id) {
			continue
		}
		ordered = append(ordered, id)
	}
	group := &RuntimeLeaseGroup{guards: make([]*RuntimeLeaseGuard, 0, len(ordered))}
	for _, id := range ordered {
		guard, err := AcquireMutation(sessionDir, id)
		if err != nil {
			group.Release()
			return nil, err
		}
		group.guards = append(group.guards, guard)
	}
	return group, nil
}

// AcquireFork reserves an idle source Session while a child snapshot is made.
func AcquireFork(sessionDir, sessionID string) (*RuntimeLeaseGuard, error) {
	return acquireRuntimeLeaseGuard(sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose: RuntimeLeasePurposeFork,
		mode:    runtimeLeaseAcquireNoActiveRun,
	})
}

// AcquireRecovery claims an unowned or expired active Run for fenced recovery.
func AcquireRecovery(sessionDir, sessionID, expectedRunID string) (*RuntimeLeaseGuard, error) {
	return AcquireRecoveryContext(context.Background(), sessionDir, sessionID, expectedRunID)
}

// AcquireRecoveryContext bounds the lease acquisition transaction by the
// recovery attempt deadline.
func AcquireRecoveryContext(ctx context.Context, sessionDir, sessionID, expectedRunID string) (*RuntimeLeaseGuard, error) {
	if strings.TrimSpace(expectedRunID) == "" {
		return nil, ErrRuntimeLeaseRunMismatch
	}
	if ctx == nil {
		ctx = context.Background()
	}
	guard, err := acquireRuntimeLeaseGuardContext(ctx, sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose: RuntimeLeasePurposeRecovery,
		runID:   expectedRunID,
		mode:    runtimeLeaseAcquireRecovery,
	})
	return guard, err
}

// TryLockRuntime serializes one session across all processes. The process-local
// mutex remains a fast path, while the SQLite lease is the authority and is
// automatically renewed until release or lease loss.
func TryLockRuntime(sessionDir, sessionID string) (func(), bool) {
	return tryLockRuntimePurpose(sessionDir, sessionID, "run")
}

func tryLockRuntimePurpose(sessionDir, sessionID, purpose string) (func(), bool) {
	guard, err := acquireRuntimeLeaseGuard(sessionDir, sessionID, runtimeLeaseAcquireOptions{
		purpose:             RuntimeLeasePurpose(purpose),
		mode:                runtimeLeaseAcquireLegacy,
		allowMissingSession: true,
	})
	if err != nil {
		return func() {}, false
	}
	return guard.Release, true
}

// LockRuntime waits for the single-session lease. It is intentionally
// implemented as retrying TryLockRuntime so no database transaction remains
// open while an execution is running.
func LockRuntime(sessionDir, sessionID string) func() {
	for {
		if release, ok := TryLockRuntime(sessionDir, sessionID); ok {
			return release
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// LockSessionData serializes short persistence mutations inside one process.
// Cross-process data consistency still comes from SQLite transactions.
func LockSessionData(sessionDir, sessionID string) func() {
	if sessionID == "" {
		return func() {}
	}
	key := runtimeLockKey(sessionDir, sessionID)
	sessionDataLocks.Lock()
	lock := sessionDataLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		sessionDataLocks.locks[key] = lock
	}
	sessionDataLocks.Unlock()
	lock.Lock()
	return lock.Unlock
}

// TryLockRuntimes acquires multiple session leases in sorted order. Different
// sessions remain independently concurrent; ordering only applies to an
// operation that explicitly spans more than one session.
func TryLockRuntimes(sessionDir string, sessionIDs []string) (func(), bool) {
	ids := append([]string(nil), sessionIDs...)
	sort.Strings(ids)
	ordered := ids[:0]
	for _, id := range ids {
		if id == "" || (len(ordered) > 0 && ordered[len(ordered)-1] == id) {
			continue
		}
		ordered = append(ordered, id)
	}
	releases := make([]func(), 0, len(ordered))
	for _, id := range ordered {
		release, ok := TryLockRuntime(sessionDir, id)
		if !ok {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return func() {}, false
		}
		releases = append(releases, release)
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, true
}
