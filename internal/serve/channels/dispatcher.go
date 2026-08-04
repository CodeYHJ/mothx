package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/a2a"
	"github.com/startvibecoding/mothx/internal/agent"
	browserfeature "github.com/startvibecoding/mothx/internal/browser"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/contextfiles"
	"github.com/startvibecoding/mothx/internal/cron"
	"github.com/startvibecoding/mothx/internal/mcp"
	"github.com/startvibecoding/mothx/internal/memory"
	"github.com/startvibecoding/mothx/internal/messaging"
	"github.com/startvibecoding/mothx/internal/provider"
	providerfactory "github.com/startvibecoding/mothx/internal/provider/factory"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/serve/hooks"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
	"github.com/startvibecoding/mothx/internal/util"
	"github.com/startvibecoding/mothx/internal/workflow"
)

// ToolCatalogItem describes a tool that may be configured for channel sessions.
type ToolCatalogItem struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Default   bool   `json:"default"`
}

// ToolCatalog returns the complete channel tool catalog. Startup options determine
// each dynamic tool's default selection; every catalog item remains selectable per session.
func (d *Dispatcher) ToolCatalog(platform string) []ToolCatalogItem {
	workDir := d.cfg.GetPlatformWorkDir(platform)
	reg := tools.NewRegistry(workDir, nil)
	reg.RegisterDefaults()
	seen := map[string]bool{}
	result := make([]ToolCatalogItem, 0)
	add := func(name string, available, defaultEnabled bool) {
		if seen[name] {
			return
		}
		seen[name] = true
		result = append(result, ToolCatalogItem{Name: name, Available: available, Default: defaultEnabled})
	}
	for _, item := range reg.All() {
		add(item.Name(), true, true)
	}
	add("browser", true, d.browser)
	add("memory", true, true)
	add("cron", d.cronStore != nil, d.cronStore != nil)
	add("a2a_dispatch", true, d.a2aMaster)
	add("delegate_subagent", true, d.multiAgent)
	for _, name := range []string{"subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy"} {
		add(name, true, d.multiAgent)
	}
	for _, name := range []string{"workflow_lint", "workflow_run", "workflow_status", "workflow_cancel"} {
		add(name, true, d.multiAgent)
	}
	return result
}

type agentApprovalHandler func(toolCallID, toolName string, args map[string]any) bool

// Dispatcher routes messages to per-user agent sessions.
type Dispatcher struct {
	mu         sync.RWMutex
	cfg        *Config
	settings   *config.Settings
	allow      *config.AllowConfig
	version    string
	sessionDir string
	security   *Security
	hooksMgr   *hooks.Manager

	// Cached provider/model for creating agent instances
	provider     provider.Provider
	providerName string // user-configured vendor name
	model        *provider.Model

	// Multi-agent mode
	multiAgent bool
	agentMgr   *agent.AgentManager

	// Cron
	cronStore cron.CronStore
	scheduler *cron.Scheduler

	// Sandbox mode
	sandbox    bool
	sandboxMgr *sandbox.Manager
	browser    bool
	a2aMaster  bool

	// Active sessions: key = "<platform-channel>/<user_id>"
	sessions map[string]*ChannelSession
	// Optional callback invoked when a channel session execution state changes.
	runObserver func(string)
}

// ChannelSession holds state for a single channel user session.
type ChannelSession struct {
	ID         string // e.g. "channels/wechat/wxid_user1"
	Platform   string // "wechat", "feishu", "ws"
	UserID     string
	WorkDir    string
	Manager    *session.Manager
	SandboxMgr *sandbox.Manager
	Registry   *tools.Registry
	MCPClients []*mcp.Client // connected MCP clients (nil if none)
	Mode       string
	LastUsed   time.Time
	mu         sync.Mutex // serializes requests within this session
	// ForceCompact is a legacy/session flag consumed by the next agent run.
	// The /compact command now executes compaction immediately.
	ForceCompact bool
	runStateMu   sync.Mutex
	runID        string
	runCancel    context.CancelFunc
}

// Lock acquires the session lock.
func (s *ChannelSession) Lock() { s.mu.Lock() }

// Unlock releases the session lock.
func (s *ChannelSession) Unlock() { s.mu.Unlock() }

// Touch updates the last-used timestamp.
func (s *ChannelSession) Touch() { s.LastUsed = time.Now() }

// NewDispatcher creates a dispatcher with the given configuration.
func NewDispatcher(cfg *Config, settings *config.Settings, version string, cronStore cron.CronStore, scheduler *cron.Scheduler) (*Dispatcher, error) {
	if cfg.WebSearch {
		settings.WebSearch.Enabled = config.BoolPtr(true)
	}
	providerName := cfg.GetDefaultProvider(settings.DefaultProvider)
	modelID := cfg.GetDefaultModel(settings.DefaultModel)

	p, model, err := providerfactory.Create(settings, providerName, modelID)
	if err != nil {
		return nil, fmt.Errorf("create provider: %w", err)
	}

	d := &Dispatcher{
		cfg:          cfg,
		settings:     settings,
		allow:        config.LoadAllow(),
		version:      version,
		sessionDir:   settings.GetSessionDir(),
		security:     NewSecurity(cfg),
		hooksMgr:     hooks.NewManager(cfg.Hooks.PreToolCall, cfg.Hooks.PostToolCall),
		provider:     p,
		providerName: providerName,
		model:        model,
		multiAgent:   cfg.MultiAgent,
		sandbox:      cfg.Sandbox,
		sandboxMgr:   sandbox.NewManagerWithOptions(cfg.GetWorkDir(), settings.Sandbox.Options()),
		browser:      cfg.Browser,
		a2aMaster:    cfg.A2AMaster,
		cronStore:    cronStore,
		scheduler:    scheduler,
		sessions:     make(map[string]*ChannelSession),
	}

	if cfg.MultiAgent || cronStore != nil {
		d.ensureAgentManager()
	}

	return d, nil
}

