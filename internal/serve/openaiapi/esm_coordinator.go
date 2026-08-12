package openaiapi

// Server-side ESM execution. This deliberately owns no browser state: the
// objective store is the durable state machine and this coordinator is a
// restartable, best-effort worker for it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/esm"
	"github.com/startvibecoding/mothx/internal/session"
)

const esmCoordinatorTimeout = 30 * time.Minute

type esmCoordinator struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func newESMCoordinator() *esmCoordinator {
	return &esmCoordinator{running: make(map[string]context.CancelFunc)}
}
func (c *esmCoordinator) start(s *Server, sessionID string) {
	if c == nil || s == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	if _, ok := c.running[sessionID]; ok {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.running[sessionID] = cancel
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); delete(c.running, sessionID); c.mu.Unlock() }()
		s.runESMCoordinator(ctx, sessionID)
	}()
}

func (s *Server) ensureESMCoordinator() *esmCoordinator {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.esmCoordinator == nil {
		s.esmCoordinator = newESMCoordinator()
	}
	return s.esmCoordinator
}
func (s *Server) startESM(sessionID string) { s.ensureESMCoordinator().start(s, sessionID) }

func (s *Server) stopESM(sessionID string) {
	s.mu.RLock()
	c := s.esmCoordinator
	s.mu.RUnlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel := c.running[sessionID]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Server) runESMCoordinator(ctx context.Context, sessionID string) {
	store := s.esmStore()
	if store == nil {
		return
	}
	workDir, found, err := s.findSessionWorkDir(sessionID)
	if err != nil {
		return
	}
	if !found {
		workDir = s.cfg.GetWorkDir()
	}
	sess, err := s.getOrCreateSession(sessionID, workDir)
	if err != nil || sess == nil || !s.pool.Pin(sess) {
		return
	}
	defer s.pool.Unpin(sess)
	release, ok := session.TryLockRuntime(s.settings.GetSessionDir(), sessionID)
	if !ok {
		return
	}
	if !sess.TryLock() {
		release()
		return
	}
	defer release()
	defer sess.Unlock()
	if err := sess.Manager.Reload(); err != nil {
		return
	}
	obj, err := store.Get(context.Background(), sessionID)
	if err != nil {
		return
	}
	for obj != nil && obj.CanAutoRun() {
		if ctx.Err() != nil {
			return
		}
		if obj.Status == esm.StatusActive {
			if !s.runESMRole(ctx, sess, store, obj, "worker", nil, workDir) {
				return
			}
		} else if obj.Status == esm.StatusCompleteCandidate {
			if !s.runESMRole(ctx, sess, store, obj, "critic", []string{"read", "grep", "find", "ls"}, workDir) {
				return
			}
			obj, err = store.Get(context.Background(), sessionID)
			if err != nil {
				return
			}
			if obj.Status == esm.StatusCompleteCandidate {
				if !s.runESMRole(ctx, sess, store, obj, "audit", []string{"read", "grep", "find", "ls"}, workDir) {
					return
				}
			}
		}
		obj, err = store.Get(context.Background(), sessionID)
		if err != nil {
			return
		}
		if obj.Status == esm.StatusActive || obj.Status == esm.StatusCompleteCandidate {
			continue
		}
		break
	}
}

