// Package feishu implements the Feishu (Lark) messaging platform adapter.
// Uses the official Feishu Go SDK with WebSocket long connection for receiving messages.
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

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

// SendImage uploads and sends an image message to a Feishu chat. It is kept
// separate from the text-only Platform contract so transports without native
// media output (notably WeChat iLink) do not pretend to support it.
func (b *Bot) SendImage(ctx context.Context, chatID string, image io.Reader) error {
	key, err := b.uploadImage(ctx, image)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"image_key": key})
	return b.sendMediaMessage(ctx, chatID, "image", string(content))
}

// SendFile uploads and sends a file message to a Feishu chat.
func (b *Bot) SendFile(ctx context.Context, chatID, filename, fileType string, file io.Reader) error {
	key, err := b.uploadFile(ctx, filename, fileType, file)
	if err != nil {
		return err
	}
	content, _ := json.Marshal(map[string]string{"file_key": key})
	return b.sendMediaMessage(ctx, chatID, "file", string(content))
}

func (b *Bot) uploadImage(ctx context.Context, image io.Reader) (string, error) {
	if image == nil {
		return "", fmt.Errorf("feishu image reader is required")
	}
	upload, err := b.client.Im.Image.Create(ctx, larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().ImageType("message").Image(image).Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu upload image: %w", err)
	}
	if !upload.Success() || upload.Data == nil || upload.Data.ImageKey == nil || *upload.Data.ImageKey == "" {
		return "", fmt.Errorf("feishu upload image: code=%d msg=%s", upload.Code, upload.Msg)
	}
	return *upload.Data.ImageKey, nil
}

func (b *Bot) uploadFile(ctx context.Context, filename, fileType string, file io.Reader) (string, error) {
	if file == nil {
		return "", fmt.Errorf("feishu file reader is required")
	}
	if filename == "" {
		filename = "attachment"
	}
	if fileType == "" {
		fileType = strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	}
	upload, err := b.client.Im.File.Create(ctx, larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().FileName(filename).FileType(fileType).File(file).Build()).
		Build())
	if err != nil {
		return "", fmt.Errorf("feishu upload file: %w", err)
	}
	if !upload.Success() || upload.Data == nil || upload.Data.FileKey == nil || *upload.Data.FileKey == "" {
		return "", fmt.Errorf("feishu upload file: code=%d msg=%s", upload.Code, upload.Msg)
	}
	return *upload.Data.FileKey, nil
}

func (b *Bot) sendMediaMessage(ctx context.Context, chatID, msgType, content string) error {
	receiveIDType := "chat_id"
	if len(chatID) >= 3 && chatID[:3] == "ou_" {
		receiveIDType = "open_id"
	}
	resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(msgType).
			Content(content).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu send %s message: %w", msgType, err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu send %s message: code=%d msg=%s", msgType, resp.Code, resp.Msg)
	}
	return nil
}

// --- Event handler ---

func (b *Bot) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	b.mu.Lock()
	handler := b.handler
	b.mu.Unlock()

	if handler == nil || event == nil || event.Event == nil {
		return nil
	}

	msg := event.Event.Message
	sender := event.Event.Sender

	if msg == nil || sender == nil {
		return nil
	}

	inbound, accepted := b.inboundMessage(msg, sender)
	if !accepted {
		msgType := ""
		if msg.MessageType != nil {
			msgType = *msg.MessageType
		}
		log.Printf("[feishu] Ignoring unsupported message type: %s", msgType)
		return nil
	}
	chatID, userID := inbound.ChatID, inbound.UserID

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
			response.Text = "⚠️ Error: " + err.Error()
		}
		replyID := ""
		if msg.MessageId != nil {
			replyID = *msg.MessageId
		}
		if response.Text != "" {
			// Reply in the same chat
			if replyErr := b.replyMessage(context.Background(), replyID, chatID, response.Text); replyErr != nil {
				log.Printf("[feishu] Reply error: %v", replyErr)
			}
		}
		for _, attachment := range response.Attachments {
			if err := b.replyAttachment(context.Background(), replyID, chatID, attachment); err != nil {
				log.Printf("[feishu] Media reply error: %v", err)
				completeOutboundAttachment(attachment, "failed", "", "send_media_failed")
				_ = b.replyMessage(context.Background(), replyID, chatID, "⚠️ Unable to send generated attachment: "+attachment.Filename)
				continue
			}
			completeOutboundAttachment(attachment, "delivered", "", "")
		}
	}()

	return nil
}

