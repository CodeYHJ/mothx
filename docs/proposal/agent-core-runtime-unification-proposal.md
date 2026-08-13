# 统一 Agent 核心与多入口 Runtime 方案

> 状态：实施中（Phase 0–4 核心迁移已完成，Phase 5/TUI 与统一 Approval/Question/完整跨入口 ExecutionRuntime 待实施）
>
> 最近同步：2026-08-13
>
> 当前进度：已新增并接入 `internal/agentruntime`，完成 Source/Policy/Mode、SessionRuntime、Context/Skills/Registry/MCP、Agent 创建、Session 生命周期，以及 Channel/ACP 的基础 ExecutionRuntime 迁移。Channel/ACP 生产代码已通过 direct-construction 审计；WebUI/API 仍保留部分 adapter 级 run/approval 编排，TUI 尚未迁移。
>
> 目标：建立一个完整、前端中立的 Agent 核心运行时。TUI、WebUI、微信/飞书等 Channel、ACP 只负责自己的协议、交互和权限配置，不再分别实现 Agent 的装配、Session 恢复、工具/MCP/Skill 管理和运行生命周期。
>
> 关联代码：`internal/agent`、`internal/tools`、`internal/mcp`、`internal/skills`、`internal/session`、`internal/provider`、`internal/tui`、`internal/serve`、`internal/acp`

## 1. 摘要

MothX 当前已经拥有较完整的 Agent 核心：Agent loop、Provider 抽象、Tools Registry、MCP、Skills、SQLite Session、Sandbox、Sub-agent、Workflow 和多厂商兼容能力均已存在，并且多数入口最终复用 `internal/agent`。

但当前的复用主要停留在“共享底层能力”层面。TUI、WebUI/API、Channel、ACP 仍分别负责：

- 创建和恢复 Session；
- 组装 `agent.Config`；
- 创建 Tools Registry；
- 加载 MCP 和 Skills；
- 选择 mode、sandbox、工具和能力开关；
- 处理 approval；
- 管理 run、cancel、事件和生命周期。

因此当前架构不是四套独立 Agent loop，而是“四套 Agent Runtime 装配和执行编排”。这会造成行为漂移。已经出现的典型问题是：微信/飞书 Channel 侧使用 `yolo`，WebUI 恢复同一 Session 时 `APISession.Mode` 为空，展示层显示为 `yolo`，但 run submit 又回退到 API 默认 `agent`。

本方案不重写 Agent loop，而是在现有 `AgentFactory`、`AgentManager`、`RunExecutor` 和 Session 持久化能力之上，增加一个前端中立的 Runtime 层，逐步把入口层变成薄适配器。

## 1.1 与当前代码核对后的修正结论

本方案的总体判断成立，但实施前必须以以下事实为准：

1. **项目已经有 Channel binding/source 的持久化基础。** `internal/session` 已存在 `sessions.channel_type`、`sessions.channel_id`、`session.Header.ChannelType`、`session.Header.ChannelID`，并提供 `FindBindingBySessionID`、`ListBindings` 等 API。WebUI 也已经将 `ChannelType`、`ChannelID` 和 `Bound` 暴露给会话列表。因此 Phase 1 不应重新发明来源识别，而应把已有 binding 映射为统一 `RuntimeSource`。
2. **项目已经有部分 serve runtime 抽象。** `internal/serve/runtime` 当前包含 Responses background 的通用请求/driver 类型，但它还不是完整的 Agent Runtime；Agent 装配、Session resources、capabilities 和普通 run 生命周期仍主要位于 `internal/serve/openaiapi`。
3. **Channel 的 yolo 目前是默认值，不是强制不变量。** `ChannelSession` 创建时设置 `Mode: "yolo"`，普通 dispatcher 路径使用该值；但 Channel `/mode` 命令仍允许设置 `plan`/`agent`，`SubmitExternalResponsesBackground` 也按 `req.Mode → sess.Mode → s.cfg.DefaultMode` 解析。因此“微信/飞书永远 yolo”目前尚未实现，必须通过统一 Policy/ModeResolver 修复。
4. **WebUI 已经识别 Channel session，但执行层还没有使用该来源做 mode policy。** `APISession` 和会话列表包含 channel binding 信息，然而 `getOrCreateSession` 恢复后仍主要依赖 `APISession.Mode` 和 capability 记录；这正是 channel session 与 WebUI run mode 漂移的来源。
5. **Approval 不是完全没有持久化模型。** WebUI 已有 `pendingSessionApproval`、approval request/resolution、事件和 Session 持久化；当前缺少的是跨入口统一的可挂起 decision service，而不是从零建立 approval persistence。

