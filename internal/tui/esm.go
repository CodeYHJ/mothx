package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	agentpkg "github.com/startvibecoding/mothx/agent"
	internalagent "github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/esm"
	"github.com/startvibecoding/mothx/internal/provider"
)

const (
	esmGetToolName    = "get_esm"
	esmUpdateToolName = "update_esm"

	esmRoleTimeout             = 30 * time.Minute
	esmRecoveryObserverTimeout = 5 * time.Minute
)

func (a *App) ensureESMStore() *esm.Store {
	sessionFile := ""
	if a.session != nil {
		sessionFile = a.session.GetFile()
	}
	dir := resolveESMStoreDir(sessionFile, a.getSessionDir())
	if a.esmStore != nil && a.esmStoreDir == dir {
		return a.esmStore
	}
	if a.esmToolsRegistered && a.registry != nil {
		a.registry.Remove(esmGetToolName)
		a.registry.Remove(esmUpdateToolName)
		a.esmToolsRegistered = false
		a.resetAgent(fmt.Errorf("esm store changed"))
	}
	a.esmStore = esm.NewStore(dir)
	a.esmStoreDir = dir
	return a.esmStore
}

func resolveESMStoreDir(sessionFile, fallback string) string {
	if sessionFile == "" {
		return fallback
	}
	dir := filepath.Dir(filepath.Clean(sessionFile))
	if dir == "." || dir == "" {
		return fallback
	}
	if strings.Contains(filepath.Base(dir), "--") {
		dir = filepath.Dir(dir)
	}
	return dir
}

func (a *App) currentSessionID() string {
	if a.session == nil || a.session.GetHeader() == nil {
		return ""
	}
	return a.session.GetHeader().ID
}

func (a *App) currentESMRunID() string {
	a.esmMu.Lock()
	defer a.esmMu.Unlock()
	return a.esmRunID
}

func (a *App) loadESMObjective(ctx context.Context) (*esm.Objective, error) {
	store := a.ensureESMStore()
	sessionID := a.currentSessionID()
	if store == nil || sessionID == "" {
		return nil, esm.ErrNotFound
	}
	return store.Get(ctx, sessionID)
}

func (a *App) syncESMTools() error {
	if a.registry == nil {
		return nil
	}
	store := a.ensureESMStore()
	sessionID := a.currentSessionID()
	var obj *esm.Objective
	var err error
	if sessionID != "" {
		obj, err = store.Get(context.Background(), sessionID)
		if errors.Is(err, esm.ErrNotFound) {
			err = nil
		}
		if err != nil {
			return err
		}
	}

	shouldRegister := obj != nil && esm.IsRunnableStatus(obj.Status)
	if shouldRegister && !a.esmToolsRegistered {
		a.registry.Register(esm.NewGetTool(store, a.currentSessionID))
		a.registry.Register(esm.NewUpdateTool(store, a.currentSessionID, a.currentESMRunID))
		a.esmToolsRegistered = true
		a.resetAgent(fmt.Errorf("esm tools changed"))
	} else if !shouldRegister && a.esmToolsRegistered {
		a.registry.Remove(esmGetToolName)
		a.registry.Remove(esmUpdateToolName)
		a.esmToolsRegistered = false
		a.resetAgent(fmt.Errorf("esm tools changed"))
	}
	a.setESMFooter(obj)
	return nil
}

func (a *App) setESMFooter(obj *esm.Objective) {
	if obj == nil {
		a.esmFooter = ""
		return
	}
	parts := []string{"ESM", string(obj.Status), string(effectiveESMPhase(obj))}
	tokenPart := formatTokens(int(obj.TokensUsed))
	if obj.TokenBudget != nil {
		tokenPart += "/" + formatTokens(int(*obj.TokenBudget))
	}
	parts = append(parts, tokenPart)
	if obj.TimeUsedMS > 0 {
		parts = append(parts, formatDuration(time.Duration(obj.TimeUsedMS)*time.Millisecond))
	}
	if obj.RejectionCount > 0 {
		parts = append(parts, fmt.Sprintf("reject %d/%d", obj.RejectionCount, esm.CompletionRejectionLimit))
	}
	if obj.RecoveryCount > 0 {
		parts = append(parts, fmt.Sprintf("recover %d/%d", obj.RecoveryCount, esm.RecoveryLimit))
	}
	a.esmFooter = strings.Join(parts, " ")
}

