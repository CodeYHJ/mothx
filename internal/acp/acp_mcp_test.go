package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestExtractSamplingInput(t *testing.T) {
	raw := json.RawMessage(`{"maxTokens":512,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`)
	prompt, systemPrompt, maxTokens := extractSamplingInput(raw)
	if prompt != "hello" {
		t.Errorf("prompt: got %q", prompt)
	}
	if systemPrompt != "sys" {
		t.Errorf("systemPrompt: got %q", systemPrompt)
	}
	if maxTokens != 512 {
		t.Errorf("maxTokens: got %d", maxTokens)
	}
}

func TestParseJSONRawToMap(t *testing.T) {
	raw := json.RawMessage("{}")
	m := parseJSONRawToMap(raw)
	if m == nil {
		t.Fatal("expected map")
	}
	m = parseJSONRawToMap(json.RawMessage("bad"))
	if m != nil {
		t.Error("expected nil")
	}
}

func TestRequestPermissionTimeoutCleansPending(t *testing.T) {
	s := &server{
		pending:           make(map[string]chan json.RawMessage),
		w:                 &bytes.Buffer{},
		permissionTimeout: time.Millisecond,
	}

	if s.requestPermission("session-1", "tool-1", "bash", map[string]any{"command": "date"}) {
		t.Fatal("requestPermission returned true, want false on timeout")
	}

	if len(s.pending) != 0 {
		t.Fatalf("pending len = %d, want 0", len(s.pending))
	}
}

func TestWriteMessageReturnsWriteError(t *testing.T) {
	s := &server{w: errWriter{}}

	if err := s.writeMessage(map[string]any{"jsonrpc": "2.0"}); err == nil {
		t.Fatal("writeMessage error = nil, want error")
	}
}

func TestReadRequestRejectsOversizedMessage(t *testing.T) {
	s := &server{r: bufio.NewReader(strings.NewReader(strings.Repeat("x", maxRequestBytes+1) + "\n"))}

	if _, err := s.readRequest(); err == nil {
		t.Fatal("readRequest error = nil, want oversized error")
	}
}

func TestValidRPCIDRejectsNonScalarIDs(t *testing.T) {
	for _, raw := range []string{`{"x":1}`, `[1]`, `true`, `1.5`, `1e3`} {
		if validRPCID(json.RawMessage(raw)) {
			t.Errorf("validRPCID(%s) = true, want false", raw)
		}
	}
	for _, raw := range []string{`"request-1"`, `1`, `-42`, `null`} {
		if !validRPCID(json.RawMessage(raw)) {
			t.Errorf("validRPCID(%s) = false, want true", raw)
		}
	}
}

func TestResolveACPModelSelection(t *testing.T) {
	providerName, modelID, err := resolveACPModelSelection(RunOptions{Provider: "", Model: ""}, "test-provider/model-two", true)
	if err != nil || providerName != "test-provider" || modelID != "model-two" {
		t.Fatalf("environment model selection = %q/%q, err=%v", providerName, modelID, err)
	}
	providerName, modelID, err = resolveACPModelSelection(RunOptions{Provider: "test-provider"}, "test-provider/model-two", true)
	if err != nil || providerName != "test-provider" || modelID != "model-two" {
		t.Fatalf("provider completion = %q/%q, err=%v", providerName, modelID, err)
	}
	if _, _, err = resolveACPModelSelection(RunOptions{Provider: "test-provider", Model: "model-one"}, "test-provider/model-two", true); err == nil {
		t.Fatal("conflicting explicit model accepted")
	}
	if _, _, err = resolveACPModelSelection(RunOptions{}, "", true); err == nil {
		t.Fatal("empty requested model accepted")
	}
}

func TestWriteResponseSuppressesNotifications(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	if err := s.writeResponse(nil, map[string]any{"ok": true}, nil); err != nil {
		t.Fatalf("writeResponse(notification) error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification response = %q, want empty output", out.String())
	}
	if err := s.writeResponse(json.RawMessage("null"), map[string]any{"ok": true}, nil); err != nil {
		t.Fatalf("writeResponse(null id) error = %v", err)
	}
	if len(jsonLines(t, &out)) != 1 {
		t.Fatalf("explicit null ID response missing: %q", out.String())
	}
}

