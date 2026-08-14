package agentruntime

import (
	"testing"

	"github.com/startvibecoding/mothx/internal/session"
)

func TestSessionRuntimeBindSessionUpdatesLazyIdentity(t *testing.T) {
	workDir := t.TempDir()
	manager := session.New(workDir, t.TempDir())
	if err := manager.Init(); err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{Source: SourceTUI, WorkDir: workDir}
	if err := runtime.BindSession(manager, SourceTUI); err != nil {
		t.Fatal(err)
	}
	if runtime.Manager != manager || runtime.ID != manager.GetHeader().ID || runtime.WorkDir != manager.GetHeader().Cwd || runtime.Source != SourceTUI {
		t.Fatalf("runtime binding = %#v", runtime)
	}
}

func TestSessionRuntimeBindSessionRejectsClosedRuntime(t *testing.T) {
	manager := session.New(t.TempDir(), t.TempDir())
	if err := manager.Init(); err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{Source: SourceTUI}
	runtime.Close()
	if err := runtime.BindSession(manager, SourceTUI); err == nil {
		t.Fatal("BindSession on closed runtime unexpectedly succeeded")
	}
}
