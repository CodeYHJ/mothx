package session

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	runtimeLeaseTTL       = 15 * time.Second
	runtimeHeartbeatEvery = 3 * time.Second
)

var (
	ErrRuntimeLeaseBusy = errors.New("session runtime lease is held by another process")
	ErrRuntimeLeaseLost = errors.New("session runtime lease was lost")
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
	sessionDir string
	sessionID  string
	ownerID    string
	tokenHash  string
	epoch      int64
	stop       chan struct{}
	lost       chan struct{}
	stopOnce   sync.Once
	lostOnce   sync.Once
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
	var now int64
	if err := tx.QueryRow(`SELECT CAST(strftime('%s','now') AS INTEGER)`).Scan(&now); err != nil {
		return 0, err
	}
	return now, nil
}

func acquireRuntimeLease(sessionDir, sessionID, purpose string) (*runtimeLease, error) {
	if sessionID == "" {
		return nil, ErrRuntimeLeaseBusy
	}
	if sessionDir == "" {
		// Test and embedded in-memory adapters may intentionally omit a session
		// root. Production adapters resolve Settings.GetSessionDir before
		// admission, so only this compatibility path remains process-local.
		return nil, nil
	}
	db, err := OpenRootDB(sessionDir)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now, err := sqliteNow(tx)
	if err != nil {
		return nil, err
	}
	var sessionExists int
	if err := tx.QueryRow(`SELECT 1 FROM sessions WHERE id = ?`, sessionID).Scan(&sessionExists); err == sql.ErrNoRows {
		// A few adapters reserve a client-side ID before the first durable
		// session insert. Keep the local guard until that first write creates
		// the row; a lease cannot exist for a row that does not exist yet.
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	expires := now + int64(runtimeLeaseTTL/time.Second)
	ownerID := runtimeOwnerID()
	tokenHash := newLeaseTokenHash()
	lease := &runtimeLease{sessionDir: sessionDir, sessionID: sessionID, ownerID: ownerID, tokenHash: tokenHash, stop: make(chan struct{}), lost: make(chan struct{})}

	var currentOwner, currentToken, currentPurpose string
	var currentEpoch, currentExpiry int64
	err = tx.QueryRow(`SELECT owner_instance_id, lease_token_hash, epoch, purpose, expires_at
		FROM session_runtime_leases WHERE session_id = ?`, sessionID).
		Scan(&currentOwner, &currentToken, &currentEpoch, &currentPurpose, &currentExpiry)
	switch {
	case err == sql.ErrNoRows:
		lease.epoch = 1
		_, err = tx.Exec(`INSERT INTO session_runtime_leases
			(session_id, owner_instance_id, owner_pid, owner_kind, lease_token_hash, epoch, run_id, purpose, state, acquired_at, heartbeat_at, expires_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, 'active', ?, ?, ?, ?)`,
			sessionID, ownerID, os.Getpid(), "process", tokenHash, lease.epoch, purpose, now, now, expires, now)
	case err != nil:
		return nil, err
	case currentExpiry > now:
		return nil, ErrRuntimeLeaseBusy
	default:
		lease.epoch = currentEpoch + 1
		result, updateErr := tx.Exec(`UPDATE session_runtime_leases SET
			owner_instance_id = ?, owner_pid = ?, owner_kind = ?, lease_token_hash = ?, epoch = ?, run_id = '', purpose = ?, state = 'active', acquired_at = ?, heartbeat_at = ?, expires_at = ?, updated_at = ?
			WHERE session_id = ? AND epoch = ? AND expires_at <= ?`,
			ownerID, os.Getpid(), "process", tokenHash, lease.epoch, purpose, now, now, expires, now,
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
	return lease, nil
}

func leaseHeartbeat(lease *runtimeLease) {
	ticker := time.NewTicker(runtimeHeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			db, err := OpenRootDB(lease.sessionDir)
			if err != nil {
				continue
			}
			result, updateErr := db.Exec(`UPDATE session_runtime_leases SET
				heartbeat_at = CAST(strftime('%s','now') AS INTEGER),
				expires_at = CAST(strftime('%s','now') AS INTEGER) + ?,
				updated_at = CAST(strftime('%s','now') AS INTEGER)
				WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ?
				AND state = 'active'
				AND expires_at > CAST(strftime('%s','now') AS INTEGER)`,
				int64(runtimeLeaseTTL/time.Second), lease.sessionID, lease.ownerID, lease.epoch, lease.tokenHash)
			if updateErr != nil {
				continue
			}
			count, countErr := result.RowsAffected()
			if countErr == nil && count == 0 {
				forgetRuntimeLease(lease)
				lease.lostOnce.Do(func() { close(lease.lost) })
				return
			}
		case <-lease.stop:
			return
		}
	}
}

func (lease *runtimeLease) release() {
	if lease == nil {
		return
	}
	lease.stopOnce.Do(func() { close(lease.stop) })
	forgetRuntimeLease(lease)
	db, err := OpenRootDB(lease.sessionDir)
	if err != nil {
		return
	}
	// Keep a released tombstone. Removing the row would make a delayed write
	// from an old owner indistinguishable from a legacy cold write after the
	// new owner has finished and released its lease.
	_, _ = db.Exec(`UPDATE session_runtime_leases SET
		state = 'released',
		expires_at = CAST(strftime('%s','now') AS INTEGER),
		heartbeat_at = CAST(strftime('%s','now') AS INTEGER),
		updated_at = CAST(strftime('%s','now') AS INTEGER)
		WHERE session_id = ? AND owner_instance_id = ? AND epoch = ? AND lease_token_hash = ? AND state = 'active'`,
		lease.sessionID, lease.ownerID, lease.epoch, lease.tokenHash)
}

// validateRuntimeLeaseTx fences transcript writes from a stale process. A
// session with no lease is a cold/manual mutation; once a lease row exists,
// only the current owner and epoch may append entries.
func validateRuntimeLeaseTx(tx *sql.Tx, sessionDir, sessionID string) error {
	var ownerID, tokenHash, state string
	var epoch, expiresAt int64
	err := tx.QueryRow(`SELECT owner_instance_id, lease_token_hash, epoch, state, expires_at FROM session_runtime_leases WHERE session_id = ?`, sessionID).
		Scan(&ownerID, &tokenHash, &epoch, &state, &expiresAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var now int64
	if err := tx.QueryRow(`SELECT CAST(strftime('%s','now') AS INTEGER)`).Scan(&now); err != nil {
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

// TryLockRuntime serializes one session across all processes. The process-local
// mutex remains a fast path, while the SQLite lease is the authority and is
// automatically renewed until release or lease loss.
func TryLockRuntime(sessionDir, sessionID string) (func(), bool) {
	return tryLockRuntimePurpose(sessionDir, sessionID, "run")
}

func tryLockRuntimePurpose(sessionDir, sessionID, purpose string) (func(), bool) {
	if sessionID == "" {
		return func() {}, false
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
		return func() {}, false
	}
	lease, err := acquireRuntimeLease(sessionDir, sessionID, purpose)
	if err != nil {
		lock.Unlock()
		return func() {}, false
	}
	return func() {
		if lease != nil {
			lease.release()
		}
		lock.Unlock()
	}, true
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