func TestInitializeAdvertisesStandardSessionLifecycleCapabilities(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleInitialize(rpcRequest{ID: json.RawMessage("1")})

	message := jsonLines(t, &out)[0]
	result := message["result"].(map[string]any)
	caps := result["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := caps["close"].(map[string]any); !ok {
		t.Fatalf("close capability = %#v, want object", caps["close"])
	}
	if _, ok := caps["list"].(map[string]any); !ok {
		t.Fatalf("list capability = %#v, want object", caps["list"])
	}
	if _, ok := caps["delete"].(map[string]any); !ok {
		t.Fatalf("delete capability = %#v, want object", caps["delete"])
	}
	if _, ok := caps["resume"].(map[string]any); !ok {
		t.Fatalf("resume capability = %#v, want object", caps["resume"])
	}
	if _, ok := caps["configOptions"]; ok {
		t.Fatalf("configOptions is a client capability, not an agent session capability: %#v", caps["configOptions"])
	}
	mcpCaps := result["agentCapabilities"].(map[string]any)["mcpCapabilities"].(map[string]any)
	if _, ok := mcpCaps["stdio"]; ok {
		t.Fatalf("stdio must not be advertised as an MCP extension: %#v", mcpCaps)
	}
	meta := result["agentCapabilities"].(map[string]any)["_meta"].(map[string]any)
	if _, ok := meta[mothxExtensionNamespace]; !ok {
		t.Fatalf("missing MothX extension capability: %#v", meta)
	}
}

func TestInitializeParsesTypedClientCapabilities(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleInitialize(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(`{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"auth":{"terminal":false},"elicitation":{"form":{},"url":{}},"session":{"configOptions":{"boolean":{}}}}}`),
	})
	if !s.clientCaps.FS.ReadTextFile || !s.clientCaps.FS.WriteTextFile || !s.clientCaps.Terminal {
		t.Fatalf("typed fs/terminal capabilities = %#v", s.clientCaps)
	}
	if s.clientCaps.Auth == nil || s.clientCaps.Auth.Terminal {
		t.Fatalf("typed auth capabilities = %#v", s.clientCaps.Auth)
	}
	if s.clientCaps.Elicitation == nil || s.clientCaps.Elicitation.Form == nil || s.clientCaps.Elicitation.URL == nil {
		t.Fatalf("typed elicitation capabilities = %#v", s.clientCaps.Elicitation)
	}
	if s.clientCaps.Session == nil || s.clientCaps.Session.ConfigOptions == nil || s.clientCaps.Session.ConfigOptions.Boolean == nil {
		t.Fatalf("typed session capabilities = %#v", s.clientCaps.Session)
	}
	message := jsonLines(t, &out)[0]
	caps := message["result"].(map[string]any)["agentCapabilities"].(map[string]any)["sessionCapabilities"].(map[string]any)
	if _, ok := caps["additionalDirectories"].(map[string]any); !ok {
		t.Fatalf("additionalDirectories capability = %#v, want object", caps["additionalDirectories"])
	}
}

