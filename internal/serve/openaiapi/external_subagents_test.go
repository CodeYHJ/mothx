package openaiapi

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/agent"
	"golang.org/x/net/websocket"
)

func TestExternalSubAgentEventsExposeHistoryAndPublishLiveUpdates(t *testing.T) {
	srv := &Server{eventBroker: NewEventBroker(), pool: NewSessionPool(0, 0)}
	events, cancel := srv.eventBroker.Subscribe("wechat-session")
	defer cancel()

	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{
		Type: agent.EventTextDelta, AgentID: "child-1", TextDelta: "working",
	})
	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{
		Type: agent.EventToolCall, AgentID: "child-1", ToolCallID: "call-1", ToolName: "grep", ToolArgs: map[string]any{"pattern": "TODO"},
	})
	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{
		Type: agent.EventToolExecutionEnd, AgentID: "child-1", ToolCallID: "call-1", ToolName: "grep", ToolResult: "found",
	})
	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{
		Type: agent.EventDone, AgentID: "child-1",
	})

	agents, err := srv.GetSessionSubAgents("wechat-session")
	if err != nil {
		t.Fatalf("GetSessionSubAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "child-1" {
		t.Fatalf("agents = %#v", agents)
	}
	if agents[0].Status != "done" || agents[0].Active || agents[0].MessageCount != 4 {
		t.Fatalf("agent status = %#v", agents[0])
	}

	messages, err := srv.GetSessionSubAgentMessages("wechat-session", "child-1")
	if err != nil {
		t.Fatalf("GetSessionSubAgentMessages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0].Role != "assistant" || messages[0].Content != "working" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if messages[1].Role != "toolCall" || messages[1].ToolName != "grep" {
		t.Fatalf("tool call = %#v", messages[1])
	}
	if messages[2].Role != "toolResult" || messages[2].ToolName != "grep" {
		t.Fatalf("tool result = %#v", messages[2])
	}
	if messages[3].Role != "status" || messages[3].Content != "done" {
		t.Fatalf("completion = %#v", messages[3])
	}

	var gotTranscript, gotTool, gotDone bool
	for i := 0; i < 4; i++ {
		ev := <-events
		switch ev.Event {
		case "transcript":
			gotTranscript = true
			if item, ok := ev.Data.(TranscriptStreamEvent); ok && item.Type == "subagent_status" {
				gotDone = true
			}
		case "tool_event":
			gotTool = true
		}
	}
	if !gotTranscript || !gotTool || !gotDone {
		t.Fatalf("live events transcript=%v tool=%v done=%v", gotTranscript, gotTool, gotDone)
	}

	if _, err := srv.GetSessionSubAgentMessages("wechat-session", "unknown"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown agent error = %v, want ErrSessionNotFound", err)
	}
}

func TestExternalSubAgentEventsReachWebSocketClient(t *testing.T) {
	srv := NewExternalSubAgentServer()
	defer srv.pool.Stop()
	wsServer := httptest.NewServer(srv.RunWebSocketHandler())
	defer wsServer.Close()

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	ws, err := websocket.Dial(wsURL, "", wsServer.URL)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer ws.Close()

	if err := websocket.JSON.Send(ws, map[string]any{"type": "hello", "clientId": "external-subagent-test"}); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	var ready map[string]any
	if err := websocket.JSON.Receive(ws, &ready); err != nil {
		t.Fatalf("receive ready: %v", err)
	}
	if ready["type"] != "ready" {
		t.Fatalf("ready message = %#v", ready)
	}
	if err := websocket.JSON.Send(ws, map[string]any{
		"type":          "subscribe",
		"subscriptions": []map[string]any{{"sessionId": "wechat-session"}},
	}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	var subscribed map[string]any
	if err := websocket.JSON.Receive(ws, &subscribed); err != nil {
		t.Fatalf("receive subscribed: %v", err)
	}
	if subscribed["type"] != "subscribed" || subscribed["sessionId"] != "wechat-session" {
		t.Fatalf("subscribed message = %#v", subscribed)
	}

	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{Type: agent.EventTextDelta, AgentID: "child-1", TextDelta: "live"})
	srv.PublishExternalSubAgentEvent("wechat-session", agent.Event{Type: agent.EventDone, AgentID: "child-1"})

	var gotText, gotDone bool
	for !(gotText && gotDone) {
		var event struct {
			Type      string `json:"type"`
			SessionID string `json:"sessionId"`
			Event     string `json:"event"`
			Data      struct {
				Type    string `json:"type"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"data"`
		}
		if err := ws.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set websocket deadline: %v", err)
		}
		if err := websocket.JSON.Receive(ws, &event); err != nil {
			t.Fatalf("receive live event: %v (text=%v done=%v)", err, gotText, gotDone)
		}
		if event.Type != "session_event" || event.SessionID != "wechat-session" || event.Event != "transcript" {
			continue
		}
		if event.Data.Type == "assistant_delta" && event.Data.Message.Content == "live" {
			gotText = true
		}
		if event.Data.Type == "subagent_status" && event.Data.Message.Content == "done" {
			gotDone = true
		}
	}
}
