package session

import "testing"

func TestRotateBoundSessionRebindsChannel(t *testing.T) {
	sessionDir := t.TempDir()
	const (
		workDir     = "/tmp/rotate-bound-session"
		channelType = "wechat"
		channelID   = "user-123"
	)

	old, err := CreateBound(workDir, sessionDir, channelType, channelID)
	if err != nil {
		t.Fatalf("create bound session: %v", err)
	}
	oldID := old.GetHeader().ID

	rotated, err := RotateBoundSession(workDir, sessionDir, channelType, channelID, oldID)
	if err != nil {
		t.Fatalf("rotate bound session: %v", err)
	}
	newID := rotated.GetHeader().ID
	if newID == oldID {
		t.Fatalf("rotated session reused old ID %q", oldID)
	}

	binding, err := FindBinding(sessionDir, channelType, channelID)
	if err != nil {
		t.Fatalf("find rotated binding: %v", err)
	}
	if binding == nil || binding.SessionID != newID {
		t.Fatalf("binding = %#v, want session %q", binding, newID)
	}

	oldBinding, err := FindBindingBySessionID(sessionDir, oldID)
	if err != nil {
		t.Fatalf("find old session binding: %v", err)
	}
	if oldBinding != nil {
		t.Fatalf("old binding = %#v, want no external binding", oldBinding)
	}
}