func TestQuestionUsesStandardElicitationForm(t *testing.T) {
	lines := make(chan string, 1)
	s := &server{
		w:       acpLineWriter{lines: lines},
		pending: make(map[string]chan json.RawMessage),
		clientCaps: clientCapabilities{Elicitation: &clientElicitationCapabilities{
			Form: &struct{}{},
		}},
	}
	answer := make(chan string, 1)
	go func() {
		answer <- s.requestQuestion(context.Background(), "session-1", "Continue?", []string{"yes", "no"}, "Choose one")
	}()

	var request map[string]any
	select {
	case line := <-lines:
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("standard elicitation request was not sent")
	}
	if request["method"] != "elicitation/create" {
		t.Fatalf("request method = %#v, want elicitation/create", request["method"])
	}
	id, ok := request["id"].(string)
	if !ok || id == "" {
		t.Fatalf("request id = %#v, want string", request["id"])
	}
	params := request["params"].(map[string]any)
	if params["sessionId"] != "session-1" || params["message"] != "Continue?" || params["mode"] != "form" {
		t.Fatalf("elicitation params = %#v", params)
	}
	schema := params["requestedSchema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	answerSchema := properties["answer"].(map[string]any)
	if answerSchema["type"] != "string" {
		t.Fatalf("answer schema = %#v", answerSchema)
	}

	rawID, _ := json.Marshal(id)
	s.deliverResponse(rawID, json.RawMessage(`{"action":"accept","content":{"answer":"yes"}}`), nil)
	select {
	case got := <-answer:
		if got != "yes" {
			t.Fatalf("answer = %q, want yes", got)
		}
	case <-time.After(time.Second):
		t.Fatal("standard elicitation answer was not delivered")
	}
}

type acpLineWriter struct {
	lines chan<- string
}

func (w acpLineWriter) Write(p []byte) (int, error) {
	w.lines <- string(p)
	return len(p), nil
}

func TestCloseSessionCancelsAndRemovesRuntime(t *testing.T) {
	var out bytes.Buffer
	cancelled := make(chan struct{})
	s := &server{
		w: &out,
		sessions: map[string]*sessionRuntime{
			"session-1": {cancel: func() { close(cancelled) }},
		},
	}
	s.handleCloseSession(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(`{"sessionId":"session-1"}`),
	})

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel active session")
	}
	if _, ok := s.sessions["session-1"]; ok {
		t.Fatal("closed session remains in runtime map")
	}
	message := jsonLines(t, &out)[0]
	if _, ok := message["result"].(map[string]any); !ok {
		t.Fatalf("close result = %#v, want empty object", message["result"])
	}
}

func TestListSessionsReturnsPersistedSessions(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	newTestSession(t, cwd, dir, "session-one", 1)
	newTestSession(t, cwd, dir, "session-two", 2)
	for id, providerName := range map[string]string{"session-one": "moark", "session-two": "volcengine-agentplan"} {
		mgr, err := session.OpenByIDExact(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.AppendModelChange(providerName, "model"); err != nil {
			t.Fatal(err)
		}
	}

	var out bytes.Buffer
	s := &server{settings: &config.Settings{SessionDir: dir}, w: &out}
	s.handleListSessions(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(fmt.Sprintf(`{"cwd":%q}`, cwd)),
	})

	message := jsonLines(t, &out)[0]
	result := message["result"].(map[string]any)
	sessions := result["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("listed sessions = %d, want 2", len(sessions))
	}
	for _, item := range sessions {
		listed := item.(map[string]any)
		if listed["cwd"] != cwd {
			t.Fatalf("listed cwd = %q, want %q", listed["cwd"], cwd)
		}
		if listed["sessionId"] == "" {
			t.Fatalf("missing session ID: %#v", listed)
		}
		if listed["provider"] == "" || listed["model"] == "" {
			t.Fatalf("missing provider/model: %#v", listed)
		}
	}
}