func (a *App) handleESMCommand(cmd string) tea.Cmd {
	if err := a.ensureSession(); err != nil {
		a.addCommandError(fmt.Sprintf("Error creating session: %v", err))
		return nil
	}
	raw := strings.TrimSpace(strings.TrimPrefix(cmd, "/esm"))
	if raw == "" || raw == "status" {
		a.showESMStatus()
		return nil
	}
	if a.isThinking {
		a.addCommandError("Cannot change ESM while the agent is running. Press Esc to abort first.")
		return nil
	}

	ctx := context.Background()
	store := a.ensureESMStore()
	sessionID := a.currentSessionID()
	sub, rest := splitESMSubcommand(raw)
	var (
		obj            *esm.Objective
		err            error
		startOnSuccess bool
	)
	switch sub {
	case "edit":
		if strings.TrimSpace(rest) == "" {
			a.addCommandError(commandUsage(a.translator, "/esm edit <objective>"))
			return nil
		}
		obj, err = store.Edit(ctx, sessionID, rest)
	case "pause":
		if strings.TrimSpace(rest) != "" {
			a.addCommandError(commandUsage(a.translator, "/esm pause"))
			return nil
		}
		obj, err = store.Pause(ctx, sessionID)
	case "resume":
		if strings.TrimSpace(rest) != "" {
			a.addCommandError(commandUsage(a.translator, "/esm resume"))
			return nil
		}
		obj, err = store.Resume(ctx, sessionID)
		startOnSuccess = true
	case "clear":
		if strings.TrimSpace(rest) != "" {
			a.addCommandError(commandUsage(a.translator, "/esm clear"))
			return nil
		}
		err = store.Clear(ctx, sessionID)
	case "budget":
		obj, err = a.handleESMBudget(ctx, store, sessionID, rest)
	default:
		obj, err = store.Create(ctx, sessionID, raw, nil)
		startOnSuccess = true
	}
	if err != nil {
		a.addCommandError(formatESMCommandError(err))
		return nil
	}
	if err := a.syncESMTools(); err != nil {
		a.addCommandError(fmt.Sprintf("ESM updated, but tool sync failed: %v", err))
		return nil
	}
	if sub != "status" {
		a.resetAgent(fmt.Errorf("esm changed"))
	}
	if sub == "clear" {
		a.addCommandStatus("Enable Supervisor Mode cleared.")
		return nil
	}
	a.addCommandStatus(formatESMStatus(obj))
	if startOnSuccess && obj != nil && obj.Status == esm.StatusActive {
		return a.startESMContinuationIfIdle()
	}
	return nil
}

func splitESMSubcommand(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	idx := strings.IndexAny(raw, " \t")
	if idx < 0 {
		return raw, ""
	}
	return raw[:idx], strings.TrimSpace(raw[idx+1:])
}

func (a *App) handleESMBudget(ctx context.Context, store *esm.Store, sessionID, rest string) (*esm.Objective, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil, fmt.Errorf("%s", commandUsage(a.translator, "/esm budget <tokens|off>"))
	}
	if rest == "off" {
		return store.SetBudget(ctx, sessionID, nil)
	}
	value, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || value <= 0 {
		return nil, fmt.Errorf("ESM budget must be a positive integer or off")
	}
	return store.SetBudget(ctx, sessionID, &value)
}

func formatESMCommandError(err error) string {
	switch {
	case errors.Is(err, esm.ErrNotFound):
		return "No ESM objective. Create one with /esm <objective>."
	case errors.Is(err, esm.ErrObjectiveExists):
		return "An unfinished ESM objective already exists. Use /esm edit <objective> or /esm clear."
	case errors.Is(err, esm.ErrInvalidObjective):
		return "ESM objective cannot be empty."
	case errors.Is(err, esm.ErrBudgetStillHit):
		return "ESM is still budget_limited. Raise the budget with /esm budget <tokens> or remove it with /esm budget off, then /esm resume."
	case errors.Is(err, esm.ErrInvalidTransition):
		return "ESM status cannot be changed that way."
	default:
		return err.Error()
	}
}

func (a *App) showESMStatus() {
	obj, err := a.loadESMObjective(context.Background())
	if errors.Is(err, esm.ErrNotFound) {
		a.setESMFooter(nil)
		a.addCommandStatus("Enable Supervisor Mode\nStatus: none\n\nCreate one with /esm <objective>.")
		return
	}
	if err != nil {
		a.addCommandError(fmt.Sprintf("Failed to load ESM status: %v", err))
		return
	}
	a.setESMFooter(obj)
	a.addCommandStatus(formatESMStatus(obj))
}

