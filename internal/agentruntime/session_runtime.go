package agentruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/startvibecoding/mothx/internal/browser"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/contextfiles"
	"github.com/startvibecoding/mothx/internal/mcp"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
	"github.com/startvibecoding/mothx/internal/workflow"
)

// SessionRuntime is the front-end-neutral state required to construct and run
// an agent session. Adapters may wrap it with protocol-specific locks, approval
// state, and event delivery, but must not rebuild these shared resources.
type SessionRuntime struct {
	mu           sync.RWMutex
	closed       bool
	ID           string
	Source       RuntimeSource
	WorkDir      string
	Manager      *session.Manager
	Registry     *tools.Registry
	SandboxMgr   *sandbox.Manager
	SkillsMgr    *skills.Manager
	MCPClients   []*mcp.Client
	ExtraContext string
	RuleContent  string
	LastUsed     time.Time
	Execution    *ExecutionRuntime
}

// SetExecution attaches the session's canonical execution lifecycle.
func (r *SessionRuntime) SetExecution(execution *ExecutionRuntime) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.Execution = execution
	r.mu.Unlock()
}

// Shutdown cancels the active execution, waits for its terminal transition, and
// then releases Runtime-owned MCP resources. A context bounds the wait.
func (r *SessionRuntime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.RLock()
	execution := r.Execution
	r.mu.RUnlock()
	if execution != nil {
		if err := execution.Shutdown("session runtime shutdown"); err != nil {
			return err
		}
		if err := execution.Wait(ctx); err != nil {
			return err
		}
	}
	r.Close()
	return nil
}

// Close releases resources owned by this runtime. It is safe to call more than
// once and prevents new resource mutations after the first close.
func (r *SessionRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	clients := r.MCPClients
	r.MCPClients = nil
	r.mu.Unlock()
	mcp.CloseClients(clients)
}

func (r *SessionRuntime) ensureOpen() error {
	if r == nil {
		return fmt.Errorf("agent runtime is nil")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return fmt.Errorf("agent runtime is closed")
	}
	return nil
}

// BindSession attaches or replaces the persisted session identity owned by this
// Runtime. It is used by frontends that create sessions lazily.
func (r *SessionRuntime) BindSession(manager *session.Manager, requested RuntimeSource) error {
	if err := r.ensureOpen(); err != nil {
		return err
	}
	if manager == nil || manager.GetHeader() == nil {
		return fmt.Errorf("initialized session manager is required")
	}
	header := manager.GetHeader()
	resolved := ResolveSource(SourceResolutionInput{SessionHeader: header, Current: r.Source, Requested: requested})
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("agent runtime is closed")
	}
	r.ID = header.ID
	r.Source = resolved.Source
	r.WorkDir = header.Cwd
	r.Manager = manager
	r.LastUsed = time.Now()
	return nil
}

// UnbindSession clears persisted session identity while retaining reusable
// Runtime-owned resources for a frontend that will lazily create another session.
func (r *SessionRuntime) UnbindSession() error {
	if err := r.ensureOpen(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("agent runtime is closed")
	}
	r.ID = ""
	r.Manager = nil
	r.LastUsed = time.Now()
	return nil
}

type Builder struct {
	Settings     *config.Settings
	SandboxLevel sandbox.Level
}

// RegistryHook injects adapter-specific tools into a fully initialized shared
// runtime. Hooks run after core tools are registered and before MCP connects,
// so MCP tools see the final registry without owning Runtime lifecycle.
type RegistryHook func(*SessionRuntime) error

// BuildOptions are the resource-affecting session capabilities. They are kept
// separate from adapter-specific presentation and approval options.
type BuildOptions struct {
	ID            string
	Source        RuntimeSource
	WorkDir       string
	Manager       *session.Manager
	Workflows     bool
	Browser       bool
	RegistryHooks []RegistryHook
}

// RefreshOptions are mutable resource-affecting session capabilities.
type RefreshOptions struct {
	Workflows    bool
	Browser      bool
	ActiveSkills map[string]bool
}

// ContextResources are the shared context/skill inputs used by a session runtime.
type ContextResources struct {
	SkillsMgr    *skills.Manager
	ExtraContext string
	RuleContent  string
}