func TestListSessionsUsesOpaqueCursor(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	for i := 0; i < sessionListPageSize+1; i++ {
		newTestSession(t, cwd, dir, fmt.Sprintf("page-%03d", i), 0)
	}
	var out bytes.Buffer
	s := &server{settings: &config.Settings{SessionDir: dir}, w: &out}
	s.handleListSessions(rpcRequest{ID: json.RawMessage("1"), Params: json.RawMessage(fmt.Sprintf(`{"cwd":%q}`, cwd))})
	first := jsonLines(t, &out)[0]["result"].(map[string]any)
	if len(first["sessions"].([]any)) != sessionListPageSize {
		t.Fatalf("first page size = %d, want %d", len(first["sessions"].([]any)), sessionListPageSize)
	}
	cursor, ok := first["nextCursor"].(string)
	if !ok || cursor == "" || strings.Trim(cursor, "0123456789") == "" {
		t.Fatalf("nextCursor = %#v, want opaque non-numeric token", first["nextCursor"])
	}
	out.Reset()
	s.handleListSessions(rpcRequest{ID: json.RawMessage("2"), Params: json.RawMessage(fmt.Sprintf(`{"cwd":%q,"cursor":%q}`, cwd, cursor))})
	second := jsonLines(t, &out)[0]["result"].(map[string]any)
	if len(second["sessions"].([]any)) != 1 {
		t.Fatalf("second page size = %d, want 1", len(second["sessions"].([]any)))
	}
	out.Reset()
	s.handleListSessions(rpcRequest{ID: json.RawMessage("3"), Params: json.RawMessage(fmt.Sprintf(`{"cwd":%q,"cursor":"bad"}`, cwd))})
	if code := jsonLines(t, &out)[0]["error"].(map[string]any)["code"]; code != float64(-32602) {
		t.Fatalf("invalid cursor error code = %#v, want -32602", code)
	}
}

func TestLoadSessionReplaysAllMessages(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	newTestSession(t, cwd, dir, "full-history", 41)

	var out bytes.Buffer
	s := &server{
		settings:   &config.Settings{SessionDir: dir},
		sbMgr:      sandbox.NewManager(cwd),
		sessions:   make(map[string]*sessionRuntime),
		toolTitles: make(map[string]string),
		mcpNotify:  make(map[string]bool),
		w:          &out,
	}
	s.handleLoadSession(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(fmt.Sprintf(`{"sessionId":"full-history","cwd":%q}`, cwd)),
	})

	messages := jsonLines(t, &out)
	updates := 0
	for _, message := range messages {
		if message["method"] == "session/update" {
			updates++
		}
	}
	if updates != 41 {
		t.Fatalf("replayed updates = %d, want 41", updates)
	}
	rt := s.sessions["full-history"]
	if rt == nil || rt.runtime == nil || rt.runtime.Manager != rt.mgr || rt.runtime.Registry != rt.registry {
		t.Fatalf("ACP session is not backed by shared runtime: %#v", rt)
	}
}