func formatESMStatus(obj *esm.Objective) string {
	if obj == nil {
		return "Enable Supervisor Mode\nStatus: none"
	}
	var b strings.Builder
	b.WriteString(esm.FormatObjective(obj))
	b.WriteString("\n\nCommands: /esm edit <objective>, /esm pause, /esm resume, /esm clear, /esm budget <tokens|off>")
	return b.String()
}

func (a *App) prepareESMRun() {
	var obj *esm.Objective
	if current, err := a.loadESMObjective(context.Background()); err == nil {
		obj = current
	}
	a.esmMu.Lock()
	a.esmRunSeq++
	a.esmSteeredSeq = 0
	a.esmBudgetLimitedSeq = 0
	a.esmBudgetSteeredSeq = 0
	a.esmRunTokens = 0
	a.esmRunSessionID = a.currentSessionID()
	a.esmRunTracked = obj != nil && (obj.Status == esm.StatusActive || obj.Status == esm.StatusCompleteCandidate)
	a.esmRunID = ""
	if a.esmRunTracked {
		a.esmRunID = fmt.Sprintf("esm-run-%d-%d", time.Now().UnixNano(), a.esmRunSeq)
	}
	a.esmMu.Unlock()
	a.setESMFooter(obj)
}

func (a *App) nextESMSteeringMessages() []provider.Message {
	a.esmMu.Lock()
	seq := a.esmRunSeq
	if seq == 0 {
		a.esmMu.Unlock()
		return nil
	}
	tracked := a.esmRunTracked
	includeRegular := a.esmSteeredSeq != seq
	if includeRegular {
		a.esmSteeredSeq = seq
	}
	includeBudgetLimit := a.esmBudgetLimitedSeq == seq && a.esmBudgetSteeredSeq != seq
	if includeBudgetLimit {
		a.esmBudgetSteeredSeq = seq
	}
	a.esmMu.Unlock()
	if !tracked {
		return nil
	}
	obj, err := a.loadESMObjective(context.Background())
	if err != nil || obj == nil {
		return nil
	}
	var messages []provider.Message
	if includeRegular && obj.Status == esm.StatusActive {
		messages = append(messages, esm.SteeringMessage(obj))
	}
	if includeBudgetLimit && obj.Status == esm.StatusBudgetLimited {
		messages = append(messages, esm.BudgetLimitMessage(obj))
	}
	return messages
}

func (a *App) recordESMUsage(usage *provider.Usage) {
	if usage == nil {
		return
	}
	total := usage.TotalTokens
	if total <= 0 {
		total = usage.Input + usage.Output
	}
	if total <= 0 {
		return
	}
	a.esmMu.Lock()
	tracked := a.esmRunTracked
	sessionID := a.esmRunSessionID
	seq := a.esmRunSeq
	if !tracked || sessionID == "" {
		a.esmMu.Unlock()
		return
	}
	a.esmMu.Unlock()

	store := a.ensureESMStore()
	obj, err := store.AccountUsage(context.Background(), sessionID, int64(total), 0)
	if err != nil {
		if !errors.Is(err, esm.ErrNotFound) {
			a.addCommandError(fmt.Sprintf("Failed to account ESM usage: %v", err))
		}
		a.esmMu.Lock()
		if a.esmRunTracked && a.esmRunSessionID == sessionID {
			a.esmRunTokens += int64(total)
		}
		a.esmMu.Unlock()
		return
	}
	a.setESMFooter(obj)
	if obj != nil && obj.Status == esm.StatusBudgetLimited {
		a.esmMu.Lock()
		if a.esmRunTracked && a.esmRunSessionID == sessionID && a.esmRunSeq == seq {
			a.esmBudgetLimitedSeq = seq
		}
		a.esmMu.Unlock()
	}
}

