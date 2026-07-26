# ACP 与 Serve API 共享后端抽象 Proposal

> Date: 2026-07-26
> Status: Rejected（2026-07-26）
> 关联: `docs/proposal/desktop-client-packaging-proposal.md`

> **决议**：放弃共享后端抽象。理由：三个前端的交互语义差异（approval 决议方式、run 生命周期、capability 模型）会被压进共享层的 hook/adapter 设计里，抽象本身带来的复杂性超过去重收益。ACP 保持独立实现，按需单独维护。

## 背景

用户反馈：ACP（`internal/acp`）长期缺乏维护，能力已明显落后于 serve Web UI API。根因是架构性的——**同一套"会话 + agent 运行时"在仓库里被重复实现了至少三遍**，新能力只加在 serve 一侧，ACP 和 channels 自然腐烂。

## 现状：三份重复的运行时装配

### 1. ACP（`internal/acp/acp.go`，约 1800 行）

- 自有 `server` + `sessionRuntime` map（session.Manager + tools.Registry + mcp clients + cancel）
- `newToolRegistry`：defaults + plan + question + skill_ref + browser + sub-agent/delegate/workflow 工具
- `handlePrompt` 里手工装配 `agent.Config`（provider、mode、thinking、compaction、ApprovalHandler→`session/request_permission`）
- 事件映射：`agent.Event` → ACP `session/update` 通知
- **缺失**：per-session capability 开关（webSearch/browser/a2aMaster/multiAgent/workflows 只能在进程启动时全局定）、capability 持久化、session pool 管理、cron 工具、usage 统计事件、skills 热切换、`/settings` 类操作、图片 prompt（`promptCaps.Image: false`）

### 2. serve OpenAI API（`internal/serve/openaiapi/`，约 8700 行）

- `Server` + `SessionPool`（上限 + idle 淘汰）+ `APISession`
- `buildSessionResources` / `syncSessionTools`：sandbox per workdir、skills + context files、registry、可选工具注册
- `SessionCapabilities`：per-session 开关 + 持久化到 session 文件 + 变更事件
- `handler_chat` 装配 `agent.Config`，approval 走 HTTP pendingApprovals 轮询/决议
- 事件映射：`agent.Event` → SSE transcript / WebUI 消息
- slash commands、usage 上报、模型切换、cron 工具、A2A 工具

### 3. serve channels（`internal/serve/channels/dispatcher.go`）

- 又一份 `agent.NewWithLoopConfig` 装配 + `mcp.ConnectServers` + `session.New`
- 自有 AgentManager 工厂、A2A 工具注册
- yolo 语义、ProgressFunc 进度推送

### 重复点清单

| 关注点 | ACP | openaiapi | channels |
| --- | --- | --- | --- |
| session 创建/打开/关闭/删除/列表 | ✅ 自有 | ✅ 自有 + pool | ✅ 自有 |
| workdir 资源构建（sandbox/skills/context/rule） | ✅ 简化版 | ✅ 完整 | 部分 |
| 工具注册（含可选工具） | ✅ 启动时定死 | ✅ 运行时可切 | ✅ 部分 |
| per-session capability 模型 + 持久化 | ❌ | ✅ | ❌ |
| agent.Config 装配 | ✅ | ✅ | ✅ |
| MCP 连接/清理 | ✅ | ✅ | ✅ |
| approval / question 交互 | request_permission | HTTP 决议 | 自动通过 |
| 事件→线格式映射 | sessionUpdate | SSE transcript | ProgressFunc |

每加一个新能力（比如 session tools 开关、cron 工具），目前要改 1–3 个地方，ACP 基本被跳过。

## 目标

抽出一个前端中立的**会话运行时服务**（下称 `agentruntime`），ACP、serve HTTP API、channels 都变成薄协议适配层。能力只在共享层实现一次，三个前端自动同时获得。

## 非目标

- 不统一事件线格式（ACP `session/update`、SSE transcript、channel ProgressFunc 各自的映射保留在适配层，改动最小）。
- 不改变 agent loop 本身（`internal/agent` 的 Run/事件语义不动）。
- 不改变 `settings.json` / `serve.json` schema。
- TUI 不第一批迁移（它和 agent loop 耦合最深，交互语义不同）。

## 设计

### 新包：`internal/agentruntime`

位置与 `internal/agent`（loop）、`internal/session`（持久化）平级，职责是"装配好的多会话运行时"。

