// Package wechat implements the WeChat iLink Bot messaging platform adapter.
// iLink media compatibility follows the locked Tencent npm release and local
// protocol fixtures. Zero external dependencies - uses only Go standard library.
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

// UploadMediaType is the iLink getuploadurl media discriminator.
type UploadMediaType int

const (
	UploadMediaImage UploadMediaType = 1
	UploadMediaVideo UploadMediaType = 2
	UploadMediaFile  UploadMediaType = 3
	UploadMediaVoice UploadMediaType = 4
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
	VoiceItem *VoiceItem      `json:"voice_item,omitempty"`
	FileItem  *FileItem       `json:"file_item,omitempty"`
	VideoItem *VideoItem      `json:"video_item,omitempty"`
	RefMsg    *RefMessage     `json:"ref_msg,omitempty"`
}

// TextItem holds text content.
type TextItem struct {
	Text string `json:"text"`
}

// CDNMedia is the opaque WeChat CDN reference returned in a media message.
// The reference and AES key only live in the transport closure that downloads
// the content; neither is persisted as an Agent input or exposed to users.
//
// iLink has no public protocol document for these fields. Their layout follows
// the locked Tencent release and is covered by fixture tests in this package.
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
	MidSize    int64     `json:"mid_size,omitempty"`
	ThumbSize  int64     `json:"thumb_size,omitempty"`
	HDSize     int64     `json:"hd_size,omitempty"`
}

// VoiceItem describes an inbound voice message. The CDN media fields are
// intentionally opaque and are consumed only by the transport Open closure.
type VoiceItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	Duration int       `json:"duration,omitempty"`
}

// FileItem describes one inbound file. Len is supplied as a decimal string
// by observed iLink implementations and is only a hint for Runtime limits.
type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

// VideoItem describes an inbound video message.
type VideoItem struct {
	Media     *CDNMedia `json:"media,omitempty"`
	FileName  string    `json:"file_name,omitempty"`
	Duration  int       `json:"duration,omitempty"`
	VideoSize int64     `json:"video_size,omitempty"`
}

// RefMessage is the optional quoted-message envelope used by iLink. Different
// clients have emitted either one nested message_item or an item_list; both
// are accepted and normalized by the transport adapter.
type RefMessage struct {
	MessageItem *MessageItem  `json:"message_item,omitempty"`
	ItemList    []MessageItem `json:"item_list,omitempty"`
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

// GetUploadURLRequest contains the plaintext/ciphertext metadata required by
// iLink before a CDN upload. Thumbnail fields are optional when no_need_thumb
// is true, which is the behavior used by the locked 2.4.6 package.
type GetUploadURLRequest struct {
	FileKey         string          `json:"filekey,omitempty"`
	MediaType       UploadMediaType `json:"media_type,omitempty"`
	ToUserID        string          `json:"to_user_id,omitempty"`
	RawSize         int64           `json:"rawsize,omitempty"`
	RawFileMD5      string          `json:"rawfilemd5,omitempty"`
	FileSize        int64           `json:"filesize,omitempty"`
	ThumbRawSize    int64           `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string          `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int64           `json:"thumb_filesize,omitempty"`
	NoNeedThumb     bool            `json:"no_need_thumb,omitempty"`
	AESKey          string          `json:"aeskey,omitempty"`
}

// GetUploadURLResponse is the pre-signed CDN upload response.
type GetUploadURLResponse struct {
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
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
