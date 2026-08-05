package channels

import (
	"testing"
)

func TestPromoteFailureUnderflowsPendingEntrants(t *testing.T) {
	d := &Dispatcher{sessions: make(map[string]*ChannelSession)}
	key := sessionKey("wechat", "underflow-user")
	sess := &ChannelSession{ID: "session-underflow", Platform: "wechat", UserID: "underflow-user"}
	d.sessions[key] = sess

	leaseA, ok := d.acquireSessionLease(key, "wechat", "underflow-user", sess)
	if !ok {
		t.Fatal("acquire lease A failed")
	}
	leaseB, ok := d.acquireSessionLease(key, "wechat", "underflow-user", sess)
	if !ok {
		t.Fatal("acquire lease B failed")
	}
	if sess.pendingEntrants != 2 {
		t.Fatalf("pendingEntrants = %d, want 2", sess.pendingEntrants)
	}
	_ = leaseB

	// Invalidate the session while both leases are still pending.
	d.mu.Lock()
	d.invalidateSessionLocked(key, sess)
	d.mu.Unlock()
	if !sess.invalidated {
		t.Fatal("session was not invalidated")
	}

	// First lease fails to promote (session invalidated).
	if leaseA.promoteAfterRuntimeLock() {
		t.Fatal("lease A promoted despite invalidation")
	}

	// leaseA's failure must release exactly one pending entrant. leaseB is
	// still pending, so the session must NOT be evicted yet.
	d.mu.Lock()
	stillPresent := d.sessions[key] == sess
	pending := sess.pendingEntrants
	d.mu.Unlock()
	if !stillPresent {
		t.Fatal("session was evicted while lease B was still pending (double-decrement)")
	}
	if pending != 1 {
		t.Fatalf("pendingEntrants = %d after lease A failure, want 1", pending)
	}
}

func TestLeaseGenerationRejectsStaleEntrant(t *testing.T) {
	d := &Dispatcher{sessions: make(map[string]*ChannelSession)}
	key := sessionKey("wechat", "generation-user")
	sess := &ChannelSession{ID: "session-generation", Platform: "wechat", UserID: "generation-user", generation: 1}
	d.sessions[key] = sess
	lease, ok := d.acquireSessionLease(key, "wechat", "generation-user", sess)
	if !ok {
		t.Fatal("acquire lease failed")
	}
	d.mu.Lock()
	sess.generation++
	d.mu.Unlock()
	if lease.promoteAfterRuntimeLock() {
		t.Fatal("stale generation was promoted")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if sess.pendingEntrants != 0 || sess.activeRuns != 0 {
		t.Fatalf("stale lease counters = pending=%d active=%d", sess.pendingEntrants, sess.activeRuns)
	}
}

func TestInvalidatedSessionStaysUntilActiveLeaseReleases(t *testing.T) {
	d := &Dispatcher{sessions: make(map[string]*ChannelSession)}
	key := sessionKey("wechat", "active-user")
	sess := &ChannelSession{ID: "session-active", Platform: "wechat", UserID: "active-user"}
	d.sessions[key] = sess
	lease, ok := d.acquireSessionLease(key, "wechat", "active-user", sess)
	if !ok {
		t.Fatal("acquire active lease failed")
	}
	if !lease.promoteAfterRuntimeLock() {
		t.Fatal("active lease did not promote")
	}
	d.mu.Lock()
	d.invalidateSessionLocked(key, sess)
	d.mu.Unlock()
	if d.GetSession(key) == nil {
		t.Fatal("active invalidated session was evicted too early")
	}
	lease.release()
	if d.GetSession(key) != nil {
		t.Fatal("session remained after active lease release")
	}
}
