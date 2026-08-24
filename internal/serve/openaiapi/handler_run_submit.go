package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/ai/title"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

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

func marshalRunPolicySnapshot(s *Server, sess *APISession, req submitRunRequest, source, mode string) (json.RawMessage, error) {
	activeSkills := make([]string, 0)
	if sess != nil {
		for name, enabled := range sess.ActiveSkills {
			if enabled {
				activeSkills = append(activeSkills, name)
			}
		}
	}
	sort.Strings(activeSkills)
	requestedTools := append([]string(nil), req.Tools...)
	sort.Strings(requestedTools)
	effectiveTools := append([]string(nil), requestedTools...)
	if sess != nil && sess.Registry != nil {
		effectiveTools = effectiveTools[:0]
		for _, definition := range sess.Registry.Definitions() {
			if definition.Name != "" {
				effectiveTools = append(effectiveTools, definition.Name)
			}
		}
		sort.Strings(effectiveTools)
	}
	snapshot := map[string]any{
		"source": source,
		"mode":   mode,
		"workDir": func() string {
			if sess == nil {
				return ""
			}
			return sess.WorkDir
		}(),
		"tools":          requestedTools,
		"effectiveTools": effectiveTools,
		"skills":         activeSkills,
		"capabilities":   capabilitySnapshotFromSession(sess).values(),
		"approvalPolicy": "runtime",
		"questionPolicy": "runtime",
		"runPolicy":      map[string]any{},
	}
	if s != nil && s.cfg != nil {
		snapshot["sandbox"] = map[string]any{
			"enabled": s.cfg.Sandbox.Enabled,
			"level":   s.cfg.Sandbox.Level,
		}
		snapshot["runPolicy"] = map[string]any{
			"requestTimeoutSeconds":   s.cfg.RequestTimeoutSecs,
			"backgroundRunMaxSeconds": s.cfg.BackgroundRunMaxSecs,
		}
	} else {
		snapshot["sandbox"] = map[string]any{"enabled": false, "level": ""}
	}
	return json.Marshal(snapshot)
}

