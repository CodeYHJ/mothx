package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	appversion "github.com/startvibecoding/mothx/internal/version"
)

const (
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"
	CDNBaseURL     = "https://novac2c.cdn.weixin.qq.com/c2c"
	iLinkAppID     = "bot"

	maxAPIResponseBytes        = 1 << 20
	defaultLongPollTimeout     = 35 * time.Second
	defaultAPITimeout          = 15 * time.Second
	defaultNotificationTimeout = 10 * time.Second

	// fallbackILinkClientVersion is the encoded form of 0.1.0. It is only
	// used by source builds that do not carry a semver build version; iLink
	// rejects an empty or zero client-version header.
	fallbackILinkClientVersion uint32 = 0x00000100
)

// Client wraps HTTP calls to the iLink API.
type Client struct {
	HTTP *http.Client
}

// NewClient creates a protocol client.
func NewClient() *Client {
	return &Client{
		// Every CGI call supplies a context deadline. A client-wide deadline
		// would override the server-provided getupdates long-poll timeout.
		HTTP: &http.Client{},
	}
}

// CommonHeaders returns headers for iLink API requests.
func CommonHeaders() http.Header {
	h := http.Header{}
	h.Set("iLink-App-Id", iLinkAppID)
	h.Set("iLink-App-ClientVersion", strconv.FormatUint(uint64(iLinkClientVersion()), 10))
	return h
}

// AuthHeaders returns the standard iLink POST headers.
func AuthHeaders(token string) http.Header {
	h := CommonHeaders()
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("Authorization", "Bearer "+token)
	h.Set("X-WECHAT-UIN", randomWechatUIN())
	return h
}

func randomWechatUIN() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fall back to a time-based value if the system RNG is unavailable.
		binary.BigEndian.PutUint32(buf[:], uint32(time.Now().UnixNano()))
	}
	val := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(val), 10)))
}

func baseInfo() map[string]string {
	return map[string]string{
		"channel_version": channelVersion(),
		"bot_agent":       botAgent(),
	}
}

func channelVersion() string {
	if version := strings.TrimSpace(appversion.Current()); version != "" && version != "unknown" {
		return version
	}
	return "0.1.0"
}

func botAgent() string {
	if version, ok := semverComponents(channelVersion()); ok {
		return fmt.Sprintf("MothX/%d.%d.%d", version[0], version[1], version[2])
	}
	return "MothX"
}

func iLinkClientVersion() uint32 {
	version, ok := semverComponents(channelVersion())
	if !ok {
		return fallbackILinkClientVersion
	}
	return uint32(version[0]&0xff)<<16 | uint32(version[1]&0xff)<<8 | uint32(version[2]&0xff)
}

// semverComponents extracts the three numeric components iLink encodes into
// iLink-App-ClientVersion. It accepts release strings such as v1.2.3 and
// v1.2.3-rc.1 but deliberately rejects source-build hashes.
func semverComponents(version string) ([3]int, bool) {
	var result [3]int
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if suffix := strings.IndexByte(version, '-'); suffix >= 0 {
		version = version[:suffix]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return result, false
	}
	for index, part := range parts {
		if part == "" {
			return result, false
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 0xff {
			return result, false
		}
		result[index] = value
	}
	return result, true
}

// GetQRCode requests a new QR code for login.
func (c *Client) GetQRCode(ctx context.Context, baseURL string) (*QRCodeResponse, error) {
	u, err := endpointURL(baseURL, "/ilink/bot/get_bot_qrcode?bot_type=3")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("get_bot_qrcode request: %w", err)
	}
	for k, v := range CommonHeaders() {
		req.Header[k] = v
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get_bot_qrcode: %w", err)
	}
	defer resp.Body.Close()
	var result QRCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("get_bot_qrcode decode: %w", err)
	}
	return &result, nil
}

// PollQRStatus polls the QR code scan status.
func (c *Client) PollQRStatus(ctx context.Context, baseURL, qrcode string) (*QRStatusResponse, error) {
	u, err := endpointURL(baseURL, "/ilink/bot/get_qrcode_status?qrcode="+url.QueryEscape(qrcode))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("get_qrcode_status request: %w", err)
	}
	for k, v := range CommonHeaders() {
		req.Header[k] = v
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result QRStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("get_qrcode_status decode: %w", err)
	}
	return &result, nil
}

// apiPost sends a POST to the iLink API and parses the response.
func (c *Client) apiPost(ctx context.Context, baseURL, endpoint, token string, body interface{}, timeout time.Duration) (json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("apiPost marshal %s: %w", endpoint, err)
	}
	u, err := endpointURL(baseURL, endpoint)
	if err != nil {
		return nil, err
	}
	httpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", endpoint, err)
	}
	for k, v := range AuthHeaders(token) {
		req.Header[k] = v
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", endpoint, err)
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{Message: string(raw), HTTPStatus: resp.StatusCode}
	}

	var check struct {
		Ret     int    `json:"ret"`
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &check); err != nil {
		return nil, fmt.Errorf("%s decode response: %w", endpoint, err)
	}
	if check.Ret != 0 || check.ErrCode != 0 {
		code := check.ErrCode
		if code == 0 {
			code = check.Ret
		}
		msg := check.ErrMsg
		if msg == "" {
			msg = fmt.Sprintf("ret=%d", check.Ret)
		}
		return nil, &APIError{Message: msg, HTTPStatus: resp.StatusCode, ErrCode: code}
	}

	return json.RawMessage(raw), nil
}