func (a *App) finishESMRun(err error) tea.Cmd {
	a.esmMu.Lock()
	tracked := a.esmRunTracked
	sessionID := a.esmRunSessionID
	runID := a.esmRunID
	tokens := a.esmRunTokens
	a.esmRunTracked = false
	a.esmRunSessionID = ""
	a.esmRunID = ""
	a.esmBudgetLimitedSeq = 0
	a.esmBudgetSteeredSeq = 0
	a.esmRunTokens = 0
	a.esmMu.Unlock()

	if !tracked || sessionID == "" {
		return nil
	}
	store := a.ensureESMStore()
	if tokens > 0 || a.lastDuration > 0 {
		if obj, accountErr := store.AccountUsage(context.Background(), sessionID, tokens, int64(a.lastDuration.Milliseconds())); accountErr == nil {
			a.setESMFooter(obj)
		} else if !errors.Is(accountErr, esm.ErrNotFound) {
			a.addCommandError(fmt.Sprintf("Failed to account ESM usage: %v", accountErr))
		}
	}
	if err != nil && esm.IsUsageLimitError(err) {
		if obj, markErr := store.MarkUsageLimited(context.Background(), sessionID); markErr == nil {
			a.setESMFooter(obj)
		}
	}
	if obj, finishErr := store.FinishRun(context.Background(), sessionID, runID); finishErr == nil {
		a.setESMFooter(obj)
	} else if !errors.Is(finishErr, esm.ErrNotFound) {
		a.addCommandError(fmt.Sprintf("Failed to finish ESM run: %v", finishErr))
	}
	if syncErr := a.syncESMTools(); syncErr != nil {
		a.addCommandError(fmt.Sprintf("Failed to sync ESM tools: %v", syncErr))
		return nil
	}
	a.refreshESMPanel()
	if err != nil {
		return nil
	}
	return a.startESMContinuationIfIdle()
}

func (a *App) startESMContinuationIfIdle() tea.Cmd {
	if a.mode == "plan" || a.manualCompactionActive || a.waitingForApproval || a.waitingForQuestion || a.hasQueuedInput() {
		return nil
	}
	if strings.TrimSpace(a.input.Value()) != "" {
		return nil
	}
	if err := a.ensureSession(); err != nil {
		a.addCommandError(fmt.Sprintf("Error creating session: %v", err))
		return nil
	}
	obj, err := a.loadESMObjective(context.Background())
	if errors.Is(err, esm.ErrNotFound) || obj == nil || !obj.CanAutoRun() {
		return nil
	}
	if err != nil {
		a.addCommandError(fmt.Sprintf("Failed to load ESM status: %v", err))
		return nil
	}
	if err := a.syncESMTools(); err != nil {
		a.addCommandError(fmt.Sprintf("Failed to sync ESM tools: %v", err))
		return nil
	}
	if cmd := a.startESMSubAgentContinuation(obj); cmd != nil {
		return cmd
	}
	a.prepareESMRun()
	a.ensureAgent()
	a.registerManagedAgent()
	msg := esm.ContinuationMessage(obj)
	ctx := context.Background()
	return func() tea.Msg {
		return agentStreamStartMsg{
			input:      "",
			eventCh:    a.agent.RunWithUserMessage(ctx, msg),
			compacting: false,
		}
	}
}

func (a *App) startESMSubAgentContinuation(obj *esm.Objective) tea.Cmd {
	if obj == nil || a.agentMgr == nil || a.provider == nil || a.model == nil {
		return nil
	}
	a.prepareESMRun()
	sessionID := a.currentSessionID()
	runID := a.currentESMRunID()
	workDir := a.currentCwd()
	store := a.ensureESMStore()
	manager := a.agentMgr
	mode := a.esmRoleMode()
	eventCh := make(chan internalagent.Event, 100)
	return func() tea.Msg {
		go a.runESMSubAgentSupervisor(context.Background(), eventCh, manager, store, sessionID, runID, workDir, mode)
		return agentStreamStartMsg{
			input:      "",
			eventCh:    eventCh,
			compacting: false,
		}
	}
}

func (a *App) esmRoleMode() string {
	if a.mode != "" {
		return a.mode
	}
	return "agent"
}

