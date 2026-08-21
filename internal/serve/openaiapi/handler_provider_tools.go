package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	providerfactory "github.com/startvibecoding/mothx/internal/provider/factory"
)

// providerProbeRequest is deliberately separate from config.ProviderConfig so
// WebUI drafts are never persisted as a side effect of probing them.
type providerProbeRequest struct {
	API         string            `json:"api"`
	BaseURL     string            `json:"baseUrl"`
	APIKey      string            `json:"apiKey"`
	HTTPProxy   string            `json:"httpProxy"`
	ForceHTTP11 bool              `json:"forceHTTP11"`
	Headers     map[string]string `json:"headers"`
	Model       string            `json:"model"`
}

type discoveredModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
	Input         []string `json:"input,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
}

func (s *Server) handleProviderModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var req providerProbeRequest
	if err := decodeProbeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	endpoint, err := modelsEndpoint(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	client, err := provider.NewHTTPClientWithOptions(30*time.Second, provider.HTTPClientOptions{ProxyURL: req.HTTPProxy, ForceHTTP11: req.ForceHTTP11})
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("configure HTTP client: %v", err), "invalid_request_error")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	applyProbeHeaders(request, req.API, resolveProbeSecret(req.APIKey), req.Headers)
	resp, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("fetch models: %v", err), "upstream_error")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("read models response: %v", err), "upstream_error")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not reflect an upstream response body. Providers can return
		// credentials, private diagnostics, or arbitrary HTML here; the HTTP
		// status is enough to explain a model-discovery failure to the client.
		writeError(w, http.StatusBadGateway, fmt.Sprintf("models endpoint returned HTTP %d", resp.StatusCode), "upstream_error")
		return
	}
	models, err := parseDiscoveredModels(body)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("parse models response: %v", err), "upstream_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (s *Server) handleProviderModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var req providerProbeRequest
	if err := decodeProbeRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "model is required", "invalid_request_error")
		return
	}
	providerID := "webui-probe"
	settings := &config.Settings{
		DefaultProvider: providerID,
		Providers: map[string]*config.ProviderConfig{providerID: {
			API: req.API, BaseURL: req.BaseURL, APIKey: resolveProbeSecret(req.APIKey), HTTPProxy: req.HTTPProxy,
			ForceHTTP11: req.ForceHTTP11, Headers: req.Headers,
			Models: []config.ModelConfig{{ID: req.Model, Name: req.Model, Input: []string{"text"}}},
		}},
	}
	p, _, err := providerfactory.Create(settings, providerID, req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("create provider: %v", err), "invalid_request_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	events := p.Chat(ctx, provider.ChatParams{
		ModelID: req.Model, ThinkingLevel: provider.ThinkingOff, MaxTokens: 1,
		Messages: []provider.Message{{Role: "user", Content: "ping"}},
	})
	for event := range events {
		if event.Type == provider.StreamError {
			message := "model request failed"
			if event.Error != nil {
				message = event.Error.Error()
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": message})
			return
		}
		if event.Type == provider.StreamDone {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": req.Model})
			return
		}
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "model request ended without a completion"})
}

func decodeProbeRequest(r *http.Request, dst *providerProbeRequest) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	dst.API = strings.TrimSpace(dst.API)
	dst.BaseURL = strings.TrimSpace(dst.BaseURL)
	if dst.API == "" || dst.BaseURL == "" {
		return fmt.Errorf("api and baseUrl are required")
	}
	return nil
}

func modelsEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("baseUrl must be an absolute http(s) URL")
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/models") {
		path += "/models"
	}
	u.Path = path
	return u.String(), nil
}

func resolveProbeSecret(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return os.Getenv(value[2 : len(value)-1])
	}
	return value
}

func applyProbeHeaders(req *http.Request, api, apiKey string, headers map[string]string) {
	api = strings.ToLower(strings.TrimSpace(api))
	switch {
	case strings.HasPrefix(api, "anthropic"):
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case strings.HasPrefix(api, "google"):
		if strings.HasPrefix(apiKey, "ya29.") || strings.HasPrefix(apiKey, "gya29.") {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		} else {
			req.Header.Set("x-goog-api-key", apiKey)
		}
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")
	provider.ApplyHeaders(req, headers)
}

func parseDiscoveredModels(body []byte) ([]discoveredModel, error) {
	var envelope struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		var raw []json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, err
		}
		envelope.Data = raw
	}
	items := envelope.Data
	if len(items) == 0 {
		items = envelope.Models
	}
	result := make([]discoveredModel, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		var item struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			DisplayName   string   `json:"displayName"`
			ContextWindow int      `json:"contextWindow"`
			ContextLength int      `json:"context_length"`
			MaxTokens     int      `json:"maxTokens"`
			MaxOutput     int      `json:"max_output_tokens"`
			MaxTokensAlt  int      `json:"max_tokens"`
			Input         []string `json:"input"`
			InputAlt      []string `json:"input_modalities"`
			Reasoning     bool     `json:"reasoning"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		id := normalizeDiscoveredModelID(item.ID)
		if id == "" {
			id = normalizeDiscoveredModelID(item.Name)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(item.Name)
		if name == "" || normalizeDiscoveredModelID(name) == id {
			name = strings.TrimSpace(item.DisplayName)
		}
		if name == "" {
			name = id
		}
		input := append([]string(nil), item.Input...)
		if len(input) == 0 {
			input = append([]string(nil), item.InputAlt...)
		}
		if len(input) == 0 {
			input = []string{"text"}
		}
		contextWindow := item.ContextWindow
		if contextWindow == 0 {
			contextWindow = item.ContextLength
		}
		maxTokens := item.MaxTokens
		if maxTokens == 0 {
			maxTokens = item.MaxOutput
		}
		if maxTokens == 0 {
			maxTokens = item.MaxTokensAlt
		}
		result = append(result, discoveredModel{ID: id, Name: name, ContextWindow: contextWindow, MaxTokens: maxTokens, Input: input, Reasoning: item.Reasoning})
	}
	return result, nil
}

func normalizeDiscoveredModelID(value string) string {
	value = strings.TrimSpace(value)
	if marker := strings.LastIndex(value, "/models/"); marker >= 0 {
		value = value[marker+len("/models/"):]
	} else {
		value = strings.TrimPrefix(value, "models/")
	}
	return strings.TrimSpace(value)
}
