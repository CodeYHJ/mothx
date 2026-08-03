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