因此本方案的第一阶段应优先做“已有 binding/source → 统一 policy → 所有执行路径”的接通，而不是先新增一套独立的来源存储。

## 1.2 实施进度（2026-08-13）

| 阶段 | 状态 | 已完成 | 仍未完成 |
|---|---|---|---|
| Phase 0 | ✅ 完成 | mode/source/capability/approval 关键不变量测试 | 跨入口完整 approval decision service 仍待抽取 |
| Phase 1 | ✅ 完成 | `RuntimeSource`、`ExecutionPolicy`、`ModeResolver`、Channel forced `yolo`，覆盖 WebUI/API/Channel recovery/background/approval | 完整 policy schema 仍可继续扩展到 sandbox/tools/MCP/approval |
| Phase 2 | ✅ 核心完成 | `internal/agentruntime`、SessionRuntime、Builder、Context/Skills/Rule、Registry/MCP policy、Agent builder、Session lifecycle | WebUI/API 的全部 run/approval 生命周期尚未完全下沉 |
| Phase 3 | ✅ 完成当前方案边界 | Channel Session/Registry/MCP/Skills/Context/Agent/Session lifecycle 和基础 ExecutionRuntime 已迁移，保留 Channel Security/Hooks/watchdog/ProgressFunc | Channel run persistence/event mapping 仍由 adapter 负责，属于预期边界 |
| Phase 4 | ✅ 完成当前方案边界 | ACP Session/Registry/MCP/Skills/Context/Agent/Session lifecycle 和基础 ExecutionRuntime 已迁移，保留 JSON-RPC/permission/question/callback/rollback | ACP 完整共享 Run/Approval service 仍待后续统一 |
| Phase 5 | ⏳ 未开始 | — | TUI Session/Registry/MCP/Skills/Agent/Run 迁移 |

当前 `internal/agentruntime` 主要能力：

- `RuntimeSource`、`ExecutionPolicy`、`ModeResolver`；
- `SessionRuntime`、`Builder`、`AttachSessionResources`；
- `LoadContextResources`、`RefreshResources`；
- `RegistryPolicy`、`RegistryMutator`、`BuildRegistry`；
- `MCPPolicy`、严格/可选 MCP 连接和 Runtime-owned cleanup；
- `CreateSession`、`OpenSession`、`OpenSessionForWorkDir`、`DeleteSession`；
- `NewAgentManager`、`SessionRuntime.BuildAgent`；
- `ExecutionRuntime` 的 Begin/Cancel/SetAgent/Finish。

当前明确保留在适配层的内容：

- Channel 的平台鉴权、Security、Hooks、ProgressFunc、watchdog、消息和 run event 映射；
- ACP 的 JSON-RPC framing、permission/question、MCP callback、protocol error 和 replay 输出；
- WebUI/API 的 HTTP/SSE/WebSocket、approval persistence、Responses background driver；
- TUI 的 Bubble Tea state、终端交互和显示映射。

目标结构如下：

```text
TUI adapter ───────────────┐
WebUI/API adapter ─────────┤
WeChat/Feishu adapter ─────┼── Agent Runtime ── AgentFactory ── Agent Core
ACP adapter ───────────────┘          │
                                      ├── Session
                                      ├── Tools Registry
                                      ├── MCP lifecycle
                                      ├── Skills/context
                                      ├── Provider/model
                                      ├── Sandbox
                                      ├── Approval/Question
                                      └── Run/events/cancel
```

职责边界：

### Agent Core

`internal/agent` 负责一次 Agent 执行本身：

- Provider 请求和流式事件；
- 工具调用与结果处理；
- Context、历史和 compaction；
- Agent mode 对工具执行的核心语义；
- 子 Agent、Delegate、Workflow；
- 事件生成、abort 和 terminal result。

Agent Core 不应知道 HTTP、WebSocket、Bubble Tea、微信、飞书或 ACP。

### Agent Runtime

