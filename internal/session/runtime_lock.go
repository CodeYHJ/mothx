package session

import (
	"path/filepath"
	"sync"
)

var runtimeLocks = struct {
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
