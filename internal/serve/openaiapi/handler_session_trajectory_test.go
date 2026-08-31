package openaiapi

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeTrajectoryCursor(t *testing.T) {
	if cursor, err := decodeTrajectoryCursor(""); err != nil || cursor.EntrySeq != 0 {
		t.Fatalf("empty cursor = %#v, %v", cursor, err)
	}
	if _, err := decodeTrajectoryCursor("bad-cursor"); err == nil {
		t.Fatal("expected invalid cursor error")
	}
}

func TestTrajectoryHelpers(t *testing.T) {
	if got := safeSessionFilename("../../Session id"); got != "mothx-session-Sessionid" {
		t.Fatalf("safe filename = %q", got)
	}
	if got := normalizeTrajectoryStatus("", "run_failed"); got != "failed" {
		t.Fatalf("status = %q", got)
	}
	if got := normalizeTrajectoryStatus("waiting_for_approval", ""); got != "pending" {
		t.Fatalf("waiting status = %q", got)
	}
	value := compactTrajectoryValue(map[string]any{"empty": "", "hasDetail": false, "value": "ok"})
	if len(value) != 1 || value["value"] != "ok" {
		t.Fatalf("compact value = %#v", value)
	}
	redacted := redactTrajectoryValue(map[string]any{"token": "secret", "workDir": "/private/project", "nested": map[string]any{"api_key": "hidden"}})
	mapValue, ok := redacted.(map[string]any)
	if !ok || mapValue["token"] != "[REDACTED]" || mapValue["workDir"] != "[OMITTED]" || mapValue["nested"].(map[string]any)["api_key"] != "[REDACTED]" {
		t.Fatalf("redacted value = %#v", redacted)
	}
}

func TestFilterTrajectoryRecordsUsesCanonicalTranscriptAndDecisionCursors(t *testing.T) {
	records := []trajectoryRecord{
		{source: "transcript", seq: 1, value: map[string]any{"id": "transcript:s:m1"}},
		{source: "transcript", seq: 4, value: map[string]any{"id": "transcript:s:m4"}},
		{source: "decision", seq: 2, value: map[string]any{"id": "decision:s:d2"}},
		{source: "decision", seq: 5, value: map[string]any{"id": "decision:s:d5"}},
	}
	filtered := filterTrajectoryRecords(records, trajectoryCursor{EntrySeq: 4, DecisionSeq: 5})
	if len(filtered) != 2 || filtered[0].seq != 1 || filtered[1].seq != 2 {
		t.Fatalf("filtered records = %#v", filtered)
	}
}

func TestTrajectoryEventIDUsesSourceForReplayDeduplication(t *testing.T) {
	if got := trajectoryEventID("decision", "session-1", "event-2"); got != "decision:session-1:event-2" {
		t.Fatalf("trajectory event id = %q", got)
	}
}

func TestTrajectoryMessageIDMergesToolLifecycleAcrossStores(t *testing.T) {
	toolCall := trajectoryMessageID("session-1", SessionMessageEntry{ID: "entry:tool:1", Role: "toolCall", ToolCallID: "call-1"})
	toolResult := trajectoryMessageID("session-1", SessionMessageEntry{ID: "entry:result:1", Role: "toolResult", ToolCallID: "call-1"})
	if toolCall != "tool:session-1:call-1" || toolResult != toolCall {
		t.Fatalf("tool ids = %q, %q", toolCall, toolResult)
	}
}

func TestMergeTrajectoryRecordsCombinesToolLifecycle(t *testing.T) {
	merged := mergeTrajectoryRecords([]trajectoryRecord{
		{source: "transcript", seq: 3, value: map[string]any{
			"id": "tool:session-1:call-1", "status": "running", "input": map[string]any{"path": "package.json"}, "seq": int64(3),
		}},
		{source: "transcript", seq: 4, value: map[string]any{
			"id": "tool:session-1:call-1", "status": "completed", "output": "ok", "seq": int64(4),
		}},
	})
	if len(merged) != 1 || merged[0].seq != 3 || merged[0].value["status"] != "completed" {
		t.Fatalf("merged lifecycle = %#v", merged)
	}
	if merged[0].value["input"] == nil || merged[0].value["output"] != "ok" {
		t.Fatalf("merged payload = %#v", merged[0].value)
	}
}

func TestGetSessionTrajectoryRequiresExistingSession(t *testing.T) {
	if _, err := (&Server{}).GetSessionTrajectory("missing", "", 10); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session error = %v", err)
	}
}

func TestWriteTrajectoryErrorDoesNotExposeInternalDetails(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeTrajectoryError(recorder, fmt.Errorf("sqlite path /private/db/session.sqlite: locked"))
	if body := recorder.Body.String(); body == "" || containsAny(body, "/private/db", "sqlite path") {
		t.Fatalf("internal details leaked: %s", body)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) > 0 && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
