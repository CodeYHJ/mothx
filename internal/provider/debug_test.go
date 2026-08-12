package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugJSONWritesRequestAndCompleteResponse(t *testing.T) {
	t.Setenv("VIBECODING_DEBUG", "1")
	t.Setenv(DebugLogOnlyEnv, "1")
	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	DebugJSON("OpenAI request JSON", []byte(`{"model":"test","stream":true}`))
	DebugCompleteResponse(DebugResponse{
		Provider: "openai",
		API:      "chat-completions",
		Content:  "complete response",
	})

	data, err := os.ReadFile(filepath.Join(workDir, "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, `OpenAI request JSON: {"model":"test","stream":true}`) {
		t.Fatalf("debug log missing request JSON: %s", log)
	}
	if !strings.Contains(log, `Response JSON: {"provider":"openai","api":"chat-completions","content":"complete response"}`) {
		t.Fatalf("debug log missing complete response JSON: %s", log)
	}
	if strings.Contains(log, "data:") {
		t.Fatalf("debug log contains an SSE fragment: %s", log)
	}
}

func TestDebugLogfWritesOnlyWhenDebugEnabled(t *testing.T) {
	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv(DebugLogOnlyEnv, "1")

	DebugLogf("not written")
	if _, err := os.Stat(filepath.Join(workDir, "debug.log")); !os.IsNotExist(err) {
		t.Fatalf("debug log exists without debug mode: %v", err)
	}

	t.Setenv("VIBECODING_DEBUG", "1")
	DebugLogf("session %q sync failed: %v", "s1", os.ErrNotExist)
	data, err := os.ReadFile(filepath.Join(workDir, "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "diagnostic: session \"s1\" sync failed") {
		t.Fatalf("debug log missing diagnostic: %s", data)
	}
}

func TestDebugCompleteResponseLogsResponseWhenJSONMarshalFails(t *testing.T) {
	t.Setenv("VIBECODING_DEBUG", "1")
	t.Setenv(DebugLogOnlyEnv, "1")
	workDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	DebugCompleteResponse(DebugResponse{
		Provider: "openai",
		API:      "chat-completions",
		ToolCalls: []ToolCallBlock{{
			ID:        "call_1",
			Name:      "read_file",
			Arguments: json.RawMessage(`not valid json`),
		}},
	})

	data, err := os.ReadFile(filepath.Join(workDir, "debug.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, "Response JSON marshal error") {
		t.Fatalf("debug log missing marshal error: %s", log)
	}
	if !strings.Contains(log, "call_1") || !strings.Contains(log, "not valid json") {
		t.Fatalf("debug log missing unmarshalable response content: %s", log)
	}
}
