package acp

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestACPStdioProcessInitializeLoadResumeClose(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("VIBECODING_DIR", configDir)
	settings := config.DefaultSettings()
	settings.DefaultProvider = "process-test"
	settings.DefaultModel = "process-model"
	settings.SessionDir = filepath.Join(configDir, "sessions")
	settings.Providers = map[string]*config.ProviderConfig{
		"process-test": {APIKey: "test-key", BaseURL: "http://127.0.0.1:1/v1", API: "openai-chat", Models: []config.ModelConfig{{ID: "process-model", Name: "Process Model", ContextWindow: 32768, MaxTokens: 1024}}},
	}
	if err := config.SaveGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}
	mgr := session.New(workDir, settings.GetSessionDir())
	if err := mgr.Init(); err != nil {
		t.Fatal(err)
	}
	decisionRequest := agentruntime.DecisionRequest{ID: "process-question", SessionID: mgr.GetHeader().ID, RunID: "process-run", Kind: agentruntime.DecisionQuestion}
	decisionRecord, err := agentruntime.NewDecisionRequestRecordWithDeadline(decisionRequest, questionRequest{SessionID: mgr.GetHeader().ID, Question: "continue?"}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	decisionData, err := json.Marshal(map[string]any{"decision": decisionRecord})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.SaveSessionRunEvent(settings.GetSessionDir(), session.SessionRunEvent{SessionID: mgr.GetHeader().ID, RunID: "process-run", EventType: "decision_pending", Source: "acp", Status: "pending", Data: decisionData}); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestACPStdioProcessHelper$")
	cmd.Env = append(os.Environ(), "MOTHX_ACP_PROCESS_HELPER=1", "VIBECODING_DIR="+configDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	assertACPResponseID(t, reader, 1)
	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/load", "params": map[string]any{"sessionId": mgr.GetHeader().ID, "cwd": workDir}})
	assertACPResponseID(t, reader, 2)
	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/resume", "params": map[string]any{"sessionId": mgr.GetHeader().ID, "cwd": workDir}})
	assertACPResponseID(t, reader, 3)
	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/close", "params": map[string]any{"sessionId": mgr.GetHeader().ID}})
	assertACPResponseID(t, reader, 4)
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("ACP process did not exit after stdin EOF")
	}
	events, err := session.ListSessionRunEvents(settings.GetSessionDir(), mgr.GetHeader().ID)
	if err != nil {
		t.Fatal(err)
	}
	latestDecisionStatus := ""
	for _, event := range events {
		var envelope struct {
			Decision agentruntime.DecisionRecord `json:"decision"`
		}
		if json.Unmarshal(event.Data, &envelope) == nil && envelope.Decision.ID == decisionRequest.ID {
			latestDecisionStatus = envelope.Decision.Status
		}
	}
	if latestDecisionStatus != "cancelled" {
		t.Fatalf("restored offline decision status = %q, want cancelled", latestDecisionStatus)
	}
}

func TestACPStdioProcessHelper(t *testing.T) {
	if os.Getenv("MOTHX_ACP_PROCESS_HELPER") != "1" {
		return
	}
	if err := Run(RunOptions{}); err != nil {
		t.Fatal(err)
	}
}

func sendACPRequest(t *testing.T, stdin interface{ Write([]byte) (int, error) }, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := stdin.Write(data); err != nil {
		t.Fatal(err)
	}
}

func assertACPResponseID(t *testing.T, reader *bufio.Reader, id float64) map[string]any {
	t.Helper()
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
		if got, ok := message["id"].(float64); ok && got == id {
			if rpcErr := message["error"]; rpcErr != nil {
				t.Fatalf("ACP response %v error: %#v", id, rpcErr)
			}
			return message
		}
	}
}
