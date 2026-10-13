package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileLockExclusiveAndRelease(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root, "mothx")

	release, err := m.LockDir(context.Background(), "/repo/wt")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	// A second acquire with a short timeout must fail while the lock is held.
	blocked := newFileLock(root, "/repo/wt")
	blocked.staleTTL = time.Hour
	blocked.interval = 10 * time.Millisecond
	blocked.timeout = 100 * time.Millisecond
	if _, err := blocked.acquire(context.Background()); err == nil {
		t.Fatal("expected the second acquire to time out")
	}
	release()

	release2, err := m.LockDir(context.Background(), "/repo/wt")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}

func TestFileLockReclaimsStaleLock(t *testing.T) {
	root := t.TempDir()
	lock := newFileLock(root, "/repo/stale")
	if err := os.MkdirAll(filepath.Dir(lock.path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock.path, []byte("999999 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock.path, old, old); err != nil {
		t.Fatal(err)
	}
	lock.staleTTL = time.Minute
	lock.timeout = time.Second
	lock.interval = 5 * time.Millisecond

	release, err := lock.acquire(context.Background())
	if err != nil {
		t.Fatalf("stale lock was not reclaimed: %v", err)
	}
	release()
}

func TestFileLockHeartbeatKeepsHeldLockFresh(t *testing.T) {
	root := t.TempDir()
	lock := newFileLock(root, "/repo/heartbeat")
	lock.staleTTL = 200 * time.Millisecond
	lock.interval = 5 * time.Millisecond
	lock.timeout = time.Second

	release, err := lock.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	// Wait several staleTTLs. The heartbeat (staleTTL/10) must keep the mtime
	// fresh so another process never reclaims a legitimately held lock during
	// a long populate/Reset.
	time.Sleep(3 * lock.staleTTL)
	info, err := os.Stat(lock.path)
	if err != nil {
		t.Fatalf("held lock disappeared: %v", err)
	}
	if age := time.Since(info.ModTime()); age > lock.staleTTL {
		t.Fatalf("held lock looks stale: mtime age %s > staleTTL %s", age, lock.staleTTL)
	}
	// Release stops the heartbeat and removes the file; a later tick must not
	// resurrect a lock this process no longer owns.
	release()
	time.Sleep(lock.staleTTL / 5)
	if _, err := os.Stat(lock.path); !os.IsNotExist(err) {
		t.Fatalf("lock file was not removed or was resurrected: err=%v", err)
	}
}
