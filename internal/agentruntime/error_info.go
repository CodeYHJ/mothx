package agentruntime

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/startvibecoding/mothx/internal/provider"
)

// FailureClass identifies the stable, adapter-neutral category of a failed
// execution. Adapters render it differently, but must not infer it from an
// English provider error string.
type FailureClass string

const (
	FailureValidation  FailureClass = "validation"
	FailurePolicy      FailureClass = "policy"
	FailureTransient   FailureClass = "transient"
	FailureProvider    FailureClass = "provider"
	FailureTool        FailureClass = "tool"
	FailureTransport   FailureClass = "transport"
	FailureCancelled   FailureClass = "canceled"
	FailureIncomplete  FailureClass = "incomplete"
	FailurePersistence FailureClass = "persistence"
	FailureInternal    FailureClass = "internal"
)

// RunPhase indicates where a failure or retry occurred.
type RunPhase string

const (
	PhaseAdmission       RunPhase = "admission"
	PhaseModel           RunPhase = "model"
	PhaseContext         RunPhase = "context"
	PhaseTool            RunPhase = "tool"
	PhaseApproval        RunPhase = "approval"
	PhasePersistence     RunPhase = "persistence"
	PhaseTransport       RunPhase = "transport"
	PhaseTerminalization RunPhase = "terminalization"
)

// RetryMode states who may make progress after a failure. Automatic retry is
// owned by Agent Core/Runtime; adapters only project its progress. Reconcile
// means the caller must first discover whether a previous submission exists.
type RetryMode string

const (
	RetryNone             RetryMode = "none"
	RetryAutomatic        RetryMode = "automatic"
	RetryReconcile        RetryMode = "reconcile"
	RetryUser             RetryMode = "user"
	RetryDecisionRequired RetryMode = "decision_required"
)

// SideEffectState is deliberately conservative. A runtime must not replay an
// execution with unknown or mutating side effects without an explicit policy
// decision.
type SideEffectState string

const (
	SideEffectNone     SideEffectState = "none"
	SideEffectReadOnly SideEffectState = "read_only"
	SideEffectMutating SideEffectState = "mutating"
	SideEffectUnknown  SideEffectState = "unknown"
)

// ErrorInfo is the durable, adapter-neutral description of an execution
// failure. Message is a safe fallback; technical provider details belong in
// server logs and are correlated through RequestID.
type ErrorInfo struct {
	Code            string          `json:"code,omitempty"`
	Type            string          `json:"type,omitempty"`
	FailureClass    FailureClass    `json:"failureClass,omitempty"`
	Phase           RunPhase        `json:"phase,omitempty"`
	MessageKey      string          `json:"messageKey,omitempty"`
	Message         string          `json:"message,omitempty"`
	RetryMode       RetryMode       `json:"retryMode,omitempty"`
	Retryable       bool            `json:"retryable,omitempty"`
	RetryAfterMS    int             `json:"retryAfterMs,omitempty"`
	Attempt         int             `json:"attempt,omitempty"`
	MaxAttempts     int             `json:"maxAttempts,omitempty"`
	SideEffectState SideEffectState `json:"sideEffectState,omitempty"`
	PartialOutput   bool            `json:"partialOutput,omitempty"`
	RunID           string          `json:"runId,omitempty"`
	IntentID        string          `json:"intentId,omitempty"`
	RequestID       string          `json:"requestId,omitempty"`
}

