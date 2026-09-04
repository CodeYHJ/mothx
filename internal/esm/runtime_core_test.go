package esm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/startvibecoding/mothx/internal/session"
)

type runtimeTestAdapter struct {
	responses map[Role]string
	roles     []Role
	prompts   map[Role]string
	roleErr   error
	observers int
}

type runtimeTestEvents struct {
	events []RuntimeEvent
}

func (e *runtimeTestEvents) PublishESMEvent(_ context.Context, event RuntimeEvent) error {
	e.events = append(e.events, event)
	return nil
}

func (a *runtimeTestAdapter) RunRole(_ context.Context, req RoleRequest) (RoleResult, error) {
	a.roles = append(a.roles, req.Role)
	if a.prompts != nil {
		a.prompts[req.Role] = req.Prompt
	}
	if a.roleErr != nil {
		return RoleResult{}, a.roleErr
	}
	return RoleResult{Response: a.responses[req.Role], ToolCalls: 1, ToolError: map[string]bool{}}, nil
}

func (a *runtimeTestAdapter) RunRecoveryObserver(context.Context, RoleRequest, error) (RoleResult, error) {
	a.observers++
	return RoleResult{}, context.Canceled
}

func newRuntimeTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	sess := session.New(root, root)
	if err := sess.Init(); err != nil {
		t.Fatal(err)
	}
	return NewStore(root), sess.GetHeader().ID
}

func TestSupervisorWorkerContinueStopsAtActive(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeTestAdapter{responses: map[Role]string{
		RoleWorker: `{"status":"continue","summary":"made progress","evidence":["read source"],"remaining_work":["finish objective"],"blockers":[]}`,
	}}
	obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-1", rootForRuntimeTest(t), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != StatusActive {
		t.Fatalf("status=%s, want active", obj.Status)
	}
	if !reflect.DeepEqual(adapter.roles, []Role{RoleWorker}) {
		t.Fatalf("roles=%v, want worker only", adapter.roles)
	}
}

func TestSupervisorCompletionUsesCriticThenAudit(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeTestAdapter{responses: map[Role]string{
		RoleWorker: `{"status":"complete_candidate","summary":"done","evidence":["tests pass"],"remaining_work":[],"blockers":[]}`,
		RoleCritic: `{"verdict":"pass","review":"critic verified","requirements_checked":["objective -> covered"],"missing_work":[],"evidence":["read source"]}`,
		RoleAudit:  `{"verdict":"pass","review":"audit verified","requirements_checked":["objective -> covered"],"missing_work":[],"evidence":["read source"]}`,
	}}
	obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-1", rootForRuntimeTest(t), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != StatusComplete {
		t.Fatalf("status=%s, want complete", obj.Status)
	}
	if !reflect.DeepEqual(adapter.roles, []Role{RoleWorker, RoleCritic, RoleAudit}) {
		t.Fatalf("roles=%v", adapter.roles)
	}
}

func TestSupervisorPublishesLifecycleEvents(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeTestAdapter{responses: map[Role]string{RoleWorker: `{"status":"continue","summary":"progress","evidence":["inspection"],"remaining_work":["finish"],"blockers":[]}`}}
	events := &runtimeTestEvents{}
	if _, err := (&Supervisor{Store: store, Adapter: adapter, Events: events}).Run(ctx, sessionID, "run-events", rootForRuntimeTest(t), "agent"); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 2 || events.events[0].Type != "role_started" || events.events[1].Type != "role_finished" {
		t.Fatalf("events=%#v", events.events)
	}
}

func TestSupervisorRecoveryLimitPausesWithoutObserver(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < RecoveryLimit; i++ {
		if _, err := store.RecordRecovery(ctx, sessionID, "previous interruption", "retry", []string{"finish"}); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &runtimeTestAdapter{roleErr: context.DeadlineExceeded}
	obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-limit", rootForRuntimeTest(t), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != StatusPaused || adapter.observers != 0 {
		t.Fatalf("objective=%#v observers=%d", obj, adapter.observers)
	}
}

func TestSupervisorNonRetryableFailurePausesUntilExplicitResume(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("provider rejected the request")
	adapter := &runtimeTestAdapter{roleErr: wantErr}
	obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, "run-failed", rootForRuntimeTest(t), "yolo")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if obj == nil || obj.Status != StatusPaused || obj.CanAutoRun() {
		t.Fatalf("failed objective = %#v, want paused and not runnable", obj)
	}
	stored, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusPaused {
		t.Fatalf("persisted status = %s, want paused", stored.Status)
	}
}

