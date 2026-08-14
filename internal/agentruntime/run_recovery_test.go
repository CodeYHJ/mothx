package agentruntime

import (
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestRecoverOrphanedRunsFailsLocalAndKeepsRemote(t *testing.T) {
	sessionDir := t.TempDir()
	store := RunStore{SessionDir: sessionDir}
	now := time.Now()
	for _, run := range []DurableRun{
		{ID: "local", SessionID: "session-1", Source: "acp", Status: "running", StartedAt: now},
		{ID: "remote", SessionID: "session-2", Source: "responses_background", Status: "running", StartedAt: now},
	} {
		if err := store.Create(run); err != nil {
			t.Fatal(err)
		}
	}
	var cleaned []string
	result, err := RecoverOrphanedRuns(sessionDir, nil, func(run session.SessionRun) error {
		cleaned = append(cleaned, run.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) != 1 || result.Failed[0].ID != "local" || len(result.Kept) != 1 || result.Kept[0].ID != "remote" {
		t.Fatalf("result = %#v", result)
	}
	if len(cleaned) != 1 || cleaned[0] != "local" {
		t.Fatalf("cleaned = %#v", cleaned)
	}
	local, _ := session.GetSessionRun(sessionDir, "local")
	remote, _ := session.GetSessionRun(sessionDir, "remote")
	if local.Status != "failed" || remote.Status != "running" {
		t.Fatalf("local=%#v remote=%#v", local, remote)
	}
}
