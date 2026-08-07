package wechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/startvibecoding/mothx/internal/messaging"
)

// Bot implements messaging.Platform for WeChat via the iLink protocol.
type Bot struct {
	client         *Client
	creds          *Credentials
	credPath       string
	autoTyping     bool
	connected      bool
	stopped        bool
	mu             sync.Mutex
	cancelPoll     context.CancelFunc
	contextTokens  sync.Map // map[userID]contextToken
	pendingReplies sync.Map // map[userID]*pendingReplyState
	cursor         string
	statusCallback func(connected bool)
	ready          chan error
}

const (
	wechatMaxRepliesPerMessage = 10
	wechatMessageTextLimit     = 4000
)

type pendingReplyState struct {
	mu           sync.Mutex
	chunks       []string
	lastProgress string
}

type replySession struct {
	bot          *Bot
	userID       string
	contextToken string
	remaining    int
}

func (b *Bot) pendingReply(userID string) *pendingReplyState {
	value, _ := b.pendingReplies.LoadOrStore(userID, &pendingReplyState{})
	return value.(*pendingReplyState)
}

func (b *Bot) newReplySession(userID, contextToken string) *replySession {
	return &replySession{bot: b, userID: userID, contextToken: contextToken, remaining: wechatMaxRepliesPerMessage}
}

func (s *replySession) Send(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	chunks := chunkText(text, wechatMessageTextLimit-replyFooterLen(0))
	for i, chunk := range chunks {
		if s.remaining == 0 {
			s.queue(chunks[i:])
			return nil
		}
		if err := s.bot.sendChunk(ctx, s.userID, chunk, s.contextToken, s.remaining-1); err != nil {
			return err
		}
		s.remaining--
	}
	return nil
}

func (s *replySession) SendProgress(ctx context.Context, text string) error {
	state := s.bot.pendingReply(s.userID)
	state.mu.Lock()
	state.lastProgress = text
	state.mu.Unlock()
	return s.Send(ctx, text)
}
func (s *replySession) queue(chunks []string) {
	state := s.bot.pendingReply(s.userID)
	state.mu.Lock()
	state.chunks = append(state.chunks, chunks...)
	state.mu.Unlock()
}

func (b *Bot) sendMore(ctx context.Context, userID, contextToken string) error {
	state := b.pendingReply(userID)
	state.mu.Lock()
	chunks := append([]string(nil), state.chunks...)
	state.chunks = nil
	lastProgress := state.lastProgress
	state.lastProgress = ""
	state.mu.Unlock()

	s := b.newReplySession(userID, contextToken)
	if len(chunks) > 0 {
		for _, chunk := range chunks {
			if err := s.Send(ctx, chunk); err != nil {
				return err
			}
		}
		return nil
	}
	if lastProgress != "" {
		return s.Send(ctx, lastProgress)
	}
	return nil
}

func replyFooterLen(remaining int) int {
	if remaining == 0 {
		return len("\n\n剩余推送次数: 0次\n输入 /more 继续接收消息。")
	}
	return len(fmt.Sprintf("\n\n剩余推送次数: %d次", remaining))
}

func replyFooter(remaining int) string {
	if remaining == 0 {
		return "\n\n剩余推送次数: 0次\n输入 /more 继续接收消息。"
	}
	return fmt.Sprintf("\n\n剩余推送次数: %d次", remaining)
}

type BotOptions struct {
	CredPath   string
	AutoTyping bool
}

