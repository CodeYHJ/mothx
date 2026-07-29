package openaiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/startvibecoding/mothx/internal/session"
)

// HandleRunAPI exposes durable run inspection and explicit cancellation.
func (s *Server) HandleRunAPI(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "run ID required", "invalid_request_error")
		return
	}
	if r.Method == http.MethodGet {
		run, err := s.GetRun(id)
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
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel") {
		id = strings.TrimSuffix(id, "/cancel")
		if err := s.CancelRun(id); err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				writeError(w, http.StatusNotFound, err.Error(), "not_found")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		run, _ := s.GetRun(id)
		if run == nil {
			run = &session.SessionRun{ID: id, Status: "cancelling"}
		}
		writeJSON(w, http.StatusAccepted, run)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	_ = json.NewEncoder(w)
}
