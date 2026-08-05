package openaiapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

// historyRecordingProvider captures Chat params so tests can assert that the
// submit-run path replays persisted session history into the agent.
type historyRecordingProvider struct {
	mu     sync.Mutex
	models []*provider.Model
	calls  []provider.ChatParams
}

func newHistoryRecordingProvider() *historyRecordingProvider {
	return &historyRecordingProvider{
		models: []*provider.Model{{ID: "m1", Name: "Model 1", ContextWindow: 32768, MaxTokens: 2048}},
	}
}

func (p *historyRecordingProvider) Chat(ctx context.Context, params provider.ChatParams) <-chan provider.StreamEvent {
	p.mu.Lock()
	p.calls = append(p.calls, provider.ChatParams{
		Messages:     append([]provider.Message(nil), params.Messages...),
		SystemPrompt: params.SystemPrompt,
	})
	p.mu.Unlock()
	ch := make(chan provider.StreamEvent, 3)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{Type: provider.StreamStart}
		ch <- provider.StreamEvent{Type: provider.StreamTextDelta, TextDelta: "ok"}
		ch <- provider.StreamEvent{Type: provider.StreamDone}
	}()
	return ch
}

func (p *historyRecordingProvider) Name() string              { return "history-recording" }
func (p *historyRecordingProvider) API() string               { return "openai-chat" }
func (p *historyRecordingProvider) Models() []*provider.Model { return p.models }
func (p *historyRecordingProvider) GetModel(id string) *provider.Model {
	for _, m := range p.models {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func (p *historyRecordingProvider) recordedCalls() []provider.ChatParams {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.ChatParams(nil), p.calls...)
}

// TestSubmitRunReplaysSessionHistory verifies that a background run submitted
// through POST /api/sessions/{id}/runs (the WebUI chat path) loads the
// persisted session history into the fresh agent — including after a runtime
// mode switch — so the model does not treat the turn as a new conversation.
func TestSubmitRunReplaysSessionHistory(t *testing.T) {
	srv := newTestServer(t)
	srv.cfg.DefaultMode = "yolo"
	p := newHistoryRecordingProvider()
	srv.provider = p
	srv.model = p.models[0]

	sessionID := "run-history-session"
	sess, err := srv.getOrCreateSession(sessionID, srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("getOrCreateSession: %v", err)
	}
	if _, err := sess.Manager.AppendMessage(provider.NewUserMessage("之前的问题")); err != nil {
		t.Fatalf("append user history: %v", err)
	}
	assistant := provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "之前的回答"}})
	if _, err := sess.Manager.AppendMessage(assistant); err != nil {
		t.Fatalf("append assistant history: %v", err)
	}

	// Simulate the WebUI runtime mode switch before the next turn.
	mode := "agent"
	if _, err := srv.PatchSessionRuntime(sessionID, SessionRuntimePatch{Mode: &mode}); err != nil {
		t.Fatalf("PatchSessionRuntime: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/runs", strings.NewReader(`{"message":"接着聊","transcript":true}`))
	w := httptest.NewRecorder()
	srv.HandleSubmitRun(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if calls := p.recordedCalls(); len(calls) > 0 {
			msgs := calls[0].Messages
			// The agent prepends a system-injected session context message, so
			// expect: [session context, user history, assistant history, new user].
			if len(msgs) != 4 {
				t.Fatalf("provider received %d messages, want 4 (context + 2 history + 1 new): %#v", len(msgs), msgs)
			}
			if !msgs[0].SystemInjected {
				t.Fatalf("msgs[0] should be the system-injected session context: %#v", msgs[0])
			}
			if msgs[1].Role != "user" || messageText(msgs[1]) != "之前的问题" {
				t.Fatalf("msgs[1] = %s/%q, want user history", msgs[1].Role, messageText(msgs[1]))
			}
			if msgs[2].Role != "assistant" || messageText(msgs[2]) != "之前的回答" {
				t.Fatalf("msgs[2] = %s/%q, want assistant history", msgs[2].Role, messageText(msgs[2]))
			}
			if msgs[3].Role != "user" || messageText(msgs[3]) != "接着聊" {
				t.Fatalf("msgs[3] = %s/%q, want new user message", msgs[3].Role, messageText(msgs[3]))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("provider was not called within deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForProviderCall(t *testing.T, p *historyRecordingProvider) provider.ChatParams {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if calls := p.recordedCalls(); len(calls) > 0 {
			return calls[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("provider was not called within deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newHistoryRecordingServer(t *testing.T) (*Server, *historyRecordingProvider) {
	t.Helper()
	srv := newTestServer(t)
	srv.cfg.DefaultMode = "yolo"
	p := newHistoryRecordingProvider()
	srv.provider = p
	srv.model = p.models[0]
	return srv, p
}

func submitRun(t *testing.T, srv *Server, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/runs", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.HandleSubmitRun(w, req)
	return w
}

func TestSubmitRunRejectsWhenSharedRuntimeLockIsHeld(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	sess, err := srv.getOrCreateSession("runtime-lock-submit", srv.cfg.GetWorkDir())
	if err != nil || sess == nil {
		t.Fatalf("create session: %v", err)
	}
	release := session.LockRuntime(srv.settings.GetSessionDir(), sess.ID)
	defer release()
	w := submitRun(t, srv, sess.ID, `{"message":"must conflict"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
}

// TestSubmitRunAppliesImages verifies that base64 data-URL images in the
// submit body reach the provider as image content blocks.
func TestSubmitRunAppliesImages(t *testing.T) {
	srv, p := newHistoryRecordingServer(t)
	p.models[0].Input = []string{"text", "image"}

	w := submitRun(t, srv, "run-images-session", `{"message":"看图","images":["data:image/png;base64,iVBORw0KGgo="],"transcript":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}

	call := waitForProviderCall(t, p)
	last := call.Messages[len(call.Messages)-1]
	if last.Role != "user" {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	textFound := false
	var img *provider.ImageContent
	for _, block := range last.Contents {
		switch block.Type {
		case "text":
			if block.Text == "看图" {
				textFound = true
			}
		case "image":
			img = block.Image
		}
	}
	if !textFound {
		t.Fatalf("text block missing from submitted message: %#v", last.Contents)
	}
	if img == nil {
		t.Fatalf("image block missing from submitted message: %#v", last.Contents)
	}
	if img.MimeType != "image/png" || img.Data != "iVBORw0KGgo=" {
		t.Fatalf("image block = %s/%q, want image/png/iVBORw0KGgo=", img.MimeType, img.Data)
	}
}

// TestSubmitRunRejectsImagesForTextOnlyModel verifies the model capability
// guard mirrors the chat-completions path.
func TestSubmitRunRejectsImagesForTextOnlyModel(t *testing.T) {
	srv, _ := newHistoryRecordingServer(t)
	w := submitRun(t, srv, "run-images-unsupported", `{"message":"看图","images":["data:image/png;base64,iVBORw0KGgo="]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// TestSubmitRunAppliesToolOptionsAndMode verifies the submit body `tools`
// array is applied as the authoritative capability set and an explicit `mode`
// is persisted for subsequent runs.
func TestSubmitRunAppliesToolOptionsAndMode(t *testing.T) {
	srv, p := newHistoryRecordingServer(t)

	sessionID := "run-tools-session"
	w := submitRun(t, srv, sessionID, `{"message":"hi","mode":"plan","tools":["webSearch"],"transcript":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	waitForProviderCall(t, p)

	caps, err := srv.GetSessionCapabilities(sessionID)
	if err != nil {
		t.Fatalf("GetSessionCapabilities: %v", err)
	}
	if caps.Mode != "plan" {
		t.Fatalf("mode = %q, want plan (submit mode must persist)", caps.Mode)
	}
	if !caps.WebSearch {
		t.Fatal("webSearch should be enabled by tools array")
	}
	if caps.Browser || caps.MultiAgent || caps.Workflows || caps.DelegateMode || caps.A2AMaster {
		t.Fatalf("unlisted capabilities must be disabled: %#v", caps)
	}

	// Unknown tool names are rejected.
	w = submitRun(t, srv, "run-tools-bogus", `{"message":"hi","tools":["bogusTool"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}

// TestSubmitRunAppliesSkills verifies the submit body `skills` array activates
// session skills, and unknown skills are rejected.
func TestSubmitRunAppliesSkills(t *testing.T) {
	srv, p := newHistoryRecordingServer(t)

	skillDir := filepath.Join(srv.cfg.GetWorkDir(), ".skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Demo Skill\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	sessionID := "run-skills-session"
	w := submitRun(t, srv, sessionID, `{"message":"hi","skills":["demo-skill"],"transcript":true}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	waitForProviderCall(t, p)

	sess := srv.pool.Get(sessionID)
	if sess == nil {
		t.Fatal("session not found in pool")
	}
	if !sess.ActiveSkills["demo-skill"] {
		t.Fatalf("demo-skill should be active: %#v", sess.ActiveSkills)
	}

	// Unknown skills are rejected.
	w = submitRun(t, srv, "run-skills-bogus", `{"message":"hi","skills":["missing-skill"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("submit status = %d, want 400, body = %s", w.Code, w.Body.String())
	}
}
