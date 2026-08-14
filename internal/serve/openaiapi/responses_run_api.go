package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/session"
)

var errResponseRunWorkDirNotAllowed = errors.New("response run work directory is not allowed")

// HandleResponsesRunAPI exposes durable OpenAI Responses background runs.
// Paths:
//
//	GET  /api/responses/runs/{localRunID}?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/cancel?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/reconnect?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/abandon?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/recover?session_id={sessionID}
func (s *Server) HandleResponsesRunAPI(w http.ResponseWriter, r *http.Request) {
	if s == nil {
		writeError(w, http.StatusServiceUnavailable, "API server not ready", "server_error")
		return
	}
	s.mu.RLock()
	manager := s.responsesRuns
	s.mu.RUnlock()
	if manager == nil {
		writeError(w, http.StatusNotImplemented, "Responses background runs are unavailable for the active provider", "capability_error")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/responses/runs/")
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusBadRequest, "response run ID required", "invalid_request_error")
		return
	}
	localRunID := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if len(parts) > 2 || (action != "" && action != "cancel" && action != "reconnect" && action != "abandon" && action != "recover") {
		writeError(w, http.StatusBadRequest, "invalid response run path", "invalid_request_error")
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session_id is required", "invalid_request_error")
		return
	}
	if err := s.authorizeResponseRunSession(sessionID); err != nil {
		status := http.StatusInternalServerError
		errType := "server_error"
		if errors.Is(err, ErrSessionNotFound) {
			status = http.StatusNotFound
			errType = "not_found"
		} else if errors.Is(err, errResponseRunWorkDirNotAllowed) {
			status = http.StatusForbidden
			errType = "permission_error"
		}
		writeError(w, status, err.Error(), errType)
		return
	}

	switch {
	case r.Method == http.MethodGet && action == "":
		run, err := manager.Get(r.Context(), sessionID, localRunID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
			return
		}
		if run == nil {
			writeError(w, http.StatusNotFound, "response run not found", "not_found")
			return
		}
		writeJSON(w, http.StatusOK, run)
	case r.Method == http.MethodPost && action == "cancel":
		// A durable remote cancel mutates response lineage and must serialize
		// with lifecycle deletion/transfer. A live local monitor owns this lock;
		// callers should use the session stop endpoint first in that window.
		release, locked := session.TryLockRuntime(s.settings.GetSessionDir(), sessionID)
		if !locked {
			writeError(w, http.StatusConflict, "session run is active; stop the local run before cancelling the remote response", "session_run_active")
			return
		}
		defer release()
		if err := manager.Cancel(r.Context(), sessionID, localRunID); err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
			return
		}
		run, err := manager.Get(r.Context(), sessionID, localRunID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
			return
		}
		writeJSON(w, http.StatusAccepted, run)
	case r.Method == http.MethodPost && action == "reconnect":
		run, err := manager.Get(r.Context(), sessionID, localRunID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
			return
		}
		if run == nil {
			writeError(w, http.StatusNotFound, "response run not found", "not_found")
			return
		}
		parent, err := s.responsesBackgroundParentRun(sessionID, run.LocalTurnID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		if parent == nil {
			writeError(w, http.StatusConflict, "response run has no recoverable local background run", "conflict_error")
			return
		}
		reattached, err := s.reattachResponsesBackgroundRun(*parent, run)
		if err != nil {
			if errors.Is(err, ErrResponsesRuntimeBusy) {
				writeError(w, http.StatusConflict, err.Error(), "session_run_active")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		status := http.StatusOK
		if reattached {
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"run": run, "reattached": reattached})
	case r.Method == http.MethodPost && action == "abandon":
		s.abandonResponsesRun(w, r, manager, sessionID, localRunID)
	case r.Method == http.MethodPost && action == "recover":
		s.recoverResponsesRun(w, r, manager, sessionID, localRunID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) recoverResponsesRun(w http.ResponseWriter, r *http.Request, manager interface {
	Get(context.Context, string, string) (*session.ResponseRun, error)
	Cancel(context.Context, string, string) error
}, sessionID, localRunID string) {
	var request struct {
		Confirm     bool     `json:"confirm"`
		ToolCallIDs []string `json:"toolCallIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error")
		return
	}
	if !request.Confirm || len(request.ToolCallIDs) == 0 || len(request.ToolCallIDs) > 32 {
		writeError(w, http.StatusBadRequest, "confirm=true and one to 32 toolCallIds are required", "invalid_request_error")
		return
	}
	run, err := session.GetResponseRun(s.settings.GetSessionDir(), sessionID, localRunID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "response run not found", "not_found")
		return
	}
	if !isTerminalResponsesRunState(run.State) {
		writeError(w, http.StatusConflict, "response run must be terminal before tool recovery", "conflict_error")
		return
	}
	release, locked := session.TryLockRuntime(s.settings.GetSessionDir(), sessionID)
	if !locked {
		writeError(w, http.StatusConflict, "response run is still active", "conflict_error")
		return
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	parentRun, err := s.responsesBackgroundParentRun(sessionID, run.LocalTurnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	workDir, _, err := s.findSessionWorkDir(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	sess, err := s.getOrCreateSession(sessionID, workDir)
	if err != nil || sess == nil {
		if err == nil {
			err = fmt.Errorf("session is unavailable")
		}
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if parentRun != nil && sess.ActiveRunID() == parentRun.ID {
		writeError(w, http.StatusConflict, "response run is still active", "conflict_error")
		return
	}
	requested, err := session.RequestToolExecutionRecovery(s.settings.GetSessionDir(), sessionID, run.LocalTurnID, request.ToolCallIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if requested == 0 {
		writeError(w, http.StatusConflict, "no interrupted tool calls matched the requested recovery", "conflict_error")
		return
	}
	if parentRun == nil {
		writeError(w, http.StatusConflict, "parent session run is unavailable", "conflict_error")
		return
	}
	parentRun.Status = "queued"
	parentRun.Error = ""
	store := agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()}
	if err := store.Update(parentRun.ID, agentruntime.RunStateCreated, ""); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	released = true
	release()
	reattached, err := s.reattachResponsesBackgroundRun(*parentRun, run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run": run, "reattached": reattached, "recoveryRequested": requested,
	})
}

func (s *Server) abandonResponsesRun(w http.ResponseWriter, r *http.Request, manager interface {
	Get(context.Context, string, string) (*session.ResponseRun, error)
	Cancel(context.Context, string, string) error
}, sessionID, localRunID string) {
	run, err := session.GetResponseRun(s.settings.GetSessionDir(), sessionID, localRunID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if run == nil {
		writeError(w, http.StatusNotFound, "response run not found", "not_found")
		return
	}

	// Serializing with the background coordinator prevents abandoning a tool
	// while a live execution can still write a successful result.
	release, locked := session.TryLockRuntime(s.settings.GetSessionDir(), sessionID)
	if !locked {
		writeError(w, http.StatusConflict, "response run is still active; cancel it before abandoning interrupted tools", "conflict_error")
		return
	}
	defer release()

	parentRun, err := s.responsesBackgroundParentRun(sessionID, run.LocalTurnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	workDir, _, err := s.findSessionWorkDir(sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	sess, err := s.getOrCreateSession(sessionID, workDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if parentRun != nil && sess.ActiveRunID() == parentRun.ID {
		writeError(w, http.StatusConflict, "response run is still active; cancel it before abandoning interrupted tools", "conflict_error")
		return
	}

	if !isTerminalResponsesRunState(run.State) {
		if err := manager.Cancel(r.Context(), sessionID, localRunID); err != nil {
			writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
			return
		}
	}
	abandoned, err := session.AbandonInterruptedToolExecutionRecords(s.settings.GetSessionDir(), sessionID, run.LocalTurnID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
		return
	}
	if parentRun != nil {
		s.FinalizeRun(sess, parentRun.ID, "failed", "abandoned after interrupted tool execution")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":                     run,
		"abandonedToolExecutions": abandoned,
		"abandonedAt":             time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) responsesBackgroundParentRun(sessionID, localTurnID string) (*session.SessionRun, error) {
	if sessionID == "" || localTurnID == "" {
		return nil, nil
	}
	runs, err := session.ListSessionRuns(s.settings.GetSessionDir(), sessionID, 500)
	if err != nil {
		return nil, err
	}
	var parent *session.SessionRun
	for i := range runs {
		candidate := runs[i]
		if candidate.Source != "responses_background" || (candidate.ID != localTurnID && !strings.HasPrefix(localTurnID, candidate.ID+":")) {
			continue
		}
		if parent == nil || len(candidate.ID) > len(parent.ID) {
			parent = &candidate
		}
	}
	return parent, nil
}

func (s *Server) authorizeResponseRunSession(sessionID string) error {
	workDir, found, err := s.findSessionWorkDir(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return ErrSessionNotFound
	}
	if workDir == "" || sameWorkDir(workDir, s.cfg.GetWorkDir()) {
		return nil
	}
	if err := s.cfg.ValidateWorkDir(workDir); err != nil {
		return errResponseRunWorkDirNotAllowed
	}
	return nil
}
