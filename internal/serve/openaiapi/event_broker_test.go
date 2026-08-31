package openaiapi

import (
	"testing"
	"time"
)

func TestEventBrokerSlowSubscriberRequestsReconnect(t *testing.T) {
	broker := NewEventBroker()
	events, resync, cancel := broker.SubscribeWithResync("session-1")
	defer cancel()

	for i := 0; i < cap(events)+1; i++ {
		broker.PublishWithResync(BrokerEvent{SessionID: "session-1", Stream: "run", Event: "run_event"})
	}
	select {
	case <-resync:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber did not receive a reconnect signal")
	}
	for range events {
	}
}

func TestEventBrokerUnsubscribeDoesNotRequestReconnect(t *testing.T) {
	broker := NewEventBroker()
	_, resync, cancel := broker.SubscribeWithResync("session-1")
	cancel()
	select {
	case <-resync:
		t.Fatal("ordinary unsubscribe unexpectedly requested reconnect")
	case <-time.After(20 * time.Millisecond):
	}
}