新增 `internal/agentruntime`，作为与 `internal/agent`、`internal/session` 并列的前端中立 Runtime 包，负责跨入口共享的运行时能力。现有 `internal/serve/runtime` 保留为 Serve/Responses 的专用 background driver 支撑；不得把 ACP、TUI 或 Channel 的通用运行时依赖继续放入 `internal/serve/runtime`：

- Session Runtime；
- Agent/AgentFactory 组装；
- Tools Registry 构建；
- MCP 连接和清理；
- Skills/context 构建；
- Provider/model 运行时绑定；
- Sandbox 和工具策略；
- Mode/policy 解析；
- Approval/Question 抽象；
- Run 生命周期、取消、事件和锁；
- Session capability 持久化和恢复。

### Adapters

入口层只负责：

- 解析自己的输入协议；
- 创建入口特有的 Policy 和 Hook；
- 调用 Runtime；
- 将统一 Agent Event 映射为自己的输出格式；
- 处理 UI/协议交互。

适配层不应再直接拼接完整 `agent.Config`，也不应分别实现 MCP、Skill、Session 恢复或 Run 核心逻辑。

## 3. 当前状态评估

| 区域 | 当前复用度 | 与目标偏离 | 结论 |
|---|---:|---:|---|
| Agent loop | 75%～85% | 较小 | 已是共享核心，不应重写 |
| Provider/factory | 80%～90% | 较小 | 方向正确，继续作为唯一创建入口 |
| Tools 实现 | 80%～90% | 较小 | 实现集中，Registry 装配仍重复 |
| MCP | 70%～85% | 中等 | 底层共享，连接生命周期分散 |
| Skills/context | 70%～80% | 中等 | Manager 共享，加载时机和上下文拼接分散 |
| Session 持久化 | 75%～85% | 中等 | 数据层统一，内存 Runtime 类型分裂 |
| Session Runtime | 35%～50% | 明显 | `APISession`、`ChannelSession`、ACP runtime、TUI state 各自维护 |
| Mode/policy | 35%～50% | 明显 | 默认值、强制策略和恢复逻辑不统一 |
| Approval | 40%～55% | 明显 | 入口回调存在，但缺少统一 pending/decision 模型 |
| Run lifecycle | 40%～55% | 明显 | WebUI、Channel、ACP、TUI 各有编排逻辑 |
| 入口薄封装程度 | 40%～55% | 明显 | 入口仍承担大量 Runtime 职责 |

总体判断：项目整体距离目标架构约偏离 35%～45%，但核心能力已经具备，不需要推倒重写。

## 4. 必须保持的架构原则

1. **只有一个 Agent 核心执行语义。** 所有入口都调用同一个 Agent loop。
2. **只有一个 Agent Runtime 装配路径。** Provider、Session、Tools、MCP、Skills、Sandbox 和 Policy 必须由统一 Runtime 构造。
3. **入口差异通过 Policy 和 Adapter 表达。** 不能通过复制 Agent loop 或复制 Session Runtime 表达。
4. **Provider 行为继续经过 `internal/provider/factory`。** 入口不得自行判断 vendor 或绕过 factory。
5. **Session 数据访问继续经过 `internal/session` / `internal/commondb`。** 不新增直接 SQLite schema 初始化。
6. **事件内容可以适配，事件语义不能分裂。** ACP、WebUI、Channel、TUI 可以有不同线格式，但统一来自 Agent/Runtime Event。
7. **配置 schema 保持兼容。** 不改变 `settings.json`、`serve.json` 现有字段语义，新增字段需单独评审。
8. **能力默认值必须在一个 resolver 中决定。** 展示、run、agent、approval、event 不得各自 fallback。

## 5. 核心设计

### 5.1 Runtime Source

Session 必须使用现有 binding/header 或新增兼容字段，记录或可靠推导其来源。当前项目已有 Channel binding 基础，优先复用它：

```go
type RuntimeSource string

const (
    SourceTUI     RuntimeSource = "tui"
    SourceWebUI   RuntimeSource = "webui"
    SourceWeChat  RuntimeSource = "wechat"
    SourceFeishu  RuntimeSource = "feishu"
    SourceACP     RuntimeSource = "acp"
    SourceCLI     RuntimeSource = "cli"
)
```

来源不是展示字段，而是影响默认 mode、approval、sandbox、工具和能力边界的运行时输入。`channel_type=wechat/feishu` 应映射为强制 yolo 的 Runtime Policy；不能只把它作为会话列表的展示字段。