func TestSupervisorSharedStorePersistsAcrossRuntimeInstances(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	response := `{"status":"continue","summary":"progress","evidence":["inspection"],"remaining_work":["finish"],"blockers":[]}`
	first := &runtimeTestAdapter{responses: map[Role]string{RoleWorker: response}}
	second := &runtimeTestAdapter{responses: map[Role]string{RoleWorker: response}}
	if _, err := (&Supervisor{Store: store, Adapter: first}).Run(ctx, sessionID, "tui-run", rootForRuntimeTest(t), "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Supervisor{Store: store, Adapter: second}).Run(ctx, sessionID, "webui-run", rootForRuntimeTest(t), "agent"); err != nil {
		t.Fatal(err)
	}
	obj, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if obj.ProgressSummary != "progress" || len(first.roles) != 1 || len(second.roles) != 1 {
		t.Fatalf("persisted objective=%#v roles=%v/%v", obj, first.roles, second.roles)
	}
}

func rootForRuntimeTest(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TestSupervisorRejectionCircuitBreakerSurvivesContinuations is the P0
// regression test: rejection streaks are recorded under the base continuation
// run ID and must survive the Supervisor-owned FinishRun at the end of each
// continuation, pausing once CompletionRejectionLimit is reached.
func TestSupervisorRejectionCircuitBreakerSurvivesContinuations(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	adapter := &runtimeTestAdapter{responses: map[Role]string{
		RoleWorker: `{"status":"complete_candidate","summary":"done","evidence":["tests pass"],"remaining_work":[],"blockers":[]}`,
		RoleCritic: `{"verdict":"fail","review":"missing regression tests","requirements_checked":["objective -> gap"],"missing_work":["add regression tests"],"evidence":["read source"]}`,
	}}
	for i := 1; i <= CompletionRejectionLimit; i++ {
		obj, err := (&Supervisor{Store: store, Adapter: adapter}).Run(ctx, sessionID, fmt.Sprintf("run-%d", i), rootForRuntimeTest(t), "yolo")
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		wantStatus := StatusActive
		if i == CompletionRejectionLimit {
			wantStatus = StatusPaused
		}
		if obj.Status != wantStatus || obj.RejectionCount != i {
			t.Fatalf("after continuation %d: status=%s rejectionCount=%d, want %s/%d", i, obj.Status, obj.RejectionCount, wantStatus, i)
		}
	}
	obj, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if obj.CanAutoRun() {
		t.Fatalf("circuit breaker did not stop continuation: %#v", obj)
	}
}

// TestSupervisorBlockedAuditAccumulatesAcrossContinuations verifies the
// three-run blocked audit across continuations and that an intervening
// successful continue clears the stale streak.
func TestSupervisorBlockedAuditAccumulatesAcrossContinuations(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective"); err != nil {
		t.Fatal(err)
	}
	blocked := &runtimeTestAdapter{responses: map[Role]string{
		RoleWorker: `{"status":"blocked_candidate","summary":"cannot proceed","evidence":["attempted provisioning"],"remaining_work":[],"blockers":["missing API token"]}`,
	}}
	continueAdapter := &runtimeTestAdapter{responses: map[Role]string{
		RoleWorker: `{"status":"continue","summary":"progress","evidence":["inspection"],"remaining_work":["finish"],"blockers":[]}`,
	}}

	obj, err := (&Supervisor{Store: store, Adapter: blocked}).Run(ctx, sessionID, "run-1", rootForRuntimeTest(t), "yolo")
	if err != nil {
		t.Fatal(err)
	}
	if obj.Status != StatusActive || obj.BlockedCount != 1 {
		t.Fatalf("after run-1: status=%s blockedCount=%d", obj.Status, obj.BlockedCount)
	}

	// A continuation that finishes without the blocker clears the streak.
	obj, err = (&Supervisor{Store: store, Adapter: continueAdapter}).Run(ctx, sessionID, "run-2", rootForRuntimeTest(t), "yolo")
	if err != nil {
		t.Fatal(err)
	}
	if obj.BlockedCount != 0 {
		t.Fatalf("stale blocked streak not cleared: %#v", obj)
	}

	for i := 3; i <= 5; i++ {
		obj, err = (&Supervisor{Store: store, Adapter: blocked}).Run(ctx, sessionID, fmt.Sprintf("run-%d", i), rootForRuntimeTest(t), "yolo")
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}
	if obj.Status != StatusBlocked {
		t.Fatalf("after repeated blocker: status=%s blockedCount=%d, want blocked", obj.Status, obj.BlockedCount)
	}
}
