package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/ai/title"
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

	// Parse request body
	type submitRunRequest struct {
		Message    string   `json:"message"`
		Model      string   `json:"model"`
		Mode       string   `json:"mode"`
		Tools      []string `json:"tools"`
		Skills     []string `json:"skills"`
		Images     []string `json:"images"`
		Transcript bool     `json:"transcript"`
		WorkDir    string   `json:"workDir"`
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 256 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is too long", "invalid_request_error")
		return
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
	requestFP := requestFingerprint(struct {
		Message string   `json:"message"`
		Model   string   `json:"model"`
		Mode    string   `json:"mode"`
		Tools   []string `json:"tools"`
		Skills  []string `json:"skills"`
		Images  []string `json:"images"`
		Trace   bool     `json:"transcript"`
		WorkDir string   `json:"workDir"`
	}{req.Message, req.Model, req.Mode, req.Tools, req.Skills, req.Images, req.Transcript, req.WorkDir})

	// Resolve workDir. Sessions created client-side (e.g. by the Web UI)
	// are not persisted yet; fall back to the default workDir for those,
	// mirroring handleChatCompletions.
	workDir, found, err := s.findSessionWorkDir(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if !found {
		// A brand-new client-created session; honor the workDir chosen in the
		// Web UI when provided, otherwise fall back to the default workDir.
		workDir = strings.TrimSpace(req.WorkDir)
		if workDir == "" {
			workDir = s.cfg.GetWorkDir()
		} else if !sameWorkDir(workDir, s.cfg.GetWorkDir()) {
			if err := s.cfg.ValidateWorkDir(workDir); err != nil {
				writeError(w, http.StatusForbidden, err.Error(), "permission_error")
				return
			}
		}
	} else if workDir != "" && !sameWorkDir(workDir, s.cfg.GetWorkDir()) {
		if err := s.cfg.ValidateWorkDir(workDir); err != nil {
			writeError(w, http.StatusForbidden, err.Error(), "permission_error")
			return
		}
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
	if existing, err := findIdempotentRun(s.settings.GetSessionDir(), sess.ID, idempotencyKey, requestFP); err != nil {
		if errors.Is(err, ErrIdempotencyKeyConflict) {
			writeError(w, http.StatusConflict, err.Error(), "idempotency_conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	} else if existing != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"sessionId": existing.SessionID, "runId": existing.ID, "status": existing.Status,
			"idempotent": true,
		})
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
	if err := sess.Manager.Reload(); err != nil {
		sess.Unlock()
		runtimeRelease()
		writeError(w, http.StatusInternalServerError, "reload session before run: "+err.Error(), "server_error")
		return
	}
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

	// Resolve mode once for the runtime, record, approval, and agent config.
	requestedMode := strings.TrimSpace(req.Mode)
	if err := validateCapabilityMode(requestedMode); err != nil {
		sess.Unlock()
		runtimeRelease()
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	resolution, mode, err := s.resolveSessionPolicy(sess, requestedMode)
	if err != nil {
		sess.Unlock()
		runtimeRelease()
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	runSource := string(resolution.Source)
	if runSource == "" {
		runSource = string(agentruntime.SourceWebUI)
	}
	modeProvided := requestedMode != ""

	// Run admission is started after capability validation so durable local
	// lifecycle creation cannot be followed by preflight failures.
	runID := newRunID()
	runStartedAt := time.Now()

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
		if err := s.persistSessionCapabilitiesWithEvents(sess, before, "run_mode", "webui", runID, map[string]any{
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

	// Responses background keeps its provider-specific remote driver, while the
	// canonical local Run lifecycle is owned by ExecutionRuntime like other runs.
	if s.responsesBackgroundEnabled() {
		if sess.Execution == nil {
			sess.Execution = &agentruntime.ExecutionRuntime{}
		}
		sess.Execution.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
		sess.Execution.SetEventSink(s.runtimeRunEventSink(sess))
		if sess.Runtime != nil {
			sess.Runtime.SetExecution(sess.Execution)
		}
		sess.beginRunBookkeeping(runID)
		if _, err := sess.Execution.BeginDurable(context.Background(), agentruntime.DurableRun{
			ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir, Source: runSource,
			Model: currentModel.ID, Mode: mode, Status: "queued", StartedAt: runStartedAt,
		}, agentruntime.RunEvent{SessionID: sess.ID, RunID: runID, EventType: "started", Source: runSource, Status: "queued", Model: currentModel.ID, Mode: mode, Timestamp: runStartedAt, Data: rawEventData(map[string]any{
			"source": "webui", "idempotencyKey": idempotencyKey, "requestFingerprint": requestFP,
		})}); err != nil {
			sess.finishRun(runID)
			sess.Unlock()
			runtimeRelease()
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		sess.markDurableRun(runID)
		if s.runManager != nil {
			_ = s.runManager.Register(session.SessionRun{ID: runID, SessionID: sess.ID})
		}
		go s.executeResponsesBackgroundRun(sess, runID, runtimeRelease, currentModel, mode, msg, req.Transcript)
	} else {
		if sess.Execution == nil {
			sess.Execution = &agentruntime.ExecutionRuntime{}
		}
		sess.Execution.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
		sess.Execution.SetEventSink(s.runtimeRunEventSink(sess))
		if sess.Runtime != nil {
			sess.Runtime.SetExecution(sess.Execution)
		}
		sess.beginRunBookkeeping(runID)
		if _, err := sess.Execution.BeginDurable(context.Background(), agentruntime.DurableRun{
			ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir, Source: runSource,
			Model: currentModel.ID, Mode: mode, Status: "queued", StartedAt: runStartedAt,
		}, agentruntime.RunEvent{SessionID: sess.ID, RunID: runID, EventType: "started", Source: runSource, Status: "queued", Model: currentModel.ID, Mode: mode, Timestamp: runStartedAt, Data: rawEventData(map[string]any{
			"source": "webui", "idempotencyKey": idempotencyKey, "requestFingerprint": requestFP,
		})}); err != nil {
			sess.finishRun(runID)
			sess.Unlock()
			runtimeRelease()
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		sess.markDurableRun(runID)
		if s.runManager != nil {
			_ = s.runManager.Register(session.SessionRun{ID: runID, SessionID: sess.ID})
		}
		go s.executeBackgroundRun(sess, runID, runtimeRelease, currentModel, providerName, runSource, mode, msg, req.Transcript)
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
func (s *Server) executeBackgroundRun(sess *APISession, runID string, runtimeRelease func(), model *provider.Model, providerName, source, mode string, msg provider.Message, transcript bool) {
	terminalStatus := "failed"
	terminalErrMsg := ""
	firstTurn := len(sess.Manager.GetMessages()) == 0

	// Run completion always finalizes the run, releases the session lock and
	// releases the runtime pin, even on panic paths. The explicit success path
	// below performs these steps itself and sets finalized to skip this fallback.
	durableLifecycle := sess.isDurableRun(runID)
	finalized := false
	defer func() {
		if !finalized {
			if durableLifecycle && sess.Execution != nil {
				_ = sess.Execution.FinishDurable(runID, webUIRunState(terminalStatus, terminalErrMsg), terminalErrMsg, agentruntime.RunEvent{
					SessionID: sess.ID, RunID: runID, EventType: runEventTypeForStatus(terminalStatus), Source: source,
					Status: terminalStatus, Model: model.ID, Mode: mode, Timestamp: time.Now(),
				})
			}
			s.FinalizeRun(sess, runID, terminalStatus, terminalErrMsg)
			sess.Unlock()
		}
		runtimeRelease()
	}()

	// Build the local Agent through the shared SessionRuntime. Responses
	// background runs use a separate remote driver and do not enter here.
	a, err := sess.Runtime.BuildAgent(agentruntime.AgentBuildOptions{
		Provider: s.provider, ProviderName: providerName, Model: model,
		Settings: s.settingsForSession(sess), Allow: s.getAllow(), Mode: mode,
		ThinkingLevel: provider.ThinkingLevel(s.cfg.DefaultThinkingLevel),
		MultiAgent:    sess.MultiAgent, DelegateMode: sess.DelegateMode, Workflows: sess.Workflows,
	})
	if err != nil {
		terminalErrMsg = err.Error()
		return
	}

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
		Source: source, Model: model.ID, Mode: mode,
		Status: "running", StartedAt: time.Now(),
	})

	result, err := executor.Execute(ctx, sess, a, rawEventCh, model.ID, mode, transcript)
	if err != nil {
		terminalStatus = "failed"
		terminalErrMsg = err.Error()
	} else if result != nil {
		terminalStatus = result.Status
		terminalErrMsg = result.Error
	}

	// Publish the terminal events and persist the terminal lifecycle run event
	// while the session lock is still held, so the WebUI sees completion promptly
	// and a refresh reconstructs the status from durable run events (recording
	// only usage-bearing completions would leave a cancelled run stuck on its
	// initial "started" event).
	executor.Finalize(sess, result)
	terminalData := map[string]any{}
	if result != nil && result.Usage != nil {
		terminalData = usageEventData(*result.Usage, result.Error)
	} else if terminalErrMsg != "" {
		terminalData["error"] = terminalErrMsg
	}
	if result != nil {
		terminalData = withContextUsageEventData(terminalData, result.ContextUsage)
	}
	if durableLifecycle && sess.Execution != nil {
		if err := sess.Execution.FinishDurable(runID, webUIRunState(terminalStatus, terminalErrMsg), terminalErrMsg, agentruntime.RunEvent{
			SessionID: sess.ID, RunID: runID, EventType: runEventTypeForStatus(terminalStatus), Source: source,
			Status: terminalStatus, Model: model.ID, Mode: mode, Timestamp: time.Now(), Data: rawEventData(terminalData),
		}); err != nil {
			log.Printf("[serve] finish durable run %s: %v", runID, err)
		}
	} else {
		_ = s.recordSessionRunEvent(sess, runID, runEventTypeForStatus(terminalStatus), terminalStatus, source, model.ID, mode, terminalData)
	}

	// Release the session lock before the title generation provider call so the
	// session is not blocked by it for up to 20 seconds.
	s.FinalizeRun(sess, runID, terminalStatus, terminalErrMsg)
	sess.Unlock()
	finalized = true

	// Session title generation is best-effort and must not delay the run
	// completion events published above.
	if firstTurn && terminalStatus == "completed" {
		if s.pool == nil {
			// A server without a session pool is only used by small embedded
			// adapters/tests; preserve the best-effort behavior there.
			s.generateSessionTitle(sess, model)
		} else {
			// If shutdown won the race with the execution goroutine, skip this
			// optional write rather than starting untracked work after the pool has
			// stopped accepting background tasks.
			_ = s.pool.Go(func() { s.generateSessionTitle(sess, model) })
		}
	}
}

// buildSubmitRunMessage builds the user message for a run submission,
// combining the text prompt with optional base64 data-URL images.
func (s *Server) generateSessionTitle(sess *APISession, model *provider.Model) {
	if s == nil {
		log.Printf("[serve] session title generation skipped: server is nil")
		return
	}
	if sess == nil {
		log.Printf("[serve] session title generation skipped: session is nil")
		return
	}
	if sess.Manager == nil {
		log.Printf("[serve] session title generation skipped: session=%s manager is nil", sess.ID)
		return
	}
	if model == nil {
		log.Printf("[serve] session title generation skipped: session=%s model is nil", sess.ID)
		return
	}
	if title, _, err := session.LatestSessionTitle(s.settings.GetSessionDir(), sess.ID); err != nil || strings.TrimSpace(title) != "" {
		return
	}
	// Capture the provider under the server lock: s.provider is swapped while
	// holding s.mu (e.g. when the default model changes) and this function runs
	// in a background goroutine after the session lock has been released.
	s.mu.RLock()
	provider := s.provider
	s.mu.RUnlock()
	if provider == nil {
		log.Printf("[serve] session title generation skipped: session=%s provider is nil", sess.ID)
		return
	}

	log.Printf("[serve] generating session title: session=%s provider=%s api=%s model=%s", sess.ID, provider.Name(), provider.API(), model.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name, err := (title.Generator{Provider: provider, Model: model}).Generate(ctx, sess.Manager.GetMessages())
	if err != nil {
		log.Printf("[serve] session title generation failed: session=%s provider=%s model=%s: %v", sess.ID, provider.Name(), model.ID, err)
		return
	}
	if name == "" {
		log.Printf("[serve] session title generation returned empty title: session=%s provider=%s model=%s", sess.ID, provider.Name(), model.ID)
		return
	}
	if title, _, err := session.LatestSessionTitle(s.settings.GetSessionDir(), sess.ID); err != nil || strings.TrimSpace(title) != "" {
		return
	}
	if _, err := sess.Manager.AppendSessionTitle(name, "auto"); err != nil {
		log.Printf("[serve] persist session title failed: session=%s title=%q: %v", sess.ID, name, err)
		return
	}
	log.Printf("[serve] session title generated: session=%s title=%q", sess.ID, name)
	if broker := s.getEventBroker(); broker != nil {
		broker.PublishRawJSON(sess.ID, "", "title_updated", map[string]any{"title": name})
	}
}

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

// sessionToolOptionsFromNames maps the WebUI submit body `tools` array to
// local-tool capability toggles. Hosted/provider tools are intentionally
// excluded because their configuration is owned by provider/settings.
// A nil slice means the client did not send tool intent and leaves session
// state untouched; a non-nil slice enables listed capabilities and disables
// the rest. `webSearch` is accepted for backward compatibility but ignored;
// unknown names are rejected.
func sessionToolOptionsFromNames(names []string) (*SessionToolOptions, error) {
	if names == nil {
		return nil, nil
	}
	enabled := make(map[string]bool, len(names))
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "webSearch":
			// Hosted tools must not be changed by the WebUI local tool list.
		case "browser", "a2aMaster", "delegate", "multiAgent", "workflows":
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
		// WebSearch is intentionally nil; hosted configuration is preserved.
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
