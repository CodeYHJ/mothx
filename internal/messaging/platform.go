// Package messaging defines the messaging platform abstraction for serve channels.
// Each platform (WeChat, Feishu, etc.) implements the Platform interface.
package messaging

import (
	"context"
	"io"
	"time"
)

// Platform defines the interface that all messaging platform adapters must implement.
type Platform interface {
	// Name returns the platform identifier (e.g. "wechat", "feishu").
	Name() string
	// Start begins receiving messages. Blocks until ctx is cancelled or Stop is called.
	Start(ctx context.Context, handler MessageHandler) error
	// Stop gracefully shuts down the platform connection.
	Stop() error
	// SendMessage sends a text message to a specific chat.
	SendMessage(ctx context.Context, chatID string, text string) error
	// IsConnected reports whether the platform is currently connected.
	IsConnected() bool
}

// Readiness is implemented by transports that can distinguish successful
// startup from the long-running receive loop. Serve uses it during hot
// replacement so a failed candidate never takes down the healthy instance.
// Implementations that do not expose readiness retain the legacy immediate
// promotion behavior.
type Readiness interface {
	Ready() <-chan error
}

// MessageHandler is called for each incoming message. Its result is a
// platform-neutral delivery projection: transports render the text and, when
// their capability permits, execute its opaque media operations.
type MessageHandler func(ctx context.Context, msg InboundMessage) (MessageResponse, error)

// MessageResponse is a transport projection of one canonical Agent result.
// Text remains available to every platform; Attachments carry no local path or
// provider reference and can only be opened through Runtime-supplied closures.
type MessageResponse struct {
	Text        string
	Attachments []OutboundAttachment
}

// OutboundAttachment is an already-authorized media delivery operation. The
// platform calls Open only while delivering and reports the terminal outcome
// through Complete so the Runtime-owned delivery record stays canonical.
type OutboundAttachment struct {
	ID        string
	Kind      AttachmentKind
	Filename  string
	MediaType string
	Open      func(context.Context) (io.ReadCloser, error)
	Complete  func(ctx context.Context, status, providerMessageID, failureCode string)
}

// StatusCallbackSetter is an optional interface platforms can implement to receive
// connection status change notifications.
type StatusCallbackSetter interface {
	SetStatusCallback(callback func(connected bool))
}

// InboundMessage represents a message received from a messaging platform.
type InboundMessage struct {
	Platform string // "wechat", "feishu", etc.
	ChatID   string // Conversation/chat identifier
	UserID   string // Sender user ID
	// MessageID is an optional provider-native event/message identifier. It is
	// used only for durable background idempotency when a platform supplies one.
	MessageID string
	UserName  string    // Sender display name
	Text      string    // Message text content
	Timestamp time.Time // When the message was sent
	// Attachments contains opaque, platform-authenticated media references.
	// The channel dispatcher turns these into Runtime-owned attachments before
	// any Agent is constructed. Transport adapters must not put a public URL or
	// credential in Reference.
	Attachments []PlatformAttachment

	// ProgressFunc is called to send intermediate progress updates during agent execution.
	// If nil, no progress updates are sent.
	ProgressFunc func(text string)
}

// AttachmentKind identifies the media class exposed by a transport. It stays
// transport-neutral and is converted to agentruntime.AttachmentKind only at
// the shared Runtime boundary.
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

// AttachmentStream is a one-shot authenticated download supplied by a
// transport adapter. The Runtime owns copying it into its private session
// store and closes Reader after use.
type AttachmentStream struct {
	Reader      io.ReadCloser
	Filename    string
	MediaType   string
	ContentSize int64
}

// PlatformAttachment is an inbound transport reference, not a persisted
// attachment or provider input. Open must authenticate against the platform's
// own API and may only use the opaque Reference from the event that created
// this value.
type PlatformAttachment struct {
	Reference string
	Kind      AttachmentKind
	Filename  string
	MediaType string
	SizeHint  int64
	MessageID string
	Open      func(context.Context) (AttachmentStream, error)
}
