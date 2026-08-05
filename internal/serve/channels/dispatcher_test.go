package channels

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agent "github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/cron"
	"github.com/startvibecoding/mothx/internal/messaging"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/serve/hooks"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/tools"
	"github.com/startvibecoding/mothx/internal/workflow"
)

type recordingChannelProvider struct {
	models []*provider.Model
	calls  []provider.ChatParams
}

func newRecordingChannelProvider() *recordingChannelProvider {
	return &recordingChannelProvider{
		models: []*provider.Model{{ID: "m1", Name: "Model 1", ContextWindow: 4096, MaxTokens: 1024}},
	}
}

func TestChannelRouteIDUsesConversationIDForMessagingChannels(t *testing.T) {
	tests := []struct {
		name string
		msg  messaging.InboundMessage
		want string
	}{
		{
			name: "feishu chat id",
			msg:  messaging.InboundMessage{Platform: "feishu", UserID: "ou_sender", ChatID: "oc_chat"},
			want: "oc_chat",
		},
		{
			name: "wechat chat id",
			msg:  messaging.InboundMessage{Platform: "wechat", UserID: "user", ChatID: "chat"},
			want: "chat",
		},
		{
			name: "fallback user id",
			msg:  messaging.InboundMessage{Platform: "ws", UserID: "user", ChatID: "chat"},
			want: "user",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := channelRouteID(tt.msg); got != tt.want {
				t.Fatalf("channelRouteID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatAttachmentSummary(t *testing.T) {
	got := formatAttachmentSummary([]provider.Attachment{
		{Kind: "citation", Name: "OpenAI", URL: "https://openai.com"},
		{Kind: "file", ProviderRef: "file_123"},
		{Kind: "citation", Name: "OpenAI", URL: "https://openai.com"},
	})
	for _, want := range []string{"Attachments:", "OpenAI: https://openai.com", "file: file_123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary = %q, want %q", got, want)
		}
	}
	if strings.Count(got, "https://openai.com") != 1 {
		t.Fatalf("summary should deduplicate attachments: %q", got)
	}
}

func (p *recordingChannelProvider) Chat(ctx context.Context, params provider.ChatParams) <-chan provider.StreamEvent {
	p.calls = append(p.calls, provider.ChatParams{
		Messages:     append([]provider.Message(nil), params.Messages...),
		SystemPrompt: params.SystemPrompt,
	})
	ch := make(chan provider.StreamEvent, 3)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{Type: provider.StreamStart}
		ch <- provider.StreamEvent{Type: provider.StreamTextDelta, TextDelta: "ok"}
		ch <- provider.StreamEvent{Type: provider.StreamDone}
	}()
	return ch
}

func (p *recordingChannelProvider) Name() string              { return "recording-channel" }
func (p *recordingChannelProvider) API() string               { return "openai-chat" }
func (p *recordingChannelProvider) Models() []*provider.Model { return p.models }
func (p *recordingChannelProvider) GetModel(id string) *provider.Model {
	for _, m := range p.models {
		if m.ID == id {
			return m
		}
	}
	return nil
}

type failingChannelProvider struct {
	model *provider.Model
}

func (p *failingChannelProvider) Chat(context.Context, provider.ChatParams) <-chan provider.StreamEvent {
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.StreamStart}
	ch <- provider.StreamEvent{Type: provider.StreamError, Error: fmt.Errorf("upstream returned HTTP 522")}
	close(ch)
	return ch
}
func (p *failingChannelProvider) Name() string              { return "failing-channel" }
func (p *failingChannelProvider) API() string               { return "openai-chat" }
func (p *failingChannelProvider) Models() []*provider.Model { return []*provider.Model{p.model} }
func (p *failingChannelProvider) GetModel(id string) *provider.Model {
	if p.model != nil && p.model.ID == id {
		return p.model
	}
	return nil
}