func TestLoadSessionSwitchesToPersistedProvider(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	mgr := session.New(cwd, dir)
	if err := mgr.InitWithID("foreign-provider-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AppendModelChange("volcengine-agentplan", "ark-code-latest"); err != nil {
		t.Fatal(err)
	}
	moarkModel := &provider.Model{ID: "moark-model", Name: "Moark Model"}
	volcModel := &provider.Model{ID: "ark-code-latest", Name: "Ark Code Latest"}
	moark := provider.NewMockProvider("moark", []*provider.Model{moarkModel}, nil)
	volc := provider.NewMockProvider("volcengine-agentplan", []*provider.Model{volcModel}, nil)
	var out bytes.Buffer
	s := &server{
		settings:     &config.Settings{SessionDir: dir},
		p:            moark,
		providerName: "moark",
		m:            moarkModel,
		providers:    map[string]provider.Provider{"moark": moark, "volcengine-agentplan": volc},
		sbMgr:        sandbox.NewManager(cwd),
		sessions:     make(map[string]*sessionRuntime),
		toolTitles:   make(map[string]string),
		mcpNotify:    make(map[string]bool),
		w:            &out,
	}
	s.handleLoadSession(rpcRequest{ID: json.RawMessage("1"), Params: json.RawMessage(fmt.Sprintf(`{"sessionId":"foreign-provider-session","cwd":%q}`, cwd))})
	message := jsonLines(t, &out)[0]
	result := message["result"].(map[string]any)
	options := result["configOptions"].([]any)
	if got := configOptionCurrent(options, "provider"); got != "volcengine-agentplan" {
		t.Fatalf("loaded provider = %q", got)
	}
	if got := configOptionCurrent(options, "model"); got != "volcengine-agentplan/ark-code-latest" {
		t.Fatalf("loaded model = %q", got)
	}
	if _, err := s.closeSessionRuntime("foreign-provider-session"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSessionProviderMismatchIsStructured(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	mgr := session.New(cwd, dir)
	if err := mgr.InitWithID("missing-provider-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AppendModelChange("missing-provider", "model"); err != nil {
		t.Fatal(err)
	}
	model := &provider.Model{ID: "moark-model"}
	moark := provider.NewMockProvider("moark", []*provider.Model{model}, nil)
	var out bytes.Buffer
	s := &server{
		settings:     &config.Settings{SessionDir: dir},
		p:            moark,
		providerName: "moark",
		m:            model,
		providers:    map[string]provider.Provider{"moark": moark},
		sbMgr:        sandbox.NewManager(cwd),
		sessions:     make(map[string]*sessionRuntime),
		toolTitles:   make(map[string]string),
		mcpNotify:    make(map[string]bool),
		w:            &out,
	}
	s.handleLoadSession(rpcRequest{ID: json.RawMessage("1"), Params: json.RawMessage(fmt.Sprintf(`{"sessionId":"missing-provider-session","cwd":%q}`, cwd))})
	message := jsonLines(t, &out)[0]
	errPayload := message["error"].(map[string]any)
	if errPayload["code"] != float64(-32002) || errPayload["message"] != "session provider mismatch" {
		t.Fatalf("provider mismatch error = %#v", errPayload)
	}
	data := errPayload["data"].(map[string]any)
	if data["sessionProvider"] != "missing-provider" || data["sessionModel"] != "model" || data["currentProvider"] != "moark" {
		t.Fatalf("provider mismatch data = %#v", data)
	}
}

func TestLoadBoundSessionUsesPersistedRuntimePolicy(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	bound, err := session.CreateBound(cwd, dir, "wechat", "acp-policy-user")
	if err != nil {
		t.Fatalf("CreateBound: %v", err)
	}
	var out bytes.Buffer
	s := testSessionServer(cwd, dir, &out)
	s.handleLoadSession(rpcRequest{
		ID: json.RawMessage("1"),
		Params: json.RawMessage(fmt.Sprintf(
			`{"sessionId":%q,"cwd":%q}`,
			bound.GetHeader().ID,
			cwd,
		)),
	})

	rt := s.sessions[bound.GetHeader().ID]
	if rt == nil || rt.runtime == nil {
		t.Fatalf("bound ACP runtime = %#v", rt)
	}
	if rt.runtime.Source != agentruntime.SourceWeChat || rt.runtime.EntrySource != agentruntime.SourceACP {
		t.Fatalf("runtime source/entry = %q/%q, want wechat/acp", rt.runtime.Source, rt.runtime.EntrySource)
	}
	_, mode, err := rt.runtime.ResolvePolicy("", agentruntime.ModeAgent, agentruntime.ModeAgent)
	if err != nil {
		t.Fatalf("ResolvePolicy: %v", err)
	}
	if mode != agentruntime.ModeYolo {
		t.Fatalf("ACP bound mode = %q, want yolo", mode)
	}
}

func TestResumeSessionDoesNotReplayMessages(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	newTestSession(t, cwd, dir, "resume-history", 2)

	var out bytes.Buffer
	s := testSessionServer(cwd, dir, &out)
	s.handleResumeSession(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(fmt.Sprintf(`{"sessionId":"resume-history","cwd":%q}`, cwd)),
	})

	messages := jsonLines(t, &out)
	if len(messages) != 1 || messages[0]["result"] == nil {
		t.Fatalf("resume messages = %#v, want one response without replay", messages)
	}
}

func TestDeleteSessionRemovesPersistedSession(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	newTestSession(t, cwd, dir, "delete-me", 1)

	var out bytes.Buffer
	s := &server{settings: &config.Settings{SessionDir: dir}, w: &out}
	s.handleDeleteSession(rpcRequest{
		ID:     json.RawMessage("1"),
		Params: json.RawMessage(`{"sessionId":"delete-me"}`),
	})
	if _, err := session.OpenByID(cwd, dir, "delete-me"); err == nil {
		t.Fatal("deleted session is still available")
	}
}

func TestNewSessionMCPFailureRollsBackPersistedSession(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	var out bytes.Buffer
	s := testSessionServer(cwd, dir, &out)

	s.handleNewSession(rpcRequest{
		ID: json.RawMessage("1"),
		Params: json.RawMessage(fmt.Sprintf(
			`{"cwd":%q,"mcpServers":[{"name":"broken","type":"stdio"}]}`,
			cwd,
		)),
	})

	messages := jsonLines(t, &out)
	if len(messages) != 1 || messages[0]["error"] == nil {
		t.Fatalf("new session response = %#v, want MCP error", messages)
	}
	if len(s.sessions) != 0 {
		t.Fatalf("runtime sessions = %d, want 0", len(s.sessions))
	}
	persisted, err := session.ListForDir(cwd, dir)
	if err != nil {
		t.Fatalf("list persisted sessions: %v", err)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted sessions = %#v, want none after MCP failure", persisted)
	}
}

func TestCancelRequestCancelsMatchingPrompt(t *testing.T) {
	cancelled := make(chan struct{})
	s := &server{sessions: map[string]*sessionRuntime{
		"session-1": {promptID: "prompt-1", cancel: func() { close(cancelled) }},
	}}
	s.handleCancelRequest(rpcRequest{Params: json.RawMessage(`{"requestId":"prompt-1"}`)})
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("$/cancel_request did not cancel matching prompt")
	}
}

