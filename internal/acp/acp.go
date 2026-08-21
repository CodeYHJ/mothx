package acp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/startvibecoding/mothx/agent"
	"github.com/startvibecoding/mothx/internal/agent"
	"github.com/startvibecoding/mothx/internal/agentruntime"
	"github.com/startvibecoding/mothx/internal/config"
	"github.com/startvibecoding/mothx/internal/debugpprof"
	"github.com/startvibecoding/mothx/internal/mcp"
	"github.com/startvibecoding/mothx/internal/provider"
	providerfactory "github.com/startvibecoding/mothx/internal/provider/factory"
	"github.com/startvibecoding/mothx/internal/sandbox"
	"github.com/startvibecoding/mothx/internal/session"
	"github.com/startvibecoding/mothx/internal/skills"
	"github.com/startvibecoding/mothx/internal/systeminit"
	"github.com/startvibecoding/mothx/internal/tools"
	"github.com/startvibecoding/mothx/internal/workflow"
)

const protocolVersion = 1
const maxRequestBytes = 10 << 20

var errEmptyMessage = errors.New("empty message")

const mothxExtensionNamespace = "mothx.dev"

type RunOptions struct {
	Provider   string
	Model      string
	Mode       string
	Thinking   string
	Sandbox    bool
	Verbose    bool
	Debug      bool
	MultiAgent bool
	Delegate   bool
	Workflows  bool
	WebSearch  bool
	Browser    bool
}

type server struct {
	mu  sync.Mutex
	wmu sync.Mutex

	settings *config.Settings
	allow    *config.AllowConfig
	cwd      string

	p            provider.Provider
	providerName string
	m            *provider.Model

	mode          string
	thinkingLevel provider.ThinkingLevel
	sbMgr         *sandbox.Manager
	skillsMgr     *skills.Manager
	extraContext  string
	ruleContent   string
	contextFiles  string

	multiAgent bool
	delegate   bool
	workflows  bool
	browser    bool
	runtime    *agentruntime.SessionRuntime
	agentMgr   *agent.AgentManager

	sessions map[string]*sessionRuntime
	pending  map[string]chan json.RawMessage

	toolTitles  map[string]string
	mcpNotify   map[string]bool
	initialized bool
	clientCaps  clientCapabilities

	nextID int64
	r      *bufio.Reader
	w      io.Writer

	permissionTimeout time.Duration
}

type sessionRuntime struct {
	runtime   *agentruntime.SessionRuntime
	execution *agentruntime.ExecutionRuntime
	decisions *agentruntime.DecisionService
	id        string
	mgr       *session.Manager
	agent     agentpkg.Agent
	registry  *tools.Registry
	cancel    context.CancelFunc
	promptID  string
	// runID is the canonical durable run identity. promptID remains the ACP
	// request key used by $/cancel_request and must not be conflated with it.
	runID            string
	closed           bool
	terminalNotified bool
	cancelMu         sync.Mutex
	// ACP message IDs group streamed chunks into logical messages. They are
	// scoped to the active prompt and are reset before each turn.
	messageID        string
	thoughtMessageID string
	userMessageID    string
	activeModel      *provider.Model
	activeMode       string
	activeThinking   provider.ThinkingLevel
	mcp              []*mcp.Client
	agentMgr         *agent.AgentManager

	usageMu sync.Mutex
	cost    float64
}

var errACPActiveSessionRun = errors.New("session already has an active run")

// acquirePromptAdmission serializes ACP with the other local entry points and
// checks the durable row before attempting the unique active-run insert. The
// database check is still needed for a run owned by another process; the
// shared runtime lock covers concurrent local adapters and prompt requests.
func (s *server) acquirePromptAdmission(rt *sessionRuntime) (func(), error) {
	if s == nil || s.settings == nil || rt == nil || strings.TrimSpace(rt.id) == "" {
		return nil, fmt.Errorf("ACP session runtime is unavailable")
	}
	release, ok := session.TryLockRuntime(s.settings.GetSessionDir(), rt.id)
	if !ok {
		return nil, errACPActiveSessionRun
	}
	active, err := session.GetActiveSessionRun(s.settings.GetSessionDir(), rt.id)
	if err != nil {
		release()
		return nil, fmt.Errorf("check active session run: %w", err)
	}
	if active != nil {
		release()
		return nil, errACPActiveSessionRun
	}
	rt.cancelMu.Lock()
	localActive := rt.cancel != nil
	rt.cancelMu.Unlock()
	if localActive {
		release()
		return nil, errACPActiveSessionRun
	}
	return release, nil
}

// closeResources releases the shared runtime resources. The legacy MCP slice
// remains as a compatibility alias for ACP session fixtures during migration.
func (r *sessionRuntime) closeResources() {
	if r == nil {
		return
	}
	if r.runtime != nil {
		r.runtime.Close()
		r.mcp = nil
		return
	}
	agentruntime.CloseMCPClients(r.mcp)
	r.mcp = nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcp.RPCError   `json:"error,omitempty"`
}

type clientInfo struct {
	Name    string `json:"name,omitempty"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type initializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities,omitempty"`
	ClientInfo         clientInfo         `json:"clientInfo,omitempty"`
}

// clientCapabilities is deliberately typed even where the current Runtime
// does not invoke client-owned reverse requests. This keeps capability
// negotiation truthful and gives future fs/terminal/elicitation bridges one
// shared state model instead of ACP-local ad hoc maps.
type clientCapabilities struct {
	FS          *clientFSCapabilities          `json:"fs,omitempty"`
	Terminal    bool                           `json:"terminal,omitempty"`
	Auth        *clientAuthCapabilities        `json:"auth,omitempty"`
	Elicitation *clientElicitationCapabilities `json:"elicitation,omitempty"`
	Session     *clientSessionCapabilities     `json:"session,omitempty"`
}

type clientFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

type clientAuthCapabilities struct {
	Terminal bool `json:"terminal,omitempty"`
}

type clientElicitationCapabilities struct {
	Form *struct{} `json:"form,omitempty"`
	URL  *struct{} `json:"url,omitempty"`
}

type clientSessionCapabilities struct {
	ConfigOptions *clientConfigOptionCapabilities `json:"configOptions,omitempty"`
}

type clientConfigOptionCapabilities struct {
	Boolean *struct{} `json:"boolean,omitempty"`
}

type initializeResult struct {
	ProtocolVersion   int          `json:"protocolVersion"`
	AgentCapabilities agentCaps    `json:"agentCapabilities"`
	AgentInfo         clientInfo   `json:"agentInfo"`
	AuthMethods       []authMethod `json:"authMethods"`
}

type authMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type agentCaps struct {
	LoadSession         bool           `json:"loadSession"`
	PromptCapabilities  promptCaps     `json:"promptCapabilities"`
	SessionCapabilities sessionCaps    `json:"sessionCapabilities"`
	McPCapabilities     mcpCaps        `json:"mcpCapabilities"`
	Meta                map[string]any `json:"_meta,omitempty"`
}

type mcpCaps struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type promptCaps struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type sessionCaps struct {
	// session/new, session/prompt, session/cancel, and session/update are
	// required ACP v1 baseline methods, so they are not capability flags.
	Close                 *struct{} `json:"close,omitempty"`
	Delete                *struct{} `json:"delete,omitempty"`
	List                  *struct{} `json:"list,omitempty"`
	Resume                *struct{} `json:"resume,omitempty"`
	AdditionalDirectories *struct{} `json:"additionalDirectories,omitempty"`
}

type newSessionRequest struct {
	Cwd                   string             `json:"cwd"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
	McpServers            []mcp.ServerConfig `json:"mcpServers,omitempty"`
}

type newSessionResult struct {
	SessionID     string                             `json:"sessionId"`
	Modes         *sessionModeState                  `json:"modes,omitempty"`
	ConfigOptions []agentruntime.SessionConfigOption `json:"configOptions,omitempty"`
}

type sessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []sessionMode `json:"availableModes"`
}

type sessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func sessionModes(runtime *agentruntime.SessionRuntime) *sessionModeState {
	if runtime == nil {
		return nil
	}
	_, _, _, mode, _ := runtime.ConfigSnapshot()
	return &sessionModeState{
		CurrentModeID: mode,
		AvailableModes: []sessionMode{
			{ID: agentruntime.ModeAgent, Name: "Agent"},
			{ID: agentruntime.ModePlan, Name: "Plan"},
			{ID: agentruntime.ModeYolo, Name: "Yolo"},
			{ID: agentruntime.ModeOS, Name: "OS"},
		},
	}
}

type setConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type setModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId,omitempty"`
	// Mode is accepted as a compatibility alias for older ACP clients.
	Mode string `json:"mode,omitempty"`
}

type loadSessionRequest struct {
	SessionID             string             `json:"sessionId"`
	Cwd                   string             `json:"cwd"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
	McpServers            []mcp.ServerConfig `json:"mcpServers,omitempty"`
}

type resumeSessionRequest struct {
	SessionID             string             `json:"sessionId"`
	Cwd                   string             `json:"cwd"`
	AdditionalDirectories []string           `json:"additionalDirectories,omitempty"`
	McpServers            []mcp.ServerConfig `json:"mcpServers,omitempty"`
}

type promptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

type promptResult struct {
	StopReason string `json:"stopReason"`
}

type cancelRequest struct {
	SessionID string `json:"sessionId"`
}

type closeSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type deleteSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type cancelRequestNotification struct {
	RequestID json.RawMessage `json:"requestId"`
}

type listSessionsRequest struct {
	Cwd    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type listSessionsResult struct {
	Sessions   []listedSession `json:"sessions"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type listedSession struct {
	SessionID             string         `json:"sessionId"`
	Cwd                   string         `json:"cwd"`
	AdditionalDirectories []string       `json:"additionalDirectories,omitempty"`
	Title                 string         `json:"title,omitempty"`
	UpdatedAt             string         `json:"updatedAt,omitempty"`
	Meta                  map[string]any `json:"_meta,omitempty"`
}

type requestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  permissionToolCall `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type contentBlock struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Data        string `json:"data,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URI         string `json:"uri,omitempty"`
	Size        *int   `json:"size,omitempty"`
}

type sessionUpdate struct {
	SessionUpdate     string                             `json:"sessionUpdate"`
	MessageID         string                             `json:"messageId,omitempty"`
	ConfigID          string                             `json:"configId,omitempty"`
	Value             string                             `json:"value,omitempty"`
	ConfigOptions     []agentruntime.SessionConfigOption `json:"configOptions,omitempty"`
	CurrentModeID     string                             `json:"currentModeId,omitempty"`
	Content           any                                `json:"content,omitempty"`
	ToolCallID        string                             `json:"toolCallId,omitempty"`
	Locations         []toolCallLocation                 `json:"locations,omitempty"`
	Title             string                             `json:"title,omitempty"`
	Kind              string                             `json:"kind,omitempty"`
	Status            string                             `json:"status,omitempty"`
	RawInput          map[string]any                     `json:"rawInput,omitempty"`
	RawOutput         map[string]any                     `json:"rawOutput,omitempty"`
	Used              *int                               `json:"used,omitempty"`
	Size              *int                               `json:"size,omitempty"`
	Cost              *usageCost                         `json:"cost,omitempty"`
	Entries           []planEntry                        `json:"entries,omitempty"`
	AvailableCommands []availableCommand                 `json:"availableCommands,omitempty"`
	UpdatedAt         string                             `json:"updatedAt,omitempty"`
	Meta              map[string]any                     `json:"_meta,omitempty"`
}