// GetUpdates performs a long-poll for new messages.
func (c *Client) GetUpdates(ctx context.Context, baseURL, token, cursor string) (*GetUpdatesResponse, error) {
	return c.GetUpdatesWithTimeout(ctx, baseURL, token, cursor, defaultLongPollTimeout)
}

// GetUpdatesWithTimeout performs one iLink long-poll using the current
// server-recommended timeout. A local timeout is an expected empty poll, not a
// transport failure, so callers can retry it without exponential backoff.
func (c *Client) GetUpdatesWithTimeout(ctx context.Context, baseURL, token, cursor string, timeout time.Duration) (*GetUpdatesResponse, error) {
	if timeout <= 0 {
		timeout = defaultLongPollTimeout
	}
	body := map[string]interface{}{
		"get_updates_buf": cursor,
		"base_info":       baseInfo(),
	}
	raw, err := c.apiPost(ctx, baseURL, "/ilink/bot/getupdates", token, body, timeout)
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return &GetUpdatesResponse{Ret: 0, Msgs: []json.RawMessage{}, GetUpdatesBuf: cursor}, nil
		}
		return nil, err
	}
	var result GetUpdatesResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getupdates decode response: %w", err)
	}
	return &result, nil
}

// SendMessage sends a message through the iLink API.
func (c *Client) SendMessage(ctx context.Context, baseURL, token string, msg interface{}) error {
	body := map[string]interface{}{
		"msg":       msg,
		"base_info": baseInfo(),
	}
	_, err := c.apiPost(ctx, baseURL, "/ilink/bot/sendmessage", token, body, defaultAPITimeout)
	return err
}

// GetConfig gets the typing ticket for a user.
func (c *Client) GetConfig(ctx context.Context, baseURL, token, userID, contextToken string) (*GetConfigResponse, error) {
	body := map[string]interface{}{
		"ilink_user_id": userID,
		"context_token": contextToken,
		"base_info":     baseInfo(),
	}
	raw, err := c.apiPost(ctx, baseURL, "/ilink/bot/getconfig", token, body, defaultAPITimeout)
	if err != nil {
		return nil, err
	}
	var result GetConfigResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("getconfig decode response: %w", err)
	}
	return &result, nil
}

// SendTyping sends or cancels the typing indicator.
func (c *Client) SendTyping(ctx context.Context, baseURL, token, userID, ticket string, status int) error {
	body := map[string]interface{}{
		"ilink_user_id": userID,
		"typing_ticket": ticket,
		"status":        status,
		"base_info":     baseInfo(),
	}
	_, err := c.apiPost(ctx, baseURL, "/ilink/bot/sendtyping", token, body, defaultAPITimeout)
	return err
}

// NotifyStart tells iLink that this account's receive loop is online. Tencent's
// reference channel issues this before getupdates so the upstream can reconcile
// the bot's online state after a process restart.
func (c *Client) NotifyStart(ctx context.Context, baseURL, token string) error {
	_, err := c.apiPost(ctx, baseURL, "/ilink/bot/msg/notifystart", token, map[string]interface{}{
		"base_info": baseInfo(),
	}, defaultNotificationTimeout)
	return err
}

// NotifyStop tells iLink that this account's receive loop is offline.
func (c *Client) NotifyStop(ctx context.Context, baseURL, token string) error {
	_, err := c.apiPost(ctx, baseURL, "/ilink/bot/msg/notifystop", token, map[string]interface{}{
		"base_info": baseInfo(),
	}, defaultNotificationTimeout)
	return err
}

func endpointURL(baseURL, endpoint string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		if err == nil {
			err = fmt.Errorf("missing scheme or host")
		}
		return "", fmt.Errorf("invalid iLink base URL: %w", err)
	}
	path, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid iLink endpoint: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(path.Path, "/")
	base.RawQuery = path.RawQuery
	return base.String(), nil
}

// BuildTextMessage creates a text message payload.
func BuildTextMessage(fromUserID, toUserID, contextToken, text string) map[string]interface{} {
	return map[string]interface{}{
		"from_user_id":  fromUserID,
		"to_user_id":    toUserID,
		"client_id":     newUUID(),
		"message_type":  2,
		"message_state": 2,
		"context_token": contextToken,
		"item_list": []map[string]interface{}{
			{"type": 1, "text_item": map[string]string{"text": text}},
		},
	}
}

func newUUID() string {
	var buf [16]byte
	rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