func (s *Server) runESMRole(parent context.Context, sess *APISession, store *esm.Store, obj *esm.Objective, role string, tools []string, workDir string) bool {
	ctx, cancel := context.WithTimeout(parent, esmCoordinatorTimeout)
	defer cancel()
	phase := esm.PhaseWorker
	prompt := esm.WorkerTaskPrompt(obj)
	guidance, _ := session.ListESMGuidance(s.settings.GetSessionDir(), obj.SessionID, "pending", 100)
	if len(guidance) > 0 {
		prompt += "\n\nUser guidance queued for this objective:\n"
		for _, g := range guidance {
			prompt += "- " + g.Guidance + "\n"
		}
	}
	max := 200
	if role == "critic" {
		phase = esm.PhaseCritic
		prompt = esm.CriticTaskPrompt(obj)
		max = 80
	}
	if role == "audit" {
		phase = esm.PhaseAudit
		prompt = esm.AuditTaskPrompt(obj)
		max = 80
	}
	if len(guidance) > 0 {
		prompt += "\n\nUser guidance queued for this objective:\n"
		for _, g := range guidance {
			prompt += "- " + g.Guidance + "\n"
		}
	}
	if _, err := store.SetPhase(ctx, obj.SessionID, phase); err != nil {
		return false
	}
	runID := newRunID() + "-" + role
	s.mu.RLock()
	model := cloneModel(s.model)
	s.mu.RUnlock()
	if model == nil {
		return false
	}
	_ = s.recordSessionRunEvent(sess, runID, "esm.role_started", "running", "webui_esm_"+role, model.ID, "agent", map[string]any{"role": role, "phase": string(phase)})
	if s.runManager != nil {
		_ = s.runManager.Create(session.SessionRun{ID: runID, SessionID: obj.SessionID, WorkDir: workDir, Source: "webui_esm_" + role, Model: model.ID, Mode: "agent", Status: "running", StartedAt: time.Now(), UpdatedAt: time.Now()})
	}
	started := time.Now()
	defer func() {
		if latest, e := store.Get(context.Background(), obj.SessionID); e == nil {
			s.publishESM(obj.SessionID, esmSnapshot(latest))
		}
		if s.runManager != nil {
			s.runManager.Finish(runID, "completed", "")
		}
		_ = s.recordSessionRunEvent(sess, runID, "esm.role_finished", "completed", "webui_esm_"+role, model.ID, "agent", map[string]any{"role": role, "phase": string(phase)})
	}()
	mgr := s.newAgentManagerForSession(sess)
	no := false
	child, err := mgr.Create(agent.AgentOptions{ID: agentpkg.AgentID(runID), IsSubAgent: true, Mode: "agent", WorkDir: workDir, Tools: tools, MaxIterations: max, MultiAgent: &no, DelegateMode: &no, Workflows: &no})
	if err != nil {
		return s.esmRecovery(store, obj, role, err, runID)
	}
	defer mgr.Destroy(child.ID())
	mgr.MarkRunning(child.ID())
	s.PublishExternalSubAgentEvent(obj.SessionID, agent.Event{AgentID: child.ID(), Type: agent.EventAgentStart})
	var response string
	var tokens int64
	for ev := range child.Run(ctx, prompt) {
		s.publishESMSubAgentEvent(obj.SessionID, child.ID(), ev)
		if ev.Usage != nil {
			n := int64(ev.Usage.TotalTokens)
			if n <= 0 {
				n = int64(ev.Usage.InputTokens + ev.Usage.OutputTokens)
			}
			tokens += n
		}
		if ev.Type == agentpkg.EventAgentEnd && len(ev.Messages) > 0 {
			response = ev.Messages[len(ev.Messages)-1].Content
		}
		if ev.Type == agentpkg.EventRunFinished && ev.Error != nil {
			mgr.MarkError(child.ID(), ev.Error)
			return s.esmRecovery(store, obj, role, ev.Error, runID)
		}
	}
	mgr.MarkDone(child.ID(), response)
	s.PublishExternalSubAgentEvent(obj.SessionID, agent.Event{AgentID: child.ID(), Type: agent.EventRunFinished, Status: agent.TaskSuccess})
	if tokens > 0 {
		next, e := store.AccountUsage(ctx, obj.SessionID, tokens, time.Since(started).Milliseconds())
		if e != nil {
			return false
		}
		if next.Status == esm.StatusBudgetLimited {
			return true
		}
	}
	var ok bool
	if len(guidance) > 0 {
		ids := make([]string, 0, len(guidance))
		for _, g := range guidance {
			ids = append(ids, g.ID)
		}
		_ = session.ConsumeESMGuidance(s.settings.GetSessionDir(), obj.SessionID, ids)
	}
	if role == "worker" {
		ok = s.applyESMWorker(ctx, store, obj, runID, response)
	} else {
		ok = s.applyESMReview(ctx, store, obj, role, runID, response)
	}
	if latest, e := store.Get(ctx, obj.SessionID); e == nil {
		_ = s.recordSessionRunEvent(sess, runID, "esm.state_changed", string(latest.Status), "webui_esm_"+role, model.ID, "agent", map[string]any{"role": role, "phase": string(latest.Phase), "status": string(latest.Status), "progress": latest.ProgressSummary, "review": latest.CompletionReview, "remainingWork": latest.RemainingWork})
	}
	return ok
}