// ApplyConfig updates runtime channel settings and drops cached sessions so the next
// inbound message is built from the new configuration.
func (d *Dispatcher) ApplyConfig(cfg *Config) error {
	if d == nil || cfg == nil {
		return fmt.Errorf("dispatcher config is required")
	}
	if cfg.WebSearch {
		d.settings.WebSearch.Enabled = config.BoolPtr(true)
	}
	providerName := cfg.GetDefaultProvider(d.settings.DefaultProvider)
	modelID := cfg.GetDefaultModel(d.settings.DefaultModel)
	p, model, err := providerfactory.Create(d.settings, providerName, modelID)
	if err != nil {
		return fmt.Errorf("create provider: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = cfg
	d.provider = p
	d.providerName = providerName
	d.model = model
	d.security = NewSecurity(cfg)
	d.hooksMgr = hooks.NewManager(cfg.Hooks.PreToolCall, cfg.Hooks.PostToolCall)
	d.multiAgent = cfg.MultiAgent
	d.sandbox = cfg.Sandbox
	d.browser = cfg.Browser
	d.a2aMaster = cfg.A2AMaster
	for key, sess := range d.sessions {
		if len(sess.MCPClients) > 0 {
			mcp.CloseClients(sess.MCPClients)
		}
		delete(d.sessions, key)
	}
	if !cfg.MultiAgent && d.agentMgr != nil {
		d.agentMgr = nil
	}
	return nil
}

// AgentManager returns the dispatcher agent manager used by sub-agents and cron.
func (d *Dispatcher) AgentManager() *agent.AgentManager {
	if d == nil {
		return nil
	}
	return d.agentMgr
}

// EnsureAgentManager creates the dispatcher agent manager if it is not already available.
func (d *Dispatcher) EnsureAgentManager() *agent.AgentManager {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ensureAgentManager()
}

func (d *Dispatcher) ensureAgentManager() *agent.AgentManager {
	if d.agentMgr != nil {
		return d.agentMgr
	}
	compactionSettings := agent.CompactionSettingsFromConfig(d.settings.Compaction)

	if d.sandboxMgr != nil {
		if d.sandbox {
			if err := d.sandboxMgr.SetLevel(sandbox.LevelStandard); err != nil {
				return nil
			}
			if err := d.sandboxMgr.FallbackError(); err != nil {
				log.Printf("[channels] sandbox unavailable; using direct execution: %v", err)
			}
		} else {
			_ = d.sandboxMgr.SetLevel(sandbox.LevelNone)
		}
	}
	factory := agent.NewAgentFactoryWithOptions(d.provider, d.model, d.settings, d.sandboxMgr, "", "", nil, compactionSettings, nil, agent.AgentFactoryOptions{
		MultiAgentEnabled: true,
		ProviderName:      d.providerName,
		Allow:             d.allow,
	})
	d.agentMgr = agent.NewAgentManager(factory)
	return d.agentMgr
}

// SetCronStore updates the cron store used by newly created channel sessions.
func (d *Dispatcher) SetCronStore(store cron.CronStore) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cronStore = store
}

// SetCronScheduler updates the scheduler used by cron tools created for sessions.
func (d *Dispatcher) SetCronScheduler(s *cron.Scheduler) {
	if d == nil {
		return
	}
	d.scheduler = s
}

// SetRunObserver installs a callback for channel execution lifecycle changes.
// The callback is session-ID based so the WebUI can reuse its canonical runtime
// snapshot and event broker.
func (d *Dispatcher) SetRunObserver(observer func(string)) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.runObserver = observer
	d.mu.Unlock()
}

func (d *Dispatcher) notifyRunObserver(sessionID string) {
	if d == nil || sessionID == "" {
		return
	}
	d.mu.RLock()
	observer := d.runObserver
	d.mu.RUnlock()
	if observer != nil {
		observer(sessionID)
	}
}