func TestHandleMessagePersistsChannelFailureEvent(t *testing.T) {
	workDir := t.TempDir()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = workDir
	model := &provider.Model{ID: "m1", ContextWindow: 32768, MaxTokens: 1024}
	d := &Dispatcher{
		cfg: cfg, settings: settings, allow: &config.AllowConfig{}, sessionDir: settings.SessionDir,
		security: NewSecurity(cfg), hooksMgr: hooks.NewManager("", ""),
		provider: &failingChannelProvider{model: model}, model: model,
		sessions: make(map[string]*ChannelSession),
	}
	_, err := d.HandleMessage(context.Background(), messaging.InboundMessage{Platform: "wechat", UserID: "failure-user", Text: "继续"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 522") {
		t.Fatalf("HandleMessage error = %v, want HTTP 522", err)
	}
	sess, err := d.resolveSession("wechat", "failure-user")
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	events, err := session.ListSessionRunEvents(settings.SessionDir, sess.ID)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	if len(events) != 2 || events[1].EventType != "failed" || events[1].Status != "failed" || !strings.Contains(string(events[1].Data), "HTTP 522") {
		t.Fatalf("run events = %#v, want started and failed HTTP 522", events)
	}
}

func TestCancelChannelSessionRunAbortsActiveRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := &ChannelSession{ID: "channels/wechat/cancel-user"}
	sess.runStateMu.Lock()
	sess.runID = "channel-run"
	sess.runCancel = cancel
	sess.runStateMu.Unlock()
	d := &Dispatcher{
		sessionDir: t.TempDir(),
		sessions:   map[string]*ChannelSession{sess.ID: sess},
	}
	if !d.CancelChannelSessionRun(sess.ID) {
		t.Fatal("CancelChannelSessionRun returned false for active run")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("active channel context was not cancelled")
	}
}

func TestResolveSessionCronOnlyDoesNotExposeSubAgentTools(t *testing.T) {
	workDir := t.TempDir()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = workDir
	cfg.MultiAgent = false
	cfg.Cron.Enabled = true

	store := cron.NewSQLiteCronStore(t.TempDir())
	p := newRecordingChannelProvider()
	d := &Dispatcher{
		cfg:        cfg,
		settings:   settings,
		allow:      &config.AllowConfig{},
		sessionDir: settings.SessionDir,
		security:   NewSecurity(cfg),
		hooksMgr:   hooks.NewManager("", ""),
		provider:   p,
		model:      p.models[0],
		cronStore:  store,
		sessions:   make(map[string]*ChannelSession),
	}

	if d.EnsureAgentManager() == nil {
		t.Fatal("cron should be able to initialize an agent manager without multi-agent")
	}
	sess, err := d.resolveSession("ws", "test-user")
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	if _, ok := sess.Registry.Get("cron"); !ok {
		t.Fatal("cron-only session should expose cron tool")
	}
	if sess.ID != sess.Manager.GetHeader().ID {
		t.Fatalf("channel session ID = %q, want canonical session ID %q", sess.ID, sess.Manager.GetHeader().ID)
	}
	if sess.ID == sessionKey("ws", "test-user") {
		t.Fatal("channel session ID must not be the routing key")
	}
	for _, name := range []string{"subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy"} {
		if _, ok := sess.Registry.Get(name); ok {
			t.Fatalf("cron-only session should not expose %s", name)
		}
	}
}

func TestRefreshBindingRemovesCachedRoute(t *testing.T) {
	d := &Dispatcher{sessions: make(map[string]*ChannelSession)}
	key := sessionKey("wechat", "user-a")
	d.sessions[key] = &ChannelSession{ID: "session-1", Platform: "wechat", UserID: "user-a"}

	d.RefreshBinding("wechat", "user-a")
	if got := d.GetSession(key); got != nil {
		t.Fatalf("cached binding route was not removed: %#v", got)
	}
}

