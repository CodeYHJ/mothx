package session

import (
	"path/filepath"
	"sort"
	"sync"
)

var runtimeLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

var sessionDataLocks = struct {
	sync.Mutex
	locks map[string]*sync.Mutex
}{locks: make(map[string]*sync.Mutex)}

func runtimeLockKey(sessionDir, sessionID string) string {
	return filepath.Clean(sessionDir) + "\x00" + sessionID
}

// TryLockRuntime serializes agent turns across all serve entry points that use
// the same session database and session ID.
func TryLockRuntime(sessionDir, sessionID string) (func(), bool) {
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
	return lock.Unlock, true
}

// LockRuntime acquires the shared runtime lock.
func LockRuntime(sessionDir, sessionID string) func() {
	if sessionID == "" {
		return func() {}
	}
	key := runtimeLockKey(sessionDir, sessionID)
	runtimeLocks.Lock()
	lock := runtimeLocks.locks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		runtimeLocks.locks[key] = lock
	}
	runtimeLocks.Unlock()
	lock.Lock()
	return lock.Unlock
}

// LockSessionData serializes short persistence mutations that are allowed
// while an Agent run is active, such as channel-tool policy updates. Session
// deletion takes the same lock after acquiring the runtime lock so it cannot
// race a tool-policy transaction.
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

// TryLockRuntimes acquires the runtime locks for a set of sessions in a stable
// order. It is used by binding transfers so concurrent management operations
// cannot deadlock or observe only half of the pair.
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