// HandleMessage processes an inbound message from any platform.
func (d *Dispatcher) HandleMessage(ctx context.Context, msg messaging.InboundMessage) (response string, runErr error) {
	log.Printf("[channels] HandleMessage: platform=%s userID=%s text=%q", msg.Platform, msg.UserID, truncate(msg.Text, 80))

	// Check user whitelist
	if err := d.security.CheckUserAllowed(msg.Platform, msg.UserID); err != nil {
		return "", err
	}

	// Check if command
	if strings.HasPrefix(msg.Text, "/") {
		return d.handleCommand(msg)
	}

	sess, err := d.resolveSession(msg.Platform, msg.UserID)
	if err != nil {
		return "", fmt.Errorf("resolve session: %w", err)
	}

	releaseRuntime := session.LockRuntime(d.sessionDir, sess.Manager.GetHeader().ID)
	sessionID := sess.Manager.GetHeader().ID
	d.notifyRunObserver(sessionID)
	defer func() {
		releaseRuntime()
		d.notifyRunObserver(sessionID)
	}()
	sess.Lock()
	defer sess.Unlock()
	sess.Touch()
	if err := sess.Manager.Reload(); err != nil {
		return "", fmt.Errorf("reload session before channel run: %w", err)
	}

	runID := "channel_" + session.GenerateID()
	runCtx, cancelRun := context.WithCancel(ctx)
	sess.runStateMu.Lock()
	sess.runID = runID
	sess.runCancel = cancelRun
	sess.runStateMu.Unlock()
	defer func() {
		cancelRun()
		sess.runStateMu.Lock()
		if sess.runID == runID {
			sess.runID = ""
			sess.runCancel = nil
		}
		sess.runStateMu.Unlock()
	}()
	runStartedAt := time.Now()
	modelID := ""
	if d.model != nil {
		modelID = d.model.ID
	}
	if err := session.SaveSessionRun(d.sessionDir, session.SessionRun{
		ID: runID, SessionID: sessionID, WorkDir: sess.WorkDir,
		Source: "channel:" + msg.Platform, Model: modelID, Mode: sess.Mode,
		Status: "running", StartedAt: runStartedAt, UpdatedAt: runStartedAt,
	}); err != nil {
		return "", fmt.Errorf("create channel run: %w", err)
	}
	if _, err := session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
		SessionID: sessionID, RunID: runID, EventType: "started",
		Source: "channel:" + msg.Platform, Status: "running", Model: modelID, Mode: sess.Mode,
	}); err != nil {
		log.Printf("[channels] save run start event %s: %v", runID, err)
	}
	d.notifyRunObserver(sessionID)
	defer func() {
		status := "completed"
		message := ""
		if runErr != nil {
			status = "failed"
			if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				status = "canceled"
			}
			message = runErr.Error()
		}
		finishedAt := time.Now()
		if err := session.UpdateSessionRunStatus(d.sessionDir, runID, status, message, &finishedAt); err != nil {
			log.Printf("[channels] update run %s status: %v", runID, err)
		}
		eventType := "finished"
		if status == "failed" {
			eventType = "failed"
		} else if status == "canceled" {
			eventType = "canceled"
		}
		var eventData json.RawMessage
		if message != "" {
			eventData, _ = json.Marshal(map[string]string{"error": message})
		}
		if _, err := session.SaveSessionRunEvent(d.sessionDir, session.SessionRunEvent{
			SessionID: sessionID, RunID: runID, EventType: eventType,
			Source: "channel:" + msg.Platform, Status: status, Model: modelID, Mode: sess.Mode,
			Data: eventData,
		}); err != nil {
			log.Printf("[channels] save run end event %s: %v", runID, err)
		}
		d.notifyRunObserver(sessionID)
	}()

	return d.runAgent(runCtx, sess, msg.Text, msg.ProgressFunc)
}

// CancelChannelSessionRun aborts an active WeChat/Feishu (or other channel)
// execution. It returns false when the session is not currently running in
// this dispatcher, allowing the API runtime to handle its own runs.
func (d *Dispatcher) CancelChannelSessionRun(sessionID string) bool {
	if d == nil || sessionID == "" {
		return false
	}
	d.mu.RLock()
	var target *ChannelSession
	for _, sess := range d.sessions {
		if sess != nil && sess.ID == sessionID {
			target = sess
			break
		}
	}
	d.mu.RUnlock()
	if target == nil {
		return false
	}
	target.runStateMu.Lock()
	cancel := target.runCancel
	runID := target.runID
	target.runStateMu.Unlock()
	if cancel == nil || runID == "" {
		return false
	}
	cancel()
	if err := session.UpdateSessionRunStatus(d.sessionDir, runID, "cancelling", "run cancellation requested", nil); err != nil {
		log.Printf("[channels] update cancelled run %s: %v", runID, err)
	}
	d.notifyRunObserver(sessionID)
	return true
}

