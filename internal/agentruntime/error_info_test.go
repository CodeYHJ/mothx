package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestClassifyErrorRetrySafety(t *testing.T) {
	base := ClassifyError(fmt.Errorf("HTTP 503 upstream unavailable"), ErrorClassificationOptions{})
	if base.Code != "provider_unavailable" || base.RetryMode != RetryAutomatic || !base.Retryable {
		t.Fatalf("base classification = %#v", base)
	}

	unsafe := ClassifyError(fmt.Errorf("HTTP 503 upstream unavailable"), ErrorClassificationOptions{
		SideEffectState: SideEffectUnknown,
	})
	if unsafe.RetryMode != RetryDecisionRequired || !unsafe.Retryable {
		t.Fatalf("unsafe classification = %#v", unsafe)
	}
}

func TestClassifyErrorCancellationAndTimeout(t *testing.T) {
	cancelled := ClassifyError(context.Canceled, ErrorClassificationOptions{})
	if cancelled.Code != "run_cancelled" || cancelled.RetryMode != RetryUser || cancelled.FailureClass != FailureCancelled {
		t.Fatalf("cancelled classification = %#v", cancelled)
	}
	timedOut := ClassifyError(context.DeadlineExceeded, ErrorClassificationOptions{})
	if timedOut.Code != "run_timed_out" || timedOut.RetryMode != RetryAutomatic || !timedOut.Retryable {
		t.Fatalf("timed out classification = %#v", timedOut)
	}

	plain := ClassifyError(errors.New("boom"), ErrorClassificationOptions{Phase: PhaseTool})
	if plain.Code != "run_failed" || plain.Phase != PhaseTool || plain.FailureClass != FailureInternal {
		t.Fatalf("plain classification = %#v", plain)
	}
}

func TestSharedFailureContractAcrossAdapters(t *testing.T) {
	// The adapters intentionally have different wire formats, but the runtime
	// classification is the contract they must all project.
	entries := []struct {
		name  string
		phase RunPhase
	}{
		{name: "tui", phase: PhaseModel},
		{name: "acp", phase: PhaseModel},
		{name: "channel", phase: PhaseModel},
		{name: "webui", phase: PhaseModel},
	}
	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			info := ClassifyError(errors.New("HTTP 503 provider secret=should-not-leak"), ErrorClassificationOptions{
				Phase: entry.phase, RunID: "run-1", IntentID: "intent-1", RequestID: "req-1",
			})
			if info.Code != "provider_unavailable" || info.FailureClass != FailureTransient || info.RetryMode != RetryAutomatic || !info.Retryable {
				t.Fatalf("classification = %#v", info)
			}
			if info.Message == "" || containsError(errors.New(info.Message), "provider secret", "503") {
				t.Fatalf("unsafe message = %q", info.Message)
			}
			payload, err := json.Marshal(info)
			if err != nil {
				t.Fatalf("marshal ErrorInfo: %v", err)
			}
			if containsError(errors.New(string(payload)), "provider secret") {
				t.Fatalf("unsafe payload = %s", payload)
			}
		})
	}
}
