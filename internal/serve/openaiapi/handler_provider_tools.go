package openaiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	endpoint, err := provider.ModelsEndpoint(req.BaseURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	client, err := provider.NewHTTPClientWithOptions(30*time.Second, provider.HTTPClientOptions{ProxyURL: req.HTTPProxy, ForceHTTP11: req.ForceHTTP11})
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("configure HTTP client: %v", err), "invalid_request_error")
		return
	}
	models, err := provider.FetchDiscoveredModels(r.Context(), client, endpoint, req.API, provider.ResolveSecretRef(req.APIKey), req.Headers)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), "upstream_error")
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
			API: req.API, BaseURL: req.BaseURL, APIKey: provider.ResolveSecretRef(req.APIKey), HTTPProxy: req.HTTPProxy,
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
