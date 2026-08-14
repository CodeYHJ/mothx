package agentruntime

import (
	"fmt"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/tools"
)

// AgentBuildOptions are per-run inputs supplied by an adapter after Runtime has
// resolved its source, policy, session resources, and effective mode.
type AgentBuildOptions struct {
	ID                     agentpkg.AgentID
	ParentID               agentpkg.AgentID
	Provider               provider.Provider
	ProviderName           string
	Model                  *provider.Model
	Settings               *config.Settings
	Allow                  *config.AllowConfig
	Mode                   string
	ExtraContext           string
	RuleContent            string
	ThinkingLevel          provider.ThinkingLevel
	MaxTokens              int
	MaxTokensSet           bool
	MultiAgent             bool
	DelegateMode           bool
	Workflows              bool
	ApprovalHandler        func(string, string, map[string]any) bool
	ApprovalDecisionLookup func(string, string, map[string]any) (bool, bool)
	MaxIterations          int
	ContextPressure        float64
	BudgetPressure         float64
	AfterToolCall          func(agent.AfterToolCallContext) *agent.ToolCallResult
	GetSteeringMessages    func() []provider.Message
}

// AgentBuildOptionsFromConfig converts the legacy Agent.Config shape used by
// provider-specific drivers into Runtime-owned build inputs.
func AgentBuildOptionsFromConfig(cfg agent.Config) AgentBuildOptions {
	return AgentBuildOptions{
		ID: cfg.ID, ParentID: cfg.ParentID, Provider: cfg.Provider, ProviderName: cfg.Vendor,
		Model: cfg.Model, Settings: cfg.Settings, Allow: cfg.Allow, Mode: cfg.Mode,
		RuleContent: cfg.RuleContent, ExtraContext: cfg.ExtraContext,
		ThinkingLevel: cfg.ThinkingLevel, MaxTokens: cfg.MaxTokens, MaxTokensSet: cfg.MaxTokensUserSet,
		MultiAgent: cfg.MultiAgent, DelegateMode: cfg.DelegateMode, Workflows: cfg.Workflows,
		ApprovalHandler: cfg.ApprovalHandler, ApprovalDecisionLookup: cfg.ApprovalDecisionLookup,
	}
}

// This preserves adapter-selected session tools and MCP clients while keeping
// provider/config/sandbox/context assembly out of adapters.
func (r *SessionRuntime) BuildAgent(opts AgentBuildOptions) (*agent.Agent, error) {
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	registry := r.Registry
	manager := r.Manager
	r.mu.RUnlock()
	return r.buildAgent(registry, manager, opts)
}

// BuildTransientAgent constructs a non-persisted agent over an adapter-provided
// registry. It is intended for temporary side queries such as TUI /btw. The
// shared Runtime still supplies provider-independent context and sandbox
// defaults, while the adapter retains ownership of the temporary registry.
func (r *SessionRuntime) BuildTransientAgent(registry *tools.Registry, opts AgentBuildOptions) (*agent.Agent, error) {
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, fmt.Errorf("transient agent registry is required")
	}
	return r.buildAgent(registry, nil, opts)
}

func (r *SessionRuntime) buildAgent(registry *tools.Registry, manager *session.Manager, opts AgentBuildOptions) (*agent.Agent, error) {
	if registry == nil {
		return nil, fmt.Errorf("agent runtime registry is required")
	}
	if opts.Provider == nil || opts.Model == nil {
		return nil, fmt.Errorf("agent provider and model are required")
	}
	r.mu.RLock()
	sandboxMgr := r.SandboxMgr
	extraContext := r.ExtraContext
	ruleContent := r.RuleContent
	r.mu.RUnlock()
	settings := opts.Settings
	if settings == nil {
		settings = &config.Settings{}
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeAgent
	}
	if opts.ExtraContext != "" {
		extraContext = opts.ExtraContext
	}
	if opts.RuleContent != "" {
		ruleContent = opts.RuleContent
	}
	maxTokens := agent.ResolveMaxTokens(opts.Model)
	if opts.MaxTokensSet {
		maxTokens = opts.MaxTokens
	}
	return agent.NewWithLoopConfig(agent.AgentLoopConfig{
		Config: agent.Config{
			ID: opts.ID, ParentID: opts.ParentID, Provider: opts.Provider, Vendor: opts.ProviderName, Model: opts.Model, Mode: mode,
			ThinkingLevel: opts.ThinkingLevel, MaxTokens: maxTokens,
			SandboxMgr: sandboxMgr, Settings: settings, Allow: opts.Allow, Session: manager,
			ExtraContext: extraContext, RuleContent: ruleContent,
			CompactionSettings: agent.CompactionSettingsFromConfig(settings.Compaction),
			ApprovalHandler:    opts.ApprovalHandler, ApprovalDecisionLookup: opts.ApprovalDecisionLookup, MultiAgent: opts.MultiAgent,
			DelegateMode: opts.DelegateMode, Workflows: opts.Workflows,
		},
		MaxIterations: opts.MaxIterations, ContextPressureThreshold: opts.ContextPressure,
		BudgetPressureThreshold: opts.BudgetPressure, AfterToolCall: opts.AfterToolCall,
		GetSteeringMessages: opts.GetSteeringMessages,
	}, registry), nil
}
