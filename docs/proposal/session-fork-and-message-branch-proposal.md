# MothX Session 分叉与消息进度分叉方案

> 状态: Implemented（核心落地完成，发布验收受环境工具限制）
> 日期: 2026-08-22
> 目标版本: 待定
> 调研来源: `/home/free/src/deepseek-harness`

## 实现进度

- [x] Session schema 增加分叉 lineage 字段、`conversation_turns`、分叉幂等表和跨进程 runtime lease 表。
- [x] `turn/start`、`turn/end` 条目类型进入 Session 编解码和删除完整性边界。
- [x] 现有 runtime lock 调用点获得 SQLite lease、TTL 心跳、进程实例 ID、随机 token hash 和 epoch 接管能力；进程内 mutex 仅作为快速路径。
- [x] ConversationTurn 原子起止事务与所有会写入用户/助手 transcript 的入口接入；durable Run admission 会在同一事务写入 intent、Run、started event、`conversation_turns` 和 `turn/start`，终止路径由 Agent 与 Runtime 共同保证 `turn/end` 幂等收敛；ESM、子 Agent、`/btw` 和维护 Run 明确不创建对话轮次。
- [x] Session 行/消息边界解析、历史 Session 保守映射、原子前缀复制、ID 重映射和统一 `Runtime.Fork` 接入。
- [x] CLI print、TUI、API/WebUI/Channel 入口、跨进程 fencing 写入和真实子进程故障测试接入；ACP 继续通过统一 Runtime/Session admission 复用同一轮次模型，暂不新增 ACP 私有 fork RPC。
- [x] lease 丢失自动取消 ExecutionRuntime，释放采用持久化 tombstone；Run/Event、Response archive、tool execution、capability、intent 和 usage 写入均经过 lease fencing。

本进度表随实现同步更新；上述入口已具备服务端校验、幂等保护和跨进程故障恢复测试。完整入口包测试仍受当前沙箱禁止 IPv6 loopback 的既有测试限制影响，WebUI 构建需具备 `npm` 后再做发布验收。

最近一次验收记录：`go test ./internal/session ./internal/agentruntime ./internal/agent ./internal/architecture` 通过；全仓 `go test ./... -run '^$'` 编译通过；真实子进程 lease 故障和 Session A/B 并发测试通过。`internal/serve/openaiapi`、`internal/acp` 的部分 httptest 运行测试因当前沙箱禁止 IPv6 loopback 而无法启动监听器；TUI 两个认证模型数量断言为既有失败，和本功能无关；当前环境没有 `npm`，因此 WebUI 构建未执行。

## 1. 概述

本方案为 MothX 增加两种入口相同、语义不同的 Session 分叉能力：

1. 从 Session 列表行分叉：复制当前 Session 最近一个已结束的对话轮次。
2. 从某条助手消息分叉：复制包含该消息的已结束对话轮次，在该轮次之后创建独立 Session。

两种入口都调用同一个 Runtime 分叉操作。分叉结果是一个新的、可继续执行的 Session；源 Session 不修改，不共享后续运行状态。消息分叉不是字符级截断，也不是“编辑用户问题后重新提问”，而是以消息所在的完整对话轮次为最小复制单位。

本方案参考了 deepseek-harness 的实现和交互，但按 MothX 的 SQLite Session、`SessionRuntime`、`ExecutionRuntime`、决策服务和通道绑定模型重新定义边界。方案确认前不应在 WebUI 或 API 中分别实现一套分叉逻辑。

## 2. 调研结论

### 2.1 deepseek-harness 的核心模型

deepseek-harness 将 Session 视为追加式事件日志。其核心 `ctx.sessions.fork(source, boundary)` 会复制源 Session 的事件前缀，子 Session 通过 `parentSession` 和 `seedLength` 记录来源和复制长度，之后追加 `session/end-seed` 标记。源 Session 的日志不会被截断或改写。

Host 层的 `session.fork` RPC 做了产品化边界解析：

| 入口 | 请求 | 分叉边界 |
|---|---|---|
| Session 行 | 不传 `atSeq` | 最近一个已完成 turn 的结尾；进行中的尾部被忽略 |
| 助手消息 | 传该消息的 `seq` | 包含该消息的第一个已完成 turn 的结尾 |
| 任意开放 turn | 传入开放 turn 内的 `seq` | 返回不可分叉，不自动向前回退到上一轮 |

边界确定后，还会吸收下一个 turn 开始前的尾随元数据事件，避免子 Session 在重放时丢失必要的状态。子 Session 作为普通会话出现在列表中，源会话和子会话都可以继续运行。

### 2.2 deepseek-harness 的 UI 约束

- Session 行的分叉按钮实际分叉最近一个已完成 turn。
- 只有已结束 turn 尾部的最终助手文本消息显示可用的“分叉”操作。
- 工具调用、工具结果、重试、错误或仍在流式输出时，消息分叉按钮禁用。
- 用户消息不提供同一个操作。若要编辑用户问题并重新请求，需要另一个“重问/预填输入框”操作。
- 子 Session 默认是列表中的平级条目，不在侧栏中展开分叉树；标题可追加 `(1)`、`(2)` 等稳定后缀。

调研涉及的主要文件包括：

- `/home/free/src/deepseek-harness/packages/core/session/src/index.ts`
- `/home/free/src/deepseek-harness/packages/host/apiproxy/src/api/sessions.ts`
- `/home/free/src/deepseek-harness/packages/client/runtime/src/client/sessions/service.ts`
- `/home/free/src/deepseek-harness/packages/client/ui-conversation/src/turn-tail.ts`
- `/home/free/src/deepseek-harness/apps/web/tests/message-actions.e2e.ts`

## 3. 目标与非目标

### 3.1 目标

1. 通过一个共享 Runtime 操作完成 Session 行分叉和消息分叉。
2. 分叉结果包含可重放的历史前缀，并能作为普通 Session 发起下一轮 Agent 执行。
3. 分叉边界稳定、可解释，不能复制半个运行中的 turn。
4. 记录父 Session、源边界和复制长度，支持诊断和后续 lineage 展示。
5. 支持已驻留 Runtime 的热 Session 和只存在于 SQLite 的冷 Session。
6. 保持源 Session、源 Run、审批/提问决策、工具幂等记录和通道身份不变；子 Session 不继承这些活动状态。
7. WebUI 提供 Session 行入口和符合消息尾部规则的助手消息入口。

### 3.2 非目标