func TestCancelRejectsInvalidOrUnknownSession(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out, sessions: map[string]*sessionRuntime{}}
	s.handleCancel(rpcRequest{ID: json.RawMessage("1"), Params: json.RawMessage(`{}`)})
	message := jsonLines(t, &out)[0]
	if code := message["error"].(map[string]any)["code"]; code != float64(-32602) {
		t.Fatalf("invalid cancel error code = %#v, want -32602", code)
	}
	out.Reset()
	s.handleCancel(rpcRequest{ID: json.RawMessage("2"), Params: json.RawMessage(`{"sessionId":"missing"}`)})
	message = jsonLines(t, &out)[0]
	if code := message["error"].(map[string]any)["code"]; code != float64(-32000) {
		t.Fatalf("unknown cancel error code = %#v, want -32000", code)
	}
}

func TestPlanUpdateUsesStandardPlanVariant(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{
		Type: agentpkg.EventPlanUpdate,
		Plan: &agentpkg.TaskPlan{
			Title: "Implementation",
			Steps: []agentpkg.PlanStep{{Title: "Inspect", Status: "running"}},
		},
	})
	update := jsonLines(t, &out)[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "plan" {
		t.Fatalf("plan update type = %#v", update)
	}
	entry := update["entries"].([]any)[0].(map[string]any)
	if entry["content"] != "Inspect" || entry["priority"] != "medium" || entry["status"] != "in_progress" {
		t.Fatalf("plan entry = %#v", entry)
	}
}

func TestMothxStatusUsesExtensionNotification(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{Type: agentpkg.EventStatus, StatusMessage: "working"})
	message := jsonLines(t, &out)[0]
	if message["method"] != "_mothx/session_event" {
		t.Fatalf("status method = %#v, want extension notification", message["method"])
	}
}

func TestMothxRetryUsesStructuredExtensionNotification(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{
		Type:             agentpkg.EventRetry,
		RetryAttempt:     2,
		RetryMaxAttempts: 4,
		RetryAfterMS:     1500,
		RetryReason:      "provider diagnostic that must not be rendered",
	})

	params := jsonLines(t, &out)[0]["params"].(map[string]any)
	if params["event"] != "retrying" || params["attempt"] != float64(2) || params["maxAttempts"] != float64(4) || params["retryAfterMs"] != float64(1500) {
		t.Fatalf("retry params = %#v, want structured retry metadata", params)
	}
	message, _ := params["message"].(string)
	if !strings.Contains(message, "Retrying (attempt 2/4); waiting 1.5s...") || strings.Contains(message, "provider diagnostic") {
		t.Fatalf("retry message = %q, want safe structured retry status", message)
	}
}

