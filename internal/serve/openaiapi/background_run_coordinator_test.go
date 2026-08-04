package openaiapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	openaiprovider "github.com/startvibecoding/mothx/internal/provider/openai"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/tools"
)

func TestResponsesBackgroundText(t *testing.T) {
	text, needsContinuation, err := responsesBackgroundText([]session.ResponseItemArchive{
		{ItemID: "msg-1", SanitizedJSON: json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"hello "},{"type":"output_text","text":"world"}]}`)},
	})
	if err != nil || needsContinuation || text != "hello world" {
		t.Fatalf("text=%q needsContinuation=%v err=%v", text, needsContinuation, err)
	}

	_, needsContinuation, err = responsesBackgroundText([]session.ResponseItemArchive{
		{ItemID: "call-1", SanitizedJSON: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"write","arguments":"{}"}`)},
	})
	if err != nil || !needsContinuation {
		t.Fatalf("needsContinuation=%v err=%v, want local continuation", needsContinuation, err)
	}
	_, needsContinuation, err = responsesBackgroundText([]session.ResponseItemArchive{
		{ItemID: "computer-1", SanitizedJSON: json.RawMessage(`{"type":"computer_call","action":{"type":"screenshot"}}`)},
	})
	if err == nil || needsContinuation || !strings.Contains(err.Error(), "computer use is not supported") {
		t.Fatalf("computer item result needsContinuation=%v err=%v", needsContinuation, err)
	}
}

func TestResponsesBackgroundRecoverySkipsCancellingRuns(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled", "canceled", "cancelling", "terminalizing"} {
		if !isTerminalSessionRunState(state) {
			t.Fatalf("state %q must not be reattached", state)
		}
	}
	if isTerminalSessionRunState("running") {
		t.Fatal("running state must remain recoverable")
	}
}