// resolveSession finds or creates the active session for a platform user.
func (d *Dispatcher) resolveSession(platform, userID string) (*ChannelSession, error) {
	key := sessionKey(platform, userID)

	d.mu.RLock()
	if sess, ok := d.sessions[key]; ok {
		d.mu.RUnlock()
		log.Printf("[channels] session reused: %s", key)
		return sess, nil
	}
	d.mu.RUnlock()

	log.Printf("[channels] session not found in cache, creating: %s", key)

	// Create or load session
	d.mu.Lock()
	defer d.mu.Unlock()

	// Double-check after acquiring write lock
	if sess, ok := d.sessions[key]; ok {
		log.Printf("[channels] session found after write lock: %s", key)
		return sess, nil
	}

	workDir := d.cfg.GetPlatformWorkDir(platform)
	if err := d.security.CheckWorkDirAllowed(workDir); err != nil {
		return nil, err
	}
	var mgr *session.Manager
	var bound *session.Binding
	var err error
	if platform == "wechat" || platform == "feishu" {
		bound, err = session.FindBinding(d.sessionDir, platform, userID)
		if err != nil {
			return nil, fmt.Errorf("find channel binding: %w", err)
		}
	}
	if bound != nil {
		mgr, err = session.OpenByIDExact(d.sessionDir, bound.SessionID)
		if err != nil {
			return nil, fmt.Errorf("open bound session: %w", err)
		}
	} else if platform == "wechat" || platform == "feishu" {
		mgr, err = session.CreateBound(workDir, d.sessionDir, platform, userID)
		if err != nil {
			return nil, fmt.Errorf("create bound session: %w", err)
		}
	} else {
		mgr = session.New(workDir, d.sessionDir)
		if err := mgr.Init(); err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
	}

	sbMgr := sandbox.NewManagerWithOptions(workDir, d.settings.Sandbox.Options())
	if d.sandbox {
		if err := sbMgr.SetLevel(sandbox.LevelStandard); err != nil {
			return nil, fmt.Errorf("enable sandbox: %w", err)
		}
		if err := sbMgr.FallbackError(); err != nil {
			log.Printf("[channels] sandbox unavailable; using direct execution: %v", err)
		}
	} else {
		_ = sbMgr.SetLevel(sandbox.LevelNone)
	}
	configured, toolErr := session.ListChannelTools(d.sessionDir, mgr.GetHeader().ID)
	if toolErr != nil {
		return nil, fmt.Errorf("load channel tools: %w", toolErr)
	}
	enabled := make(map[string]bool, len(configured))
	for _, item := range configured {
		enabled[item.ToolName] = item.Enabled
	}
	hasToolConfig := len(configured) > 0
	toolEnabled := func(name string, defaultEnabled bool) bool {
		if value, ok := enabled[name]; ok {
			return value
		}
		return !hasToolConfig && defaultEnabled
	}

	reg := tools.NewRegistry(workDir, sbMgr.GetActive())
	reg.RegisterDefaults()
	if toolEnabled("browser", d.browser) {
		browserfeature.RegisterTool(reg)
	}
	if toolEnabled("a2a_dispatch", d.a2aMaster) {
		if err := d.registerA2AMasterTool(reg); err != nil {
			return nil, err
		}
	}
	if toolEnabled("memory", true) {
		reg.Register(memory.NewMemoryTool(memory.NewStore(d.cfg.Memory.Path, workDir)))
	}

	multiAgentTools := []string{"delegate_subagent", "subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy", "workflow_lint", "workflow_run", "workflow_status", "workflow_cancel"}
	registerMultiAgent := false
	for _, name := range multiAgentTools {
		if toolEnabled(name, d.multiAgent) {
			registerMultiAgent = true
			break
		}
	}
	if registerMultiAgent {
		manager := d.ensureAgentManager()
		agent.RegisterSubAgentTools(reg, manager)
		agent.RegisterDelegateSubAgentTool(reg, manager)
		workflow.RegisterTools(reg, manager, nil)
	}

	if d.cronStore != nil && toolEnabled("cron", true) {
		sessionID := ""
		if header := mgr.GetHeader(); header != nil {
			sessionID = header.ID
		}
		reg.Register(cron.NewCronTool(cron.NewSessionScopedStoreWithWorkDir(d.cronStore, sessionID, workDir), d.scheduler))
	}

	// Load and connect MCP servers
	var mcpClients []*mcp.Client
	mcpServers, err := mcp.LoadConfiguredServers(workDir)
	if err != nil {
		log.Printf("[channels] load MCP servers: %v", err)
	} else if len(mcpServers) > 0 {
		clients, err := mcp.ConnectServers(context.Background(), mcpServers, reg, mcp.Callbacks{})
		if err != nil {
			log.Printf("[channels] connect MCP servers: %v", err)
		} else {
			mcpClients = clients
			log.Printf("[channels] connected %d MCP server(s) for %s/%s", len(clients), platform, userID)
		}
	}

	if platform == "wechat" || platform == "feishu" {
		for _, item := range reg.All() {
			if value, ok := enabled[item.Name()]; ok && !value {
				reg.Remove(item.Name())
			}
		}
	}

	sess := &ChannelSession{
		ID:         mgr.GetHeader().ID,
		Platform:   platform,
		UserID:     userID,
		WorkDir:    workDir,
		Manager:    mgr,
		Registry:   reg,
		SandboxMgr: sbMgr,
		MCPClients: mcpClients,
		Mode:       "yolo",
		LastUsed:   time.Now(),
	}

	d.sessions[key] = sess
	log.Printf("[channels] session created: %s (workDir=%s)", key, workDir)
	return sess, nil
}