func TestACPFailureRPCErrorPreservesProviderDiagnostic(t *testing.T) {
	rpcErr := acpFailureRPCError(errors.New("upstream HTTP 503 request_id=provider-secret"), nil, agentruntime.PhaseModel)
	if rpcErr.Message != "upstream HTTP 503 request_id=provider-secret" {
		t.Fatalf("RPC error = %#v, want provider diagnostic", rpcErr)
	}
	data, ok := rpcErr.Data.(map[string]any)
	if !ok {
		t.Fatalf("RPC error data = %#v, want structured data", rpcErr.Data)
	}
	if detail, ok := data["detail"].(string); !ok || detail != rpcErr.Message {
		t.Fatalf("RPC error data detail = %#v, want provider diagnostic", data["detail"])
	}

	observed := &agentruntime.ErrorInfo{Code: "event_stream_interrupted", Message: "The run stopped before it could finish."}
	rpcErr = acpFailureRPCError(errors.New("raw stream diagnostic"), observed, agentruntime.PhaseTransport)
	if rpcErr.Message != observed.Message {
		t.Fatalf("observed RPC error = %#v, want Runtime observation message", rpcErr)
	}
}

func TestHostedItemUsesNonExecutableToolUpdate(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{Type: agentpkg.EventHostedItem, HostedItem: &agentpkg.HostedItem{
		ID: "search-1", Type: "web_search_call", Status: "completed",
	}})
	update := jsonLines(t, &out)[0]["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "tool_call_update" || update["toolCallId"] != "search-1" || update["kind"] != "other" || update["status"] != "completed" {
		t.Fatalf("hosted ACP update = %#v", update)
	}
}

func TestToolDiffUsesACPStructuredContentAndLocations(t *testing.T) {
	var out bytes.Buffer
	oldText := "before\n"
	path := "/tmp/acp-diff.txt"
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{
		Type:       agentpkg.EventToolExecutionEnd,
		ToolCallID: "write-1",
		ToolName:   "write_file",
		ToolDiff:   &agentpkg.FileDiff{Path: path, OldText: &oldText, NewText: "after\n"},
	})
	update := jsonLines(t, &out)[0]["params"].(map[string]any)["update"].(map[string]any)
	contents := update["content"].([]any)
	if len(contents) != 1 {
		t.Fatalf("tool content = %#v, want one diff item", contents)
	}
	diff := contents[0].(map[string]any)
	if diff["type"] != "diff" || diff["path"] != path || diff["oldText"] != oldText || diff["newText"] != "after\n" {
		t.Fatalf("ACP diff = %#v", diff)
	}
	locations := update["locations"].([]any)
	if len(locations) != 1 || locations[0].(map[string]any)["path"] != path {
		t.Fatalf("ACP locations = %#v", locations)
	}
}

func TestToolDiffIncludesNullOldTextForCreatedFile(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out}
	s.handleAgentEvent("session-1", agentpkg.Event{
		Type:       agentpkg.EventToolExecutionEnd,
		ToolCallID: "write-1",
		ToolName:   "write_file",
		ToolDiff:   &agentpkg.FileDiff{Path: "/tmp/new.txt", NewText: "new"},
	})
	diff := jsonLines(t, &out)[0]["params"].(map[string]any)["update"].(map[string]any)["content"].([]any)[0].(map[string]any)
	if value, ok := diff["oldText"]; !ok || value != nil {
		t.Fatalf("created-file oldText = %#v, want explicit null", diff["oldText"])
	}
}

func TestStreamedContentChunksShareMessageID(t *testing.T) {
	var out bytes.Buffer
	s := &server{w: &out, sessions: map[string]*sessionRuntime{}}
	s.handleAgentEvent("session-1", agentpkg.Event{Type: agentpkg.EventTextDelta, TextDelta: "hello"})
	s.handleAgentEvent("session-1", agentpkg.Event{Type: agentpkg.EventTextDelta, TextDelta: " world"})
	lines := jsonLines(t, &out)
	if len(lines) != 2 {
		t.Fatalf("updates = %d, want 2", len(lines))
	}
	first := lines[0]["params"].(map[string]any)["update"].(map[string]any)
	second := lines[1]["params"].(map[string]any)["update"].(map[string]any)
	if first["messageId"] == "" || first["messageId"] != second["messageId"] {
		t.Fatalf("message IDs = %#v and %#v, want stable non-empty ID", first["messageId"], second["messageId"])
	}
}