// NewBot creates a new WeChat bot.
func NewBot(opts BotOptions) *Bot {
	return &Bot{
		client:     NewClient(),
		credPath:   opts.CredPath,
		autoTyping: opts.AutoTyping,
		ready:      make(chan error, 1),
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

func (b *Bot) Name() string { return "wechat" }

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

// Start begins long-poll message receiving. Blocks until ctx is cancelled.
func (b *Bot) Start(ctx context.Context, handler messaging.MessageHandler) error {
	// Load credentials
	creds, err := LoadCredentials(b.credPath)
	if err != nil || creds == nil {
		if err == nil {
			err = fmt.Errorf("wechat: no credentials found at %s", b.credPath)
		}
		b.signalReady(err)
		return fmt.Errorf("wechat: no credentials found at %s", b.credPath)
	}

	b.mu.Lock()
	b.creds = creds
	b.connected = true
	b.stopped = false
	pollCtx, cancel := context.WithCancel(ctx)
	b.cancelPoll = cancel
	cb := b.statusCallback
	b.mu.Unlock()
	if cb != nil {
		cb(true)
	}
	b.signalReady(nil)

	log.Printf("[wechat] Long-poll loop started (user: %s)", creds.UserID)
	retryDelay := time.Second

	for {
		select {
		case <-pollCtx.Done():
			b.mu.Lock()
			b.connected = false
			cb := b.statusCallback
			b.mu.Unlock()
			if cb != nil {
				cb(false)
			}
			log.Printf("[wechat] Long-poll loop stopped")
			return nil
		default:
		}

		b.mu.Lock()
		currentCreds := b.creds
		b.mu.Unlock()

		updates, err := b.client.GetUpdates(pollCtx, currentCreds.BaseURL, currentCreds.Token, b.cursor)
		if err != nil {
			if pollCtx.Err() != nil {
				return nil
			}

			apiErr, isAPI := err.(*APIError)
			if isAPI && apiErr.IsSessionExpired() {
				log.Printf("[wechat] Session expired — re-login required")
				ClearCredentials(b.credPath)
				b.contextTokens = sync.Map{}
				b.cursor = ""
				// Try re-login
				newCreds, loginErr := Login(pollCtx, b.client, LoginOptions{
					CredPath: b.credPath,
					Force:    true,
				})
				if loginErr != nil {
					log.Printf("[wechat] Re-login failed: %v", loginErr)
					time.Sleep(retryDelay)
					continue
				}
				b.mu.Lock()
				b.creds = newCreds
				b.mu.Unlock()
				retryDelay = time.Second
				continue
			}

			log.Printf("[wechat] Poll error: %v", err)
			time.Sleep(retryDelay)
			if retryDelay < 10*time.Second {
				retryDelay *= 2
			}
			continue
		}

		if updates.GetUpdatesBuf != "" {
			b.cursor = updates.GetUpdatesBuf
		}
		retryDelay = time.Second

		for _, rawMsg := range updates.Msgs {
			var wire WireMessage
			if err := json.Unmarshal(rawMsg, &wire); err != nil {
				continue
			}

			// Remember context tokens
			b.rememberContext(&wire)

			// Only process user messages
			if wire.MessageType != MessageTypeUser {
				continue
			}

			text := extractText(wire.ItemList)
			if text == "" {
				continue
			}

			msg := messaging.InboundMessage{
				Platform:  "wechat",
				ChatID:    wire.FromUserID,
				UserID:    wire.FromUserID,
				MessageID: fmt.Sprintf("%d", wire.MessageID),
				Text:      text,
				Timestamp: time.UnixMilli(wire.CreateTimeMs),
			}

			// Show typing indicator
			if b.autoTyping {
				go b.sendTyping(pollCtx, wire.FromUserID)
			}

			// Handle message
			go func(m messaging.InboundMessage, ct string) {
				runCtx := context.WithoutCancel(pollCtx)
				reply := b.newReplySession(m.UserID, ct)
				if strings.TrimSpace(m.Text) == "/more" {
					if err := b.sendMore(runCtx, m.UserID, ct); err != nil {
						log.Printf("[wechat] More send error for %s: %v", m.UserID, err)
					}
					return
				}

				// Create progress buffer: max 7 progress lines per batch, reserve 3 for summary
				progressBuf := messaging.NewProgressBuffer(7, func(text string) {
					if err := reply.SendProgress(runCtx, text); err != nil {
						log.Printf("[wechat] Progress send error: %v", err)
					}
				})
				m.ProgressFunc = func(text string) {
					progressBuf.Add(text)
				}

				response, err := handler(runCtx, m)

				// Flush remaining progress lines before final summary
				progressBuf.Flush()

				if err != nil {
					log.Printf("[wechat] Handler error for %s: %v", m.UserID, err)
					response = "⚠️ Error: " + err.Error()
				}
				if response != "" {
					if sendErr := reply.Send(runCtx, response); sendErr != nil {
						log.Printf("[wechat] Send error for %s: %v", m.UserID, sendErr)
					} else {
						log.Printf("[wechat] Message sent to %s successfully (len=%d)", m.UserID, len(response))
					}
				} else {
					log.Printf("[wechat] Empty response for %s, not sending", m.UserID)
				}
				// Stop typing
				if b.autoTyping {
					b.stopTyping(runCtx, m.UserID)
				}
			}(msg, wire.ContextToken)
		}
	}
}

// Stop gracefully stops the bot.
func (b *Bot) Stop() error {
	b.mu.Lock()
	b.stopped = true
	if b.cancelPoll != nil {
		b.cancelPoll()
	}
	b.connected = false
	cb := b.statusCallback
	b.mu.Unlock()
	if cb != nil {
		cb(false)
	}
	return nil
}

// SendMessage sends a text message to a user.
func (b *Bot) SendMessage(ctx context.Context, chatID string, text string) error {
	ct, ok := b.contextTokens.Load(chatID)
	if !ok {
		return fmt.Errorf("no context_token for user %s", chatID)
	}
	return b.sendText(ctx, chatID, text, ct.(string))
}

// --- Internal ---

func (b *Bot) sendText(ctx context.Context, userID, text, contextToken string) error {
	return b.newReplySession(userID, contextToken).Send(ctx, text)
}

func (b *Bot) sendChunk(ctx context.Context, userID, text, contextToken string, remaining int) error {
	b.mu.Lock()
	creds := b.creds
	b.mu.Unlock()

	if creds == nil {
		return fmt.Errorf("not logged in")
	}

	msg := BuildTextMessage(creds.UserID, userID, contextToken, text+replyFooter(remaining))
	return b.client.SendMessage(ctx, creds.BaseURL, creds.Token, msg)
}

func (b *Bot) sendTyping(ctx context.Context, userID string) {
	ct, ok := b.contextTokens.Load(userID)
	if !ok {
		return
	}
	b.mu.Lock()
	creds := b.creds
	b.mu.Unlock()
	if creds == nil {
		return
	}
	config, err := b.client.GetConfig(ctx, creds.BaseURL, creds.Token, userID, ct.(string))
	if err != nil || config.TypingTicket == "" {
		return
	}
	b.client.SendTyping(ctx, creds.BaseURL, creds.Token, userID, config.TypingTicket, 1)
}

func (b *Bot) stopTyping(ctx context.Context, userID string) {
	ct, ok := b.contextTokens.Load(userID)
	if !ok {
		return
	}
	b.mu.Lock()
	creds := b.creds
	b.mu.Unlock()
	if creds == nil {
		return
	}
	config, err := b.client.GetConfig(ctx, creds.BaseURL, creds.Token, userID, ct.(string))
	if err != nil || config.TypingTicket == "" {
		return
	}
	b.client.SendTyping(ctx, creds.BaseURL, creds.Token, userID, config.TypingTicket, 2)
}

func (b *Bot) rememberContext(wire *WireMessage) {
	userID := wire.FromUserID
	if wire.MessageType == MessageTypeBot {
		userID = wire.ToUserID
	}
	if userID != "" && wire.ContextToken != "" {
		b.contextTokens.Store(userID, wire.ContextToken)
	}
}

func extractText(items []MessageItem) string {
	var parts []string
	for _, item := range items {
		if item.Type == ItemText && item.TextItem != nil {
			parts = append(parts, item.TextItem.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func chunkText(text string, limit int) []string {
	if limit <= 0 || len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(text) > 0 {
		if len(text) <= limit {
			chunks = append(chunks, text)
			break
		}
		cut := limit
		if idx := strings.LastIndex(text[:limit], "\n\n"); idx > limit*3/10 {
			cut = idx + 2
		} else if idx := strings.LastIndex(text[:limit], "\n"); idx > limit*3/10 {
			cut = idx + 1
		}
		for cut > 0 && cut < len(text) && (text[cut]&0xc0) == 0x80 {
			cut--
		}
		if cut == 0 {
			_, size := utf8.DecodeRuneInString(text)
			cut = size
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return chunks
}

// Ensure Bot implements messaging.Platform at compile time.
var _ messaging.Platform = (*Bot)(nil)
