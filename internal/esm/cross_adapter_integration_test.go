package esm

import (
	"context"
	"reflect"
	"testing"
)

type namedAdapter struct {
	name  string
	roles []Role
	resp  string
}

func (a *namedAdapter) RunRole(_ context.Context, req RoleRequest) (RoleResult, error) {
	a.roles = append(a.roles, req.Role)
	return RoleResult{Response: a.resp, ToolCalls: 1, ToolError: map[string]bool{}}, nil
}

func (a *namedAdapter) RunRecoveryObserver(context.Context, RoleRequest, error) (RoleResult, error) {
	return RoleResult{}, context.Canceled
}

func TestTUIAndWebUIAdaptersContinueSamePersistedObjective(t *testing.T) {
	store, sessionID := newRuntimeTestStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, sessionID, "finish shared objective"); err != nil {
		t.Fatal(err)
	}
	workerResponse := `{"status":"continue","summary":"TUI inspected the repository","evidence":["read source"],"remaining_work":["finish implementation"],"blockers":[]}`
	tui := &namedAdapter{name: "tui", resp: workerResponse}
	webui := &namedAdapter{name: "webui", resp: workerResponse}

	first, err := (&Supervisor{Store: store, Adapter: tui}).Run(ctx, sessionID, "tui-run", t.TempDir(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	second, err := (&Supervisor{Store: store, Adapter: webui}).Run(ctx, sessionID, "webui-run", t.TempDir(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusActive || second.Status != StatusActive {
		t.Fatalf("statuses=%s/%s", first.Status, second.Status)
	}
	if !reflect.DeepEqual(tui.roles, []Role{RoleWorker}) || !reflect.DeepEqual(webui.roles, []Role{RoleWorker}) {
		t.Fatalf("adapter roles=%s:%v %s:%v", tui.name, tui.roles, webui.name, webui.roles)
	}
	persisted, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ProgressSummary != "TUI inspected the repository" || len(persisted.RemainingWork) != 1 {
		t.Fatalf("persisted=%#v", persisted)
	}
}