### 5.2 ExecutionPolicy

入口通过 Policy 描述场景差异：

```go
type ExecutionPolicy struct {
    Source                  RuntimeSource
    ForcedMode              *Mode
    DefaultMode             Mode
    Approval                ApprovalPolicy
    Question                QuestionPolicy
    Tools                   ToolPolicy
    MCP                     MCPPolicy
    Skills                  SkillPolicy
    Sandbox                 SandboxPolicy
    MultiAgent              MultiAgentPolicy
    AllowInteractiveInput   bool
}
```

Policy 只描述限制和默认值。最终有效配置必须由 Runtime 统一计算，不能由入口直接修改 Agent config。

### 5.3 ModeResolver

统一提供：

```go
func ResolveMode(policy ExecutionPolicy, sessionMode, requestedMode string) (Mode, error)
```

规则：

```text
1. ForcedMode 存在时，始终使用 ForcedMode。
2. 否则使用合法的显式 requestedMode。
3. 否则使用合法的 sessionMode。
4. 否则使用 policy.DefaultMode。
5. 所有结果必须是 plan、agent、yolo 之一。
```

微信和飞书必须使用强制策略：

```text
SourceWeChat / SourceFeishu
    ForcedMode = yolo
```

因此微信/飞书的以下路径全部必须得到 `yolo`：

- Channel 直接执行；
- WebUI 恢复后执行；
- chat completion；
- submit run；
- background run；
- retry/recovery；
- compaction；
- sub-agent 的继承策略；
- run record 和 run event；
- approval event；
- agent.Config。

普通 API Session 仍可保持现有语义：明确 mode 优先，空 mode 使用 API `DefaultMode`。不能把所有空 mode 全局变成 yolo。

### 5.4 SessionRuntime

建议引入统一的前端中立结构：

```go
type SessionRuntime struct {
    ID          string
    Source      RuntimeSource
    WorkDir     string
    Manager     *session.Manager
    Registry    *tools.Registry
    SandboxMgr  *sandbox.Manager
    Skills      *skills.Manager
    MCPClients  []*mcp.Client
    AgentMgr    *agent.AgentManager
    Capabilities Capabilities
    Policy      ExecutionPolicy
    LastUsed    time.Time
}
```

它是 `APISession`、`ChannelSession`、ACP `sessionRuntime` 和 TUI session runtime 的共享语义模型。入口可以持有自己的包装类型，但不能重新定义一套互不兼容的核心状态。

### 5.5 RuntimeBuilder

RuntimeBuilder 负责：

1. 校验和打开 Session；
2. 加载 Session capabilities；
3. 应用 Source/Policy 默认值；
4. 创建 Sandbox；
5. 加载 context files、rule 和 Skills；
6. 构建 Registry；
7. 注册 builtin/optional tools；
8. 连接 MCP；
9. 创建 AgentFactory/AgentManager；
10. 恢复历史；
11. 提供统一的 Agent 和 Run 创建入口。

建议接口形态：

```go
type Service struct { ... }

func NewService(cfg ServiceConfig) (*Service, error)
func (s *Service) CreateSession(ctx context.Context, opts SessionOptions) (*SessionRuntime, error)
func (s *Service) OpenSession(ctx context.Context, id string, opts OpenSessionOptions) (*SessionRuntime, error)
func (s *Service) CloseSession(ctx context.Context, id string) error
func (s *Service) DeleteSession(ctx context.Context, id string) error
func (s *Service) GetCapabilities(ctx context.Context, id string) (*Capabilities, error)
func (s *Service) PatchCapabilities(ctx context.Context, id string, patch CapabilityPatch) (*Capabilities, error)
func (s *Service) RunPrompt(ctx context.Context, id string, input PromptInput, hooks RunHooks) (*Run, error)
func (s *Service) CancelRun(ctx context.Context, sessionID, runID string) error
```

接口名称可以调整，但职责必须保持前端中立。

## 6. Approval 和 Question 统一模型

当前 `func(...) bool` callback 不足以表达挂起、拒绝、取消、超时和持久化决议。建议升级为：

```go
type ApprovalDecision string

const (
    ApprovalAllow   ApprovalDecision = "allow"
    ApprovalDeny    ApprovalDecision = "deny"
    ApprovalCancel  ApprovalDecision = "cancel"
    ApprovalTimeout ApprovalDecision = "timeout"
)

type ApprovalService interface {
    Request(context.Context, ApprovalRequest) (ApprovalDecision, error)
}
```