// RetryInfo is a non-terminal progress record. It is persisted as a run event
// so reconnecting adapters can render the same automatic retry state.
type RetryInfo struct {
	Attempt      int      `json:"attempt,omitempty"`
	MaxAttempts  int      `json:"maxAttempts,omitempty"`
	Phase        RunPhase `json:"phase,omitempty"`
	ReasonCode   string   `json:"reasonCode,omitempty"`
	RetryAfterMS int      `json:"retryAfterMs,omitempty"`
	Continue     bool     `json:"continue,omitempty"`
	MessageKey   string   `json:"messageKey,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// ErrorClassificationOptions adds execution facts that cannot be derived from
// an error value alone. The defaults are intentionally safe for callers that
// have not observed tools or output yet.
type ErrorClassificationOptions struct {
	Code            string
	Type            string
	Phase           RunPhase
	Message         string
	MessageKey      string
	HTTPStatus      int
	RetryAfterMS    int
	Attempt         int
	MaxAttempts     int
	SideEffectState SideEffectState
	PartialOutput   bool
	RunID           string
	IntentID        string
	RequestID       string
}

// ClassifyError converts a raw failure into the shared durable contract. It
// intentionally has no adapter/UI dependencies, and only marks automatic
// retries safe before output or side effects exist.
func ClassifyError(err error, opts ErrorClassificationOptions) ErrorInfo {
	info := ErrorInfo{
		Code:            strings.TrimSpace(opts.Code),
		Type:            strings.TrimSpace(opts.Type),
		Phase:           opts.Phase,
		MessageKey:      strings.TrimSpace(opts.MessageKey),
		Message:         safeMessage(err, opts.Message),
		RetryAfterMS:    opts.RetryAfterMS,
		Attempt:         opts.Attempt,
		MaxAttempts:     opts.MaxAttempts,
		SideEffectState: opts.SideEffectState,
		PartialOutput:   opts.PartialOutput,
		RunID:           opts.RunID,
		IntentID:        opts.IntentID,
		RequestID:       opts.RequestID,
	}
	if info.SideEffectState == "" {
		info.SideEffectState = SideEffectNone
	}
	if info.Phase == "" {
		info.Phase = PhaseModel
	}

	switch {
	case errors.Is(err, context.Canceled):
		return applyErrorDefaults(info, "run_cancelled", "canceled", FailureCancelled, RetryUser, false, "run.error.cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return applyErrorDefaults(info, "run_timed_out", "timeout_error", FailureCancelled, retryModeForSafety(info), false, "run.error.timedOut")
	case provider.IsContextOverflowError(err):
		info.Phase = PhaseContext
		return applyErrorDefaults(info, "context_overflow", "context_error", FailureProvider, retryModeForSafety(info), false, "run.error.contextOverflow")
	case provider.IsRetryable(err, opts.HTTPStatus):
		code, key := retryableErrorCode(err, opts.HTTPStatus)
		return applyErrorDefaults(info, code, "provider_error", FailureTransient, retryModeForSafety(info), true, key)
	}

	if info.Code == "" {
		info.Code = "run_failed"
	}
	if info.Type == "" {
		info.Type = "server_error"
	}
	if info.MessageKey == "" {
		info.MessageKey = "run.error.failed"
	}
	if info.RetryMode == "" {
		info.RetryMode = retryModeForSafety(info)
	}
	if info.RetryMode == RetryAutomatic {
		info.RetryMode = RetryUser
	}
	info.Retryable = info.RetryMode == RetryUser || info.RetryMode == RetryDecisionRequired
	if info.FailureClass == "" {
		info.FailureClass = FailureInternal
	}
	return info
}

func applyErrorDefaults(info ErrorInfo, code, typ string, class FailureClass, mode RetryMode, retryable bool, key string) ErrorInfo {
	if info.Code == "" {
		info.Code = code
	}
	if info.Type == "" {
		info.Type = typ
	}
	info.FailureClass = class
	if info.MessageKey == "" {
		info.MessageKey = key
	}
	info.RetryMode = mode
	info.Retryable = retryable || mode == RetryAutomatic || mode == RetryUser || mode == RetryDecisionRequired
	return info
}

func retryModeForSafety(info ErrorInfo) RetryMode {
	if info.PartialOutput || info.SideEffectState == SideEffectMutating || info.SideEffectState == SideEffectUnknown {
		return RetryDecisionRequired
	}
	return RetryAutomatic
}

func retryableErrorCode(err error, status int) (string, string) {
	if status == 429 || containsError(err, "429", "rate limit", "rate_limit") {
		return "rate_limited", "run.error.rateLimited"
	}
	if status >= 500 || containsError(err, "500", "502", "503", "504", "524", "overloaded", "server_error") {
		return "provider_unavailable", "run.error.providerUnavailable"
	}
	var netErr net.Error
	if errors.As(err, &netErr) || containsError(err, "connection", "dns", "eof") {
		return "network_unavailable", "run.error.networkUnavailable"
	}
	if containsError(err, "timeout", "deadline") {
		return "provider_timeout", "run.error.providerTimeout"
	}
	return "provider_interrupted", "run.error.providerInterrupted"
}

func containsError(err error, parts ...string) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, part := range parts {
		if strings.Contains(message, strings.ToLower(part)) {
			return true
		}
	}
	return false
}

func safeMessage(err error, override string) string {
	if message := strings.TrimSpace(override); message != "" {
		return message
	}
	if err == nil {
		return "The run could not be completed."
	}
	// Raw provider errors can include provider-specific details. The adapters
	// should localize MessageKey first; this fallback stays intentionally broad.
	if errors.Is(err, context.Canceled) {
		return "The run was cancelled."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The run timed out."
	}
	if provider.IsRetryable(err, 0) {
		return "The service is temporarily unavailable."
	}
	return "The run could not be completed."
}