1. 本期不支持句子、字符或内容块级别的截断。
2. 本期不支持修改用户问题后自动重问；该能力需要独立的 composer 预填和新 Run 语义。
3. 本期不支持从正在运行、等待审批、等待提问或正在取消的 Session 分叉。
4. 本期不建立嵌套的分叉树视图，不改变 Session 列表的平级展示模型。
5. 本期不复制历史 Run、Delivery、Decision、MCP 客户端或远端 Responses 状态。
6. 本期不为分叉自动创建 Git worktree；子 Session 默认与源 Session 使用相同工作目录。

## 4. MothX 当前状态与缺口

### 4.1 可以复用的能力

- `internal/session` 已有 `sessions`、`entries` 和追加式 `EntryBase.ParentID` 链。
- `entries.seq` 可作为 API 和 UI 的稳定定位值；消息列表已经通过 `SequencedMessage` 暴露该序号。
- `GetReplayState`、压缩条目和现有分支摘要条目已能重建当前会话上下文；现有 `BranchSummaryEntry` 不是跨 Session lineage 边界的权威来源。
- `internal/agentruntime` 已统一 Session 创建、Agent 构建、Run 生命周期和决策恢复。
- `session_metadata` 已提供项目归属和置顶等列表元数据。

### 4.2 必须补齐的能力

MothX 当前主要持久化消息条目，没有覆盖所有 Agent 入口的通用**对话轮次**起止标记。`response_turns` 只服务 OpenAI Responses 归档，不能作为通用 Session 分叉边界。这里必须区分 `ExecutionRuntime Run` 与 `ConversationTurn`：ESM 角色、后台工具恢复、只执行工具的维护任务等 Run 不一定产生对话消息，不能自动成为可分叉 turn；只有会向 Session transcript 写入用户/助手/工具消息的逻辑请求才创建 `ConversationTurn`。

需要增加不参与模型消息重放的元数据条目：

```text
turn/start  { turnId, intentId, runId, attempt, timestamp }
turn/end    { turnId, intentId, runId, status, stopReason, timestamp }
```

建议在 `internal/session/entry.go` 中增加 `EntryTurnStart`、`EntryTurnEnd` 及对应结构体，并新增 `conversation_turns` 表作为可恢复的边界索引：

```sql
CREATE TABLE conversation_turns (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  intent_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'conversation',
  status TEXT NOT NULL,
  start_seq INTEGER NOT NULL,
  end_seq INTEGER,
  started_at TEXT NOT NULL,
  ended_at TEXT
);
CREATE INDEX idx_conversation_turns_session ON conversation_turns(session_id, start_seq);
CREATE INDEX idx_conversation_turns_open ON conversation_turns(session_id, status);
```

`conversation_turns` 是边界解析的权威索引，`turn/start`、`turn/end` 是写入 `entries` 的可审计序列投影，并提供 UI/API 所需的全库 `seq`。一次逻辑对话请求只创建一个 `ConversationTurn`；同一 intent 的自动 Provider 重试或 durable attempt 共享 `turnId`，只有逻辑请求最终结束时才写 `turn/end`。非对话 Run 不写这两类条目。

边界条目必须纳入 Session 的所有编码/解码路径：`getEntryMetadata`、`Manager.load`、内存 replay、`GetReplayState`、消息列表和条目类型校验都要识别它们；消息列表和 Provider replay 过滤它们，fork resolver 和恢复逻辑读取它们。

对话 turn 的开始事务必须同时写入 `session_runs`/intent（若该入口有 durable Run）、`conversation_turns` 行和 `turn/start` 条目；结束事务必须同时写入 `turn/end` 条目、更新 `conversation_turns.end_seq/status`、终结 `session_runs` 并追加终止 Run event。条目加载和 Provider 上下文重放必须忽略这两类元数据，但边界解析和恢复逻辑必须保留它们。

若进程在开始或结束事务前崩溃，恢复逻辑以 `conversation_turns.status=open` 和 durable Run 状态为依据：补写终止边界或将其标为不可分叉，不能把未闭合 turn 当作已完成 turn。

分叉 UI 只有在该边界模型对所有会产生 transcript 的入口生效后才可启用；否则不同 Provider、TUI、WebUI 和后台 Run 会产生不一致的“已完成轮次”判断。

### 4.3 历史 Session 兼容

已有 Session 不会被强行重写成新的事件序列。对没有 `turn/start`、`turn/end` 的历史数据，边界解析器只考虑会产生 transcript 的已结束 `session_runs`，并用消息条目时间戳做严格的候选映射；只有 Run 与消息能够形成唯一、单调、无重叠的区间时才允许分叉。ESM、后台工具和没有消息的 Run 不参与映射。映射不明确时返回 `legacy_boundary_unavailable`，不猜测边界，也不把整段历史当作一个 turn。新 Session 或继续运行过的 Session 从新逻辑请求开始写入标准 turn 标记。

## 5. 产品语义

### 5.1 Session 行分叉

请求不带 `atSeq` 时，服务端选择源 Session 中最后一个 `conversation_turns.status` 为终止状态且存在 `end_seq` 的对话轮次。若末尾存在未结束的 turn，只复制到上一轮结束位置；若没有任何可分叉的已结束轮次，返回 `no_completed_turn`。

### 5.2 助手消息分叉

请求带 `atSeq` 时，`atSeq` 必须指向源 Session 的 `EntryMessage` 条目序号，且服务端必须验证该消息是助手角色、属于一个已终止的对话 turn、是该 turn 的最终助手文本节点，并且其后没有工具调用、工具结果、重试或错误节点。验证通过后，服务端选择该消息所属的完整已结束 turn，并复制到该轮次结束位置。用户消息、工具消息或任意元数据序号即使落在已结束 turn 内也返回 `fork_unavailable`，不能只依靠 UI 禁用操作。

消息操作只在以下条件同时满足时显示为可用：

- 消息角色为助手；
- 消息是该已结束 turn 的最终助手文本节点；
- 消息之后没有未完成的工具调用、工具结果、重试或错误节点；
- 源 Session 当前没有活动 Run、未决 Decision 或开放 `conversation_turns`。

### 5.3 尾随元数据

确定 `turn/end` 后，继续吸收其后的非 `turn/start` 元数据条目，例如模型、模式、目录或标题变更。遇到下一个 `turn/start` 时停止。这样子 Session 的重放状态与源 Session 在该边界处一致，而不会把下一轮用户输入复制进去。

### 5.4 标题和列表

子 Session 默认使用源标题加稳定分支后缀，例如 `原标题 (1)`。后缀编号在同一 `parentSession` 下分配，不能依赖前端当前列表顺序。子 Session 在列表中作为平级会话展示；详情接口可以返回 `parentSession`、`forkBoundarySeq` 和 `forkKind`，但本期不要求侧栏绘制父子树。

## 6. 持久化模型

### 6.1 Session 头信息

