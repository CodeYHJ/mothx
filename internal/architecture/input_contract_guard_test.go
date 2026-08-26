package architecture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFrontendInputContractGuard keeps every interactive entrypoint on the
// Runtime-owned input/content boundary. It is intentionally a source-level
// guard: the concrete wire shapes differ, but each adapter must normalize
// them before the Agent is built and must execute the resulting message with
// the same Agent Core method.
func TestFrontendInputContractGuard(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate input contract guard")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	checks := []struct {
		path     string
		requires []string
	}{
		{
			path: "internal/tui/input.go",
			requires: []string{
				".AcceptInput(", ".BuildUserMessage(", "BeginArtifactCollection(",
			},
		},
		{
			path: "internal/tui/run.go",
			requires: []string{
				"RunWithUserMessage(ctx, userMessage)",
			},
		},
		{
			path: "cmd/mothx/main_util.go",
			requires: []string{
				".AcceptInput(", ".BuildUserMessage(", "RunWithUserMessage(runCtx, userMessage)",
			},
		},
		{
			path: "internal/acp/acp.go",
			requires: []string{
				"promptToRunInput(", ".BuildUserMessage(", "RunWithUserMessage(ctx, promptMessage)",
			},
		},
		{
			path: "internal/serve/openaiapi/handler_run_submit.go",
			requires: []string{
				".AcceptInput(", ".BuildUserMessage(", "BeginArtifactCollection(",
			},
		},
		{
			path: "internal/serve/openaiapi/handler_chat.go",
			requires: []string{
				"requestRunInput(", ".AcceptInput(", ".BuildUserMessage(", "BeginArtifactCollection(",
			},
		},
		{
			path: "internal/serve/openaiapi/background_external.go",
			requires: []string{
				"req.Input", ".BuildUserMessage(", "BeginArtifactCollection(",
			},
		},
		{
			path: "internal/serve/channels/dispatcher.go",
			requires: []string{
				"channelAttachmentIngresses", ".AcceptInput(", ".BuildUserMessage(", "BeginArtifactCollection(",
			},
		},
	}
	for _, check := range checks {
		t.Run(check.path, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(check.path)))
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range check.requires {
				if !strings.Contains(string(src), required) {
					t.Fatalf("%s is missing required Runtime input contract call %q", check.path, required)
				}
			}
		})
	}
}
