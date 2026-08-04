package openaiapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	openaiprovider "github.com/startvibecoding/mothx/internal/provider/openai"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestRegisterRoutesResponsesRunAPI(t *testing.T) {
	srv := newTestServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, srv, RunOptions{})

	req := httptest.NewRequest(http.MethodGet, "/api/responses/runs/run-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNotFound {
		t.Fatalf("Responses run route returned 404; route was not registered")
	}
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("Responses run route status = %d, want 501 for non-Responses provider", w.Code)
	}
}

func TestRegisterRoutesResponsesRunAPIDisabledWithAPI(t *testing.T) {
	srv := newTestServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, srv, RunOptions{DisableAPI: true})

	req := httptest.NewRequest(http.MethodGet, "/api/responses/runs/run-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled Responses run route status = %d, want 404", w.Code)
	}
}

func TestResponsesRunAPIAbandonMarksInterruptedToolsWithoutRetry(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	p := openaiprovider.NewProviderWithModels("test-key", "https://example.invalid/v1", []*provider.Model{srv.model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set Responses config: %v", err)
	}
	srv.provider = p
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	sess, err := srv.getOrCreateSession("abandon-session", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now()
	if err := session.SaveSessionRun(srv.settings.GetSessionDir(), session.SessionRun{
		ID: "abandon-local", SessionID: sess.ID, WorkDir: sess.WorkDir, Source: "responses_background",
		Model: srv.model.ID, Mode: "yolo", Status: "failed", StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save local run: %v", err)
	}
	if err := session.SaveResponseRun(srv.settings.GetSessionDir(), session.ResponseRun{
		SessionID: sess.ID, LocalRunID: "abandon-remote", LocalTurnID: "abandon-local", ResponseID: "resp-abandon",
		Provider: p.Name(), API: p.API(), State: "completed", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save response run: %v", err)
	}
	record := session.ToolExecutionRecord{
		SessionID: sess.ID, LocalTurnID: "abandon-local", ExecutionKey: "abandon-tool", Provider: p.Name(), API: p.API(),
		ProviderCallID: "call-abandon", ToolKind: "function", ToolName: "write", ArgsHash: "args", ExecutionState: "running",
	}
	if _, created, err := session.ClaimToolExecutionRecord(srv.settings.GetSessionDir(), record); err != nil || !created {
		t.Fatalf("claim interrupted tool: created=%v err=%v", created, err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/responses/runs/abandon-remote/abandon?session_id="+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.HandleResponsesRunAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("abandon response status = %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode abandon response: %v", err)
	}
	if body["abandonedToolExecutions"] != float64(1) {
		t.Fatalf("abandon response = %#v", body)
	}
	stored, created, err := session.ClaimToolExecutionRecord(srv.settings.GetSessionDir(), record)
	if err != nil || created || stored.ExecutionState != "abandoned" {
		t.Fatalf("abandoned tool record = %#v, created=%v err=%v", stored, created, err)
	}
	local, err := session.GetSessionRun(srv.settings.GetSessionDir(), "abandon-local")
	if err != nil || local == nil || local.Status != "failed" || local.Error != "abandoned after interrupted tool execution" {
		t.Fatalf("abandoned local run = %#v, err=%v", local, err)
	}
}

func TestResponsesRunAPIReconnectReattachesDurableBackgroundRun(t *testing.T) {
	srv := newTestServer(t)
	defer srv.pool.Stop()
	p := openaiprovider.NewProviderWithModels("test-key", "https://example.invalid/v1", []*provider.Model{srv.model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set Responses config: %v", err)
	}
	srv.provider = p
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	sess, err := srv.getOrCreateSession("reconnect-session", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now()
	if err := session.SaveSessionRun(srv.settings.GetSessionDir(), session.SessionRun{
		ID: "reconnect-local", SessionID: sess.ID, WorkDir: sess.WorkDir, Source: "responses_background",
		Model: srv.model.ID, Mode: "yolo", Status: "running", StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save local run: %v", err)
	}
	if err := session.SaveResponseRun(srv.settings.GetSessionDir(), session.ResponseRun{
		SessionID: sess.ID, LocalRunID: "reconnect-remote", LocalTurnID: "reconnect-local", ResponseID: "resp-reconnect",
		Provider: p.Name(), API: p.API(), State: "completed", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save response run: %v", err)
	}
	if err := session.SaveResponseTurn(srv.settings.GetSessionDir(), session.ResponseTurn{
		SessionID: sess.ID, LocalTurnID: "reconnect-local", ResponseID: "resp-reconnect", Provider: p.Name(), API: p.API(),
		Model: "background", StateMode: "replay", Status: "completed", ResponseSummary: json.RawMessage(`{"status":"completed"}`), CreatedAt: now,
	}); err != nil {
		t.Fatalf("save response archive: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/responses/runs/reconnect-remote/reconnect?session_id="+sess.ID, nil)
	w := httptest.NewRecorder()
	srv.HandleResponsesRunAPI(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("reconnect response status = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Reattached bool `json:"reattached"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Reattached {
		t.Fatalf("reconnect body = %s, err=%v", w.Body.String(), err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		local, err := session.GetSessionRun(srv.settings.GetSessionDir(), "reconnect-local")
		if err == nil && local != nil && local.Status == "completed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	local, err := session.GetSessionRun(srv.settings.GetSessionDir(), "reconnect-local")
	t.Fatalf("reattached local run did not complete: %#v, err=%v", local, err)
}