func (a *App) runESMSubAgentSupervisor(ctx context.Context, eventCh chan<- internalagent.Event, manager *internalagent.AgentManager, store *esm.Store, sessionID, runID, workDir, roleMode string) {
	defer close(eventCh)
	if store == nil || sessionID == "" || runID == "" || manager == nil {
		sendESMEvent(ctx, eventCh, internalagent.Event{Type: internalagent.EventError, Error: fmt.Errorf("ESM supervisor missing session, run, or agent state")})
		return
	}
	adapter := a.newESMRuntimeAdapter(eventCh, manager)
	runtime := &esm.Supervisor{Store: store, Adapter: adapter, Events: adapter}
	if _, err := runtime.Run(ctx, sessionID, runID, workDir, roleMode); err != nil {
		sendESMEvent(ctx, eventCh, internalagent.Event{Type: internalagent.EventError, Error: err})
		return
	}
	sendESMEvent(ctx, eventCh, internalagent.Event{Type: internalagent.EventDone, Done: true})
}

type esmRoleResult struct {
	Response  string
	Tokens    int64
	ToolCalls int
	ToolNames map[string]int
	ToolError map[string]bool
}

type esmRoleRunner func(ctx context.Context, eventCh chan<- internalagent.Event, manager *internalagent.AgentManager, id, workDir, mode string, toolFilter []string, maxIterations int, task string) (esmRoleResult, error)

func (a *App) runESMRoleAgent(ctx context.Context, eventCh chan<- internalagent.Event, manager *internalagent.AgentManager, id, workDir, mode string, toolFilter []string, maxIterations int, task string) (esmRoleResult, error) {
	return a.runESMRoleAgentWithTimeout(ctx, eventCh, manager, id, workDir, mode, toolFilter, maxIterations, task, esmRoleTimeout)
}

func (a *App) runESMRoleAgentWithTimeout(ctx context.Context, eventCh chan<- internalagent.Event, manager *internalagent.AgentManager, id, workDir, mode string, toolFilter []string, maxIterations int, task string, timeout time.Duration) (esmRoleResult, error) {
	if a.esmRoleRunner != nil {
		return a.esmRoleRunner(ctx, eventCh, manager, id, workDir, mode, toolFilter, maxIterations, task)
	}
	no := false
	childID := agentpkg.AgentID(id)
	child, err := manager.Create(internalagent.AgentOptions{
		ID:            childID,
		IsSubAgent:    true,
		Mode:          mode,
		WorkDir:       workDir,
		Tools:         toolFilter,
		MaxIterations: maxIterations,
		MultiAgent:    &no,
		DelegateMode:  &no,
		Workflows:     &no,
	})
	if err != nil {
		return esmRoleResult{}, err
	}
	a.setActiveESMAgent(childID)
	defer func() {
		a.clearActiveESMAgent(childID)
		_ = manager.Destroy(childID)
	}()

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	manager.MarkRunning(childID)
	manager.SetCancel(childID, cancel)
	defer manager.SetCancel(childID, nil)

	var runErr error
	result := esmRoleResult{ToolNames: make(map[string]int), ToolError: make(map[string]bool)}
	completed := false
	for ev := range child.Run(runCtx, task) {
		if ev.Usage != nil {
			total := ev.Usage.TotalTokens
			if total <= 0 {
				total = ev.Usage.InputTokens + ev.Usage.OutputTokens
			}
			if total > 0 {
				result.Tokens += int64(total)
			}
		}
		switch ev.Type {
		case agentpkg.EventToolExecutionStart:
			name := ev.ToolName
			if name == "" && ev.ToolCall != nil {
				name = ev.ToolCall.Name
			}
			if name != "" {
				result.ToolCalls++
				result.ToolNames[name]++
			}
		case agentpkg.EventToolExecutionEnd, agentpkg.EventToolResult:
			if ev.ToolError != nil && ev.ToolCallID != "" {
				result.ToolError[ev.ToolCallID] = true
			}
		}
		if shouldForwardESMRoleEvent(ev.Type) {
			sendESMEvent(ctx, eventCh, publicAgentEventToInternal(ev, childID))
		}
		switch ev.Type {
		case agentpkg.EventRunFinished:
			completed = true
			switch ev.Status {
			case agentpkg.TaskFailed:
				runErr = ev.Error
				manager.MarkError(childID, ev.Error)
			case agentpkg.TaskCanceled:
				runErr = ev.Error
				manager.MarkCanceled(childID, ev.Error)
			default:
				manager.MarkDone(childID, lastPublicAssistantResponse(child))
			}
		case agentpkg.EventDone:
			if !completed {
				completed = true
				manager.MarkDone(childID, lastPublicAssistantResponse(child))
			}
		case agentpkg.EventError:
			if !completed {
				completed = true
				runErr = ev.Error
				manager.MarkError(childID, ev.Error)
			}
		}
	}
	if !completed && runCtx.Err() != nil {
		runErr = runCtx.Err()
		manager.MarkError(childID, runErr)
	} else if !completed {
		manager.MarkDone(childID, lastPublicAssistantResponse(child))
	}
	result.Response = lastPublicAssistantResponse(child)
	return result, runErr
}

