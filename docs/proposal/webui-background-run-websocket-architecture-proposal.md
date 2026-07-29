# WebUI 后台 Run 与 WebSocket 会话同步完整改造方案

> 状态：In Progress (implementing)
> 日期：2026-07-29
> 最后更新：2026-07-30
> 目标：一次性完成 WebUI 会话、后台任务执行、实时同步和刷新恢复的彻底解耦

## 实现进度

### 已完成

- [x] 持久化 Run 表与 schema migration（`internal/session/migrations.go`, `schema.go`）
- [x] Run 持久化接口：`SaveSessionRun`, `GetSessionRun`, `GetActiveSessionRun`, `ListSessionRuns`, `UpdateSessionRunStatus`（`internal/session/run_store.go`）
- [x] session 删除时级联清理 `session_runs`（`internal/session/session.go`）
- [x] Run 持久化测试（`internal/session/session_test.go`）
- [x] 基础 `RunManager`：`Create`, `Attach`, `Start`, `Subscribe`, `SetHook`, `Publish`, `Cancel`, `Finish`, `Get`, `Active`（`internal/serve/openaiapi/run_manager.go`）
- [x] `Server` 注入 `runManager`（`internal/serve/openaiapi/server.go`）
- [x] handler 使用独立 `context.Background()` 而非 `r.Context()`（`handler_chat.go`）
- [x] handler 在 Run 开始/结束时写入持久化状态
- [x] `runtimeSnapshotFromCapabilities` 优先读取 `runManager.Active()`（`session_mgr.go`）
- [x] Run API：`GET /api/runs/{runID}`, `POST /api/runs/{runID}/cancel`（`run_api.go`）
- [x] WS 路由 `/ws/runs`（`internal/serve/run.go`）
- [x] WebSocket：`hello`, `subscribe`, `unsubscribe`, `replay`（`websocket.go`）
- [x] WebSocket replay 三类游标：`entrySeq`, `runSeq`, `capabilitySeq`
- [x] 前端全局 WS store：`runsSocket`, `runsConnected`, `runEvents`, `runCursors`, `connectRuns()`, `disconnectRuns()`（`ui/src/lib/stores.js`）
- [x] `App.svelte` 接入 `connectRuns()` / `disconnectRuns()`
- [x] `Chat.svelte` 接入 WebSocket 事件投影到 session 状态
- [x] 前端 WebSocket 自动重连 + 指数退避（max 15s）
- [x] 前端重连使用已有 `runCursors` 订阅，不再每次从零 replay
- [x] RunManager subscriber 关闭/清理统一为 `closeSubscribersLocked`
- [x] `/api/sessions/{sessionID}/runs` 列表接口（`internal/serve/run.go`）
- [x] `Server.ListSessionRuns` 实现（`session_mgr.go`）
- [x] `go test ./...` 和 `cd ui && npm run build` 已多次通过

### 待完成（按优先级排序）

#### P0 - 阻塞性：不完成则方案核心目标不成立

1. **独立 RunExecutor**：把完整 Agent event 语义从 HTTP subscriber（`handler_chat.go:431-590`）迁入独立 RunExecutor，使 HTTP handler 只做订阅而不拥有事件处理
   - 需要新文件 `internal/serve/openaiapi/run_executor.go`
   - 统一处理：text delta / tool call / tool result / approval / usage / retry / done / error / sub-agent
   - 处理顺序：Agent event → 转成 SessionEvent → 写 SQLite → 发布 EventBroker → 所有 subscriber

2. **WebUI 专用 Run 提交 API**：新增 `POST /api/sessions/{sessionID}/runs`，让 WebUI 不再通过 `/v1/chat/completions` 提交任务
   - 响应只返回 runID + status: queued
   - Chat.svelte 改为：提交 Run → 获得 runID → WebSocket 订阅 → 只通过 WS 接收消息

