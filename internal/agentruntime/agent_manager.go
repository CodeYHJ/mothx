package agentruntime

import (
	"fmt"

	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/provider"
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
		},
	)
	return agent.NewAgentManager(factory), nil
}