func TestSessionLeaseDefersInvalidatedEvictionUntilRelease(t *testing.T) {
	d := &Dispatcher{sessions: make(map[string]*ChannelSession)}
	key := sessionKey("wechat", "lease-user")
	sess := &ChannelSession{ID: "session-lease", Platform: "wechat", UserID: "lease-user"}
	d.sessions[key] = sess

	lease, ok := d.acquireSessionLease(key, "wechat", "lease-user", sess)
	if !ok {
		t.Fatal("acquireSessionLease failed")
	}
	d.mu.Lock()
	d.invalidateSessionLocked(key, sess)
	d.mu.Unlock()
	if d.GetSession(key) == nil {
		t.Fatal("invalidated session was evicted while a request was pending")
	}
	lease.release()
	if d.GetSession(key) != nil {
		t.Fatal("invalidated session was not evicted after the last lease released")
	}
}

func TestToolCatalogReportsRuntimeAvailability(t *testing.T) {
	d := &Dispatcher{cfg: DefaultConfig()}
	items := d.ToolCatalog("wechat")
	byName := make(map[string]ToolCatalogItem, len(items))
	for _, item := range items {
		byName[item.Name] = item
	}
	// Runtime-dependent tool that cannot be enabled without its runtime: a2a is
	// unavailable (and unchecked) when the A2A master feature is disabled.
	a2aItem, ok := byName["a2a_dispatch"]
	if !ok {
		t.Fatalf("catalog missing %q", "a2a_dispatch")
	}
	if a2aItem.Available || a2aItem.Default || a2aItem.UnavailableReason == "" {
		t.Fatalf("catalog item %q = %#v, want unavailable with reason", "a2a_dispatch", a2aItem)
	}
	// The browser and multi-agent tools are always selectable; their feature
	// flags only decide the default checked state, so with the flags off they
	// are unchecked but still available.
	for _, name := range []string{"browser", "delegate_subagent", "subagent_spawn", "workflow_run"} {
		item, ok := byName[name]
		if !ok {
			t.Fatalf("catalog missing %q", name)
		}
		if !item.Available {
			t.Fatalf("catalog item %q available=%v, want selectable when feature flag is off", name, item.Available)
		}
		if item.Default {
			t.Fatalf("catalog item %q default=%v, want unchecked when feature flag is off", name, item.Default)
		}
	}
}

func TestToolCatalogMatchesResolvedRegistryContract(t *testing.T) {
	workDir := t.TempDir()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = workDir
	p := newRecordingChannelProvider()
	d := &Dispatcher{
		cfg: cfg, settings: settings, allow: &config.AllowConfig{}, sessionDir: settings.SessionDir,
		security: NewSecurity(cfg), hooksMgr: hooks.NewManager("", ""), provider: p, model: p.models[0],
		sessions: make(map[string]*ChannelSession), identityLocks: session.NewIdentityLocks(),
	}
	sess, err := d.resolveSession("ws", "tool-contract")
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool)
	for _, tool := range sess.Registry.All() {
		registered[tool.Name()] = true
	}
	for _, item := range d.ToolCatalog("ws") {
		if item.Available && item.Default && !registered[item.Name] {
			t.Errorf("catalog says %q is available/default but registry omitted it", item.Name)
		}
		if !item.Available && registered[item.Name] {
			t.Errorf("catalog says %q is unavailable but registry registered it", item.Name)
		}
	}
}