3. **统一 Run finalizer**：所有结束路径必须调用同一个幂等 finalizer
   - 写入终态、结束时间、错误和 usage
   - 清理 pending approvals
   - 记录 approval 终态事件
   - 清除 activeBySession
   - 释放 session runtime lock
   - 发布最终 runtime snapshot
   - 持久化 run finished/failed/cancelled 事件
   - 发布 run_done
   - 关闭 Run 内存 event channel
   - 释放 Agent、MCP、sub-agent 资源

#### P1 - 严重：影响可靠性和协议一致性

4. **WebSocket replay 与实时推送竞态修复**：当前 replay 期间实时事件可能进入 hub channel，需要 replay boundary + 去重 + 切入 live 的顺序保证
5. **WebSocket live 事件统一 envelope**：`sessionStreamEvent` 应改为结构化 `SessionEvent`，包含 sessionID/runID/stream/event/seq/ID/data
6. **WebSocket replay 事件名与前端协议统一**：当前 replay 发送的 `event: "transcript"` 等与前端现有 SSE event 名不一致
7. **前端重连完整恢复**：重连后重新订阅时使用保存的 cursor（已完成基础版，需验证所有场景）
8. **WebSocket Session 访问权限校验**：订阅前校验 session/workDir/token 权限
9. **RunManager.Cancel 不能取消只有持久化记录的 Run**：区分 Run exists but not attached / attached and cancellable / already terminal
10. **RunManager subscriber channel 关闭语义**：Subscription 应封装 Close()，WebSocket 连接关闭时停止对应 forwarding goroutine

#### P2 - 重要：完善性和健壮性

11. **Run 创建记录不完整**：当前不写 model/mode，需在 mode/model 解析完成后创建 Run
12. **`runManager` 启动恢复机制**：serve 重启后扫描孤儿 Run 并收敛为 failed/cancelled
13. **`POST /api/runs/{id}/cancel` 路径解析**：应使用明确的 path segments 解析
14. **运行状态双事实来源**：APISession / session_runs / RunManager 三套状态可能不一致，需确定唯一事实来源为持久化 session_runs
15. **前端完整验收测试**：自动重连、多 Session subscription、cursor 保存与 replay、页面刷新恢复 running 状态等

## 1. 目标与原则

本方案将 WebUI 从“HTTP 请求承载 Agent 执行”改造成“服务端独立 Run + 可断开重连的 WebSocket 订阅”。改造完成后：

- 关闭网页、刷新页面、切换路由或 WebSocket 断开，不会停止后台 Agent Run；
- 用户明确点击“停止”才会取消指定的 Run；
- WebUI 打开后通过一个全局 WebSocket 同步多个 Session 的状态；
- WebSocket 断线期间产生的事件可以依靠 SQLite 游标完整恢复；
- OpenAI-compatible API、WebUI、未来其他入口共享同一个后台 Run 执行核心；
- Session 表示持久化对话身份，Run 表示一次执行，Connection/Subscription 表示观察关系，三者绝不互相替代；
- 任何实时广播失败都不能影响 Agent 执行和事件持久化；
- 所有关键状态均有明确的持久化来源，内存只作为运行时缓存。

本方案是完整替换方案，不采用“先只改前端多流、以后再解耦”的渐进路径。实现时应直接按最终架构改造，避免保留两套互相竞争的任务生命周期。

## 2. 当前问题与必须移除的耦合

当前聊天入口在 `internal/serve/openaiapi/handler_chat.go` 内同时完成：

1. 解析请求；
2. 获取或创建 `APISession`；
3. 创建 Agent；
4. 以 `r.Context()` 派生 Agent context；
5. 消费 Agent event channel；
6. 向当前 HTTP 响应写 OpenAI SSE；
7. 发布 transcript、tool 和 runtime 事件；
8. 在 handler 返回时结束 Run。

其中最危险的耦合是：

```go
ctx, cancel := context.WithTimeout(r.Context(), timeout)
```

以及：

```go
for ev := range eventCh {
    // 直接写 http.ResponseWriter
}
```

