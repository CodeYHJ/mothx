package esm

import (
	"context"
	"reflect"
	"testing"

	"github.com/startvibecoding/mothx/internal/session"
)

type runtimeTestAdapter struct {
	responses map[Role]string
	roles     []Role
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
	if _, err := store.Create(ctx, sessionID, "finish the objective", nil); err != nil {
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
	if _, err := store.Create(ctx, sessionID, "finish the objective", nil); err != nil {
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
	if _, err := store.Create(ctx, sessionID, "finish the objective", nil); err != nil {
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
	if _, err := store.Create(ctx, sessionID, "finish the objective", nil); err != nil {
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

func TestSupervisorSharedStorePersistsAcrossRuntimeInstances(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish the objective", nil); err != nil {
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
