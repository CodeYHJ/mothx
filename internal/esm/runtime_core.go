package esm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
)

// Role is the role in the TUI-defined ESM pipeline.
type Role string

const (
	RoleWorker   Role = "worker"
	RoleCritic   Role = "critic"
	RoleAudit    Role = "audit"
	RoleRecovery Role = "recovery"
)

const (
	RoleTimeout             = 30 * time.Minute
	RecoveryObserverTimeout = 5 * time.Minute
)

// RoleRequest is the complete host execution contract. The host runs an
// isolated agent; it must not make ESM state decisions.
type RoleRequest struct {
	SessionID     string
	RunID         string
	Role          Role
	WorkDir       string
	Mode          string
	Tools         []string
	MaxIterations int
	Prompt        string
	Objective     *Objective
}

// RuntimeAdapter is implemented by TUI/WebUI agent hosts. ESM policy remains
// in Supervisor; adapters only execute roles and project host events.
type RuntimeAdapter interface {
	RunRole(context.Context, RoleRequest) (RoleResult, error)
	RunRecoveryObserver(context.Context, RoleRequest, error) (RoleResult, error)
}

// RuntimeEvent is UI-neutral lifecycle output. It is informational only.
type RuntimeEvent struct {
	SessionID string
	RunID     string
	Role      Role
	Type      string
	Status    string
	Message   string
}

type RuntimeEventSink interface {
	PublishESMEvent(context.Context, RuntimeEvent) error
}

// Supervisor is the single ESM runtime extracted from the TUI implementation.
type Supervisor struct {
	Store   *Store
	Adapter RuntimeAdapter
	Events  RuntimeEventSink
}

// Run executes one continuation using the TUI order: worker, then critic and
// audit only for a completion candidate. A normal worker continue returns
// active and is resumed by the next continuation.
func (s *Supervisor) Run(ctx context.Context, sessionID, runID, workDir, mode string) (*Objective, error) {
	if s == nil || s.Store == nil {
		return nil, errors.New("esm supervisor store is nil")
	}
	if s.Adapter == nil {
		return nil, errors.New("esm supervisor adapter is nil")
	}
	obj, err := s.Store.Get(ctx, sessionID)
	if err != nil || obj == nil || !obj.CanAutoRun() {
		return obj, err
	}
	if obj.Status == StatusActive {
		obj, err = s.runRole(ctx, obj, RoleWorker, runID+"-worker", workDir, mode)
		if err != nil || obj == nil || obj.Status != StatusCompleteCandidate {
			return obj, err
		}
	}
	if obj.Status != StatusCompleteCandidate {
		return obj, nil
	}
	obj, err = s.runRole(ctx, obj, RoleCritic, runID+"-critic", workDir, mode)
	if err != nil || obj == nil || obj.Status != StatusCompleteCandidate {
		return obj, err
	}
	return s.runRole(ctx, obj, RoleAudit, runID+"-audit", workDir, mode)
}

func (s *Supervisor) runRole(ctx context.Context, obj *Objective, role Role, runID, workDir, mode string) (*Objective, error) {
	req := RoleRequest{SessionID: obj.SessionID, RunID: runID, Role: role, WorkDir: workDir, Mode: mode, Objective: obj, Prompt: rolePrompt(obj, role)}
	phase := PhaseWorker
	switch role {
	case RoleWorker:
		req.MaxIterations = 200
	case RoleCritic, RoleAudit:
		phase = PhaseCritic
		if role == RoleAudit {
			phase = PhaseAudit
		}
		req.MaxIterations = 80
		req.Tools = []string{"read", "grep", "find", "ls"}
	default:
		return obj, fmt.Errorf("unsupported ESM role %q", role)
	}
	if _, err := s.Store.SetPhase(ctx, obj.SessionID, phase); err != nil {
		return obj, err
	}
	_ = s.publish(ctx, RuntimeEvent{SessionID: obj.SessionID, RunID: runID, Role: role, Type: "role_started", Status: "running"})

	started := time.Now()
	result, runErr := s.Adapter.RunRole(ctx, req)
	if result.DurationMS <= 0 {
		result.DurationMS = time.Since(started).Milliseconds()
	}
	obj, err := s.account(ctx, obj, result)
	if err != nil {
		return obj, err
	}
	if obj.Status == StatusBudgetLimited {
		return obj, nil
	}
	if runErr != nil {
		return s.handleRoleFailure(ctx, obj, req, result, runErr)
	}

	var outcome Outcome
	var ok bool
	if role == RoleWorker {
		outcome, ok, err = ApplyWorkerResult(ctx, s.Store, obj.SessionID, runID, result)
	} else {
		outcome, ok, err = ApplyReviewResult(ctx, s.Store, obj.SessionID, runID, string(role), result)
	}
	if err != nil {
		return obj, err
	}
	if !ok {
		return obj, fmt.Errorf("ESM %s result was not applied", role)
	}
	if outcome.Objective != nil {
		obj = outcome.Objective
	}
	message := outcome.Message
	if outcome.Rejected {
		message = outcome.Subject + " rejected: " + outcome.Reason
	}
	_ = s.publish(ctx, RuntimeEvent{SessionID: obj.SessionID, RunID: runID, Role: role, Type: "role_finished", Status: string(obj.Status), Message: message})
	return obj, nil
}