// RotateSession archives the current session and creates a new one.
// Called when user sends /new.
func (d *Dispatcher) RotateSession(platform, userID string) error {
	key := sessionKey(platform, userID)
	log.Printf("[channels] rotating session: %s", key)
	d.mu.Lock()
	defer d.mu.Unlock()
	if platform != "wechat" && platform != "feishu" {
		delete(d.sessions, key)
		return nil
	}
	bound, err := session.FindBinding(d.sessionDir, platform, userID)
	if err != nil {
		return fmt.Errorf("find channel binding: %w", err)
	}
	if bound != nil {
		workDir := d.cfg.GetPlatformWorkDir(platform)
		mgr, rotateErr := session.RotateBoundSession(workDir, d.sessionDir, platform, userID, bound.SessionID)
		if rotateErr != nil {
			return fmt.Errorf("rotate bound session: %w", rotateErr)
		}
		if sess, ok := d.sessions[key]; ok && len(sess.MCPClients) > 0 {
			mcp.CloseClients(sess.MCPClients)
		}
		delete(d.sessions, key)
		_ = mgr
	}
	return nil
}

// GetSession returns a session by key, or nil if not found.
func (d *Dispatcher) GetSession(key string) *ChannelSession {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.sessions[key]
}

// ListSessions returns all active session keys.
func (d *Dispatcher) ListSessions() []*ChannelSession {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*ChannelSession, 0, len(d.sessions))
	for _, s := range d.sessions {
		result = append(result, s)
	}
	return result
}

// RefreshBinding invalidates the cached runtime route for a channel identity.
// The next inbound message will resolve the identity from the canonical binding
// stored in the root sessions database.
func (d *Dispatcher) RefreshBinding(platform, userID string) {
	if d == nil || userID == "" {
		return
	}
	d.RemoveSession(sessionKey(platform, userID))
}

// RefreshSessionTools drops the cached channel session so its registry is rebuilt
// from the latest persisted tool configuration on the next message.
func (d *Dispatcher) RefreshSessionTools(sessionID string) {
	if d == nil || sessionID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, sess := range d.sessions {
		if sess.Manager != nil && sess.Manager.GetHeader() != nil && sess.Manager.GetHeader().ID == sessionID {
			if len(sess.MCPClients) > 0 {
				mcp.CloseClients(sess.MCPClients)
			}
			delete(d.sessions, key)
			return
		}
	}
}

// RemoveSession removes a session from the pool.
func (d *Dispatcher) RemoveSession(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sess, ok := d.sessions[key]; ok {
		if len(sess.MCPClients) > 0 {
			mcp.CloseClients(sess.MCPClients)
		}
		delete(d.sessions, key)
	}
}

// buildAgent creates and configures an agent for a session.
// Returns the agent and a cleanup function to call with the run error.
func (d *Dispatcher) buildAgent(ctx context.Context, sess *ChannelSession, approvalHandler agentApprovalHandler) (*agent.Agent, func(error)) {
	workDir := sess.WorkDir
	extraContext := d.buildExtraContext(workDir)
	ruleContent := contextfiles.LoadRuleFile(workDir)
	compactionSettings := agent.CompactionSettingsFromConfig(d.settings.Compaction)

	// Prompt gating flags must reflect the tools actually present in the
	// session registry. Per-session tool config can enable or disable
	// sub-agent/delegate/workflow tools individually (and wechat/feishu
	// sessions drop explicitly disabled tools), so derive the flags from the
	// registry instead of the dispatcher-level multiAgent flag alone.
	hasTool := func(name string) bool {
		if sess.Registry == nil {
			return false
		}
		_, ok := sess.Registry.Get(name)
		return ok
	}

	agentCfg := agent.Config{
		Provider:           d.provider,
		Vendor:             d.providerName,
		Model:              d.model,
		Mode:               sess.Mode,
		ThinkingLevel:      provider.ThinkingLevel(d.settings.DefaultThinkingLevel),
		SandboxMgr:         sess.SandboxMgr,
		Settings:           d.settings,
		Allow:              d.allow,
		Session:            sess.Manager,
		ExtraContext:       extraContext,
		RuleContent:        ruleContent,
		CompactionSettings: compactionSettings,
		MultiAgent:         hasTool("subagent_spawn"),
		DelegateMode:       hasTool("delegate_subagent"),
		Workflows:          hasTool("workflow_run"),
		ApprovalHandler:    approvalHandler,
	}

	a := agent.NewWithLoopConfig(agent.AgentLoopConfig{
		Config:                   agentCfg,
		MaxIterations:            d.cfg.Agent.MaxTurns,
		ContextPressureThreshold: d.cfg.Agent.ContextPressureThreshold,
		BudgetPressureThreshold:  d.cfg.Agent.BudgetPressureThreshold,
		AfterToolCall: func(ctx2 agent.AfterToolCallContext) *agent.ToolCallResult {
			if d.hooksMgr.HasPostHook() {
				argsMap, _ := ctx2.Args.(map[string]any)
				errMsg := ""
				if ctx2.IsError {
					errMsg = ctx2.Result.Content
				}
				d.hooksMgr.PostToolCall(ctx, ctx2.ToolCall.Name, argsMap, ctx2.Result.Content, errMsg, sess.Platform, sess.UserID)
			}
			return nil
		},
	}, sess.Registry)

	var runErr error
	if d.agentMgr != nil {
		d.agentMgr.Register(agent.NewAgentAdapter(a))
	}
	cleanup := func(err error) {
		runErr = err
		if d.agentMgr != nil {
			d.agentMgr.Finish(a.ID(), runErr)
		}
	}

	if sess.ForceCompact {
		a.SetForceCompact()
		sess.ForceCompact = false
	}

	if replayState := sess.Manager.GetReplayState(); len(replayState.Messages) > 0 {
		a.LoadHistoryState(replayState.Messages, replayState.EntryIDs)
	}

	return a, cleanup
}