各入口实现自己的 adapter：

- TUI：终端交互；
- WebUI：pending approval + HTTP/WebSocket resolution；
- WeChat/Feishu：根据强制 yolo 和 Channel security 自动决策；
- ACP：映射到 `session/request_permission`。

共享层负责：

- approval ID；
- run/session/tool call 关联；
- context cancel；
- timeout；
- event persistence；
- terminal state。

`QuestionService` 采用同样方向，但允许入口决定是否支持交互式提问。Channel 默认不应依赖无法送达的交互式 question。

## 7. Tool/MCP/Skill 统一装配

新增统一 RegistryBuilder，集中处理：

- builtin tools；
- plan tool；
- question tool；
- skill_ref；
- browser；
- image generation；
- cron；
- A2A；
- sub-agent/delegate/workflow；
- external tools；
- MCP tools；
- tool filters；
- per-session capability 开关。

入口只提供 `ToolPolicy` 和 `MCPPolicy`，不再分别复制：

```go
registry := tools.NewRegistry(...)
mcp.LoadConfiguredServers(...)
mcp.ConnectServers(...)
registry.Register(...)
```

MCP client 的连接和关闭必须绑定到 `SessionRuntime` 生命周期，避免 ACP、WebUI、Channel 分别泄漏或采用不同清理规则。

Skills 的全局/项目目录发现、Load、context 构建也归入 RuntimeBuilder；入口只决定激活哪些 Skill 以及如何展示结果。

## 8. AgentFactory 使用规则

现有 `internal/agent.AgentFactory` 是目标架构的重要基础，应提升为所有入口的标准 Agent 创建入口。

目标规则：

```text
adapter 不直接构造完整 agent.Config
adapter 不直接调用 agent.New / agent.NewWithLoopConfig
adapter 只调用 Runtime/AgentFactory 的 Create
```

允许的入口特有参数包括：

- Agent ID；
- parent ID；
- 用户请求文本；
- 入口专属 Approval/Question service；
- 事件 sink；
- 明确的 per-run options。

不应由入口自行决定：

- provider/model 绑定方式；
- mode fallback；
- Registry 基础工具；
- MCP/Skills 初始化；
- Session replay；
- Sandbox 基础策略；
- 子 Agent 默认继承规则。

## 9. Run/Execution Runtime

`RunExecutor` 当前主要服务于 WebUI，应演进为跨入口的 Execution Runtime。

共享职责：

- 创建 run ID；
- 建立 session/run 锁；
- 解析最终 Policy 和 Mode；
- 创建 Agent；
- 启动 Agent loop；
- 转发统一 Event；
- 处理 approval/question；
- cancel/abort；
- terminalization；
- usage；
- run、run event 和 approval event 持久化；
- recovery/replay 的通用部分。

以下内容留在适配层：

- WebUI 的 HTTP 状态码、SSE、WebSocket；
- TUI 的 Bubble Tea message；
- Channel 的 ProgressFunc 和平台消息；
- ACP 的 JSON-RPC notification；
- OpenAI-compatible response JSON。

Responses API 的 provider-specific background driver 可以继续保留在 serve/runtime，但应通过通用 Run/Execution 接口接入，不应复制普通 Agent 的生命周期语义。

## 10. 四类适配层的目标职责

### TUI

保留：

- Bubble Tea model/update/view；
- 输入历史和终端渲染；
- terminal approval/question；
- Agent Event 到终端内容的映射；
- TUI 特有命令和显示状态。

下沉：

- Agent 装配；
- Session replay；
- Registry/MCP/Skills 构建；
- mode/policy resolution；
- 通用 run/cancel 生命周期。

TUI 可以是第一阶段暂不迁移的适配层，但长期目标仍是使用同一个 Runtime。

### WebUI/API

保留：

- HTTP handlers；
- JSON/OpenAI API compatibility；
- WebSocket/SSE；
- WebUI 专属响应和事件格式。

下沉：

- `APISession` 的核心 Runtime 状态；
- `buildSessionResources`；
- `syncSessionTools`；
- capability persistence；
- Agent config 组装；
- run 核心生命周期。

### WeChat/Feishu Channel

保留：

