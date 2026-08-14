package tui

import (
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
)

// tuiRuntime wraps the resources already prepared by the CLI in the shared
// front-end-neutral Runtime. TUI remains responsible for constructing its
// existing Registry/MCP policy during this migration phase.
func tuiRuntime(sess *session.Manager, registry *tools.Registry, sandboxInfo, extraContext, ruleContent string, skillsMgr *skills.Manager) *agentruntime.SessionRuntime {
	_ = sandboxInfo
	_ = extraContext
	_ = ruleContent
	_ = skillsMgr
	if sess == nil || registry == nil {
		return nil
	}
	header := sess.GetHeader()
	if header == nil {
		return nil
	}
	runtime, err := agentruntime.AttachSessionResources(agentruntime.AttachedResources{
		ID: header.ID, Source: agentruntime.SourceTUI, WorkDir: header.Cwd,
		Manager: sess, Registry: registry,
	})
	if err != nil {
		return nil
	}
	return runtime
}

// SetRuntime attaches the shared front-end-neutral runtime to the TUI. The
// adapter aliases are synchronized for compatibility with existing commands.
func (a *App) SetRuntime(runtime *agentruntime.SessionRuntime) {
	if a == nil || runtime == nil {
		return
	}
	a.runtime = runtime
	if runtime.Manager != nil {
		a.session = runtime.Manager
	}
	if runtime.Registry != nil {
		a.registry = runtime.Registry
	}
	if runtime.SandboxMgr != nil {
		a.sandboxInfo = sandbox.FormatSandboxInfo(runtime.SandboxMgr.GetActive())
	}
	if runtime.SkillsMgr != nil {
		a.skillsMgr = runtime.SkillsMgr
	}
	if runtime.ExtraContext != "" {
		a.extraContext = runtime.ExtraContext
		a.baseExtraContext = runtime.ExtraContext
	}
	if runtime.RuleContent != "" {
		a.ruleContent = runtime.RuleContent
	}
}
func (a *App) buildRuntimeAgent() (*agent.Agent, error) {
	if a == nil || a.runtime == nil {
		return nil, nil
	}
	return a.runtime.BuildAgent(agentruntime.AgentBuildOptions{
		Provider: a.provider, ProviderName: a.providerName, Model: a.model,
		Settings: a.settings, Allow: a.allow, Mode: a.mode,
		ThinkingLevel: provider.ThinkingLevel(a.settings.DefaultThinkingLevel),
		MultiAgent:    a.multiAgent, DelegateMode: a.delegateMode, Workflows: a.workflows,
		GetSteeringMessages: a.nextESMSteeringMessages,
	})
}