func (s *Supervisor) account(ctx context.Context, obj *Objective, result RoleResult) (*Objective, error) {
	if result.Tokens == 0 && result.DurationMS == 0 {
		return obj, nil
	}
	return s.Store.AccountUsage(ctx, obj.SessionID, result.Tokens, result.DurationMS)
}

func (s *Supervisor) handleRoleFailure(ctx context.Context, obj *Objective, req RoleRequest, result RoleResult, runErr error) (*Objective, error) {
	role := string(req.Role)
	_ = s.publish(ctx, RuntimeEvent{SessionID: obj.SessionID, RunID: req.RunID, Role: req.Role, Type: "role_failed", Status: "failed", Message: compactESMError(runErr)})
	if req.Role != RoleWorker {
		review := fmt.Sprintf("%s sub-agent failed; completion candidate rejected: %s", strings.Title(role), compactESMError(runErr))
		if next, err := s.Store.RejectCompletionCandidateForRun(ctx, obj.SessionID, req.RunID, review, nil); err == nil {
			obj = next
		}
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		if obj.RecoveryCount >= RecoveryLimit {
			return s.recordRecovery(ctx, obj, string(req.Role)+" timed out after recovery limit: "+compactESMError(runErr), obj.RemainingWork)
		}
		return s.runRecoveryObserver(ctx, obj, req, runErr)
	}
	if isRetryableTransportError(runErr) {
		return s.recordRecovery(ctx, obj, role+" provider transport failure: "+compactESMError(runErr), obj.RemainingWork)
	}
	return obj, runErr
}

func (s *Supervisor) runRecoveryObserver(ctx context.Context, obj *Objective, req RoleRequest, interruption error) (*Objective, error) {
	observer := req
	observer.Role = RoleRecovery
	baseRunID := strings.TrimSuffix(req.RunID, "-"+string(req.Role))
	observer.RunID = baseRunID + "-recovery-observer"
	observer.MaxIterations = 40
	observer.Tools = []string{"read", "grep", "find", "ls"}
	observer.Prompt = RecoveryObserverTaskPrompt(obj, string(req.Role), compactESMError(interruption))
	result, err := s.Adapter.RunRecoveryObserver(ctx, observer, interruption)
	if result.DurationMS == 0 {
		result.DurationMS = 0
	}
	if accounted, accountErr := s.account(ctx, obj, result); accountErr != nil {
		return obj, accountErr
	} else if accounted != nil && accounted.Status == StatusBudgetLimited {
		return accounted, nil
	}
	if err != nil {
		return s.recordRecovery(ctx, obj, string(req.Role)+" observer failed: "+compactESMError(err), obj.RemainingWork)
	}
	if result.ToolCalls == 0 || len(result.ToolError) >= result.ToolCalls {
		return s.recordRecovery(ctx, obj, string(req.Role)+" observer inspection was not usable", obj.RemainingWork)
	}
	report, err := ParseRecoveryReport(result.Response)
	if err != nil {
		return s.recordRecovery(ctx, obj, string(req.Role)+" observer report invalid: "+compactESMError(err), obj.RemainingWork)
	}
	next, err := s.Store.RecordRecovery(ctx, obj.SessionID, string(req.Role)+" timed out: "+compactESMError(interruption), "Recovery observer: "+report.Summary, report.RemainingWork)
	if err != nil || next.Status == StatusPaused {
		return next, err
	}
	if report.Decision == RecoveryDecisionBlocked {
		return s.Store.UpdateFromModelForRun(ctx, obj.SessionID, StatusBlocked, FormatItemDetail("recovery observer blockers", report.Blockers), req.RunID+"-recovery")
	}
	return next, nil
}

func (s *Supervisor) recordRecovery(ctx context.Context, obj *Objective, reason string, remaining []string) (*Objective, error) {
	return s.Store.RecordRecovery(ctx, obj.SessionID, reason, "A fresh worker will retry from the persisted repository state.", remaining)
}

func (s *Supervisor) publish(ctx context.Context, event RuntimeEvent) error {
	if s.Events == nil {
		return nil
	}
	return s.Events.PublishESMEvent(ctx, event)
}

func rolePrompt(obj *Objective, role Role) string {
	switch role {
	case RoleWorker:
		return WorkerTaskPrompt(obj)
	case RoleCritic:
		return CriticTaskPrompt(obj)
	case RoleAudit:
		return AuditTaskPrompt(obj)
	default:
		return ""
	}
}

func isRetryableTransportError(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && strings.Contains(strings.ToLower(err.Error()), "send request:") && provider.IsRetryable(err, 0)
}

func compactESMError(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(err.Error(), "\n", "; ")
}
