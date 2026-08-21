package acp

import (
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestAcquirePromptAdmissionRejectsDurableActiveRun(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := session.New(t.TempDir(), sessionDir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	now := time.Now()
	if err := session.CreateSessionRun(sessionDir, session.SessionRun{
		ID: "existing-run", SessionID: mgr.GetHeader().ID, Status: "running", StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create active run: %v", err)
	}

	s := &server{settings: &config.Settings{SessionDir: sessionDir}}
	_, err := s.acquirePromptAdmission(&sessionRuntime{id: mgr.GetHeader().ID})
	if err != errACPActiveSessionRun {
		t.Fatalf("admission error = %v, want %v", err, errACPActiveSessionRun)
	}
}

func TestAcquirePromptAdmissionHoldsSharedRuntimeLock(t *testing.T) {
	sessionDir := t.TempDir()
	mgr := session.New(t.TempDir(), sessionDir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	s := &server{settings: &config.Settings{SessionDir: sessionDir}}
	rt := &sessionRuntime{id: mgr.GetHeader().ID}
	release, err := s.acquirePromptAdmission(rt)
	if err != nil {
		t.Fatalf("acquire admission: %v", err)
	}
	defer release()
	if _, err := s.acquirePromptAdmission(rt); err != errACPActiveSessionRun {
		t.Fatalf("second admission error = %v, want %v", err, errACPActiveSessionRun)
	}
	if rt.cancel != nil {
		t.Fatal("admission unexpectedly installed a local cancel function")
	}
}