这会导致浏览器关闭或请求连接断开时，Agent 被错误取消；同时，事件消费和 HTTP 输出绑定，后台任务无法在没有浏览器的情况下继续运行。

必须移除以下关系：

```text
HTTP request 生命周期 = Run 生命周期
HTTP response writer = Agent event 消费者
SSE subscriber = Run owner
APISession.running = 完整 Run 状态来源
前端 AbortController = 服务端 Run cancel
sessionStreamHub = 事件事实来源
```

目标关系应为：

```text
Session       = 持久化会话身份与历史
Run           = 独立后台 Agent 执行
EventLog      = SQLite 中的有序事实事件
RunManager    = Run 生命周期与执行资源管理器
WebSocket     = 可断开、可重连的观察连接
Subscription  = Connection 对 Session/Run 的观察关系
HTTP request  = 创建、查询或控制 Run 的短请求
```

## 3. 最终领域模型

### 3.1 Session

Session 继续由 `internal/session` 管理，唯一职责是：

- session ID、工作目录和持久化 header；
- 对话消息和 replay state；
- session capability/runtime 配置；
- session 级别的历史事件查询。

Session 不保存某个浏览器连接，也不以某个 HTTP 请求是否存在来决定是否运行。

### 3.2 Run

新增服务端 Run 实体，表示一次用户消息或命令触发的 Agent 执行：

```go
type Run struct {
    ID          string
    SessionID   string
    WorkDir     string
    Status      RunStatus
    Source      string
    Model       string
    Mode        string
    StartedAt   time.Time
    UpdatedAt   time.Time
    FinishedAt  *time.Time
    Error       string

    // 仅内存运行资源
    Agent       *agent.Agent
    Context     context.Context
    Cancel      context.CancelFunc
}
```

Run 状态固定为：

```text
created
  -> queued
  -> running
  -> cancelling
  -> terminalizing
  -> completed | failed | cancelled
```

状态约束：

- 同一 Session 同时最多一个 `queued/running/cancelling/terminalizing` Run；
- `cancelling` 和 `terminalizing` 都不允许新审批进入 pending；
- 终态不可逆；
- Run 终态确认后才释放 session runtime lock；
- 浏览器连接断开不改变 Run 状态；
- 服务进程关闭时，无法继续执行的 Run 必须被标记为 `failed` 或 `cancelled`，不能遗留为永久 `running`。

### 3.3 Subscription

Subscription 只描述观察关系：

```go
type Subscription struct {
    ConnectionID string
    SessionID    string
    RunID        string
    EntrySeq     int64
    RunSeq       int64
    CapabilitySeq int64
}
```

Subscription 断开时只删除订阅，不取消 Run。用户点击停止时必须调用明确的 Run cancel API。

### 3.4 Connection

一个 WebSocket 连接可以订阅多个 Session，连接关闭时统一释放所有 Subscription。前端应用只建立一个全局 WebSocket，不由每个 Chat 组件创建独立连接。

## 4. 服务端组件设计

新增目录建议：

```text
internal/serve/openaiapi/run_manager.go
internal/serve/openaiapi/run_store.go
internal/serve/openaiapi/run_events.go
internal/serve/openaiapi/websocket.go
internal/serve/openaiapi/websocket_protocol.go
```

### 4.1 RunManager

`RunManager` 是唯一的 Run 生命周期入口：

```go
type RunManager struct {
    mu       sync.RWMutex
    runs     map[string]*Run
    activeBySession map[string]string
    executor *RunExecutor
    store    RunStore
    broker   *EventBroker
}
```

提供以下能力：

```go
CreateRun(ctx context.Context, req CreateRunRequest) (*Run, error)
StartRun(runID string) error
GetRun(runID string) (*Run, error)
GetActiveRun(sessionID string) (*Run, error)
ListRuns(sessionID string, opts RunListOptions) ([]RunInfo, error)
CancelRun(runID, actor string) error
CancelActiveRun(sessionID, actor string) error
Subscribe(sessionID string, cursor EventCursor) (*Subscription, error)
```

