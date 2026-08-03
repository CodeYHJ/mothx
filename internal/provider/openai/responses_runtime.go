package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/ua"
)

// ResponsesRunManager owns background Responses runs. It is intentionally
// separate from Provider.Chat: background requests have a durable lifecycle
// and cannot be represented by a single synchronous stream channel.
type ResponsesRunManager struct {
	provider   *Provider
	sessionDir string
}

func (p *Provider) NewResponsesRunManager(sessionDir string) *ResponsesRunManager {
	return &ResponsesRunManager{provider: p, sessionDir: sessionDir}
}

func (m *ResponsesRunManager) Start(ctx context.Context, sessionID, localTurnID string, params provider.ChatParams) (*session.ResponseRun, error) {
	if m == nil || m.provider == nil {
		return nil, fmt.Errorf("responses run manager is not configured")
	}
	if sessionID == "" || localTurnID == "" {
		return nil, fmt.Errorf("session ID and local turn ID are required")
	}
	if m.provider.apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	modelID := params.ModelID
	if modelID == "" {
		models := m.provider.Models()
		if len(models) == 0 {
			return nil, fmt.Errorf("no models available from provider %q", m.provider.Name())
		}
		modelID = models[0].ID
	}
	model := m.provider.GetModel(modelID)
	if err := m.provider.validateResponsesCapabilities(model, params); err != nil {
		return nil, err
	}
	reqBody, err := m.provider.buildResponsesRequest(params, modelID, model, false, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal background request: %w", err)
	}

	now := time.Now()
	run := session.ResponseRun{
		SessionID:   sessionID,
		LocalRunID:  session.GenerateID(),
		LocalTurnID: localTurnID,
		Provider:    m.provider.Name(),
		API:         "openai-responses",
		State:       "queued",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := session.SaveResponseRun(m.sessionDir, run); err != nil {
		return nil, fmt.Errorf("persist background run: %w", err)
	}

	response, err := m.doJSON(ctx, http.MethodPost, "/responses", body)
	if err != nil {
		run.State = "failed"
		run.UpdatedAt = time.Now()
		_ = session.SaveResponseRun(m.sessionDir, run)
		return nil, err
	}
	if response.ID == "" {
		run.State = "failed"
		run.UpdatedAt = time.Now()
		_ = session.SaveResponseRun(m.sessionDir, run)
		return nil, fmt.Errorf("background response did not return an id")
	}
	run.ResponseID = response.ID
	run.State = response.Status
	if run.State == "" {
		run.State = "queued"
	}
	run.UpdatedAt = time.Now()
	if isResponsesTerminalStatus(run.State) {
		run.UpdatedAt = time.Now()
		if err := archiveBackgroundResponse(m.sessionDir, run, response); err != nil {
			return nil, fmt.Errorf("archive background response: %w", err)
		}
	}
	if err := session.SaveResponseRun(m.sessionDir, run); err != nil {
		return nil, fmt.Errorf("persist background response: %w", err)
	}
	return &run, nil
}

// Continue submits tool outputs against a completed/terminal response. Each
// continuation receives its own local turn id so response archive rows remain
// immutable and replayable while the caller keeps the same user-facing run.
func (m *ResponsesRunManager) Continue(ctx context.Context, sessionID, localTurnID string, previous *session.ResponseRun, outputs []provider.Message, params provider.ChatParams) (*session.ResponseRun, error) {
	if previous == nil || strings.TrimSpace(previous.ResponseID) == "" {
		return nil, fmt.Errorf("previous Responses response ID is required")
	}
	if localTurnID == "" {
		return nil, fmt.Errorf("continuation local turn ID is required")
	}
	params.Messages = outputs
	if params.ResponseOptions == nil {
		params.ResponseOptions = &provider.ResponseOptions{}
	}
	params.ResponseOptions.ReplayItems = nil
	params.ResponseOptions.PreviousResponseID = previous.ResponseID
	return m.Start(ctx, sessionID, localTurnID, params)
}

func (m *ResponsesRunManager) Get(ctx context.Context, sessionID, localRunID string) (*session.ResponseRun, error) {
	run, err := session.GetResponseRun(m.sessionDir, sessionID, localRunID)
	if err != nil || run == nil {
		return run, err
	}
	if run.ResponseID == "" || isResponsesTerminalStatus(run.State) {
		return run, nil
	}
	response, err := m.doJSON(ctx, http.MethodGet, "/responses/"+url.PathEscape(run.ResponseID), nil)
	if err != nil {
		return nil, err
	}
	applyResponsesRemoteState(run, response)
	if isResponsesTerminalStatus(run.State) {
		if err := archiveBackgroundResponse(m.sessionDir, *run, response); err != nil {
			return nil, fmt.Errorf("archive background response: %w", err)
		}
	}
	if err := session.SaveResponseRun(m.sessionDir, *run); err != nil {
		return nil, fmt.Errorf("persist background response state: %w", err)
	}
	return run, nil
}

func archiveBackgroundResponse(sessionDir string, run session.ResponseRun, response *responsesCompletedObject) error {
	if response == nil || run.SessionID == "" || run.LocalTurnID == "" {
		return nil
	}
	now := time.Now()
	status := response.Status
	if status == "" {
		status = run.State
	}
	var incompleteReason string
	if response.IncompleteDetails != nil {
		incompleteReason = response.IncompleteDetails.Reason
	}
	summary, err := json.Marshal(map[string]any{
		"responseId": response.ID, "status": status, "itemCount": len(response.Output),
		"incompleteReason": incompleteReason,
	})
	if err != nil {
		return err
	}
	if err := session.SaveResponseTurn(sessionDir, session.ResponseTurn{
		SessionID: run.SessionID, LocalTurnID: run.LocalTurnID, ResponseID: response.ID,
		PreviousResponseID: response.PreviousResponseID, ConversationID: responsesConversationID(response),
		Provider: run.Provider, API: run.API, Model: "background", StateMode: "replay",
		Status: status, IncompleteReason: incompleteReason, ResponseSummary: summary,
		CreatedAt: run.CreatedAt, CompletedAt: &now,
	}); err != nil {
		return err
	}
	for index, raw := range response.Output {
		item, err := decodeResponsesOutputItem(raw, index)
		if err != nil || item == nil || item.Type == "" {
			continue
		}
		if err := session.SaveResponseItem(sessionDir, session.ResponseItemArchive{
			SessionID: run.SessionID, LocalTurnID: run.LocalTurnID, ResponseID: response.ID,
			ItemID: item.ID, OutputIndex: index, ItemType: item.Type, ItemStatus: item.Status,
			SanitizedJSON: item.Canonical,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *ResponsesRunManager) Cancel(ctx context.Context, sessionID, localRunID string) error {
	run, err := session.GetResponseRun(m.sessionDir, sessionID, localRunID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("background run %q not found", localRunID)
	}
	if run.ResponseID == "" || isResponsesTerminalStatus(run.State) {
		return nil
	}
	if _, err := m.doJSON(ctx, http.MethodPost, "/responses/"+url.PathEscape(run.ResponseID)+"/cancel", nil); err != nil {
		return err
	}
	run.CancelRequested = true
	run.State = "cancelling"
	run.UpdatedAt = time.Now()
	return session.SaveResponseRun(m.sessionDir, *run)
}

// Recover refreshes every non-terminal local run for a session. Callers can
// invoke it during process startup before accepting new work.
func (m *ResponsesRunManager) Recover(ctx context.Context, sessionID string) ([]session.ResponseRun, error) {
	runs, err := session.ListResponseRuns(m.sessionDir, sessionID, 500)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if isResponsesTerminalStatus(runs[i].State) || runs[i].ResponseID == "" {
			continue
		}
		refreshed, err := m.Get(ctx, sessionID, runs[i].LocalRunID)
		if err != nil {
			return nil, err
		}
		if refreshed != nil {
			runs[i] = *refreshed
		}
	}
	return runs, nil
}

func (m *ResponsesRunManager) doJSON(ctx context.Context, method, path string, body []byte) (*responsesCompletedObject, error) {
	reqBody := io.Reader(nil)
	if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.provider.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create background request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.provider.apiKey)
	req.Header.Set("User-Agent", ua.ProviderUserAgent())
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	provider.ApplyHeaders(req, m.provider.headers)

	resp, err := m.provider.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("background request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read background response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("background API error %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var response responsesCompletedObject
	if len(responseBody) > 0 && json.Unmarshal(responseBody, &response) != nil {
		return nil, fmt.Errorf("decode background response: invalid JSON")
	}
	return &response, nil
}

func applyResponsesRemoteState(run *session.ResponseRun, response *responsesCompletedObject) {
	if run == nil || response == nil {
		return
	}
	if response.ID != "" {
		run.ResponseID = response.ID
	}
	if response.Status != "" {
		run.State = response.Status
	}
	run.UpdatedAt = time.Now()
}

func isResponsesTerminalStatus(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "failed", "incomplete", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}