func formatESMRejectionStatus(subject string, obj *esm.Objective, reason string) string {
	if obj == nil {
		return fmt.Sprintf("ESM %s rejected: %s", subject, strings.ReplaceAll(reason, "\n", "; "))
	}
	message := fmt.Sprintf("ESM %s rejected (%d/%d)", subject, obj.RejectionCount, esm.CompletionRejectionLimit)
	if obj.Status == esm.StatusPaused && obj.RejectionCount >= esm.CompletionRejectionLimit {
		message = "WARNING: " + message + "; workflow paused by the rejection circuit breaker. Review the remaining work, then run /esm resume"
	} else {
		message += "; objective stays active"
	}
	if reason != "" {
		message += ": " + strings.ReplaceAll(reason, "\n", "; ")
	}
	return message
}

func shouldForwardESMRoleEvent(eventType agentpkg.EventType) bool {
	switch eventType {
	case agentpkg.EventAgentStart, agentpkg.EventAgentEnd, agentpkg.EventDone, agentpkg.EventError, agentpkg.EventRunFinished:
		return false
	default:
		return true
	}
}

func (a *App) setActiveESMAgent(id agentpkg.AgentID) {
	a.esmMu.Lock()
	defer a.esmMu.Unlock()
	a.esmActiveAgentID = id
}

func (a *App) clearActiveESMAgent(id agentpkg.AgentID) {
	a.esmMu.Lock()
	defer a.esmMu.Unlock()
	if a.esmActiveAgentID == id {
		a.esmActiveAgentID = ""
	}
}

func (a *App) abortActiveESMAgent() {
	a.esmMu.Lock()
	id := a.esmActiveAgentID
	a.esmActiveAgentID = ""
	manager := a.agentMgr
	a.esmMu.Unlock()
	if id != "" && manager != nil {
		_ = manager.Destroy(id)
	}
}

func sendESMEvent(ctx context.Context, ch chan<- internalagent.Event, ev internalagent.Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func publicAgentEventToInternal(ev agentpkg.Event, childID agentpkg.AgentID) internalagent.Event {
	out := internalagent.Event{
		Type:            internalagent.EventType(ev.Type),
		AgentID:         childID,
		TextDelta:       ev.TextDelta,
		ThinkDelta:      ev.ThinkDelta,
		ToolCallID:      ev.ToolCallID,
		ToolName:        ev.ToolName,
		ToolArgs:        ev.ToolArgs,
		ToolResult:      ev.ToolResult,
		ToolError:       ev.ToolError,
		PartialResult:   ev.PartialResult,
		StatusMessage:   ev.StatusMessage,
		Done:            ev.Done,
		StopReason:      ev.StopReason,
		Error:           ev.Error,
		Status:          internalagent.TaskStatus(ev.Status),
		ApprovalID:      ev.ApprovalID,
		ApprovalTool:    ev.ApprovalTool,
		ApprovalArgs:    ev.ApprovalArgs,
		ApprovalResult:  ev.ApprovalResult,
		QuestionID:      ev.QuestionID,
		QuestionText:    ev.QuestionText,
		QuestionOptions: ev.QuestionOptions,
		QuestionContext: ev.QuestionContext,
		QuestionAnswer:  ev.QuestionAnswer,
	}
	if ev.ToolCall != nil {
		out.ToolCall = &provider.ToolCallBlock{
			ID:               ev.ToolCall.ID,
			Name:             ev.ToolCall.Name,
			Arguments:        ev.ToolCall.Arguments,
			InvalidArguments: ev.ToolCall.InvalidArguments,
			ThoughtSignature: ev.ToolCall.ThoughtSignature,
		}
	}
	return out
}

func lastPublicAssistantResponse(a agentpkg.Agent) string {
	if a == nil {
		return ""
	}
	messages := a.GetMessages()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentpkg.RoleAssistant {
			continue
		}
		if messages[i].Content != "" {
			return messages[i].Content
		}
		var b strings.Builder
		for _, block := range messages[i].Contents {
			if block.Type == "text" && block.Text != "" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
}
