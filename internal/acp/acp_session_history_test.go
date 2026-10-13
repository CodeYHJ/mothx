package acp

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/oschina/mothx/internal/config"
)

// Reproduces: within one ACP session, a second prompt should still carry the
// first turn's conversation. If history is lost the second provider request
// will NOT contain the first user message.
func TestACPSessionRetainsHistoryAcrossPrompts(t *testing.T) {
	configDir := t.TempDir()
	workDir := t.TempDir()

	var mu sync.Mutex
	var requests []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer providerServer.Close()

	t.Setenv("MOTHX_DIR", configDir)
	settings := config.DefaultSettings()
	settings.DefaultProvider = "process-test"
	settings.DefaultModel = "process-model"
	settings.DefaultMode = "yolo"
	settings.SessionDir = filepath.Join(configDir, "sessions")
	settings.Providers = map[string]*config.ProviderConfig{
		"process-test": {
			APIKey:  "test-key",
			BaseURL: providerServer.URL + "/v1",
			API:     "openai-chat",
			Models:  []config.ModelConfig{{ID: "process-model", Name: "Process Model", ContextWindow: 32768, MaxTokens: 1024}},
		},
	}
	if err := config.SaveGlobalSettings(settings); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestACPStdioProcessHelper$")
	cmd.Env = append(os.Environ(), "MOTHX_ACP_PROCESS_HELPER=1", "MOTHX_DIR="+configDir)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)

	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	assertACPResponseID(t, reader, 1)
	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": workDir}})
	newSession := assertACPResponseID(t, reader, 2)
	result := newSession["result"].(map[string]any)
	sessionID := result["sessionId"].(string)

	// Turn 1
	sendACPRequest(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": []map[string]any{{"type": "text", "text": "My favorite fruit is mango"}}},
	})
	assertACPResponseIDCollecting(t, reader, 3)

	// Turn 2: ask the model to recall turn 1
	sendACPRequest(t, stdin, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "session/prompt",
		"params": map[string]any{"sessionId": sessionID, "prompt": []map[string]any{{"type": "text", "text": "What fruit did I just mention?"}}},
	})
	assertACPResponseIDCollecting(t, reader, 4)

	sendACPRequest(t, stdin, map[string]any{"jsonrpc": "2.0", "id": 5, "method": "session/close", "params": map[string]any{"sessionId": sessionID}})
	assertACPResponseID(t, reader, 5)
	_ = stdin.Close()
	waitForACPProcess(t, cmd)

	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(requests))
	}
	if !strings.Contains(requests[len(requests)-1], "My favorite fruit is mango") {
		t.Fatalf("SECOND prompt did not carry first turn's history — conversation context lost within one ACP session")
	}
}
