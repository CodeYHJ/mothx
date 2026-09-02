package session

import "testing"

// TestLockRegistryEvictsUnreferencedEntries locks the M4 fix: per-key mutexes
// must be removed once no goroutine references them, so long-running processes
// do not accumulate one mutex per historical key. Mutual exclusion must still
// hold across an eviction boundary.
func TestLockRegistryEvictsUnreferencedEntries(t *testing.T) {
	registry := newLockRegistry()

	lock := registry.acquire("k")
	registry.mu.Lock()
	_, present := registry.locks["k"]
	registry.mu.Unlock()
	if !present {
		t.Fatal("entry missing after acquire")
	}

	lock.Lock()
	lock.Unlock()
	registry.drop("k", lock)

	registry.mu.Lock()
	_, stillPresent := registry.locks["k"]
	registry.mu.Unlock()
	if stillPresent {
		t.Fatal("entry not evicted after the last reference dropped")
	}
}

// TestLockRegistryKeepsEntryWhileReferenced proves an entry survives while any
// reference remains, so two concurrent holders never operate on different
// mutexes for the same key.
func TestLockRegistryKeepsEntryWhileReferenced(t *testing.T) {
	registry := newLockRegistry()

	first := registry.acquire("k")
	second := registry.acquire("k")

	first.Lock()
	first.Unlock()
	registry.drop("k", first)

	registry.mu.Lock()
	_, present := registry.locks["k"]
	registry.mu.Unlock()
	if !present {
		t.Fatal("entry evicted while a second reference was still held")
	}

	second.Lock()
	second.Unlock()
	registry.drop("k", second)

	registry.mu.Lock()
	_, stillPresent := registry.locks["k"]
	registry.mu.Unlock()
	if stillPresent {
		t.Fatal("entry not evicted after every reference dropped")
	}
}
