package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

// StreamIdleTimeout is the maximum time a streaming response body may go
// without delivering any data before it is considered stalled and aborted.
// Unlike a fixed wall-clock timeout, this only fires when the upstream stops
// sending data, so long-lived streams that keep delivering data are unaffected.
const StreamIdleTimeout = 30 * time.Minute

// idleTimeoutReadCloser wraps an io.ReadCloser so that a read that does not
// deliver data within the idle window is aborted with context.DeadlineExceeded.
// Every successful read resets the idle window, so an SSE stream that keeps
// sending data (tokens, keep-alives) is never cut off by an overall timeout.
type idleTimeoutReadCloser struct {
	rc    io.ReadCloser
	idle  time.Duration
	timer *time.Timer

	mu       sync.Mutex
	timedOut bool
}

// NewIdleTimeoutReadCloser wraps rc with an inactivity deadline of idle. If rc
// is nil or idle <= 0, rc is returned unchanged.
func NewIdleTimeoutReadCloser(rc io.ReadCloser, idle time.Duration) io.ReadCloser {
	if rc == nil || idle <= 0 {
		return rc
	}
	b := &idleTimeoutReadCloser{rc: rc, idle: idle}
	b.timer = time.AfterFunc(idle, b.onIdle)
	return b
}

func (b *idleTimeoutReadCloser) onIdle() {
	b.mu.Lock()
	b.timedOut = true
	b.mu.Unlock()
	// Close the underlying body to unblock any pending Read. The connection is
	// aborted and will not be reused; a fresh request is made on retry.
	_ = b.rc.Close()
}

func (b *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	if !b.timer.Stop() {
		select {
		case <-b.timer.C:
		default:
		}
	}
	timedOut := b.timedOut
	if n > 0 {
		// Data keeps flowing; reset the idle window.
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	if timedOut && n == 0 {
		return 0, context.DeadlineExceeded
	}
	return n, err
}

func (b *idleTimeoutReadCloser) Close() error {
	b.mu.Lock()
	b.timer.Stop()
	b.mu.Unlock()
	return b.rc.Close()
}

// IsStreamTimeoutError reports whether err indicates an upstream/stream timeout
// (idle stream stall, response-header timeout, or a wrapped deadline). It is
// distinct from user-initiated cancellation (context.Canceled).
func IsStreamTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "timed out") ||
		strings.Contains(s, "timeout")
}