// TestToolCatalogMultiAgentFlagControlsDefaultOnly verifies that turning the
// multiAgent feature off keeps the multi-agent tools selectable (available),
// only flipping their default checked state. Explicitly enabling such a tool
// must still register it in the resolved session registry.
func TestToolCatalogMultiAgentFlagControlsDefaultOnly(t *testing.T) {
	workDir := t.TempDir()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = workDir
	cfg.MultiAgent = false // feature flag OFF
	p := newRecordingChannelProvider()
	d := &Dispatcher{
		cfg: cfg, settings: settings, allow: &config.AllowConfig{}, sessionDir: settings.SessionDir,
		security: NewSecurity(cfg), hooksMgr: hooks.NewManager("", ""), provider: p, model: p.models[0],
		sessions: make(map[string]*ChannelSession), identityLocks: session.NewIdentityLocks(),
	}

	// Catalog: multi-agent tools must be available but unchecked by default.
	for _, item := range d.ToolCatalog("wechat") {
		switch item.Name {
		case "delegate_subagent", "subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy",
			"workflow_lint", "workflow_run", "workflow_status", "workflow_cancel", "browser":
			if !item.Available {
				t.Errorf("catalog %q available=%v, want selectable even when multiAgent/browser is off", item.Name, item.Available)
			}
			if item.Default {
				t.Errorf("catalog %q default=%v, want unchecked when multiAgent/browser is off", item.Name, item.Default)
			}
		}
	}

	// Explicitly enabling a multi-agent tool must register it at runtime.
	binding, err := session.CreateBound(workDir, settings.SessionDir, "wechat", "ma-default-only")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.SetChannelTools(settings.SessionDir, binding.GetHeader().ID, []session.ChannelToolConfig{
		{ToolName: "subagent_spawn", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	sess, err := d.resolveSession("wechat", "ma-default-only")
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool)
	for _, tool := range sess.Registry.All() {
		registered[tool.Name()] = true
	}
	if !registered["subagent_spawn"] {
		t.Fatalf("subagent_spawn not registered despite explicit enable; got %v", registered)
	}
	if !registered["workflow_run"] {
		t.Fatalf("workflow_run not registered despite explicit enable")
	}
}

func TestToolCatalogEveryAvailableSelectionMatchesRegistry(t *testing.T) {
	workDir := t.TempDir()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()
	cfg := DefaultConfig()
	cfg.WorkDir = workDir
	p := newRecordingChannelProvider()
	d := &Dispatcher{
		cfg: cfg, settings: settings, allow: &config.AllowConfig{}, sessionDir: settings.SessionDir,
		security: NewSecurity(cfg), hooksMgr: hooks.NewManager("", ""), provider: p, model: p.models[0],
		sessions: make(map[string]*ChannelSession), identityLocks: session.NewIdentityLocks(),
	}
	binding, err := session.CreateBound(workDir, settings.SessionDir, "wechat", "catalog-contract")
	if err != nil {
		t.Fatal(err)
	}
	selections := make([]session.ChannelToolConfig, 0)
	for _, item := range d.ToolCatalog("wechat") {
		selections = append(selections, session.ChannelToolConfig{ToolName: item.Name, Enabled: item.Available})
	}
	if err := session.SetChannelTools(settings.SessionDir, binding.GetHeader().ID, selections); err != nil {
		t.Fatal(err)
	}
	sess, err := d.resolveSession("wechat", "catalog-contract")
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool)
	for _, tool := range sess.Registry.All() {
		registered[tool.Name()] = true
	}
	for _, item := range d.ToolCatalog("wechat") {
		if registered[item.Name] != item.Available {
			t.Errorf("tool %q registered=%v, catalog available=%v", item.Name, registered[item.Name], item.Available)
		}
	}
}

func TestBuildAgentLoadsReplayState(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()
	settings.SessionDir = t.TempDir()

	mgr := session.New(tmpDir, settings.SessionDir)
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	oldUser := provider.NewUserMessage("old user context")
	oldAssistant := provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "old assistant context"}})
	recentUser := provider.NewUserMessage("recent user context")
	recentAssistant := provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "recent assistant context"}})
	_, _ = mgr.AppendMessage(oldUser)
	_, _ = mgr.AppendMessage(oldAssistant)
	recentUserID, _ := mgr.AppendMessage(recentUser)
	_, _ = mgr.AppendMessage(recentAssistant)
	_, _ = mgr.AppendCompaction("## Goal\ncompacted checkpoint", recentUserID, 100)

	registry := tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox())
	d := &Dispatcher{
		cfg:      DefaultConfig(),
		settings: settings,
		hooksMgr: hooks.NewManager("", ""),
		provider: p,
		model:    p.models[0],
	}
	sess := &ChannelSession{
		ID:       "channels/ws/test-user",
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: registry,
		Mode:     "agent",
	}

	a, cleanup := d.buildAgent(context.Background(), sess, nil)
	defer cleanup(nil)

	for range a.Run(context.Background(), "continue") {
	}

	if len(p.calls) != 1 {
		t.Fatalf("provider call count = %d, want 1", len(p.calls))
	}

	foundSummary := false
	foundOldUser := false
	foundRecentUser := false
	for _, msg := range p.calls[0].Messages {
		if msg.SystemInjected && msg.Content == "## Goal\ncompacted checkpoint" {
			foundSummary = true
		}
		if msg.Content == oldUser.Content {
			foundOldUser = true
		}
		if msg.Content == recentUser.Content {
			foundRecentUser = true
		}
	}

	if !foundSummary {
		t.Fatal("channel agent did not replay compacted summary")
	}
	if foundOldUser {
		t.Fatal("channel agent still included pre-compaction old user message")
	}
	if !foundRecentUser {
		t.Fatal("channel agent lost recent user message from replay state")
	}
}