type toolCallContent struct {
	Type    string        `json:"type"`
	Content *contentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`
	OldText *string       `json:"-"`
	NewText string        `json:"-"`
}

// MarshalJSON keeps the ACP ToolCallContent union strict. In particular, a
// newly-created file must encode oldText as JSON null rather than omitting the
// field, while text content must not acquire diff-only fields.
func (c toolCallContent) MarshalJSON() ([]byte, error) {
	switch c.Type {
	case "diff":
		return json.Marshal(struct {
			Type    string  `json:"type"`
			Path    string  `json:"path"`
			OldText *string `json:"oldText"`
			NewText string  `json:"newText"`
		}{Type: c.Type, Path: c.Path, OldText: c.OldText, NewText: c.NewText})
	default:
		return json.Marshal(struct {
			Type    string        `json:"type"`
			Content *contentBlock `json:"content,omitempty"`
		}{Type: c.Type, Content: c.Content})
	}
}

type availableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
}

type toolCallLocation struct {
	Path string `json:"path"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type usageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type permissionToolCall struct {
	ToolCallID string         `json:"toolCallId"`
	Title      string         `json:"title,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Status     string         `json:"status,omitempty"`
	RawInput   map[string]any `json:"rawInput,omitempty"`
}

type questionRequest struct {
	SessionID   string   `json:"sessionId"`
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Explanation string   `json:"explanation,omitempty"`
	TimeoutMs   int64    `json:"timeoutMs"`
	// Protocol marks requests persisted for standard ACP elicitation replay.
	// Legacy extension payloads leave it empty so their wire shape remains
	// backwards compatible.
	Protocol string `json:"protocol,omitempty"`
}

type questionResult struct {
	Answer string `json:"answer,omitempty"`
}

type elicitationCreateRequest struct {
	SessionID       string         `json:"sessionId"`
	Message         string         `json:"message"`
	Mode            string         `json:"mode"`
	RequestedSchema map[string]any `json:"requestedSchema"`
}

type elicitationResult struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

type permissionResult struct {
	Outcome *permissionOutcome `json:"outcome,omitempty"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// Run starts the ACP stdio server.
func Run(opts RunOptions) error {
	config.Verbose = opts.Verbose || opts.Debug
	if opts.Debug {
		_ = os.Setenv("VIBECODING_DEBUG", "1")
		debugpprof.StartForDebug(os.Stderr)
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	// ACP Agent loops are local to this process and cannot be reattached after
	// restart. Recover only ACP-owned rows here; another local adapter may be
	// running against the same session database and remains responsible for its
	// own orphan policy.
	if _, err := agentruntime.RecoverOrphanedRuns(settings.GetSessionDir(), func(run session.SessionRun) agentruntime.RecoveryAction {
		if strings.EqualFold(strings.TrimSpace(run.Source), string(agentruntime.SourceACP)) {
			return agentruntime.RecoveryFailLocal
		}
		return agentruntime.RecoveryKeepRemote
	}, nil); err != nil {
		return fmt.Errorf("recover orphaned runs: %w", err)
	}
	if opts.WebSearch {
		settings.WebSearch.Enabled = config.BoolPtr(true)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	srv := &server{
		settings:   settings,
		allow:      config.LoadAllow(),
		cwd:        cwd,
		multiAgent: opts.MultiAgent,
		delegate:   opts.Delegate,
		workflows:  opts.Workflows,
		browser:    opts.Browser,
		sessions:   make(map[string]*sessionRuntime),
		pending:    make(map[string]chan json.RawMessage),
		toolTitles: make(map[string]string),
		mcpNotify:  make(map[string]bool),
		r:          bufio.NewReader(os.Stdin),
		w:          os.Stdout,
	}
	defer srv.shutdownAllSessionRuntimes()

	requestedModel, requestedSet := os.LookupEnv("HARBOR_ACP_REQUESTED_MODEL")
	providerName, modelID, err := resolveACPModelSelection(opts, requestedModel, requestedSet)
	if err != nil {
		return err
	}
	p, model, err := createProvider(settings, providerName, modelID)
	if err != nil {
		return err
	}
	srv.p = p
	srv.providerName = providerName
	if srv.providerName == "" {
		srv.providerName = settings.DefaultProvider
	}
	srv.m = model

	mode := opts.Mode
	if mode == "" {
		mode = settings.DefaultMode
	}
	if mode == "" {
		mode = "agent"
	}
	srv.mode = mode

	thinkingLevel := opts.Thinking
	if thinkingLevel == "" {
		thinkingLevel = settings.DefaultThinkingLevel
	}
	srv.thinkingLevel = provider.ThinkingLevel(thinkingLevel)

	sbMgr := sandbox.NewManagerWithOptions(cwd, settings.Sandbox.Options())
	sbEnabled := opts.Sandbox || settings.Sandbox.Enabled
	if !sbEnabled {
		_ = sbMgr.SetLevel(sandbox.LevelNone)
	} else {
		level := sandbox.LevelStandard
		if settings.Sandbox.Level == "strict" {
			level = sandbox.LevelStrict
		}
		if err := sbMgr.SetLevel(level); err != nil {
			return fmt.Errorf("strict sandbox enabled but unavailable: %w", err)
		}
		if err := sbMgr.FallbackError(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: sandbox unavailable; using direct execution: %v\n", err)
		}
	}
	srv.sbMgr = sbMgr

	resources, err := agentruntime.LoadContextResources(settings, cwd, opts.Workflows, opts.Browser)
	if err != nil {
		return err
	}
	srv.skillsMgr = resources.SkillsMgr
	srv.extraContext = resources.ExtraContext
	srv.ruleContent = resources.RuleContent

	srv.runtime = &agentruntime.SessionRuntime{
		Source: agentruntime.SourceACP, EntrySource: agentruntime.SourceACP,
		WorkDir: cwd, SandboxMgr: sbMgr, SkillsMgr: resources.SkillsMgr,
		ExtraContext: srv.extraContext, RuleContent: srv.ruleContent,
	}
	// Agent manager backs multi-agent and delegate workflows.
	if opts.MultiAgent || opts.Delegate || opts.Workflows {
		mgr, err := agentruntime.NewAgentManager(agentruntime.AgentManagerOptions{
			Runtime: srv.runtime, Provider: p, Model: model, Settings: settings,
			ProviderName: srv.providerName, Allow: srv.allow, MultiAgentEnabled: true,
			DelegateEnabled: opts.Delegate, WorkflowsEnabled: opts.Workflows,
		})
		if err != nil {
			return fmt.Errorf("create agent manager: %w", err)
		}
		srv.agentMgr = mgr
	}

	for {
		req, err := srv.readRequest()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if errors.Is(err, errEmptyMessage) {
				continue
			}
			if err := srv.writeMessage(map[string]any{
				"jsonrpc": "2.0",
				"id":      nil,
				"error":   &mcp.RPCError{Code: -32700, Message: "parse error"},
			}); err != nil {
				return err
			}
			continue
		}
		if req.JSONRPC != "2.0" || !validRPCID(req.ID) {
			id := req.ID
			if !validRPCID(id) || (len(bytes.TrimSpace(id)) == 0 && req.JSONRPC != "2.0") {
				id = json.RawMessage("null")
			}
			if len(req.ID) > 0 || req.JSONRPC != "2.0" {
				srv.writeResponse(id, nil, &mcp.RPCError{Code: -32600, Message: "invalid request"})
			}
			continue
		}

		if len(req.Method) == 0 && len(req.ID) > 0 {
			srv.deliverResponse(req.ID, req.Result, req.Error)
			continue
		}
		if len(req.Method) == 0 {
			continue
		}
		if req.Method != "initialize" {
			srv.mu.Lock()
			initialized := srv.initialized
			srv.mu.Unlock()
			if !initialized {
				if len(req.ID) > 0 {
					srv.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32600, Message: "initialize must be called first"})
				}
				continue
			}
		}

		switch req.Method {
		case "initialize":
			srv.handleInitialize(req)
		case "session/new":
			srv.handleNewSession(req)
		case "session/load":
			srv.handleLoadSession(req)
		case "session/resume":
			srv.handleResumeSession(req)
		case "session/prompt":
			srv.handlePrompt(req)
		case "session/cancel":
			srv.handleCancel(req)
		case "$/cancel_request":
			srv.handleCancelRequest(req)
		case "session/close":
			srv.handleCloseSession(req)
		case "session/delete":
			srv.handleDeleteSession(req)
		case "session/list":
			srv.handleListSessions(req)
		case "session/set_config_option":
			srv.handleSetConfigOption(req)
		case "session/set_mode":
			srv.handleSetMode(req)
		default:
			if len(req.ID) > 0 {
				srv.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32601, Message: "method not found"})
			}
		}
	}
}

// resolveACPModelSelection applies the Harbor requested-model override while
// keeping explicit CLI selections fail-closed when they disagree. A caller
// may provide either provider or model explicitly; the environment supplies
// the missing half, but never silently changes an explicit value.
func resolveACPModelSelection(opts RunOptions, requested string, requestedSet bool) (providerName, modelID string, err error) {
	providerName = strings.TrimSpace(opts.Provider)
	modelID = strings.TrimSpace(opts.Model)
	if !requestedSet {
		return providerName, modelID, nil
	}
	requestedProvider, requestedModel, err := providerfactory.ParseQualifiedModel(requested)
	if err != nil {
		return "", "", fmt.Errorf("parse HARBOR_ACP_REQUESTED_MODEL: %w", err)
	}
	if providerName != "" && !strings.EqualFold(providerName, requestedProvider) {
		return "", "", fmt.Errorf("HARBOR_ACP_REQUESTED_MODEL provider %q conflicts with configured provider %q", requestedProvider, providerName)
	}
	if modelID != "" && !strings.EqualFold(modelID, requestedModel) {
		return "", "", fmt.Errorf("HARBOR_ACP_REQUESTED_MODEL model %q conflicts with configured model %q", requestedModel, modelID)
	}
	return requestedProvider, requestedModel, nil
}

func createProvider(settings *config.Settings, providerName, modelID string) (provider.Provider, *provider.Model, error) {
	enabled := true
	return providerfactory.CreateWithOptions(settings, providerName, modelID, providerfactory.Options{
		BuiltinAnthropicCacheControl: &enabled,
		RequireModel:                 true,
	})
}

func (s *server) newToolRegistry(cwd string) *tools.Registry {
	if cwd == "" {
		cwd = s.cwd
	}
	registry, err := agentruntime.BuildRegistry(cwd, s.sbMgr, s.settings, agentruntime.RegistryPolicy{
		RegisterDefaults: true,
		EnablePlanTool:   agentruntime.DefaultPlanToolPolicy(s.settings),
		SkillsMgr:        s.skillsMgr,
		Browser:          s.browser,
		Mutators: []agentruntime.RegistryMutator{func(registry *tools.Registry) error {
			// The interactive question tool is exposed in plan/agent modes (see
			// Registry.ModeTools). ACP maps it to request_permission.
			registry.Register(tools.NewQuestionTool(registry))
			if s.agentMgr != nil {
				if s.multiAgent {
					agent.RegisterSubAgentTools(registry, s.agentMgr)
				}
				if s.delegate {
					agent.RegisterDelegateSubAgentTool(registry, s.agentMgr)
				}
				if s.workflows {
					workflow.RegisterTools(registry, s.agentMgr, nil)
				}
			}
			return nil
		}},
	})
	if err != nil {
		return nil
	}
	return registry
}

// configureSessionBindings restores persisted per-session configuration and
// records defaults for sessions created before these entries were introduced.
// Provider construction remains process-owned; only the model binding varies
// between ACP sessions.
func (s *server) configureSessionBindings(runtime *agentruntime.SessionRuntime, mgr *session.Manager) error {
	if runtime == nil || mgr == nil {
		return fmt.Errorf("session runtime and manager are required")
	}
	// Some unit fixtures exercise session replay without constructing the
	// process-owned ACP provider. Leave those runtimes unbound; production ACP
	// servers always initialize s.p before accepting session requests.
	if s == nil || s.p == nil {
		return nil
	}
	model := s.m
	modelEntry, hasModel := mgr.GetLatestModelChange()
	if hasModel {
		if modelEntry.Provider != "" && !strings.EqualFold(modelEntry.Provider, s.providerName) {
			return fmt.Errorf("session model provider %q does not match ACP provider %q", modelEntry.Provider, s.providerName)
		}
		resolved, err := providerfactory.ResolveModel(s.p, s.providerName, modelEntry.ModelID)
		if err != nil {
			return err
		}
		model = resolved
	}
	if model == nil {
		return fmt.Errorf("ACP provider has no usable model")
	}
	mode := s.mode
	modeEntry, hasMode := mgr.GetLatestModeChange()
	if hasMode && strings.TrimSpace(modeEntry.Mode) != "" {
		mode = modeEntry.Mode
	}
	thinking := s.thinkingLevel
	thinkingEntry, hasThinking := mgr.GetLatestThinkingLevelChange()
	if hasThinking && strings.TrimSpace(thinkingEntry.ThinkingLevel) != "" {
		thinking = provider.ThinkingLevel(thinkingEntry.ThinkingLevel)
	}
	_, effectiveMode, err := runtime.ResolvePolicy(mode, mode, agentruntime.ModeAgent)
	if err != nil {
		return err
	}
	if err := runtime.ConfigureSession(s.p, s.providerName, model, effectiveMode, thinking); err != nil {
		return err
	}
	if !hasModel {
		if _, err := mgr.AppendModelChange(s.providerName, model.ID); err != nil {
			return err
		}
	}
	if !hasMode {
		if _, err := mgr.AppendModeChange(effectiveMode); err != nil {
			return err
		}
	}
	if !hasThinking {
		_, _, _, _, effectiveThinking := runtime.ConfigSnapshot()
		if _, err := mgr.AppendThinkingLevelChange(string(effectiveThinking)); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) sessionConfigOptions(sessionID string) []agentruntime.SessionConfigOption {
	rt := s.sessionRuntime(sessionID)
	if rt == nil || rt.runtime == nil {
		return nil
	}
	return rt.runtime.ConfigOptions()
}

func (s *server) handleInitialize(req rpcRequest) {
	var in initializeRequest
	if len(bytes.TrimSpace(req.Params)) == 0 {
		// Keep direct unit fixtures and legacy embedders working; wire clients
		// must provide protocolVersion per ACP v1.
		in.ProtocolVersion = protocolVersion
	} else if err := json.Unmarshal(req.Params, &in); err != nil || in.ProtocolVersion != protocolVersion {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: fmt.Sprintf("unsupported protocolVersion %d", in.ProtocolVersion)})
		return
	}
	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32600, Message: "initialize may only be called once"})
		return
	}
	s.initialized = true
	s.clientCaps = in.ClientCapabilities
	s.mu.Unlock()
	result := initializeResult{
		ProtocolVersion: protocolVersion,
		AgentCapabilities: agentCaps{
			LoadSession: true,
			PromptCapabilities: promptCaps{
				Image:           false,
				Audio:           false,
				EmbeddedContext: false,
			},
			SessionCapabilities: sessionCaps{
				Close:                 &struct{}{},
				Delete:                &struct{}{},
				List:                  &struct{}{},
				Resume:                &struct{}{},
				AdditionalDirectories: &struct{}{},
			},
			McPCapabilities: mcpCaps{HTTP: true, SSE: true},
			Meta: map[string]any{
				mothxExtensionNamespace: map[string]any{
					"requestQuestion": true,
					"sessionEvent":    true,
				},
			},
		},
		AgentInfo: clientInfo{
			Name:    "vibecoding",
			Title:   "VibeCoding",
			Version: "dev",
		},
		AuthMethods: []authMethod{},
	}
	s.writeResponse(req.ID, result, nil)
}