- 平台鉴权和消息收发；
- 用户/平台 identity 映射；
- ProgressFunc；
- 平台错误和消息格式；
- Channel-specific Policy 创建。

必须下沉或统一：

- Session 创建/恢复；
- Agent 组装；
- MCP/Skills/Tools；
- run lifecycle；
- mode resolver。

必须强制：

```text
WeChat/Feishu policy.ForcedMode = yolo
```

无论 WebUI 是否打开过该 Session、是否有 capability 记录、请求是否省略 mode，实际执行都必须是 yolo。

### ACP

保留：

- stdio JSON-RPC framing；
- ACP method dispatch；
- ACP request/notification 映射；
- ACP approval/question 输出；
- ACP protocol error mapping。

下沉：

- `sessionRuntime` 的核心实现；
- Registry/MCP/Skills setup；
- Agent 创建；
- Session replay；
- cancel 和 run lifecycle。

## 11. 分阶段实施计划

### Phase 0：建立不变量和测试 ✅ 已完成

已建立并验证跨入口不变量：

- WeChat/Feishu session 的 effective mode 永远是 yolo；
- runtime 展示 mode 与 run/agent/event mode 一致；
- 普通 API 空 mode 仍使用 API DefaultMode；
- Session 重启后 capability 和 source 可恢复；
- approval payload 的 mode、run ID、tool call ID 保持一致。

跨入口完整 ApprovalDecision/Question service 仍属于后续收敛工作。

### Phase 1：统一 Mode/Source/Policy ✅ 已完成

已完成：

- `RuntimeSource` 到现有 channel binding/header 的映射；
- `ExecutionPolicy`；
- `ModeResolver`；
- Channel forced `yolo`；
- run/agent/approval/event mode 统一解析；
- `/mode`、external background、WebUI submit、chat completion、recovery 等入口的 policy 检查。

后续可继续扩展统一 policy schema，使 sandbox/tools/MCP/approval policy 也由同一策略对象表达。

### Phase 2：抽取 Session Runtime 和资源构建 ✅ 核心完成

已从 WebUI/Serve 资源装配中抽取并接入 `internal/agentruntime`：

- `SessionRuntime`、`Builder`、`AttachSessionResources`；
- Context/Skills/Rule/Sandbox 初始化与刷新；
- `RegistryPolicy`、`RegistryMutator`、`BuildRegistry`；
- 严格/可选 `MCPPolicy` 和 Runtime-owned MCP cleanup；
- `CreateSession`、`OpenSession`、`DeleteSession`；
- `NewAgentManager`、`SessionRuntime.BuildAgent`；
- 基础 `ExecutionRuntime`。

OpenAI API 的 HTTP/approval/run orchestration 仍部分保留在 adapter，后续需要继续收敛。

### Phase 3：迁移 Channel ✅ 已完成当前方案边界

Channel 已使用共享 Runtime：

- Session 创建/恢复通过 `agentruntime`；
- Registry/MCP/Skills/Context/Rule 通过 Runtime policy；
- 普通 Agent 创建通过 `SessionRuntime.BuildAgent`；
- active run 的 Begin/Cancel/SetAgent/Finish 通过 `ExecutionRuntime`；
- 保留 Channel adapter 的 forced yolo、Security、Hooks、ProgressFunc、watchdog、消息和 run event 映射。

Channel 的平台持久化和协议事件映射仍属于 adapter 预期职责。

### Phase 4：迁移 ACP ✅ 已完成当前方案边界

ACP 已收敛为协议适配层：

- Session 创建/恢复通过 `agentruntime`；
- Registry/MCP/Skills/Context/Rule 通过 Runtime policy；
- 普通 Agent 创建通过 `SessionRuntime.BuildAgent`；
- prompt active state/cancel/agent abort/finish 通过 `ExecutionRuntime`；
- 保留 ACP JSON-RPC framing、permission/question、MCP callbacks、protocol error、replay 和 rollback。

完整跨入口 Run/Approval/Question service 仍需后续统一。

### Phase 5：迁移 TUI ⏳ 未开始

TUI 尚未迁移：

- TUI 保持显示和交互状态；
- Agent/Session/Tool/Skill/MCP 由共享 Runtime 管理；
- 保证终端体验和现有命令行为不变。

## 12. 兼容性和迁移要求

