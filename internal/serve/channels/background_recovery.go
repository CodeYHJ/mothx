package channels

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

// reconcileCompletedBackgroundRun delivers a completed channel background
// result after a dispatcher restart. Delivery state lives in run events, while
// the message body remains in the canonical session transcript.
func (d *Dispatcher) reconcileCompletedBackgroundRun(sess *ChannelSession, progress func(string)) {
	if d == nil || sess == nil || progress == nil || sess.Manager == nil {
		return
	}
	header := sess.Manager.GetHeader()
	if header == nil || header.ID == "" {
		return
	}
	events, err := session.ListSessionRunEvents(d.sessionDir, header.ID)
	if err != nil {
		return
	}
	type pendingDelivery struct {
		runID          string
		assistantEntry string
		progress       []string
	}
	pending := make(map[string]pendingDelivery)
	progressByRun := make(map[string][]string)
	delivered := make(map[string]struct{})
	for _, event := range events {
		var data struct {
			Pending        bool   `json:"channelDeliveryPending"`
			AssistantEntry string `json:"assistantEntryId"`
		}
		_ = json.Unmarshal(event.Data, &data)
		switch event.EventType {
		case "tool_progress":
			if strings.HasPrefix(strings.ToLower(event.Source), "channel:") {
				var progress struct {
					Tool    string `json:"tool"`
					Status  string `json:"status"`
					Summary string `json:"summary"`
				}
				if json.Unmarshal(event.Data, &progress) == nil {
					if strings.TrimSpace(progress.Tool) == "" && strings.TrimSpace(progress.Status) == "" {
						continue
					}
					line := strings.TrimSpace(fmt.Sprintf("Tool %s %s", progress.Tool, progress.Status))
					if strings.TrimSpace(progress.Summary) != "" && progress.Summary != "(empty result)" {
						line += ": " + progress.Summary
					}
					if line != "" {
						progressByRun[event.RunID] = append(progressByRun[event.RunID], line)
					}
				}
			}
		case "finished":
			if data.Pending && strings.HasPrefix(strings.ToLower(event.Source), "channel:") {
				pending[event.RunID] = pendingDelivery{runID: event.RunID, assistantEntry: data.AssistantEntry, progress: progressByRun[event.RunID]}
			}
		case "channel_delivery_reconciled":
			delivered[event.RunID] = struct{}{}
		}
	}
	for runID := range delivered {
		delete(pending, runID)
	}
	if len(pending) == 0 {
		return
	}
	messages, err := session.ListSessionMessagesAfter(d.sessionDir, header.ID, 0, 500)
	if err != nil {
		return
	}
	for runID, item := range pending {
		var message *provider.Message
		for _, candidate := range messages {
			if item.assistantEntry != "" && candidate.EntryID == item.assistantEntry {
				value := candidate.Message
				message = &value
				break
			}
		}
		if message == nil {
			continue
		}
		for _, line := range item.progress {
			progress(line)
		}
		text := strings.TrimSpace(message.Content)
		if text == "" {
			for _, block := range message.Contents {
				if block.Type == "text" {
					text += block.Text
				}
			}
			text = strings.TrimSpace(text)
		}
		if summary := FormatAttachmentSummary(message.Attachments); summary != "" {
			if text != "" {
				text += "\n\n"
			}
			text += summary
		}
		if text != "" {
			progress(text)
		}
		_, _ = session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
			SessionID: header.ID, RunID: runID, EventType: "channel_delivery_reconciled",
			Source: "channel", Status: "delivered", Data: json.RawMessage(`{"reason":"dispatcher_restart"}`),
		})
	}
}
