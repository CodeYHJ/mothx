package openaiapi

import (
	"errors"
	"net/http"
	"strings"
)

var errResponseRunWorkDirNotAllowed = errors.New("response run work directory is not allowed")

// HandleResponsesRunAPI exposes durable OpenAI Responses background runs.
// Paths:
//
//	GET  /api/responses/runs/{localRunID}?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/cancel?session_id={sessionID}
//	POST /api/responses/runs/{localRunID}/reconnect?session_id={sessionID}
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
	if len(parts) > 2 || (action != "" && action != "cancel" && action != "reconnect") {
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
		writeJSON(w, http.StatusOK, run)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
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
