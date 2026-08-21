package feishu

import (
	"context"
	"errors"
	"testing"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/startvibecoding/mothx/internal/messaging"
)

func feishuString(value string) *string {
	return &value
}

func newFeishuMessageEvent(messageType, content, chatID, userID string) *larkim.P2MessageReceiveV1 {
	return &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{OpenId: feishuString(userID)},
			},
			Message: &larkim.EventMessage{
				MessageId:   feishuString("om_test_message"),
				MessageType: feishuString(messageType),
				Content:     feishuString(content),
				ChatId:      feishuString(chatID),
			},
		},
	}
}

func TestOnMessageMapsTextEventToInboundMessage(t *testing.T) {
	bot := NewBot(BotOptions{AppID: "test-app", AppSecret: "test-secret"})
	received := make(chan messaging.InboundMessage, 1)
	bot.mu.Lock()
	bot.handler = func(_ context.Context, msg messaging.InboundMessage) (string, error) {
		received <- msg
		return "", nil
	}
	bot.mu.Unlock()

	if err := bot.onMessage(context.Background(), newFeishuMessageEvent(
		"text", `{"text":"hello Feishu"}`, "oc_test_chat", "ou_test_user",
	)); err != nil {
		t.Fatalf("onMessage returned error: %v", err)
	}

	select {
	case msg := <-received:
		if msg.Platform != "feishu" {
			t.Fatalf("platform = %q, want feishu", msg.Platform)
		}
		if msg.ChatID != "oc_test_chat" || msg.UserID != "ou_test_user" || msg.Text != "hello Feishu" {
			t.Fatalf("unexpected inbound message: %#v", msg)
		}
		if msg.ProgressFunc == nil {
			t.Fatal("expected ProgressFunc to be installed before handler invocation")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for text message handler")
	}
}

func TestOnMessageFiltersInvalidEvents(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*larkim.P2MessageReceiveV1)
	}{
		{
			name: "nil message",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message = nil
			},
		},
		{
			name: "nil sender",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Sender = nil
			},
		},
		{
			name: "missing message type",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message.MessageType = nil
			},
		},
		{
			name: "non text message",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message.MessageType = feishuString("image")
			},
		},
		{
			name: "nil content",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message.Content = nil
			},
		},
		{
			name: "empty text",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message.Content = feishuString(`{"text":""}`)
			},
		},
		{
			name: "invalid content JSON",
			mutate: func(event *larkim.P2MessageReceiveV1) {
				event.Event.Message.Content = feishuString("not-json")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			bot := NewBot(BotOptions{AppID: "test-app", AppSecret: "test-secret"})
			bot.mu.Lock()
			bot.handler = func(context.Context, messaging.InboundMessage) (string, error) {
				called <- struct{}{}
				return "", nil
			}
			bot.mu.Unlock()

			event := newFeishuMessageEvent("text", `{"text":"should be filtered"}`, "oc_test_chat", "ou_test_user")
			tt.mutate(event)
			if err := bot.onMessage(context.Background(), event); err != nil {
				t.Fatalf("onMessage returned error: %v", err)
			}

			select {
			case <-called:
				t.Fatal("filtered event invoked the message handler")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestOnMessageIgnoresNilEvent(t *testing.T) {
	bot := NewBot(BotOptions{AppID: "test-app", AppSecret: "test-secret"})
	bot.mu.Lock()
	bot.handler = func(context.Context, messaging.InboundMessage) (string, error) {
		t.Fatal("nil event invoked the message handler")
		return "", nil
	}
	bot.mu.Unlock()

	if err := bot.onMessage(context.Background(), nil); err != nil {
		t.Fatalf("onMessage returned error: %v", err)
	}
}

func TestOnMessageWithoutHandlerIsIgnored(t *testing.T) {
	bot := NewBot(BotOptions{AppID: "test-app", AppSecret: "test-secret"})
	if err := bot.onMessage(context.Background(), newFeishuMessageEvent(
		"text", `{"text":"no handler"}`, "oc_test_chat", "ou_test_user",
	)); err != nil {
		t.Fatalf("onMessage returned error: %v", err)
	}
}

func TestReadyAndStopWithoutNetwork(t *testing.T) {
	bot := NewBot(BotOptions{AppID: "test-app", AppSecret: "test-secret"})
	if bot.Name() != "feishu" {
		t.Fatalf("name = %q, want feishu", bot.Name())
	}
	if bot.IsConnected() {
		t.Fatal("new bot should not be connected")
	}
	ready := bot.Ready()
	if ready == nil || ready != bot.Ready() {
		t.Fatal("Ready should return the bot's stable readiness channel")
	}

	bot.signalReady(nil)
	bot.signalReady(errors.New("second result must be dropped"))
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("first ready result = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ready result")
	}
	select {
	case err := <-ready:
		t.Fatalf("unexpected second ready result: %v", err)
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := make(chan bool, 2)
	bot.SetStatusCallback(func(connected bool) { status <- connected })
	bot.mu.Lock()
	bot.connected = true
	bot.cancel = cancel
	bot.mu.Unlock()

	if err := bot.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the local context")
	}
	if bot.IsConnected() {
		t.Fatal("bot should be disconnected after Stop")
	}
	select {
	case connected := <-status:
		if connected {
			t.Fatal("Stop status callback reported connected=true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Stop status callback")
	}

	if err := bot.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	select {
	case connected := <-status:
		if connected {
			t.Fatal("second Stop status callback reported connected=true")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second Stop status callback")
	}
}