在 `sessions` 表追加迁移字段，并同步到 `session.Header`；新库的 `currentSchema`、`requiredSchema`、旧库 migration、`Manager.load`、Session insert、详情查询和删除完整性测试必须同时更新：

```sql
ALTER TABLE sessions ADD COLUMN fork_boundary_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN seed_length INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN fork_kind TEXT NOT NULL DEFAULT '';
```

同时创建上一节的 `conversation_turns` 表。所有 schema 变更必须作为 `internal/session/migrations.go` 的追加版本完成，不能只修改迁移 SQL 而遗漏空数据库 schema。

同时创建分叉请求幂等表，key 只保存哈希，不保存原始请求头：

```sql
CREATE TABLE session_fork_requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_key_hash TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL,
  source_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  child_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  UNIQUE(request_key_hash, source_session_id)
);
```

字段含义：

| 字段 | 含义 |
|---|---|
| `parent_session` | 源 Session ID；根 Session 为空 |
| `fork_boundary_seq` | 源 Session 中被复制的最后一个 `entries.seq` |
| `seed_length` | 子 Session 实际复制的条目数量，不等同于全库 `seq` |
| `fork_kind` | `session` 或 `message`，用于审计和 UI 展示 |

迁移必须作为 `internal/session/migrations.go` 的追加版本完成，不能在新逻辑中增加独立的 `CREATE TABLE` 或旁路 schema 初始化。

### 6.2 条目复制

分叉在一个 SQLite 事务中完成：

1. 读取源 Session 的稳定快照和边界内全部条目。
2. 创建新的 Session ID 和新的条目 ID。
3. 按源顺序插入条目，重新生成子 Session 的全库 `seq`，并将 `conversation_turns.start_seq/end_seq` 映射为 child 的新序号。
4. 用旧 ID 到新 ID 的映射重写 `parent_id` 以及已知结构化引用，例如压缩条目的 `firstKeptEntryId`、`lastSummarizedEntryId`、`previousCompactionId`，分支摘要的 `fromId` 和标签的 `targetId`。
5. 复制允许继承的 Session 配置快照：复制 `session_capabilities` 的 mode、display mode 和 feature flags；不复制 `session_capability_events`，也不复制通道专属的工具绑定。绑定通道时清除通道身份并重新按 child source 解析。复制 `session_metadata.project_id`，将子 Session 的 `pinned` 设为 `false`，并在同一事务内写入子 Session 头信息和标题条目；若需要可读摘要，另增明确的 fork 摘要条目，不能用摘要代替边界字段。
6. 提交事务；标题后缀、lineage、能力快照、项目关联和幂等结果必须与历史前缀一起成功或一起回滚。

条目 ID 在当前 schema 中是全局唯一的，不能直接复用源 ID；不能只复制 JSON 文本而留下跨 Session 的父引用。未知 `EntryCustom` 类型如果包含不可重写的 ID 引用，必须声明“可复制”策略，否则分叉应返回明确错误，而不是产生不可重放的子 Session。

### 6.3 不复制的记录

以下记录属于执行或外部状态，不随历史前缀复制：

- `session_runs`、`session_run_events`、`session_execution_intents`；
- `conversation_turns` 不复制为活动状态；只为边界内已结束 turn 重建 child 索引行，并将其 `session_id`、`start_seq`、`end_seq` 重映射为 child 值；
- 审批/提问 Decision 及其投影；
- `response_turns`、`response_items`、`response_runs`、`response_session_state`；
- 分叉请求幂等记录只在 child 事务中写入新的 child 结果映射，不复制源 Session 的幂等 key；`session_fork_requests` 必须加入 Session 删除和 schema 完整性维护清单；
- 工具执行幂等记录、能力事件、Delivery 记录和活动取消状态；
- Cron 绑定、后台恢复状态和 MCP 客户端连接。
- `session_runtime_leases`；child 不继承源进程的 owner、epoch、token 或运行时租约。

子 Session 下一次运行必须建立新的 Run、Decision 和远端 Provider 状态。历史消息仍通过 Session 条目重放，不依赖源 Session 的远端 `previous_response_id` 或 conversation ID。若某 Provider 的历史包含无法从本地消息安全重建的 hosted item、附件或函数调用归档，必须返回 `fork_unsupported_entry`；不能复制远端 ID 后假装 child 可继续。

### 6.4 工作目录、项目和通道

- `cwd`、追加目录和可重放的会话配置按边界复制；运行时仍通过 `ResolveSource`/`ResolvePolicy` 重新解析有效能力、模式和沙箱。
- `session_metadata.project_id` 可复制，`pinned` 默认置为 `false`。
- 分叉出的 WebUI 子 Session 默认强制使用 `channel_type=local`、空 `channel_id`。不复制 WeChat/Feishu 绑定，避免同一个外部身份收到两份回复。
- 若未来允许通道适配器发起分叉，必须由该适配器显式提供绑定策略；不能在通用 Session 分叉中复制通道身份，也不能绕过强制 `yolo` 规则。

## 7. 边界解析算法

边界解析属于 `internal/session`，不属于 Svelte、HTTP handler 或某一个 Agent 适配器。活动 Run、未决 Decision、开放 turn 和通道策略由 `SessionRuntime` 在调用数据层前检查；热 Session 和冷 Session 都必须先通过跨进程 Session ownership lease，再查询 canonical SQLite 状态。SQLite `BEGIN IMMEDIATE` 仍用于快照和提交原子性，但不能单独充当“运行中 Session 锁”；数据层伪代码如下：

```text
resolveBoundary(source, atSeq?):
  entries = source.entries ordered by seq
  turns = source.conversation_turns joined to start_seq/end_seq

  if atSeq is omitted:
    end = last conversation_turn with terminal status and end_seq
  else:
    reject if atSeq is not an EntryMessage in source
    containing = turn containing atSeq
    reject with fork_unavailable if message is not final assistant text
    reject with fork_unavailable if containing has no terminal end_seq
    turn = containing
    end = turn.end

  end = absorb entries after end until next turn/start
  return { boundarySeq: end.seq, entries: entries where seq <= end.seq, turnID }
```

实现时需要注意：

- `entries.seq` 是全库自增值，解析时必须始终附带 `session_id` 过滤；不能把它当作子 Session 内的从零序号。
- `atSeq` 只接受整数；小数、负数、其他 Session 的序号和不存在的序号返回 `invalid_boundary`。
- “开放 turn”不能静默退回上一轮，因为用户点击的消息已经表达了明确的分叉位置。
- 压缩条目按原始条目顺序复制，`GetReplayState` 在子 Session 中重新计算当前上下文。
- 源 Session 在快照和提交之间如果追加了新条目，事务应检测叶子或最大序号变化并返回 `session_modified`，由客户端重新读取后重试。
- 对冷 Session，事务开始前和复制提交前都必须确认 `GetActiveSessionRun`、Decision replay 和 `conversation_turns` 没有变化；发现 orphan Run 或状态不一致时返回 `session_active`/`session_modified`，不能只依赖内存 Runtime。

