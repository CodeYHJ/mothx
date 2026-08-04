package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

const responsesBackgroundPollInterval = time.Second

func (s *Server) responsesBackgroundEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	provider := s.provider
	manager := s.responsesRuns
	s.mu.RUnlock()
	backgroundProvider, ok := provider.(interface{ ResponsesBackgroundEnabled() bool })
	return manager != nil && ok && backgroundProvider.ResponsesBackgroundEnabled()
}

// executeResponsesBackgroundRun owns one remote Responses background task.
// It deliberately coordinates durable run state instead of invoking Agent.Run:
// a remote response_id can outlive this process, whereas an Agent loop cannot.
func (s *Server) executeResponsesBackgroundRun(sess *APISession, runID string, runtimeRelease func(), model *provider.Model, mode string, msg provider.Message, transcript bool) {
	s.executeResponsesBackgroundRunWithConfig(sess, runID, runtimeRelease, model, mode, msg, transcript, nil, nil)
}

// executeResponsesBackgroundRunWithConfig lets another serve entry point hand
// the already-resolved request configuration to the shared durable runtime.
func (s *Server) executeResponsesBackgroundRunWithConfig(sess *APISession, runID string, runtimeRelease func(), model *provider.Model, mode string, msg provider.Message, transcript bool, agentCfg *agent.Config, initialHistory []provider.Message) {
	defer runtimeRelease()
	defer sess.Unlock()

	terminalStatus := "failed"
	defer func() {
		s.FinalizeRun(sess, runID, terminalStatus, "")
	}()

	s.mu.RLock()
	manager := s.responsesRuns
	s.mu.RUnlock()
	if manager == nil {
		return
	}

	if agentCfg == nil {
		cfg := s.buildAgentConfigForSession(sess, model, mode)
		agentCfg = &cfg
	}
	backgroundAgent := agent.New(*agentCfg, sess.Registry)
	replayState := sess.Manager.GetReplayState()
	if len(replayState.Messages) > 0 {
		backgroundAgent.LoadHistoryState(replayState.Messages, replayState.EntryIDs)
	} else if len(initialHistory) > 0 {
		backgroundAgent.LoadHistoryMessages(initialHistory)
	}
	params, err := backgroundAgent.BuildBackgroundChatParams(runID, msg)
	if err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": err.Error()})
		return
	}

	// Keep the local transcript authoritative before any remote request exists.
	if _, err := sess.Manager.AppendMessage(msg); err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": err.Error()})
		return
	}

	requestCtx, requestCancel := context.WithTimeout(context.Background(), 30*time.Second)
	run, err := manager.Start(requestCtx, sess.ID, runID, params)
	requestCancel()
	if err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": err.Error()})
		return
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if !sess.attachRunAgent(runID, backgroundAgent, func() {
		cancelRun()
		backgroundAgent.Abort()
	}) {
		return
	}
	if s.runManager != nil {
		s.attachResponsesBackgroundCancel(runID, sess.ID, run.LocalRunID, func() {
			cancelRun()
			backgroundAgent.Abort()
		})
		_ = session.UpdateSessionRunStatus(s.settings.GetSessionDir(), runID, "running", "", nil)
	}
	_ = s.recordSessionRunEvent(sess, runID, "remote_started", "running", "responses_background", model.ID, mode, map[string]any{
		"responseRunId": run.LocalRunID,
		"responseId":    run.ResponseID,
		"state":         run.State,
	})
	s.publishSessionRuntime(sess)

	for {
		if isTerminalResponsesRunState(run.State) {
			calls, err := responsesBackgroundFunctionCallsForRun(s.settings.GetSessionDir(), sess.ID, run.LocalTurnID)
			if err != nil {
				_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": err.Error()})
				return
			}
			if len(calls) > 0 {
				outputs, ok := s.executeResponsesBackgroundTools(runCtx, sess, backgroundAgent, runID, run.LocalTurnID, calls)
				if !ok {
					return
				}
				continuationTurnID := runID + ":" + run.LocalRunID
				continueCtx, continueCancel := context.WithTimeout(context.Background(), 30*time.Second)
				next, continueErr := manager.Continue(continueCtx, sess.ID, continuationTurnID, run, outputs, params)
				continueCancel()
				if continueErr != nil {
					_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": continueErr.Error(), "responseRunId": run.LocalRunID})
					return
				}
				run = next
				_ = s.recordSessionRunEvent(sess, runID, "remote_continuation", "running", "responses_background", model.ID, mode, map[string]any{"responseRunId": run.LocalRunID, "responseId": run.ResponseID})
				continue
			}
			terminalStatus = s.finalizeResponsesBackgroundResult(sess, runID, model.ID, mode, run, transcript)
			return
		}
		timer := time.NewTimer(responsesBackgroundPollInterval)
		<-timer.C
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 30*time.Second)
		run, err = manager.Get(pollCtx, sess.ID, run.LocalRunID)
		pollCancel()
		if err != nil {
			_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", model.ID, mode, map[string]any{"error": err.Error()})
			return
		}
		s.publishSessionRuntime(sess)
	}
}

