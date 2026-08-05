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

func TestChannelToolsPersistForBoundSession(t *testing.T) {
	sessionDir := t.TempDir()
	mgr, err := CreateBound("/tmp/channel-tools", sessionDir, "feishu", "chat-123")
	if err != nil {
		t.Fatalf("create bound session: %v", err)
	}
	sessionID := mgr.GetHeader().ID
	want := []ChannelToolConfig{
		{ToolName: "bash", Enabled: false},
		{ToolName: "read", Enabled: true},
	}
	if err := SetChannelTools(sessionDir, sessionID, want); err != nil {
		t.Fatalf("set channel tools: %v", err)
	}
	got, err := ListChannelTools(sessionDir, sessionID)
	if err != nil {
		t.Fatalf("list channel tools: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tools, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
