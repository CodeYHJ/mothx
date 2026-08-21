package openaiapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleProviderModelsProxiesCredentialsAndNormalizesResponse(t *testing.T) {
	type upstreamRequest struct {
		method        string
		path          string
		rawQuery      string
		authorization string
		tenant        string
		accept        string
	}
	requests := make(chan upstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- upstreamRequest{
			method:        r.Method,
			path:          r.URL.Path,
			rawQuery:      r.URL.RawQuery,
			authorization: r.Header.Get("Authorization"),
			tenant:        r.Header.Get("X-Tenant"),
			accept:        r.Header.Get("Accept"),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-test","name":"GPT Test","context_length":8192,"max_output_tokens":1024,"input_modalities":["text","image"],"reasoning":true}]}`)
	}))
	defer upstream.Close()

	body, err := json.Marshal(providerProbeRequest{
		API:     "openai-chat",
		BaseURL: upstream.URL + "/v1?region=cn",
		APIKey:  "probe-secret",
		Headers: map[string]string{"X-Tenant": "workspace-a"},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	mux := http.NewServeMux()
	registerRoutes(mux, &Server{}, RunOptions{DisableAPI: true})
	req := httptest.NewRequest(http.MethodPost, "/api/provider/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("provider models status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var response struct {
		Object string            `json:"object"`
		Data   []discoveredModel `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Object != "list" || len(response.Data) != 1 {
		t.Fatalf("unexpected model list: %#v", response)
	}
	model := response.Data[0]
	if model.ID != "gpt-test" || model.Name != "GPT Test" || model.ContextWindow != 8192 || model.MaxTokens != 1024 || !model.Reasoning {
		t.Fatalf("unexpected normalized model: %#v", model)
	}
	if len(model.Input) != 2 || model.Input[0] != "text" || model.Input[1] != "image" {
		t.Fatalf("model input = %#v, want text/image", model.Input)
	}

	upstreamReq := <-requests
	if upstreamReq.method != http.MethodGet || upstreamReq.path != "/v1/models" || upstreamReq.rawQuery != "region=cn" {
		t.Fatalf("upstream request = %s %s?%s", upstreamReq.method, upstreamReq.path, upstreamReq.rawQuery)
	}
	if upstreamReq.authorization != "Bearer probe-secret" || upstreamReq.tenant != "workspace-a" || upstreamReq.accept != "application/json" {
		t.Fatalf("unexpected upstream headers: %#v", upstreamReq)
	}
}

func TestHandleProviderModelsSanitizesUpstreamFailure(t *testing.T) {
	const upstreamSecret = "upstream-private-diagnostic"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, upstreamSecret, http.StatusTooManyRequests)
	}))
	defer upstream.Close()

	body, err := json.Marshal(providerProbeRequest{
		API:     "openai-chat",
		BaseURL: upstream.URL + "/v1",
		APIKey:  "probe-secret",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/provider/models", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handleProviderModels(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("provider models status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), upstreamSecret) {
		t.Fatalf("response leaked upstream body: %s", rec.Body.String())
	}
	var response ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error.Type != "upstream_error" || response.Error.Message == "" {
		t.Fatalf("unexpected structured error: %#v", response.Error)
	}
}

func TestHandleProviderModelTestCompletesOpenAIStreamingProbe(t *testing.T) {
	type upstreamRequest struct {
		method        string
		path          string
		authorization string
		customHeader  string
		body          map[string]any
	}
	requests := make(chan upstreamRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- upstreamRequest{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			customHeader:  r.Header.Get("X-Probe"),
			body:          body,
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	body, err := json.Marshal(providerProbeRequest{
		API:     "openai-chat",
		BaseURL: upstream.URL + "/v1",
		APIKey:  "probe-secret",
		Headers: map[string]string{"X-Probe": "enabled"},
		Model:   "gpt-probe",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/provider/test", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	(&Server{}).handleProviderModelTest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("provider test status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		OK    bool   `json:"ok"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.Model != "gpt-probe" {
		t.Fatalf("unexpected probe response: %#v", response)
	}

	upstreamReq := <-requests
	if upstreamReq.method != http.MethodPost || upstreamReq.path != "/v1/chat/completions" {
		t.Fatalf("upstream request = %s %s", upstreamReq.method, upstreamReq.path)
	}
	if upstreamReq.authorization != "Bearer probe-secret" || upstreamReq.customHeader != "enabled" {
		t.Fatalf("unexpected upstream headers: %#v", upstreamReq)
	}
	if upstreamReq.body["model"] != "gpt-probe" || upstreamReq.body["stream"] != true || upstreamReq.body["max_tokens"] != float64(1) {
		t.Fatalf("unexpected probe request body: %#v", upstreamReq.body)
	}
	messages, ok := upstreamReq.body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("unexpected probe messages: %#v", upstreamReq.body["messages"])
	}
	message, ok := messages[0].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != "ping" {
		t.Fatalf("unexpected probe message: %#v", messages[0])
	}
}

func TestModelsEndpoint(t *testing.T) {
	tests := []struct {
		base string
		want string
	}{
		{"https://api.example.test/v1", "https://api.example.test/v1/models"},
		{"https://api.example.test/v1/", "https://api.example.test/v1/models"},
		{"https://api.example.test/v1/models", "https://api.example.test/v1/models"},
	}
	for _, tt := range tests {
		got, err := modelsEndpoint(tt.base)
		if err != nil {
			t.Errorf("modelsEndpoint(%q): %v", tt.base, err)
			continue
		}
		if got != tt.want {
			t.Errorf("modelsEndpoint(%q) = %q, want %q", tt.base, got, tt.want)
		}
	}
	for _, base := range []string{"", "ftp://example.test/v1", "/relative"} {
		if _, err := modelsEndpoint(base); err == nil {
			t.Errorf("modelsEndpoint(%q) succeeded, want error", base)
		}
	}
}

func TestParseDiscoveredModels(t *testing.T) {
	got, err := parseDiscoveredModels([]byte(`{"models":[{"name":"models/gemini-2.0-flash","displayName":"Gemini 2.0 Flash"},{"name":"models/gemini-2.0-flash"},{"id":"gpt-4o","context_length":128000,"max_output_tokens":4096,"input_modalities":["text","image"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2", len(got))
	}
	if got[0].ID != "gemini-2.0-flash" || got[0].Name != "Gemini 2.0 Flash" {
		t.Fatalf("unexpected Gemini model: %#v", got[0])
	}
	if got[1].ID != "gpt-4o" || got[1].ContextWindow != 128000 || got[1].MaxTokens != 4096 || len(got[1].Input) != 2 {
		t.Fatalf("unexpected OpenAI model: %#v", got[1])
	}
}