func (s *server) handleNewSession(req rpcRequest) {
	var in newSessionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if strings.TrimSpace(in.Cwd) == "" {
		in.Cwd = s.cwd
	}
	if !filepath.IsAbs(in.Cwd) {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "cwd must be an absolute path"})
		return
	}
	additionalDirectories, err := agentruntime.NormalizeAdditionalDirectories(in.AdditionalDirectories)
	if err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: err.Error()})
		return
	}
	mgr, err := agentruntime.CreateSession(agentruntime.CreateSessionOptions{WorkDir: in.Cwd, SessionDir: s.settings.GetSessionDir()})
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	id := mgr.GetHeader().ID
	registry := s.newToolRegistry(in.Cwd)
	if registry == nil {
		_ = session.DeleteSession(mgr.GetFile(), s.settings.GetSessionDir())
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "build ACP registry failed"})
		return
	}
	runtime, err := agentruntime.AttachSessionResources(agentruntime.AttachedResources{
		ID: id, Source: agentruntime.SourceACP, WorkDir: in.Cwd, Manager: mgr, Registry: registry,
		SandboxMgr: s.sbMgr, SkillsMgr: s.skillsMgr, ExtraContext: s.extraContext, RuleContent: s.ruleContent,
	})
	if err == nil {
		err = s.configureSessionBindings(runtime, mgr)
	}
	if err == nil {
		registry.SetAdditionalDirectories(runtime.AdditionalDirectoriesSnapshot())
	}
	if err == nil {
		err = runtime.SetAdditionalDirectories(additionalDirectories)
	}
	if err == nil {
		registry.SetAdditionalDirectories(runtime.AdditionalDirectoriesSnapshot())
	}
	if err == nil {
		err = runtime.ConnectMCP(context.Background(), agentruntime.MCPPolicy{Servers: in.McpServers, Callbacks: s.buildMCPCallbacks(id)})
	}
	if err != nil {
		runtime.Close()
		if cleanupErr := session.DeleteSession(mgr.GetFile(), s.settings.GetSessionDir()); cleanupErr != nil {
			log.Printf("[acp] cleanup failed session %s: %v", id, cleanupErr)
		}
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	mcpClients := runtime.MCPClients
	s.mu.Lock()
	if old := s.sessions[id]; old != nil {
		old.closeResources()
	}
	runRuntime := &agentruntime.ExecutionRuntime{}
	runRuntime.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
	runtime.SetExecution(runRuntime)
	runRuntime.SetEventSink(agentruntime.SessionRunEventSink{SessionDir: s.settings.GetSessionDir()})
	s.sessions[id] = &sessionRuntime{
		runtime: runtime, execution: runRuntime,
		decisions: &agentruntime.DecisionService{},
		id:        id, mgr: mgr, registry: registry, mcp: mcpClients,
	}
	runtime.SetDecisions(s.sessions[id].decisions)
	s.mu.Unlock()
	s.writeResponse(req.ID, newSessionResult{SessionID: id, Modes: sessionModes(runtime), ConfigOptions: runtime.ConfigOptions()}, nil)
}

func (s *server) handleLoadSession(req rpcRequest) {
	var in loadSessionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if strings.TrimSpace(in.Cwd) == "" {
		in.Cwd = s.cwd
	}
	if !filepath.IsAbs(in.Cwd) {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "cwd must be an absolute path"})
		return
	}
	additionalDirectories, err := agentruntime.NormalizeAdditionalDirectories(in.AdditionalDirectories)
	if err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: err.Error()})
		return
	}
	if existing := s.sessionRuntime(in.SessionID); existing != nil {
		if err := existing.runtime.SetAdditionalDirectories(additionalDirectories); err != nil {
			s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
			return
		}
		existing.registry.SetAdditionalDirectories(existing.runtime.AdditionalDirectoriesSnapshot())
		if err := s.replayPendingDecisionRequests(in.SessionID); err != nil {
			s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
			return
		}
		for _, msg := range existing.mgr.GetMessages() {
			s.emitMessage(in.SessionID, msg)
		}
		s.writeResponse(req.ID, newSessionResult{SessionID: in.SessionID, Modes: sessionModes(existing.runtime), ConfigOptions: existing.runtime.ConfigOptions()}, nil)
		return
	}
	rt, err := s.openSessionRuntime(in.SessionID, in.Cwd, in.McpServers)
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	if err := rt.runtime.SetAdditionalDirectories(additionalDirectories); err != nil {
		rt.closeResources()
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	rt.registry.SetAdditionalDirectories(rt.runtime.AdditionalDirectoriesSnapshot())
	s.installSessionRuntime(rt)
	allMsgs := rt.mgr.GetMessages()
	for _, msg := range allMsgs {
		s.emitMessage(in.SessionID, msg)
	}
	s.writeResponse(req.ID, newSessionResult{SessionID: in.SessionID, Modes: sessionModes(rt.runtime), ConfigOptions: rt.runtime.ConfigOptions()}, nil)
}

func (s *server) handleResumeSession(req rpcRequest) {
	var in resumeSessionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if !filepath.IsAbs(in.Cwd) {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "cwd must be an absolute path"})
		return
	}
	additionalDirectories, err := agentruntime.NormalizeAdditionalDirectories(in.AdditionalDirectories)
	if err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: err.Error()})
		return
	}
	if existing := s.sessionRuntime(in.SessionID); existing != nil {
		if err := existing.runtime.SetAdditionalDirectories(additionalDirectories); err != nil {
			s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
			return
		}
		existing.registry.SetAdditionalDirectories(existing.runtime.AdditionalDirectoriesSnapshot())
		if err := s.replayPendingDecisionRequests(in.SessionID); err != nil {
			s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
			return
		}
		s.writeResponse(req.ID, newSessionResult{SessionID: in.SessionID, Modes: sessionModes(existing.runtime), ConfigOptions: existing.runtime.ConfigOptions()}, nil)
		return
	}
	rt, err := s.openSessionRuntime(in.SessionID, in.Cwd, in.McpServers)
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	if err := rt.runtime.SetAdditionalDirectories(additionalDirectories); err != nil {
		rt.closeResources()
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	rt.registry.SetAdditionalDirectories(rt.runtime.AdditionalDirectoriesSnapshot())
	s.installSessionRuntime(rt)
	s.writeResponse(req.ID, newSessionResult{SessionID: in.SessionID, Modes: sessionModes(rt.runtime), ConfigOptions: rt.runtime.ConfigOptions()}, nil)
}

func (s *server) handleSetConfigOption(req rpcRequest) {
	var in setConfigOptionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil || strings.TrimSpace(in.SessionID) == "" || strings.TrimSpace(in.ConfigID) == "" || strings.TrimSpace(in.Value) == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "sessionId, configId, and value are required"})
		return
	}
	rt := s.sessionRuntime(in.SessionID)
	if rt == nil || rt.runtime == nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "unknown session"})
		return
	}
	// ACP permits changing session configuration while an agent is generating.
	// SessionRuntime snapshots the binding for the active prompt, so this
	// updates the next prompt without hot-switching the current Agent.
	if err := rt.runtime.SetConfigOption(in.ConfigID, in.Value); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: err.Error()})
		return
	}
	options := rt.runtime.ConfigOptions()
	s.notify(in.SessionID, sessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: options})
	s.notifySessionInfo(in.SessionID)
	s.writeResponse(req.ID, map[string]any{"configOptions": options}, nil)
}

func (s *server) handleSetMode(req rpcRequest) {
	var in setModeRequest
	if err := json.Unmarshal(req.Params, &in); err != nil || strings.TrimSpace(in.SessionID) == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "sessionId and mode are required"})
		return
	}
	modeID := strings.TrimSpace(in.ModeID)
	if modeID == "" {
		modeID = strings.TrimSpace(in.Mode)
	}
	if modeID == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "sessionId and modeId are required"})
		return
	}
	rt := s.sessionRuntime(in.SessionID)
	if rt == nil || rt.runtime == nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "unknown session"})
		return
	}
	// SessionRuntime snapshots the binding for the active prompt; changing the
	// mode here therefore affects the next prompt and remains protocol-safe.
	if err := rt.runtime.SetConfigOption(agentruntime.ConfigOptionMode, modeID); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: err.Error()})
		return
	}
	options := rt.runtime.ConfigOptions()
	_, _, _, effectiveMode, _ := rt.runtime.ConfigSnapshot()
	s.notify(in.SessionID, sessionUpdate{SessionUpdate: "current_mode_update", CurrentModeID: effectiveMode})
	s.notify(in.SessionID, sessionUpdate{SessionUpdate: "config_option_update", ConfigOptions: options})
	s.notifySessionInfo(in.SessionID)
	s.writeResponse(req.ID, map[string]any{}, nil)
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func (s *server) sessionRuntime(sessionID string) *sessionRuntime {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	return rt
}

func (s *server) openSessionRuntime(sessionID, cwd string, servers []mcp.ServerConfig) (*sessionRuntime, error) {
	registry := s.newToolRegistry(cwd)
	if registry == nil {
		return nil, fmt.Errorf("build ACP registry failed")
	}
	mgr, err := agentruntime.OpenSessionForWorkDir(cwd, s.settings.GetSessionDir(), sessionID)
	if err != nil {
		return nil, err
	}
	resolvedSource, err := agentruntime.ResolveSourceFromSession(s.settings.GetSessionDir(), sessionID, agentruntime.SourceResolutionInput{
		SessionHeader: mgr.GetHeader(), Requested: agentruntime.SourceACP,
	})
	if err != nil {
		return nil, err
	}
	runtime, err := agentruntime.AttachSessionResources(agentruntime.AttachedResources{
		ID: sessionID, Source: resolvedSource.Source, EntrySource: agentruntime.SourceACP, WorkDir: cwd, Manager: mgr, Registry: registry,
		SandboxMgr: s.sbMgr, SkillsMgr: s.skillsMgr, ExtraContext: s.extraContext, RuleContent: s.ruleContent,
	})
	if err == nil {
		err = s.configureSessionBindings(runtime, mgr)
	}
	if err == nil {
		registry.SetAdditionalDirectories(runtime.AdditionalDirectoriesSnapshot())
	}
	if err == nil {
		err = runtime.ConnectMCP(context.Background(), agentruntime.MCPPolicy{Servers: servers, Callbacks: s.buildMCPCallbacks(sessionID)})
	}
	if err != nil {
		runtime.Close()
		return nil, err
	}
	mcpClients := runtime.MCPClients
	runRuntime := &agentruntime.ExecutionRuntime{}
	runRuntime.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
	runtime.SetExecution(runRuntime)
	runRuntime.SetEventSink(agentruntime.SessionRunEventSink{SessionDir: s.settings.GetSessionDir()})
	rt := &sessionRuntime{
		runtime: runtime, execution: runRuntime,
		decisions: &agentruntime.DecisionService{},
		id:        sessionID, mgr: mgr, registry: registry, mcp: mcpClients,
		cost: s.persistedSessionCost(mgr, runtimeModel(runtime)),
	}
	runtime.SetDecisions(rt.decisions)
	if err := s.rehydrateSessionDecisions(rt); err != nil {
		rt.closeResources()
		return nil, err
	}
	return rt, nil
}

func (s *server) installSessionRuntime(rt *sessionRuntime) {
	s.mu.Lock()
	old := s.sessions[rt.id]
	s.sessions[rt.id] = rt
	s.mu.Unlock()
	if old != nil {
		_ = s.shutdownSessionRuntime(old)
	}
}