func (d *Dispatcher) compactSession(ctx context.Context, sess *ChannelSession) error {
	a, cleanup := d.buildAgent(ctx, sess, nil)
	defer cleanup(nil)

	eventCh := make(chan agent.Event, 16)
	if err := a.CompactForced(ctx, eventCh); err != nil {
		return err
	}
	for len(eventCh) > 0 {
		<-eventCh
	}
	return nil
}

// messagingApprovalHandler returns an ApprovalHandler for messaging platforms.
// Medium risk → auto-approve + notify; high risk → auto-reject + notify.
func (d *Dispatcher) messagingApprovalHandler(ctx context.Context, sess *ChannelSession, progress func(string)) agentApprovalHandler {
	return func(toolCallID, toolName string, args map[string]any) bool {
		if toolName == "git_access" {
			if progress != nil {
				progress("⛔ Git metadata access is not available in unattended channel sessions")
			}
			return false
		}
		if d.security.ShouldAutoApprove(toolName, args, sess.Mode) {
			return true
		}

		risk := "medium"
		if toolName == "bash" {
			if cmd, ok := args["command"]; ok {
				risk = CommandRiskLevel(fmt.Sprintf("%v", cmd))
			}
		}

		if d.hooksMgr.HasPreHook() {
			allowed, _, _ := d.hooksMgr.PreToolCall(ctx, toolName, args, sess.Platform, sess.UserID)
			if allowed {
				return true
			}
		}

		if risk == "medium" {
			if progress != nil {
				progress(FormatApprovalNotification(toolName, args, risk, true))
			}
			return true
		}

		if progress != nil {
			progress(FormatApprovalNotification(toolName, args, risk, false))
		}
		return false
	}
}

// runAgent executes the agent loop synchronously (for messaging platforms).
func (d *Dispatcher) runAgent(ctx context.Context, sess *ChannelSession, userInput string, progress func(string)) (string, error) {
	a, cleanup := d.buildAgent(ctx, sess, d.messagingApprovalHandler(ctx, sess, progress))
	var runErr error
	defer cleanup(runErr)

	eventCh := a.Run(ctx, userInput)

	var response strings.Builder
	var thinkBuf strings.Builder
	var eventCount int
	var toolCount int
	var attachments []provider.Attachment
	pendingToolArgs := make(map[string]map[string]any) // ToolCallID → args
	flushThink := func() {
		if progress != nil && thinkBuf.Len() > 0 {
			text := thinkBuf.String()
			if len(text) > 500 {
				text = text[:500] + "..."
			}
			progress("💭 " + text)
			thinkBuf.Reset()
		}
	}
	for ev := range eventCh {
		eventCount++
		switch ev.Type {
		case agent.EventAgentStart:
			// RunWithUserMessage persists the inbound message before entering the
			// loop, so this is the first safe point to sync it to WebUI.
			d.notifyRunObserver(sess.Manager.GetHeader().ID)
		case agent.EventThinkDelta:
			thinkBuf.WriteString(ev.ThinkDelta)
		case agent.EventTextDelta:
			flushThink()
			response.WriteString(ev.TextDelta)
		case agent.EventToolExecutionStart:
			if ev.ToolCallID != "" && ev.ToolArgs != nil {
				pendingToolArgs[ev.ToolCallID] = ev.ToolArgs
			}
		case agent.EventToolExecutionEnd:
			flushThink()
			toolCount++
			if progress != nil {
				args := pendingToolArgs[ev.ToolCallID]
				delete(pendingToolArgs, ev.ToolCallID)
				line := formatToolProgress(ev, args)
				if line != "" {
					progress(line)
				}
			}
		case agent.EventContextPressure, agent.EventBudgetPressure:
			// Forward pressure warnings to messaging platform
			if progress != nil && ev.PressureMessage != "" {
				progress("\n" + ev.PressureMessage)
			}
			log.Printf("[channels] %s pressure event for %s/%s: %s", ev.PressureType, sess.Platform, sess.UserID, ev.PressureMessage)
		case agent.EventCompactionStart:
			if progress != nil {
				progress("🗜️ Compacting context...")
			}
		case agent.EventCompactionEnd:
			if progress != nil {
				if ev.Error != nil {
					progress(fmt.Sprintf("⚠️ Context compaction failed: %v", ev.Error))
				} else if ev.StatusMessage != "" {
					progress("🗜️ " + ev.StatusMessage)
				}
			}
		case agent.EventStatus:
			// Surface context-recovery notices (overflow compaction/truncation)
			// so unattended channel users can see why a reply was delayed.
			if progress != nil && strings.HasPrefix(ev.StatusMessage, "Context recovery:") {
				progress("🗜️ " + ev.StatusMessage)
			}
		case agent.EventError:
			flushThink()
			if ev.Error != nil {
				runErr = ev.Error
				d.notifyRunObserver(sess.Manager.GetHeader().ID)
				log.Printf("[channels] Agent error for %s/%s: %v", sess.Platform, sess.UserID, ev.Error)
				return "", ev.Error
			}
		case agent.EventDone:
			d.notifyRunObserver(sess.Manager.GetHeader().ID)
			attachments = append(attachments, ev.Attachments...)
		}
	}

	result := response.String()
	log.Printf("[channels] Agent completed for %s/%s: events=%d, tools=%d, response_len=%d", sess.Platform, sess.UserID, eventCount, toolCount, len(result))

	// If agent produced no text but executed tools, provide a fallback summary
	if result == "" && toolCount > 0 {
		result = fmt.Sprintf("✅ Done (%d tool calls completed)", toolCount)
	}
	if attachmentText := formatAttachmentSummary(attachments); attachmentText != "" {
		if result != "" {
			result += "\n\n"
		}
		result += attachmentText
	}

	return result, nil
}

