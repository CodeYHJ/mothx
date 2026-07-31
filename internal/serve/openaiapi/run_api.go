package openaiapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/startvibecoding/mothx/internal/session"
)

// HandleRunAPI exposes durable run inspection and explicit cancellation.
// Paths: GET /api/runs/{runID}, POST /api/runs/{runID}/cancel
func (s *Server) HandleRunAPI(w http.ResponseWriter, r *http.Request) {
	// Parse path segments: /api/runs/<runID>[/cancel]
	path := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	path = strings.TrimSuffix(path, "/")
	segments := strings.Split(path, "/")
	if len(segments) < 1 || segments[0] == "" {
		writeError(w, http.StatusBadRequest, "run ID required", "invalid_request_error")
		return
	}
	runID := segments[0]
	isCancel := len(segments) == 2 && segments[1] == "cancel"

	if r.Method == http.MethodGet {
		if isCancel {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		run, err := s.GetRun(runID)
		if errors.Is(err, ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, err.Error(), "not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}
	if r.Method == http.MethodPost && isCancel {
		if err := s.CancelRun(runID); err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				writeError(w, http.StatusNotFound, err.Error(), "not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		run, _ := s.GetRun(runID)
		if run == nil {
			run = &session.SessionRun{ID: runID, Status: "cancelling"}
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	w.WriteHeader(http.StatusMethodNotAllowed)
}