`CreateRun` 只做校验、创建持久化 Run 记录和建立 active ownership，不执行 Agent。`StartRun` 启动独立 goroutine。这样 HTTP handler 可以在提交成功后立即返回。

### 4.2 RunExecutor

`RunExecutor` 负责完整消费 Agent 事件：

```text
Create Run
  -> load Session replay state
  -> build Agent
  -> create context.Background() + timeout
  -> attach Agent/cancel to Run
  -> RunWithUserMessage
  -> consume every agent.Event
  -> persist normalized event
  -> publish event after persistence
  -> finalize Run exactly once
```

Run context 必须从 `context.Background()` 派生：

```go
runCtx, cancel := context.WithTimeout(context.Background(), timeout)
```

HTTP 请求的 `r.Context()` 只用于：

- 校验请求是否已经结束；
- 在创建 Run 前取消尚未提交的请求；
- 记录提交请求的来源。

一旦 Run 进入 `queued` 或 `running`，请求断开不得传递取消信号。

### 4.3 事件处理管线

Agent 事件必须经过统一处理函数：

```go
func (e *RunExecutor) handleEvent(run *Run, ev agent.Event) error {
    normalized := normalizeAgentEvent(run, ev)
    if err := e.store.AppendEvent(normalized); err != nil {
        return err
    }
    e.broker.Publish(normalized)
    return nil
}
```

顺序必须是：

```text
Agent event
  -> 分配持久化 seq
  -> SQLite commit
  -> broker publish
  -> WebSocket subscriber
```

不得先广播后写库，否则断线重连时会出现客户端看到了但数据库没有的事件。

广播失败、没有订阅者、单个订阅者发送队列溢出，都不能阻塞或终止 Run。发送队列溢出时应关闭该订阅连接或标记为需要 replay，不能静默丢失事件。

### 4.4 Run finalizer

所有结束路径必须调用同一个幂等 finalizer：

```go
FinalizeRun(runID string, status RunStatus, errMsg string) error
```

finalizer 必须完成：

1. 将 Run 写入终态；
2. 保存结束时间、错误和 usage；
3. 清理该 Run 的 pending approvals；
4. 记录 approval 终态事件；
5. 清除 `activeBySession`；
6. 释放 session runtime lock；
7. 发布最终 runtime snapshot；
8. 持久化 `run finished/failed/cancelled` 事件；
9. 发布 `run_done`；
10. 关闭 Run 的内存 event channel；
11. 释放 Agent、MCP、sub-agent 等运行资源。

finalizer 必须通过 `sync.Once` 或数据库条件更新保证重复调用不会产生重复终态事件。

## 5. 持久化设计

### 5.1 迁移要求

所有 schema 改动必须在 `internal/session/migrations.go` 的 `migrations` slice 中追加迁移，禁止业务路径直接使用新的 `CREATE TABLE IF NOT EXISTS`。

建议追加以下表：

```sql
CREATE TABLE session_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    work_dir TEXT NOT NULL,
    source TEXT NOT NULL,
    model TEXT,
    mode TEXT,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT,
    error TEXT,
    usage_json TEXT,
    FOREIGN KEY(session_id) REFERENCES sessions(id)
);

CREATE UNIQUE INDEX session_runs_active_session
ON session_runs(session_id)
WHERE status IN ('created', 'queued', 'running', 'cancelling', 'terminalizing');
```

现有 `session_run_events` 可以继续使用，但必须保证：

- 每个事件关联 `session_id` 和 `run_id`；
- 每个 Session 的 run event 有单调递增 `seq`；
- `started`、`finished`、`failed`、`cancelled`、`approval_requested`、`approval_resolved` 都持久化；
- 事件数据包含稳定的 `eventType`、`runId` 和 timestamp；
- terminal event 不依赖内存 hub。