func formatAttachmentSummary(items []provider.Attachment) string {
	if len(items) == 0 {
		return ""
	}
	lines := []string{"Attachments:"}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		label := strings.TrimSpace(item.Name)
		if label == "" {
			label = strings.TrimSpace(item.Kind)
		}
		if label == "" {
			label = "attachment"
		}
		target := strings.TrimSpace(item.URL)
		if target == "" {
			target = strings.TrimSpace(item.ProviderRef)
		}
		if target == "" {
			continue
		}
		key := label + "\x00" + target
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		lines = append(lines, fmt.Sprintf("- %s: %s", label, target))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// formatToolProgress formats a tool execution event into a concise one-line progress string.
func formatToolProgress(ev agent.Event, args map[string]any) string {
	name := ev.ToolName
	if name == "" && ev.ToolCall != nil {
		name = ev.ToolCall.Name
	}
	if name == "" {
		return ""
	}

	var icon string
	if ev.ToolError != nil {
		icon = "❌"
	} else {
		icon = "✅"
	}

	// Build a concise summary per tool type
	switch name {
	case "read", "write", "edit", "insert":
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("[%s]: %s %s", name, path, icon)
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			if len(cmd) > 60 {
				cmd = cmd[:60] + "..."
			}
			return fmt.Sprintf("[bash]: %s %s", cmd, icon)
		}
	case "grep":
		if pat, ok := args["pattern"].(string); ok {
			return fmt.Sprintf("[grep]: %s %s", pat, icon)
		}
	case "find":
		if pat, ok := args["pattern"].(string); ok {
			return fmt.Sprintf("[find]: %s %s", pat, icon)
		}
	case "ls":
		if path, ok := args["path"].(string); ok {
			return fmt.Sprintf("[ls]: %s %s", path, icon)
		}
	}

	return fmt.Sprintf("[%s] %s", name, icon)
}

// buildExtraContext loads context files and skills for a working directory.
func (d *Dispatcher) buildExtraContext(workDir string) string {
	var extra string
	if d.settings.ContextFiles.Enabled {
		cfResult := contextfiles.LoadContextFiles(workDir, config.ConfigDir(), d.settings.ContextFiles.ExtraFiles)
		if ctx := contextfiles.BuildContextString(cfResult); ctx != "" {
			extra = ctx
		}
	}

	skillsMgr := skills.NewManagerWithProjectDirs(d.settings.GetGlobalSkillsDir(), skills.ProjectSkillDirs(workDir))
	if d.browser {
		if _, _, err := browserfeature.EnsureProjectSkill(workDir); err != nil {
			log.Printf("[channels] create browser skill: %v", err)
		}
	}
	_ = skillsMgr.Load()
	extra += skillsMgr.BuildAllSkillsContext()
	if d.browser {
		extra += skillsMgr.BuildSkillContext(browserfeature.SkillName)
	}

	return extra
}

func (d *Dispatcher) registerA2AMasterTool(registry *tools.Registry) error {
	if !d.a2aMaster {
		return nil
	}
	a2aListPath := a2a.ProjectAgentListConfigPath()
	if _, err := os.Stat(a2aListPath); err != nil {
		a2aListPath = a2a.AgentListConfigPath()
	}
	a2aListCfg, err := a2a.LoadAgentList(a2aListPath)
	if err != nil {
		return fmt.Errorf("load a2a-list.json: %w", err)
	}
	a2aMgr := a2a.NewA2AManager(a2aListCfg)
	registry.Register(tools.NewA2ADispatchTool(&a2aDispatcherAdapter{mgr: a2aMgr}))
	return nil
}

type a2aDispatcherAdapter struct {
	mgr *a2a.A2AManager
}

func (a *a2aDispatcherAdapter) List() []tools.AgentEntry {
	entries := a.mgr.List()
	result := make([]tools.AgentEntry, 0, len(entries))
	for _, e := range entries {
		result = append(result, tools.AgentEntry{Name: e.Name, URL: e.URL})
	}
	return result
}

func (a *a2aDispatcherAdapter) Dispatch(ctx context.Context, name, message string) (string, error) {
	return a.mgr.Dispatch(ctx, name, message)
}