```go
// Service 是进程级的共享后端：持有 settings/provider/sandbox 基线，
// 管理一组 Session，提供运行 prompt 的统一入口。
type Service struct { ... }

func NewService(cfg ServiceConfig) (*Service, error)

// 会话生命周期（三个前端共用同一语义）
func (s *Service) CreateSession(opts SessionOptions) (*Session, error)
func (s *Service) OpenSession(id, workDir string, mcpServers []mcp.ServerConfig) (*Session, error)
func (s *Service) CloseSession(id string) error
func (s *Service) DeleteSession(id string) error
func (s *Service) ListSessions(workDir string, cursor string) ([]SessionInfo, string, error)

// 能力模型：从 openaiapi 的 SessionCapabilities 提升为共享模型
func (s *Service) GetCapabilities(id string) (*Capabilities, error)
func (s *Service) PatchCapabilities(id string, patch CapabilityPatch) (*Capabilities, error)

// 运行
func (s *Service) RunPrompt(ctx context.Context, id string, in PromptInput, hooks RunHooks) (<-chan agent.Event, error)
func (s *Service) Cancel(id string) error

type RunHooks struct {
    Approval ApprovalHandler // 前端各自实现
    Question QuestionHandler // ACP: request_permission 扩展；WebUI: HTTP；channel: 默认
}

type Capabilities struct {
    Mode       string
    WebSearch  bool
    Browser    bool
    A2AMaster  bool
    Delegate   bool
    MultiAgent bool
    Workflows  bool
    // 持久化沿用现有 session 文件中的 SessionCapabilities 存储
}
```

`Session` 统一现在 `acp.sessionRuntime` 与 `openaiapi.APISession` 的并集：Manager、Registry、SandboxMgr、SkillsMgr、AgentMgr、Capabilities、mcp clients、active run（cancel/runID）、LastUsed。pool（上限 + idle 淘汰）作为 `Service` 的可选配置，ACP 默认不启用淘汰。

### 三个适配层

1. **ACP adapter**（留在 `internal/acp`，大幅瘦身）：JSON-RPC 帧读写、方法分发 → `Service` 调用；`agent.Event` → `session/update`；ApprovalHandler → `session/request_permission`。`session/new` 的 `_meta` 扩展透传 capabilities。
2. **openaiapi adapter**：HTTP handler 只做请求解析/响应写线；session 操作、capability patch、run 管理全部委托。现有 HTTP API 形状不变（Web UI 无感）。
3. **channels adapter**：dispatcher 只保留消息平台路由和 ProgressFunc；session/agent 装配删除。

### 附带收益

- ACP 自动获得：per-session 工具开关、capability 持久化、skills 切换、cron 工具、usage/上下文用量事件、图片 prompt（共享层解析 content block 后 ACP 打开 `promptCaps.Image`）。
- 桌面客户端（mothxwork 路线）面对的 ACP 是"与 WebUI 能力等价"的协议，直接回应 desktop proposal 里的"ACP 差距清单"。
- 回归测试集中在共享层写一遍，适配层只测线格式。

## 分阶段计划

### Phase 1：抽取共享层（不动 ACP）

- 从 `openaiapi` 提取 `buildSessionResources` / `syncSessionTools` / capabilities / pool / run 生命周期为 `internal/agentruntime`。
- openaiapi 改为委托，HTTP 行为不变。现有 `server_test.go`（3600+ 行）即回归保障。
- channels 迁移到共享层。

### Phase 2：ACP 重写为适配层

- `internal/acp` 删除自有 sessionRuntime/装配逻辑，改调 `agentruntime.Service`。
- 补齐能力协商：`initialize` 的 `_meta` 声明 mothx 扩展（capabilities patch、question、usage），`promptCaps.Image: true`。
- ACP 协议文档（方法/参数/通知/错误码/扩展）落到 `docs/`。

### Phase 3：协议对等验证

- 用 mothxwork 的 `acp-research*.mjs` 思路写一个 Go 版 ACP 集成测试 harness：spawn `mothx acp`，跑 initialize→session/new→prompt→cancel→load 全流程。
- 桌面联调 checklist（Windows stdio、无 sandbox 降级）。

## 风险

- **抽取边界**：openaiapi 的 approval 与 run 生命周期耦合较深（`pendingApprovals` 持有 agent 指针用于 resume）。共享层需要把"approval 挂起/恢复"建模为 hook + resume token，这是设计中最难的部分，建议 Phase 1 先在 openaiapi 内部收敛成接口再提取。
- **行为回归**：serve 是生产路径，Phase 1 必须保持 HTTP API 完全兼容；ACP 重写放在 Phase 2 单独发版。
- **channels 语义差异**：yolo 默认、无 approval，迁移时注意保持自动通过 hook。

## 待讨论

1. 包名/位置：`internal/agentruntime`？还是放进 `internal/agent`（runtime 子文件）或 `internal/serve/backend`（但 ACP 不属于 serve）？
2. Approval/question 的共享抽象形态：hook 回调（各前端阻塞方式不同）vs 共享层内置"决议通道"模式（前端投递决议，统一超时/取消语义）？倾向后者，因为超时/取消逻辑现在也是重复的。
3. channels 是否 Phase 1 一起迁移，还是 Phase 2 之后单独做？
4. 共享层是否同时接管 slash commands（目前只在 openaiapi `commands.go`）？ACP 客户端也有 `/` 语义需求（如 `/compact`）。
5. TUI 最终是否也迁到共享层（长期单一份运行时），还是接受 TUI 特例？

## 验收标准

- `mothx acp` 与 serve API 对同一 session 的操作（创建、prompt、capability 切换、取消、删除）行为一致。
- ACP `initialize` 声明的能力与 WebUI 实际可用能力一一对应；新增 session 能力只需要改共享层 + 各适配层线格式。
- openaiapi 现有测试全绿；新增 agentruntime 包级测试覆盖 session 生命周期与 capability patch。
