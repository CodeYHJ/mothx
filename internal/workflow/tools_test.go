package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	internalagent "github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/tools"
)

func TestRegisterToolsRegistersOnlyWorkflowTools(t *testing.T) {
	registry := tools.NewRegistry(t.TempDir(), nil)
	manager := internalagent.NewAgentManager(&internalagent.AgentFactory{})

	RegisterTools(registry, manager, &memoryStore{})

	for _, name := range []string{"workflow_lint", "workflow_run", "workflow_status", "workflow_cancel"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("expected %s to be registered", name)
		}
	}
	for _, name := range []string{"subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy", "delegate_subagent"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("did not expect %s to be registered by workflow tools", name)
		}
	}
}

func TestLintToolValidatesJavaScriptSourceWithoutRunningAgents(t *testing.T) {
	result, err := NewLintTool().Execute(context.Background(), map[string]any{"source": `workflow("lint me", {phases:[phase("scan", agent("handler-audit", {key:"r0", mode:"plan", tools:["read","grep"], prompt:"Audit handler."})), phase("verify", agent("cross-check", {mode:"plan", prompt:resultKey("scan.handler-audit","r0")}))]});`})
	if err != nil {
		t.Fatal(err)
	}
	var parsed lintResult
	if err := json.Unmarshal([]byte(result.Text), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.Valid || parsed.Status != StatusDone {
		t.Fatalf("%#v", parsed)
	}
}
func TestLintToolReportsWorkflowErrors(t *testing.T) {
	result, err := NewLintTool().Execute(context.Background(), map[string]any{"source": `workflow("bad", {phases:[phase("verify", agent("check", {prompt:result("scan.missing")}))]});`})
	if err != nil {
		t.Fatal(err)
	}
	var parsed lintResult
	if err := json.Unmarshal([]byte(result.Text), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Valid || !strings.Contains(parsed.Error, `workflow result "scan.missing" not found`) {
		t.Fatalf("%#v", parsed)
	}
}
func TestRunToolPromptGuidelinesRequireCompleteJavaScriptSource(t *testing.T) {
	tool := NewRunTool(nil, nil)
	guidelines := strings.Join(tool.PromptGuidelines(), "\n")
	params := string(tool.Parameters())
	for _, want := range []string{"JavaScript", "Markdown code fences", "timeoutSeconds"} {
		if !strings.Contains(guidelines+params, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestRunToolExecutionTimeout(t *testing.T) {
	tool := NewRunTool(nil, nil)

	if _, ok := tool.ExecutionTimeout(map[string]any{}); ok {
		t.Fatal("expected omitted timeoutSeconds to use default tool timeout")
	}

	timeout, ok := tool.ExecutionTimeout(map[string]any{"timeoutSeconds": float64(90)})
	if !ok {
		t.Fatal("expected timeoutSeconds override")
	}
	if timeout != 90*time.Second {
		t.Fatalf("timeout = %s, want 90s", timeout)
	}

	timeout, ok = tool.ExecutionTimeout(map[string]any{"timeoutSeconds": 0})
	if !ok {
		t.Fatal("expected zero timeoutSeconds override")
	}
	if timeout != 0 {
		t.Fatalf("timeout = %s, want no agent-level deadline", timeout)
	}

	if _, ok := tool.ExecutionTimeout(map[string]any{"timeoutSeconds": float64(1.5)}); ok {
		t.Fatal("expected fractional timeoutSeconds to be ignored")
	}
}

func TestCancelToolCancelsActiveRun(t *testing.T) {
	active := NewActiveRegistry()
	canceled := false
	if err := active.Register("run-1", func() { canceled = true }); err != nil {
		t.Fatalf("register active run: %v", err)
	}

	result, err := NewCancelTool(active).Execute(context.Background(), map[string]any{"id": "run-1"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !canceled {
		t.Fatal("expected active run cancel function to be called")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Text), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["status"] != StatusCanceled {
		t.Fatalf("status = %v, want canceled", parsed["status"])
	}
}

func TestCancelToolRejectsInactiveRun(t *testing.T) {
	_, err := NewCancelTool(NewActiveRegistry()).Execute(context.Background(), map[string]any{"id": "missing"})
	if err == nil {
		t.Fatal("expected inactive workflow error")
	}
}
