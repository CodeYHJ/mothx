package openaiapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatCompletionsRejectsVibeCodingExtensionFields(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"messages":[{"role":"user","content":"hello"}],
		"x_session_id":"legacy-session"
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unsupported extension field") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestChatHandlerRejectsConcurrentDefaultSession(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()

	sess, err := srv.getOrCreateSession("", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatal(err)
	}
	sess.Lock()
	defer sess.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"type":"session_run_active"`) {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatHandlerRejectsWhenConcurrencyLimitReached(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	srv.runSlots = make(chan struct{}, 1)
	srv.runSlots <- struct{}{}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	w := httptest.NewRecorder()
	srv.handleChatCompletions(w, req)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"type":"concurrency_limit_reached"`) {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestChatCompletionResponseHasNoVibeCodingExtensionFields(t *testing.T) {
	payload := ChatCompletionResponse{
		ID:     "chatcmpl-test",
		Object: "chat.completion",
		Model:  "test-model",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, field := range []string{"x_session_id", "x_command", "x_tool_calls", "x_attachments"} {
		if strings.Contains(encoded, field) {
			t.Fatalf("response contains removed extension field %q: %s", field, encoded)
		}
	}
}
