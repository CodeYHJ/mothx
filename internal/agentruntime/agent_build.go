package agentruntime

import (
	"fmt"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
)

// AgentBuildOptions are per-run inputs supplied by an adapter after Runtime has
// resolved its source, policy, session resources, and effective mode.
type AgentBuildOptions struct {
	Provider            provider.Provider
	ProviderName        string
	Model               *provider.Model
	Settings            *config.Settings
	Allow               *config.AllowConfig
	Mode                string
	ThinkingLevel       provider.ThinkingLevel
	MultiAgent          bool
	DelegateMode        bool
	Workflows           bool
	ApprovalHandler     func(string, string, map[string]any) bool
	MaxIterations       int
	ContextPressure     float64
	BudgetPressure      float64
	AfterToolCall       func(agent.AfterToolCallContext) *agent.ToolCallResult
	GetSteeringMessages func() []provider.Message
}

// BuildAgent constructs a root agent over this session's existing Registry.
// This preserves adapter-selected session tools and MCP clients while keeping
// provider/config/sandbox/context assembly out of adapters.
func (r *SessionRuntime) BuildAgent(opts AgentBuildOptions) (*agent.Agent, error) {
	if r == nil || r.Registry == nil {
		return nil, fmt.Errorf("agent runtime registry is required")
	}
	if opts.Provider == nil || opts.Model == nil {
		return nil, fmt.Errorf("agent provider and model are required")
	}
	settings := opts.Settings
	if settings == nil {
		settings = &config.Settings{}
	}
	if r.Manager == nil {
		return nil, fmt.Errorf("agent runtime session manager is required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeAgent
	}
	return agent.NewWithLoopConfig(agent.AgentLoopConfig{
		Config: agent.Config{
			Provider: opts.Provider, Vendor: opts.ProviderName, Model: opts.Model, Mode: mode,
			ThinkingLevel: opts.ThinkingLevel, MaxTokens: agent.ResolveMaxTokens(opts.Model),
			SandboxMgr: r.SandboxMgr, Settings: settings, Allow: opts.Allow, Session: r.Manager,
			ExtraContext: r.ExtraContext, RuleContent: r.RuleContent,
			CompactionSettings: agent.CompactionSettingsFromConfig(settings.Compaction),
			ApprovalHandler:    opts.ApprovalHandler, MultiAgent: opts.MultiAgent,
			DelegateMode: opts.DelegateMode, Workflows: opts.Workflows,
		},
		MaxIterations: opts.MaxIterations, ContextPressureThreshold: opts.ContextPressure,
		BudgetPressureThreshold: opts.BudgetPressure, AfterToolCall: opts.AfterToolCall,
		GetSteeringMessages: opts.GetSteeringMessages,
	}, r.Registry), nil
}