func TestResponsesBackgroundFunctionCallsLoadCustomInput(t *testing.T) {
	sessionDir := t.TempDir()
	if err := session.SaveResponseItem(sessionDir, session.ResponseItemArchive{
		SessionID: "session-custom", LocalTurnID: "turn-custom", ResponseID: "resp-custom",
		ItemID: "custom-1", OutputIndex: 0, ItemType: "custom_tool_call",
		SanitizedJSON: json.RawMessage(`{"id":"custom-1","type":"custom_tool_call","call_id":"call-custom","name":"shell_script","input":"echo hello"}`),
	}); err != nil {
		t.Fatalf("save custom response item: %v", err)
	}
	calls, err := responsesBackgroundFunctionCallsForRun(sessionDir, "session-custom", "turn-custom")
	if err != nil {
		t.Fatalf("load custom tool call: %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "call-custom" || calls[0].Kind != "custom" || calls[0].Input != "echo hello" || string(calls[0].Arguments) != `{"input":"echo hello"}` {
		t.Fatalf("custom calls = %#v", calls)
	}
}

func TestResponsesBackgroundDetailsLoadsArchivedUsageAndAttachments(t *testing.T) {
	sessionDir := t.TempDir()
	if err := session.SaveResponseTurn(sessionDir, session.ResponseTurn{
		SessionID: "session-details", LocalTurnID: "turn-details", Provider: "openai", API: "openai-responses",
		Model: "test", StateMode: "replay", Status: "completed",
		ResponseSummary: json.RawMessage(`{"usage":{"input":11,"output":7,"totalTokens":18},"attachments":[{"kind":"citation","name":"OpenAI","url":"https://openai.com"}]}`),
	}); err != nil {
		t.Fatalf("save response turn: %v", err)
	}
	usage, attachments, err := responsesBackgroundDetails(sessionDir, "session-details", "turn-details")
	if err != nil {
		t.Fatalf("load background details: %v", err)
	}
	if usage == nil || usage.TotalTokens != 18 || len(attachments) != 1 || attachments[0].URL != "https://openai.com" {
		t.Fatalf("usage=%#v attachments=%#v", usage, attachments)
	}
}

func TestResponsesBackgroundToolsRunParallelAndPreserveOutputOrder(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	sess, err := srv.getOrCreateSession("parallel-background", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tool := &parallelBackgroundTool{started: make(chan struct{}), release: make(chan struct{})}
	sess.Registry.Register(tool)
	backgroundAgent := agent.New(agent.Config{
		Provider: srv.provider,
		Model:    srv.model,
		Mode:     "yolo",
		Session:  sess.Manager,
	}, sess.Registry)
	calls := []provider.ToolCallBlock{
		{ID: "call-1", Name: "parallel_test", Arguments: json.RawMessage(`{"index":1}`)},
		{ID: "call-2", Name: "parallel_test", Arguments: json.RawMessage(`{"index":2}`)},
	}
	done := make(chan struct{})
	var outputs []provider.Message
	var ok bool
	go func() {
		outputs, ok = srv.executeResponsesBackgroundTools(context.Background(), sess, backgroundAgent, "run-parallel", "run-parallel", calls)
		close(done)
	}()
	select {
	case <-tool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background function calls did not start in parallel")
	}
	close(tool.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("parallel background tools did not finish")
	}
	if !ok || len(outputs) != 2 {
		t.Fatalf("ok=%v outputs=%#v", ok, outputs)
	}
	if got := messageText(outputs[0]); got != "1" {
		t.Fatalf("first output = %q, want call-1 result", got)
	}
	if got := messageText(outputs[1]); got != "2" {
		t.Fatalf("second output = %q, want call-2 result", got)
	}
}

func TestResponsesBackgroundToolsScopeIdempotencyToResponseTurn(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	sess, err := srv.getOrCreateSession("turn-scoped-background", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tool := &parallelBackgroundTool{started: make(chan struct{}), release: make(chan struct{})}
	close(tool.release)
	sess.Registry.Register(tool)
	backgroundAgent := agent.New(agent.Config{
		Provider: srv.provider,
		Model:    srv.model,
		Mode:     "yolo",
		Session:  sess.Manager,
	}, sess.Registry)
	call := provider.ToolCallBlock{ID: "reused-call-id", Name: "parallel_test", Arguments: json.RawMessage(`{"index":1}`)}

	if outputs, ok := srv.executeResponsesBackgroundTools(context.Background(), sess, backgroundAgent, "webui-run", "response-turn-one", []provider.ToolCallBlock{call}); !ok || len(outputs) != 1 {
		t.Fatalf("first response-turn outputs=%#v ok=%v", outputs, ok)
	}
	if outputs, ok := srv.executeResponsesBackgroundTools(context.Background(), sess, backgroundAgent, "webui-run", "response-turn-two", []provider.ToolCallBlock{call}); !ok || len(outputs) != 1 {
		t.Fatalf("second response-turn outputs=%#v ok=%v", outputs, ok)
	}

	tool.mu.Lock()
	count := tool.count
	tool.mu.Unlock()
	if count != 2 {
		t.Fatalf("tool executions=%d, want 2 for separate Responses turns", count)
	}
}

func TestResponsesBackgroundToolsStopsOnInterruptedExecutionRecord(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	sess, err := srv.getOrCreateSession("interrupted-background", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tool := &parallelBackgroundTool{started: make(chan struct{}), release: make(chan struct{})}
	sess.Registry.Register(tool)
	backgroundAgent := agent.New(agent.Config{Provider: srv.provider, Model: srv.model, Mode: "yolo", Session: sess.Manager}, sess.Registry)
	call := provider.ToolCallBlock{ID: "call-interrupted", Name: "parallel_test", Arguments: json.RawMessage(`{"index":1}`)}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatalf("decode call args: %v", err)
	}
	normalized, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("normalize call args: %v", err)
	}
	argsHash := sha256.Sum256(normalized)
	keyHash := sha256.Sum256([]byte(sess.ID + "\x00" + "interrupted-run" + "\x00" + call.ID + "\x00" + call.Name + "\x00" + fmt.Sprintf("%x", argsHash[:])))
	if _, created, err := session.ClaimToolExecutionRecord(srv.settings.GetSessionDir(), session.ToolExecutionRecord{
		SessionID: sess.ID, LocalTurnID: "interrupted-run", ExecutionKey: fmt.Sprintf("tool:%x", keyHash[:]),
		Provider: srv.provider.Name(), API: srv.provider.API(), ProviderCallID: call.ID, ToolKind: "function",
		ToolName: call.Name, ArgsHash: fmt.Sprintf("%x", argsHash[:]), ExecutionState: "running",
	}); err != nil || !created {
		t.Fatalf("save interrupted execution record: created=%v err=%v", created, err)
	}
	outputs, ok := srv.executeResponsesBackgroundTools(context.Background(), sess, backgroundAgent, "interrupted-run", "interrupted-run", []provider.ToolCallBlock{call})
	if ok || outputs != nil {
		t.Fatalf("interrupted background execution = outputs=%#v ok=%v, want stop", outputs, ok)
	}
}

func TestRecoverResponsesBackgroundFunctionContinuation(t *testing.T) {
	var mu sync.Mutex
	continuationPosts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-recovery":
			_, _ = w.Write([]byte(`{"id":"resp-recovery","status":"completed","output":[{"id":"fc-recovery","type":"function_call","call_id":"call-recovery","name":"read","arguments":"{\"path\":\"missing.txt\"}"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			continuationPosts++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode continuation request: %v", err)
			}
			if body["previous_response_id"] != "resp-recovery" {
				t.Fatalf("previous_response_id = %#v", body["previous_response_id"])
			}
			_, _ = w.Write([]byte(`{"id":"resp-recovery-final","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-recovery-final":
			_, _ = w.Write([]byte(`{"id":"resp-recovery-final","status":"completed","output":[{"id":"msg-recovery-final","type":"message","content":[{"type":"output_text","text":"recovered continuation"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())
	runID := "recover-background-run"
	sess, err := srv.getOrCreateSession("recover-background-session", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := session.SaveSessionRun(srv.settings.GetSessionDir(), session.SessionRun{ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir, Source: "responses_background", Model: model.ID, Mode: "yolo", Status: "running", StartedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save session run: %v", err)
	}
	if err := session.SaveResponseRun(srv.settings.GetSessionDir(), session.ResponseRun{SessionID: sess.ID, LocalRunID: "remote-recovery", LocalTurnID: runID, ResponseID: "resp-recovery", Provider: p.Name(), API: "openai-responses", State: "queued", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatalf("save response run: %v", err)
	}
	sess.Lock()
	sess.beginRun(runID)
	srv.monitorRecoveredResponsesBackgroundRun(sess, session.SessionRun{ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir, Source: "responses_background", Model: model.ID, Mode: "yolo"}, &session.ResponseRun{SessionID: sess.ID, LocalRunID: "remote-recovery", LocalTurnID: runID, ResponseID: "resp-recovery", Provider: p.Name(), API: "openai-responses", State: "queued", CreatedAt: time.Now(), UpdatedAt: time.Now()}, model, func() {})
	messages := sess.Manager.GetMessages()
	if len(messages) == 0 || messageText(messages[len(messages)-1]) != "recovered continuation" {
		t.Fatalf("recovered messages = %#v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if continuationPosts != 1 {
		t.Fatalf("continuation POST count = %d, want 1", continuationPosts)
	}
}

type parallelBackgroundTool struct {
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
}

func (t *parallelBackgroundTool) Name() string               { return "parallel_test" }
func (t *parallelBackgroundTool) Description() string        { return "test parallel background execution" }
func (t *parallelBackgroundTool) PromptSnippet() string      { return "test parallel background execution" }
func (t *parallelBackgroundTool) PromptGuidelines() []string { return nil }
func (t *parallelBackgroundTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"}}}`)
}
func (t *parallelBackgroundTool) Execute(ctx context.Context, params map[string]any) (tools.ToolResult, error) {
	t.mu.Lock()
	t.count++
	if t.count == 2 {
		close(t.started)
	}
	t.mu.Unlock()
	select {
	case <-t.release:
	case <-ctx.Done():
		return tools.ToolResult{}, ctx.Err()
	}
	return tools.ToolResult{Text: fmt.Sprintf("%v", params["index"])}, nil
}

func TestSubmitRunUsesResponsesBackgroundCoordinator(t *testing.T) {
	var mu sync.Mutex
	var postCount, getCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode background request: %v", err)
			}
			if request["background"] != true || request["stream"] != false {
				t.Fatalf("background request = %#v", request)
			}
			_, _ = w.Write([]byte(`{"id":"resp-background","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-background":
			getCount++
			_, _ = w.Write([]byte(`{"id":"resp-background","status":"completed","output":[{"id":"msg-background","type":"message","status":"completed","content":[{"type":"output_text","text":"background complete","annotations":[{"type":"url_citation","title":"OpenAI","url":"https://openai.com"}]}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	w := submitRun(t, srv, "responses-background-session", `{"message":"run remotely"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("accepted response = %s, err=%v", w.Body.String(), err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background session run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}

	sess, err := srv.getOrCreateSession("responses-background-session", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	messages := sess.Manager.GetMessages()
	if len(messages) != 2 || messageText(messages[0]) != "run remotely" || messageText(messages[1]) != "background complete" {
		t.Fatalf("session messages = %#v", messages)
	}
	if len(messages[1].Attachments) != 1 || messages[1].Attachments[0].Kind != "citation" || messages[1].Attachments[0].URL != "https://openai.com" {
		t.Fatalf("background attachments = %#v", messages[1].Attachments)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 1 || getCount == 0 {
		t.Fatalf("POST /responses=%d GET /responses/{id}=%d", postCount, getCount)
	}
}

func TestChatCompletionsXBackgroundUsesResponsesCoordinator(t *testing.T) {
	var mu sync.Mutex
	var postCount, getCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode background request: %v", err)
			}
			if body["background"] != true || body["stream"] != false {
				t.Fatalf("background request = %#v", body)
			}
			_, _ = w.Write([]byte(`{"id":"resp-chat-background","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-chat-background":
			getCount++
			_, _ = w.Write([]byte(`{"id":"resp-chat-background","status":"completed","output":[{"id":"msg-chat-background","type":"message","content":[{"type":"output_text","text":"chat background complete"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	defer srv.pool.Stop()
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"run remotely"}],"x_session_id":"chat-background-session","x_background":true}`))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID     string `json:"x_run_id"`
		SessionID string `json:"x_session_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.RunID == "" || accepted.SessionID != "chat-background-session" {
		t.Fatalf("accepted response = %s, err=%v", w.Body.String(), err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat background run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 1 || getCount == 0 {
		t.Fatalf("POST /responses=%d GET /responses/{id}=%d", postCount, getCount)
	}
}

func TestChatCompletionsXBackgroundRejectsStreaming(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"run remotely"}],"stream":true,"x_background":true}`))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "x_background does not support stream") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitRunContinuesResponsesBackgroundFunctionCall(t *testing.T) {
	var mu sync.Mutex
	postCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			switch postCount {
			case 1:
				_, _ = w.Write([]byte(`{"id":"resp-tool-call","status":"completed","output":[{"id":"fc-1","type":"function_call","call_id":"call-1","name":"read","arguments":"{\"path\":\"missing.txt\"}"}]}`))
			case 2:
				if request["previous_response_id"] != "resp-tool-call" {
					t.Fatalf("previous_response_id = %#v", request["previous_response_id"])
				}
				input, ok := request["input"].([]any)
				if !ok || len(input) != 1 {
					t.Fatalf("continuation input = %#v", request["input"])
				}
				output, ok := input[0].(map[string]any)
				if !ok || output["type"] != "function_call_output" || output["call_id"] != "call-1" {
					t.Fatalf("function output = %#v", input[0])
				}
				_, _ = w.Write([]byte(`{"id":"resp-final","status":"queued"}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-final":
			_, _ = w.Write([]byte(`{"id":"resp-final","status":"completed","output":[{"id":"msg-final","type":"message","status":"completed","content":[{"type":"output_text","text":"continued after tool"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	w := submitRun(t, srv, "responses-background-function", `{"message":"read a file"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background function run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess, err := srv.getOrCreateSession("responses-background-function", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	messages := sess.Manager.GetMessages()
	if len(messages) < 4 || messageText(messages[len(messages)-1]) != "continued after tool" {
		t.Fatalf("session messages = %#v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Fatalf("POST /responses=%d, want 2", postCount)
	}
}

func TestSubmitRunContinuesResponsesBackgroundCustomToolCall(t *testing.T) {
	var mu sync.Mutex
	postCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-custom-final" {
			_, _ = w.Write([]byte(`{"id":"resp-custom-final","status":"completed","output":[{"id":"msg-custom-final","type":"message","content":[{"type":"output_text","text":"continued after custom tool"}]}]}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		postCount++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch postCount {
		case 1:
			_, _ = w.Write([]byte(`{"id":"resp-custom-tool","status":"completed","output":[{"id":"ctc-1","type":"custom_tool_call","call_id":"call-custom-1","name":"read","input":"missing.txt"}]}`))
		case 2:
			if request["previous_response_id"] != "resp-custom-tool" {
				t.Fatalf("previous_response_id = %#v", request["previous_response_id"])
			}
			input, ok := request["input"].([]any)
			if !ok || len(input) != 1 {
				t.Fatalf("continuation input = %#v", request["input"])
			}
			output, ok := input[0].(map[string]any)
			if !ok || output["type"] != "custom_tool_call_output" || output["call_id"] != "call-custom-1" {
				t.Fatalf("custom tool output = %#v", input[0])
			}
			_, _ = w.Write([]byte(`{"id":"resp-custom-final","status":"queued"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	defer srv.pool.Stop()
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	w := submitRun(t, srv, "responses-background-custom", `{"message":"read with custom input"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("decode submit response: %s, err=%v", w.Body.String(), err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background custom run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Fatalf("POST /responses=%d, want 2", postCount)
	}
}