## 8. Runtime 与模块边界

### 8.1 `internal/session`

提供纯数据层能力。`RequestID` 必须是调用方生成的稳定幂等标识；HTTP handler 将 `Idempotency-Key` 映射为该字段，TUI/ACP/通道直接调用 Runtime 时也必须提供等价标识：

```go
type ForkOptions struct {
    SourceSessionID string
    AtSeq           *int64
    RequestID       string // required for direct Runtime callers; HTTP maps Idempotency-Key here
}

type ForkResult struct {
    SessionID        string
    ParentSessionID  string
    ForkKind         ForkKind
    BoundarySeq      int64
    SeedLength       int64
}

func Fork(ctx context.Context, options ForkOptions) (ForkResult, error)
```

该层负责快照、边界解析、条目复制、ID 重映射、事务和数据错误，不构建 Agent，不启动 Run，也不操作 UI。

### 8.2 `internal/agentruntime`

增加面向入口的 `SessionRuntime.Fork` 或等价服务，负责：

1. 找到热 Session 或打开冷 Session；
2. 检查内存 `ExecutionRuntime` 与 canonical SQLite 是否有活动 Run、等待决策、取消流程或开放对话 turn；
3. 调用 `internal/session` 完成原子复制；
4. 为子 Session 重新解析 `RuntimeSource`、`ExecutionPolicy`、能力、沙箱和 MCP 策略；
5. 通过既有 `BuildAgent`/`BuildTransientAgent` 路径在子 Session 下一次运行时构建 Agent；
6. 写入标题、项目元数据和 lineage 事件，并保证关闭和错误回滚符合 `SessionRuntime.Shutdown` 语义。

分叉不创建第二套 Agent、Run、Decision 或 SessionPool 生命周期。源 Session 的 Agent 不被迁移到子 Session；热源只提供 lease 保护下的一致性快照，不能把进程内 mutex 当作跨进程所有权。

### 8.3 适配器

`internal/serve/openaiapi`、WebUI、TUI 和未来 ACP 只负责传递 `sessionId`、可选 `atSeq`、展示结果和映射错误。它们不能直接复制 `entries`，也不能自行拼装 `agent.Config` 或 Run 记录。

### 8.4 跨进程 Session ownership lease

#### 8.4.1 问题边界

MothX 的 TUI、CLI、ACP、WebUI 和通道入口可能由不同进程同时打开同一个 Session。当前 `internal/session/runtime_lock.go` 中的 `runtimeLocks`/`sync.Mutex` 只在单个 Go 进程内共享；`session_runs` 的 active 状态只能表达持久化运行记录，不能表达哪个进程仍然拥有实时执行权。因此以下做法都不充分：

- 只使用进程内 `sync.Mutex`：不同进程互相不可见；
- 只使用 SQLite 写事务：它只能短暂地互斥数据库写入，事务结束后无法表示 Agent 仍在运行；
- 只使用 `session_runs.status=running`：进程崩溃后会留下无法判断是否仍有效的永久状态；
- 只使用 UDP 的“加锁/解锁”消息：UDP 可能丢包、重复、乱序，也可能被同机其他进程伪造，不能作为所有权或 fencing 的权威来源。

因此必须把“运行中 Session 的独占权”建模为可过期、可恢复、可 fencing 的 lease。SQLite 是恢复和裁决的持久化权威；进程内互斥锁是本进程的快速路径；UDP 只做低延迟通知，绝不能单独授予、撤销或恢复所有权。

这里的互斥范围是单个 `session_id`，不是进程、数据库或整个 MothX 实例：

- Session A 和 Session B 可以在同一个进程或不同进程中同时运行，各自拥有独立的 lease、心跳、Run 和 epoch；不能增加一个全局 runtime mutex 把它们串行化。
- 同一个 Session 同时只能有一个有效的 `purpose=run` lease；`purpose=fork` 只允许在没有活动 Run 时短暂持有。不同 Session 的运行、分叉和恢复互不阻塞。
- SQLite 的写事务只覆盖短时 admission、心跳、entry/Run event 提交和 lease 更新，不能在模型调用、工具执行或等待审批期间持有数据库事务；因此 SQLite 的单写者特性不会把多个 Session 的完整执行串行化。
- Session lease 不负责工作目录、Provider 配额、MCP 服务或其他全局资源的互斥。多个 Session 共享同一 `cwd` 仍可能同时修改文件，这属于独立的工作区并发风险，必须由沙箱、工具策略或 worktree 方案处理。

#### 8.4.2 权威数据模型

在 `internal/session/migrations.go` 中追加 `session_runtime_leases` 表。一个 Session 同时最多有一条有效 lease：

```sql
CREATE TABLE session_runtime_leases (
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  owner_instance_id TEXT NOT NULL,
  owner_pid INTEGER NOT NULL,
  owner_kind TEXT NOT NULL,
  lease_token_hash TEXT NOT NULL,
  epoch INTEGER NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  purpose TEXT NOT NULL,
  state TEXT NOT NULL,
  acquired_at INTEGER NOT NULL,
  heartbeat_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_session_runtime_leases_expiry
  ON session_runtime_leases(expires_at);
```

字段和约束：

| 字段 | 语义 |
|---|---|
| `owner_instance_id` | 进程启动时生成的随机实例 ID，不能只使用 PID；应包含随机 boot nonce，避免 PID 复用误认旧进程。 |
| `owner_pid`、`owner_kind` | 诊断字段，例如 `tui`、`cli`、`acp`、`webui`、`channel`；不参与安全裁决。 |
| `lease_token_hash` | 本次持有权的随机 token 哈希；原 token 只驻留内存，不能写入数据库或 UDP 明文。 |
| `epoch` | 每次接管或重新获得所有权单调递增的 fencing 代数。所有 Run、entry、Decision 和终止写入都必须携带并校验它。 |
| `run_id`、`purpose` | 关联 durable Run；分叉等短事务使用 `purpose=fork`，完整 Agent 执行使用 `purpose=run`。 |
| `heartbeat_at`、`expires_at` | 由 SQLite 当前时间裁决的租约心跳和失效时间，不能以 UDP 是否到达作为存活判断。 |
| `state` | `acquiring`、`active`、`releasing`、`lost`；`lost` 只用于诊断，过期后新持有者必须递增 `epoch`。 |