func (b *Bot) inboundMessage(msg *larkim.EventMessage, sender *larkim.EventSender) (messaging.InboundMessage, bool) {
	if msg == nil || sender == nil || msg.MessageType == nil {
		return messaging.InboundMessage{}, false
	}
	messageID, chatID, userID := stringValue(msg.MessageId), stringValue(msg.ChatId), ""
	if sender.SenderId != nil {
		userID = stringValue(sender.SenderId.OpenId)
	}
	if messageID == "" || chatID == "" || userID == "" {
		return messaging.InboundMessage{}, false
	}
	inbound := messaging.InboundMessage{
		Platform: "feishu", ChatID: chatID, UserID: userID, MessageID: messageID,
		Timestamp: eventTimestamp(stringValue(msg.CreateTime)),
	}
	content := []byte(stringValue(msg.Content))
	switch *msg.MessageType {
	case "text":
		var value struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(content, &value); err != nil || value.Text == "" {
			return messaging.InboundMessage{}, false
		}
		inbound.Text = value.Text
		return inbound, true
	case "image":
		var value struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal(content, &value); err != nil || value.ImageKey == "" {
			return messaging.InboundMessage{}, false
		}
		inbound.Attachments = []messaging.PlatformAttachment{b.messageResourceAttachment(messageID, value.ImageKey, messaging.AttachmentImage, "image")}
		return inbound, true
	case "file":
		var value struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal(content, &value); err != nil || value.FileKey == "" {
			return messaging.InboundMessage{}, false
		}
		attachment := b.messageResourceAttachment(messageID, value.FileKey, messaging.AttachmentFile, "file")
		attachment.Filename = value.FileName
		inbound.Attachments = []messaging.PlatformAttachment{attachment}
		return inbound, true
	default:
		return messaging.InboundMessage{}, false
	}
}

func (b *Bot) messageResourceAttachment(messageID, key string, kind messaging.AttachmentKind, resourceType string) messaging.PlatformAttachment {
	return messaging.PlatformAttachment{
		Reference: key, Kind: kind, MessageID: messageID,
		Open: func(ctx context.Context) (messaging.AttachmentStream, error) {
			resp, err := b.client.Im.MessageResource.Get(ctx, larkim.NewGetMessageResourceReqBuilder().
				MessageId(messageID).FileKey(key).Type(resourceType).Build())
			if err != nil {
				return messaging.AttachmentStream{}, fmt.Errorf("feishu download %s: %w", resourceType, err)
			}
			if !resp.Success() || resp.File == nil {
				return messaging.AttachmentStream{}, fmt.Errorf("feishu download %s: code=%d msg=%s", resourceType, resp.Code, resp.Msg)
			}
			return messaging.AttachmentStream{Reader: io.NopCloser(resp.File), Filename: resp.FileName}, nil
		},
	}
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func eventTimestamp(raw string) time.Time {
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ms)
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

func (b *Bot) replyAttachment(ctx context.Context, messageID, chatID string, attachment messaging.OutboundAttachment) error {
	if attachment.Open == nil {
		return fmt.Errorf("attachment %s is not readable", attachment.ID)
	}
	reader, err := attachment.Open(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	var msgType, content string
	switch attachment.Kind {
	case messaging.AttachmentImage:
		key, err := b.uploadImage(ctx, reader)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]string{"image_key": key})
		msgType, content = "image", string(body)
	case messaging.AttachmentFile:
		key, err := b.uploadFile(ctx, attachment.Filename, "", reader)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]string{"file_key": key})
		msgType, content = "file", string(body)
	default:
		return fmt.Errorf("unsupported outbound attachment kind %q", attachment.Kind)
	}
	return b.replyMediaMessage(ctx, messageID, chatID, msgType, content)
}

func (b *Bot) replyMediaMessage(ctx context.Context, messageID, chatID, msgType, content string) error {
	if messageID == "" {
		return b.sendMediaMessage(ctx, chatID, msgType, content)
	}
	resp, err := b.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().MsgType(msgType).Content(content).Build()).
		Build())
	if err != nil {
		return fmt.Errorf("feishu reply %s: %w", msgType, err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu reply %s: code=%d msg=%s", msgType, resp.Code, resp.Msg)
	}
	return nil
}

func completeOutboundAttachment(attachment messaging.OutboundAttachment, status, providerMessageID, failureCode string) {
	if attachment.Complete != nil {
		attachment.Complete(context.Background(), status, providerMessageID, failureCode)
	}
}

// Ensure Bot implements messaging.Platform at compile time.
var _ messaging.Platform = (*Bot)(nil)
