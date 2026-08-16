package agentruntime

import (
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
		if policy(run) == RecoveryKeepRemote {
			result.Kept = append(result.Kept, run)
			continue
		}
		if beforeFail != nil {
			if err := beforeFail(run); err != nil {
				return result, err
			}
		}
		if err := RecoverDurableRun(sessionDir, run, RunStateFailed, "server restarted while run was active", RunEvent{
			EventType: "recovered", Source: "agentruntime", Status: "failed", Model: run.Model, Mode: run.Mode,
			Data: []byte(`{"reason":"server restarted while run was active"}`),
		}); err != nil {
			return result, fmt.Errorf("recover orphaned run %s: %w", run.ID, err)
		}
		result.Failed = append(result.Failed, run)
	}
	return result, nil
}
