package esm

import (
	"context"
	"fmt"
	"strings"
)

// RoleResult is the UI/runtime-neutral result of one isolated ESM role run.
type RoleResult struct {
	Response   string
	Tokens     int64
	DurationMS int64
	ToolCalls  int
	ToolNames  map[string]int
	ToolError  map[string]bool
}

// Outcome describes the state transition produced by applying one ESM role result.
type Outcome struct {
	Objective *Objective
	Subject   string
	Message   string
	Reason    string
	Rejected  bool
	Completed bool
}

// ApplyWorkerResult applies the canonical TUI ESM worker semantics to durable state.
func ApplyWorkerResult(ctx context.Context, store *Store, sessionID, runID string, result RoleResult) (Outcome, bool, error) {
	if store == nil {
		return Outcome{}, false, fmt.Errorf("esm store is nil")
	}
	report, err := ParseWorkerReport(result.Response)
	if err != nil {
		reason := "worker report was not structured: " + err.Error()
		next, rejectErr := store.RejectWorkerReport(ctx, sessionID, runID, reason, nil)
		return Outcome{Objective: next, Subject: "worker report", Reason: reason, Rejected: true}, rejectErr == nil, rejectErr
	}
	if _, err = store.RecordWorkerProgress(ctx, sessionID, report.Summary, report.RemainingWork); err != nil {
		return Outcome{}, false, err
	}
	switch report.Status {
	case WorkerStatusContinue:
		next, err := store.FinishRun(ctx, sessionID, runID)
		return Outcome{Objective: next, Subject: "worker", Message: WorkerContinueMessage(report)}, err == nil, err
	case WorkerStatusCompleteCandidate:
		if reason := InvalidWorkerCandidateReason(result, report); reason != "" {
			next, rejectErr := store.RejectWorkerReport(ctx, sessionID, runID, reason, WorkerOutstandingWork(report))
			return Outcome{Objective: next, Subject: "worker completion candidate", Reason: reason, Rejected: true}, rejectErr == nil, rejectErr
		}
		reason := FormatWorkerCompletion(report, result.Response)
		next, updateErr := store.UpdateFromModelForRun(ctx, sessionID, StatusComplete, reason, runID)
		return Outcome{Objective: next, Subject: "worker completion candidate", Reason: reason}, updateErr == nil, updateErr
	case WorkerStatusBlockedCandidate:
		if len(report.Blockers) == 0 {
			reason := "worker blocked_candidate report did not include a concrete blocker"
			next, rejectErr := store.RejectWorkerReport(ctx, sessionID, runID, reason, report.RemainingWork)
			return Outcome{Objective: next, Subject: "worker blocker report", Reason: reason, Rejected: true}, rejectErr == nil, rejectErr
		}
		reason := FormatWorkerBlocker(report)
		next, updateErr := store.UpdateFromModelForRun(ctx, sessionID, StatusBlocked, reason, runID)
		return Outcome{Objective: next, Subject: "worker blocker", Reason: reason}, updateErr == nil, updateErr
	}
	return Outcome{}, false, fmt.Errorf("invalid worker status %q", report.Status)
}

// ApplyReviewResult applies the canonical TUI ESM critic/audit semantics to durable state.
func ApplyReviewResult(ctx context.Context, store *Store, sessionID, runID, role string, result RoleResult) (Outcome, bool, error) {
	if store == nil {
		return Outcome{}, false, fmt.Errorf("esm store is nil")
	}
	report, err := ParseAuditReport(result.Response)
	if err != nil {
		review := titleESMRole(role) + " report was not structured; completion candidate rejected: " + err.Error()
		next, rejectErr := store.RejectCompletionCandidateForRun(ctx, sessionID, runID, review, nil)
		return Outcome{Objective: next, Subject: role + " completion candidate", Reason: review, Rejected: true}, rejectErr == nil, rejectErr
	}
	if reason := InvalidSupervisorPassReason(role, result, report); reason != "" {
		next, rejectErr := store.RejectCompletionCandidateForRun(ctx, sessionID, runID, reason, report.MissingWork)
		return Outcome{Objective: next, Subject: role + " completion candidate", Reason: reason, Rejected: true}, rejectErr == nil, rejectErr
	}
	review := FormatAuditReview(report, result.Response)
	if report.Verdict == AuditVerdictFail {
		next, rejectErr := store.RejectCompletionCandidateForRun(ctx, sessionID, runID, review, report.MissingWork)
		return Outcome{Objective: next, Subject: role + " completion candidate", Reason: review, Rejected: true}, rejectErr == nil, rejectErr
	}
	if role == "critic" {
		return Outcome{Subject: "critic", Message: "ESM critic found no hard blocker; verifier will audit"}, true, nil
	}
	next, completeErr := store.MarkCompleteFromAudit(ctx, sessionID, review)
	return Outcome{Objective: next, Subject: "audit", Reason: review, Completed: completeErr == nil}, completeErr == nil, completeErr
}

