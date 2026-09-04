package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCmdESMLifecycle(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	id := "cmd-esm-session"
	sess, err := srv.getOrCreateSession(id, srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatal(err)
	}

	res := srv.cmdESM(sess, "/esm")
	if res.Error || !strings.Contains(res.Message, "Status: none") {
		t.Fatalf("empty status = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm finish the migration and keep tests green")
	if res.Error || !strings.Contains(res.Message, "finish the migration and keep tests green") {
		t.Fatalf("create = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm another objective")
	if !res.Error || !strings.Contains(res.Message, "already exists") {
		t.Fatalf("duplicate create = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm guide prioritize failing tests")
	if res.Error || !strings.Contains(res.Message, "Guidance queued") {
		t.Fatalf("guide = %#v", res)
	}
	pending, err := srv.esmStore().PendingGuidance(context.Background(), id)
	if err != nil || len(pending) != 1 || pending[0].Guidance != "prioritize failing tests" {
		t.Fatalf("pending guidance = %#v, err = %v", pending, err)
	}

	res = srv.cmdESM(sess, "/esm pause")
	if res.Error || !strings.Contains(res.Message, "paused") {
		t.Fatalf("pause = %#v", res)
	}
	res = srv.cmdESM(sess, "/esm resume")
	if res.Error || !strings.Contains(res.Message, "active") {
		t.Fatalf("resume = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm edit ship the new objective text")
	if res.Error || !strings.Contains(res.Message, "ship the new objective text") {
		t.Fatalf("edit = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm")
	if res.Error || !strings.Contains(res.Message, "ship the new objective text") || !strings.Contains(res.Message, "/esm guide") {
		t.Fatalf("status = %#v", res)
	}

	res = srv.cmdESM(sess, "/esm clear")
	if res.Error || !strings.Contains(res.Message, "cleared") {
		t.Fatalf("clear = %#v", res)
	}
	res = srv.cmdESM(sess, "/esm")
	if res.Error || !strings.Contains(res.Message, "Status: none") {
		t.Fatalf("status after clear = %#v", res)
	}

	if got := srv.handleCommand(sess, "plain message"); got != nil {
		t.Fatalf("plain message should not be a command: %#v", got)
	}
}

func TestSubmitRunInterceptsSlashCommands(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	id := "cmd-esm-submit"
	if _, err := srv.getOrCreateSession(id, srv.cfg.GetWorkDir()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+id+"/runs", strings.NewReader(`{"message":"/esm start background work","model":"default"}`))
	w := httptest.NewRecorder()
	srv.HandleSubmitRun(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("command submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["command"] != true || body["sessionId"] != id {
		t.Fatalf("command response = %#v", body)
	}
	message, _ := body["message"].(string)
	if !strings.Contains(message, "start background work") {
		t.Fatalf("command message = %q", message)
	}

	// The command never creates a durable Run.
	snap, err := srv.GetESM(id)
	if err != nil || snap.Status != "active" {
		t.Fatalf("objective after command = %#v, err = %v", snap, err)
	}
}
