package agentruntime

import (
	"fmt"
	"time"

	"github.com/startvibecoding/mothx/internal/mcp"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/tools"
)

// AttachedResources are adapter-policy-selected resources attached to the
// common runtime. Use this only when protocol-specific registry or MCP policy
// cannot yet be represented by Builder; Runtime retains all lifecycle ownership.
type AttachedResources struct {
	ID           string
	Source       RuntimeSource
	EntrySource  RuntimeSource
	WorkDir      string
	Manager      *session.Manager
	Registry     *tools.Registry
	SandboxMgr   *sandbox.Manager
	SkillsMgr    *skills.Manager
	MCPClients   []*mcp.Client
	ExtraContext string
	RuleContent  string
}

// AttachSessionResources creates a SessionRuntime around already-selected
// resources. It validates the session ownership boundary and is the sole
// compatibility bridge for adapters with protocol-specific Registry/MCP policy.
func AttachSessionResources(resources AttachedResources) (*SessionRuntime, error) {
	if resources.WorkDir == "" || resources.Manager == nil || resources.Registry == nil {
		return nil, fmt.Errorf("runtime work directory, session manager, and registry are required")
	}
	if resources.ID == "" && resources.Manager.GetHeader() != nil {
		resources.ID = resources.Manager.GetHeader().ID
	}
	if resources.ID == "" {
		return nil, fmt.Errorf("runtime session ID is required")
	}
	resolved, err := resolveManagerSource(resources.Manager, SourceResolutionInput{Requested: resources.Source})
	if err != nil {
		return nil, err
	}
	entrySource := resources.EntrySource
	if entrySource == SourceUnknown {
		entrySource = resources.Source
	}
	return &SessionRuntime{
		ID: resources.ID, Source: resolved.Source, EntrySource: entrySource,
		Policy: PolicyForSource(resolved.Source, ""), WorkDir: resources.WorkDir,
		Manager: resources.Manager, Registry: resources.Registry, SandboxMgr: resources.SandboxMgr,
		SkillsMgr: resources.SkillsMgr, MCPClients: resources.MCPClients,
		ExtraContext: resources.ExtraContext, RuleContent: resources.RuleContent, LastUsed: time.Now(),
	}, nil
}