func (s *Server) executeResponsesBackgroundTools(ctx context.Context, sess *APISession, backgroundAgent *agent.Agent, runID, localTurnID string, calls []provider.ToolCallBlock) ([]provider.Message, bool) {
	if backgroundAgent == nil {
		return nil, false
	}
	blocks := make([]provider.ContentBlock, 0, len(calls))
	for i := range calls {
		call := calls[i]
		blocks = append(blocks, provider.ContentBlock{Type: "toolCall", ToolCall: &call})
	}
	if _, err := sess.Manager.AppendMessage(provider.NewAssistantMessage(blocks)); err != nil {
		return nil, false
	}
	// Responses may return multiple function calls in one output. Execute them
	// concurrently so independent calls do not serialize remote latency, but
	// collect and persist outputs by original call order for deterministic
	// continuation input and transcript replay.
	type toolOutcome struct {
		index       int
		output      *provider.Message
		interrupted bool
	}
	outcomeCh := make(chan toolOutcome, len(calls))
	for index, call := range calls {
		go func(index int, call provider.ToolCallBlock) {
			stream := backgroundAgent.ExecuteBackgroundToolCall(ctx, call, localTurnID)
			var output *provider.Message
			interrupted := false
			for ev := range stream {
				s.publishResponsesBackgroundToolEvent(sess, backgroundAgent, runID, ev)
				if ev.ToolExecutionState == "interrupted" {
					interrupted = true
				}
				if ev.Type == agent.EventToolResult {
					result := provider.NewToolResultMessage(ev.ToolCallID, ev.ToolName, ev.ToolResult, ev.ToolError != nil)
					result.ToolKind = call.Kind
					output = &result
				} else if ev.Type == agent.EventToolExecutionEnd && output == nil {
					result := provider.NewToolResultMessage(ev.ToolCallID, ev.ToolName, ev.ToolResult, ev.ToolError != nil)
					result.ToolKind = call.Kind
					output = &result
				}
			}
			outcomeCh <- toolOutcome{index: index, output: output, interrupted: interrupted}
		}(index, call)
	}
	ordered := make([]*provider.Message, len(calls))
	allSucceeded := true
	for range calls {
		outcome := <-outcomeCh
		if outcome.output == nil {
			allSucceeded = false
			continue
		}
		if outcome.interrupted {
			allSucceeded = false
		}
		ordered[outcome.index] = outcome.output
	}
	if !allSucceeded {
		return nil, false
	}
	outputs := make([]provider.Message, 0, len(ordered))
	for _, output := range ordered {
		if output == nil {
			return nil, false
		}
		if _, err := sess.Manager.AppendMessage(*output); err != nil {
			return nil, false
		}
		outputs = append(outputs, *output)
	}
	return outputs, true
}