如果现有表无法表达全部字段，应通过迁移补充列或建立独立 `run_events` 表。不得让 WebSocket 事件成为唯一的状态来源。

### 5.2 审批持久化

审批完整归属键：

```text
sessionID + runID + approvalID
```

每个审批至少持久化：

```text
session_id
run_id
approval_id
event_type
status
requested_at
resolved_at
request_json
action
message
actor
```

Run 终止时，所有 pending approval 必须变成 `cancelled`。之后旧 approval 的响应必须失败，不能复活 Agent 或继续执行工具。

### 5.3 事件游标

继续支持三类游标：

```go
type EventCursor struct {
    EntrySeq       int64
    RunSeq         int64
    CapabilitySeq  int64
}
```

所有事件必须包含：

```json
{
  "sessionId": "sess-1",
  "runId": "run-1",
  "seq": 42,
  "eventType": "tool_event",
  "timestamp": "...",
  "data": {}
}
```

WebSocket 客户端检测到 seq gap 时必须发送 replay 请求，服务端从 SQLite 补发，而不是依赖内存缓存。

## 6. API 设计

### 6.1 创建 Run

新增 WebUI 专用接口：

```http
POST /api/sessions/{sessionID}/runs
```

请求：

```json
{
  "message": "请检查项目测试",
  "model": "gpt-5.6",
  "mode": "agent",
  "tools": [],
  "skills": [],
  "images": [],
  "transcript": true
}
```

响应：

```json
{
  "sessionId": "sess-1",
  "runId": "run-1",
  "status": "queued"
}
```

该接口必须在 Run 成功持久化后返回，不等待 Agent 完成。

### 6.2 OpenAI API 兼容

`POST /v1/chat/completions` 仍然保留，但内部必须调用同一个 `RunManager.CreateRun`。

当 `stream=false`：

- 外部客户端可以选择等待 Run 完成并返回最终 OpenAI response；
- 等待只属于该客户端的 response adapter，不属于 Run 生命周期；
- 客户端断开后 Run 继续执行。

当 `stream=true`：

- 创建后台 Run；
- 当前 HTTP handler 订阅 Run；
- 只负责将事件转换为 OpenAI SSE；
- HTTP 断开只取消该订阅；
- Run 继续执行。

如需要保留旧客户端的“断开即取消”行为，应改成显式请求参数或独立 cancel 选项，不能隐式依赖 HTTP context。

### 6.3 查询 Session runtime

```http
GET /api/sessions/{sessionID}/runtime
```

返回：

```json
{
  "sessionId": "sess-1",
  "mode": "agent",
  "workDir": "/project",
  "running": true,
  "activeRun": {
    "runId": "run-1",
    "status": "running",
    "startedAt": "...",
    "source": "webui"
  },
  "pendingApprovals": [],
  "capabilities": {}
}
```

`running` 必须由 RunManager/持久化 active Run 推导，不能只读取 `APISession.running`。

### 6.4 Run 查询与取消

```http
GET  /api/runs/{runID}
POST /api/runs/{runID}/cancel
GET  /api/sessions/{sessionID}/runs
```

取消响应只表示服务端接受取消请求：

```json
{
  "runId": "run-1",
  "status": "cancelling"
}
```

最终状态通过 WebSocket `run_state`/`run_done` 同步，或通过查询接口确认。

兼容接口：

```http
POST /api/sessions/{sessionID}/stop
```

内部必须解析 active Run 后调用 `CancelRun`，不能再直接依赖浏览器请求 Abort。

### 6.5 WebSocket

新增：

```http
GET /api/ws
```

连接建立后客户端发送：

```json
{
  "type": "hello",
  "clientId": "webui-tab-1",
  "protocol": 1
}
```

订阅：

```json
{
  "type": "subscribe",
  "subscriptions": [
    {
      "sessionId": "sess-1",
      "cursor": {
        "entrySeq": 42,
        "runSeq": 18,
        "capabilitySeq": 3
      }
    }
  ]
}
```