func TestPromptSupportsResourceLinksAndRejectsUnadvertisedContent(t *testing.T) {
	text, err := promptToText([]contentBlock{{Type: "resource_link", Name: "notes", URI: "file:///notes.md"}})
	if err != nil || text != "notes: file:///notes.md" {
		t.Fatalf("resource link = %q, %v", text, err)
	}
	if _, err := promptToText([]contentBlock{{Type: "image"}}); err == nil {
		t.Fatal("unadvertised image content was accepted")
	}
	size := 12
	message, err := promptToMessage([]contentBlock{{Type: "text", Text: "read this"}, {Type: "resource_link", Name: "notes", Title: "Notes", Description: "A note", URI: "file:///notes.md", MimeType: "text/markdown", Size: &size}})
	if err != nil || len(message.Contents) != 2 || message.Contents[1].Type != "file" || message.Contents[1].File == nil || message.Contents[1].File.URL != "file:///notes.md" || message.Contents[1].File.Title != "Notes" || message.Contents[1].File.Description != "A note" || message.Contents[1].File.Size == nil || *message.Contents[1].File.Size != size {
		t.Fatalf("resource link message = %#v, %v", message, err)
	}
}

func TestNormalizeAdditionalDirectories(t *testing.T) {
	directories, err := agentruntime.NormalizeAdditionalDirectories([]string{"/tmp/extra/..", "/tmp/other", "/tmp/other"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(directories), "[/tmp /tmp/other]"; got != want {
		t.Fatalf("directories = %s, want %s", got, want)
	}
	if _, err := agentruntime.NormalizeAdditionalDirectories([]string{"relative"}); err == nil {
		t.Fatal("relative additional directory was accepted")
	}
}

func TestUsageEventEmitsUsageUpdate(t *testing.T) {
	var out bytes.Buffer
	s := &server{
		m: &provider.Model{ContextWindow: 100, Cost: provider.ModelPricing{Input: 1, Output: 2}},
		sessions: map[string]*sessionRuntime{
			"session-1": {},
		},
		w: &out,
	}
	s.handleAgentEvent("session-1", agentpkg.Event{
		Type: agentpkg.EventUsage,
		Usage: &agentpkg.Usage{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
		ContextUsage: &agentpkg.ContextUsage{Tokens: 20, ContextWindow: 100},
	})

	message := jsonLines(t, &out)[0]
	update := message["params"].(map[string]any)["update"].(map[string]any)
	if update["sessionUpdate"] != "usage_update" || update["used"] != float64(20) || update["size"] != float64(100) {
		t.Fatalf("usage update = %#v", update)
	}
	cost := update["cost"].(map[string]any)
	if cost["currency"] != "USD" || cost["amount"] != 0.00002 {
		t.Fatalf("usage cost = %#v", cost)
	}
}

func newTestSession(t *testing.T, cwd, dir, id string, messages int) {
	t.Helper()
	mgr := session.New(cwd, dir)
	if err := mgr.InitWithID(id); err != nil {
		t.Fatalf("initialize session: %v", err)
	}
	for i := 0; i < messages; i++ {
		if _, err := mgr.AppendMessage(provider.NewUserMessage(fmt.Sprintf("message %d", i))); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}
}

func testSessionServer(cwd, dir string, out *bytes.Buffer) *server {
	return &server{
		settings:   &config.Settings{SessionDir: dir},
		sbMgr:      sandbox.NewManager(cwd),
		sessions:   make(map[string]*sessionRuntime),
		toolTitles: make(map[string]string),
		mcpNotify:  make(map[string]bool),
		w:          out,
	}
}

func jsonLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("decode JSON-RPC message %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
