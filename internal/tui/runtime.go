package tui

import (
	"fmt"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/provider"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
)

// tuiRuntime wraps CLI-prepared resources in the shared Runtime. Resource
// ownership remains a migration bridge until CLI construction moves into Builder.
func tuiRuntime(sess *session.Manager, registry *tools.Registry, sandboxInfo, extraContext, ruleContent string, skillsMgr *skills.Manager) *agentruntime.SessionRuntime {
	_ = sandboxInfo
	if sess == nil || registry == nil {
		return nil
	}
	header := sess.GetHeader()
	if header == nil {
		return nil
	}
	resolved := agentruntime.ResolveSource(agentruntime.SourceResolutionInput{
		SessionHeader: header, Current: agentruntime.SourceTUI, Requested: agentruntime.SourceTUI,
	})
	runtime, err := agentruntime.AttachSessionResources(agentruntime.AttachedResources{
		ID: header.ID, Source: resolved.Source, WorkDir: header.Cwd, Manager: sess, Registry: registry,
		ExtraContext: extraContext, RuleContent: ruleContent, SkillsMgr: skillsMgr,
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

func (a *App) bindRuntimeSession(manager *session.Manager) error {
	if a == nil || a.runtime == nil {
		return nil
	}
	return a.runtime.BindSession(manager, agentruntime.SourceTUI)
}
func (a *App) effectiveRuntimeMode() (string, error) {
	if a == nil || a.runtime == nil {
		return "", fmt.Errorf("tui session runtime is unavailable")
	}
	var header *session.Header
	if a.session != nil {
		header = a.session.GetHeader()
	}
	_, mode, err := agentruntime.ResolvePolicy(agentruntime.SourceResolutionInput{
		SessionHeader: header, Current: a.runtime.Source, Requested: agentruntime.SourceTUI,
	}, a.mode, "", a.settings.DefaultMode)
	return mode, err
}

func (a *App) buildRuntimeAgent() (*agent.Agent, error) {
	if a == nil {
		return nil, fmt.Errorf("tui app is nil")
	}
	if a.runtime == nil {
		runtime := tuiRuntime(a.session, a.registry, a.sandboxInfo, a.extraContext, a.ruleContent, a.skillsMgr)
		if runtime == nil {
			return nil, fmt.Errorf("tui session runtime is unavailable")
		}
		a.SetRuntime(runtime)
	}
	mode, err := a.effectiveRuntimeMode()
	if err != nil {
		return nil, err
	}
	return a.runtime.BuildAgent(agentruntime.AgentBuildOptions{
		ID: agentpkg.AgentID("agent-master"), Provider: a.provider, ProviderName: a.providerName,
		Model: a.model, Settings: a.settings, Allow: a.allow, Mode: mode,
		ExtraContext: a.extraContext, ThinkingLevel: provider.ThinkingLevel(a.settings.DefaultThinkingLevel),
		MultiAgent: a.multiAgent, DelegateMode: a.delegateMode, Workflows: a.workflows,
		GetSteeringMessages: a.nextESMSteeringMessages,
	})
}
