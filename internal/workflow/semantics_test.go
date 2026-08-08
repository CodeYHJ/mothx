package workflow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParallelAggregatesFailuresAndCancelsSiblings(t *testing.T) {
	started := make(chan string, 3)
	var once sync.Once
	host := &funcHost{run: func(ctx context.Context, task AgentTask) (AgentResult, error) {
		started <- task.Name
		if task.Name == "fail" {
			once.Do(func() {})
			return AgentResult{}, errors.New("boom")
		}
		<-ctx.Done()
		return AgentResult{}, ctx.Err()
	}}
	r := &Runner{Host: host, Concurrency: 3, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("parallel", {phases:[phase("p", parallel(agent("fail", {prompt:"fail"}), agent("wait-a", {prompt:"wait"}), agent("wait-b", {prompt:"wait"})))]});`)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v", err)
	}
	if state.Status != StatusError {
		t.Fatalf("status = %s", state.Status)
	}
}

func TestResultsAndLogsAreDeterministicallyOrdered(t *testing.T) {
	rt := testRuntime()
	t0 := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	rt.state.Results["p.b"] = AgentResult{Key: "p.b", Phase: "p", Name: "b", StartedAt: t0}
	rt.state.Results["p.a"] = AgentResult{Key: "p.a", Phase: "p", Name: "a", StartedAt: t0}
	rt.state.Results["p.c"] = AgentResult{Key: "p.c", Phase: "p", Name: "c", StartedAt: t0.Add(time.Second)}
	got := rt.resultsText("p")
	if !strings.Contains(got, "p.a:\n") || strings.Index(got, "p.a:\n") > strings.Index(got, "p.b:\n") {
		t.Fatalf("results order = %q", got)
	}
	_, err := resolveJSValue(rt, context.Background(), &jsExpr{kind: "log", args: []any{"hello", "world"}})
	if err != nil || len(rt.state.Logs) != 1 || rt.state.Logs[0].Message != "hello world" {
		t.Fatalf("logs = %#v err=%v", rt.state.Logs, err)
	}
}

type funcHost struct {
	run func(context.Context, AgentTask) (AgentResult, error)
}

func (h *funcHost) RunAgent(ctx context.Context, task AgentTask) (AgentResult, error) {
	return h.run(ctx, task)
}
