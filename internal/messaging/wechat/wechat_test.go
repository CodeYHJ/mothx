package wechat

import (
	"context"
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

var _ messaging.Platform = (*Bot)(nil)