func TestBuildAgentUsesCompactionSettings(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()
	settings.Compaction.KeepRecentTokens = 1

	mgr := session.New(tmpDir, t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	_, _ = mgr.AppendMessage(provider.NewUserMessage("old user context"))
	_, _ = mgr.AppendMessage(provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "old assistant context"}}))
	_, _ = mgr.AppendMessage(provider.NewUserMessage("recent user context"))
	_, _ = mgr.AppendMessage(provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "recent assistant context"}}))

	d := &Dispatcher{
		cfg:      DefaultConfig(),
		settings: settings,
		hooksMgr: hooks.NewManager("", ""),
		provider: p,
		model:    p.models[0],
	}
	sess := &ChannelSession{
		ID:       "channels/ws/test-user",
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox()),
		Mode:     "agent",
	}

	a, cleanup := d.buildAgent(context.Background(), sess, nil)
	defer cleanup(nil)

	if !a.CanCompact() {
		t.Fatal("agent should use channel compaction keepRecent settings")
	}
}

func TestCompactCommandRunsImmediately(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()
	settings.Compaction.KeepRecentTokens = 1

	mgr := session.New(tmpDir, t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	_, _ = mgr.AppendMessage(provider.NewUserMessage("old user context"))
	_, _ = mgr.AppendMessage(provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "old assistant context"}}))
	_, _ = mgr.AppendMessage(provider.NewUserMessage("recent user context"))
	_, _ = mgr.AppendMessage(provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "recent assistant context"}}))

	sess := &ChannelSession{
		ID:       sessionKey("ws", "test-user"),
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox()),
		Mode:     "agent",
	}
	d := &Dispatcher{
		cfg:      DefaultConfig(),
		settings: settings,
		hooksMgr: hooks.NewManager("", ""),
		provider: p,
		model:    p.models[0],
		sessions: map[string]*ChannelSession{sess.ID: sess},
	}

	reply, err := d.handleCommand(messaging.InboundMessage{Platform: "ws", UserID: "test-user", Text: "/compact"})
	if err != nil {
		t.Fatalf("handleCommand() error = %v", err)
	}
	if !strings.Contains(reply, "compacted") {
		t.Fatalf("reply = %q, want compaction confirmation", reply)
	}
	if sess.ForceCompact {
		t.Fatal("ForceCompact should not be set for immediate compaction")
	}
	replay := mgr.GetReplayState()
	if len(replay.Messages) == 0 || !replay.Messages[0].SystemInjected {
		t.Fatalf("expected compacted summary in replay, got %#v", replay.Messages)
	}
}