func (s *server) handlePrompt(req rpcRequest) {
	var in promptRequest
	if err := json.Unmarshal(req.Params, &in); err != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	rt := s.sessionForPrompt(in.SessionID)
	if rt == nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "unknown session"})
		return
	}
	promptMessage, err := promptToMessage(in.Prompt)
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhaseAdmission))
		return
	}
	userText := strings.TrimSpace(promptMessage.Content)
	if userText == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "empty prompt"})
		return
	}
	if rt.runtime == nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "session runtime is unavailable"})
		return
	}
	sessionProvider, sessionProviderName, sessionModel, sessionMode, sessionThinking := rt.runtime.ConfigSnapshot()
	if sessionProvider == nil || sessionModel == nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "session model is unavailable"})
		return
	}
	resolution, effectiveMode, err := rt.runtime.ResolvePolicy(sessionMode, "", agentruntime.ModeAgent)
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhaseAdmission))
		return
	}
	runSource := string(resolution.Source)
	if runSource == "" {
		runSource = string(agentruntime.SourceACP)
	}
	// Expand the /systeminit slash command into the full instruction prompt.
	// In ACP the question tool is available, so use the interactive variant.
	// /systeminit must also be able to write AGENTS.md, so upgrade plan mode to
	// agent for this prompt only.
	if fields := strings.Fields(strings.TrimSpace(userText)); len(fields) > 0 && fields[0] == systeminit.Command {
		extra := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(userText), systeminit.Command))
		userText = systeminit.Prompt(true, extra)
		if effectiveMode == "plan" {
			effectiveMode = "agent"
		}
	}
	promptKey := mcp.RawIDKey(req.ID)
	// ACP SDK request IDs restart at 0 for every connection (initialize,
	// new_session, set_config_option, prompt), so a bare request ID would
	// collide with runs persisted by earlier processes. Keep the request ID
	// for readability but append a random suffix for durable uniqueness.
	runID := "acp_" + promptKey + "_" + session.GenerateID()
	runtimeRelease, admissionErr := s.acquirePromptAdmission(rt)
	if admissionErr != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(admissionErr, nil, agentruntime.PhaseAdmission))
		return
	}
	admissionTransferred := false
	defer func() {
		if !admissionTransferred {
			runtimeRelease()
		}
	}()
	if rt.execution == nil {
		rt.execution = &agentruntime.ExecutionRuntime{}
		rt.execution.SetRunStore(agentruntime.RunStore{SessionDir: s.settings.GetSessionDir()})
		rt.execution.SetEventSink(agentruntime.SessionRunEventSink{SessionDir: s.settings.GetSessionDir()})
	}
	startedAt := time.Now()
	requestSnapshot, snapshotErr := json.Marshal(map[string]any{"prompt": userText, "request": in})
	if snapshotErr != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(snapshotErr, nil, agentruntime.PhaseAdmission))
		return
	}
	webSearchEnabled := false
	sandboxEnabled := false
	if s.settings != nil {
		webSearchEnabled = s.settings.IsWebSearchEnabled()
		sandboxEnabled = s.settings.Sandbox.Enabled
	}
	workDir := rt.runtime.WorkDir
	if workDir == "" {
		workDir = s.cwd
	}
	policySnapshot, snapshotErr := json.Marshal(map[string]any{
		"source": runSource, "mode": effectiveMode, "workDir": workDir,
		"capabilities": map[string]any{"multiAgent": s.multiAgent, "delegate": s.delegate, "workflows": s.workflows, "browser": s.browser, "webSearch": webSearchEnabled},
		"sandbox":      map[string]any{"enabled": sandboxEnabled}, "approvalPolicy": "runtime", "questionPolicy": "runtime",
	})
	if snapshotErr != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(snapshotErr, nil, agentruntime.PhaseAdmission))
		return
	}
	intent := agentruntime.ExecutionIntent{ID: "intent_" + session.GenerateID(), SessionID: rt.id, Source: runSource, Model: sessionModel.ID, Mode: effectiveMode, WorkDir: workDir, RequestFingerprint: fmt.Sprintf("prompt:%x", sha256.Sum256(requestSnapshot)), Request: requestSnapshot, Policy: policySnapshot, CreatedAt: startedAt}
	startData, _ := json.Marshal(map[string]any{"intentId": intent.ID, "attempt": 1})
	ctx, err := rt.execution.BeginIntentDurable(context.Background(), intent, agentruntime.DurableRun{
		ID: runID, SessionID: rt.id, IntentID: intent.ID, Attempt: 1, WorkDir: workDir, Source: runSource, Model: sessionModel.ID, Mode: effectiveMode,
		Status: "running", StartedAt: startedAt,
	}, agentruntime.RunEvent{SessionID: rt.id, RunID: runID, EventType: "started", Source: runSource, Status: "running", Model: sessionModel.ID, Mode: effectiveMode, Timestamp: startedAt, Data: startData})
	if err != nil {
		if active, activeErr := session.GetActiveSessionRun(s.settings.GetSessionDir(), rt.id); activeErr == nil && active != nil {
			err = errACPActiveSessionRun
		}
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhaseAdmission))
		return
	}
	cancel := func() { rt.execution.Cancel() }
	finishEarly := func(state agentruntime.RunState, message string) {
		cancel()
		_ = rt.execution.FinishDurable(runID, state, message, agentruntime.RunEvent{
			SessionID: rt.id, RunID: runID, EventType: "finished", Source: runSource,
			Status: string(state), Model: sessionModel.ID, Mode: effectiveMode, Timestamp: time.Now(),
		})
	}
	rt.cancelMu.Lock()
	rt.cancel = cancel
	rt.promptID = promptKey
	rt.runID = runID
	rt.messageID = "acp_" + rt.id + "_" + promptKey + "_message"
	rt.thoughtMessageID = "acp_" + rt.id + "_" + promptKey + "_thought"
	rt.userMessageID = "acp_" + rt.id + "_" + promptKey + "_user"
	rt.activeModel = sessionModel
	rt.activeMode = effectiveMode
	rt.activeThinking = sessionThinking
	rt.terminalNotified = false
	rt.cancelMu.Unlock()
	// Echo the accepted user content using the same message grouping contract
	// used by streamed agent chunks.
	s.notify(rt.id, sessionUpdate{SessionUpdate: "user_message_chunk", MessageID: rt.userMessageID, Content: &contentBlock{Type: "text", Text: userText}})

	a, err := rt.runtime.BuildAgent(agentruntime.AgentBuildOptions{
		Provider: sessionProvider, ProviderName: sessionProviderName, Model: sessionModel,
		Settings: s.settings, Allow: s.allow, Mode: effectiveMode, ThinkingLevel: sessionThinking,
		MultiAgent: s.multiAgent, DelegateMode: s.delegate, Workflows: s.workflows,
		ApprovalHandler: func(toolCallID, toolName string, args map[string]any) bool {
			if err := rt.execution.WaitForApproval(runID); err != nil {
				return false
			}
			defer func() { _ = rt.execution.Resume(runID) }()
			return s.requestPermissionContext(ctx, rt.id, toolCallID, toolName, args)
		},
	})
	if err != nil {
		rt.cancelMu.Lock()
		rt.cancel = nil
		rt.promptID = ""
		rt.runID = ""
		rt.activeModel = nil
		rt.activeMode = ""
		rt.activeThinking = ""
		rt.cancelMu.Unlock()
		info, observeErr := rt.execution.RecordFailure(err, agentruntime.ErrorClassificationOptions{Phase: agentruntime.PhaseAdmission})
		if observeErr != nil {
			log.Printf("[acp] record agent build failure for %s: %v", runID, observeErr)
		}
		log.Printf("[acp] build agent for %s failed: %v", runID, err)
		finishEarly(agentruntime.RunStateFailed, agentruntime.DisplayErrorMessage(info))
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, &info, agentruntime.PhaseAdmission))
		return
	}
	rt.execution.SetAgent(a)
	if s.agentMgr != nil {
		s.agentMgr.Register(agent.NewAgentAdapter(a))
	}
	rt.agent = agent.NewAgentAdapter(a)
	// The runtime lock is held for the full lifetime of the admitted Run so
	// another adapter cannot race its terminal persistence with a new Run.
	admissionTransferred = true
	go func() {
		defer runtimeRelease()
		stopReason := "end_turn"
		var runErr error
		var terminalInfo *agentruntime.ErrorInfo
		defer func() {
			if s.agentMgr != nil && rt.agent != nil {
				s.agentMgr.Finish(rt.agent.ID(), runErr)
			}
			rt.cancelMu.Lock()
			closed := rt.closed
			if rt.promptID == promptKey {
				rt.cancel = nil
				rt.promptID = ""
				rt.runID = ""
				rt.messageID = ""
				rt.thoughtMessageID = ""
				rt.userMessageID = ""
				rt.activeModel = nil
				rt.activeMode = ""
				rt.activeThinking = ""
			}
			rt.cancelMu.Unlock()
			if closed {
				rt.closeResources()
			}
			cancel()
			state := agentruntime.RunStateCompleted
			switch {
			case errors.Is(runErr, context.DeadlineExceeded):
				state = agentruntime.RunStateTimedOut
			case stopReason == "cancelled" || errors.Is(runErr, context.Canceled):
				state = agentruntime.RunStateCancelled
			case runErr != nil:
				state = agentruntime.RunStateFailed
			}
			message := ""
			var data json.RawMessage
			if runErr != nil {
				info := acpFailureInfo(runErr, terminalInfo, agentruntime.PhaseModel)
				message = agentruntime.DisplayErrorMessage(info)
				data, _ = json.Marshal(map[string]any{"error": message, "errorInfo": info})
			}
			_ = rt.execution.FinishDurable(runID, state, message, agentruntime.RunEvent{SessionID: rt.id, RunID: runID, EventType: "finished", Source: runSource, Status: string(state), Model: sessionModel.ID, Mode: effectiveMode, Timestamp: time.Now(), Data: data})
		}()
		// Consume the canonical internal event stream for Runtime observation,
		// then project each event to ACP's public wire format below.
		events := a.RunWithUserMessage(ctx, promptMessage)
		terminalSeen := false
		legacyTerminalSeen := false
		for coreEvent := range events {
			// Child-agent terminal events are projected to ACP as sub-agent
			// activity and must not mutate the parent Run's terminal facts.
			if coreEvent.AgentID == "" || (coreEvent.Type != agent.EventRunFinished && coreEvent.Type != agent.EventError) {
				observation, observeErr := rt.execution.ObserveAgentEvent(coreEvent)
				if observeErr != nil {
					log.Printf("[acp] observe agent event for %s: %v", runID, observeErr)
				} else if observation.Error != nil {
					info := *observation.Error
					terminalInfo = &info
				}
			}
			ev := agent.EventToPublic(coreEvent)
			s.handleAgentEvent(rt.id, ev)
			// A child-agent timeout is isolated to the child and must not
			// turn the ACP request into a failed parent run.
			if ev.AgentID != "" {
				continue
			}
			switch ev.Type {
			case agentpkg.EventQuestionRequest:
				go s.handleQuestion(ctx, rt, runID, ev)
			case agentpkg.EventRunFinished:
				terminalSeen = true
				switch ev.Status {
				case agentpkg.TaskFailed:
					if ev.Error != nil {
						runErr = ev.Error
					} else {
						runErr = errors.New("run failed")
					}
					stopReason = normalizeStopReason(ev.StopReason)
				case agentpkg.TaskCanceled:
					stopReason = "cancelled"
				case agentpkg.TaskIncomplete:
					stopReason = "max_tokens"
				default:
					stopReason = normalizeStopReason(ev.StopReason)
				}
			case agentpkg.EventDone:
				if !terminalSeen {
					legacyTerminalSeen = true
					stopReason = normalizeStopReason(ev.StopReason)
				}
			case agentpkg.EventError:
				if !terminalSeen {
					legacyTerminalSeen = true
					if ev.Error != nil {
						runErr = ev.Error
					} else {
						runErr = errors.New("agent error event without error detail")
					}
					stopReason = normalizeStopReason(ev.StopReason)
				}
			}
		}
		if !terminalSeen && !legacyTerminalSeen && runErr == nil {
			// Event stream closed without any terminal event — protocol failure,
			// never a successful completion.
			runErr = errors.New("event stream closed without terminal result")
			info, observeErr := rt.execution.RecordFailure(runErr, agentruntime.ErrorClassificationOptions{
				Code: "event_stream_interrupted", Type: "transport_error", Phase: agentruntime.PhaseTransport,
				MessageKey: "run.error.streamInterrupted", Message: "The run stopped before it could finish.",
			})
			if observeErr != nil {
				log.Printf("[acp] record interrupted stream for %s: %v", runID, observeErr)
			}
			terminalInfo = &info
		}
		if runErr != nil && stopReason != "cancelled" {
			log.Printf("[acp] agent prompt %s failed: %v", runID, runErr)
			s.writeResponse(req.ID, nil, acpFailureRPCError(runErr, terminalInfo, agentruntime.PhaseModel))
			return
		}
		s.writeResponse(req.ID, promptResult{StopReason: stopReason}, nil)
	}()
}