func sameRunPolicySnapshot(previous, current json.RawMessage) bool {
	var previousValue, currentValue any
	if len(previous) == 0 || len(current) == 0 || json.Unmarshal(previous, &previousValue) != nil || json.Unmarshal(current, &currentValue) != nil {
		return false
	}
	// Intents written before the full policy snapshot was introduced only have
	// source/mode. Keep that named migration bridge readable, but require an
	// exact match once the intent contains any of the expanded policy facts.
	previousMap, previousOK := previousValue.(map[string]any)
	currentMap, currentOK := currentValue.(map[string]any)
	if previousOK && currentOK && len(previousMap) <= 2 {
		for key, value := range previousMap {
			if !reflect.DeepEqual(currentMap[key], value) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(previousValue, currentValue)
}

// retryRunContext is injected only by HandleRunAPI after it validates a
// terminal attempt. Re-entering this handler keeps retry admission on the
// same runtime path as a first submission.
type retryRunContext struct {
	Intent         agentruntime.ExecutionIntent
	RetryOf        string
	MinimumAttempt int
}

type retryRunContextKey struct{}

func retryRunFromRequest(r *http.Request) (retryRunContext, bool) {
	if r == nil {
		return retryRunContext{}, false
	}
	value, ok := r.Context().Value(retryRunContextKey{}).(retryRunContext)
	return value, ok
}

// submitErrorInfo projects a preflight failure into the shared safe error
// contract. The raw error is used only for classification/logical matching;
// the explicit message is what crosses the HTTP boundary.
func submitErrorInfo(err error, status int, code, errType string, failureClass agentruntime.FailureClass, phase agentruntime.RunPhase, messageKey, message string, retryMode agentruntime.RetryMode, retryable bool) agentruntime.ErrorInfo {
	info := agentruntime.ClassifyError(err, agentruntime.ErrorClassificationOptions{
		Code: code, Type: errType, Phase: phase, MessageKey: messageKey, Message: message, HTTPStatus: status,
	})
	// This is an adapter-facing preflight error with an explicit safe message.
	// Keep err available above for classification, but do not project its raw
	// parser/storage diagnostic through DisplayErrorMessage.
	info.Detail = ""
	if failureClass != "" {
		info.FailureClass = failureClass
	}
	if retryMode != "" {
		info.RetryMode = retryMode
	}
	info.Retryable = retryable
	return info
}

// writeSubmitError projects preflight failures through the shared safe error
// contract while preserving the legacy ErrorResponse message/type fields.
func writeSubmitError(w http.ResponseWriter, status int, err error, code, errType string, failureClass agentruntime.FailureClass, phase agentruntime.RunPhase, messageKey, message string, retryMode agentruntime.RetryMode, retryable bool) {
	info := submitErrorInfo(err, status, code, errType, failureClass, phase, messageKey, message, retryMode, retryable)
	writeErrorInfo(w, status, info)
}

// HandleSubmitRun creates a background run for a session and returns immediately.
// POST /api/sessions/{sessionID}/runs
func (s *Server) HandleSubmitRun(w http.ResponseWriter, r *http.Request) {
	log.Printf("[diag-submit] ENTER sessionID=%q key=%q remote=%q body=%d\n", extractSessionIDFromPath(r.URL.Path, "/runs"), r.Header.Get("Idempotency-Key"), r.RemoteAddr, r.ContentLength)
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.pool == nil {
		writeSubmitError(w, http.StatusServiceUnavailable, nil, "server_not_ready", "server_error", agentruntime.FailureInternal, agentruntime.PhaseAdmission, "run.error.serverNotReady", "API server not ready", agentruntime.RetryReconcile, true)
		return
	}

	// Extract session ID from path: /api/sessions/{sessionID}/runs
	id := extractSessionIDFromPath(r.URL.Path, "/runs")
	if id == "" {
		writeSubmitError(w, http.StatusBadRequest, nil, "session_id_required", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.sessionIDRequired", "session ID required", agentruntime.RetryNone, false)
		return
	}

	// Parse request body.
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idempotencyKey) > 256 {
		writeSubmitError(w, http.StatusBadRequest, nil, "idempotency_key_too_long", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.idempotencyKeyTooLong", "Idempotency-Key is too long", agentruntime.RetryNone, false)
		return
	}
	var req submitRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSubmitError(w, http.StatusBadRequest, err, "invalid_json", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.invalidJSON", "The request body is not valid JSON.", agentruntime.RetryNone, false)
		return
	}
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 {
		writeSubmitError(w, http.StatusBadRequest, nil, "message_required", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.messageRequired", "message is required", agentruntime.RetryNone, false)
		return
	}
	// A retry is a private re-entry from HandleRetryRun. Read its identity
	// before idempotency lookup so one client key cannot accidentally reconcile
	// a retry that was requested for a different terminal Run.
	retryContext, isRetry := retryRunFromRequest(r)
	idempotencyScope := "submit"
	if isRetry {
		idempotencyScope = retryIdempotencyScope(retryContext.Intent.ID, retryContext.RetryOf)
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
		writeSubmitError(w, http.StatusInternalServerError, err, "session_lookup_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.sessionLookupFailed", "The session work directory could not be loaded.", agentruntime.RetryReconcile, true)
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
				writeSubmitError(w, http.StatusForbidden, err, "workdir_not_allowed", "permission_error", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.workdirNotAllowed", "The selected work directory is not allowed.", agentruntime.RetryNone, false)
				return
			}
		}
	} else if workDir != "" && !sameWorkDir(workDir, s.cfg.GetWorkDir()) {
		if err := s.cfg.ValidateWorkDir(workDir); err != nil {
			writeSubmitError(w, http.StatusForbidden, err, "workdir_not_allowed", "permission_error", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.workdirNotAllowed", "The selected work directory is not allowed.", agentruntime.RetryNone, false)
			return
		}
	}

	// Get or create session
	sess, err := s.getOrCreateSession(id, workDir)
	if err != nil {
		writeSubmitError(w, http.StatusInternalServerError, err, "session_create_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.sessionCreateFailed", "The session could not be created.", agentruntime.RetryReconcile, true)
		return
	}
	if sess == nil {
		writeSubmitError(w, http.StatusServiceUnavailable, nil, "session_pool_unavailable", "server_error", agentruntime.FailureTransient, agentruntime.PhaseAdmission, "run.error.sessionPoolUnavailable", "session pool is at capacity", agentruntime.RetryReconcile, true)
		return
	}
	if isRetry && strings.TrimSpace(retryContext.Intent.WorkDir) != "" && !sameWorkDir(retryContext.Intent.WorkDir, sess.WorkDir) {
		writeErrorInfo(w, http.StatusConflict, agentruntime.ErrorInfo{
			Code: "retry_policy_conflict", Type: "conflict_error", FailureClass: agentruntime.FailurePolicy,
			Phase: agentruntime.PhaseAdmission, MessageKey: "run.error.retryWorkDirConflict",
			Message: "The original run workspace is no longer available for retry.", IntentID: retryContext.Intent.ID,
		})
		return
	}
	if existing, err := findIdempotentRun(s.settings.GetSessionDir(), sess.ID, idempotencyKey, requestFP, idempotencyScope); err != nil {
		if errors.Is(err, ErrIdempotencyKeyConflict) {
			writeSubmitError(w, http.StatusConflict, err, "idempotency_conflict", "idempotency_conflict", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.idempotencyConflict", "The idempotency key conflicts with an existing request.", agentruntime.RetryNone, false)
			return
		}
		if errors.Is(err, ErrIdempotencyRunMissing) {
			writeErrorInfo(w, http.StatusServiceUnavailable, agentruntime.ErrorInfo{
				Code: "submission_unknown", Type: "transport_error", FailureClass: agentruntime.FailureTransport,
				Phase: agentruntime.PhaseTransport, MessageKey: "run.error.submissionUnknown",
				Message:   "The request was accepted but its Run status is temporarily unavailable.",
				RetryMode: agentruntime.RetryReconcile, Retryable: true, IntentID: retryContext.Intent.ID,
			})
			return
		}
		writeSubmitError(w, http.StatusInternalServerError, err, "idempotency_lookup_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.idempotencyLookupFailed", "The request could not be reconciled.", agentruntime.RetryReconcile, true)
		return
	} else if existing != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"sessionId": existing.SessionID, "runId": existing.ID, "status": existing.Status,
			"intentId": existing.IntentID, "attempt": existing.Attempt,
			"idempotent": true,
		})
		return
	}
	if !s.pool.Pin(sess) {
		writeSubmitError(w, http.StatusServiceUnavailable, nil, "session_pool_unavailable", "server_error", agentruntime.FailureTransient, agentruntime.PhaseAdmission, "run.error.sessionPoolUnavailable", "session pool is at capacity", agentruntime.RetryReconcile, true)
		return
	}
	defer s.pool.Unpin(sess)

	runtimeRelease, runtimeOK := session.TryLockRuntime(s.settings.GetSessionDir(), sess.ID)
	if !runtimeOK {
		log.Printf("[diag-submit] 409 runtime-lock held session=%q\n", sess.ID)
		// Attach the blocking run identity when it can be determined so clients
		// can reconcile their view (e.g. surface the stop control) instead of
		// only showing a generic conflict.
		activeRunID := ""
		if s.runManager != nil {
			if active, err := s.runManager.Active(sess.ID); err == nil && active != nil {
				activeRunID = active.ID
			}
		}
		info := submitErrorInfo(nil, http.StatusConflict, "session_run_active", "session_run_active", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.sessionRunActive", "session already has an active run", agentruntime.RetryUser, true)
		info.RunID = activeRunID
		writeErrorInfo(w, http.StatusConflict, info)
		return
	}
	// Note: runtimeRelease is intentionally NOT deferred here; ownership
	// transfers to the background goroutine.

	// The runtime lock is the admission guard for concurrent runs. The session
	// mutex is also used by short-lived capability/SkillHub refreshes, so wait
	// for it after admission instead of turning that harmless overlap into a
	// false session_run_active conflict.
	sess.Lock()
	// Session lock is released in the background goroutine after the agent finishes.
	if err := sess.Manager.Reload(); err != nil {
		sess.Unlock()
		runtimeRelease()
		writeSubmitError(w, http.StatusInternalServerError, err, "session_reload_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.sessionReloadFailed", "The session could not be reloaded.", agentruntime.RetryReconcile, true)
		return
	}
	// A retry is an internal re-entry after HandleRetryRun has validated the
	// prior terminal Run. Its persisted effective model is authoritative over a
	// request-body default, while the current provider must still support it.

	// Resolve model
	s.mu.RLock()
	currentModel := s.model
	currentProvider := s.provider
	providerName := s.providerName
	s.mu.RUnlock()

	if isRetry && strings.TrimSpace(retryContext.Intent.Model) != "" {
		currentModel = currentProvider.GetModel(retryContext.Intent.Model)
		if currentModel == nil {
			sess.Unlock()
			runtimeRelease()
			writeErrorInfo(w, http.StatusConflict, agentruntime.ErrorInfo{
				Code: "retry_policy_conflict", Type: "conflict_error", FailureClass: agentruntime.FailurePolicy,
				Phase: agentruntime.PhaseAdmission, MessageKey: "run.error.retryPolicyConflict",
				Message: "The model used by the original run is no longer available.",
				RunID:   retryContext.RetryOf, IntentID: retryContext.Intent.ID,
			})
			return
		}
	} else if req.Model != "" && req.Model != "default" {
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
		writeSubmitError(w, http.StatusBadRequest, err, "invalid_message", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.invalidMessage", "The submitted message or image is invalid.", agentruntime.RetryNone, false)
		return
	}
	if messageHasImage(msg) && !modelSupportsInput(currentModel, "image") {
		sess.Unlock()
		runtimeRelease()
		writeSubmitError(w, http.StatusBadRequest, nil, "image_input_unsupported", "invalid_request_error", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.imageInputUnsupported", fmt.Sprintf("model %q does not support image input", currentModel.ID), agentruntime.RetryNone, false)
		return
	}

	// Resolve mode once for the runtime, record, approval, and agent config.
	// A linked retry reuses the accepted effective mode, except when current
	// shared policy forces a stricter mode (for example a bound channel's yolo
	// requirement) during ResolveSessionPolicy.
	requestedMode := strings.TrimSpace(req.Mode)
	if isRetry && strings.TrimSpace(retryContext.Intent.Mode) != "" {
		requestedMode = strings.TrimSpace(retryContext.Intent.Mode)
	}
	if err := validateCapabilityMode(requestedMode); err != nil {
		sess.Unlock()
		runtimeRelease()
		writeSubmitError(w, http.StatusBadRequest, err, "invalid_mode", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.invalidMode", "The requested execution mode is invalid.", agentruntime.RetryNone, false)
		return
	}
	resolution, mode, err := s.resolveSessionPolicy(sess, requestedMode)
	if err != nil {
		sess.Unlock()
		runtimeRelease()
		writeSubmitError(w, http.StatusBadRequest, err, "policy_resolution_failed", "invalid_request_error", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.policyResolutionFailed", "The execution policy could not be resolved.", agentruntime.RetryNone, false)
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
	requestSnapshot, err := json.Marshal(req)
	if err != nil {
		sess.Unlock()
		runtimeRelease()
		writeSubmitError(w, http.StatusInternalServerError, err, "run_request_snapshot_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.requestSnapshotFailed", "The run request could not be prepared.", agentruntime.RetryReconcile, true)
		return
	}
	// The policy is completed after session tool/skill capability updates below.
	// Keep a valid placeholder here so the intent object can be assembled before
	// the common admission path; the initial intent is replaced with the full
	// snapshot before persistence.
	policySnapshot := json.RawMessage(`{}`)
	intent := agentruntime.ExecutionIntent{
		ID:                 newExecutionIntentID(),
		SessionID:          sess.ID,
		Source:             runSource,
		Model:              currentModel.ID,
		Mode:               mode,
		WorkDir:            sess.WorkDir,
		RequestFingerprint: requestFP,
		Request:            requestSnapshot,
		Policy:             policySnapshot,
		CreatedAt:          runStartedAt,
	}
	attempt := 1
	retryOf := ""
	if isRetry {
		intent = retryContext.Intent
		retryOf = retryContext.RetryOf
		if intent.ID == "" || intent.SessionID != sess.ID || retryOf == "" {
			sess.Unlock()
			runtimeRelease()
			writeSubmitError(w, http.StatusConflict, nil, "retry_unavailable", "retry_unavailable", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.retryUnavailable", "retry request is no longer valid", agentruntime.RetryNone, false)
			return
		}
		// This code runs after the session/runtime admission locks have been
		// acquired. That makes the maximum attempt lookup and the following
		// BeginRetryDurable admission one serialized operation for this session.
		attempt, err = session.NextSessionRunAttempt(s.settings.GetSessionDir(), sess.ID, intent.ID)
		if err != nil {
			sess.Unlock()
			runtimeRelease()
			writeErrorInfo(w, http.StatusInternalServerError, agentruntime.ErrorInfo{
				Code: "run_persistence_failed", Type: "server_error", FailureClass: agentruntime.FailurePersistence,
				Phase: agentruntime.PhaseAdmission, MessageKey: "run.error.persistence",
				Message: "The retry could not be prepared.", RunID: retryOf, IntentID: intent.ID,
			})
			return
		}
		if attempt < retryContext.MinimumAttempt {
			attempt = retryContext.MinimumAttempt
		}
	}

	failSubmit := func(status int, err error, code, errType string, failureClass agentruntime.FailureClass, phase agentruntime.RunPhase, messageKey, message string, retryMode agentruntime.RetryMode, retryable bool) {
		sess.finishRun(runID)
		sess.Unlock()
		runtimeRelease()
		info := submitErrorInfo(err, status, code, errType, failureClass, phase, messageKey, message, retryMode, retryable)
		writeErrorInfo(w, status, info)
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
			failSubmit(http.StatusInternalServerError, err, "session_capabilities_persist_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhasePersistence, "run.error.capabilitiesPersistFailed", "The session capabilities could not be saved.", agentruntime.RetryReconcile, true)
			return
		}
	}
	toolOpts, err := sessionToolOptionsFromNames(req.Tools)
	if err != nil {
		failSubmit(http.StatusBadRequest, err, "invalid_tool_option", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.invalidToolOption", "The requested tool configuration is invalid.", agentruntime.RetryNone, false)
		return
	}
	// applySessionToolOptions always synchronizes the session tool registry,
	// even with nil options, so tool registration stays owned by the session
	// runtime/capability layer rather than individual runs.
	if err := s.applySessionToolOptions(sess, toolOpts, runID); err != nil {
		failSubmit(http.StatusInternalServerError, err, "session_tools_update_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhasePersistence, "run.error.sessionToolsUpdateFailed", "The session tools could not be updated.", agentruntime.RetryReconcile, true)
		return
	}
	if req.Skills != nil {
		if err := s.setActiveSkillsLocked(sess, req.Skills); err != nil {
			failSubmit(http.StatusBadRequest, err, "invalid_skill_option", "invalid_request_error", agentruntime.FailureValidation, agentruntime.PhaseAdmission, "run.error.invalidSkillOption", "The requested skill configuration is invalid.", agentruntime.RetryNone, false)
			return
		}
	}
	policySnapshot, err = marshalRunPolicySnapshot(s, sess, req, runSource, mode)
	if err != nil {
		failSubmit(http.StatusInternalServerError, err, "run_policy_snapshot_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhaseAdmission, "run.error.policySnapshotFailed", "The run policy could not be prepared.", agentruntime.RetryReconcile, true)
		return
	}
	if isRetry {
		if !sameRunPolicySnapshot(retryContext.Intent.Policy, policySnapshot) {
			failSubmit(http.StatusConflict, nil, "retry_policy_conflict", "conflict_error", agentruntime.FailurePolicy, agentruntime.PhaseAdmission, "run.error.retryPolicyConflict", "The execution policy has changed since the original run.", agentruntime.RetryNone, false)
			return
		}
	} else {
		intent.Policy = policySnapshot
	}

	// Responses background keeps its provider-specific remote driver, while the
	// canonical local Run lifecycle is owned by ExecutionRuntime like other runs.
	execution := sess.ensureExecution()
	execution.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
	execution.SetEventSink(s.runtimeRunEventSink(sess))
	if sess.Runtime != nil {
		sess.Runtime.SetExecution(execution)
	}
	durableRun := agentruntime.DurableRun{
		ID: runID, SessionID: sess.ID, IntentID: intent.ID, RetryOf: retryOf, Attempt: attempt,
		WorkDir: sess.WorkDir, Source: runSource, Model: currentModel.ID, Mode: mode, Status: "queued", StartedAt: runStartedAt,
		ConversationTurnID: "turn-" + intent.ID, ConversationTurn: true,
	}
	startEvent := agentruntime.RunEvent{SessionID: sess.ID, RunID: runID, EventType: "started", Source: runSource, Status: "queued", Model: currentModel.ID, Mode: mode, Timestamp: runStartedAt, Data: rawEventData(map[string]any{
		"source": "webui", "idempotencyKeyHash": idempotencyKeyFingerprint(idempotencyKey), "idempotencyScope": idempotencyScope, "requestFingerprint": requestFP,
		"intentId": intent.ID, "attempt": attempt, "retryOf": retryOf,
	})}
	sess.beginRunBookkeeping(runID)
	var beginErr error
	if isRetry {
		_, _, beginErr = execution.BeginRetryDurable(context.Background(), durableRun, startEvent)
	} else {
		_, beginErr = execution.BeginIntentDurable(context.Background(), intent, durableRun, startEvent)
	}
	if beginErr != nil {
		sess.finishRun(runID)
		sess.Unlock()
		runtimeRelease()
		info := submitErrorInfo(beginErr, http.StatusInternalServerError, "run_persistence_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhasePersistence, "run.error.persistence", "The run could not be started.", agentruntime.RetryReconcile, true)
		info.RunID = runID
		info.IntentID = intent.ID
		writeErrorInfo(w, http.StatusInternalServerError, info)
		return
	}
	// Durable admission atomically starts the conversation turn, which appends
	// a turn_start entry outside the in-memory Manager. Refresh while we still
	// hold the session/runtime locks so the background coordinator appends the
	// user message to that new leaf instead of failing its optimistic check.
	if err := sess.Manager.Reload(); err != nil {
		sess.finishRun(runID)
		sess.Unlock()
		runtimeRelease()
		info := submitErrorInfo(err, http.StatusInternalServerError, "session_reload_failed", "server_error", agentruntime.FailurePersistence, agentruntime.PhasePersistence, "run.error.sessionReloadFailed", "The session could not be reloaded.", agentruntime.RetryReconcile, true)
		info.RunID = runID
		info.IntentID = intent.ID
		writeErrorInfo(w, http.StatusInternalServerError, info)
		return
	}
	sess.markDurableRun(runID)
	if s.runManager != nil {
		_ = s.runManager.Register(session.SessionRun{ID: runID, SessionID: sess.ID, IntentID: intent.ID, RetryOf: retryOf, Attempt: attempt})
	}
	if s.responsesBackgroundEnabled() {
		go s.executeResponsesBackgroundRun(sess, runID, runtimeRelease, currentModel, mode, msg, req.Transcript)
	} else {
		go s.executeBackgroundRun(sess, runID, intent.ID, runtimeRelease, currentModel, providerName, runSource, mode, msg, req.Transcript)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"sessionId": sess.ID,
		"runId":     runID,
		"status":    "queued",
		"intentId":  intent.ID,
		"attempt":   attempt,
	})
}

// executeBackgroundRun runs the agent in a background goroutine and publishes
// all events via the EventBroker. It is responsible for releasing the session
// lock and runtime lock when done.
func (s *Server) executeBackgroundRun(sess *APISession, runID, intentID string, runtimeRelease func(), model *provider.Model, providerName, source, mode string, msg provider.Message, transcript bool) {
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
			terminalData := map[string]any{}
			if terminalStatus != "completed" {
				failure := errors.New(terminalErrMsg)
				if terminalErrMsg == "" {
					failure = errors.New("background run ended before it could start")
				}
				info := agentruntime.ClassifyError(failure, agentruntime.ErrorClassificationOptions{Phase: agentruntime.PhaseModel})
				if execution := sess.executionRuntime(); execution != nil {
					if recorded, recordErr := execution.RecordFailure(failure, agentruntime.ErrorClassificationOptions{Phase: agentruntime.PhaseModel}); recordErr == nil {
						info = recorded
					}
				}
				terminalErrMsg = agentruntime.DisplayErrorMessage(info)
				terminalData["error"] = info
				terminalData["errorInfo"] = info
				terminalData["errorMessage"] = terminalErrMsg
			}
			if execution := sess.executionRuntime(); durableLifecycle && execution != nil {
				_ = execution.FinishDurable(runID, webUIRunState(terminalStatus, terminalErrMsg), terminalErrMsg, agentruntime.RunEvent{
					SessionID: sess.ID, RunID: runID, EventType: runEventTypeForStatus(terminalStatus), Source: source,
					Status: terminalStatus, Model: model.ID, Mode: mode, Timestamp: time.Now(), Data: rawEventData(terminalData),
				})
			} else {
				_ = s.recordSessionRunEvent(sess, runID, runEventTypeForStatus(terminalStatus), terminalStatus, source, model.ID, mode, terminalData)
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
		ConversationTurnID: "turn-" + intentID, IntentID: intentID, RunID: runID,
		ConversationTurn: true, RuntimeOwnsTurnEnd: true,
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

	if s.runRetriesPersistedMessage(runID, sess, replayState.Messages, msg) {
		rawEventCh := a.RunWithLoadedHistory(ctx)
		// Use RunWithLoadedHistory for a linked retry so the user request stored
		// by its first attempt remains a single transcript entry.
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

		s.finishExecutedBackgroundRun(sess, runID, source, model, mode, transcript, executor, result, terminalStatus, terminalErrMsg, durableLifecycle)
		finalized = true
		return
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

	s.finishExecutedBackgroundRun(sess, runID, source, model, mode, transcript, executor, result, terminalStatus, terminalErrMsg, durableLifecycle)
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

func (s *Server) finishExecutedBackgroundRun(sess *APISession, runID, source string, model *provider.Model, mode string, transcript bool, executor *RunExecutor, result *RunResult, terminalStatus, terminalErrMsg string, durableLifecycle bool) {
	// Publish the terminal events and persist the terminal lifecycle run event
	// while the session lock is still held, so the WebUI sees completion promptly
	// and a refresh reconstructs the status from durable run events.
	if executor != nil && result != nil {
		executor.Finalize(sess, result)
	}
	terminalData := map[string]any{}
	if result != nil && result.Usage != nil {
		terminalData = usageEventData(*result.Usage, result.Error)
	}
	if result != nil && result.ErrorInfo != nil {
		terminalData["error"] = result.ErrorInfo
		terminalData["errorInfo"] = result.ErrorInfo
		terminalErrMsg = agentruntime.DisplayErrorMessage(*result.ErrorInfo)
		terminalData["errorMessage"] = terminalErrMsg
	} else if terminalErrMsg != "" {
		info := agentruntime.ClassifyError(fmt.Errorf("%s", terminalErrMsg), agentruntime.ErrorClassificationOptions{Phase: agentruntime.PhaseModel})
		terminalData["error"] = info
		terminalData["errorInfo"] = info
		terminalErrMsg = agentruntime.DisplayErrorMessage(info)
		terminalData["errorMessage"] = terminalErrMsg
	}
	if result != nil {
		terminalData = withContextUsageEventData(terminalData, result.ContextUsage)
	}
	if execution := sess.executionRuntime(); durableLifecycle && execution != nil {
		var usageJSON, contextUsageJSON json.RawMessage
		if result != nil && result.Usage != nil {
			usageJSON, _ = json.Marshal(result.Usage)
		}
		if result != nil && result.ContextUsage != nil {
			contextUsageJSON, _ = json.Marshal(result.ContextUsage)
		}
		_ = execution.RecordUsage(runID, usageJSON, contextUsageJSON)
		if err := execution.FinishDurable(runID, webUIRunState(terminalStatus, terminalErrMsg), terminalErrMsg, agentruntime.RunEvent{
			SessionID: sess.ID, RunID: runID, EventType: runEventTypeForStatus(terminalStatus), Source: source,
			Status: terminalStatus, Model: model.ID, Mode: mode, Timestamp: time.Now(), Data: rawEventData(terminalData),
		}); err != nil {
			// A concurrent cancel/recovery may have terminalized this run first.
			// Only log failures that still leave the run active and actionable.
			if _, active := execution.Active(); active {
				log.Printf("[serve] finish durable run %s: %v", runID, err)
			}
		}
	} else {
		_ = s.recordSessionRunEvent(sess, runID, runEventTypeForStatus(terminalStatus), terminalStatus, source, model.ID, mode, terminalData)
	}

	// Release the session lock before the title generation provider call so the
	// session is not blocked by it for up to 20 seconds.
	s.FinalizeRun(sess, runID, terminalStatus, terminalErrMsg)
	sess.Unlock()
}

func (s *Server) runRetriesPersistedMessage(runID string, sess *APISession, messages []provider.Message, msg provider.Message) bool {
	if s == nil || s.settings == nil || sess == nil || runID == "" {
		return false
	}
	run, err := session.GetSessionRun(s.settings.GetSessionDir(), runID)
	if err != nil || run == nil || run.RetryOf == "" {
		return false
	}
	for i := len(messages) - 1; i >= 0; i-- {
		candidate := messages[i]
		if candidate.Role != "user" || candidate.SystemInjected {
			continue
		}
		if candidate.Content == msg.Content && reflect.DeepEqual(candidate.Contents, msg.Contents) {
			return true
		}
	}
	return false
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
