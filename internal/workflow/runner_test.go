package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu                  sync.Mutex
	running, maxRunning int
	tasks               []AgentTask
	resultsByName       map[string]string
}

func (h *fakeHost) RunAgent(ctx context.Context, task AgentTask) (AgentResult, error) {
	h.mu.Lock()
	h.running++
	if h.running > h.maxRunning {
		h.maxRunning = h.running
	}
	h.tasks = append(h.tasks, task)
	h.mu.Unlock()
	select {
	case <-time.After(5 * time.Millisecond):
	case <-ctx.Done():
		return AgentResult{}, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.resultsByName[task.Name]
	if out == "" {
		out = fmt.Sprintf("%s:%s", task.Name, task.Prompt)
	}
	h.running--
	return AgentResult{Result: out}, nil
}

func TestRunnerExecutesJavaScriptWorkflow(t *testing.T) {
	host := &fakeHost{resultsByName: map[string]string{"api": "api findings", "channels": "channels findings"}}
	r := &Runner{Host: host, Concurrency: 2, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("auth audit", {concurrency:2, phases:[phase("scan", parallel(agent("api", {mode:"plan", tools:["read","grep"], prompt:"scan api"}), agent("channels", {mode:"plan", tools:["read","grep"], prompt:"scan channels"}))), phase("verify", agent("cross-check", {mode:"plan", prompt:"verify prior findings"}))]});`)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusDone {
		t.Fatalf("status=%s", state.Status)
	}
	if state.Results["scan.api"].Result != "api findings" {
		t.Fatalf("result=%#v", state.Results)
	}
	if host.maxRunning > 2 {
		t.Fatalf("max=%d", host.maxRunning)
	}
}
func TestRunnerRejectsInvalidJavaScriptAgentOption(t *testing.T) {
	r := &Runner{Host: &fakeHost{}, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("bad",{phases:[phase("scan",agent("worker",{prompt:"bad",unknown:true}))]});`)
	if err == nil || !strings.Contains(err.Error(), "unknown agent option") || state.Status != StatusError {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}
func TestRunnerReportsMissingResult(t *testing.T) {
	r := &Runner{Host: &fakeHost{}, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("bad",{phases:[phase("verify",agent("cross-check",{prompt:result("scan.missing")}))]});`)
	if err == nil || state.Status != StatusError {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

type blockingHost struct {
	started chan struct{}
	once    sync.Once
}

func (h *blockingHost) RunAgent(ctx context.Context, task AgentTask) (AgentResult, error) {
	h.once.Do(func() { close(h.started) })
	<-ctx.Done()
	return AgentResult{}, ctx.Err()
}

func findTask(tasks []AgentTask, name string) *AgentTask {
	for i := range tasks {
		if tasks[i].Name == name {
			return &tasks[i]
		}
	}
	return nil
}

func equalStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunnerConcurrencyLimitAndSemaphoreReuse(t *testing.T) {
	host := &fakeHost{resultsByName: map[string]string{}}
	r := &Runner{Host: host, Concurrency: 1, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("bounded", {phases:[phase("p", parallel(agent("a", {prompt:"a"}), agent("b", {prompt:"b"}), agent("c", {prompt:"c"})))]});`)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusDone {
		t.Fatalf("status = %s, want done", state.Status)
	}
	if host.maxRunning != 1 {
		t.Fatalf("max concurrent workers = %d, want 1", host.maxRunning)
	}
	if got := cap((&runtime{concurrency: 2}).semaphore()); got != 2 {
		t.Fatalf("semaphore capacity = %d, want 2", got)
	}
}

func TestRunnerKeyedResultsAndFanIn(t *testing.T) {
	host := &promptHost{}
	r := &Runner{Host: host, Concurrency: 2, Now: fixedClock()}
	state, err := r.Run(context.Background(), `workflow("keyed", {phases:[phase("scan", [agent("worker", {key:"r0", prompt:"item 0"}), agent("worker", {key:"r1", prompt:"item 1"})]), phase("verify", agent("check", {prompt:results("scan")}))]});`)
	if err != nil {
		t.Fatal(err)
	}
	if state.Results["scan.worker[r0]"].Status != StatusDone || state.Results["scan.worker[r1]"].Status != StatusDone {
		t.Fatalf("keyed results = %#v", state.Results)
	}
	if got := host.promptFor("check"); !strings.Contains(got, "item 0") || !strings.Contains(got, "scan.worker[r1]:") {
		t.Fatalf("fan-in prompt = %q", got)
	}
}

func TestFileStorePersistsLoadsAndListsWorkflowState(t *testing.T) {
	store := NewFileStore(t.TempDir())
	started := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	state := &RunState{ID: "run-1", Name: "demo", Status: StatusDone, StartedAt: started, UpdatedAt: started, Results: map[string]AgentResult{"p.a": {Key: "p.a", Result: "ok"}}}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "run-1")
	if err != nil || loaded.Results["p.a"].Result != "ok" {
		t.Fatalf("loaded = %#v, err = %v", loaded, err)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 || listed[0].ID != "run-1" {
		t.Fatalf("listed = %#v, err = %v", listed, err)
	}
	if err := store.Save(context.Background(), &RunState{}); err == nil {
		t.Fatal("expected missing ID error")
	}
	if _, err := store.Load(context.Background(), "../escape"); err == nil {
		t.Fatal("expected invalid/path-like ID to fail or miss")
	}
}

type promptHost struct {
	mu      sync.Mutex
	prompts map[string]string
}

func (h *promptHost) RunAgent(ctx context.Context, task AgentTask) (AgentResult, error) {
	select {
	case <-ctx.Done():
		return AgentResult{}, ctx.Err()
	default:
	}
	h.mu.Lock()
	if h.prompts == nil {
		h.prompts = map[string]string{}
	}
	h.prompts[task.Name] = task.Prompt
	h.mu.Unlock()
	return AgentResult{Result: task.Prompt}, nil
}

func (h *promptHost) promptFor(name string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.prompts[name]
}

func fixedClock() func() time.Time {
	var mu sync.Mutex
	t := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Millisecond)
		return t
	}
}

type memoryStore struct {
	mu     sync.Mutex
	saved  int
	states []*RunState
}

func (s *memoryStore) Save(ctx context.Context, state *RunState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *state
	s.states = append(s.states, &cp)
	s.saved++
	return nil
}

func (s *memoryStore) Load(ctx context.Context, id string) (*RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.states) - 1; i >= 0; i-- {
		state := s.states[i]
		if state.ID == id {
			cp := *state
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (s *memoryStore) List(ctx context.Context) ([]RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RunState, 0, len(s.states))
	for _, state := range s.states {
		out = append(out, *state)
	}
	return out, nil
}

func waitForWorkflowID(t testing.TB, store *memoryStore) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		for _, state := range store.states {
			if state.ID != "" {
				id := state.ID
				store.mu.Unlock()
				return id
			}
		}
		store.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for workflow id")
	return ""
}
