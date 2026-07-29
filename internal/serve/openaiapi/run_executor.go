package openaiapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/session"
)

// RunExecutor owns the lifecycle of a single agent execution.
// It consumes agent events, normalizes them, persists to SQLite,
// and publishes via EventBroker. It is independent of HTTP/SSE/WebSocket.
type RunExecutor struct {
	broker  *EventBroker
	store   RunStore
	run     *session.SessionRun
	server  *Server
	once    sync.Once
	done    chan struct{}
}

// RunStore is the persistence interface for run events.
type RunStore interface {
	SaveSessionRun(sessionDir string, run session.SessionRun) error
	UpdateSessionRunStatus(sessionDir, runID, status, message string, finishedAt *time.Time) error
	GetSessionRun(sessionDir, runID string) (*session.SessionRun, error)
	GetActiveSessionRun(sessionDir, sessionID string) (*session.SessionRun, error)
}

// NewRunExecutor creates a new RunExecutor for the given run.
func NewRunExecutor(srv *Server, broker *EventBroker, run *session.SessionRun) *RunExecutor {
	return &RunExecutor{
		broker: broker,
		server: srv,
		run:    run,
		done:   make(chan struct{}),
	}
}

// Execute consumes agent events from the channel and processes them.
// It runs in the calling goroutine (blocking) and closes the done channel
// when the agent finishes or errors.
// The caller is responsible for creating the agent and starting RunWithUserMessage.
func (e *RunExecutor) Execute(ctx context.Context, sess *APISession, a *agent.Agent, eventCh <-chan agent.Event, modelID, mode string, transcript bool) (*RunResult, error) {
	if e == nil {
		return nil, fmt.Errorf("run executor is nil")
	}
	defer close(e.done)

	result := &RunResult{
		RunID:       e.run.ID,
		SessionID:   e.run.SessionID,
		Status:      "completed",
		ToolCalls:   []XToolCall{},
		ModelID:     modelID,
		StartTime:   time.Now(),
	}

	toolMode := ""
	toolDetail := ""
	if e.server != nil && e.server.cfg != nil {
		toolMode = e.server.cfg.ToolVisibility.Mode
		toolDetail = e.server.cfg.GetToolDetail()
	}

	pendingTools := make(map[string]*toolCallInfo)
	var totalUsage CompletionUsage

	for ev := range eventCh {
		select {
		case <-ctx.Done():
			result.Status = "canceled"
			result.Error = ctx.Err().Error()
			return result, nil
		default:
		}

		switch ev.Type {
		case agent.EventTextDelta:
			if e.server != nil {
				evt := TranscriptStreamEvent{
					Type: "message",
					Message: &SessionMessageEntry{
						AgentID: string(ev.AgentID),
						Role:    "assistant",
						Content: ev.TextDelta,
					},
				}
				if transcript {
					e.server.publishTranscriptEvent(sess.ID, evt)
				} else {
					// Always publish to the EventBroker for SSE subscribers.
					if broker := e.broker; broker != nil {
						runID := ""
						if e.run != nil {
							runID = e.run.ID
						}
						broker.PublishTranscriptEvent(sess.ID, runID, evt)
					}
				}
			}

		case agent.EventToolCall:
			name, callID := resolveToolEvent(ev)
			tc := &toolCallInfo{Name: name, Args: ev.ToolArgs, Status: "running"}
			if callID != "" {
				pendingTools[callID] = tc
			}
			result.ToolCalls = append(result.ToolCalls, XToolCall{Name: name, Args: ev.ToolArgs, Status: "running"})
			if e.server != nil {
				e.server.publishToolEvent(sess.ID, ToolStatusEvent{
					Tool: name, ToolCallID: callID, AgentID: string(ev.AgentID),
					Status: "running", Args: ev.ToolArgs,
				})
			}

		case agent.EventToolExecutionEnd:
			status := "completed"
			if ev.ToolError != nil {
				status = "failed"
			}
			for i := len(result.ToolCalls) - 1; i >= 0; i-- {
				if result.ToolCalls[i].Name == ev.ToolName && result.ToolCalls[i].Status == "running" {
					result.ToolCalls[i].Status = status
					break
				}
			}
			tc := pendingTools[ev.ToolCallID]
			if tc == nil {
				tc = &toolCallInfo{Name: ev.ToolName, Args: ev.ToolArgs}
			}
			tc.Status = status
			tc.Result = ev.ToolResult
			tc.Diff = ev.ToolDiff
			tc.Error = ev.ToolError
			delete(pendingTools, ev.ToolCallID)
			name := ev.ToolName
			if name == "" {
				name = tc.Name
			}
			_ = toolMode
			_ = toolDetail
			if e.server != nil {
				e.server.publishToolEvent(sess.ID, ToolStatusEvent{
					Tool: name, ToolCallID: ev.ToolCallID, AgentID: string(ev.AgentID),
					Status: status, Args: tc.Args, Summary: summarizeToolStatusResult(ev.ToolResult),
					IsError: ev.ToolError != nil, HasDetail: ev.ToolCallID != "",
				})
			}

		case agent.EventToolApprovalRequest:
			if e.server != nil {
				e.server.registerSessionApproval(sess, a, ev)
			}

		case agent.EventUsage:
			if ev.Usage != nil {
				totalUsage.PromptTokens += ev.Usage.TotalInputTokens()
				totalUsage.CompletionTokens += ev.Usage.Output
				totalUsage.CacheReadTokens += ev.Usage.CacheRead
				totalUsage.CacheWriteTokens += ev.Usage.CacheWrite
				totalUsage.TotalTokens = totalUsage.PromptTokens + totalUsage.CompletionTokens
			}

		case agent.EventRetry:
			// Retry events are informational; no action needed in executor.

		case agent.EventDone:
			if ev.AgentID != "" {
				continue // sub-agent done, not the main run
			}
			result.Usage = &totalUsage
			result.Status = "completed"
			return result, nil

		case agent.EventError:
			if ev.AgentID != "" {
				continue // sub-agent error, not the main run
			}
			result.Usage = &totalUsage
			if ev.Error != nil {
				if errors.Is(ev.Error, context.Canceled) || errors.Is(ev.Error, context.DeadlineExceeded) {
					result.Status = "canceled"
				} else {
					result.Status = "failed"
				}
				result.Error = ev.Error.Error()
			}
			return result, nil
		}
	}
	// Channel closed without EventDone — treat as completed.
	result.Usage = &totalUsage
	return result, nil
}

// Done returns a channel that is closed when the run finishes.
func (e *RunExecutor) Done() <-chan struct{} {
	if e == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return e.done
}

// Finalize is called exactly once to clean up the run after execution.
// It is idempotent via sync.Once.
func (e *RunExecutor) Finalize(sess *APISession, result *RunResult) {
	if e == nil {
		return
	}
	e.once.Do(func() {
		if e.server != nil && sess != nil {
			// Broadcast final runtime snapshot
			e.server.publishSessionRuntime(sess)
			e.server.publishSessionStreamDone(sess.ID, e.run.ID, result.Status)
		}
	})
}

// RunResult captures the outcome of a single run execution.
type RunResult struct {
	RunID     string
	SessionID string
	Status    string           // "completed", "failed", "canceled"
	Error     string           // non-empty if failed/canceled
	Usage     *CompletionUsage // final token usage
	ToolCalls []XToolCall      // tool calls made during the run
	ModelID   string
	StartTime time.Time
}

// toolCallInfo is defined in tool_format.go.

// resolveToolEvent is defined in handler_chat.go.