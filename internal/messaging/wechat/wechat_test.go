package wechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/startvibecoding/mothx/internal/messaging"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestChunkTextKeepsUTF8Boundaries(t *testing.T) {
	chunks := chunkText("你好世界", 6)
	if got, want := len(chunks), 2; got != want {
		t.Fatalf("chunk count = %d, want %d: %#v", got, want, chunks)
	}
	if strings.Join(chunks, "") != "你好世界" {
		t.Fatalf("chunks lost text: %#v", chunks)
	}
	for _, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("invalid UTF-8 chunk: %q", chunk)
		}
	}
}

func TestReplySessionQueuesAfterTenMessages(t *testing.T) {
	var sent []string
	bot := NewBot(BotOptions{})
	bot.creds = &Credentials{UserID: "bot", BaseURL: "https://example.test", Token: "token"}
	bot.client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		sent = append(sent, string(body))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ret":0}`)), Header: make(http.Header), Request: r}, nil
	})}

	s := bot.newReplySession("user", "context")
	if err := s.Send(context.Background(), strings.Repeat("x", (wechatMessageTextLimit-replyFooterLen(0))*(wechatMaxRepliesPerMessage+1))); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := len(sent); got != wechatMaxRepliesPerMessage {
		t.Fatalf("sent messages = %d, want %d", got, wechatMaxRepliesPerMessage)
	}
	if !strings.Contains(sent[len(sent)-1], "剩余推送次数: 0次") {
		t.Fatalf("last message missing exhausted footer: %s", sent[len(sent)-1])
	}
	state := bot.pendingReply("user")
	state.mu.Lock()
	queued := len(state.chunks)
	state.mu.Unlock()
	if queued == 0 {
		t.Fatal("expected queued remainder")
	}
}

func TestInboundAttachmentsDownloadAndDecryptMedia(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := []byte("iLink attachment bytes")
	ciphertext, err := EncryptAESECB(plaintext, key)
	if err != nil {
		t.Fatalf("EncryptAESECB: %v", err)
	}

	bot := NewBot(BotOptions{})
	bot.client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if got := r.URL.Query().Get("encrypted_query_param"); got != "opaque-ref" {
			t.Fatalf("encrypted_query_param = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(ciphertext)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	// This is the locked wire fixture derived from independent open-source
	// iLink clients. It verifies the actual JSON names rather than relying on
	// a Go struct constructed directly by the test.
	fixture := fmt.Sprintf(`{
"message_id":42,"message_type":1,"item_list":[
  {"type":2,"image_item":{"media":{"encrypt_query_param":"opaque-ref","aes_key":"not-the-direct-key"},"aeskey":%q}},
  {"type":4,"file_item":{"media":{"encrypt_query_param":"opaque-ref","aes_key":%q},"file_name":"notes.txt","len":"23"}}
]}`, EncodeAESKeyHex(key), EncodeAESKeyBase64(key))
	var wire WireMessage
	if err := json.Unmarshal([]byte(fixture), &wire); err != nil {
		t.Fatalf("unmarshal iLink media fixture: %v", err)
	}

	attachments := bot.inboundAttachments(&wire)
	if got, want := len(attachments), 2; got != want {
		t.Fatalf("attachment count = %d, want %d", got, want)
	}
	if attachments[0].Kind != messaging.AttachmentImage || attachments[1].Kind != messaging.AttachmentFile {
		t.Fatalf("attachment kinds = %#v", []messaging.AttachmentKind{attachments[0].Kind, attachments[1].Kind})
	}
	if attachments[1].Filename != "notes.txt" || attachments[1].SizeHint != 23 {
		t.Fatalf("file metadata = %#v", attachments[1])
	}
	for _, attachment := range attachments {
		if strings.Contains(attachment.Reference, "opaque-ref") || strings.Contains(attachment.Reference, EncodeAESKeyHex(key)) {
			t.Fatalf("transport secret leaked into reference: %q", attachment.Reference)
		}
		stream, err := attachment.Open(context.Background())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		data, readErr := io.ReadAll(stream.Reader)
		closeErr := stream.Reader.Close()
		if readErr != nil {
			t.Fatalf("ReadAll: %v", readErr)
		}
		if closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
		if !bytes.Equal(data, plaintext) {
			t.Fatalf("decrypted media = %q, want %q", data, plaintext)
		}
	}
}

func TestAESECBDecryptReaderRejectsTruncatedCiphertext(t *testing.T) {
	key, err := GenerateAESKey()
	if err != nil {
		t.Fatalf("GenerateAESKey: %v", err)
	}
	reader, err := newAESECBDecryptReadCloser(io.NopCloser(strings.NewReader("truncated")), key)
	if err != nil {
		t.Fatalf("newAESECBDecryptReadCloser: %v", err)
	}
	defer reader.Close()
	if _, err := io.ReadAll(reader); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ReadAll error = %v, want ciphertext block error", err)
	}
}

var _ messaging.Platform = (*Bot)(nil)
