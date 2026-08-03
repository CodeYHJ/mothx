package openaiapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	openaiprovider "github.com/startvibecoding/mothx/internal/provider/openai"
	"github.com/startvibecoding/mothx/internal/session"
)

func TestResponsesBackgroundText(t *testing.T) {
	text, needsContinuation, err := responsesBackgroundText([]session.ResponseItemArchive{
		{ItemID: "msg-1", SanitizedJSON: json.RawMessage(`{"type":"message","content":[{"type":"output_text","text":"hello "},{"type":"output_text","text":"world"}]}`)},
	})
	if err != nil || needsContinuation || text != "hello world" {
		t.Fatalf("text=%q needsContinuation=%v err=%v", text, needsContinuation, err)
	}

	_, needsContinuation, err = responsesBackgroundText([]session.ResponseItemArchive{
		{ItemID: "call-1", SanitizedJSON: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"write","arguments":"{}"}`)},
	})
	if err != nil || !needsContinuation {
		t.Fatalf("needsContinuation=%v err=%v, want local continuation", needsContinuation, err)
	}
}

func TestSubmitRunUsesResponsesBackgroundCoordinator(t *testing.T) {
	var mu sync.Mutex
	var postCount, getCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode background request: %v", err)
			}
			if request["background"] != true || request["stream"] != false {
				t.Fatalf("background request = %#v", request)
			}
			_, _ = w.Write([]byte(`{"id":"resp-background","status":"queued"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-background":
			getCount++
			_, _ = w.Write([]byte(`{"id":"resp-background","status":"completed","output":[{"id":"msg-background","type":"message","status":"completed","content":[{"type":"output_text","text":"background complete"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	w := submitRun(t, srv, "responses-background-session", `{"message":"run remotely"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.RunID == "" {
		t.Fatalf("accepted response = %s, err=%v", w.Body.String(), err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background session run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}

	sess, err := srv.getOrCreateSession("responses-background-session", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	messages := sess.Manager.GetMessages()
	if len(messages) != 2 || messageText(messages[0]) != "run remotely" || messageText(messages[1]) != "background complete" {
		t.Fatalf("session messages = %#v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 1 || getCount == 0 {
		t.Fatalf("POST /responses=%d GET /responses/{id}=%d", postCount, getCount)
	}
}

func TestSubmitRunContinuesResponsesBackgroundFunctionCall(t *testing.T) {
	var mu sync.Mutex
	postCount := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/responses":
			postCount++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			switch postCount {
			case 1:
				_, _ = w.Write([]byte(`{"id":"resp-tool-call","status":"completed","output":[{"id":"fc-1","type":"function_call","call_id":"call-1","name":"read","arguments":"{\"path\":\"missing.txt\"}"}]}`))
			case 2:
				if request["previous_response_id"] != "resp-tool-call" {
					t.Fatalf("previous_response_id = %#v", request["previous_response_id"])
				}
				input, ok := request["input"].([]any)
				if !ok || len(input) != 1 {
					t.Fatalf("continuation input = %#v", request["input"])
				}
				output, ok := input[0].(map[string]any)
				if !ok || output["type"] != "function_call_output" || output["call_id"] != "call-1" {
					t.Fatalf("function output = %#v", input[0])
				}
				_, _ = w.Write([]byte(`{"id":"resp-final","status":"queued"}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/responses/resp-final":
			_, _ = w.Write([]byte(`{"id":"resp-final","status":"completed","output":[{"id":"msg-final","type":"message","status":"completed","content":[{"type":"output_text","text":"continued after tool"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	srv := newTestServer(t)
	model := srv.model
	p := openaiprovider.NewProviderWithModels("test-key", upstream.URL+"/v1", []*provider.Model{model})
	p.SetUseResponsesAPI(true)
	if err := p.SetResponsesConfig(config.ResponsesConfig{Background: config.BoolPtr(true)}); err != nil {
		t.Fatalf("set responses config: %v", err)
	}
	srv.provider = p
	srv.model = model
	srv.responsesRuns = p.NewResponsesRunManager(srv.settings.GetSessionDir())
	srv.runManager = NewRunManager(srv.settings.GetSessionDir())

	w := submitRun(t, srv, "responses-background-function", `{"message":"read a file"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", w.Code, w.Body.String())
	}
	var accepted struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := session.GetSessionRun(srv.settings.GetSessionDir(), accepted.RunID)
		if err != nil {
			t.Fatalf("get session run: %v", err)
		}
		if run != nil && run.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background function run did not complete: %#v", run)
		}
		time.Sleep(20 * time.Millisecond)
	}
	sess, err := srv.getOrCreateSession("responses-background-function", srv.cfg.GetWorkDir())
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	messages := sess.Manager.GetMessages()
	if len(messages) < 4 || messageText(messages[len(messages)-1]) != "continued after tool" {
		t.Fatalf("session messages = %#v", messages)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Fatalf("POST /responses=%d, want 2", postCount)
	}
}
