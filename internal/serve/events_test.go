package serve

import "testing"

func TestManagementEventsRetainStructuredData(t *testing.T) {
	hub := newLogHub()
	ch, history, unsubscribe := hub.subscribe()
	defer unsubscribe()
	hub.publish(serveLogEvent{Type: "binding_changed", Data: map[string]any{
		"channelType": "wechat", "fromSessionId": "old", "toSessionId": "new",
	}})
	if len(history) != 0 {
		t.Fatalf("unexpected history before publish: %#v", history)
	}
	ev := <-ch
	if ev.Type != "binding_changed" {
		t.Fatalf("event type = %q", ev.Type)
	}
	data, ok := ev.Data.(map[string]any)
	if !ok || data["toSessionId"] != "new" {
		t.Fatalf("event data = %#v", ev.Data)
	}
	_, history, unsubscribe2 := hub.subscribe()
	defer unsubscribe2()
	if len(history) != 1 || history[0].Type != "binding_changed" {
		t.Fatalf("event was not retained in history: %#v", history)
	}
}