取消订阅：

```json
{
  "type": "unsubscribe",
  "sessionIds": ["sess-1"]
}
```

补发：

```json
{
  "type": "replay",
  "sessionId": "sess-1",
  "cursor": {
    "entrySeq": 42,
    "runSeq": 18,
    "capabilitySeq": 3
  }
}
```

服务端事件：

```json
{
  "type": "session_event",
  "sessionId": "sess-1",
  "runId": "run-1",
  "stream": "run",
  "seq": 19,
  "event": "tool_event",
  "data": {}
}
```

运行状态：

```json
{
  "type": "run_state",
  "sessionId": "sess-1",
  "runId": "run-1",
  "status": "running",
  "active": true
}
```

连接维护：

- 服务端发送 heartbeat；
- 客户端回应 ping/pong；
- 每个事件包含 sessionID、runID、seq；
- 单个连接异常不影响 Run；
- 订阅者发送队列满时连接收到 `resync_required` 并关闭或暂停推送；
- 重连后客户端必须按最后 cursor replay；
- 服务端必须先完成 replay，再切换到实时 broker，期间不能丢事件。

## 7. WebSocket replay 与实时广播一致性

订阅流程必须保证以下顺序：

```text
1. 注册订阅并记录当前 broker 边界
2. 从 SQLite replay cursor 之后的事件
3. replay 期间接收的事件按 seq 去重
4. replay 完成后切换实时发送
5. 发现 seq gap 时发送 resync_required
```

推荐实现方式是 broker 为每个 Session 维护一个递增内存边界，但 SQLite seq 是最终权威。客户端收到重复事件必须按 `(stream, seq)` 去重。

不允许使用当前实现中的静默丢弃：

```go
default:
    // drop
```

替代策略：

```text
队列未满 -> 写入
队列已满 -> 标记 connection dirty
          -> 发送 resync_required
          -> 关闭该订阅/连接
```

## 8. WebUI 前端重构

### 8.1 全局 WebSocket Store

新增：

```text
ui/src/lib/ws.js
ui/src/lib/session-runs.js
```

`ws.js` 负责：

- 单例 WebSocket；
- 自动重连；
- heartbeat；
- hello/subscribe/unsubscribe；
- 连接状态；
- 将事件分发到 `session-runs.js`。

`session-runs.js` 负责：

- 每个 Session 的事件 cursor；
- run 状态；
- messages/tool/run/capability/approval 数据；
- gap 检测；
- replay 请求；
- 当前 UI 是否正在查看某 Session。

### 8.2 Chat.svelte 的职责变化

`Chat.svelte` 不再：

- 持有 Agent 执行的 fetch stream；
- 用 AbortController 表示服务端 Run；
- 在 onDestroy 中 abort Run；
- 为当前 Session 建立独立 SSE observer。

`Chat.svelte` 只负责：

- 调用创建 Run API；
- 调用 cancel Run API；
- 从 session store 读取状态；
- 渲染当前 Session；
- 将输入、审批和控制操作提交到服务端。

关闭页面时：

```text
销毁组件 -> 关闭/移除 WebSocket subscription
         -> 不调用 cancel Run
```

用户点击 Stop 时：

```text
POST /api/runs/{runID}/cancel
```

### 8.3 前端权威状态

页面刷新后不得依赖旧的 Svelte 内存状态：

```text
1. 拉取 sessions
2. 拉取每个 active session 的 runtime
3. 建立 WebSocket
4. 以本地 cursor 订阅并 replay
5. 以服务端事件覆盖本地 optimistic 状态
```

本地 optimistic 状态只允许在服务端确认前短暂显示，不能覆盖服务端 terminal 状态。

## 9. SessionPool 与资源生命周期

SessionPool 继续缓存 Session runtime，但不能决定 Run 是否存在。idle eviction 必须检查：

```text
RunManager.HasActiveRun(sessionID) == false
pending approval == 0
session pin == 0
MCP/sub-agent 等资源已释放
超过 idle TTL
```

