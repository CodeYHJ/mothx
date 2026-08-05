// Package feishu implements the Feishu (Lark) messaging platform adapter.
// Uses the official Feishu Go SDK with WebSocket long connection for receiving messages.
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/startvibecoding/mothx/internal/messaging"
)

// Bot implements messaging.Platform for Feishu via official SDK WebSocket.
type Bot struct {
	appID     string
	appSecret string
	client    *lark.Client
	wsClient  *larkws.Client
	handler   messaging.MessageHandler
	connected bool
	mu        sync.Mutex
	cancel    context.CancelFunc

	statusCallback func(connected bool)
	ready          chan error
}

// BotOptions configures a Feishu Bot.
type BotOptions struct {
	AppID     string
	AppSecret string
}

// NewBot creates a new Feishu bot.
func NewBot(opts BotOptions) *Bot {
	client := lark.NewClient(opts.AppID, opts.AppSecret)
	return &Bot{
		appID:     opts.AppID,
		appSecret: opts.AppSecret,
		client:    client,
		ready:     make(chan error, 1),
	}
}

// Ready returns the one-shot startup result for the current Bot instance.
func (b *Bot) Ready() <-chan error { return b.ready }

func (b *Bot) signalReady(err error) {
	select {
	case b.ready <- err:
	default:
	}
}

// --- messaging.Platform implementation ---

func (b *Bot) Name() string { return "feishu" }

func (b *Bot) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

func (b *Bot) SetStatusCallback(callback func(connected bool)) {
	b.mu.Lock()
	b.statusCallback = callback
	b.mu.Unlock()
}

// Start begins receiving messages via WebSocket long connection.
func (b *Bot) Start(ctx context.Context, handler messaging.MessageHandler) error {
	b.mu.Lock()
	b.handler = handler
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.mu.Unlock()

	// Create event dispatcher
	eventDispatcher := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(b.onMessage).
		// Feishu sends read receipts for messages sent by the bot when this
		// event is subscribed. They do not affect channel conversations, but
		// must still be acknowledged so the SDK does not report an unknown
		// handler for every receipt.
		OnP2MessageReadV1(func(context.Context, *larkim.P2MessageReadV1) error { return nil })

	// Create WebSocket client
	wsClient := larkws.NewClient(b.appID, b.appSecret,
		larkws.WithEventHandler(eventDispatcher),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	b.mu.Lock()
	b.wsClient = wsClient
	b.connected = true
	cb := b.statusCallback
	b.mu.Unlock()
	if cb != nil {
		cb(true)
	}

	log.Printf("[feishu] WebSocket long connection started")

	// A config reload can stop the bot while its startup goroutine is still
	// being scheduled. Avoid entering the SDK with an already-cancelled
	// context; its reconnect loop would log a misleading endpoint failure.
	if ctx.Err() != nil {
		wsClient.Close()
		b.signalReady(ctx.Err())
		return nil
	}
	b.signalReady(nil)

	// Start blocks until connection drops or context cancelled
	err := wsClient.Start(ctx)

	b.mu.Lock()
	b.connected = false
	cb = b.statusCallback
	b.mu.Unlock()
	if cb != nil {
		cb(false)
	}

	if ctx.Err() != nil {
		return nil // normal shutdown
	}
	return err
}

// Stop gracefully shuts down the bot.
func (b *Bot) Stop() error {
	b.mu.Lock()
	wsClient := b.wsClient
	cancel := b.cancel
	b.cancel = nil
	b.wsClient = nil
	b.connected = false
	cb := b.statusCallback
	b.mu.Unlock()

	// Close first: the SDK disables auto-reconnect in Close. Cancelling the
	// request context first makes its reconnect loop call the endpoint with an
	// already-cancelled context and log a spurious connection failure.
	if wsClient != nil {
		wsClient.Close()
	}
	if cancel != nil {
		cancel()
	}
	if cb != nil {
		cb(false)
	}
	return nil
}

// SendMessage sends a text message to a chat.
func (b *Bot) SendMessage(ctx context.Context, chatID string, text string) error {
	content, _ := json.Marshal(map[string]string{"text": text})
	receiveIDType := "chat_id"
	// Older bindings stored a Feishu open_id (ou_...) before channel sessions
	// were routed by chat_id. Keep those direct sends working while new
	// bindings use chat_id (oc_...). This is still a create-message call, not a
	// reply-to-message call, so it does not require a reply/message ID.
	if len(chatID) >= 3 && chatID[:3] == "ou_" {
		receiveIDType = "open_id"
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()

	resp, err := b.client.Im.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu send message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// --- Event handler ---

func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	b.mu.Lock()
	handler := b.handler
	b.mu.Unlock()

	if handler == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	// Only handle text messages
	if msg == nil || sender == nil {
		return nil
	}

	msgType := ""
	if msg.MessageType != nil {
		msgType = *msg.MessageType
	}
	if msgType != "text" {
		log.Printf("[feishu] Ignoring non-text message type: %s", msgType)
		return nil
	}

	// Parse text content
	var textContent struct {
		Text string `json:"text"`
	}
	if msg.Content != nil {
		json.Unmarshal([]byte(*msg.Content), &textContent)
	}
	if textContent.Text == "" {
		return nil
	}

	// Extract user info
	userID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		userID = *sender.SenderId.OpenId
	}

	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}

	inbound := messaging.InboundMessage{
		Platform: "feishu",
		ChatID:   chatID,
		UserID:   userID,
		Text:     textContent.Text,
	}

	// Handle message asynchronously
	go func() {
		// Create progress buffer: max 7 progress lines per batch, reserve 3 for summary
		progressBuf := messaging.NewProgressBuffer(7, func(text string) {
			if err := b.SendMessage(context.Background(), chatID, text); err != nil {
				log.Printf("[feishu] Progress send error: %v", err)
			}
		})
		inbound.ProgressFunc = func(text string) {
			progressBuf.Add(text)
		}

		response, err := handler(context.Background(), inbound)

		// Flush remaining progress lines before final summary
		progressBuf.Flush()

		if err != nil {
			log.Printf("[feishu] Handler error for %s: %v", userID, err)
			response = "⚠️ Error: " + err.Error()
		}
		if response != "" {
			// Reply in the same chat
			replyID := ""
			if msg.MessageId != nil {
				replyID = *msg.MessageId
			}
			if replyErr := b.replyMessage(context.Background(), replyID, chatID, response); replyErr != nil {
				log.Printf("[feishu] Reply error: %v", replyErr)
			}
		}
	}()

	return nil
}

// replyMessage replies to a message or sends to chat.
func (b *Bot) replyMessage(ctx context.Context, messageID, chatID, text string) error {
	content, _ := json.Marshal(map[string]string{"text": text})

	if messageID != "" {
		// Reply to specific message
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(messageID).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("text").
				Content(string(content)).
				Build()).
			Build()

		resp, err := b.client.Im.Message.Reply(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	// Send to chat directly
	return b.SendMessage(ctx, chatID, text)
}

// Ensure Bot implements messaging.Platform at compile time.
var _ messaging.Platform = (*Bot)(nil)