// Build constructs context, skills, sandbox, tools and MCP connections for one
// session. The caller owns the returned runtime and must call Close on failures
// after successful construction or when the session is evicted.
func (b Builder) Build(ctx context.Context, opts BuildOptions) (*SessionRuntime, error) {
	if b.Settings == nil {
		return nil, fmt.Errorf("agent runtime settings are required")
	}
	if opts.WorkDir == "" {
		return nil, fmt.Errorf("agent runtime work directory is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resources, err := LoadContextResources(b.Settings, opts.WorkDir, opts.Workflows, opts.Browser)
	if err != nil {
		return nil, err
	}
	skillsMgr, extraContext := resources.SkillsMgr, resources.ExtraContext

	sandboxMgr := sandbox.NewManagerWithOptions(opts.WorkDir, b.Settings.Sandbox.Options())
	if err := sandboxMgr.SetLevel(b.SandboxLevel); err != nil {
		return nil, fmt.Errorf("sandbox for work directory: %w", err)
	}
	registry := tools.NewRegistry(opts.WorkDir, sandboxMgr.GetActive())
	registry.RegisterDefaultsWithPlanTool(b.Settings.IsPlanToolEnabled())
	if b.Settings.IsImageGenerationEnabled() {
		registry.Register(tools.NewImageGenerationTool(b.Settings))
	}
	if skillsMgr != nil {
		registry.Register(tools.NewSkillRefTool(skillsMgr))
	}
	if opts.Browser {
		browser.RegisterTool(registry)
	}

	runtime := &SessionRuntime{
		ID:           opts.ID,
		Source:       opts.Source,
		WorkDir:      opts.WorkDir,
		Manager:      opts.Manager,
		Registry:     registry,
		SandboxMgr:   sandboxMgr,
		SkillsMgr:    skillsMgr,
		ExtraContext: extraContext,
		RuleContent:  resources.RuleContent,
		LastUsed:     time.Now(),
	}
	if err := runtime.ApplyRegistryHooks(opts.RegistryHooks); err != nil {
		return nil, err
	}
	servers, err := mcp.LoadConfiguredServers(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	clients, err := mcp.ConnectServers(ctx, servers, registry, mcp.Callbacks{})
	if err != nil {
		return nil, fmt.Errorf("connect MCP servers: %w", err)
	}
	runtime.MCPClients = clients
	return runtime, nil
}

// ApplyRegistryHooks injects adapter-owned tools into this Runtime. It is
// intentionally limited to Registry mutation; policy resolution, sandbox,
// allow rules, session ownership and run lifecycle remain Runtime-owned.
func (r *SessionRuntime) ApplyRegistryHooks(hooks []RegistryHook) error {
	if err := r.ensureOpen(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("agent runtime is closed")
	}
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if err := hook(r); err != nil {
			return err
		}
	}
	return nil
}

// RefreshResources reloads context files and skills, synchronizes the shared
// skill_ref/browser tools, and updates the Runtime fields atomically after all
// validation succeeds. Adapter-specific AgentManager and optional tools are
// deliberately outside this method.
func (r *SessionRuntime) RefreshResources(settings *config.Settings, opts RefreshOptions) error {
	if r == nil {
		return fmt.Errorf("agent runtime is nil")
	}
	if settings == nil {
		return fmt.Errorf("agent runtime settings are required")
	}
	if err := r.ensureOpen(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("agent runtime is closed")
	}
	resources, err := LoadContextResources(settings, r.WorkDir, opts.Workflows, opts.Browser)
	if err != nil {
		return err
	}
	skillsMgr, extraContext := resources.SkillsMgr, resources.ExtraContext
	activeContext, err := activeSkillsContext(skillsMgr, opts.ActiveSkills)
	if err != nil {
		return err
	}
	if r.Registry != nil {
		r.Registry.Register(tools.NewSkillRefTool(skillsMgr))
	}
	r.synchronizeCoreToolsLocked(opts.Browser)
	r.SkillsMgr = skillsMgr
	r.ExtraContext = extraContext + activeContext
	r.RuleContent = resources.RuleContent
	r.LastUsed = time.Now()
	return nil
}

// SynchronizeCoreTools applies mutable registry tools that have no adapter
// dependency. It is safe to call when context content itself is unchanged.
func (r *SessionRuntime) SynchronizeCoreTools(browserEnabled bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.synchronizeCoreToolsLocked(browserEnabled)
}

func (r *SessionRuntime) synchronizeCoreToolsLocked(browserEnabled bool) {
	if r.Registry == nil {
		return
	}
	if browserEnabled {
		browser.RegisterTool(r.Registry)
	} else {
		browser.RemoveTool(r.Registry)
	}
}

func activeSkillsContext(manager *skills.Manager, active map[string]bool) (string, error) {
	if len(active) == 0 {
		return "", nil
	}
	names := make([]string, 0, len(active))
	for name, enabled := range active {
		if enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var context strings.Builder
	for _, name := range names {
		if manager == nil || manager.Get(name) == nil {
			return "", fmt.Errorf("skill not found: %s", name)
		}
		context.WriteString(manager.BuildSkillContext(name))
	}
	return context.String(), nil
}

// LoadContextResources loads context files, project/global skills, and rules.
// It is shared by all adapters; adapters choose only the requested capabilities.
func LoadContextResources(settings *config.Settings, workDir string, workflows, browserEnabled bool) (*ContextResources, error) {
	if workflows {
		if _, _, err := workflow.EnsureProjectSkill(workDir); err != nil {
			return nil, fmt.Errorf("create workflow skill: %w", err)
		}
	}
	if browserEnabled {
		if _, _, err := browser.EnsureProjectSkill(workDir); err != nil {
			return nil, fmt.Errorf("create browser skill: %w", err)
		}
	}
	skillsMgr := skills.NewManagerWithProjectDirs(settings.GetGlobalSkillsDir(), skills.ProjectSkillDirs(workDir))
	_ = skillsMgr.Load()

	var extraContext string
	if settings.ContextFiles.Enabled {
		result := contextfiles.LoadContextFiles(workDir, config.ConfigDir(), settings.ContextFiles.ExtraFiles)
		extraContext = contextfiles.BuildContextString(result)
	}
	extraContext += skillsMgr.BuildAllSkillsContext()
	if workflows {
		extraContext += skillsMgr.BuildSkillContext(workflow.SkillName)
	}
	if browserEnabled {
		extraContext += skillsMgr.BuildSkillContext(browser.SkillName)
	}
	return &ContextResources{SkillsMgr: skillsMgr, ExtraContext: extraContext, RuleContent: contextfiles.LoadRuleFile(workDir)}, nil
}