// publishESMSubAgentEvent projects the role's useful live activity into the
// existing WebUI sub-agent history. ESM has no conversational parent agent, so
// registering it in APISession.AgentMgr would make it invisible to the normal
// parent-child projection.
func (s *Server) publishESMSubAgentEvent(sessionID string, childID agentpkg.AgentID, ev agentpkg.Event) {
	if s == nil || childID == "" {
		return
	}
	out := agent.Event{AgentID: childID}
	switch ev.Type {
	case agentpkg.EventTextDelta:
		out.Type, out.TextDelta = agent.EventTextDelta, ev.TextDelta
	case agentpkg.EventToolCall:
		out.Type, out.ToolName, out.ToolCallID, out.ToolArgs = agent.EventToolCall, ev.ToolName, ev.ToolCallID, ev.ToolArgs
	case agentpkg.EventToolExecutionEnd:
		out.Type, out.ToolName, out.ToolCallID, out.ToolArgs, out.ToolResult, out.ToolError = agent.EventToolExecutionEnd, ev.ToolName, ev.ToolCallID, ev.ToolArgs, ev.ToolResult, ev.ToolError
	case agentpkg.EventRunFinished:
		out.Type, out.Status, out.Error = agent.EventRunFinished, agent.TaskStatus(ev.Status), ev.Error
	default:
		return
	}
	s.PublishExternalSubAgentEvent(sessionID, out)
}

func (s *Server) applyESMWorker(ctx context.Context, store *esm.Store, obj *esm.Objective, runID, response string) bool {
	report, err := esm.ParseWorkerReport(response)
	if err != nil {
		_, e := store.RejectWorkerReport(ctx, obj.SessionID, runID, "worker report was not structured: "+err.Error(), nil)
		return e == nil
	}
	if _, err = store.RecordWorkerProgress(ctx, obj.SessionID, report.Summary, report.RemainingWork); err != nil {
		return false
	}
	switch report.Status {
	case esm.WorkerStatusContinue:
		return true
	case esm.WorkerStatusBlockedCandidate:
		_, err = store.UpdateFromModelForRun(ctx, obj.SessionID, esm.StatusBlocked, strings.Join(report.Blockers, "; "), runID)
		return err == nil
	case esm.WorkerStatusCompleteCandidate:
		if len(report.RemainingWork) > 0 || len(report.Blockers) > 0 {
			_, err = store.RejectWorkerReport(ctx, obj.SessionID, runID, "completion candidate contains remaining work or blockers", report.RemainingWork)
			return err == nil
		}
		_, err = store.UpdateFromModelForRun(ctx, obj.SessionID, esm.StatusCompleteCandidate, report.Summary, runID)
		return err == nil
	}
	return false
}
func (s *Server) applyESMReview(ctx context.Context, store *esm.Store, obj *esm.Objective, role, runID, response string) bool {
	report, err := esm.ParseAuditReport(response)
	if err != nil {
		_, e := store.RejectCompletionCandidateForRun(ctx, obj.SessionID, runID, role+" report invalid: "+err.Error(), nil)
		return e == nil
	}
	review := report.Review
	if report.Verdict == esm.AuditVerdictPass {
		if role == "critic" {
			return true
		}
		_, err = store.MarkCompleteFromAudit(ctx, obj.SessionID, review)
		return err == nil
	}
	_, err = store.RejectCompletionCandidateForRun(ctx, obj.SessionID, runID, review, report.MissingWork)
	return err == nil
}
func (s *Server) esmRecovery(store *esm.Store, obj *esm.Objective, role string, runErr error, runID string) bool {
	reason := fmt.Sprintf("%s interrupted: %v", role, runErr)
	_, err := store.RecordRecovery(context.Background(), obj.SessionID, reason, "A fresh worker will retry from the persisted repository state.", obj.RemainingWork)
	return err == nil
}

// reconcileESMObjectives restarts durable objectives whose local role process
// disappeared with the service. It is intentionally idempotent.
func (s *Server) reconcileESMObjectives() {
	if s == nil || s.settings == nil {
		return
	}
	db, err := session.OpenRootDB(s.settings.GetSessionDir())
	if err != nil {
		return
	}
	defer db.Close()
	rows, err := db.Query(`SELECT session_id FROM session_esm_objectives WHERE status IN (?, ?)`, esm.StatusActive, esm.StatusCompleteCandidate)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			s.startESM(id)
		}
	}
}
