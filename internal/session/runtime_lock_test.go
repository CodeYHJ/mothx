package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRuntimeLeaseSubprocessHelper is executed by the parent test in a
// separate OS process. The helper intentionally keeps the lease until it is
// killed, modelling a process crash without running any cleanup code.
func TestRuntimeLeaseSubprocessHelper(t *testing.T) {
	if os.Getenv("MOTHX_RUNTIME_LEASE_HELPER") != "1" {
		return
	}
	release, ok := TryLockRuntime(os.Getenv("MOTHX_RUNTIME_LEASE_DIR"), os.Getenv("MOTHX_RUNTIME_LEASE_SESSION"))
	if !ok {
		t.Fatal("helper could not acquire runtime lease")
	}
	defer release()
	_, _ = fmt.Fprintln(os.Stdout, "acquired")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func TestRuntimeLeaseSurvivesProcessFailureUntilExpiry(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := mgr.InitWithID("lease-process"); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeLeaseSubprocessHelper", "-test.v")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"MOTHX_RUNTIME_LEASE_HELPER=1",
		"MOTHX_RUNTIME_LEASE_DIR="+sessionDir,
		"MOTHX_RUNTIME_LEASE_SESSION=lease-process",
	)
	// The helper reads stdin until EOF, so keep a pipe open while it owns the
	// lease and close it only after the parent has observed acquisition.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	acquired := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "acquired" {
				acquired <- true
				return
			}
		}
		acquired <- false
	}()
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("lease helper exited before acquiring")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for helper lease")
	}

	if release, ok := TryLockRuntime(sessionDir, "lease-process"); ok {
		release()
		t.Fatal("second process acquired an unexpired runtime lease")
	}
	mgrB := New(filepath.Join(t.TempDir(), "work-b"), sessionDir)
	if err := mgrB.InitWithID("lease-process-b"); err != nil {
		t.Fatal(err)
	}
	releaseB, ok := TryLockRuntime(sessionDir, "lease-process-b")
	if !ok {
		t.Fatal("session B was blocked by session A's lease")
	}
	releaseB()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	db, err := OpenRootDB(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE session_runtime_leases SET expires_at = CAST(strftime('%s','now') AS INTEGER) - 1 WHERE session_id = ?`, "lease-process"); err != nil {
		t.Fatal(err)
	}
	release, ok := TryLockRuntime(sessionDir, "lease-process")
	if !ok {
		t.Fatal("expired runtime lease was not reclaimable")
	}
	var epoch int64
	if err := db.QueryRow(`SELECT epoch FROM session_runtime_leases WHERE session_id = ?`, "lease-process").Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch < 2 {
		t.Fatalf("reclaimed runtime lease epoch = %d, want fencing epoch >= 2", epoch)
	}
	release()
	var state string
	if err := db.QueryRow(`SELECT epoch, state FROM session_runtime_leases WHERE session_id = ?`, "lease-process").Scan(&epoch, &state); err != nil {
		t.Fatalf("released runtime lease tombstone missing: %v", err)
	}
	if state != "released" {
		t.Fatalf("released runtime lease state = %q, want released", state)
	}
}

func TestReleasedLeaseFencesDelayedOwnerWrite(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := New(filepath.Join(t.TempDir(), "work"), sessionDir)
	if err := mgr.InitWithID("lease-tombstone"); err != nil {
		t.Fatal(err)
	}
	oldLease, err := acquireRuntimeLease(sessionDir, "lease-tombstone", "run")
	if err != nil || oldLease == nil {
		t.Fatalf("acquire old lease = %v, lease=%v", err, oldLease)
	}
	oldLease.release()
	newLease, err := acquireRuntimeLease(sessionDir, "lease-tombstone", "run")
	if err != nil || newLease == nil {
		t.Fatalf("acquire new lease = %v, lease=%v", err, newLease)
	}
	newLease.release()

	db, err := OpenRootDB(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = validateRuntimeLeaseTx(tx, sessionDir, "lease-tombstone")
	_ = tx.Rollback()
	if !errors.Is(err, ErrRuntimeLeaseLost) {
		t.Fatalf("delayed owner validation error = %v, want ErrRuntimeLeaseLost", err)
	}
	if _, err := SaveSessionRunEvent(sessionDir, SessionRunEvent{SessionID: "lease-tombstone", RunID: "stale-run", EventType: "late"}); !errors.Is(err, ErrRuntimeLeaseLost) {
		t.Fatalf("delayed run event error = %v, want ErrRuntimeLeaseLost", err)
	}
}
