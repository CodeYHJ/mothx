package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestResponsesRunManagerStartGetAndCancel(t *testing.T) {
	postCount := 0
	cancelReceived := false

	sessionDir := t.TempDir()
	manager := session.New(t.TempDir(), sessionDir)
	if err := manager.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	p := NewProviderWithModels("test-key", "https://api.test/v1", []*provider.Model{{ID: "mock"}})
	p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var response string
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode background request: %v", err)
			}
			if body["background"] != true {
				t.Fatalf("background request = %#v, want true", body)
			}
			if body["stream"] != false {
				t.Fatalf("background request stream = %#v, want false", body["stream"])
			}
			response = `{"id":"resp-1","status":"queued"}`
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-1":
			response = `{"id":"resp-1","status":"completed"}`
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses/resp-1/cancel":
			cancelReceived = true
			response = `{"id":"resp-1","status":"cancelling"}`
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(response)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	runs := p.NewResponsesRunManager(sessionDir)

	run, err := runs.Start(context.Background(), manager.GetHeader().ID, "turn-1", provider.ChatParams{
		ModelID:  "mock",
		Messages: []provider.Message{provider.NewUserMessage("run in background")},
		Abort:    make(chan struct{}),
	})
	if err != nil {
		t.Fatalf("start background run: %v", err)
	}
	if run == nil || run.ResponseID != "resp-1" || run.State != "queued" {
		t.Fatalf("started run = %#v", run)
	}
	got, err := runs.Get(context.Background(), manager.GetHeader().ID, run.LocalRunID)
	if err != nil {
		t.Fatalf("get background run: %v", err)
	}
	if got == nil || got.State != "completed" {
		t.Fatalf("queried run = %#v", got)
	}

	run, err = runs.Start(context.Background(), manager.GetHeader().ID, "turn-2", provider.ChatParams{
		ModelID:  "mock",
		Messages: []provider.Message{provider.NewUserMessage("cancel me")},
		Abort:    make(chan struct{}),
	})
	if err != nil {
		t.Fatalf("start second background run: %v", err)
	}
	if err := runs.Cancel(context.Background(), manager.GetHeader().ID, run.LocalRunID); err != nil {
		t.Fatalf("cancel background run: %v", err)
	}
	cancelled, err := session.GetResponseRun(sessionDir, manager.GetHeader().ID, run.LocalRunID)
	if err != nil {
		t.Fatalf("read cancelled run: %v", err)
	}
	if cancelled == nil || cancelled.State != "cancelling" || !cancelled.CancelRequested {
		t.Fatalf("cancelled run = %#v", cancelled)
	}
	if postCount != 2 || !cancelReceived {
		t.Fatalf("postCount=%d cancelReceived=%v", postCount, cancelReceived)
	}
}

func TestArchiveBackgroundResponsePreservesUsageAndAttachments(t *testing.T) {
	sessionDir := t.TempDir()
	response := &responsesCompletedObject{
		ID:     "resp-archive",
		Status: "completed",
		Usage:  &responsesUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		Output: []json.RawMessage{
			json.RawMessage(`{"id":"msg-archive","type":"message","content":[{"type":"output_text","annotations":[{"type":"url_citation","title":"OpenAI","url":"https://openai.com"}]}]}`),
		},
	}
	run := session.ResponseRun{SessionID: "session-archive", LocalRunID: "run-archive", LocalTurnID: "turn-archive", Provider: "openai", API: "openai-responses"}
	if err := archiveBackgroundResponse(sessionDir, run, response); err != nil {
		t.Fatalf("archive background response: %v", err)
	}
	turn, err := session.GetResponseTurn(sessionDir, run.SessionID, run.LocalTurnID)
	if err != nil || turn == nil {
		t.Fatalf("get archived response turn: turn=%#v err=%v", turn, err)
	}
	var summary struct {
		Usage       *provider.Usage       `json:"usage"`
		Attachments []provider.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal(turn.ResponseSummary, &summary); err != nil {
		t.Fatalf("decode response summary: %v", err)
	}
	if summary.Usage == nil || summary.Usage.TotalTokens != 18 {
		t.Fatalf("summary usage = %#v", summary.Usage)
	}
	if len(summary.Attachments) != 1 || summary.Attachments[0].Kind != "citation" || summary.Attachments[0].URL != "https://openai.com" {
		t.Fatalf("summary attachments = %#v", summary.Attachments)
	}
}