// handleCommand processes slash commands from messaging platforms.
func (d *Dispatcher) handleCommand(msg messaging.InboundMessage) (string, error) {
	parts := strings.Fields(msg.Text)
	if len(parts) == 0 {
		return "", nil
	}

	cmd := strings.ToLower(parts[0])
	switch cmd {
	case "/new":
		if err := d.RotateSession(msg.Platform, msg.UserID); err != nil {
			return "❌ Failed to create new session: " + err.Error(), nil
		}
		return "✅ New session created.", nil
	case "/clear":
		if err := d.RotateSession(msg.Platform, msg.UserID); err != nil {
			return "❌ Failed to clear session: " + err.Error(), nil
		}
		return "✅ Session cleared.", nil
	case "/status":
		sess := d.GetSession(sessionKey(msg.Platform, msg.UserID))
		if sess == nil {
			return "No active session.", nil
		}
		msgs := sess.Manager.GetMessages()
		return fmt.Sprintf("Session: %s\nMode: %s\nMessages: %d\nWorkDir: %s",
			sess.ID, sess.Mode, len(msgs), sess.WorkDir), nil
	case "/sessions":
		sessions := d.ListSessions()
		if len(sessions) == 0 {
			return "No active sessions.", nil
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Active sessions (%d):\n", len(sessions)))
		for _, s := range sessions {
			msgs := s.Manager.GetMessages()
			sb.WriteString(fmt.Sprintf("  • %s (%d msgs, %s)\n", s.ID, len(msgs), s.WorkDir))
		}
		return sb.String(), nil
	case "/mode":
		if len(parts) < 2 {
			sess := d.GetSession(sessionKey(msg.Platform, msg.UserID))
			if sess != nil {
				return fmt.Sprintf("Current mode: %s", sess.Mode), nil
			}
			return "No active session.", nil
		}
		mode := strings.ToLower(parts[1])
		switch mode {
		case "plan", "agent", "yolo":
			sess, err := d.resolveSession(msg.Platform, msg.UserID)
			if err != nil {
				return "❌ No active session.", nil
			}
			sess.Mode = mode
			return fmt.Sprintf("✅ Mode set to %s.", mode), nil
		default:
			return "Invalid mode. Use: plan, agent, yolo", nil
		}
	case "/compact":
		sess, err := d.resolveSession(msg.Platform, msg.UserID)
		if err != nil {
			return "❌ No active session.", nil
		}
		releaseRuntime := session.LockRuntime(d.sessionDir, sess.Manager.GetHeader().ID)
		defer releaseRuntime()
		sess.Lock()
		defer sess.Unlock()
		if err := sess.Manager.Reload(); err != nil {
			return fmt.Sprintf("Session reload failed: %v", err), nil
		}
		if err := d.compactSession(context.Background(), sess); err != nil {
			return fmt.Sprintf("Context compaction failed: %v", err), nil
		}
		return "✅ Context compacted.", nil
	default:
		return fmt.Sprintf("Unknown command: %s\nAvailable: /new /clear /status /sessions /mode /compact", cmd), nil
	}
}

// channelSessionDir returns the directory for a platform user's sessions.
func (d *Dispatcher) channelSessionDir(platform, userID string) string {
	return filepath.Join(d.sessionDir, "channels", safeSessionPathComponent(platform), safeSessionPathComponent(userID))
}

// sessionKey builds a session pool key.
func sessionKey(platform, userID string) string {
	return fmt.Sprintf("channels/%s/%s", platform, userID)
}

func safeSessionPathComponent(s string) string {
	if s == "" || s == "." || s == ".." {
		return "b64_" + base64.RawURLEncoding.EncodeToString([]byte(s))
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '-', '_', '.', '@':
			continue
		default:
			return "b64_" + base64.RawURLEncoding.EncodeToString([]byte(s))
		}
	}
	return s
}

// archiveCorrupt renames a corrupt session file.
func (d *Dispatcher) archiveCorrupt(path string) {
	dir := filepath.Dir(path)
	archived := filepath.Join(dir, fmt.Sprintf("%s_corrupt.db",
		time.Now().Format("20060102-150405")))
	os.Rename(path, archived)
}

// ResolveQuestion sends an answer to a currently running agent question.
func (d *Dispatcher) ResolveQuestion(questionID, answer string) bool {
	var found bool
	activeAgents.Range(func(_, value any) bool {
		ag, ok := value.(*agent.Agent)
		if !ok {
			return true
		}
		ag.HandleQuestionResponse(questionID, answer)
		found = true
		return false
	})
	return found
}

// activeAgents tracks running agents by ID for question resolution.
// Key: agent ID (string), Value: *agent.Agent
var activeAgents sync.Map

// RegisterActiveAgent registers a running agent for question resolution.
func RegisterActiveAgent(id string, a *agent.Agent) {
	activeAgents.Store(id, a)
}

// UnregisterActiveAgent removes an agent from the registry.
func UnregisterActiveAgent(id string) {
	activeAgents.Delete(id)
}

func truncate(s string, maxLen int) string {
	return util.TruncateWithSuffix(s, maxLen, "...")
}
