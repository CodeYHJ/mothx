package openaiapi

import (
	"strings"

	"github.com/startvibecoding/mothx/internal/agentruntime"
)

func webUIActiveRunState(status string) agentruntime.RunState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "created", "queued":
		return agentruntime.RunStateCreated
	case "waiting_for_approval":
		return agentruntime.RunStateWaitingApproval
	case "waiting_for_question":
		return agentruntime.RunStateWaitingQuestion
	case "cancelling":
		return agentruntime.RunStateCancelling
	default:
		return agentruntime.RunStateRunning
	}
}

func webUIRunState(status, message string) agentruntime.RunState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed":
		return agentruntime.RunStateCompleted
	case "incomplete":
		return agentruntime.RunStateIncomplete
	case "canceled", "cancelled":
		if strings.Contains(strings.ToLower(message), "deadline") {
			return agentruntime.RunStateTimedOut
		}
		return agentruntime.RunStateCancelled
	default:
		return agentruntime.RunStateFailed
	}
}