- 不修改既有 `settings.json`、`serve.json` 字段语义；
- 不改变 OpenAI-compatible API 形状；
- 不改变 ACP 外部协议，除非增加可选扩展；
- 不改变 Channel 对外消息格式；
- 现有 Session 数据可继续打开；
- 旧的空 capability mode 必须按照来源 policy 恢复；
- 对无法确认来源的旧 Session，不得静默覆盖用户明确持久化的合法 mode；
- Channel Session 优先通过现有 `channel_type`/`channel_id`、Session header 和 binding API 识别，不能只依赖 WebUI 侧推测；
- 对现有已绑定的微信/飞书 Session，Policy 必须覆盖空 mode、显式 agent/plan、外部 background 请求和 recovery 路径，最终强制为 yolo；
- 迁移期间允许 adapter wrapper 存在，但不能新增第二套核心规则。

## 13. 验收标准

### 核心一致性

- 四类入口使用同一个 Agent loop；
- 所有 Agent 创建最终经过统一 Factory/Runtime；
- 新增工具、MCP transport 或 Skill 能力时，核心实现只需增加一份；
- 新增能力后，各入口只需增加协议映射或 Policy 配置。

### Channel 强制策略

- WeChat/Feishu 所有执行路径最终 mode 都是 `yolo`；
- WebUI 恢复同一 Channel Session 后仍是 `yolo`；
- run、run event、approval event、agent config 的 mode 全部为 `yolo`；
- 请求传入 `agent` 或 `plan` 时，Channel forced policy 仍不会被覆盖。

### 普通 API 行为

- 普通 API session 的显式 mode 仍然优先；
- 普通 API session 的空 mode 仍按 API `DefaultMode`；
- runtime snapshot 不再使用与执行路径不同的展示专用 fallback。

### 资源和生命周期

- MCP clients 在 Session Runtime 关闭时可靠清理；
- Skills/context 只由共享 Runtime 构建；
- Session replay 在不同入口使用相同规则；
- cancel、timeout、approval deny 和 provider error 都能得到一致 terminal state；
- `go test ./internal/agent/... ./internal/provider/... ./internal/session/... ./internal/serve/... ./internal/acp/...` 通过。

## 14. 风险与控制措施

### 抽取边界过大

先从 Mode/Policy 和资源构建开始，不一次性重写 WebUI、Channel、ACP。每一阶段保持旧 adapter 行为不变。

### Approval 挂起模型复杂

先把现有 WebUI approval 收敛成明确接口，再迁移到共享 Runtime。不要直接把当前 `bool` callback 原样扩大到所有入口。

### Channel 安全回归

Channel forced yolo 不是“无条件放开所有高风险操作”。mode、sandbox、allow policy 和高风险命令拦截仍然是不同维度。必须保留 Channel security 的高风险保护规则。

### Session 来源丢失

新增 source metadata 时要兼容旧 Session。对旧 Channel session，优先从 channel metadata、session header 或稳定关联信息恢复来源；无法确认时不能把普通 API session 误判为 Channel。

### Provider 特殊后台任务

Responses background run、A2A 和外部子 Agent 可以有专属 driver，但必须通过统一的 session/run/policy 语义接入，不能重新定义另一套 mode 和 approval 规则。

## 15. 本次 Review 的最终结论

MothX 已经有一个可用且功能完整度较高的 Agent 核心，但当前架构仍处在“共享内核、分散 Runtime”的阶段。

最需要做的不是继续向每个入口添加功能，而是停止入口级复制，并建立统一的：

```text
Source → Policy → SessionRuntime → AgentFactory → ExecutionRuntime → Adapter
```

其中：

- `Source` 表示 TUI/WebUI/WeChat/Feishu/ACP 等场景；
- `Policy` 表示该场景允许和强制的行为；
- `SessionRuntime` 统一 Session、Tools、MCP、Skills、Sandbox 和 capabilities；
- `AgentFactory` 统一 Agent 创建；
- `ExecutionRuntime` 统一 run、approval、cancel、event 和 terminal state；
- `Adapter` 只负责协议、交互和输出映射。

这样才能实现目标：

> 一个完整的 Agent 核心，拥有完整的 Tools/MCP/Skill/Session/多厂商兼容能力；TUI、WebUI、Channel、ACP 是不同的薄封装，只根据场景注入不同权限和配置，而不是各自实现自己的 Agent 功能。
