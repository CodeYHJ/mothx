package provider

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type chunkReader struct {
	chunks  []string
	delay   time.Duration
	idx     int
	blocked chan struct{} // closed when a Read starts blocking
	closed  chan struct{} // closed by Close to unblock a pending Read
}

func newChunkReader(chunks []string) *chunkReader {
	return &chunkReader{chunks: chunks, closed: make(chan struct{})}
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.idx >= len(c.chunks) {
		if c.blocked != nil {
			c.blocked <- struct{}{}
		}
		// Block like a stalled network read until Close unblocks it.
		<-c.closed
		return 0, io.ErrClosedPipe
	}
	s := c.chunks[c.idx]
	c.idx++
	n := copy(p, s)
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return n, nil
}

func (c *chunkReader) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

// TestIdleTimeoutAllowsContinuousData verifies that a stream that keeps
// delivering data within the idle window is never cut off.
func TestIdleTimeoutAllowsContinuousData(t *testing.T) {
	rc := NewIdleTimeoutReadCloser(newChunkReader([]string{"a", "b", "c"}), 50*time.Millisecond)
	defer rc.Close()

	var got strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := rc.Read(buf)
		if err != nil {
			break
		}
		got.Write(buf[:n])
		time.Sleep(10 * time.Millisecond) // keep reads within the idle window
	}
	if got.String() != "abc" {
		t.Fatalf("got %q, want %q", got.String(), "abc")
	}
}

// TestIdleTimeoutFiresOnStall verifies that a stalled stream (no data within the
// idle window) is aborted with context.DeadlineExceeded.
func TestIdleTimeoutFiresOnStall(t *testing.T) {
	rc := NewIdleTimeoutReadCloser(&chunkReader{chunks: []string{"a"}, blocked: make(chan struct{}, 1), closed: make(chan struct{})}, 50*time.Millisecond)
	defer rc.Close()

	buf := make([]byte, 1)
	if _, err := rc.Read(buf); err != nil {
		t.Fatalf("first read should succeed, got %v", err)
	}
	start := time.Now()
	_, err := rc.Read(buf)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("idle timeout took too long: %s", elapsed)
	}
}

func TestIsStreamTimeoutError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{context.DeadlineExceeded, true},
		{errors.New("stream read error: context deadline exceeded"), true},
		{errors.New("net/http: timeout awaiting response headers"), true},
		{errors.New("request timed out"), true},
		{context.Canceled, false},
		{errors.New("connection refused"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := IsStreamTimeoutError(c.err); got != c.want {
			t.Errorf("IsStreamTimeoutError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

var _ io.ReadCloser = &chunkReader{}