func (s *server) handleCancel(req rpcRequest) {
	var in cancelRequest
	if err := json.Unmarshal(req.Params, &in); err != nil || strings.TrimSpace(in.SessionID) == "" {
		if len(req.ID) > 0 {
			s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "sessionId is required"})
		}
		return
	}
	s.mu.Lock()
	rt := s.sessions[in.SessionID]
	s.mu.Unlock()
	if rt == nil {
		if len(req.ID) > 0 {
			s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "unknown session"})
		}
		return
	}
	if rt.execution != nil && rt.execution.Cancel() {
		// ExecutionRuntime also aborts the core agent.
	} else {
		rt.cancelMu.Lock()
		if rt.cancel != nil {
			rt.cancel()
		}
		rt.cancelMu.Unlock()
	}
	if len(req.ID) > 0 {
		s.writeResponse(req.ID, map[string]any{}, nil)
	}
}

func (s *server) handleCancelRequest(req rpcRequest) {
	var in cancelRequestNotification
	if err := json.Unmarshal(req.Params, &in); err != nil || len(in.RequestID) == 0 {
		return
	}
	key := mcp.RawIDKey(in.RequestID)
	s.mu.Lock()
	pending := s.pending[key]
	if pending != nil {
		delete(s.pending, key)
	}
	var execution *agentruntime.ExecutionRuntime
	var cancel context.CancelFunc
	for _, rt := range s.sessions {
		rt.cancelMu.Lock()
		if rt.promptID == key {
			cancel = rt.cancel
			execution = rt.execution
		}
		rt.cancelMu.Unlock()
		if cancel != nil || execution != nil {
			break
		}
	}
	s.mu.Unlock()
	if pending != nil {
		pending <- json.RawMessage(`{"outcome":{"outcome":"cancelled"}}`)
	}
	if execution != nil && execution.Cancel() {
		// already cancelled through shared execution state
	} else if cancel != nil {
		cancel()
	}
}

func (s *server) handleCloseSession(req rpcRequest) {
	var in closeSessionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil || strings.TrimSpace(in.SessionID) == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}

	if _, err := s.closeSessionRuntime(in.SessionID); err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	s.writeResponse(req.ID, map[string]any{}, nil)
}

func (s *server) closeSessionRuntime(sessionID string) (*sessionRuntime, error) {
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt == nil {
		return nil, nil
	}
	if err := s.shutdownSessionRuntime(rt); err != nil {
		return rt, err
	}
	s.mu.Lock()
	if s.sessions[sessionID] == rt {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	return rt, nil
}

func (s *server) shutdownSessionRuntime(rt *sessionRuntime) error {
	if rt == nil {
		return nil
	}
	rt.cancelMu.Lock()
	rt.closed = true
	cancel := rt.cancel
	rt.cancelMu.Unlock()
	s.clearSessionDecisionsForRuntime(rt)
	if rt.runtime != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := rt.runtime.Shutdown(shutdownCtx)
		shutdownCancel()
		if err == nil {
			rt.closeResources()
		}
		return err
	}
	if rt.execution != nil && rt.execution.Cancel() {
		rt.closeResources()
		return nil
	}
	if cancel != nil {
		cancel()
	}
	rt.closeResources()
	return nil
}

// shutdownAllSessionRuntimes is the process-boundary cleanup path for stdin
// EOF and startup/runtime errors. Explicit session/close uses the same bounded
// shutdown function and removes the runtime only after cleanup succeeds.
func (s *server) shutdownAllSessionRuntimes() {
	if s == nil {
		return
	}
	s.mu.Lock()
	runtimes := make([]*sessionRuntime, 0, len(s.sessions))
	for _, rt := range s.sessions {
		runtimes = append(runtimes, rt)
	}
	s.mu.Unlock()
	for _, rt := range runtimes {
		if err := s.shutdownSessionRuntime(rt); err != nil {
			provider.DebugLogf("ACP session %q shutdown: %v", rt.id, err)
		}
		s.mu.Lock()
		if s.sessions[rt.id] == rt {
			delete(s.sessions, rt.id)
		}
		s.mu.Unlock()
	}
}

func (s *server) handleDeleteSession(req rpcRequest) {
	var in deleteSessionRequest
	if err := json.Unmarshal(req.Params, &in); err != nil || strings.TrimSpace(in.SessionID) == "" {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	s.mu.Lock()
	active := s.sessions[in.SessionID]
	s.mu.Unlock()
	if active != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32000, Message: "cannot delete an active session"})
		return
	}
	err := agentruntime.DeleteSession(s.settings.GetSessionDir(), in.SessionID)
	if err != nil && !strings.Contains(err.Error(), "not found") {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	s.writeResponse(req.ID, map[string]any{}, nil)
}

const sessionListPageSize = 50

func encodeSessionCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("acp-v1:%d", offset)))
}

func decodeSessionCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || !strings.HasPrefix(string(raw), "acp-v1:") {
		return 0, fmt.Errorf("invalid session cursor")
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(string(raw), "acp-v1:"))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid session cursor")
	}
	return offset, nil
}

func (s *server) handleListSessions(req rpcRequest) {
	var in listSessionsRequest
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &in) != nil {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid params"})
		return
	}
	if in.Cwd != "" && !filepath.IsAbs(in.Cwd) {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "cwd must be an absolute path"})
		return
	}

	offset := 0
	if in.Cursor != "" {
		var err error
		offset, err = decodeSessionCursor(in.Cursor)
		if err != nil {
			s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid cursor"})
			return
		}
	}

	var (
		details []session.SessionDetail
		err     error
	)
	if in.Cwd == "" {
		details, err = session.ListAllDetailed(s.settings.GetSessionDir())
	} else {
		details, err = session.ListForDirDetailed(in.Cwd, s.settings.GetSessionDir())
	}
	if err != nil {
		s.writeResponse(req.ID, nil, acpFailureRPCError(err, nil, agentruntime.PhasePersistence))
		return
	}
	if offset > len(details) {
		s.writeResponse(req.ID, nil, &mcp.RPCError{Code: -32602, Message: "invalid cursor"})
		return
	}

	end := offset + sessionListPageSize
	if end > len(details) {
		end = len(details)
	}
	result := listSessionsResult{Sessions: make([]listedSession, 0, end-offset)}
	for _, detail := range details[offset:end] {
		title := detail.Name
		if title == "" {
			title = detail.Preview
		}
		result.Sessions = append(result.Sessions, listedSession{
			SessionID: detail.ID,
			Cwd:       detail.Cwd,
			AdditionalDirectories: func() []string {
				directories, err := session.LatestAdditionalDirectoriesByID(s.settings.GetSessionDir(), detail.ID)
				if err != nil {
					return []string{}
				}
				return directories
			}(),
			Title:     title,
			UpdatedAt: detail.ModTime.UTC().Format(time.RFC3339),
			Meta:      map[string]any{"messageCount": detail.MessageCount},
		})
	}
	if end < len(details) {
		result.NextCursor = encodeSessionCursor(end)
	}
	s.writeResponse(req.ID, result, nil)
}

func (s *server) sessionForPrompt(sessionID string) *sessionRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessionID]
}

func (s *server) handleAgentEvent(sessionID string, ev agentpkg.Event) {
	switch ev.Type {
	case agentpkg.EventHostedItem:
		if ev.HostedItem != nil {
			status := ev.HostedItem.Status
			if status == "" {
				status = "updated"
			}
			title := ev.HostedItem.Type
			if title == "" {
				title = "hosted item"
			}
			s.notify(sessionID, sessionUpdate{
				SessionUpdate: "tool_call_update",
				ToolCallID:    ev.HostedItem.ID,
				Title:         fmt.Sprintf("%s: %s", title, status),
				Kind:          "other",
				Status:        acpHostedStatus(status),
			})
		}
	case agentpkg.EventTextDelta:
		messageID := s.streamMessageID(sessionID, false, ev.TextDelta)
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "agent_message_chunk",
			MessageID:     messageID,
			Content:       &contentBlock{Type: "text", Text: ev.TextDelta},
		})
	case agentpkg.EventThinkDelta:
		messageID := s.streamMessageID(sessionID, true, ev.ThinkDelta)
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "agent_thought_chunk",
			MessageID:     messageID,
			Content:       &contentBlock{Type: "text", Text: ev.ThinkDelta},
		})
	case agentpkg.EventToolCall:
		if ev.ToolCall != nil {
			title := s.rememberToolTitle(ev.ToolCall.ID, ev.ToolCall.Name, ev.ToolArgs)
			s.notify(sessionID, sessionUpdate{
				SessionUpdate: "tool_call",
				ToolCallID:    ev.ToolCall.ID,
				Title:         title,
				Kind:          acpToolKind(ev.ToolCall.Name),
				Status:        "pending",
				RawInput:      toolRawInput(ev.ToolArgs),
			})
		}
	case agentpkg.EventToolExecutionStart:
		title := s.rememberToolTitle(ev.ToolCallID, ev.ToolName, ev.ToolArgs)
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    ev.ToolCallID,
			Title:         title,
			Kind:          acpToolKind(ev.ToolName),
			Status:        "in_progress",
			RawInput:      toolRawInput(ev.ToolArgs),
		})
	case agentpkg.EventToolExecutionEnd:
		status := "completed"
		if ev.ToolError != nil {
			status = "failed"
		}
		toolContent := ev.ToolResult
		rawOutput := map[string]any{"content": toolContent}
		if ev.ToolError != nil {
			info := acpFailureInfo(ev.ToolError, nil, agentruntime.PhaseTool)
			toolContent = agentruntime.DisplayErrorMessage(info)
			rawOutput["content"] = toolContent
			rawOutput["errorInfo"] = info
		}
		if ev.ToolDiff != nil {
			rawOutput["diff"] = ev.ToolDiff
		}
		var toolContents []toolCallContent
		if text := strings.TrimSpace(toolContent); text != "" {
			toolContents = append(toolContents, toolCallContent{Type: "content", Content: &contentBlock{Type: "text", Text: text}})
		}
		if ev.ToolDiff != nil {
			toolContents = append(toolContents, toolCallContent{Type: "diff", Path: ev.ToolDiff.Path, OldText: ev.ToolDiff.OldText, NewText: ev.ToolDiff.NewText})
		}
		locations := toolCallLocations(ev.ToolDiff)
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    ev.ToolCallID,
			Title:         s.toolTitleFor(ev.ToolCallID, ev.ToolName),
			Kind:          acpToolKind(ev.ToolName),
			Status:        status,
			Content:       toolContents,
			Locations:     locations,
			RawOutput:     rawOutput,
		})
	case agentpkg.EventToolExecutionUpdate:
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    ev.ToolCallID,
			Content:       textToolContent(fmt.Sprint(ev.PartialResult)),
		})
	case agentpkg.EventToolResult:
	case agentpkg.EventPlanUpdate:
		if ev.Plan != nil {
			s.notify(sessionID, sessionUpdate{
				SessionUpdate: "plan",
				Entries:       acpPlanEntries(ev.Plan),
				Meta:          acpPlanMeta(ev.Plan),
			})
		}
	case agentpkg.EventUsage:
		s.emitUsageUpdate(sessionID, ev, true)
	case agentpkg.EventDone:
		s.emitUsageUpdate(sessionID, ev, false)
	case agentpkg.EventError, agentpkg.EventRunFinished:
		// Terminal errors are projected as the same structured Runtime
		// contract used by the prompt response and durable replay. Keep this
		// notification adapter-neutral; ACP clients may ignore the extension
		// while newer clients can render retry and safety actions without
		// parsing provider text.
		if ev.AgentID != "" {
			return
		}
		if !s.markTerminalNotified(sessionID) {
			return
		}
		status := "failed"
		var info *agentruntime.ErrorInfo
		if ev.Type == agentpkg.EventRunFinished {
			switch ev.Status {
			case agentpkg.TaskSuccess:
				status = "completed"
			case agentpkg.TaskCanceled:
				status = "cancelled"
			case agentpkg.TaskIncomplete:
				status = "incomplete"
			}
		}
		if status != "completed" {
			classified := acpFailureInfo(ev.Error, nil, agentruntime.PhaseModel)
			if ev.Status == agentpkg.TaskCanceled {
				classified = agentruntime.ClassifyError(context.Canceled, agentruntime.ErrorClassificationOptions{Phase: agentruntime.PhaseModel})
			} else if ev.Status == agentpkg.TaskIncomplete {
				classified.Code = "run_incomplete"
				classified.Type = "incomplete_error"
				classified.FailureClass = agentruntime.FailureIncomplete
				classified.Phase = agentruntime.PhaseModel
				classified.MessageKey = "run.error.incomplete"
				classified.Message = "The run ended before it could complete."
				classified.RetryMode = agentruntime.RetryUser
				classified.Retryable = true
			}
			info = &classified
		}
		params := map[string]any{"sessionId": sessionID, "event": "terminal", "status": status}
		if info != nil {
			params["errorInfo"] = *info
			params["error"] = agentruntime.DisplayErrorMessage(*info)
		}
		s.notifyExtension("_mothx/session_event", params)
	case agentpkg.EventRetry:
		s.notifyExtension("_mothx/session_event", acpRetryEvent(sessionID, ev))
	case agentpkg.EventStatus:
		if ev.RetryStatus {
			return
		}
		s.notifyExtension("_mothx/session_event", map[string]any{
			"sessionId": sessionID,
			"event":     "status",
			"message":   ev.StatusMessage,
		})
	case agentpkg.EventCompactionStart, agentpkg.EventCompactionEnd, agentpkg.EventTurnStart, agentpkg.EventTurnEnd:
		s.notifyExtension("_mothx/session_event", map[string]any{
			"sessionId": sessionID,
			"event":     acpEventName(ev.Type),
			"message":   ev.StatusMessage,
		})
	}
}