func TestCompactCommandForcesSummaryOnlyWhenOnlyRecentContext(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()

	mgr := session.New(tmpDir, t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}
	_, _ = mgr.AppendMessage(provider.NewUserMessage("hello"))
	_, _ = mgr.AppendMessage(provider.NewAssistantMessage([]provider.ContentBlock{{Type: "text", Text: "hi"}}))

	sess := &ChannelSession{
		ID:       sessionKey("ws", "test-user"),
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox()),
		Mode:     "agent",
	}
	d := &Dispatcher{
		cfg:      DefaultConfig(),
		settings: settings,
		hooksMgr: hooks.NewManager("", ""),
		provider: p,
		model:    p.models[0],
		sessions: map[string]*ChannelSession{sess.ID: sess},
	}

	reply, err := d.handleCommand(messaging.InboundMessage{Platform: "ws", UserID: "test-user", Text: "/compact"})
	if err != nil {
		t.Fatalf("handleCommand() error = %v", err)
	}
	if sess.ForceCompact {
		t.Fatal("ForceCompact should not be set for immediate compaction")
	}
	if !strings.Contains(reply, "compacted") {
		t.Fatalf("reply = %q, want compaction confirmation", reply)
	}
	replay := mgr.GetReplayState()
	if len(replay.Messages) != 1 || !replay.Messages[0].SystemInjected {
		t.Fatalf("expected summary-only replay, got %#v", replay.Messages)
	}
}

func TestBuildAgentPromptFlagsFollowSessionRegistry(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()

	mgr := session.New(tmpDir, t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}

	d := &Dispatcher{
		cfg:      DefaultConfig(),
		settings: settings,
		hooksMgr: hooks.NewManager("", ""),
		provider: p,
		model:    p.models[0],
	}
	manager := d.ensureAgentManager()
	if manager == nil {
		t.Fatal("ensureAgentManager returned nil")
	}

	reg := tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox())
	agent.RegisterSubAgentTools(reg, manager)
	agent.RegisterDelegateSubAgentTool(reg, manager)
	workflow.RegisterTools(reg, manager, nil)

	sess := &ChannelSession{
		ID:       "channels/ws/test-user",
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: reg,
		Mode:     "yolo",
	}

	a, cleanup := d.buildAgent(context.Background(), sess, nil)
	defer cleanup(nil)
	for range a.Run(context.Background(), "continue") {
	}

	if len(p.calls) != 1 {
		t.Fatalf("provider call count = %d, want 1", len(p.calls))
	}
	sp := p.calls[0].SystemPrompt
	for _, section := range []string{"## Sub-Agent Tools", "## Delegation Mode", "## Workflow Tools"} {
		if !strings.Contains(sp, section) {
			t.Errorf("system prompt missing %q although the corresponding tools are registered", section)
		}
	}
}

func TestBuildAgentPromptOmitsSubAgentSectionsWhenToolsAbsent(t *testing.T) {
	tmpDir := t.TempDir()
	p := newRecordingChannelProvider()
	settings := config.DefaultSettings()

	mgr := session.New(tmpDir, t.TempDir())
	if err := mgr.Init(); err != nil {
		t.Fatalf("init session: %v", err)
	}

	d := &Dispatcher{
		cfg:        DefaultConfig(),
		settings:   settings,
		hooksMgr:   hooks.NewManager("", ""),
		provider:   p,
		model:      p.models[0],
		multiAgent: true, // dispatcher flag alone must not inject prompt sections
	}

	sess := &ChannelSession{
		ID:       "channels/ws/test-user",
		Platform: "ws",
		UserID:   "test-user",
		WorkDir:  tmpDir,
		Manager:  mgr,
		Registry: tools.NewRegistry(tmpDir, sandbox.NewNoneSandbox()),
		Mode:     "yolo",
	}

	a, cleanup := d.buildAgent(context.Background(), sess, nil)
	defer cleanup(nil)
	for range a.Run(context.Background(), "continue") {
	}

	if len(p.calls) != 1 {
		t.Fatalf("provider call count = %d, want 1", len(p.calls))
	}
	sp := p.calls[0].SystemPrompt
	for _, section := range []string{"## Sub-Agent Tools", "## Delegation Mode", "## Workflow Tools"} {
		if strings.Contains(sp, section) {
			t.Errorf("system prompt contains %q although no sub-agent tools are registered", section)
		}
	}
}
