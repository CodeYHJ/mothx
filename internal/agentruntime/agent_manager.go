package agentruntime

import (
	"fmt"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/session"
)

// AgentManagerOptions binds provider-specific execution dependencies to a
// shared SessionRuntime. Adapter-specific event and approval handling remains
// outside this type.
type AgentManagerOptions struct {
	Runtime           *SessionRuntime
	Provider          provider.Provider
	Model             *provider.Model
	Settings          *config.Settings
	ProviderName      string
	Allow             *config.AllowConfig
	MultiAgentEnabled bool
	DelegateEnabled   bool
	WorkflowsEnabled  bool
}

// NewAgentManager constructs an AgentFactory and AgentManager using the shared
// runtime's sandbox, context, rules and skills. All entry points should use
// this path instead of assembling AgentFactory arguments independently.
func NewAgentManager(opts AgentManagerOptions) (*agent.AgentManager, error) {
	if opts.Runtime == nil {
		return nil, fmt.Errorf("agent runtime is required")
	}
	if opts.Settings == nil {
		return nil, fmt.Errorf("agent runtime settings are required")
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("agent provider is required")
	}
	if opts.Model == nil {
		return nil, fmt.Errorf("agent model is required")
	}
	policy, err := opts.Runtime.resolvedExecutionPolicy(ModeAgent)
	if err != nil {
		return nil, err
	}
	opts.Runtime.mu.RLock()
	runtimeManager := opts.Runtime.Manager
	entrySource := opts.Runtime.EntrySource
	opts.Runtime.mu.RUnlock()
	currentSourceFor := func(manager *session.Manager) RuntimeSource {
		if manager != nil && manager == runtimeManager {
			return policy.Source
		}
		return SourceUnknown
	}
	compaction := agent.CompactionSettingsFromConfig(opts.Settings.Compaction)
	factory := agent.NewAgentFactoryWithOptions(
		opts.Provider,
		opts.Model,
		opts.Settings,
		opts.Runtime.SandboxMgr,
		opts.Runtime.ExtraContext,
		opts.Runtime.RuleContent,
		opts.Runtime.SkillsMgr,
		compaction,
		nil,
		agent.AgentFactoryOptions{
			MultiAgentEnabled: opts.MultiAgentEnabled,
			DelegateEnabled:   opts.DelegateEnabled,
			WorkflowsEnabled:  opts.WorkflowsEnabled,
			ProviderName:      opts.ProviderName,
			Allow:             opts.Allow,
			BeforeToolCall:    beforeToolCallForPolicy(policy, nil),
			ForcedMode:        policy.ForcedMode(),
			ResolveMode: func(manager *session.Manager, requestedMode string) (string, error) {
				if manager == nil {
					return policy.ResolveMode("", requestedMode)
				}
				_, mode, err := resolveManagerPolicy(manager, SourceResolutionInput{
					Current: currentSourceFor(manager), Requested: entrySource,
				}, "", requestedMode, ModeAgent)
				return mode, err
			},
			BeforeToolCallForSession: func(manager *session.Manager) func(agent.BeforeToolCallContext) *agent.ToolCallBlockResult {
				if manager == nil {
					return nil
				}
				resolved, err := resolveManagerSource(manager, SourceResolutionInput{
					Current: currentSourceFor(manager), Requested: entrySource,
				})
				if err != nil {
					return func(agent.BeforeToolCallContext) *agent.ToolCallBlockResult {
						return &agent.ToolCallBlockResult{Block: true, Reason: err.Error()}
					}
				}
				return beforeToolCallForPolicy(PolicyForSource(resolved.Source, ModeAgent), nil)
			},
		},
	)
	return agent.NewAgentManager(factory), nil
}
