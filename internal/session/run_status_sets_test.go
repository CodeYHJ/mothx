package session

import (
	"fmt"
	"strings"
	"testing"
)

// TestSessionRunStatusSetsAreCanonical locks the single-source-of-truth status
// sets so future edits cannot silently reintroduce a second definition.
func TestSessionRunStatusSetsAreCanonical(t *testing.T) {
	wantNonTerminal := []string{"created", "queued", "running", "waiting_for_approval", "waiting_for_question", "cancelling", "terminalizing"}
	gotNonTerminal := NonTerminalSessionRunStatuses()
	if len(gotNonTerminal) != len(wantNonTerminal) {
		t.Fatalf("non-terminal statuses = %v, want %v", gotNonTerminal, wantNonTerminal)
	}
	for i, status := range wantNonTerminal {
		if gotNonTerminal[i] != status || !IsNonTerminalSessionRunStatus(status) {
			t.Fatalf("non-terminal status %q missing or out of order in %v", status, gotNonTerminal)
		}
		if IsTerminalSessionRunStatus(status) {
			t.Fatalf("status %q is both terminal and non-terminal", status)
		}
	}

	// expired must be terminal so lease validation, fork resolution, and
	// reopen handling agree even if the terminalizer starts writing it.
	wantTerminal := []string{"completed", "incomplete", "expired", "failed", "cancelled", "canceled", "timed_out"}
	gotTerminal := TerminalSessionRunStatuses()
	if len(gotTerminal) != len(wantTerminal) {
		t.Fatalf("terminal statuses = %v, want %v", gotTerminal, wantTerminal)
	}
	for i, status := range wantTerminal {
		if gotTerminal[i] != status || !IsTerminalSessionRunStatus(status) {
			t.Fatalf("terminal status %q missing or out of order in %v", status, gotTerminal)
		}
		if IsNonTerminalSessionRunStatus(status) {
			t.Fatalf("status %q is both terminal and non-terminal", status)
		}
	}
	if !IsTerminalSessionRunStatus("expired") {
		t.Fatal("expired must be treated as terminal")
	}

	// Returned slices are copies: mutating them must not corrupt the source.
	gotNonTerminal[0] = "mutated"
	if IsNonTerminalSessionRunStatus("mutated") {
		t.Fatal("NonTerminalSessionRunStatuses returned a shared slice")
	}
}

// TestNonTerminalSessionRunStatusSQLMatchesList proves the SQL literal used by
// partial unique indexes is derived from the canonical list.
func TestNonTerminalSessionRunStatusSQLMatchesList(t *testing.T) {
	quoted := make([]string, 0, len(NonTerminalSessionRunStatuses()))
	for _, status := range NonTerminalSessionRunStatuses() {
		quoted = append(quoted, "'"+status+"'")
	}
	want := strings.Join(quoted, ", ")
	if got := nonTerminalSessionRunStatusSQL(); got != want {
		t.Fatalf("nonTerminalSessionRunStatusSQL() = %s, want %s", got, want)
	}
}

// TestCurrentSchemaUsesCanonicalActiveRunStatuses locks the new-database
// partial unique index to the canonical non-terminal set.
func TestCurrentSchemaUsesCanonicalActiveRunStatuses(t *testing.T) {
	want := fmt.Sprintf("WHERE status IN (%s)", nonTerminalSessionRunStatusSQL())
	if !strings.Contains(currentSchema, want) {
		t.Fatalf("currentSchema missing canonical active run index clause %q", want)
	}
	if strings.Contains(currentSchema, "%s") {
		t.Fatal("currentSchema contains an unresolved format placeholder")
	}
}