func toolCallLocations(diff *agentpkg.FileDiff) []toolCallLocation {
	if diff == nil || !filepath.IsAbs(diff.Path) {
		return nil
	}
	return []toolCallLocation{{Path: diff.Path}}
}

// streamMessageID returns the active prompt's stable ACP message ID. Fixture
// sessions that emit events without a prompt still receive a deterministic ID
// so their wire payload remains schema-valid.
func (s *server) streamMessageID(sessionID string, thought bool, _ string) string {
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt != nil {
		rt.cancelMu.Lock()
		defer rt.cancelMu.Unlock()
		if thought && rt.thoughtMessageID != "" {
			return rt.thoughtMessageID
		}
		if !thought && rt.messageID != "" {
			return rt.messageID
		}
	}
	kind := "message"
	if thought {
		kind = "thought"
	}
	digest := sha256.Sum256([]byte(sessionID + "\x00" + kind))
	return fmt.Sprintf("acp_%s_%x", kind, digest[:8])
}

func (s *server) markTerminalNotified(sessionID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rt := s.sessions[sessionID]
	if rt == nil || rt.terminalNotified {
		return false
	}
	rt.terminalNotified = true
	return true
}

func acpRetryEvent(sessionID string, ev agentpkg.Event) map[string]any {
	return map[string]any{
		"sessionId":    sessionID,
		"event":        "retrying",
		"message":      acpRetryMessage(ev),
		"attempt":      ev.RetryAttempt,
		"maxAttempts":  ev.RetryMaxAttempts,
		"retryAfterMs": ev.RetryAfterMS,
	}
}

// acpRetryMessage deliberately uses the structured retry fields instead of
// RetryReason, which may contain provider-specific or otherwise unsafe detail.
func acpRetryMessage(ev agentpkg.Event) string {
	if ev.RetryAttempt > 0 && ev.RetryMaxAttempts > 0 {
		message := fmt.Sprintf("Retrying (attempt %d/%d)", ev.RetryAttempt, ev.RetryMaxAttempts)
		if ev.RetryAfterMS > 0 {
			message += fmt.Sprintf("; waiting %s", time.Duration(ev.RetryAfterMS)*time.Millisecond)
		}
		return message + "..."
	}
	return "Retrying..."
}

func acpHostedStatus(status string) string {
	switch status {
	case "completed", "incomplete", "expired", "failed", "cancelled", "canceled":
		return status
	default:
		return "in_progress"
	}
}

func acpEventName(eventType agentpkg.EventType) string {
	switch eventType {
	case agentpkg.EventCompactionStart:
		return "compaction_started"
	case agentpkg.EventCompactionEnd:
		return "compaction_finished"
	case agentpkg.EventTurnStart:
		return "turn_started"
	case agentpkg.EventTurnEnd:
		return "turn_finished"
	default:
		return "unknown"
	}
}

func acpToolKind(name string) string {
	switch name {
	case "read", "ls":
		return "read"
	case "write", "edit":
		return "edit"
	case "grep", "find":
		return "search"
	case "bash":
		return "execute"
	case "plan":
		return "think"
	default:
		return "other"
	}
}

func textToolContent(text string) []toolCallContent {
	if text == "" {
		return nil
	}
	return []toolCallContent{{Type: "content", Content: &contentBlock{Type: "text", Text: text}}}
}

func acpPlanEntries(plan *agentpkg.TaskPlan) []planEntry {
	entries := make([]planEntry, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		status := "pending"
		switch step.Status {
		case "running":
			status = "in_progress"
		case "done", "failed":
			status = "completed"
		}
		entries = append(entries, planEntry{Content: step.Title, Priority: "medium", Status: status})
	}
	return entries
}

func acpPlanMeta(plan *agentpkg.TaskPlan) map[string]any {
	if plan.Title == "" && plan.Note == "" {
		return nil
	}
	return map[string]any{mothxExtensionNamespace: map[string]string{"title": plan.Title, "note": plan.Note}}
}

func (s *server) emitUsageUpdate(sessionID string, ev agentpkg.Event, addCost bool) {
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt == nil {
		return
	}

	rt.cancelMu.Lock()
	model := rt.activeModel
	rt.cancelMu.Unlock()
	if model == nil {
		model = runtimeModel(rt.runtime)
	}
	if model == nil {
		model = s.m
	}
	used, size := usageContext(ev.ContextUsage, ev.Usage, model)
	rt.usageMu.Lock()
	if addCost && ev.Usage != nil {
		if model != nil {
			ev.Usage.CalculateCost(model.Cost.Input, model.Cost.Output, model.Cost.CacheRead, model.Cost.CacheWrite)
		}
		rt.cost += ev.Usage.Cost.Total
	}
	cost := rt.cost
	rt.usageMu.Unlock()

	update := sessionUpdate{
		SessionUpdate: "usage_update",
		Used:          &used,
		Size:          &size,
	}
	if cost > 0 {
		update.Cost = &usageCost{Amount: cost, Currency: "USD"}
	}
	s.notify(sessionID, update)
}

func runtimeModel(runtime *agentruntime.SessionRuntime) *provider.Model {
	if runtime == nil {
		return nil
	}
	_, _, model, _, _ := runtime.ConfigSnapshot()
	return model
}

func usageContext(contextUsage *agentpkg.ContextUsage, usage *agentpkg.Usage, model *provider.Model) (int, int) {
	used := 0
	size := 0
	if contextUsage != nil {
		used = contextUsage.TotalTokens
		if used == 0 {
			used = contextUsage.Tokens
		}
		size = contextUsage.ContextWindow
	}
	if used == 0 && usage != nil {
		used = usage.TotalTokens
		if used == 0 {
			used = usage.InputTokens + usage.CacheRead + usage.CacheWrite + usage.OutputTokens
		}
	}
	if size == 0 && model != nil {
		size = model.ContextWindow
	}
	return used, size
}

func (s *server) persistedSessionCost(mgr *session.Manager, model *provider.Model) float64 {
	if mgr == nil {
		return 0
	}
	var total float64
	for _, msg := range mgr.GetMessages() {
		if msg.Usage == nil {
			continue
		}
		if msg.Usage.Cost.Total == 0 && model != nil {
			msg.Usage.CalculateCost(model)
		}
		total += msg.Usage.Cost.Total
	}
	return total
}

func formatACPPlan(plan *agentpkg.TaskPlan) string {
	if plan == nil || len(plan.Steps) == 0 {
		return "Plan updated."
	}
	var b strings.Builder
	title := plan.Title
	if title == "" {
		title = "Plan"
	}
	b.WriteString(title)
	for _, step := range plan.Steps {
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("%s %s", planStatusMarker(step.Status), step.Title))
	}
	if plan.Note != "" {
		b.WriteString("\nnote: " + plan.Note)
	}
	return b.String()
}

func planStatusMarker(status string) string {
	switch status {
	case "running":
		return ">"
	case "done":
		return "x"
	case "failed":
		return "!"
	default:
		return "-"
	}
}

func (s *server) buildMCPCallbacks(sessionID string) mcp.Callbacks {
	return mcp.Callbacks{
		OnNotification: func(serverName, method string, params json.RawMessage) {
			s.handleMCPNotification(sessionID, serverName, method, params)
		},
		OnSamplingCreateMessage: func(ctx context.Context, serverName string, params json.RawMessage) (json.RawMessage, *mcp.RPCError) {
			return s.handleMCPSamplingCreateMessage(ctx, sessionID, serverName, params)
		},
	}
}

func (s *server) handleMCPNotification(sessionID, serverName, method string, params json.RawMessage) {
	callID := "mcp-notify-" + mcp.SanitizeToolName(serverName)
	title := "mcp_notification: " + serverName
	s.mu.Lock()
	if !s.mcpNotify[callID] {
		s.mcpNotify[callID] = true
		s.mu.Unlock()
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call",
			ToolCallID:    callID,
			Title:         title,
			Kind:          "other",
			Status:        "pending",
		})
	} else {
		s.mu.Unlock()
	}

	rawOut := map[string]any{
		"method": method,
	}
	if parsed := parseJSONRawToMap(params); parsed != nil {
		rawOut["params"] = parsed
	} else if trimmed := strings.TrimSpace(string(params)); trimmed != "" && trimmed != "null" {
		rawOut["paramsText"] = trimmed
	}

	switch method {
	case "notifications/progress", "notifications/message", "logging/message", "notifications/cancelled":
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    callID,
			Title:         title,
			Status:        "in_progress",
			RawOutput:     rawOut,
		})
	}
}

func (s *server) handleMCPSamplingCreateMessage(ctx context.Context, sessionID, serverName string, params json.RawMessage) (json.RawMessage, *mcp.RPCError) {
	rt := s.sessionRuntime(sessionID)
	if rt == nil || rt.runtime == nil {
		return nil, &mcp.RPCError{Code: -32000, Message: "unknown session"}
	}
	p, _, model, _, thinking := rt.runtime.ConfigSnapshot()
	if p == nil || model == nil {
		return nil, &mcp.RPCError{Code: -32000, Message: "session model is unavailable"}
	}
	prompt, systemPrompt, maxTokens := extractSamplingInput(params)
	if strings.TrimSpace(prompt) == "" {
		return nil, &mcp.RPCError{Code: -32602, Message: "sampling/createMessage requires non-empty messages"}
	}
	if maxTokens <= 0 {
		maxTokens = agent.ResolveMaxTokens(model)
	}
	modelID := model.ID
	chatCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	events := p.Chat(chatCtx, provider.ChatParams{
		Messages:      []provider.Message{provider.NewUserMessage(prompt)},
		SystemPrompt:  systemPrompt,
		ThinkingLevel: thinking,
		MaxTokens:     maxTokens,
		Temperature:   config.NormalizeSamplingPtr(model.Temperature),
		TopP:          config.NormalizeSamplingPtr(model.TopP),
		ModelID:       modelID,
	})
	var outText strings.Builder
	for ev := range events {
		switch ev.Type {
		case provider.StreamTextDelta:
			outText.WriteString(ev.TextDelta)
		case provider.StreamDone:
			// noop
		case provider.StreamError:
			if ev.Error != nil {
				log.Printf("[acp] MCP sampling provider error for %s: %v", serverName, ev.Error)
				return nil, acpFailureRPCError(ev.Error, nil, agentruntime.PhaseModel)
			}
		}
	}
	text := strings.TrimSpace(outText.String())
	if text == "" {
		text = "(empty response)"
	}
	result := map[string]any{
		"model": modelID,
		"role":  "assistant",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, acpFailureRPCError(err, nil, agentruntime.PhaseTransport)
	}
	s.notify(sessionID, sessionUpdate{
		SessionUpdate: "agent_message_chunk",
		Content:       &contentBlock{Type: "text", Text: "MCP[" + serverName + "] sampling/createMessage completed"},
	})
	return data, nil
}

// acpFailureInfo is a presentation adapter for the shared Runtime contract.
// It accepts a Runtime observation when one exists, so tool/output safety
// facts are preserved instead of being inferred again by ACP.
func acpFailureInfo(err error, observed *agentruntime.ErrorInfo, phase agentruntime.RunPhase) agentruntime.ErrorInfo {
	if observed != nil && strings.TrimSpace(agentruntime.DisplayErrorMessage(*observed)) != "" {
		return *observed
	}
	return agentruntime.ClassifyError(err, agentruntime.ErrorClassificationOptions{Phase: phase})
}

