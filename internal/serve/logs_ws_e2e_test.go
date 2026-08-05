package serve

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestLogsWebSocketPublishesManagementEventEndToEnd(t *testing.T) {
	rt := &channelRuntime{cfg: DefaultConfig(), logHub: newLogHub(), sessionDir: t.TempDir()}
	mux := http.NewServeMux()
	mux.Handle("/ws/logs", rt.handleLogs(nil))
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/logs"
	ws, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.SetReadDeadline(time.Now().Add(time.Second))
	var connected serveLogEvent
	if err := websocket.JSON.Receive(ws, &connected); err != nil {
		t.Fatal(err)
	}
	if connected.Type != "connected" {
		t.Fatalf("first event = %#v", connected)
	}
	rt.publishManagementEvent("binding_changed", map[string]any{
		"channelType": "wechat", "channelId": "user-e2e", "toSessionId": "session-e2e",
	})
	var event serveLogEvent
	if err := websocket.JSON.Receive(ws, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "binding_changed" {
		t.Fatalf("event type = %q", event.Type)
	}
	data, ok := event.Data.(map[string]any)
	if !ok || data["toSessionId"] != "session-e2e" {
		t.Fatalf("event data = %#v", event.Data)
	}
	_ = ws.Close()
	rt.publishManagementEvent("session_deleted", map[string]any{"sessionId": "session-e2e"})
	ws2, err := websocket.Dial(wsURL, "", "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer ws2.Close()
	_ = ws2.SetReadDeadline(time.Now().Add(time.Second))
	var reconnect serveLogEvent
	if err := websocket.JSON.Receive(ws2, &reconnect); err != nil {
		t.Fatal(err)
	}
	if reconnect.Type != "connected" {
		t.Fatalf("reconnect first event = %#v", reconnect)
	}
	foundDelete := false
	for i := 0; i < 3; i++ {
		var replay serveLogEvent
		if err := websocket.JSON.Receive(ws2, &replay); err != nil {
			t.Fatal(err)
		}
		if replay.Type == "session_deleted" {
			foundDelete = true
			break
		}
	}
	if !foundDelete {
		t.Fatal("reconnected WebSocket did not replay retained management event")
	}
}