func (s *Server) publishResponsesBackgroundToolEvent(sess *APISession, backgroundAgent *agent.Agent, runID string, ev agent.Event) {
	if s == nil || sess == nil {
		return
	}
	switch ev.Type {
	case agent.EventToolApprovalRequest:
		s.registerSessionApproval(sess, backgroundAgent, ev)
	case agent.EventToolExecutionStart:
		s.publishToolEvent(sess.ID, ToolStatusEvent{Tool: ev.ToolName, ToolCallID: ev.ToolCallID, Status: "running", Args: ev.ToolArgs})
	case agent.EventToolExecutionEnd:
		status := "completed"
		if ev.ToolError != nil {
			status = "failed"
		}
		s.publishToolEvent(sess.ID, ToolStatusEvent{Tool: ev.ToolName, ToolCallID: ev.ToolCallID, Status: status, Args: ev.ToolArgs, Summary: summarizeToolStatusResult(ev.ToolResult), IsError: ev.ToolError != nil, HasDetail: ev.ToolCallID != ""})
	}
	if s.runManager != nil {
		s.runManager.Publish(runID, ev)
	}
}

func (s *Server) attachResponsesBackgroundCancel(runID, sessionID, localRunID string, localCancel context.CancelFunc) {
	if s == nil || s.runManager == nil || runID == "" || sessionID == "" || localRunID == "" {
		return
	}
	s.mu.RLock()
	manager := s.responsesRuns
	s.mu.RUnlock()
	if manager == nil {
		return
	}
	_ = s.runManager.Attach(runID, sessionID, func() {
		if localCancel != nil {
			localCancel()
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = manager.Cancel(cancelCtx, sessionID, localRunID)
	})
}

// recoverResponsesBackgroundRuns reattaches pending remote Responses tasks
// after server startup. It deliberately requires the local SessionRun and
// ResponseRun linkage to agree before it polls a remote response.
func (s *Server) recoverResponsesBackgroundRuns() error {
	if s == nil || s.runManager == nil || s.responsesRuns == nil || s.settings == nil {
		return nil
	}
	orphans, err := session.ListOrphanedSessionRuns(s.settings.GetSessionDir())
	if err != nil {
		return err
	}
	for _, localRun := range orphans {
		if localRun.Source != "responses_background" {
			continue
		}
		responseRuns, err := session.ListResponseRuns(s.settings.GetSessionDir(), localRun.SessionID, 100)
		if err != nil {
			return err
		}
		var responseRun *session.ResponseRun
		for i := range responseRuns {
			if responseRuns[i].LocalTurnID == localRun.ID || strings.HasPrefix(responseRuns[i].LocalTurnID, localRun.ID+":") {
				candidate := responseRuns[i]
				if responseRun == nil || candidate.UpdatedAt.After(responseRun.UpdatedAt) {
					responseRun = &candidate
				}
			}
		}
		if responseRun == nil {
			_ = s.runManager.Finish(localRun.ID, "failed", "missing recoverable Responses background run")
			continue
		}
		if _, err := s.reattachResponsesBackgroundRun(localRun, responseRun); err != nil {
			_ = s.runManager.Finish(localRun.ID, "failed", err.Error())
		}
	}
	return nil
}

// reattachResponsesBackgroundRun resumes exactly one durable background run.
// It is shared by startup recovery and the authenticated reconnect endpoint so
// both paths acquire the same runtime/session locks and construct the same
// tool/approval-aware monitor.
func (s *Server) reattachResponsesBackgroundRun(localRun session.SessionRun, responseRun *session.ResponseRun) (bool, error) {
	if s == nil || s.runManager == nil || s.responsesRuns == nil || s.settings == nil {
		return false, fmt.Errorf("Responses background runtime is unavailable")
	}
	if localRun.ID == "" || localRun.SessionID == "" || responseRun == nil || responseRun.SessionID != localRun.SessionID {
		return false, fmt.Errorf("invalid Responses background run linkage")
	}
	if isTerminalSessionRunState(localRun.Status) {
		return false, nil
	}
	sess, err := s.getOrCreateSession(localRun.SessionID, localRun.WorkDir)
	if err != nil || sess == nil {
		return false, fmt.Errorf("unable to restore session for Responses background run")
	}
	runtimeRelease, locked := session.TryLockRuntime(s.settings.GetSessionDir(), sess.ID)
	if !locked {
		return false, nil
	}
	if !sess.TryLock() {
		runtimeRelease()
		return false, nil
	}
	if err := sess.Manager.Reload(); err != nil {
		sess.Unlock()
		runtimeRelease()
		return false, fmt.Errorf("reload session before Responses recovery: %w", err)
	}
	model := s.provider.GetModel(localRun.Model)
	if model == nil {
		model = s.model
	}
	if model == nil {
		sess.Unlock()
		runtimeRelease()
		return false, fmt.Errorf("model for Responses background run is unavailable")
	}
	sess.beginRun(localRun.ID)
	go s.monitorRecoveredResponsesBackgroundRun(sess, localRun, responseRun, model, runtimeRelease)
	return true, nil
}

func isTerminalSessionRunState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "cancelled", "canceled", "cancelling", "terminalizing":
		return true
	default:
		return false
	}
}