func acpFailureRPCError(err error, observed *agentruntime.ErrorInfo, phase agentruntime.RunPhase) *mcp.RPCError {
	info := acpFailureInfo(err, observed, phase)
	message := strings.TrimSpace(agentruntime.DisplayErrorMessage(info))
	if message == "" {
		message = "The run could not be completed."
	}
	// MCP/JSON-RPC clients receive the complete safe contract in Data. The
	// legacy code/message fields remain for clients that do not understand the
	// extension, while Detail carries the bounded provider diagnostic.
	return &mcp.RPCError{Code: -32000, Message: message, Data: map[string]any{
		"code":            info.Code,
		"type":            info.Type,
		"failureClass":    info.FailureClass,
		"phase":           info.Phase,
		"messageKey":      info.MessageKey,
		"detail":          info.Detail,
		"retryMode":       info.RetryMode,
		"retryable":       info.Retryable,
		"retryAfterMs":    info.RetryAfterMS,
		"attempt":         info.Attempt,
		"maxAttempts":     info.MaxAttempts,
		"sideEffectState": info.SideEffectState,
		"partialOutput":   info.PartialOutput,
		"runId":           info.RunID,
		"intentId":        info.IntentID,
		"requestId":       info.RequestID,
	}}
}

func extractSamplingPrompt(params json.RawMessage) string {
	prompt, _, _ := extractSamplingInput(params)
	return prompt
}

func extractSamplingInput(params json.RawMessage) (prompt string, systemPrompt string, maxTokens int) {
	maxTokens = 0
	if len(params) == 0 {
		return "", "", maxTokens
	}
	var raw map[string]any
	if err := json.Unmarshal(params, &raw); err != nil {
		return strings.TrimSpace(string(params)), "", maxTokens
	}
	if v, ok := raw["maxTokens"].(float64); ok && int(v) > 0 {
		maxTokens = int(v)
	}
	msgs, _ := raw["messages"].([]any)
	var parts []string
	for _, m := range msgs {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content := msgMap["content"]
		role, _ := msgMap["role"].(string)
		switch v := content.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				if role == "system" {
					if systemPrompt == "" {
						systemPrompt = v
					}
					continue
				}
				parts = append(parts, v)
			}
		case []any:
			var blockTexts []string
			for _, item := range v {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := block["type"].(string); t == "text" {
					if txt, _ := block["text"].(string); strings.TrimSpace(txt) != "" {
						blockTexts = append(blockTexts, txt)
					}
				}
			}
			if len(blockTexts) == 0 {
				continue
			}
			joined := strings.Join(blockTexts, "\n")
			if role == "system" {
				if systemPrompt == "" {
					systemPrompt = joined
				}
				continue
			}
			parts = append(parts, joined)
		}
	}
	return strings.Join(parts, "\n"), systemPrompt, maxTokens
}

func parseJSONRawToMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// handleQuestion routes MothX's interactive question tool through its own ACP
// extension instead of overloading the standard permission request.
func (s *server) handleQuestion(ctx context.Context, rt *sessionRuntime, runID string, ev agentpkg.Event) {
	qh, ok := rt.agent.(agentpkg.QuestionHandler)
	if !ok {
		return
	}
	if rt.execution == nil || rt.execution.WaitForQuestion(runID) != nil {
		qh.HandleQuestionResponse(ev.QuestionID, "")
		return
	}
	defer func() { _ = rt.execution.Resume(runID) }()
	answer := s.requestQuestion(ctx, rt.id, ev.QuestionText, ev.QuestionOptions, ev.QuestionContext)
	qh.HandleQuestionResponse(ev.QuestionID, answer)
}

func (s *server) sessionRunID(sessionID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt == nil {
		return ""
	}
	rt.cancelMu.Lock()
	defer rt.cancelMu.Unlock()
	if rt.runID != "" {
		return rt.runID
	}
	// Keep fixtures and legacy callers that only set promptID functional while
	// they migrate to the canonical durable run identity.
	return rt.promptID
}

func (s *server) loadPersistedDecisionRecords(sessionID string) ([]agentruntime.DecisionRecord, error) {
	if s == nil || s.settings == nil || sessionID == "" {
		return nil, nil
	}
	events, err := session.ListSessionRunEvents(s.settings.GetSessionDir(), sessionID)
	if err != nil {
		return nil, err
	}
	records := make([]agentruntime.DecisionRecord, 0)
	for _, ev := range events {
		if !strings.HasPrefix(ev.EventType, "decision_") {
			continue
		}
		var envelope struct {
			Decision agentruntime.DecisionRecord `json:"decision"`
		}
		if json.Unmarshal(ev.Data, &envelope) == nil && envelope.Decision.ID != "" {
			if envelope.Decision.SessionID == "" {
				envelope.Decision.SessionID = sessionID
			}
			if envelope.Decision.RunID == "" {
				envelope.Decision.RunID = ev.RunID
			}
			records = append(records, envelope.Decision)
		}
	}
	return records, nil
}

func (s *server) replayPendingDecisionRequests(sessionID string) error {
	records, err := s.loadPersistedDecisionRecords(sessionID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt == nil || rt.decisions == nil {
		return nil
	}
	rehydrated := make(map[string]struct{})
	for _, request := range rt.decisions.Pending() {
		rehydrated[request.ID] = struct{}{}
	}
	pending := agentruntime.ReplayDecisions(records)
	for id, record := range pending {
		if _, ok := rehydrated[id]; !ok {
			continue
		}
		if record.Payload == nil || len(record.Payload) == 0 {
			continue
		}
		s.mu.Lock()
		if _, exists := s.pending[id]; !exists {
			s.pending[id] = make(chan json.RawMessage, 1)
		}
		s.mu.Unlock()
		switch record.Kind {
		case agentruntime.DecisionQuestion:
			var request questionRequest
			if err := json.Unmarshal(record.Payload, &request); err != nil {
				continue
			}
			method := "_mothx/request_question"
			var payload any = request
			if request.Protocol == acpElicitationFormProtocol && s.supportsElicitationForm() {
				method = "elicitation/create"
				payload = elicitationRequestForQuestion(request)
			}
			if err := s.notifyRequest(id, method, payload); err != nil {
				return err
			}
		case agentruntime.DecisionApproval:
			var request requestPermissionRequest
			if err := json.Unmarshal(record.Payload, &request); err != nil {
				continue
			}
			if err := s.notifyRequest(id, "session/request_permission", request); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *server) rehydrateSessionDecisions(rt *sessionRuntime) error {
	if s == nil || rt == nil || rt.decisions == nil || s.settings == nil {
		return nil
	}
	records, err := s.loadPersistedDecisionRecords(rt.id)
	if err != nil {
		return fmt.Errorf("load persisted decisions for session %s: %w", rt.id, err)
	}
	now := time.Now()
	for _, record := range agentruntime.ExpiredDecisions(records, now) {
		if err := s.persistDecisionRecord(rt.id, record.RunID, record.ID, record.Kind, "timed_out", "", map[string]any{"reason": "decision expired while session was offline"}); err != nil {
			return fmt.Errorf("terminalize expired decision %s: %w", record.ID, err)
		}
	}
	pending := agentruntime.ReplayDecisionsAt(records, now)
	activeRunID, active := rt.execution.Active()
	if !active {
		for _, record := range pending {
			if err := s.persistDecisionRecord(rt.id, record.RunID, record.ID, record.Kind, "cancelled", "", map[string]any{"reason": "decision execution was not recoverable after session restore"}); err != nil {
				return fmt.Errorf("terminalize unrecoverable decision %s: %w", record.ID, err)
			}
		}
		return nil
	}
	activeRecords := make([]agentruntime.DecisionRecord, 0, len(pending))
	for _, record := range pending {
		if record.RunID == activeRunID {
			activeRecords = append(activeRecords, record)
		}
	}
	_, err = rt.decisions.Rehydrate(activeRecords)
	return err
}

func (s *server) registerDecision(sessionID, id string, kind agentruntime.DecisionKind) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt == nil {
		return
	}
	if rt.decisions == nil {
		rt.decisions = &agentruntime.DecisionService{}
	}
	runID := s.sessionRunID(sessionID)
	_ = rt.decisions.Register(agentruntime.DecisionRequest{ID: id, RunID: runID, SessionID: sessionID, Kind: kind})
}

func (s *server) resolveDecision(sessionID, id string, kind agentruntime.DecisionKind, value, status string) error {
	if s == nil || id == "" {
		return nil
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt != nil && rt.decisions != nil {
		runID := s.sessionRunID(sessionID)
		_, err := rt.decisions.ResolveWith(agentruntime.DecisionResolution{ID: id, Kind: kind, Status: status, Value: value}, func(_ agentruntime.DecisionRequest) error {
			return s.persistDecisionRecord(sessionID, runID, id, kind, status, value, map[string]any{"value": value})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *server) clearSessionDecisions(sessionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	s.clearSessionDecisionsForRuntime(rt)
}

func (s *server) clearSessionDecisionsForRuntime(rt *sessionRuntime) {
	if rt == nil || rt.decisions == nil {
		return
	}
	rt.cancelMu.Lock()
	runID := rt.runID
	if runID == "" {
		runID = rt.promptID
	}
	rt.cancelMu.Unlock()
	for _, request := range rt.decisions.ClearRunWithValue(runID, "") {
		s.persistDecisionRecord(rt.id, runID, request.ID, request.Kind, "cancelled", "", map[string]any{"reason": "ACP session closed before the decision was resolved"})
	}
}

func (s *server) persistDecisionRecord(sessionID, runID, id string, kind agentruntime.DecisionKind, status, value string, payload any) error {
	return s.persistDecisionRecordWithDeadline(sessionID, runID, id, kind, status, value, payload, time.Time{})
}

func (s *server) persistDecisionRecordWithDeadline(sessionID, runID, id string, kind agentruntime.DecisionKind, status, value string, payload any, expiresAt time.Time) error {
	if s == nil || s.settings == nil || sessionID == "" || runID == "" || id == "" {
		return nil
	}
	request := agentruntime.DecisionRequest{ID: id, SessionID: sessionID, RunID: runID, Kind: kind}
	resolution := agentruntime.DecisionResolution{ID: id, Kind: kind, Status: status, Value: value}
	record, err := agentruntime.NewDecisionResolutionRecord(request, resolution, payload)
	if status == "pending" {
		record, err = agentruntime.NewDecisionRequestRecordWithDeadline(request, payload, expiresAt)
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"decision": record, "payload": payload})
	if err != nil {
		return err
	}
	source := string(agentruntime.SourceACP)
	s.mu.Lock()
	rt := s.sessions[sessionID]
	s.mu.Unlock()
	if rt != nil && rt.runtime != nil {
		_, _, _, sessionMode, _ := rt.runtime.ConfigSnapshot()
		if sessionMode == "" {
			sessionMode = s.mode
		}
		if resolution, _, resolveErr := rt.runtime.ResolvePolicy(sessionMode, "", s.mode); resolveErr == nil && resolution.Source != agentruntime.SourceUnknown {
			source = string(resolution.Source)
		}
	}
	_, err = (agentruntime.SessionRunEventSink{SessionDir: s.settings.GetSessionDir()}).Record(agentruntime.RunEvent{
		SessionID: sessionID, RunID: runID, EventType: "decision_" + status,
		Source: source, Status: status, Timestamp: time.Now(), Data: data,
	})
	return err
}

const acpElicitationFormProtocol = "acp-elicitation-form"

func (s *server) supportsElicitationForm() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientCaps.Elicitation != nil && s.clientCaps.Elicitation.Form != nil
}

func elicitationRequestForQuestion(request questionRequest) elicitationCreateRequest {
	options := make([]any, 0, len(request.Options))
	for _, option := range request.Options {
		options = append(options, option)
	}
	answerSchema := map[string]any{
		"type":        "string",
		"title":       "Answer",
		"description": request.Explanation,
	}
	if len(options) > 0 {
		answerSchema["enum"] = options
	}
	return elicitationCreateRequest{
		SessionID: request.SessionID,
		Message:   request.Question,
		Mode:      "form",
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": answerSchema},
			"required":   []string{"answer"},
		},
	}
}

// requestQuestion sends a question through standard ACP elicitation when the
// client advertises form support, while preserving the legacy extension for
// older clients. Both paths use the same pending request and DecisionService.
func (s *server) requestQuestion(ctx context.Context, sessionID, question string, options []string, explanation string) string {
	id := s.nextRequestID()
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	s.registerDecision(sessionID, id, agentruntime.DecisionQuestion)
	deadline := time.Now().Add(5 * time.Minute)
	request := questionRequest{SessionID: sessionID, Question: question, Options: options, Explanation: explanation, TimeoutMs: int64((5 * time.Minute).Milliseconds())}
	method := "_mothx/request_question"
	var payload any = request
	if s.supportsElicitationForm() {
		request.Protocol = acpElicitationFormProtocol
		method = "elicitation/create"
		payload = elicitationRequestForQuestion(request)
	}
	if err := s.persistDecisionRecordWithDeadline(sessionID, s.sessionRunID(sessionID), id, agentruntime.DecisionQuestion, "pending", "", request, deadline); err != nil {
		s.deletePending(id)
		return ""
	}

	if err := s.notifyRequest(id, method, payload); err != nil {
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionQuestion, "", "cancelled")
		return ""
	}
	select {
	case <-ctx.Done():
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionQuestion, "", "cancelled")
		return ""
	case <-time.After(5 * time.Minute):
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionQuestion, "", "timed_out")
		return ""
	case resp := <-ch:
		answer, status := questionAnswer(resp, request.Protocol == acpElicitationFormProtocol)
		if err := s.resolveDecision(sessionID, id, agentruntime.DecisionQuestion, answer, status); err != nil {
			return ""
		}
		for _, option := range options {
			if answer == option {
				return option
			}
		}
		return ""
	}
}