func InvalidWorkerCandidateReason(result RoleResult, report WorkerReport) string {
	if len(report.RemainingWork) > 0 || len(report.Blockers) > 0 {
		var contradictions []string
		if len(report.RemainingWork) > 0 {
			contradictions = append(contradictions, FormatItemDetail("remaining work", report.RemainingWork))
		}
		if len(report.Blockers) > 0 {
			contradictions = append(contradictions, FormatItemDetail("blockers", report.Blockers))
		}
		return "worker proposed completion while reporting " + strings.Join(contradictions, "; ")
	}
	if result.ToolCalls == 0 {
		return "worker proposed completion without any tool-backed inspection or validation"
	}
	if len(result.ToolError) >= result.ToolCalls {
		return "worker proposed completion but all inspection or validation tool calls failed"
	}
	if strings.TrimSpace(report.Summary) == "" {
		return "worker proposed completion without a summary"
	}
	if len(report.Evidence) == 0 {
		return "worker proposed completion without evidence"
	}
	return ""
}

func InvalidSupervisorPassReason(role string, result RoleResult, report AuditReport) string {
	if report.Verdict != AuditVerdictPass {
		return ""
	}
	prefix := role + " pass rejected: "
	if len(report.MissingWork) > 0 {
		return prefix + FormatItemDetail("missing_work", report.MissingWork)
	}
	if result.ToolCalls == 0 {
		return prefix + "no independent tool-backed inspection was performed"
	}
	if len(result.ToolError) >= result.ToolCalls {
		return prefix + "all independent inspection tool calls failed"
	}
	if strings.TrimSpace(report.Review) == "" {
		return prefix + "review is empty"
	}
	if len(report.RequirementsChecked) == 0 {
		return prefix + "requirements_checked is empty"
	}
	if len(report.Evidence) == 0 {
		return prefix + "evidence is empty"
	}
	return ""
}

func WorkerOutstandingWork(report WorkerReport) []string {
	items := append([]string(nil), report.RemainingWork...)
	for _, blocker := range report.Blockers {
		items = append(items, "blocker: "+blocker)
	}
	return items
}

func WorkerContinueMessage(report WorkerReport) string {
	parts := []string{"ESM worker reported more work remains"}
	if report.Summary != "" {
		parts = append(parts, "progress: "+report.Summary)
	}
	if len(report.RemainingWork) > 0 {
		parts = append(parts, FormatItemDetail("remaining work", report.RemainingWork))
	}
	return strings.Join(parts, "; ")
}

func FormatItemDetail(label string, items []string) string {
	return fmt.Sprintf("%s (%d): %s", label, len(items), strings.Join(items, "; "))
}

func FormatWorkerCompletion(report WorkerReport, raw string) string {
	return FormatReportParts("summary", report.Summary, "evidence", report.Evidence, fmt.Sprintf("remaining_work (%d)", len(report.RemainingWork)), report.RemainingWork, raw)
}

func FormatWorkerBlocker(report WorkerReport) string {
	return strings.Join(report.Blockers, "; ")
}

func FormatAuditReview(report AuditReport, raw string) string {
	return FormatReportParts("review", report.Review, "requirements", report.RequirementsChecked, fmt.Sprintf("missing_work (%d)", len(report.MissingWork)), report.MissingWork, raw)
}

func FormatReportParts(primaryLabel, primary string, firstLabel string, first []string, secondLabel string, second []string, raw string) string {
	var parts []string
	if strings.TrimSpace(primary) != "" {
		parts = append(parts, primaryLabel+": "+strings.TrimSpace(primary))
	}
	if len(first) > 0 {
		parts = append(parts, firstLabel+": "+strings.Join(first, "; "))
	}
	if len(second) > 0 {
		parts = append(parts, secondLabel+": "+strings.Join(second, "; "))
	}
	if len(parts) == 0 {
		return strings.TrimSpace(raw)
	}
	return strings.Join(parts, "\n")
}

func titleESMRole(role string) string {
	if role == "" {
		return "ESM"
	}
	return strings.ToUpper(role[:1]) + role[1:]
}
