package tui

import (
	"testing"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/tools"
)

func TestTUIRuntimeUsesAuthoritativeSourceAndMode(t *testing.T) {
	workDir, sessionDir := t.TempDir(), t.TempDir()
	mgr := session.New(workDir, sessionDir)
	if err := mgr.Init(); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(workDir, nil)
	app := NewApp(nil, &provider.Model{ID: "test"}, config.DefaultSettings(), mgr, registry, "", "", "", nil, "agent", false, false, nil, nil, nil)
	app.SetRuntime(tuiRuntime(mgr, registry, "", "", "", nil))
	mode, err := app.effectiveRuntimeMode()
	if err != nil {
		t.Fatal(err)
	}
	if mode != agentruntime.ModeAgent {
		t.Fatalf("mode = %q, want agent", mode)
	}
}
