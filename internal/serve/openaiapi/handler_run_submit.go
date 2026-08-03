package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Resolve workDir. Sessions created client-side (e.g. by the Web UI)
	// are not persisted yet; fall back to the default workDir for those,
	// mirroring handleChatCompletions.
	workDir, found, err := s.findSessionWorkDir(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if !found {
		workDir = s.cfg.GetWorkDir()
	} else if workDir != "" && !sameWorkDir(workDir, s.cfg.GetWorkDir()) {
		if err := s.cfg.ValidateWorkDir(workDir); err != nil {
			writeError(w, http.StatusForbidden, err.Error(), "permission_error")
			return
		}
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
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 {
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

	// Build the user message (text + optional images) up front so validation
	// failures are reported synchronously instead of in the background run.
	msg, err := buildSubmitRunMessage(req.Message, req.Images)
	if err != nil {
		sess.Unlock()
		runtimeRelease()
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if messageHasImage(msg) && !modelSupportsInput(currentModel, "image") {
		sess.Unlock()
		runtimeRelease()
		writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q does not support image input", currentModel.ID), "invalid_request_error")
		return
	}

	// Resolve mode
	mode := s.cfg.DefaultMode
	if sess.Mode != "" {
		mode = sess.Mode
	}
	modeProvided := false
	if req.Mode != "" {
		reqMode := strings.TrimSpace(req.Mode)
		if err := validateCapabilityMode(reqMode); err != nil {
			sess.Unlock()
			runtimeRelease()
			writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
		mode = reqMode
		modeProvided = true
	}

	// Create the run record
	runID := newRunID()
	runStartedAt := time.Now()
	sess.beginRun(runID)

	failSubmit := func(status int, message, errType string) {
		sess.finishRun(runID)
		sess.Unlock()
		runtimeRelease()
		writeError(w, status, message, errType)
	}

	// Apply WebUI runtime intents (mode, tool toggles, skills) before the
	// agent is constructed, mirroring handleChatCompletions. An explicit mode
	// in the submit body is persisted so it sticks for subsequent runs.
	if modeProvided {
		before := capabilitySnapshotFromSession(sess)
		sess.Mode = mode
		if err := s.persistSessionCapabilitiesWithEvents(sess, before, "x_mode", "webui", runID, map[string]any{
			"source": "run_submit",
		}); err != nil {
			failSubmit(http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
	}
	toolOpts, err := sessionToolOptionsFromNames(req.Tools)
	if err != nil {
		failSubmit(http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	// applySessionToolOptions always synchronizes the session tool registry,
	// even with nil options, so tool registration stays owned by the session
	// runtime/capability layer rather than individual runs.
	if err := s.applySessionToolOptions(sess, toolOpts, runID); err != nil {
		failSubmit(http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if req.Skills != nil {
		if err := s.setActiveSkillsLocked(sess, req.Skills); err != nil {
			failSubmit(http.StatusBadRequest, err.Error(), "invalid_request_error")
			return
		}
	}

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

	// Responses background mode is a remote durable task. It shares the local
	// SessionRun envelope with WebUI runs but must not enter Provider.Chat or a
	// sub-agent loop.
	if s.responsesBackgroundEnabled() {
		go s.executeResponsesBackgroundRun(sess, runID, runtimeRelease, currentModel, mode, msg, req.Transcript)
	} else {
		go s.executeBackgroundRun(sess, runID, runtimeRelease, currentModel, providerName, mode, msg, req.Transcript)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"sessionId": sess.ID,
		"runId":     runID,
		"status":    "queued",
	})
}

// executeBackgroundRun runs the agent in a background goroutine and publishes
// all events via the EventBroker. It is responsible for releasing the session
// lock and runtime lock when done.
func (s *Server) executeBackgroundRun(sess *APISession, runID string, runtimeRelease func(), model *provider.Model, providerName, mode string, msg provider.Message, transcript bool) {
	defer runtimeRelease()
	defer sess.Unlock()

	terminalStatus := "failed"
	defer func() {
		s.FinalizeRun(sess, runID, terminalStatus, "")
	}()

	// Build agent config
	agentCfg := s.buildAgentConfigForSession(sess, model, mode)
	a := agent.New(agentCfg, sess.Registry)

	// Replay persisted session history into the fresh agent so background
	// runs keep the conversation context (mirrors handleChatCompletions).
	replayState := sess.Manager.GetReplayState()
	if len(replayState.Messages) > 0 {
		a.LoadHistoryState(replayState.Messages, replayState.EntryIDs)
	}

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

	result, err := executor.Execute(ctx, sess, a, rawEventCh, model.ID, mode, transcript)
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

// buildSubmitRunMessage builds the user message for a run submission,
// combining the text prompt with optional base64 data-URL images.
func buildSubmitRunMessage(text string, images []string) (provider.Message, error) {
	msg := provider.NewUserMessage(text)
	if len(images) == 0 {
		return msg, nil
	}
	contents := make([]provider.ContentBlock, 0, len(images)+1)
	if strings.TrimSpace(text) != "" {
		contents = append(contents, provider.ContentBlock{Type: "text", Text: text})
	}
	for _, dataURL := range images {
		image, err := imageFromDataURL(dataURL, "")
		if err != nil {
			return provider.Message{}, err
		}
		contents = append(contents, provider.ContentBlock{Type: "image", Image: image})
	}
	msg.Contents = contents
	return msg, nil
}

// sessionToolOptionsFromNames maps the WebUI submit body `tools` array — the
// authoritative set of enabled capability toggles — to SessionToolOptions.
// A nil slice means the client did not send tool intent and leaves session
// state untouched; a non-nil slice enables listed capabilities and disables
// the rest. Unknown names are rejected.
func sessionToolOptionsFromNames(names []string) (*SessionToolOptions, error) {
	if names == nil {
		return nil, nil
	}
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "webSearch", "browser", "a2aMaster", "delegate", "multiAgent", "workflows":
			enabled[strings.TrimSpace(name)] = true
		case "":
		default:
			return nil, fmt.Errorf("unknown tool option %q", name)
		}
	}
	boolPtr := func(key string) *bool {
		v := enabled[key]
		return &v
	}
	return &SessionToolOptions{
		WebSearch:  boolPtr("webSearch"),
		Browser:    boolPtr("browser"),
		A2AMaster:  boolPtr("a2aMaster"),
		Delegate:   boolPtr("delegate"),
		MultiAgent: boolPtr("multiAgent"),
		Workflows:  boolPtr("workflows"),
	}, nil
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