时间字段使用整数 Unix 秒，由 SQLite 的当前时间参与比较，避免不同进程的本地时钟判断不一致。空数据库 schema、迁移、Session 删除级联、列表/诊断查询和 schema 完整性测试必须同时更新。

#### 8.4.3 Lease API 和持有范围

建议在 `internal/agentruntime` 提供唯一编排接口，底层存取放在 `internal/session`：

```go
type SessionLease struct {
    SessionID string
    OwnerID   string
    Epoch     int64
    RunID     string
    Renew     func(context.Context) error
    Release   func(context.Context) error
    Lost      <-chan struct{}
}

type SessionLeaseCoordinator interface {
    Acquire(ctx context.Context, sessionID, purpose, runID string) (*SessionLease, error)
    TryAcquire(ctx context.Context, sessionID, purpose, runID string) (*SessionLease, error)
}
```

`SessionLeaseCoordinator` 内部可以先取得当前进程的 `LocalRuntimeMutex`，但不能把它暴露为跨进程语义。所有会占用 Session 的 durable Run 必须从 admission 到 terminalization 持有同一 lease；等待 Approval/Question、取消和恢复也属于持有范围。Session 分叉通常只在边界快照和复制事务期间持有 `purpose=fork` lease，提交后立即释放。

`TryLockRuntime`、`LockRuntime` 和 `TryLockRuntimes` 的调用点应迁移到该 coordinator；至少覆盖 `internal/serve/openaiapi` 的 chat/run submit、ESM coordinator、background run/external coordinator、Responses API 和 `internal/acp` admission。多 Session 操作必须按排序后的 Session ID 依次取得 lease，避免跨进程死锁。`sessionDataLocks` 可以继续作为短时本地优化，但数据事务仍需使用 SQLite 约束；涉及运行策略、运行状态或 transcript 的写入必须校验 lease。现有 `GetActiveSessionRun`/active unique index 继续保留，作为 lease 之外的 durable Run 一致性检查和恢复依据，不能替代 lease。

普通单 Session 请求只获取目标 Session 的一条 lease，不得因为另一个 Session 正在运行而返回 busy。绑定转移、批量删除、批量恢复等确实同时触碰多个 Session 的管理操作，必须先规范化、去重并按 `session_id` 字典序获取完整 lease 集合；任一获取失败就按逆序释放已获取 lease 并整体失败，不能留下部分成功的跨 Session 状态。

#### 8.4.4 获取、续租、释放和 fencing

所有 lease 操作均由 SQLite `BEGIN IMMEDIATE` 或等价的原子条件更新完成，不能先在内存中宣布成功再异步写库。

```text
Acquire(session):
  BEGIN IMMEDIATE
  row = lease(session)
  if row 不存在:
      epoch = 1
      INSERT 新 owner/token/expires
  else if row.expires_at <= SQLiteNow:
      epoch = row.epoch + 1
      CAS 更新 owner/token/epoch/expires
  else if row.owner_instance_id/token 是当前进程:
      续租并复用 epoch
  else:
      返回 session_busy，并带 owner_kind、run_id、expires_at 诊断信息
  若关联 active Run 属于其他有效 epoch，拒绝接管；若 lease 已过期，先完成 owner/token/epoch 的 CAS 接管，再用新 epoch 将旧 Run 标记为 recovered/failed，最后才能建立新 Run
  COMMIT
  发布 LEASE_ACQUIRED 通知

Heartbeat:
  每次在 BEGIN IMMEDIATE 中执行：
    UPDATE ... SET heartbeat_at=SQLiteNow, expires_at=SQLiteNow+TTL
    WHERE session_id=? AND owner_instance_id=? AND epoch=?
      AND lease_token_hash=? AND expires_at>SQLiteNow
  更新行数为 0 即视为 lease lost，关闭 Lost 通道并停止继续执行

Release:
  先停止续租；使用 session_id + owner_instance_id + epoch + token 的 CAS
  将 lease 标记为 released tombstone；若 CAS 失败，绝不能清理新 owner 的 lease。
  tombstone 必须保留到 Session 删除或后续更高 epoch 覆盖，避免旧 owner 的延迟写入
  被误判为“没有 lease 的冷写入”。
  发布 LEASE_RELEASED 通知
```

所有 canonical 持久化写入至少必须在同一个 SQLite 写事务内先校验 `session_id + epoch + owner_instance_id + lease_token_hash`，覆盖 entry 追加、Run event、Run 状态、Decision resolution、turn/end 和最终错误写入；lease heartbeat/release 则使用相同字段的 SQL CAS 条件。校验不匹配时返回 `ErrSessionLeaseLost`；旧 owner 即使从暂停中恢复，也不能在新 owner 接管后继续写入。Agent 或工具执行收到 `Lost` 后必须尽快取消；外部副作用无法回滚时，至少保证副作用结果不会以旧 epoch 写入新的 Session transcript。高风险工具在真正执行副作用前应再次检查 lease。

#### 8.4.5 时间参数和故障语义

默认参数建议固定为：`leaseTTL=15s`、`heartbeatInterval=3s`、数据库忙重试窗口不超过 `2s`。实际实现应允许配置，但必须满足 `heartbeatInterval <= leaseTTL/3`，并为调度暂停、数据库锁竞争留出余量。续租失败不能立即把锁交给别人；旧 lease 只有在 SQLite 判断 `expires_at <= SQLiteNow` 后才能被接管。

| 故障 | 处理 |
|---|---|
| 正常退出 | 停止 Agent，完成终止写入后 best-effort release；release 失败由 TTL 兜底。 |
| `SIGKILL`、进程崩溃、机器重启 | 心跳停止，TTL 到期后 lease 可接管；不会永久占锁。遗留 active Run 必须先恢复/终结，再允许新 Run。 |
| 进程被暂停超过 TTL | 旧进程视为失去所有权；恢复后收到 `ErrSessionLeaseLost`，不得继续写入。TTL 应覆盖正常调度抖动，不能无限延长。 |
| SQLite 暂时 busy/不可用 | 在忙重试窗口内重试；超过窗口后停止续租并取消执行。UDP 不能替代数据库续租。 |
| UDP 丢包、重复、乱序或监听器未启动 | 不影响正确性；接收方最终以 SQLite lease 和 epoch 为准。 |
| 旧 owner 发送延迟的 release/heartbeat | epoch、owner 和 token CAS 不匹配，消息和写入全部忽略。 |
| 两个进程同时抢占过期 lease | SQLite `BEGIN IMMEDIATE` 串行化，只有一个进程递增 epoch 成功，另一个返回 busy 或重新读取结果。 |

接管过期 lease 不是静默复用旧 Run。恢复流程必须记录旧 owner、旧 epoch、接管原因和时间，并将旧 Run/开放 turn/未决 Decision 按既有 recovery 规则终结或标记不可恢复；不得因为“没有收到 UDP release”而无限等待。

