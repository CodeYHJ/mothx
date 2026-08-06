package openaiapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/startvibecoding/mothx/internal/provider"
	serviceruntime "github.com/startvibecoding/mothx/internal/serve/runtime"
)

func (s *Server) submitChatCompletionBackground(w http.ResponseWriter, r *http.Request, req ChatCompletionRequest, workDir string, model *provider.Model, userMessage provider.Message, systemMsgs []string, history []RequestMessage) {
	s.mu.RLock()
	sessionID := s.defaultSessionIDs[workDir]
	s.mu.RUnlock()
	if sessionID == "" {
		sess, err := s.getOrCreateSession("", workDir)
		if err != nil || sess == nil {
			if err == nil {
				writeError(w, http.StatusServiceUnavailable, "session pool is at capacity", "server_error")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error(), "server_error")
			return
		}
		sessionID = sess.ID
	}
	initialHistory := convertHistoryMessages(history)
	text := strings.TrimSpace(userMessage.Content)
	runID, err := s.SubmitExternalResponsesBackground(serviceruntime.BackgroundRequest{
		Context:        r.Context(),
		SessionID:      sessionID,
		WorkDir:        workDir,
		Platform:       "chat-completions",
		ModelID:        req.Model,
		Mode:           "",
		Text:           text,
		UserMessage:    userMessage,
		InitialHistory: initialHistory,
		SystemPrompt:   strings.Join(systemMsgs, "\n"),
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxTokens,
		IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	})
	if err != nil {
		status := http.StatusInternalServerError
		errType := "server_error"
		if errors.Is(err, ErrIdempotencyKeyConflict) {
			status = http.StatusConflict
			errType = "idempotency_conflict"
		} else if strings.Contains(err.Error(), "active run") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error(), errType)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id": runID, "object": "chat.completion", "status": "queued",
		"sessionId": sessionID, "runId": runID,
	})
}
