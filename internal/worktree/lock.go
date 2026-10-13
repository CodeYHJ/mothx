package worktree

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileLock is a best-effort cross-process lock for one worktree directory. It
// uses an O_CREATE|O_EXCL lock file beside the managed worktrees: portable
// across platforms, dependency-free, and self-healing (a lock older than
// staleTTL is treated as abandoned and reclaimed). It complements, but never
// replaces, the Runtime's in-process per-directory lock.
type fileLock struct {
	path     string
	staleTTL time.Duration
	interval time.Duration
	timeout  time.Duration
}

func newFileLock(root, key string) *fileLock {
	sum := sha1.Sum([]byte(filepath.Clean(key)))
	return &fileLock{
		path:     filepath.Join(root, ".locks", hex.EncodeToString(sum[:])[:16]+".lock"),
		staleTTL: 5 * time.Minute,
		interval: 50 * time.Millisecond,
		timeout:  30 * time.Second,
	}
}

// acquire blocks until the lock is held or ctx/timeout expires, returning a
// release function. While the lock is held, a heartbeat refreshes the lock
// file's mtime well inside staleTTL, so a long legitimate operation (populate
// with a start command, Reset with fetch + submodules) is never mistaken for
// an abandoned lock and reclaimed by another process. If the file disappears
// (released, or reclaimed despite the heartbeat), the heartbeat stops instead
// of resurrecting a lock this process no longer owns.
func (l *fileLock) acquire(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(l.timeout)
	for {
		file, err := os.OpenFile(l.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d %d\n", os.Getpid(), time.Now().UnixNano())
			_ = file.Close()
			stop := make(chan struct{})
			stopped := make(chan struct{})
			go l.heartbeat(stop, stopped)
			var once sync.Once
			return func() {
				once.Do(func() {
					close(stop)
					<-stopped
					_ = os.Remove(l.path)
				})
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(l.path); statErr == nil && time.Since(info.ModTime()) > l.staleTTL {
			_ = os.Remove(l.path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, &Error{Code: CodeResetFailed, Message: "timed out acquiring the worktree lock"}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(l.interval):
		}
	}
}

// heartbeat refreshes the lock file mtime every staleTTL/10 until stop is
// closed or the file vanishes. It closes stopped on return.
func (l *fileLock) heartbeat(stop, stopped chan struct{}) {
	defer close(stopped)
	ticker := time.NewTicker(l.staleTTL / 10)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now()
			if err := os.Chtimes(l.path, now, now); err != nil {
				return
			}
		}
	}
}

// LockDir acquires a cross-process lock for one worktree directory under the
// manager root. The returned release function is always non-nil on success.
func (m *Manager) LockDir(ctx context.Context, directory string) (func(), error) {
	return newFileLock(m.root, directory).acquire(ctx)
}