func (s *Server) monitorRecoveredResponsesBackgroundRun(sess *APISession, localRun session.SessionRun, responseRun *session.ResponseRun, model *provider.Model, runtimeRelease func()) {
	defer runtimeRelease()
	defer sess.Unlock()
	terminalStatus := "failed"
	defer func() { s.FinalizeRun(sess, localRun.ID, terminalStatus, "") }()
	if responseRun == nil || model == nil {
		return
	}
	agentCfg := s.buildAgentConfigForSession(sess, model, localRun.Mode)
	agentCfg.ApprovalDecisionLookup = func(toolCallID, toolName string, args map[string]any) (bool, bool) {
		return s.recoveredApprovalDecision(sess.ID, localRun.ID, toolCallID, toolName, args)
	}
	backgroundAgent := agent.New(agentCfg, sess.Registry)
	replayState := sess.Manager.GetReplayState()
	if len(replayState.Messages) > 0 {
		backgroundAgent.LoadHistoryState(replayState.Messages, replayState.EntryIDs)
	}
	params, err := backgroundAgent.BuildBackgroundContinuationParams(responseRun.LocalTurnID)
	if err != nil {
		_ = s.recordSessionRunEvent(sess, localRun.ID, "failed", "failed", "responses_background", model.ID, localRun.Mode, map[string]any{"error": err.Error()})
		return
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	if !sess.attachRunAgent(localRun.ID, backgroundAgent, func() {
		cancelRun()
		backgroundAgent.Abort()
	}) {
		return
	}
	s.attachResponsesBackgroundCancel(localRun.ID, sess.ID, responseRun.LocalRunID, func() {
		cancelRun()
		backgroundAgent.Abort()
	})
	for {
		pollCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		refreshed, err := s.responsesRuns.Get(pollCtx, sess.ID, responseRun.LocalRunID)
		cancel()
		if err != nil {
			_ = s.recordSessionRunEvent(sess, localRun.ID, "failed", "failed", "responses_background", model.ID, localRun.Mode, map[string]any{"error": err.Error()})
			return
		}
		if isTerminalResponsesRunState(refreshed.State) {
			calls, callErr := responsesBackgroundFunctionCallsForRun(s.settings.GetSessionDir(), sess.ID, refreshed.LocalTurnID)
			if callErr != nil {
				_ = s.recordSessionRunEvent(sess, localRun.ID, "failed", "failed", "responses_background", model.ID, localRun.Mode, map[string]any{"error": callErr.Error()})
				return
			}
			if len(calls) > 0 {
				outputs, ok := s.executeResponsesBackgroundTools(runCtx, sess, backgroundAgent, localRun.ID, refreshed.LocalTurnID, calls)
				if !ok {
					return
				}
				params.ResponseOptions = &provider.ResponseOptions{PreviousResponseID: refreshed.ResponseID}
				continuationID := localRun.ID + ":" + refreshed.LocalRunID
				continueCtx, continueCancel := context.WithTimeout(context.Background(), 30*time.Second)
				next, continueErr := s.responsesRuns.Continue(continueCtx, sess.ID, continuationID, refreshed, outputs, params)
				continueCancel()
				if continueErr != nil {
					_ = s.recordSessionRunEvent(sess, localRun.ID, "failed", "failed", "responses_background", model.ID, localRun.Mode, map[string]any{"error": continueErr.Error(), "responseRunId": refreshed.LocalRunID})
					return
				}
				responseRun = next
				_ = s.recordSessionRunEvent(sess, localRun.ID, "remote_continuation", "running", "responses_background", model.ID, localRun.Mode, map[string]any{"responseRunId": next.LocalRunID, "responseId": next.ResponseID})
				continue
			}
			terminalStatus = s.finalizeResponsesBackgroundResult(sess, localRun.ID, model.ID, localRun.Mode, refreshed, false)
			return
		}
		time.Sleep(responsesBackgroundPollInterval)
	}
}

func (s *Server) finalizeResponsesBackgroundResult(sess *APISession, runID, modelID, mode string, run *session.ResponseRun, transcript bool) string {
	if run == nil {
		return "failed"
	}
	state := strings.ToLower(strings.TrimSpace(run.State))
	if state != "completed" {
		status := "failed"
		if state == "cancelled" || state == "canceled" {
			status = "cancelled"
		}
		_ = s.recordSessionRunEvent(sess, runID, runEventTypeForStatus(status), status, "responses_background", modelID, mode, map[string]any{
			"responseRunId": run.LocalRunID, "responseId": run.ResponseID, "state": run.State,
		})
		return status
	}

	items, err := session.ListResponseItems(s.settings.GetSessionDir(), sess.ID, run.LocalTurnID)
	if err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", modelID, mode, map[string]any{"error": err.Error()})
		return "failed"
	}
	usage, attachments, err := responsesBackgroundDetails(s.settings.GetSessionDir(), sess.ID, run.LocalTurnID)
	if err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", modelID, mode, map[string]any{"error": err.Error()})
		return "failed"
	}
	text, requiresLocalContinuation, err := responsesBackgroundText(items)
	if err != nil {
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", modelID, mode, map[string]any{"error": err.Error()})
		return "failed"
	}
	if requiresLocalContinuation {
		message := "background Responses response reached finalization with an unhandled local tool call"
		_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", modelID, mode, map[string]any{
			"responseRunId": run.LocalRunID, "responseId": run.ResponseID, "error": message,
		})
		return "failed"
	}
	if text != "" || len(attachments) > 0 {
		contents := []provider.ContentBlock(nil)
		if text != "" {
			contents = []provider.ContentBlock{{Type: "text", Text: text}}
		}
		message := provider.NewAssistantMessage(contents)
		message.Attachments = append([]provider.Attachment(nil), attachments...)
		if _, err := sess.Manager.AppendMessage(message); err != nil {
			_ = s.recordSessionRunEvent(sess, runID, "failed", "failed", "responses_background", modelID, mode, map[string]any{"error": err.Error()})
			return "failed"
		}
		if text != "" {
			event := agent.Event{Type: agent.EventTextDelta, TextDelta: text}
			if s.runManager != nil {
				s.runManager.Publish(runID, event)
			}
			if transcript {
				s.publishTranscriptEvent(sess.ID, assistantDeltaTranscriptEvent(text, ""))
			} else if broker := s.getEventBroker(); broker != nil {
				broker.PublishTranscriptEvent(sess.ID, runID, assistantDeltaTranscriptEvent(text, ""))
			}
		}
	}
	if len(attachments) > 0 {
		event := assistantAttachmentsTranscriptEvent(attachments, "")
		if transcript {
			s.publishTranscriptEvent(sess.ID, event)
		} else if broker := s.getEventBroker(); broker != nil {
			broker.PublishTranscriptEvent(sess.ID, runID, event)
		}
	}
	eventData := map[string]any{
		"responseRunId": run.LocalRunID, "responseId": run.ResponseID, "state": run.State,
	}
	if usage != nil {
		eventData["usage"] = usage
	}
	if len(attachments) > 0 {
		eventData["attachments"] = attachments
	}
	_ = s.recordSessionRunEvent(sess, runID, "finished", "completed", "responses_background", modelID, mode, eventData)
	return "completed"
}

