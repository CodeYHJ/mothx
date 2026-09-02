package session

import "sync"

// countedMutex is a mutex tracked by reference count so its registry entry can
// be removed after the last holder releases it.
type countedMutex struct {
	sync.Mutex
	refs int
}

// lockRegistry is a process-local map of per-key mutexes whose entries are
// evicted once no goroutine references them anymore. Long-running serve
// processes otherwise accumulate one mutex per historical session or identity
// key forever. Mutual exclusion across an eviction boundary still holds
// because a key's entry is only removed after every holder unlocked it and
// dropped its reference.
type lockRegistry struct {
	mu    sync.Mutex
	locks map[string]*countedMutex
}

func newLockRegistry() *lockRegistry {
	return &lockRegistry{locks: make(map[string]*countedMutex)}
}

// acquire returns the mutex for key with its reference count incremented.
// Every acquire must be paired with exactly one drop, whether or not the
// caller managed to lock the mutex.
func (r *lockRegistry) acquire(key string) *countedMutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.locks[key]
	if lock == nil {
		lock = &countedMutex{}
		r.locks[key] = lock
	}
	lock.refs++
	return lock
}

// drop decrements the reference count for key and forgets the entry once no
// reference remains. Call it only after releasing the mutex (or after a
// failed TryLock that never acquired it).
func (r *lockRegistry) drop(key string, lock *countedMutex) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock.refs--
	if lock.refs <= 0 {
		delete(r.locks, key)
	}
}