func questionAnswer(raw json.RawMessage, standardElicitation bool) (string, string) {
	if standardElicitation {
		var out elicitationResult
		if json.Unmarshal(raw, &out) == nil && out.Action == "accept" {
			answer, _ := out.Content["answer"].(string)
			return answer, "resolved"
		}
		// A restored request can fall back to the legacy extension when a
		// reconnecting client no longer advertises form elicitation. Accept the
		// old result shape without creating a second decision state.
		var legacy questionResult
		if json.Unmarshal(raw, &legacy) == nil && legacy.Answer != "" {
			return legacy.Answer, "resolved"
		}
		return "", "cancelled"
	}
	var out questionResult
	if json.Unmarshal(raw, &out) != nil {
		return "", "cancelled"
	}
	return out.Answer, "resolved"
}

func (s *server) requestPermission(sessionID, toolCallID, toolName string, args map[string]any) bool {
	return s.requestPermissionContext(context.Background(), sessionID, toolCallID, toolName, args)
}

func (s *server) requestPermissionContext(ctx context.Context, sessionID, toolCallID, toolName string, args map[string]any) bool {
	id := s.nextRequestID()
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	s.registerDecision(sessionID, id, agentruntime.DecisionApproval)
	timeout := s.permissionTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if err := s.persistDecisionRecordWithDeadline(sessionID, s.sessionRunID(sessionID), id, agentruntime.DecisionApproval, "pending", "", requestPermissionRequest{SessionID: sessionID, ToolCall: permissionToolCall{ToolCallID: toolCallID, Title: toolName, Status: "pending", RawInput: toolRawInput(args)}}, time.Now().Add(timeout)); err != nil {
		s.deletePending(id)
		return false
	}
	if err := s.notifyRequest(id, "session/request_permission", requestPermissionRequest{
		SessionID: sessionID,
		ToolCall: permissionToolCall{
			ToolCallID: toolCallID,
			Title:      toolName,
			Kind:       acpToolKind(toolName),
			Status:     "pending",
			RawInput:   toolRawInput(args),
		},
		Options: []permissionOption{
			{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"},
			{OptionID: "reject-once", Name: "Reject", Kind: "reject_once"},
		},
	}); err != nil {
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionApproval, "", "cancelled")
		return false
	}
	select {
	case <-ctx.Done():
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionApproval, "", "cancelled")
		return false
	case <-time.After(timeout):
		s.deletePending(id)
		s.resolveDecision(sessionID, id, agentruntime.DecisionApproval, "", "timed_out")
		return false
	case resp := <-ch:
		var out permissionResult
		_ = json.Unmarshal(resp, &out)
		value := "deny"
		if out.Outcome != nil {
			value = out.Outcome.OptionID
		}
		if err := s.resolveDecision(sessionID, id, agentruntime.DecisionApproval, value, "resolved"); err != nil {
			return false
		}
		return out.Outcome != nil && out.Outcome.Outcome == "selected" && out.Outcome.OptionID == "allow-once"
	}
}

func (s *server) deletePending(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *server) deliverResponse(id json.RawMessage, result json.RawMessage, errMsg json.RawMessage) {
	key := mcp.RawIDKey(id)
	s.mu.Lock()
	ch, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.mu.Unlock()
	if ok {
		if len(errMsg) > 0 {
			ch <- errMsg
			return
		}
		ch <- result
	}
}

func (s *server) emitMessage(sessionID string, msg provider.Message) {
	if msg.Role == "assistant" {
		for _, c := range msg.Contents {
			if c.Type == "thinking" && c.Thinking != "" {
				s.notify(sessionID, sessionUpdate{SessionUpdate: "agent_thought_chunk", MessageID: replayMessageID(sessionID, "thought", c.Thinking), Content: &contentBlock{Type: "text", Text: c.Thinking}})
			} else if c.Type == "text" && c.Text != "" {
				s.notify(sessionID, sessionUpdate{SessionUpdate: "agent_message_chunk", MessageID: replayMessageID(sessionID, "message", c.Text), Content: &contentBlock{Type: "text", Text: c.Text}})
			} else if c.Type == "toolCall" && c.ToolCall != nil {
				var rawInput map[string]any
				_ = json.Unmarshal(c.ToolCall.Arguments, &rawInput)
				title := s.rememberToolTitle(c.ToolCall.ID, c.ToolCall.Name, rawInput)
				s.notify(sessionID, sessionUpdate{
					SessionUpdate: "tool_call",
					ToolCallID:    c.ToolCall.ID,
					Title:         title,
					Kind:          acpToolKind(c.ToolCall.Name),
					Status:        "pending",
					RawInput:      toolRawInput(rawInput),
				})
			}
		}
		return
	}
	if msg.Role == "user" {
		text := msg.Content
		if text == "" {
			for _, c := range msg.Contents {
				if c.Type == "text" && c.Text != "" {
					text = c.Text
					break
				}
			}
		}
		if text != "" {
			s.notify(sessionID, sessionUpdate{SessionUpdate: "user_message_chunk", MessageID: replayMessageID(sessionID, "user", text), Content: &contentBlock{Type: "text", Text: text}})
		}
		return
	}
	if msg.Role == "toolResult" {
		rawOutput := map[string]any{"content": msg.Content}
		status := "completed"
		if msg.IsError {
			status = "failed"
		}
		title := s.toolTitleFor(msg.ToolCallID, msg.ToolName)
		s.notify(sessionID, sessionUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    msg.ToolCallID,
			Title:         title,
			Kind:          acpToolKind(msg.ToolName),
			Status:        status,
			Content:       textToolContent(msg.Content),
			RawOutput:     rawOutput,
		})
	}
}

func replayMessageID(sessionID, kind, text string) string {
	digest := sha256.Sum256([]byte(sessionID + "\x00" + kind + "\x00" + text))
	return fmt.Sprintf("acp_replay_%s_%x", kind, digest[:8])
}

func promptToText(blocks []contentBlock) (string, error) {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "resource_link":
			if b.Name == "" || b.URI == "" {
				return "", fmt.Errorf("resource_link requires name and uri")
			}
			parts = append(parts, b.Name+": "+b.URI)
		case "image", "audio", "resource":
			return "", fmt.Errorf("unsupported prompt content type: %s", b.Type)
		default:
			return "", fmt.Errorf("unsupported prompt content type: %s", b.Type)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// promptToMessage preserves ACP's baseline text/resource_link types at the
// Agent Core boundary. Text remains available in Content for providers that do
// not consume rich blocks; resource links use the provider-neutral file URL
// representation instead of being discarded or rejected.
func promptToMessage(blocks []contentBlock) (provider.Message, error) {
	var textParts []string
	var contents []provider.ContentBlock
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
				contents = append(contents, provider.ContentBlock{Type: "text", Text: block.Text})
			}
		case "resource_link":
			if block.Name == "" || block.URI == "" {
				return provider.Message{}, fmt.Errorf("resource_link requires name and uri")
			}
			textParts = append(textParts, block.Name+": "+block.URI)
			contents = append(contents, provider.ContentBlock{Type: "file", File: &provider.FileContent{
				URL: block.URI, Filename: block.Name, MimeType: block.MimeType,
				Title: block.Title, Description: block.Description, Size: block.Size,
			}})
		case "image", "audio", "resource":
			return provider.Message{}, fmt.Errorf("unsupported prompt content type: %s", block.Type)
		default:
			return provider.Message{}, fmt.Errorf("unsupported prompt content type: %s", block.Type)
		}
	}
	message := provider.NewUserMessage(strings.Join(textParts, "\n"))
	if len(contents) > 0 {
		message.Contents = contents
	}
	return message, nil
}

func toolRawInput(args map[string]any) map[string]any {
	raw := map[string]any{"args": args}
	for key, value := range args {
		raw[key] = value
	}
	return raw
}

func (s *server) rememberToolTitle(toolCallID, name string, args map[string]any) string {
	title := toolTitle(name, args)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.toolTitles[toolCallID]; existing != "" && existing != name {
		return existing
	}
	s.toolTitles[toolCallID] = title
	return title
}

func (s *server) toolTitleFor(toolCallID, fallback string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if title := s.toolTitles[toolCallID]; title != "" {
		return title
	}
	return fallback
}

func toolTitle(name string, args map[string]any) string {
	if args == nil {
		return name
	}

	var details []string
	switch name {
	case "bash":
		details = appendStringArg(details, "command", args)
	case "read", "write", "edit", "ls":
		details = appendStringArg(details, "path", args)
	case "grep":
		details = appendStringArg(details, "pattern", args)
		details = appendStringArg(details, "path", args)
	case "find":
		details = appendStringArg(details, "pattern", args)
		details = appendStringArg(details, "path", args)
	default:
		for _, key := range []string{"command", "path", "pattern", "query", "name"} {
			details = appendStringArg(details, key, args)
			if len(details) > 0 {
				break
			}
		}
	}

	if len(details) == 0 {
		return name
	}
	return name + ": " + truncateTitle(strings.Join(details, " "))
}

func appendStringArg(details []string, key string, args map[string]any) []string {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return details
	}
	if key == "command" {
		return append(details, value)
	}
	return append(details, key+"="+value)
}

func truncateTitle(title string) string {
	const maxTitleLength = 160
	title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
	if len(title) <= maxTitleLength {
		return title
	}
	return title[:maxTitleLength-3] + "..."
}

func normalizeStopReason(reason string) string {
	switch reason {
	case "", "stop", "end_turn", "tool_use":
		return "end_turn"
	case "max_tokens", "length":
		return "max_tokens"
	case "max_turn_requests":
		return "max_turn_requests"
	case "cancelled", "aborted":
		return "cancelled"
	default:
		return "refusal"
	}
}

func (s *server) nextRequestID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return fmt.Sprintf("acp-%d", s.nextID)
}

func (s *server) readRequest() (rpcRequest, error) {
	var req rpcRequest
	var buf bytes.Buffer
	for {
		part, err := s.r.ReadSlice('\n')
		if len(part) > 0 {
			if buf.Len()+len(part) > maxRequestBytes {
				return req, fmt.Errorf("message exceeds maximum size of %d bytes", maxRequestBytes)
			}
			buf.Write(part)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return req, err
		}
		break
	}
	payload := strings.TrimRight(buf.String(), "\r\n")
	if strings.TrimSpace(payload) == "" {
		return req, errEmptyMessage
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return req, err
	}
	return req, nil
}

// validRPCID accepts the JSON-RPC scalar ID domain and notifications (empty
// raw ID). Objects and arrays are invalid request IDs and must not be echoed
// back in an error response.
func validRPCID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return true
	case json.Number:
		// JSON-RPC permits integer numeric IDs. Int64 rejects fractional and
		// exponent forms, avoiding ambiguous echoing after float conversion.
		_, err := typed.Int64()
		return err == nil
	default:
		return false
	}
}

func (s *server) writeResponse(id json.RawMessage, result any, errResp *mcp.RPCError) error {
	// A missing ID denotes a JSON-RPC notification. Notifications never receive
	// a response; an explicit JSON null remains a request ID and is preserved.
	if len(bytes.TrimSpace(id)) == 0 {
		return nil
	}
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if errResp != nil {
		resp["error"] = errResp
	} else {
		resp["result"] = result
	}
	return s.writeMessage(resp)
}

func (s *server) notify(sessionID string, update sessionUpdate) error {
	return s.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update":    update,
		},
	})
}

// notifySessionInfo projects the standard session metadata update after a
// persisted session mutation. ACP v1 keeps this update intentionally small;
// the authoritative cwd/additionalDirectories remain in SessionInfo and the
// session setup/list responses.
func (s *server) notifySessionInfo(sessionID string) error {
	return s.notify(sessionID, sessionUpdate{
		SessionUpdate: "session_info_update",
		UpdatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *server) notifyExtension(method string, params any) error {
	return s.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (s *server) notifyRequest(id string, method string, params any) error {
	return s.writeMessage(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
}

func (s *server) writeMessage(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.w.Write(data); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n")); err != nil {
		return err
	}
	if f, ok := s.w.(interface{ Flush() error }); ok {
		if err := f.Flush(); err != nil {
			return err
		}
	}
	return nil
}