Run 运行期间：

- session 必须 pin；
- pool 不得驱逐；
- WebSocket 断开不 unpin Run；
- Run 终态 finalizer 才释放 pin。

如果服务重启：

1. 启动时扫描非终态 Run；
2. 如果没有可恢复的执行实体，标记为 `failed`，错误为 `server restarted`；
3. 清理 pending approvals；
4. 发布/保存 terminal event；
5. WebUI 重新连接后可看到明确终态。

本方案默认不实现跨进程 Agent 恢复；“后台继续运行”指 WebUI 连接断开后在同一 serve 进程内继续运行。

## 10. 绑定与 session ID 规则

WebUI Session 的 `sessionID` 必须始终是数据库中的 canonical session ID，不得使用：

```text
channels/{platform}/{userID}
```

作为 WebUI session ID，也不得使用 workDir + ID 的 pool key 作为外部 ID。

所有 API、Run、事件和 WebSocket subscription 使用 canonical session ID。Session 的 channel binding 只作为 metadata：

```json
{
  "sessionId": "canonical-id",
  "channelType": "wechat",
  "channelId": "user-id"
}
```

绑定变更、转移、解绑后：

- 清理 channel dispatcher 缓存；
- 更新 session runtime；
- 发布 binding/runtime event；
- 不改变历史 Run 的 sessionID；
- 新消息按数据库最新 binding 路由。

WebUI 打开或刷新时，应以 `/api/sessions` 返回的 canonical ID 建立订阅，不从本地路由键猜测 session。

## 11. 审批与停止

停止 Run 时必须：

1. 将 Run 原子标记为 `cancelling`；
2. 对 Agent 调用 `Abort()`；
3. 调用 Run context cancel；
4. 清理该 Run 已登记的 approvals；
5. 给审批等待返回不可继续结果；
6. 写入 `approval_resolved(status=cancelled)`；
7. 发布 runtime/approval event；
8. finalizer 将 Run 收敛到 `cancelled`。

`registerSessionApproval` 在登记前必须检查 Run 状态。若状态不是 `running`，不得加入 pending，直接写取消审计。

审批响应接口必须校验：

```text
sessionID
runID
approvalID
当前 Run 仍为 running
approval 仍为 pending
```

旧 Run 的 approval 即使 ID 相同，也不能被新 Run 响应。

## 12. 文件与代码改造范围

后端：

```text
internal/serve/openaiapi/run_manager.go       新增 Run 生命周期
internal/serve/openaiapi/run_store.go         新增 Run 持久化
internal/serve/openaiapi/run_events.go        统一事件写入/规范化
internal/serve/openaiapi/websocket.go         WebSocket 连接管理
internal/serve/openaiapi/websocket_protocol.go 协议类型与校验
internal/serve/openaiapi/handler_chat.go       改为创建 Run/订阅 adapter
internal/serve/openaiapi/session_mgr.go        移除 Run 事实状态耦合
internal/serve/openaiapi/session_stream.go     改为兼容 adapter 或删除重复实现
internal/serve/openaiapi/approval.go           绑定 RunID 和新 finalizer
internal/serve/openaiapi/events.go             使用统一事件序列
internal/serve/run.go                          注册 /api/ws、Run API
internal/session/migrations.go                 追加 schema migration
internal/session/*                             Run/event 查询和事务接口
```

前端：

```text
ui/src/lib/ws.js                            新增全局 WS
ui/src/lib/session-runs.js                  改为服务端 Run 状态 store
ui/src/lib/api.js                            增加 Run/WS helpers
ui/src/views/Chat.svelte                     移除 completion SSE 生命周期
ui/src/lib/stores.js                         接入 runtime/run store
ui/src/App.svelte                            管理全局 WS 生命周期
ui/src/components/Sidebar.svelte             显示 running/approval 状态
```

