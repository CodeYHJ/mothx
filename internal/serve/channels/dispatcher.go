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
	"reflect"
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
	serviceruntime "github.com/startvibecoding/mothx/internal/serve/runtime"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
	"github.com/startvibecoding/mothx/internal/util"
	"github.com/startvibecoding/mothx/internal/workflow"
)

// ToolCatalogItem describes a tool that may be configured for channel sessions.
type ToolCatalogItem struct {
	Name              string `json:"name"`
	Available         bool   `json:"available"`
	Default           bool   `json:"default"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

// ChannelToolDefinition is the single runtime definition used by the
// catalog, persisted-tool validator and session registry builder.
type ChannelToolDefinition struct {
	Name              string
	Default           bool
	Available         bool
	UnavailableReason string
}

func isMultiAgentToolName(name string) bool {
	return name == "delegate_subagent" || strings.HasPrefix(name, "subagent_") || strings.HasPrefix(name, "workflow_")
}

type ChannelToolState struct {
	Name              string `json:"name"`
	RequestedEnabled  bool   `json:"requestedEnabled"`
	Available         bool   `json:"available"`
	EffectiveEnabled  bool   `json:"effectiveEnabled"`
	Registered        bool   `json:"registered"`
	WillRegister      bool   `json:"willRegister"`
	UnavailableReason string `json:"reason,omitempty"`
}

// BackgroundRequest and BackgroundSubmitter remain available from the
// channels package for compatibility while their contract lives in the
// neutral serve/runtime package.
type BackgroundRequest = serviceruntime.BackgroundRequest
type BackgroundSubmitter = serviceruntime.BackgroundSubmitter

// ToolCatalog returns the complete channel tool catalog. Startup options determine
// each dynamic tool's default selection; every catalog item remains selectable per session.
func (d *Dispatcher) ToolCatalog(platform string) []ToolCatalogItem {
	definitions := d.channelToolDefinitions(platform)
	result := make([]ToolCatalogItem, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, ToolCatalogItem{
			Name: definition.Name, Default: definition.Default,
			Available: definition.Available, UnavailableReason: definition.UnavailableReason,
		})
	}
	return result
}

func (d *Dispatcher) channelToolDefinitions(platform string) []ChannelToolDefinition {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.channelToolDefinitionsLocked(platform)
}

func (d *Dispatcher) channelToolDefinitionsLocked(platform string) []ChannelToolDefinition {
	cfg := d.cfg
	browserEnabled := d.browser
	a2aEnabled := d.a2aMaster
	multiAgentEnabled := d.multiAgent
	cronAvailable := d.cronStore != nil
	if cfg == nil {
		cfg = DefaultConfig()
	}
	workDir := cfg.GetPlatformWorkDir(platform)
	reg := tools.NewRegistry(workDir, nil)
	reg.RegisterDefaults()
	seen := map[string]bool{}
	result := make([]ChannelToolDefinition, 0)
	add := func(name string, available, defaultEnabled bool, reason ...string) {
		if seen[name] {
			return
		}
		seen[name] = true
		item := ChannelToolDefinition{Name: name, Available: available, Default: defaultEnabled}
		if len(reason) > 0 && !available {
			item.UnavailableReason = reason[0]
		}
		result = append(result, item)
	}
	for _, item := range reg.All() {
		add(item.Name(), true, true)
	}
	// The browser runtime can be enabled on demand, so the browser tool stays
	// selectable regardless of the feature flag; browserEnabled only decides
	// the default checked state.
	add("browser", true, browserEnabled)
	add("memory", true, true)
	add("cron", cronAvailable, cronAvailable, "cron scheduler is disabled")
	a2aAvailable, a2aReason := a2aToolAvailability(a2aEnabled)
	add("a2a_dispatch", a2aAvailable, a2aAvailable, a2aReason)
	// The multi-agent runtime (agent manager) is always available, so these
	// tools stay selectable regardless of the multiAgent flag. multiAgent only
	// decides the default checked state; the user can still enable/disable
	// them per session from the WebUI.
	add("delegate_subagent", true, multiAgentEnabled)
	for _, name := range []string{"subagent_spawn", "subagent_status", "subagent_send", "subagent_destroy"} {
		add(name, true, multiAgentEnabled)
	}
	for _, name := range []string{"workflow_lint", "workflow_run", "workflow_status", "workflow_cancel"} {
		add(name, true, multiAgentEnabled)
	}
	return result
}

func (d *Dispatcher) SessionToolStates(sessionID, platform string) ([]ChannelToolState, int64, error) {
	catalog := d.ToolCatalog(platform)
	configured, err := session.ListChannelTools(d.sessionDir, sessionID)
	if err != nil {
		return nil, 0, err
	}
	generation, err := session.GetChannelToolGeneration(d.sessionDir, sessionID)
	if err != nil {
		return nil, 0, err
	}
	requested := make(map[string]bool, len(configured))
	for _, item := range configured {
		requested[item.ToolName] = item.Enabled
	}
	hasConfig := len(configured) > 0
	registered := d.registeredTools(sessionID)
	states := make([]ChannelToolState, 0, len(catalog))
	for _, item := range catalog {
		value, ok := requested[item.Name]
		if !ok {
			value = !hasConfig && item.Default
		}
		effective := value && item.Available
		isRegistered := false
		willRegister := effective
		if registered != nil {
			isRegistered = registered[item.Name]
		}
		states = append(states, ChannelToolState{
			Name: item.Name, RequestedEnabled: value, Available: item.Available,
			EffectiveEnabled: effective, Registered: isRegistered, WillRegister: willRegister, UnavailableReason: item.UnavailableReason,
		})
	}
	return states, generation, nil
}

func (d *Dispatcher) registeredTools(sessionID string) map[string]bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, sess := range d.sessions {
		if sess == nil || sess.ID != sessionID || sess.Registry == nil {
			continue
		}
		result := make(map[string]bool)
		for _, tool := range sess.Registry.All() {
			result[tool.Name()] = true
		}
		return result
	}
	return nil
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
	runObserver         func(string)
	rotateHandler       func(string, string) error
	backgroundSubmitter BackgroundSubmitter
	runRootCtx          context.Context
	runRootStop         context.CancelFunc
	identityLocks       *session.IdentityLocks
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
	ForceCompact    bool
	runStateMu      sync.Mutex
	runID           string
	runCancel       context.CancelFunc
	invalidated     bool
	generation      int64
	pendingEntrants int
	activeRuns      int
}

// ChannelSessionLease keeps a resolved session alive while a message waits for
// the shared runtime lock. This prevents refresh/invalidation from closing its
// Registry or MCP clients underneath the waiting request.
type ChannelSessionLease struct {
	d          *Dispatcher
	key        string
	platform   string
	userID     string
	session    *ChannelSession
	generation int64
	promoted   bool
	released   bool
}

type dispatcherRuntimeSnapshot struct {
	cfg          *Config
	provider     provider.Provider
	providerName string
	model        *provider.Model
	allow        *config.AllowConfig
	security     *Security
	hooksMgr     *hooks.Manager
	multiAgent   bool
	sandbox      bool
	browser      bool
	a2aMaster    bool
	cronStore    cron.CronStore
	scheduler    *cron.Scheduler
	agentMgr     *agent.AgentManager
}

func (d *Dispatcher) runtimeSnapshot() dispatcherRuntimeSnapshot {
	if d == nil {
		return dispatcherRuntimeSnapshot{}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return dispatcherRuntimeSnapshot{
		cfg: d.cfg, provider: d.provider, providerName: d.providerName, model: d.model,
		allow: d.allow, security: d.security, hooksMgr: d.hooksMgr,
		multiAgent: d.multiAgent, sandbox: d.sandbox, browser: d.browser, a2aMaster: d.a2aMaster,
		cronStore: d.cronStore, scheduler: d.scheduler, agentMgr: d.agentMgr,
	}
}

func (d *Dispatcher) acquireSessionLease(key, platform, userID string, sess *ChannelSession) (*ChannelSessionLease, bool) {
	if d == nil || sess == nil {
		return nil, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sessions[key] != sess || sess.invalidated {
		return nil, false
	}
	sess.pendingEntrants++
	return &ChannelSessionLease{d: d, key: key, platform: platform, userID: userID, session: sess, generation: sess.generation}, true
}

func (l *ChannelSessionLease) promoteAfterRuntimeLock() bool {
	if l == nil || l.d == nil || l.session == nil || l.released {
		return false
	}
	l.d.mu.Lock()
	if l.d.sessions[l.key] != l.session || l.session.invalidated || l.session.generation != l.generation {
		l.releaseLocked()
		l.d.mu.Unlock()
		return false
	}
	l.d.mu.Unlock()

	if (l.platform == "wechat" || l.platform == "feishu") && l.d.identityLocks != nil {
		releaseIdentity := l.d.identityLocks.Lock(l.platform, l.userID)
		bound, err := session.FindBinding(l.d.sessionDir, l.platform, l.userID)
		releaseIdentity()
		if err != nil || bound == nil || l.session.Manager == nil || l.session.Manager.GetHeader() == nil || bound.SessionID != l.session.Manager.GetHeader().ID {
			l.d.mu.Lock()
			l.releaseLocked()
			l.d.mu.Unlock()
			return false
		}
	}

	l.d.mu.Lock()
	defer l.d.mu.Unlock()
	if l.d.sessions[l.key] != l.session || l.session.invalidated || l.session.generation != l.generation {
		l.releaseLocked()
		return false
	}
	if l.session.pendingEntrants > 0 {
		l.session.pendingEntrants--
	}
	l.session.activeRuns++
	l.promoted = true
	return true
}

func (l *ChannelSessionLease) release() {
	if l == nil || l.d == nil || l.session == nil || l.released {
		return
	}
	l.d.mu.Lock()
	defer l.d.mu.Unlock()
	l.releaseLocked()
}

func (l *ChannelSessionLease) releaseLocked() {
	if l.released {
		return
	}
	if l.promoted && l.session.activeRuns > 0 {
		l.session.activeRuns--
	} else if !l.promoted && l.session.pendingEntrants > 0 {
		l.session.pendingEntrants--
	}
	l.released = true
	if l.session.invalidated && l.session.pendingEntrants == 0 && l.session.activeRuns == 0 && l.d.sessions[l.key] == l.session {
		l.d.closeAndDeleteLocked(l.key, l.session)
	}
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

	runRootCtx, runRootStop := context.WithCancel(context.Background())
	d := &Dispatcher{
		cfg:           cfg,
		settings:      settings,
		allow:         config.LoadAllow(),
		version:       version,
		sessionDir:    settings.GetSessionDir(),
		security:      NewSecurity(cfg),
		hooksMgr:      hooks.NewManager(cfg.Hooks.PreToolCall, cfg.Hooks.PostToolCall),
		provider:      p,
		providerName:  providerName,
		model:         model,
		multiAgent:    cfg.MultiAgent,
		sandbox:       cfg.Sandbox,
		sandboxMgr:    sandbox.NewManagerWithOptions(cfg.GetWorkDir(), settings.Sandbox.Options()),
		browser:       cfg.Browser,
		a2aMaster:     cfg.A2AMaster,
		cronStore:     cronStore,
		scheduler:     scheduler,
		sessions:      make(map[string]*ChannelSession),
		runRootCtx:    runRootCtx,
		runRootStop:   runRootStop,
		identityLocks: session.NewIdentityLocks(),
	}

	if cfg.MultiAgent || cronStore != nil {
		d.ensureAgentManager()
	}

	return d, nil
}

// SetIdentityLocks injects the shared identity lock set used by serve
// lifecycle management. It should be called before the first inbound message.
func (d *Dispatcher) SetIdentityLocks(locks *session.IdentityLocks) {
	if d == nil || locks == nil {
		return
	}
	d.mu.Lock()
	d.identityLocks = locks
	d.mu.Unlock()
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
	d.mu.RLock()
	currentProvider := d.provider
	currentModel := d.model
	currentProviderName := d.providerName
	previousCfg := d.cfg
	d.mu.RUnlock()
	p, model := currentProvider, currentModel
	if currentProvider == nil || currentProviderName != providerName || currentModel == nil || currentModel.ID != modelID {
		var err error
		p, model, err = providerfactory.Create(d.settings, providerName, modelID)
		if err != nil {
			return fmt.Errorf("create provider: %w", err)
		}
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
		if shouldInvalidateSession(previousCfg, cfg, key) {
			d.invalidateSessionLocked(key, sess)
		}
	}
	if !cfg.MultiAgent && d.agentMgr != nil {
		d.agentMgr = nil
	}
	return nil
}

func shouldInvalidateSession(previous, next *Config, key string) bool {
	if previous == nil || next == nil {
		return true
	}
	globalChanged := previous.WorkDir != next.WorkDir ||
		previous.MultiAgent != next.MultiAgent ||
		previous.Sandbox != next.Sandbox ||
		previous.Browser != next.Browser ||
		previous.A2AMaster != next.A2AMaster ||
		!reflect.DeepEqual(previous.Security, next.Security) ||
		!reflect.DeepEqual(previous.Memory, next.Memory) ||
		!reflect.DeepEqual(previous.Cron, next.Cron) ||
		!reflect.DeepEqual(previous.Hooks, next.Hooks) ||
		!reflect.DeepEqual(previous.Agent, next.Agent)
	if globalChanged {
		return true
	}
	if strings.HasPrefix(key, "channels/wechat/") {
		return !reflect.DeepEqual(previous.Wechat, next.Wechat)
	}
	if strings.HasPrefix(key, "channels/feishu/") {
		return !reflect.DeepEqual(previous.Feishu, next.Feishu)
	}
	return true
}

// AgentManager returns the dispatcher agent manager used by sub-agents and cron.
func (d *Dispatcher) AgentManager() *agent.AgentManager {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
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
	d.mu.Lock()
	d.scheduler = s
	d.mu.Unlock()
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

// SetBackgroundSubmitter routes channel messages to the serve-owned durable
// Responses runtime when the configured provider enables background mode.
func (d *Dispatcher) SetBackgroundSubmitter(submitter BackgroundSubmitter) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.backgroundSubmitter = submitter
	d.mu.Unlock()
}

func (d *Dispatcher) responsesBackgroundEnabled() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	p := d.provider
	d.mu.RUnlock()
	enabled, ok := p.(interface{ ResponsesBackgroundEnabled() bool })
	return ok && enabled.ResponsesBackgroundEnabled()
}

// SetRotateHandler lets the serve runtime route channel /new and /clear
// through the shared lifecycle coordinator.
func (d *Dispatcher) SetRotateHandler(handler func(string, string) error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.rotateHandler = handler
	d.mu.Unlock()
}

func (d *Dispatcher) PlatformWorkDir(platform string) string {
	if d == nil {
		return ""
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.cfg == nil {
		return ""
	}
	return d.cfg.GetPlatformWorkDir(platform)
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
	log.Printf("[channels] HandleMessage: platform=%s userID=%s chatID=%s text=%q", msg.Platform, msg.UserID, msg.ChatID, truncate(msg.Text, 80))

	runtime := d.runtimeSnapshot()
	// Feishu's open_id identifies the sender, while chat_id identifies the
	// conversation used for session binding and outbound delivery. Messaging
	// channels accept all users by default.
	msg.UserID = channelRouteID(msg)

	// Check if command
	if strings.HasPrefix(msg.Text, "/") {
		return d.handleCommand(msg)
	}

	var sess *ChannelSession
	var lease *ChannelSessionLease
	var releaseRuntime func()
	var leaseOK bool
	for attempt := 0; attempt < 3; attempt++ {
		var err error
		sess, err = d.resolveSession(msg.Platform, msg.UserID)
		if err != nil {
			return "", fmt.Errorf("resolve session: %w", err)
		}
		key := sessionKey(msg.Platform, msg.UserID)
		lease, leaseOK = d.acquireSessionLease(key, msg.Platform, msg.UserID, sess)
		if !leaseOK {
			continue
		}
		releaseRuntime = session.LockRuntime(d.sessionDir, sess.Manager.GetHeader().ID)
		if lease.promoteAfterRuntimeLock() {
			break
		}
		releaseRuntime()
		releaseRuntime = nil
		lease.release()
		lease = nil
	}
	if sess == nil || lease == nil || releaseRuntime == nil || !lease.promoted {
		return "", fmt.Errorf("session changed while message was waiting for runtime lock")
	}
	// A process restart can outlive the progress callback that belonged to the
	// original inbound message. Reconcile completed durable background output
	// before accepting the next message for this session.
	d.reconcileCompletedBackgroundRun(sess, msg.ProgressFunc)

	d.mu.RLock()
	backgroundSubmitter := d.backgroundSubmitter
	d.mu.RUnlock()
	if backgroundSubmitter != nil && d.responsesBackgroundEnabled() {
		backgroundReq := BackgroundRequest{
			Context: ctx, SessionID: sess.Manager.GetHeader().ID, WorkDir: sess.WorkDir,
			Platform: msg.Platform, UserID: msg.UserID, IdempotencyKey: channelMessageIdempotencyKey(msg), ModelID: func() string {
				if runtime.model == nil {
					return ""
				}
				return runtime.model.ID
			}(),
			Mode: sess.Mode, Text: msg.Text, Progress: msg.ProgressFunc,
		}
		lease.release()
		releaseRuntime()
		runID, err := backgroundSubmitter(backgroundReq)
		if err != nil {
			d.notifyRunObserver(sess.Manager.GetHeader().ID)
			return "", err
		}
		d.notifyRunObserver(sess.Manager.GetHeader().ID)
		return fmt.Sprintf("Responses background run queued: %s", runID), nil
	}

	sessionID := sess.Manager.GetHeader().ID
	d.notifyRunObserver(sessionID)
	defer func() {
		lease.release()
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
	runBase := d.runRootCtx
	if runBase == nil {
		runBase = context.Background()
	}
	runCtx, cancelRun := context.WithCancel(runBase)
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
		lease.release()
	}()
	runStartedAt := time.Now()
	modelID := ""
	if runtime.model != nil {
		modelID = runtime.model.ID
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

func channelMessageIdempotencyKey(msg messaging.InboundMessage) string {
	if strings.TrimSpace(msg.MessageID) == "" {
		return ""
	}
	// Scope provider event IDs to the channel identity; different platforms can
	// legitimately reuse the same native ID.
	return "channel:" + strings.TrimSpace(msg.Platform) + ":" + strings.TrimSpace(msg.UserID) + ":" + strings.TrimSpace(msg.MessageID)
}

func (d *Dispatcher) acquireCommandSession(platform, userID string) (*ChannelSession, func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		sess, err := d.resolveSession(platform, userID)
		if err != nil {
			return nil, nil, err
		}
		lease, ok := d.acquireSessionLease(sessionKey(platform, userID), platform, userID, sess)
		if !ok {
			continue
		}
		releaseRuntime := session.LockRuntime(d.sessionDir, sess.Manager.GetHeader().ID)
		if lease.promoteAfterRuntimeLock() {
			return sess, func() {
				releaseRuntime()
				lease.release()
			}, nil
		}
		releaseRuntime()
		lease.release()
	}
	return nil, nil, fmt.Errorf("session changed while waiting for runtime lock")
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

	cfg := d.cfg
	security := d.security
	sandboxEnabled := d.sandbox
	browserEnabled := d.browser
	a2aEnabled := d.a2aMaster
	multiAgentEnabled := d.multiAgent
	cronStore := d.cronStore
	scheduler := d.scheduler
	workDir := cfg.GetPlatformWorkDir(platform)
	if security != nil {
		if err := security.CheckWorkDirAllowed(workDir); err != nil {
			return nil, err
		}
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
	if sandboxEnabled {
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
	definitions := make(map[string]ChannelToolDefinition)
	for _, definition := range d.channelToolDefinitionsLocked(platform) {
		definitions[definition.Name] = definition
	}
	toolEnabled := func(name string, defaultEnabled bool) bool {
		if definition, ok := definitions[name]; ok {
			if !definition.Available {
				return false
			}
			defaultEnabled = definition.Default
		}
		if value, ok := enabled[name]; ok {
			return value
		}
		return !hasToolConfig && defaultEnabled
	}

	reg := tools.NewRegistry(workDir, sbMgr.GetActive())
	reg.RegisterDefaults()
	for _, item := range reg.All() {
		if !toolEnabled(item.Name(), true) {
			reg.Remove(item.Name())
		}
	}
	if toolEnabled("browser", browserEnabled) {
		browserfeature.RegisterTool(reg)
	}
	if toolEnabled("a2a_dispatch", a2aEnabled) {
		if err := d.registerA2AMasterTool(reg); err != nil {
			return nil, err
		}
	}
	if toolEnabled("memory", true) {
		reg.Register(memory.NewMemoryTool(memory.NewStore(cfg.Memory.Path, workDir)))
	}

	registerMultiAgent := false
	for name := range definitions {
		if isMultiAgentToolName(name) && toolEnabled(name, multiAgentEnabled) {
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

	if cronStore != nil && toolEnabled("cron", true) {
		sessionID := ""
		if header := mgr.GetHeader(); header != nil {
			sessionID = header.ID
		}
		reg.Register(cron.NewCronTool(cron.NewSessionScopedStoreWithWorkDir(cronStore, sessionID, workDir), scheduler))
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
	if generation, generationErr := session.GetChannelToolGeneration(d.sessionDir, mgr.GetHeader().ID); generationErr == nil {
		sess.generation = generation
	} else {
		log.Printf("[channels] load tool generation for %s: %v", key, generationErr)
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
	if platform != "wechat" && platform != "feishu" {
		d.mu.Lock()
		defer d.mu.Unlock()
		delete(d.sessions, key)
		return nil
	}
	for {
		bound, err := session.FindBinding(d.sessionDir, platform, userID)
		if err != nil {
			return fmt.Errorf("find channel binding: %w", err)
		}
		if bound == nil {
			return nil
		}
		releaseRuntime := session.LockRuntime(d.sessionDir, bound.SessionID)
		releaseIdentity := func() {}
		if d.identityLocks != nil {
			releaseIdentity = d.identityLocks.Lock(platform, userID)
		}
		current, readErr := session.FindBinding(d.sessionDir, platform, userID)
		if readErr != nil {
			releaseIdentity()
			releaseRuntime()
			return fmt.Errorf("recheck channel binding: %w", readErr)
		}
		if current == nil {
			releaseIdentity()
			releaseRuntime()
			return nil
		}
		if current.SessionID != bound.SessionID {
			releaseIdentity()
			releaseRuntime()
			continue
		}
		workDir := d.PlatformWorkDir(platform)
		if _, rotateErr := session.RotateBoundSession(workDir, d.sessionDir, platform, userID, current.SessionID); rotateErr != nil {
			releaseIdentity()
			releaseRuntime()
			return fmt.Errorf("rotate bound session: %w", rotateErr)
		}
		d.mu.Lock()
		if sess, ok := d.sessions[key]; ok {
			d.invalidateSessionLocked(key, sess)
		}
		d.mu.Unlock()
		releaseIdentity()
		releaseRuntime()
		return nil
	}
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
			d.invalidateSessionLocked(key, sess)
			return
		}
	}
}

// RemoveSession removes a session from the pool.
func (d *Dispatcher) RemoveSession(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sess, ok := d.sessions[key]; ok {
		d.invalidateSessionLocked(key, sess)
	}
}

func (d *Dispatcher) invalidateSessionLocked(key string, sess *ChannelSession) {
	if sess == nil {
		delete(d.sessions, key)
		return
	}
	busy := sess.pendingEntrants > 0 || sess.activeRuns > 0
	if busy {
		sess.invalidated = true
	}
	if busy {
		return
	}
	d.closeAndDeleteLocked(key, sess)
}

func (d *Dispatcher) closeAndDeleteLocked(key string, sess *ChannelSession) {
	if sess == nil {
		delete(d.sessions, key)
		return
	}
	if len(sess.MCPClients) > 0 {
		mcp.CloseClients(sess.MCPClients)
		sess.MCPClients = nil
	}
	delete(d.sessions, key)
}

func (d *Dispatcher) evictInvalidated(key string, target *ChannelSession) {
	d.mu.Lock()
	defer d.mu.Unlock()
	current := d.sessions[key]
	if current != target {
		return
	}
	d.closeAndDeleteLocked(key, target)
}

// Close cancels all channel runs and releases idle session resources.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	if d.runRootStop != nil {
		d.runRootStop()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for key, sess := range d.sessions {
		d.invalidateSessionLocked(key, sess)
	}
}

// buildAgent creates and configures an agent for a session.
// Returns the agent and a cleanup function to call with the run error.
func (d *Dispatcher) buildAgent(ctx context.Context, sess *ChannelSession, approvalHandler agentApprovalHandler) (*agent.Agent, func(error)) {
	runtime := d.runtimeSnapshot()
	cfg := runtime.cfg
	if cfg == nil {
		cfg = DefaultConfig()
	}
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
		Provider:           runtime.provider,
		Vendor:             runtime.providerName,
		Model:              runtime.model,
		Mode:               sess.Mode,
		ThinkingLevel:      provider.ThinkingLevel(d.settings.DefaultThinkingLevel),
		SandboxMgr:         sess.SandboxMgr,
		Settings:           d.settings,
		Allow:              runtime.allow,
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
		MaxIterations:            cfg.Agent.MaxTurns,
		ContextPressureThreshold: cfg.Agent.ContextPressureThreshold,
		BudgetPressureThreshold:  cfg.Agent.BudgetPressureThreshold,
		AfterToolCall: func(ctx2 agent.AfterToolCallContext) *agent.ToolCallResult {
			if runtime.hooksMgr != nil && runtime.hooksMgr.HasPostHook() {
				argsMap, _ := ctx2.Args.(map[string]any)
				errMsg := ""
				if ctx2.IsError {
					errMsg = ctx2.Result.Content
				}
				runtime.hooksMgr.PostToolCall(ctx, ctx2.ToolCall.Name, argsMap, ctx2.Result.Content, errMsg, sess.Platform, sess.UserID)
			}
			return nil
		},
	}, sess.Registry)

	var runErr error
	if runtime.agentMgr != nil {
		runtime.agentMgr.Register(agent.NewAgentAdapter(a))
	}
	cleanup := func(err error) {
		runErr = err
		if runtime.agentMgr != nil {
			runtime.agentMgr.Finish(a.ID(), runErr)
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
	runtime := d.runtimeSnapshot()
	return func(toolCallID, toolName string, args map[string]any) bool {
		if toolName == "git_access" {
			if progress != nil {
				progress("⛔ Git metadata access is not available in unattended channel sessions")
			}
			return false
		}
		if runtime.security != nil && runtime.security.ShouldAutoApprove(toolName, args, sess.Mode) {
			return true
		}

		risk := "medium"
		if toolName == "bash" {
			if cmd, ok := args["command"]; ok {
				risk = CommandRiskLevel(fmt.Sprintf("%v", cmd))
			}
		}

		if runtime.hooksMgr != nil && runtime.hooksMgr.HasPreHook() {
			allowed, _, _ := runtime.hooksMgr.PreToolCall(ctx, toolName, args, sess.Platform, sess.UserID)
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
		case agent.EventHostedItem:
			if progress != nil && ev.HostedItem != nil {
				typeName := strings.TrimSpace(ev.HostedItem.Type)
				status := strings.TrimSpace(ev.HostedItem.Status)
				if typeName != "" || status != "" {
					progress(fmt.Sprintf("Hosted tool %s: %s", typeName, status))
				}
			}
		case agent.EventTurnEnd:
			// The assistant turn has been appended to SQLite before this event is
			// emitted. Publish here so WebUI subscribers see each channel turn,
			// including tool-call turns, instead of waiting for EventDone.
			d.notifyRunObserver(sess.Manager.GetHeader().ID)
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
			// so unattended channel users can see why a reply was delayed, and
			// provider-timeout auto-retry notices so they know the task is not lost.
			if progress != nil {
				if strings.HasPrefix(ev.StatusMessage, "Context recovery:") {
					progress("🗜️ " + ev.StatusMessage)
				} else if strings.HasPrefix(ev.StatusMessage, "⚠️") {
					progress(ev.StatusMessage)
				}
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
	if attachmentText := FormatAttachmentSummary(attachments); attachmentText != "" {
		if result != "" {
			result += "\n\n"
		}
		result += attachmentText
	}

	return result, nil
}

// FormatAttachmentSummary renders provider-neutral attachment references for
// channel messages and background completion callbacks.
func FormatAttachmentSummary(items []provider.Attachment) string {
	return serviceruntime.FormatAttachmentSummary(items)
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
	browserEnabled := d.runtimeSnapshot().browser
	var extra string
	if d.settings.ContextFiles.Enabled {
		cfResult := contextfiles.LoadContextFiles(workDir, config.ConfigDir(), d.settings.ContextFiles.ExtraFiles)
		if ctx := contextfiles.BuildContextString(cfResult); ctx != "" {
			extra = ctx
		}
	}

	skillsMgr := skills.NewManagerWithProjectDirs(d.settings.GetGlobalSkillsDir(), skills.ProjectSkillDirs(workDir))
	if browserEnabled {
		if _, _, err := browserfeature.EnsureProjectSkill(workDir); err != nil {
			log.Printf("[channels] create browser skill: %v", err)
		}
	}
	_ = skillsMgr.Load()
	extra += skillsMgr.BuildAllSkillsContext()
	if browserEnabled {
		extra += skillsMgr.BuildSkillContext(browserfeature.SkillName)
	}

	return extra
}

func (d *Dispatcher) registerA2AMasterTool(registry *tools.Registry) error {
	if !d.a2aMaster {
		return nil
	}
	a2aListCfg, err := loadA2AAgentList()
	if err != nil {
		return fmt.Errorf("load a2a-list.json: %w", err)
	}
	a2aMgr := a2a.NewA2AManager(a2aListCfg)
	registry.Register(tools.NewA2ADispatchTool(&a2aDispatcherAdapter{mgr: a2aMgr}))
	return nil
}

func loadA2AAgentList() (*a2a.AgentListConfig, error) {
	a2aListPath := a2a.ProjectAgentListConfigPath()
	if _, err := os.Stat(a2aListPath); err != nil {
		a2aListPath = a2a.AgentListConfigPath()
	}
	return a2a.LoadAgentList(a2aListPath)
}

func a2aToolAvailability(enabled bool) (bool, string) {
	if !enabled {
		return false, "A2A master is disabled"
	}
	if _, err := loadA2AAgentList(); err != nil {
		return false, fmt.Sprintf("A2A agent list is unavailable: %v", err)
	}
	return true, ""
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
		handler := d.rotateHandlerForCommand()
		if err := handler(msg.Platform, msg.UserID); err != nil {
			return "❌ Failed to create new session: " + err.Error(), nil
		}
		return "✅ New session created.", nil
	case "/clear":
		handler := d.rotateHandlerForCommand()
		if err := handler(msg.Platform, msg.UserID); err != nil {
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
			sess, release, err := d.acquireCommandSession(msg.Platform, msg.UserID)
			if err != nil {
				return "❌ No active session.", nil
			}
			defer release()
			sess.Lock()
			defer sess.Unlock()
			sess.Mode = mode
			return fmt.Sprintf("✅ Mode set to %s.", mode), nil
		default:
			return "Invalid mode. Use: plan, agent, yolo", nil
		}
	case "/compact":
		sess, release, err := d.acquireCommandSession(msg.Platform, msg.UserID)
		if err != nil {
			return "❌ No active session.", nil
		}
		defer release()
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

func (d *Dispatcher) rotateHandlerForCommand() func(string, string) error {
	d.mu.RLock()
	handler := d.rotateHandler
	d.mu.RUnlock()
	if handler != nil {
		return handler
	}
	return d.RotateSession
}

// channelSessionDir returns the directory for a platform user's sessions.
func (d *Dispatcher) channelSessionDir(platform, userID string) string {
	return filepath.Join(d.sessionDir, "channels", safeSessionPathComponent(platform), safeSessionPathComponent(userID))
}

// sessionKey builds a session pool key.
func sessionKey(platform, userID string) string {
	return fmt.Sprintf("channels/%s/%s", platform, userID)
}

// channelRouteID returns the stable conversation identity used for channel
// sessions and outbound delivery. Feishu provides both an open_id (sender)
// and a chat_id (conversation); bindings must use the latter because the
// Feishu API sends with receive_id_type=chat_id. WeChat currently uses the
// same value for both fields, and other transports retain their user ID.
func channelRouteID(msg messaging.InboundMessage) string {
	if (msg.Platform == "feishu" || msg.Platform == "wechat") && msg.ChatID != "" {
		return msg.ChatID
	}
	return msg.UserID
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
