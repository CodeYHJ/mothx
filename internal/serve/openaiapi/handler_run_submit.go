package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

// HandleSubmitRun creates a background run for a session and returns immediately.
// POST /api/sessions/{sessionID}/runs
func (s *Server) HandleSubmitRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.pool == nil {
		writeError(w, http.StatusServiceUnavailable, "API server not ready", "server_error")
		return
	}

	// Extract session ID from path: /api/sessions/{sessionID}/runs
	id := extractSessionIDFromPath(r.URL.Path, "/runs")
	if id == "" {
		writeError(w, http.StatusBadRequest, "session ID required", "invalid_request_error")
		return
	}

	// Validate session exists and resolve workDir
	workDir, found, err := s.findSessionWorkDir(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "session not found", "not_found_error")
		return
	}

	// Parse request body
	type submitRunRequest struct {
		Message    string   `json:"message"`
		Model      string   `json:"model"`
		Mode       string   `json:"mode"`
		Tools      []string `json:"tools"`
		Skills     []string `json:"skills"`
		Images     []string `json:"images"`
		Transcript bool     `json:"transcript"`
	}
	var req submitRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeError(w, http.StatusBadRequest, "message is required", "invalid_request_error")
		return
	}

	// Get or create session
	sess, err := s.getOrCreateSession(id, workDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if sess == nil {
		writeError(w, http.StatusServiceUnavailable, "session pool is at capacity", "server_error")
		return
	}
	if !s.pool.Pin(sess) {
		writeError(w, http.StatusServiceUnavailable, "session pool is at capacity", "server_error")
		return
	}
	defer s.pool.Unpin(sess)

	runtimeRelease, runtimeOK := session.TryLockRuntime(s.settings.GetSessionDir(), sess.ID)
	if !runtimeOK {
		writeError(w, http.StatusConflict, "session already has an active run", "session_run_active")
		return
	}
	// Note: runtimeRelease is intentionally NOT deferred here; ownership
	// transfers to the background goroutine.

	if !sess.TryLock() {
		runtimeRelease()
		writeError(w, http.StatusConflict, "session already has an active run", "session_run_active")
		return
	}
	// Session lock is released in the background goroutine after the agent finishes.

	// Resolve model
	s.mu.RLock()
	currentModel := s.model
	currentProvider := s.provider
	providerName := s.providerName
	s.mu.RUnlock()

	if req.Model != "" && req.Model != "default" {
		if m := currentProvider.GetModel(req.Model); m != nil {
			currentModel = m
		}
	}
	currentModel = cloneModel(currentModel)

	// Resolve mode
	mode := s.cfg.DefaultMode
	if sess.Mode != "" {
		mode = sess.Mode
	}
	if req.Mode != "" {
		reqMode := strings.TrimSpace(req.Mode)
		if err := validateCapabilityMode(reqMode); err != nil {
			sess.Unlock()
			runtimeRelease()
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		mode = reqMode
	}

	// Create the run record
	runID := newRunID()
	runStartedAt := time.Now()
	sess.beginRun(runID)

	if s.runManager != nil {
		if err := s.runManager.Create(session.SessionRun{
			ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir,
			Source: "webui", Model: currentModel.ID, Mode: mode,
			Status: "queued", StartedAt: runStartedAt, UpdatedAt: runStartedAt,
		}); err != nil {
			sess.finishRun(runID)
			sess.Unlock()
			runtimeRelease()
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
	}

	_ = s.recordSessionRunEvent(sess, runID, "started", "queued", "webui", currentModel.ID, mode, map[string]any{
		"source": "webui",
	})

	// Start the agent execution in a background goroutine.
	go s.executeBackgroundRun(sess, runID, runtimeRelease, currentModel, providerName, mode, req)

	writeJSON(w, http.StatusAccepted, map[string]any{
		"sessionId": sess.ID,
		"runId":     runID,
		"status":    "queued",
	})
}

// executeBackgroundRun runs the agent in a background goroutine and publishes
// all events via the EventBroker. It is responsible for releasing the session
// lock and runtime lock when done.
func (s *Server) executeBackgroundRun(sess *APISession, runID string, runtimeRelease func(), model *provider.Model, providerName, mode string, req struct {
	Message    string   `json:"message"`
	Model      string   `json:"model"`
	Mode       string   `json:"mode"`
	Tools      []string `json:"tools"`
	Skills     []string `json:"skills"`
	Images     []string `json:"images"`
	Transcript bool     `json:"transcript"`
}) {
	defer runtimeRelease()
	defer sess.Unlock()

	terminalStatus := "failed"
	defer func() {
		s.FinalizeRun(sess, runID, terminalStatus, "")
	}()

	// Build agent config
	agentCfg := s.buildAgentConfigForSession(sess, model, mode)
	a := agent.New(agentCfg, sess.Registry)

	// Build user message
	msg := provider.NewUserMessage(req.Message)

	// Run agent
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.cfg.RequestTimeoutSecs)*time.Second)
	defer cancel()

	if !sess.attachRunAgent(runID, a, cancel) {
		a.Abort()
		return
	}
	if (sess.MultiAgent || sess.DelegateMode || sess.Workflows) && sess.AgentMgr != nil {
		sess.AgentMgr.Register(agent.NewAgentAdapter(a))
		defer func() {
			sess.AgentMgr.Finish(a.ID(), ctx.Err())
		}()
	}

	rawEventCh := a.RunWithUserMessage(ctx, msg)

	// Use RunExecutor to process events and publish via EventBroker
	executor := NewRunExecutor(s, s.getEventBroker(), &session.SessionRun{
		ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir,
		Source: "webui", Model: model.ID, Mode: mode,
		Status: "running", StartedAt: time.Now(),
	})

	result, err := executor.Execute(ctx, sess, a, rawEventCh, model.ID, mode, req.Transcript)
	if err != nil {
		terminalStatus = "failed"
	} else {
		terminalStatus = result.Status
	}

	executor.Finalize(sess, result)

	// Persist usage
	if result != nil && result.Usage != nil {
		_ = s.recordSessionRunEvent(sess, runID, runEventTypeForStatus(terminalStatus), terminalStatus, "webui", model.ID, mode, usageEventData(*result.Usage, result.Error))
	}
}

// extractSessionIDFromPath extracts the session ID from a path like
// /api/sessions/{sessionID}/runs or /api/sessions/{sessionID}/stop.
func extractSessionIDFromPath(path, suffix string) string {
	prefix := "/api/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if !strings.HasSuffix(rest, suffix) {
		return ""
	}
	return strings.TrimSuffix(rest, suffix)
}