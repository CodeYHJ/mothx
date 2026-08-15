package openaiapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/provider"
	serviceruntime "github.com/startvibecoding/mothx/internal/serve/runtime"
	"github.com/startvibecoding/mothx/internal/session"
)

// SubmitExternalResponsesBackground hands an external runtime message to the
// same durable coordinator used by WebUI. The caller does not retain a
// session/runtime lock while the background run executes.
func (s *Server) SubmitExternalResponsesBackground(req serviceruntime.BackgroundRequest) (string, error) {
	if s == nil || s.pool == nil || !s.responsesBackgroundEnabled() {
		return "", fmt.Errorf("Responses background runtime is unavailable")
	}
	if strings.TrimSpace(req.SessionID) == "" || (strings.TrimSpace(req.Text) == "" && req.UserMessage.Role == "") {
		return "", fmt.Errorf("session ID and message are required")
	}
	if len(strings.TrimSpace(req.IdempotencyKey)) > 256 {
		return "", fmt.Errorf("Idempotency-Key is too long")
	}
	requestFP := requestFingerprint(struct {
		Platform string                  `json:"platform"`
		ModelID  string                  `json:"model"`
		Mode     string                  `json:"mode"`
		Text     string                  `json:"text"`
		Role     string                  `json:"role"`
		Content  string                  `json:"content"`
		Contents []provider.ContentBlock `json:"contents"`
	}{req.Platform, req.ModelID, req.Mode, req.Text, req.UserMessage.Role, req.UserMessage.Content, req.UserMessage.Contents})
	workDir := req.WorkDir
	if workDir == "" {
		workDir = s.cfg.GetWorkDir()
	}
	sess, err := s.getOrCreateSession(req.SessionID, workDir)
	if err != nil {
		return "", err
	}
	if sess == nil {
		return "", fmt.Errorf("session pool is at capacity")
	}
	if existing, err := findIdempotentRun(s.settings.GetSessionDir(), sess.ID, req.IdempotencyKey, requestFP); err != nil {
		return "", err
	} else if existing != nil {
		return existing.ID, nil
	}
	if !s.pool.Pin(sess) {
		return "", fmt.Errorf("session pool is at capacity")
	}
	unpin := true
	defer func() {
		if unpin {
			s.pool.Unpin(sess)
		}
	}()

	runtimeRelease, locked := session.TryLockRuntime(s.settings.GetSessionDir(), sess.ID)
	if !locked {
		return "", fmt.Errorf("session already has an active run")
	}
	if !sess.TryLock() {
		runtimeRelease()
		return "", fmt.Errorf("session already has an active run")
	}
	if err := sess.Manager.Reload(); err != nil {
		sess.Unlock()
		runtimeRelease()
		return "", fmt.Errorf("reload session before background run: %w", err)
	}

	s.mu.RLock()
	model := s.model
	currentProvider := s.provider
	s.mu.RUnlock()
	if currentProvider == nil || model == nil {
		sess.Unlock()
		runtimeRelease()
		return "", fmt.Errorf("provider and model are required")
	}
	if req.ModelID != "" && req.ModelID != "default" {
		if selected := currentProvider.GetModel(req.ModelID); selected != nil {
			model = selected
		}
	}
	model = cloneModel(model)
	if req.Temperature != nil {
		model.Temperature = req.Temperature
	}
	if req.TopP != nil {
		model.TopP = req.TopP
	}
	resolution, mode, err := s.resolveSessionPolicy(sess, strings.TrimSpace(req.Mode))
	if err != nil {
		sess.Unlock()
		runtimeRelease()
		return "", err
	}
	runSource := "channel:" + req.Platform
	if resolution.Source != agentruntime.SourceUnknown {
		runSource = string(resolution.Source)
	}
	runID := newRunID()
	now := time.Now()
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
		ID: runID, SessionID: sess.ID, WorkDir: sess.WorkDir,
		Source: runSource, Model: model.ID, Mode: mode,
		Status: "queued", StartedAt: now,
	}, agentruntime.RunEvent{SessionID: sess.ID, RunID: runID, EventType: "started", Source: runSource, Status: "queued", Model: model.ID, Mode: mode, Timestamp: now, Data: rawEventData(map[string]any{
		"source": "channel", "idempotencyKey": strings.TrimSpace(req.IdempotencyKey), "requestFingerprint": requestFP,
	})}); err != nil {
		sess.finishRun(runID)
		sess.Unlock()
		runtimeRelease()
		return "", err
	}
	sess.markDurableRun(runID)
	if s.runManager != nil {
		_ = s.runManager.Register(session.SessionRun{ID: runID, SessionID: sess.ID})
	}

	agentOpts := s.buildAgentOptionsForSession(sess, model, mode)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		agentOpts.ExtraContext += "\n## Client Instructions\n" + strings.TrimSpace(req.SystemPrompt)
	}
	if req.MaxTokens > 0 {
		agentOpts.MaxTokens = req.MaxTokens
		agentOpts.MaxTokensSet = true
	}
	message := provider.NewUserMessage(req.Text)
	if req.UserMessage.Role != "" {
		message = req.UserMessage
	}
	// Keep the pin until the coordinator has released the session/runtime locks.
	unpin = false
	release := func() {
		runtimeRelease()
		s.pool.Unpin(sess)
	}
	var onComplete func(string, []provider.Attachment, error)
	if req.Progress != nil {
		onComplete = func(response string, attachments []provider.Attachment, runErr error) {
			if runErr != nil {
				req.Progress("Responses background run failed: " + runErr.Error())
				return
			}
			if summary := serviceruntime.FormatAttachmentSummary(attachments); summary != "" {
				if strings.TrimSpace(response) != "" {
					response += "\n\n"
				}
				response += summary
			}
			if strings.TrimSpace(response) != "" {
				req.Progress(response)
			}
		}
	}
	go s.executeResponsesBackgroundRunWithConfig(sess, runID, release, model, mode, message, true, &agentOpts, req.InitialHistory, onComplete, req.Progress)
	return runID, nil
}