#### 8.4.6 UDP 的正确定位

已实现 `SessionLeaseBus`：使用回环网段的定向广播 `127.255.255.255:49371`。每个 Serve/桌面包装进程以端口复用方式监听同一 UDP 端口，因此多个本机 Serve 实例都能收到一次通知；监听 socket 只接受 loopback 来源，广播不会离开本机。消息只用于缩短其他进程的发现延迟。Serve 收到消息后只会回读 SQLite 并投影状态；监听失败或 UDP 丢失时自动退化为原有的 SQLite 查询、WebSocket 重连回放路径：

```text
LEASE_ACQUIRED { messageID, origin, originInstanceID, sessionID, ownerInstanceID, epoch, expiresAt }
LEASE_RELEASED { messageID, origin, originInstanceID, sessionID, ownerInstanceID, epoch }
LEASE_LOST     { messageID, origin, originInstanceID, sessionID, ownerInstanceID, epoch }
RUN_STATE_CHANGED { messageID, origin, originInstanceID, sessionID }
```

接收方处理规则必须是：

1. 先按 `sessionID` 更新本地 advisory cache 或唤醒等待者；
2. 发生冲突、接管或恢复时重新读取 SQLite lease，校验 `epoch` 和 owner/token；
3. 不因一条 `LEASE_RELEASED` 就删除本地或数据库中的其他 owner；
4. 不因没有收到 heartbeat 就提前接管；唯一的超时依据是 SQLite 的 `expires_at`；
5. UDP 不携带原始 token，不作为认证、持久化或锁恢复通道。

这里的 UDP 广播不是“广播锁”：它不能授予或撤销 lease，只是跨进程 fan-out 的 advisory wake-up。为了避免广播风暴，接收端绝不把收到的消息再次发送；每条消息具有发送者实例 ID 和唯一 message ID，发送者忽略自己的 loopback 副本，接收端在短期窗口内去重。即使使用回环广播也只能改善通知速度，仍需保留 SQLite lease、epoch fencing 和 TTL，因为它们负责跨启动恢复、诊断和最终裁决。跨平台实现应把 IPC 机制封装在 `SessionLeaseBus` 后面，UDP 不可用时自动降级为纯 SQLite lease。

#### 8.4.7 与分叉及 Run admission 的关系

分叉不是运行时锁的替代品，但分叉必须遵守同一 ownership 边界：

- 源 Session 有其他进程持有未过期 `purpose=run` lease 时，分叉返回 `session_active`，不能只看当前进程的内存 Runtime；
- 源 Session 无活动 Run 时，分叉事务先取得短时 `purpose=fork` lease，再执行边界解析、最大 seq/leaf 重检和原子复制；
- lease 过期但 `session_runs` 仍为 active 时，先走 recovery gate，不能直接复制半成品历史；
- child 不复制 `session_runtime_leases`，也不复制源 Run、Decision、Delivery 或通道绑定；
- 分叉事务提交后释放源 lease，child 的下一次运行重新走普通 `Acquire`，生成新的 owner/epoch/run。

`ExecutionRuntime.BeginDurable`、`ReattachDurable`、`UpdateDurable`、`CancelDurable`、`FinishDurable` 应接收并校验 lease fencing 信息，形成“lease admission + durable Run + turn boundary”一致的生命周期。这样 TUI、CLI、ACP、WebUI 即使是不同进程，也只能有一个有效执行者，且旧进程无法在新进程接管后继续污染 Session。

## 9. API 方案

建议新增一个同时支持两种入口的接口：

```text
POST /api/sessions/{id}/fork
{
  "atSeq": 123,
  "titleMode": "increment"
}
```

请求头：`Idempotency-Key: <opaque-client-key>`。

`atSeq` 省略表示 Session 行分叉；传入表示消息分叉。`forkKind` 由服务端根据 `atSeq` 是否存在推导，客户端不能伪造。`titleMode` 只表达用户界面意图，标题写入仍由 Session/Runtime 完成。成功响应：

```json
{
  "sessionId": "child-id",
  "parentSessionId": "source-id",
  "forkKind": "message",
  "boundarySeq": 1234,
  "seedLength": 18
}
```

建议错误码：

| 错误码 | 语义 |
|---|---|
| `session_not_found` | 源 Session 不存在 |
| `session_busy` | 另一个进程持有未过期 Session lease，返回 owner kind、run ID 和 lease expiry 诊断信息 |
| `session_active` | 源 Session 有运行、审批、提问或取消中的 Run |
| `session_recovery_required` | lease 已过期但遗留 Run/Decision 无法按 recovery 规则自动终结 |
| `session_lease_lost` | 当前执行者的 lease 续租或 fencing 校验失败，执行已被取消 |
| `no_completed_turn` | 没有可复制的已结束 turn |
| `fork_unavailable` | `atSeq` 位于开放 turn 或不可分叉消息 |
| `invalid_boundary` | 序号格式错误、不存在或不属于源 Session |
| `session_modified` | 快照期间源 Session 发生追加 |
| `fork_unsupported_entry` | 条目含有无法安全重写的引用 |
| `legacy_boundary_unavailable` | 历史 Session 无法唯一推导 turn 边界 |
| `fork_channel_unsupported` | 通道适配器未提供安全的绑定策略 |
| `idempotency_key_required` | 缺少 `Idempotency-Key` |
| `request_id_required` | 直接 Runtime 调用缺少 `RequestID` |
| `idempotency_key_conflict` | 同一 key 对应不同分叉请求 |

## 10. WebUI 交互

WebUI、TUI、ACP 和受支持的通道适配器都调用同一个 API/Runtime 分叉操作；界面可以有不同的入口，但不能有不同的边界解析或复制实现。

### 10.1 Session 行

- 在会话行操作菜单中增加“从此处分叉”。
- 点击后调用不带 `atSeq` 的统一接口。
- 成功后打开子 Session；源 Session 保持原选中状态以外的内容和历史不变。
- 运行中、无已结束 turn 或通道策略不允许时禁用并显示原因。

### 10.2 助手消息

- 仅在最终助手文本消息的尾部显示分叉图标。
- 图标操作调用带该消息 `seq` 的统一接口。
- 流式消息、工具调用/结果、重试、错误和用户消息不提供可用分叉操作。
- 创建成功后切换到子 Session；消息列表重新从 API 读取，不能在前端拼接一个“临时子会话”。

### 10.3 列表和刷新

子 Session 返回后复用现有 Session 列表刷新和标题接口。父子关系只作为详情元数据，不要求改变当前项目、最近和未归类的平级列表结构。