func responsesBackgroundDetails(sessionDir, sessionID, localTurnID string) (*provider.Usage, []provider.Attachment, error) {
	turn, err := session.GetResponseTurn(sessionDir, sessionID, localTurnID)
	if err != nil || turn == nil || len(turn.ResponseSummary) == 0 {
		return nil, nil, err
	}
	var summary struct {
		Usage       *provider.Usage       `json:"usage"`
		Attachments []provider.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(turn.ResponseSummary, &summary); err != nil {
		return nil, nil, fmt.Errorf("decode Responses background summary: %w", err)
	}
	return summary.Usage, summary.Attachments, nil
}

func responsesBackgroundText(items []session.ResponseItemArchive) (string, bool, error) {
	var text strings.Builder
	for _, item := range items {
		var raw struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(item.SanitizedJSON, &raw); err != nil {
			return "", false, fmt.Errorf("decode archived Responses item %q: %w", item.ItemID, err)
		}
		switch raw.Type {
		case "function_call", "custom_tool_call":
			return "", true, nil
		case "computer_call", "computer_call_output":
			return "", false, fmt.Errorf("Responses computer use is not supported by this version")
		case "message":
			for _, part := range raw.Content {
				if part.Type == "output_text" || part.Type == "text" {
					text.WriteString(part.Text)
				}
			}
		}
	}
	return text.String(), false, nil
}

func responsesBackgroundFunctionCallsForRun(sessionDir, sessionID, localTurnID string) ([]provider.ToolCallBlock, error) {
	items, err := session.ListResponseItems(sessionDir, sessionID, localTurnID)
	if err != nil {
		return nil, err
	}
	var calls []provider.ToolCallBlock
	for _, item := range items {
		var raw struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Input     string `json:"input"`
		}
		if err := json.Unmarshal(item.SanitizedJSON, &raw); err != nil {
			return nil, fmt.Errorf("decode archived Responses item %q: %w", item.ItemID, err)
		}
		switch raw.Type {
		case "function_call":
			callID := raw.CallID
			if callID == "" {
				callID = raw.ID
			}
			if callID == "" || raw.Name == "" {
				return nil, fmt.Errorf("archived function call %q is missing call_id or name", item.ItemID)
			}
			calls = append(calls, provider.ToolCallBlock{ID: callID, Name: raw.Name, Arguments: json.RawMessage(raw.Arguments)})
		case "custom_tool_call":
			callID := raw.CallID
			if callID == "" {
				callID = raw.ID
			}
			if callID == "" || raw.Name == "" {
				return nil, fmt.Errorf("archived custom tool call %q is missing call_id or name", item.ItemID)
			}
			arguments, err := json.Marshal(map[string]string{"input": raw.Input})
			if err != nil {
				return nil, fmt.Errorf("encode archived custom tool call %q input: %w", item.ItemID, err)
			}
			calls = append(calls, provider.ToolCallBlock{ID: callID, Name: raw.Name, Kind: "custom", Input: raw.Input, Arguments: arguments})
		}
	}
	return calls, nil
}
