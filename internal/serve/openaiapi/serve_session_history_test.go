package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oschina/mothx/internal/provider"
)

// recordingProvider captures the messages sent to the provider on each Chat call.
type recordingProvider struct {
	*provider.MockProvider
	mu       sync.Mutex
	requests [][]provider.Message
}

func (p *recordingProvider) Chat(ctx context.Context, params provider.ChatParams) <-chan provider.StreamEvent {
	p.mu.Lock()
	p.requests = append(p.requests, append([]provider.Message(nil), params.Messages...))
	p.mu.Unlock()
	return p.MockProvider.Chat(ctx, params)
}

func (p *recordingProvider) snapshot() [][]provider.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]provider.Message, len(p.requests))
	copy(out, p.requests)
	return out
}

func TestServeSessionRetainsHistoryAcrossTurns(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	rec := &recordingProvider{MockProvider: provider.NewMockProvider("test", []*provider.Model{{ID: "m1", Name: "Model 1"}}, []provider.StreamEvent{
		{Type: provider.StreamStart},
		{Type: provider.StreamTextDelta, TextDelta: "ok"},
		{Type: provider.StreamDone, StopReason: "stop"},
	})}
	srv.provider = rec
	srv.model = &provider.Model{ID: "m1", Name: "Model 1"}

	doTurn := func(text string) {
		body, _ := json.Marshal(map[string]any{
			"model":  "m1",
			"stream": false,
			"messages": []map[string]any{
				{"role": "user", "content": text},
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleChatCompletions(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("turn %q status = %d body=%s", text, w.Code, w.Body.String())
		}
	}

	doTurn("My favorite fruit is mango")
	doTurn("What fruit did I just mention?")

	reqs := rec.snapshot()
	if len(reqs) < 2 {
		t.Fatalf("expected >=2 provider requests, got %d", len(reqs))
	}
	last := reqs[len(reqs)-1]
	found := false
	for _, m := range last {
		if strings.Contains(m.Content, "My favorite fruit is mango") {
			found = true
		}
		for _, c := range m.Contents {
			if c.Type == "text" && strings.Contains(c.Text, "My favorite fruit is mango") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("SECOND serve turn did not carry first turn's history — context lost within one serve session.\nmessages: %#v", last)
	}
}

// TestServeRunSubmitRetainsHistoryAcrossTurns drives the real WebUI chat path
// (POST /api/sessions/{id}/runs, async background execution) for two durable
// turns and asserts the second turn's provider request still carries the first
// turn's conversation. Unlike TestSubmitRunReplaysSessionHistory, history here
// is produced by the real durable conversation-turn persistence, not seeded via
// AppendMessage.
func TestServeRunSubmitRetainsHistoryAcrossTurns(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	srv.cfg.DefaultMode = "yolo"
	p := newHistoryRecordingProvider()
	srv.provider = p
	srv.model = p.models[0]

	sessionID := "webui-two-turn-session"
	if _, err := srv.getOrCreateSession(sessionID, srv.cfg.GetWorkDir()); err != nil {
		t.Fatalf("getOrCreateSession: %v", err)
	}

	submit := func(text string) {
		body, _ := json.Marshal(map[string]any{"message": text, "transcript": true})
		deadline := time.Now().Add(10 * time.Second)
		for {
			req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/runs", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.HandleSubmitRun(w, req)
			if w.Code == http.StatusAccepted {
				return
			}
			// The previous turn's background run may still hold the short-lived
			// session reservation; retry until it is released (mirrors a user
			// waiting for the reply before typing again).
			if w.Code == http.StatusConflict && strings.Contains(w.Body.String(), "session_reserved") && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			t.Fatalf("submit %q status = %d body=%s", text, w.Code, w.Body.String())
		}
	}

	waitTurnDone := func(wantCalls int) {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if len(p.recordedCalls()) >= wantCalls {
				if snap, err := srv.GetSessionRuntime(sessionID); err == nil && snap.ActiveRun == nil {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("turn did not complete in time (calls=%d)", len(p.recordedCalls()))
	}

	submit("My favorite fruit is mango")
	waitTurnDone(1)
	submit("What fruit did I just mention?")
	waitTurnDone(2)

	calls := p.recordedCalls()
	last := calls[len(calls)-1].Messages
	found := false
	for _, m := range last {
		if strings.Contains(m.Content, "My favorite fruit is mango") {
			found = true
		}
		for _, c := range m.Contents {
			if c.Type == "text" && strings.Contains(c.Text, "My favorite fruit is mango") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("SECOND WebUI run-submit turn did not carry first turn's history — context lost.\nmessages: %#v", last)
	}
}
