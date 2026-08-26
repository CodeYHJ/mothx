// Package wechat implements the WeChat iLink Bot messaging platform adapter.
// iLink media compatibility is cross-checked against independent open-source
// clients because no official Bot media protocol document is available.
// Zero external dependencies — uses only Go standard library.
package wechat

import (
	"encoding/json"
	"fmt"
	"time"
)

// --- Message types from iLink protocol ---

// MessageType indicates who sent the message.
type MessageType int

const (
	MessageTypeUser MessageType = 1
	MessageTypeBot  MessageType = 2
)

// MessageItemType indicates the content type.
type MessageItemType int

const (
	ItemText  MessageItemType = 1
	ItemImage MessageItemType = 2
	ItemVoice MessageItemType = 3
	ItemFile  MessageItemType = 4
	ItemVideo MessageItemType = 5
)

// --- Wire types (raw JSON from iLink API) ---

// WireMessage is the raw message from the iLink API.
type WireMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id"`
	ClientID     string        `json:"client_id"`
	CreateTimeMs int64         `json:"create_time_ms"`
	MessageType  MessageType   `json:"message_type"`
	ContextToken string        `json:"context_token"`
	ItemList     []MessageItem `json:"item_list"`
}

// MessageItem is a single content item within a message.
type MessageItem struct {
	Type      MessageItemType `json:"type"`
	TextItem  *TextItem       `json:"text_item,omitempty"`
	ImageItem *ImageItem      `json:"image_item,omitempty"`
	FileItem  *FileItem       `json:"file_item,omitempty"`
}

// TextItem holds text content.
type TextItem struct {
	Text string `json:"text"`
}

// CDNMedia is the opaque WeChat CDN reference returned in a media message.
// The reference and AES key only live in the transport closure that downloads
// the content; neither is persisted as an Agent input or exposed to users.
//
// iLink has no public protocol document for these fields. Their layout is
// cross-checked against independent open-source iLink clients and covered by
// fixture tests in this package.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

// ImageItem describes one inbound image. aeskey is a direct hexadecimal
// AES-128 key that takes precedence over media.aes_key when present.
type ImageItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	ThumbMedia *CDNMedia `json:"thumb_media,omitempty"`
	AESKey     string    `json:"aeskey,omitempty"`
	URL        string    `json:"url,omitempty"`
}

// FileItem describes one inbound file. Len is supplied as a decimal string
// by observed iLink implementations and is only a hint for Runtime limits.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// --- API response types ---

// QRCodeResponse from get_bot_qrcode.
type QRCodeResponse struct {
	QRCode       string `json:"qrcode"`
	QRCodeImgURL string `json:"qrcode_img_content"`
}

// QRStatusResponse from get_qrcode_status.
type QRStatusResponse struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token,omitempty"`
	BotID        string `json:"ilink_bot_id,omitempty"`
	UserID       string `json:"ilink_user_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	RedirectHost string `json:"redirect_host,omitempty"`
}

// GetUpdatesResponse from getupdates.
type GetUpdatesResponse struct {
	Ret                  int               `json:"ret"`
	Msgs                 []json.RawMessage `json:"msgs"`
	GetUpdatesBuf        string            `json:"get_updates_buf"`
	LongPollingTimeoutMS int64             `json:"longpolling_timeout_ms,omitempty"`
	ErrCode              int               `json:"errcode,omitempty"`
	ErrMsg               string            `json:"errmsg,omitempty"`
}

// GetConfigResponse from getconfig.
type GetConfigResponse struct {
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// Credentials holds login credentials.
type Credentials struct {
	Token         string `json:"token"`
	BaseURL       string `json:"baseUrl"`
	AccountID     string `json:"accountId"`
	UserID        string `json:"userId"`
	GetUpdatesBuf string `json:"getUpdatesBuf,omitempty"`
	SavedAt       string `json:"savedAt,omitempty"`
}

// IncomingMessage is a parsed incoming user message.
type IncomingMessage struct {
	UserID       string
	Text         string
	Timestamp    time.Time
	ContextToken string
}

// APIError is returned when the iLink API returns a non-zero ret or HTTP error.
type APIError struct {
	Message    string
	HTTPStatus int
	ErrCode    int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ilink api: %s (http=%d, errcode=%d)", e.Message, e.HTTPStatus, e.ErrCode)
}

// IsSessionExpired returns true if this error indicates session timeout.
func (e *APIError) IsSessionExpired() bool {
	return e.ErrCode == -14
}
