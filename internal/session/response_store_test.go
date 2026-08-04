package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResponseRuntimeStorePersistsSummariesItemsRunsAndDeduplication(t *testing.T) {
	sessionDir := t.TempDir()
	manager := New(t.TempDir(), sessionDir)
	if err := manager.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	sessionID := manager.GetHeader().ID
	now := time.Now().UTC()

	if err := SaveResponseTurn(sessionDir, ResponseTurn{
		SessionID:       sessionID,
		LocalTurnID:     "turn-1",
		Provider:        "openai",
		API:             "openai-responses",
		Model:           "gpt-test",
		StateMode:       "replay",
		Status:          "completed",
		RequestSummary:  json.RawMessage(`{"model":"gpt-test","api_key":"hidden"}`),
		ResponseSummary: json.RawMessage(`{"status":"completed","items":1}`),
		CreatedAt:       now,
	}); err != nil {
		t.Fatalf("save response turn: %v", err)
	}
	turn, err := GetResponseTurn(sessionDir, sessionID, "turn-1")
	if err != nil {
		t.Fatalf("get response turn: %v", err)
	}
	if turn == nil || string(turn.RequestSummary) == "" || strings.Contains(string(turn.RequestSummary), "hidden") {
		t.Fatalf("response turn = %#v, expected sanitized request summary", turn)
	}

	if err := SaveResponseItem(sessionDir, ResponseItemArchive{
		SessionID:     sessionID,
		LocalTurnID:   "turn-1",
		ResponseID:    "resp-1",
		ItemID:        "item-1",
		OutputIndex:   0,
		ItemType:      "future_item",
		ItemStatus:    "completed",
		SanitizedJSON: json.RawMessage(`{"type":"future_item","token":"hidden"}`),
	}); err != nil {
		t.Fatalf("save response item: %v", err)
	}
	items, err := ListResponseItems(sessionDir, sessionID, "turn-1")
	if err != nil {
		t.Fatalf("list response items: %v", err)
	}
	if len(items) != 1 || strings.Contains(string(items[0].SanitizedJSON), "hidden") {
		t.Fatalf("response items = %#v, expected sanitized item", items)
	}
	if err := SaveResponseItem(sessionDir, ResponseItemArchive{
		SessionID:     sessionID,
		LocalTurnID:   "turn-1",
		ResponseID:    "resp-1",
		ItemID:        "item-1",
		OutputIndex:   0,
		ItemType:      "future_item",
		ItemStatus:    "completed",
		SanitizedJSON: json.RawMessage(`{"type":"future_item","status":"completed"}`),
	}); err != nil {
		t.Fatalf("upsert response item: %v", err)
	}
	items, err = ListResponseItems(sessionDir, sessionID, "turn-1")
	if err != nil {
		t.Fatalf("list updated response items: %v", err)
	}
	if len(items) != 1 || items[0].ItemStatus != "completed" {
		t.Fatalf("response item upsert = %#v", items)
	}
	replay, err := ListResponseReplayItems(sessionDir, sessionID, 10)
	if err != nil {
		t.Fatalf("list replay items: %v", err)
	}
	if len(replay) != 1 || !strings.Contains(string(replay[0]), `"status":"completed"`) {
		t.Fatalf("replay items = %#v", replay)
	}

	record := ToolExecutionRecord{
		SessionID:      sessionID,
		LocalTurnID:    "turn-1",
		ExecutionKey:   "exec-1",
		Provider:       "openai",
		API:            "openai-responses",
		ProviderCallID: "call-1",
		ToolKind:       "function",
		ToolName:       "bash",
		ArgsHash:       "hash-1",
		ExecutionState: "running",
		SideEffecting:  true,
	}
	first, created, err := ClaimToolExecutionRecord(sessionDir, record)
	if err != nil {
		t.Fatalf("claim tool execution: %v", err)
	}
	if !created || first == nil || first.ExecutionKey != record.ExecutionKey {
		t.Fatalf("first claim = %#v, created=%v", first, created)
	}
	second, created, err := ClaimToolExecutionRecord(sessionDir, record)
	if err != nil {
		t.Fatalf("duplicate tool execution claim: %v", err)
	}
	if created || second == nil || second.ID != first.ID {
		t.Fatalf("duplicate claim = %#v, created=%v; want existing record", second, created)
	}
	finished := now.Add(time.Second)
	record.ExecutionState = "completed"
	record.ResultSummary = json.RawMessage(`{"output":"ok"}`)
	record.CompletedAt = &finished
	if err := UpdateToolExecutionRecord(sessionDir, record); err != nil {
		t.Fatalf("update tool execution: %v", err)
	}
	abandon := record
	abandon.ExecutionKey = "exec-abandon"
	abandon.ExecutionState = "running"
	abandon.CompletedAt = nil
	abandon.ResultSummary = nil
	if _, created, err := ClaimToolExecutionRecord(sessionDir, abandon); err != nil || !created {
		t.Fatalf("claim abandonable tool execution: created=%v err=%v", created, err)
	}
	abandoned, err := AbandonInterruptedToolExecutionRecords(sessionDir, sessionID, "turn-1")
	if err != nil || abandoned != 1 {
		t.Fatalf("abandon interrupted executions: abandoned=%d err=%v", abandoned, err)
	}
	stored, created, err := ClaimToolExecutionRecord(sessionDir, abandon)
	if err != nil || created || stored.ExecutionState != "abandoned" || !strings.Contains(string(stored.ResultSummary), "manual_abandon") {
		t.Fatalf("abandoned execution record = %#v, created=%v err=%v", stored, created, err)
	}

	sequence := int64(4)
	if err := SaveResponseRun(sessionDir, ResponseRun{
		SessionID:         sessionID,
		LocalRunID:        "run-1",
		ResponseID:        "resp-1",
		Provider:          "openai",
		API:               "openai-responses",
		State:             "queued",
		PollingURL:        "https://api.test/responses/resp-1",
		LastEventSequence: &sequence,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("save response run: %v", err)
	}
	run, err := GetResponseRun(sessionDir, sessionID, "run-1")
	if err != nil {
		t.Fatalf("get response run: %v", err)
	}
	if run == nil || run.State != "queued" || run.LastEventSequence == nil || *run.LastEventSequence != 4 {
		t.Fatalf("response run = %#v", run)
	}
}

func TestListResponseReplayTurnsDeduplicatesHistoricalFunctionItems(t *testing.T) {
	sessionDir := t.TempDir()
	manager := New(t.TempDir(), sessionDir)
	if err := manager.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	sessionID := manager.GetHeader().ID
	if err := SaveResponseTurn(sessionDir, ResponseTurn{
		SessionID: sessionID, LocalTurnID: "turn-1", Provider: "openai", API: "openai-responses",
		Model: "gpt-test", StateMode: "replay", Status: "completed",
	}); err != nil {
		t.Fatalf("save response turn: %v", err)
	}
	items := []ResponseItemArchive{
		{SessionID: sessionID, LocalTurnID: "turn-1", ItemID: "item-1", OutputIndex: 1, ItemType: "function_call", SanitizedJSON: json.RawMessage(`{"type":"function_call","id":"item-1","call_id":"call-1","name":"read","arguments":"{}"}`)},
		{SessionID: sessionID, LocalTurnID: "turn-1", OutputIndex: 1, ItemType: "function_call", SanitizedJSON: json.RawMessage(`{"type":"function_call","call_id":"call-1","name":"read","arguments":"{}"}`)},
	}
	for _, item := range items {
		if err := SaveResponseItem(sessionDir, item); err != nil {
			t.Fatalf("save response item: %v", err)
		}
	}
	turns, err := ListResponseReplayTurns(sessionDir, sessionID, 10)
	if err != nil {
		t.Fatalf("list replay turns: %v", err)
	}
	if len(turns) != 1 || len(turns[0].Items) != 1 {
		t.Fatalf("replay turns = %#v, want one deduplicated function item", turns)
	}
}

func TestArchiveJSONPreservesNumericUsageCounters(t *testing.T) {
	raw, err := archiveJSON(json.RawMessage(`{"usage":{"totalTokens":18,"cached_tokens":4,"access_token":"secret"}}`))
	if err != nil {
		t.Fatalf("archive JSON: %v", err)
	}
	var value struct {
		Usage struct {
			TotalTokens  int    `json:"totalTokens"`
			CachedTokens int    `json:"cached_tokens"`
			AccessToken  string `json:"access_token"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode archived JSON: %v", err)
	}
	if value.Usage.TotalTokens != 18 || value.Usage.CachedTokens != 4 || value.Usage.AccessToken != "[REDACTED]" {
		t.Fatalf("archived usage = %#v", value.Usage)
	}
}

func TestResponseSessionStateCompareAndSwap(t *testing.T) {
	sessionDir := t.TempDir()
	manager := New(t.TempDir(), sessionDir)
	if err := manager.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	sessionID := manager.GetHeader().ID
	state := ResponseSessionState{
		SessionID:          sessionID,
		StateMode:          "previous_response_id",
		PreviousResponseID: "resp-1",
		Provider:           "openai",
		API:                "openai-responses",
		Model:              "gpt-test",
	}
	created, err := CompareAndSwapResponseSessionState(sessionDir, state, 0)
	if err != nil || !created {
		t.Fatalf("create response state: created=%v err=%v", created, err)
	}
	stored, err := GetResponseSessionState(sessionDir, sessionID)
	if err != nil || stored == nil || stored.Version != 1 || stored.PreviousResponseID != "resp-1" {
		t.Fatalf("stored response state = %#v, err=%v", stored, err)
	}
	state.PreviousResponseID = "resp-2"
	updated, err := CompareAndSwapResponseSessionState(sessionDir, state, stored.Version)
	if err != nil || !updated {
		t.Fatalf("advance response state: updated=%v err=%v", updated, err)
	}
	updated, err = CompareAndSwapResponseSessionState(sessionDir, state, stored.Version)
	if err != nil || updated {
		t.Fatalf("stale response state update: updated=%v err=%v", updated, err)
	}
}
