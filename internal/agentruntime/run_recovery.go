package agentruntime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/startvibecoding/mothx/internal/session"
)

type RecoveryAction string

const (
	RecoveryFailLocal  RecoveryAction = "fail_local"
	RecoveryKeepRemote RecoveryAction = "keep_remote"
)

type RunRecoveryPolicy func(session.SessionRun) RecoveryAction

type RunRecoveryResult struct {
	Failed []session.SessionRun
	Kept   []session.SessionRun
}

// DefaultRunRecoveryPolicy preserves remotely resumable Responses runs and
// fails local Agent loops, which cannot survive process termination.
func DefaultRunRecoveryPolicy(run session.SessionRun) RecoveryAction {
	if strings.EqualFold(strings.TrimSpace(run.Source), "responses_background") {
		return RecoveryKeepRemote
	}
	return RecoveryFailLocal
}

// RecoverOrphanedRuns applies one shared startup policy to all durable runs.
// beforeFail may persist adapter-compatible decision cleanup before the run is
// marked failed.
func RecoverOrphanedRuns(sessionDir string, policy RunRecoveryPolicy, beforeFail func(session.SessionRun) error) (RunRecoveryResult, error) {
	var result RunRecoveryResult
	if policy == nil {
		policy = DefaultRunRecoveryPolicy
	}
	orphans, err := session.ListOrphanedSessionRuns(sessionDir)
	if err != nil {
		return result, fmt.Errorf("list orphaned runs: %w", err)
	}
	for _, run := range orphans {
		action, err := recoverOrphanedRun(sessionDir, run, policy, beforeFail, "server restarted while run was active")
		if err != nil {
			return result, err
		}
		switch action {
		case RecoveryKeepRemote:
			result.Kept = append(result.Kept, run)
		case RecoveryFailLocal:
			result.Failed = append(result.Failed, run)
		}
	}
	return result, nil
}

// RecoverOrphanedSessionRun reconciles the one active Run for a session before
// a new local execution is admitted. Callers must already own that session's
// runtime lease and must not have an in-memory execution for it. Those
// preconditions make an active local row an orphan rather than a concurrent
// execution. Remotely resumable Runs are retained according to policy.
func RecoverOrphanedSessionRun(sessionDir, sessionID string, policy RunRecoveryPolicy, beforeFail func(session.SessionRun) error) (RunRecoveryResult, error) {
	var result RunRecoveryResult
	if policy == nil {
		policy = DefaultRunRecoveryPolicy
	}
	run, err := session.GetActiveSessionRun(sessionDir, sessionID)
	if err != nil {
		return result, fmt.Errorf("get active session run: %w", err)
	}
	if run == nil {
		return result, nil
	}
	action, err := recoverOrphanedRun(sessionDir, *run, policy, beforeFail, "run remained active when the session became available for a new local execution")
	if err != nil {
		return result, err
	}
	if action == RecoveryKeepRemote {
		result.Kept = append(result.Kept, *run)
	} else {
		result.Failed = append(result.Failed, *run)
	}
	return result, nil
}

func recoverOrphanedRun(sessionDir string, run session.SessionRun, policy RunRecoveryPolicy, beforeFail func(session.SessionRun) error, reason string) (RecoveryAction, error) {
	if policy(run) == RecoveryKeepRemote {
		return RecoveryKeepRemote, nil
	}
	if beforeFail != nil {
		if err := beforeFail(run); err != nil {
			return "", err
		}
	}
	data, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return "", fmt.Errorf("marshal recovery reason: %w", err)
	}
	if err := RecoverDurableRun(sessionDir, run, RunStateFailed, reason, RunEvent{
		EventType: "recovered", Source: "agentruntime", Status: "failed", Model: run.Model, Mode: run.Mode, Data: data,
	}); err != nil {
		return "", fmt.Errorf("recover orphaned run %s: %w", run.ID, err)
	}
	return RecoveryFailLocal, nil
}