## 11. 安全与一致性要求

1. 分叉本身不得执行工具、写入工作区或发送通道消息。
2. 源 Session 有活动 Run、未决 Decision 或取消流程时必须拒绝，避免复制半成品和复活已解决的审批。
3. 子 Session 不得继承源 Session 的活动 Run、Decision、工具幂等键、远端响应状态或通道身份。
4. 分叉后的有效模式和能力必须由共享 resolver 重新计算；不能把源 Session 的显示模式直接当作执行策略。
5. 使用同一工作目录意味着两个 Session 后续可能修改同一文件；UI 应在首次分叉结果中提供轻量提示，Git worktree 隔离另立方案。
6. 失败事务必须不留下孤立 Session、半截条目或错误的项目关联；重试不能重复创建相同 child。
7. 任何会持续执行的 Run、等待决策、取消和恢复流程都必须持有有效 `SessionLease`；跨进程互斥的权威是 SQLite lease，不是 `runtime_lock.go` 中的进程内 mutex。
8. 每个持有者必须以固定心跳续租，并使用 `epoch + owner_instance_id + lease_token` fencing；lease 过期后旧进程不得继续追加 transcript、改变 Run 状态或解决 Decision。
9. 进程异常退出、SIGKILL、机器重启、UDP 丢包或正常 release 失败都必须由 TTL 恢复；任何路径都不能因为缺少 release 消息永久阻塞 Session。
10. UDP/IPC 消息只能刷新 advisory cache 或唤醒等待者，不能绕过 SQLite 校验授予、撤销或接管 lease；消息必须可丢失、重复和乱序。
11. 续租失败、fencing 失败和恢复接管必须生成结构化诊断事件及指标，包含 Session、旧/新 epoch、owner kind 和结果，不记录原始 token。

## 12. 完整落地方案

### 12.1 统一的 Session 分叉服务

`SessionRuntime.Fork` 是所有入口的唯一编排入口，接收源 Session ID 和可选的 `atSeq`，并返回新的 Session ID、父 Session ID、分叉类型、源边界序号和复制条目数。分叉类型由服务端根据 `atSeq` 是否存在推导，客户端不能自行声明或覆盖。

HTTP 调用必须携带 `Idempotency-Key`（长度不超过 256 字节）；直接 Runtime 调用必须传 `RequestID`。Runtime 以调用来源、源 Session、规范化后的 `atSeq`/`titleMode`、请求 key 和 boundary fingerprint 建立持久化幂等记录：同 key 同请求返回原 child；同 key 不同请求返回 `idempotency_key_conflict`；没有 key 不执行复制。这样同一边界可以合法创建多个不同 child，同时重复点击不会创建重复 child。

服务执行顺序固定为：

1. 通过 `SessionRuntime` 定位热 Session 或打开冷 Session，并先获取源 Session 的 `purpose=fork` lease；跨进程所有权由 SQLite lease + epoch fencing 保证，`BEGIN IMMEDIATE` 只负责事务原子性，不能只依赖 Go mutex。
2. 通过内存 `ExecutionRuntime` 和 canonical SQLite 同时检查活动 Run、等待中的 Approval/Question、取消流程、开放 `conversation_turns` 和不可复制的通道绑定；冷 Session 不存在内存 Runtime 时以 SQLite 为准。若存在其他进程的未过期 `purpose=run` lease，立即返回 `session_active`。
3. 在持有 fork lease 的同一 SQLite `BEGIN IMMEDIATE` 事务内读取条目/turn/能力快照、解析边界、复制 Session 头、复制允许的元数据和条目，并完成所有 ID 重映射；提交前再次校验源 Session 最大 seq、leaf ID、turn 状态、lease epoch 和活动 Run fingerprint。
4. 将子 Session 的通道设为 `local`，复制工作目录和项目归属，清除 `pinned`，写入 lineage 字段。
5. 在同一事务内为 child 分配标题后缀计数和幂等记录；标题采用源标题加 ASCII 后缀 `(1)`、`(2)`，后缀分配在父 Session 范围内原子完成。
6. 提交事务后释放源 Session 的 fork lease；若释放失败由 TTL 兜底。返回 child；子 Session 不启动 Run，下一次请求再通过既有 `BuildAgent`/`BuildTransientAgent` 构建 Agent，并生成新的 owner/epoch/run。子 Session 的 capabilities snapshot 只作为可持久化初始配置，最终 mode/source/capabilities 仍由 resolver 决定。

任何一步失败都不得留下可见的半成品 child。分叉结果、lineage、能力快照、项目归属、标题后缀和幂等记录必须同事务提交；失败全部回滚。响应丢失时，客户端使用同一个 `Idempotency-Key` 重试并取回原 child，不能依赖“父 Session + 边界”猜测 child。

### 12.2 Turn 边界和自定义条目规则

`turn/start`、`turn/end` 采用独立强类型 Session 条目，不使用 `EntryCustom` 作为通用边界。所有会产生 transcript 的逻辑请求在 `ConversationTurn` 开始和最终 `EventRunFinished` 时写入对应条目；不产生对话消息的 ESM、后台工具、恢复和维护 Run 不写入 turn 条目。TUI、WebUI、API、ACP 和通道入口都必须经过这个统一判断。

`EntryCustom` 只有在明确声明无外部 ID 引用、可安全复制时才参与分叉。含有未知 `parent`、message、tool 或外部资源引用的自定义条目一律返回 `fork_unsupported_entry`，不采用静默跳过或猜测重写。

错误终止、取消或不完整但已写入最终助手文本的 turn 仍是终止 turn：Session 行可以分叉；消息入口只有在最终助手文本满足尾部规则时可以分叉。

### 12.3 历史数据兼容

对没有标准 turn 标记的旧 Session，服务端只使用已结束 `session_runs` 与条目时间戳进行严格、唯一、单调的区间推导。推导结果必须能确定用户消息、助手消息和工具循环属于同一个 Run；存在并发、重叠或缺失时直接返回 `legacy_boundary_unavailable`。

不对旧 entries 进行猜测式重写，不把全部旧历史强行标记为一个 turn，也不在无法确定边界时退回上一条消息。旧 Session 在成功继续运行后，从新 turn 开始写入标准边界条目。

### 12.4 所有前端的最终行为

- Session 列表、TUI 命令、ACP 请求和通道管理命令均使用不带 `atSeq` 的行分叉语义，并传递稳定的 `RequestID`；HTTP 使用 `Idempotency-Key`。
- 消息操作只针对最终助手文本，传递该消息的 `seq`；服务端负责判断它所属的完整 turn。
- 用户消息、流式节点、工具调用/结果、重试、错误节点和活动 Run 没有可用的消息分叉操作。
- child 创建成功后由调用方打开 child，但历史内容必须重新从 Session API 读取，不能在前端拼接临时副本。
- child 在 Session 列表中保持平级显示；详情接口提供父 Session 和边界字段，不渲染嵌套分叉树。