当前 SSE session stream 可以保留为兼容接口，但内部必须复用 `RunManager.Subscribe` 和统一 EventBroker，不得继续保留另一套事件生成逻辑。WebUI 主路径改用 WebSocket。

## 13. 安全要求

- WebSocket 必须复用现有 Bearer auth/CORS 安全策略；
- 订阅 Session 前校验该 Session 的工作目录访问权限；
- 只能订阅当前 API token 有权限访问的 Session；
- cancel Run 必须校验 session/run 所属关系；
- 不允许客户端伪造 sessionID、runID 或 seq；
- 服务端忽略客户端提交的 event 状态，只接受 cursor；
- WebSocket 消息大小、订阅 Session 数量、连接数量和发送队列必须有上限；
- 连接断开、重连、replay 不得泄露其他工作目录的事件；
- 事件中的 tool 参数、文件路径和错误信息遵循现有 API 脱敏/权限策略。

## 14. 验收标准

### 14.1 后台执行

1. 提交 Run 后立即关闭网页，Agent 仍继续执行；
2. 任务结束后 SQLite 中存在完整消息、tool、run terminal event；
3. 重新打开网页可以看到最终结果；
4. 页面刷新不会创建第二个 Run；
5. WebSocket 断开不会改变 Run 状态；
6. 用户点击 Stop 才会取消 Run；
7. 取消后同一 Session 可以开始下一轮。

### 14.2 WebSocket 同步

1. 一个连接可以订阅多个 Session；
2. 切换 Session 不影响其他 Session 的后台 Run；
3. 多标签页可以同时订阅同一 Session；
4. 断线重连使用 cursor replay；
5. 事件不会重复渲染；
6. broker 队列溢出会触发 resync，而不是静默丢事件；
7. run、tool、transcript、approval、capability 事件都有 sessionID/runID；
8. replay 与实时切换无竞态，无事件丢失。

### 14.3 状态一致性

1. Session 列表的 running 状态与 RunManager/DB 一致；
2. runtime snapshot 有明确 activeRunID；
3. Run 终态后不再显示 Stop；
4. pending approval 只属于当前 active Run；
5. 旧 Run approval 无法影响新 Run；
6. serve 重启后旧 running Run 被明确收敛，不遗留假运行状态；
7. channel binding 变更后 WebUI 使用 canonical session ID，缓存被刷新。

### 14.4 测试

后端必须覆盖：

- request disconnect 不取消 Run；
- WebSocket disconnect 不取消 Run；
- Run cancel 只取消目标 Run；
- 同一 Session 并发创建 Run 的唯一性；
- 多 Session 并行；
- event persist-before-publish；
- replay 与 live publish 竞态；
- seq gap/resync；
- approval stop race；
- server restart cleanup；
- pool eviction 不回收 active Run；
- canonical session/binding 路由。

前端必须覆盖：

- WebSocket 自动重连；
- 多 Session subscription；
- cursor 保存与 replay；
- 页面刷新恢复 running 状态；
- 关闭组件不 cancel Run；
- Stop 调用 Run cancel API；
- 重复事件去重；
- seq gap 触发 replay；
- approval/runtime/run terminal 状态正确收敛。

## 15. 完成定义

只有以下条件全部满足，才认为本次改造完成：

- `handleChatCompletions` 不再拥有 Agent 的主要执行生命周期；
- Agent context 不再从 HTTP request context 派生；
- RunManager 是所有 WebUI/OpenAI Run 的唯一执行入口；
- Run 状态和 terminal event 已持久化；
- WebSocket 是 WebUI 主同步通道；
- WebUI 关闭、刷新、路由切换不会取消 Run；
- Stop 使用显式 Run cancel API；
- 断线重连通过 SQLite cursor 完整恢复；
- SSE（如保留）只是统一订阅层的适配器；
- Session ID、Run ID、Subscription ID 三者职责清晰；
- 所有相关测试和 `go test ./...`、前端构建均通过；
- 不保留旧的“POST 请求断开即隐式取消后台任务”行为。