### 12.5 运行策略和外部状态

子 Session 只复制可重放的会话历史。有效模式、能力、沙箱、工具、MCP 和审批策略通过共享 resolver 重新计算；不复制任何 Run、Decision、Delivery、工具幂等记录、远端 Responses 状态、Cron 绑定或通道身份。

子 Session 与源 Session 继承同一工作目录。界面在创建结果中明确提示两者可能同时修改同一工作区；Git worktree 隔离不是本分叉操作的隐式副作用。

### 12.6 观测与运维

每次分叉记录结构化审计事件，至少包含 `parentSession`、`childSession`、`forkKind`、`requestedAtSeq`、`boundarySeq`、`seedLength`、调用来源和结果码，不记录用户消息全文或 Provider 密钥。指标至少区分成功、活动 Run 拒绝、lease busy、lease lost、过期接管、恢复失败、开放 turn 拒绝、历史边界不可推导、条目引用不支持和并发修改重试；诊断接口可显示当前 owner kind、PID、epoch、run ID、heartbeat/expiry，但不得显示原始 token。

## 13. 测试与验收标准

### 13.1 Session/Runtime 测试

- 行分叉复制最后一个已结束 turn，忽略开放尾部。
- 消息分叉复制消息所在的完整 turn，不复制下一轮用户消息。
- 首轮、开放 turn、中间工具消息、无助手输出和不存在的 `atSeq` 返回预期错误。
- 没有标准 turn 标记的历史 Session 只有在边界可唯一推导时可分叉，否则返回 `legacy_boundary_unavailable`。
- 子 Session 条目 ID 全部唯一，`parent_id`、压缩引用和标签引用均指向子 Session 内的新 ID。
- `turn/start`、`turn/end` 和 `conversation_turns` 的边界序号在 child 中互相一致；非对话 Run 不会创建可分叉 turn。
- 源 Session 的 entries、标题、Run、Decision、Response 状态和通道绑定保持不变。
- child 复制 `session_capabilities` 的允许字段、项目归属和本地标题，但不复制能力事件、通道工具绑定或活动状态。
- 复制压缩前后历史得到相同的 `GetReplayState`；子 Session 新 Run 能从本地历史开始而不复用远端 response state。
- 开始/结束事务中途进程崩溃后，恢复逻辑不会把 open turn 当作 completed；孤儿 Run 或未决 Decision 会阻止分叉。
- 同一 `Idempotency-Key`/`RequestID` 重试返回原 child，不同请求复用同一 key 返回冲突；同一边界使用不同 key 可以创建多个 child。
- 活动 Run、等待审批、等待提问、取消中和并发追加均不能产生半成品 child。
- 同一进程内的 Session A 与 Session B 可以同时运行；不同进程分别持有 A/B lease 时互不阻塞，A 的 lease busy 不得影响 B 的 admission。
- 一个进程已经持有 Session A lease 时，另一个进程仍可以正常 acquire Session B；不能通过单个进程级 owner 或 UDP 全局状态误拒绝 B。
- 同一 Session 的 run/fork 竞争必须互斥，而不同 Session 的 run/run、run/fork、fork/fork 可以并发；只有共享同一 Session 或显式批量操作的 Session 集合才需要冲突。
- 绑定转移等需要两个 Session 的操作按稳定顺序获取 A/B lease；反向请求也必须使用同一顺序，验证无死锁、无半完成绑定。
- 两个独立进程同时 acquire 同一 Session 时只有一个成功，另一个收到 `session_busy`；持有者心跳期间其他进程不能分叉或启动第二个 Run。
- 持有者被 `SIGKILL` 或强制终止后，测试等待 TTL 到期，确认另一个进程能够接管并递增 epoch；旧进程恢复或延迟写入必须收到 `ErrSessionLeaseLost`。
- UDP 通知被丢弃、重复、乱序或完全禁用时，Session ownership 和超时接管仍正确；伪造旧 epoch 的 release/heartbeat 不能影响新持有者。
- 数据库忙、短暂不可用和心跳重试覆盖窗口时，不能误接管未过期 lease；超过窗口后旧 owner 自行停止执行，TTL 到期后才可恢复。
- 进程接管遗留 active Run 时，先按 recovery 规则终结旧 Run/open turn/Decision，再允许分叉或新 Run，不能复制半成品。

### 13.2 API/WebUI 测试

- Session 行和消息尾部都调用同一 endpoint，响应返回正确 `forkKind` 和边界。
- 第一条或非最终助手消息的分叉按钮禁用；最终助手文本按钮可用。
- 用户消息、工具调用、工具结果、重试和流式消息不显示可用分叉。
- 分叉后源会话继续追加内容时，子会话历史不变化；两者均可独立发起下一轮 Run。
- 刷新、冷启动、移动端布局和重复点击不会创建重复 child。
- 直接构造 API 请求传入用户消息、工具消息、元数据或不存在的 `atSeq` 均被服务端拒绝，不能仅依赖 UI 按钮状态。
- OpenAI Responses 历史含有无法本地重建的 hosted item、附件或函数调用时返回 `fork_unsupported_entry`，不产生不可继续的 child。

### 13.3 架构守护

实现后必须运行：

```bash
go test ./internal/session ./internal/agentruntime ./internal/serve/openaiapi
go test ./internal/architecture
```

若修改了 Agent 构建、Run 持久化或决策恢复路径，还应增加跨入口契约测试，确认 TUI、WebUI、API 和 ACP 仍使用同一个 Runtime 分叉和执行生命周期；跨进程 lease 至少要有真实子进程测试，不能只用同进程 goroutine 模拟。

## 14. 结论

MothX 应把“Session 分叉”和“消息进度分叉”实现为同一个 Session 前缀复制能力的两个调用入口，而不是在 WebUI 中复制消息数组或新增一套 Agent 流程。关键前置条件是建立跨 Provider 的 turn 边界事件，以及由 SQLite lease、心跳、TTL 和 epoch fencing 组成的跨进程 Session ownership。完成后，`internal/session` 负责原子数据复制和 lease 持久化，`internal/agentruntime` 负责 ownership、运行策略和生命周期，HTTP/UI 只负责入口与投影；UDP 仅作为可丢失的低延迟通知通道。

这个边界既保留了 deepseek-harness 中“从最后完成轮次分叉”和“从助手消息所在轮次分叉”的实用语义，也符合 MothX 当前的追加式 SQLite 日志、统一 Runtime、决策恢复和通道安全约束。
