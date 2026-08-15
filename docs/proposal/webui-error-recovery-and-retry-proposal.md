# WebUI 错误友好展示与统一重试方案

> 状态：Proposal
> 日期：2026-08-16
> 目标：建立一套由 Agent Core/Agent Runtime 负责决策、由 WebUI/TUI/ACP/Channel 负责协议投影的错误、执行中断、自动重试和用户恢复机制。
> 关联方案：[统一 Agent 核心与多入口 Runtime](./agent-core-runtime-unification-proposal.md)、[WebUI 后台 Run 与 WebSocket 同步](./webui-background-run-websocket-architecture-proposal.md)、[WebUI 会话停止与审批生命周期](./webui-session-stop-and-approval-lifecycle.md)

## 0. 架构决议

本方案遵循一个明确前提：TUI、ACP、Channel、WebUI 都是 Agent 的薄适配层，运行在同一个 Agent Core 和前端中立的 Agent Runtime 之上。错误分类、重试安全判断、原始请求保存、执行中断恢复和 Run 终态收敛都属于共享内核职责，不能由 WebUI 单独实现。

“重试”与“断点继续”不定义成两套互相竞争的机制。它们都由 Runtime 根据同一个原始执行请求和当前执行事实决定：

- Agent Core 在 provider 请求、上下文恢复、空响应、输出截断等安全场景中自动重试；
- Runtime 依据工具执行记录、审批状态和副作用事实决定是否可以继续或重新尝试；
- 用户在任意入口触发的“重试”只是向 Runtime 提交一个重试命令，不能在适配层重新拼装 prompt 或复制一套状态机；
- WebUI 只展示 Runtime 发出的状态、错误和重试进度，并投影一个薄的重试按钮；
- 如果执行事实表明重放可能重复外部副作用，Runtime 必须拒绝自动重放或要求显式决策，适配层不得绕过该限制。

因此，本提案不是“给 WebUI 增加一个按钮”，而是把现有 Agent Core 已有的重试语义完整投影到统一 Runtime，再为所有入口提供一致的友好错误和恢复能力。

## 1. 目标

完成后应满足以下目标：

1. 所有入口使用相同的错误分类、重试条件、原始请求和 Run 生命周期。
2. 后端错误和 Agent 执行中断都能产生稳定的机器字段和安全的用户文案。
3. WebUI 能明确区分成功、自动重试中、取消、超时、失败、不完整和传输状态未知。
4. 自动重试期间，WebUI 能显示尝试次数、等待时间和原因，不把中间重试误报为最终失败。
5. 用户点击重试时，Runtime 使用服务端保存的原始请求快照创建新尝试，不重复追加用户消息，不依赖浏览器仍然保存的表单状态。
6. HTTP 响应丢失、WebSocket 断开、SSE 中断或页面刷新都不会导致重复 Run，也不会让 UI 永久处于 loading。
7. 已产生工具副作用的 Run 不会被适配层盲目重放；旧 Run 的审批、工具和终态不会被新尝试错误复用。
8. 错误事件可以通过 WebSocket、SSE、历史 replay 和 GET /api/runs/{id} 得到同一语义。
9. 错误详情不会直接泄露 provider 堆栈、凭据、完整命令参数或内部路径。

## 2. 非目标

- 不在 WebUI 中实现第二套 Agent loop、RetryManager、Run 状态机或工具恢复逻辑。
- 不让 WebUI 直接调用 agent.New、构造完整 agent.Config 或绕过 SessionRuntime。
- 不把浏览器重新发送同一段 prompt 作为重试的权威实现。
- 不把所有失败都自动重试；认证、权限、参数、策略和副作用不确定的失败必须遵循 Runtime 的安全决策。
- 不把失败渲染成一条永久的 assistant 对话消息，避免污染正常会话上下文。
- 不改变现有 settings.json、serve.json 字段语义；重试配置继续由共享配置和 Agent Core 使用。
- 不在本方案中实现跨进程、跨机器的任意执行恢复；恢复必须以当前 Runtime 能验证的持久化事实为基础。

## 3. 当前基线与问题

### 3.1 WebUI 提交和显示

当前 WebUI 聊天入口位于 ui/src/views/Chat.svelte，主要问题是：

- sendPrompt() 使用局部 fetch，与 ui/src/lib/api.js 重复解析错误；
- 普通 Error 丢失 HTTP status、error.type/code、Retry-After、runId 和请求标识；
- WebUI 没有发送后端已经支持的 Idempotency-Key；
- 202 提交响应中的 runId 没有作为每个 Run 的事实状态保存；
- streamHadError 等组件级变量不能表达多个 Run 的独立状态；
- ui/src/lib/session-view.js 生成临时 assistant 错误块，但刷新历史后错误可能消失，也没有重试候选信息；
- 某些 failed Run 事件只更新 transcript，不更新 lastError 或会话级运行态；
- 全局 Banner 只有一个字符串槽位，WebSocket 订阅错误和历史加载错误有时只写日志或返回空数组。

### 3.2 后端和 Agent Core

后端已经具备重要基础：

- POST /api/sessions/{sessionID}/runs 已有 202 队列响应、幂等键、Run 持久化和共享 SessionRuntime.BuildAgent；
- ErrorResponse 已有 message、type、code；
- ExecutionRuntime/RunStore 已是 durable Run 生命周期的权威边界；
- Agent Core 已有 provider 超时、上下文超限、空响应、输出截断和 Responses lineage fallback 的自动重试；
- agent.EventRetry、agent.EventStatus、EventRunFinished 已携带部分重试和终态信息；
- TUI 和 Channel 已经消费部分重试事件，说明语义应继续下沉到共享内核，而不是为 WebUI 另造一套。

当前主要缺口是：

1. 当前 Serve RunExecutor 对普通 EventRetry 只做忽略，对普通 EventStatus 没有统一投影；其中的生命周期和重试决策最终必须下沉到 ExecutionRuntime，不能把 Serve RunExecutor 固化成第二个 Runtime。
2. 错误字段没有统一的 failureClass、阶段、副作用状态、可重试模式和用户文案 key。
3. Run 没有保存可供所有入口复用的原始执行请求快照。
4. 传输层无法在“请求已接受但响应丢失”时可靠收敛到已有 Run。
5. WebUI 无法从历史 replay 恢复完整的失败、重试和终态展示。

## 4. 核心模型

### 4.1 ExecutionIntent：不可变的原始执行请求

Runtime 在 Run admission 时创建一个不可变的 ExecutionIntent。它不是浏览器 request body 的简单复制，而是经过 source、mode、capability、tool、sandbox、approval/question 和 run policy 解析后的权威执行意图。

建议模型如下，字段名可按现有 Go 类型调整：

~~~go
type ExecutionIntent struct {
    ID                 string
    SessionID          string
    Source             agentruntime.RuntimeSource
    UserMessageRef     string // 指向 session 中的原始用户消息/附件记录
    ModelID            string
    EffectiveMode      string
    Tools              []string
    Skills             []string
    WorkDir            string
    Transcript         bool
    RequestFingerprint string
    RetryPolicy        RetryPolicySnapshot
    PolicySnapshot     ExecutionPolicySnapshot
    CreatedAt          time.Time
}
~~~

约束：

- UI 不负责创建或修改 ExecutionIntent；
- user message、图片和附件优先引用已有 Session 持久化记录，不在浏览器重试时重新拼装；
- model、effective mode、source、tools、skills、workDir 和安全策略在 admission 时记录快照；
- 自动重试使用同一个 intentID 和当前 Run 的 attempt；
- 用户触发的新尝试使用同一个 intentID，但创建新的 runID 和 attempt，并通过 retryOf 关联旧 Run；
- 如果当前 Session binding、sandbox 或高风险策略与原始快照冲突，Runtime 必须拒绝重试并返回新的策略错误，不能静默采用更宽松配置；
- 浏览器刷新、切换 Session 或 WebSocket 断开不影响 Intent 的持久化。

### 4.2 Run 与 Attempt

一个 Run 表示一次执行生命周期，一个 Intent 可以有多个有序 attempt：

~~~text
ExecutionIntent
  ├── Run A / attempt 1
  ├── Run B / attempt 2 (retryOf=A)
  └── Run C / attempt 3 (retryOf=B)
~~~

终态 Run 不被“复活”。重试必须创建新的 Run，旧 Run 保持原始终态和错误审计。自动 provider/turn retry 属于同一个 Run 的内部 attempt，不创建新的用户可见 Run，也不重复用户消息。

### 4.3 ErrorInfo：统一错误语义

所有入口共享同一组错误字段，适配层可以使用不同的协议格式，但不能改变语义：

~~~go
type ErrorInfo struct {
    Code            string
    Type            string
    FailureClass    string // validation, policy, transient, provider, tool, transport, canceled, incomplete, internal
    Phase           string // admission, model, context, tool, approval, persistence, transport, terminalization
    MessageKey      string // 供适配层本地化
    Message         string // 安全的 fallback 文案
    RetryMode       string // none, automatic, reconcile, user, decision_required
    Retryable       bool
    RetryAfterMs    int
    Attempt         int
    MaxAttempts     int
    SideEffectState string // none, read_only, mutating, unknown
    PartialOutput   bool
    RunID           string
    IntentID        string
    RequestID       string
}
~~~

Message 必须是可直接展示的安全 fallback，不包含堆栈、token、完整命令参数和内部路径。技术错误保留在服务端日志，前端如需“查看详情”只显示经过脱敏的诊断摘要和 RequestID。

Retryable 不能单独决定 UI 是否显示按钮。真正的用户动作由 RetryMode、SideEffectState、Run 终态和当前 Session policy 共同决定。

## 5. 错误与重试分类

| 类别 | 典型 code | 用户主文案方向 | Core 行为 | WebUI 操作 |
|---|---|---|---|---|
| 请求校验 | invalid_request、invalid_image | 输入或模型配置有误 | 不重试 | 指向字段或配置 |
| 认证/权限/策略 | unauthorized、permission_denied、policy_conflict | 当前凭据或策略不允许 | 不重试 | 登录、修正设置或联系管理员 |
| Session 并发 | session_run_active | 会话正在执行 | 不创建第二个 Run | 显示当前 Run，提供停止/等待 |
| Provider 临时故障 | provider_timeout、provider_5xx、rate_limited | 服务暂时不可用 | 按共享 RetryConfig 自动重试 | 显示尝试进度；耗尽后允许 Runtime 重试 |
| 网络/流中断 | provider_stream_interrupted、network_unavailable | 连接暂时中断 | 仅在 Core 判定安全时自动重试 | 断线重连和 Run 状态查询，不盲目新建 Run |
| 上下文压力 | context_overflow | 上下文过长 | Core 压缩/裁剪后按原始 Intent 重试 | 显示“正在整理上下文” |
| 输出截断/空响应 | output_truncated、empty_response | 模型没有完整返回 | Core 按现有恢复规则重试或继续 | 显示恢复状态，最终失败时给出建议 |
| 工具错误 | tool_failed | 工具执行失败 | 按工具执行记录和副作用策略决定 | 展示工具失败详情；不可安全重放时不显示盲重试 |
| 审批/问题等待 | approval_expired、decision_cancelled | 等待的操作已失效 | 由 DecisionService 收敛，不重放旧决策 | 提示重新发起请求 |
| 用户取消 | run_cancelled | 已取消 | 终止 Run，不重试 | 中性状态，可通过 Runtime 重试原 Intent |
| 超时 | run_timed_out | 执行超时 | Core/Runtime 按阶段决定 | 显示超时原因和安全恢复动作 |
| 持久化/终态错误 | run_persistence_failed | 服务未能确认执行结果 | 保持可恢复状态，禁止伪造成功 | 查询 Run、显示诊断编号 |
| 传输未知 | submission_unknown | 请求结果未知 | 先 reconcile 幂等 Run | 自动查询，不直接重复执行 |

共享 Core 的原则是：provider 请求在没有可见输出、工具调用或外部副作用时可以自动重试；已经产生副作用或副作用状态未知时，是否继续必须由 Core 的执行记录和策略决定。TUI、ACP、Channel、WebUI 不得用自己的字符串判断覆盖该结论。

## 6. 统一执行流程

~~~text
Adapter submit / retry command
        |
        v
SessionRuntime.ResolveSource + ResolvePolicy
        |
        v
ExecutionRuntime.CreateIntent + BeginDurable
        |
        v
Agent Core executes the intent
        |
        +--> EventStatus / EventRetry / tool / approval / question
        |       |
        |       +--> Runtime normalizes semantic state
        |       +--> RunStore + EventSink persist canonical facts
        |       +--> adapter projects TUI/WebUI/ACP/Channel payloads
        |
        +--> terminal outcome
                |
                +--> FinishDurable once
                +--> persist ErrorInfo/usage/context usage
                +--> publish terminal event
                +--> release session/runtime resources
~~~

适配层只能在流程两端参与：提交已解析的场景输入、接收和渲染 Runtime 事件。它不能在中间插入自己的 provider retry、Run persistence、tool recovery 或 terminal finalizer。

## 7. 运行和重试状态语义

### 7.1 Canonical Run 状态

继续使用 internal/agentruntime.ExecutionRuntime 作为唯一生命周期所有者。建议保留以下 durable 状态：

~~~text
created -> queued -> running
running -> waiting_for_approval | waiting_for_question
waiting_* -> running | cancelling
running -> cancelling | terminalizing
terminalizing -> completed | incomplete | failed | cancelled | timed_out
~~~

retrying 是 active Run 的一个可投影进度状态，不应为每一次 provider retry 创建新的用户可见 Run。Runtime 可以在事件和 runtime snapshot 中表达：

~~~json
{
  "status": "running",
  "progress": {
    "phase": "model",
    "state": "retrying",
    "attempt": 2,
    "maxAttempts": 5,
    "retryAfterMs": 3000,
    "reasonCode": "provider_503"
  }
}
~~~

### 7.2 自动重试

自动重试必须发生在 Agent Core/Runtime 内部：

1. Core 发现可重试错误并生成 EventStatus/EventRetry；
2. Runtime 将其规范化为 run_retrying 事件并持久化；
3. Core 使用同一个 ExecutionIntent、同一个 Run 和正确的内部上下文恢复策略重新请求；
4. 成功后清除 retrying 投影并继续正常事件流；
5. 达到上限后只产生一个结构化终态错误，不能重复插入多个 assistant 错误块。

当前 Agent Core 已有的 provider retry、上下文压缩、空响应和输出截断恢复应作为基线迁移和补全，而不是在 WebUI 或 RunExecutor 中复制。

### 7.3 用户触发的重试

用户触发的重试也必须走 Runtime：

~~~http
POST /api/runs/{runID}/retry
~~~

请求只携带必要的控制信息，例如确认标识或客户端 request ID，不携带完整 prompt、tools、skills、images 和工作目录。Runtime：

1. 读取原 Run 关联的 ExecutionIntent；
2. 检查旧 Run 的终态、ErrorInfo、工具副作用和当前 Session policy；
3. 若允许重试，创建新的 runID，设置 retryOf 和递增 attempt；
4. 用原始 Intent 启动新的 Run，不向 Session transcript 重复追加同一个用户消息；
5. 若不允许，返回结构化 decision_required、retry_forbidden 或 policy_conflict，由适配层显示对应动作。

该 API 只是 WebUI/ACP 等协议适配层进入统一 Runtime 的薄入口，不是新的重试状态机。TUI、ACP、Channel 的显式重试也应映射到相同的 Runtime 操作。

### 7.4 提交响应未知时的恢复

初始提交使用幂等键：

1. Adapter 为一次投递生成 Idempotency-Key；
2. POST /api/sessions/{sessionID}/runs 超时或连接断开后，先用同一 key 重发提交或查询幂等记录；
3. 服务端返回已有 runID 时，前端订阅该 Run 并 replay；
4. 只有确认服务端没有接受该 key，才允许重新创建 Run；
5. 用户重试旧的终态 Run 时必须使用新的 attempt key，不能复用初始投递 key。

这条流程解决“HTTP 响应丢失”问题，不等同于重新执行 Agent。

## 8. 后端契约设计

### 8.1 HTTP 错误

在现有 ErrorResponse 上扩展结构化字段，保持 OpenAI-compatible error.message/type/code 兼容：

~~~json
{
  "error": {
    "message": "服务暂时不可用，请稍后重试",
    "type": "provider_error",
    "code": "provider_timeout",
    "failureClass": "transient",
    "phase": "model",
    "retryMode": "automatic",
    "retryable": true,
    "retryAfterMs": 3000,
    "runId": "run-...",
    "intentId": "intent-...",
    "requestId": "req-..."
  }
}
~~~

要求：

- code 稳定、可测试、不可依赖英文错误字符串；
- message 是脱敏 fallback 文案；
- failureClass、phase 和 retryMode 由共享分类器产生；
- Retry-After header 与 retryAfterMs 保持一致；
- 400/403/409 等 preflight 错误也使用同一套字段；
- requestId 用于服务端日志关联，不能包含凭据或用户输入。

### 8.2 Run 事件

WebSocket、SSE 和持久化 replay 使用统一的 EventBroker envelope。现有 type/sessionId/runId/stream/event/seq/data 字段继续保留，data 中采用统一语义：

~~~json
{
  "event": "run_retrying",
  "sessionId": "session-1",
  "runId": "run-1",
  "seq": 42,
  "data": {
    "attempt": 2,
    "maxAttempts": 5,
    "phase": "model",
    "reasonCode": "provider_503",
    "retryAfterMs": 3000,
    "messageKey": "run.retrying.providerUnavailable"
  }
}
~~~

终态事件必须始终包含：

- canonical Run status；
- ErrorInfo（成功时为空）；
- usage/context usage（可用时）；
- 是否有 partial output；
- 工具副作用摘要和 pending decision 清理结果；
- intentID、attempt 和 retryOf（适用时）。

旧的 finished、failed、canceled wire event 可以在迁移期保留，但只能作为 canonical terminal event 的协议别名，不能再由不同入口分别生产不同终态。

### 8.3 Run 查询和重试接口

保留并强化：

~~~http
GET  /api/runs/{runID}
POST /api/runs/{runID}/cancel
~~~

新增：

~~~http
POST /api/runs/{runID}/retry
~~~

GET /api/runs/{runID} 必须返回当前状态、进度、ErrorInfo、attempt 关系和最后确认事件游标。retry 返回新的 runID、intentID、attempt 和 queued 状态，不返回完整原始请求。

## 9. 持久化设计

### 9.1 权威数据归属

- Run 状态、attempt 关系和结构化终态：internal/session、ExecutionRuntime、RunStore；
- 自动重试和状态事件：canonical Run event sink；
- 原始 Intent：Runtime-owned、session-scoped 持久化记录；
- 审批和问题：DecisionService/DecisionRecord；
- 工具执行和副作用：现有 tool execution records；
- WebSocket、SSE 和前端 store：只做投影和缓存。

不得在 WebUI store、APISession 临时 map 或新的 adapter 数据库中保存第二份权威重试状态。

### 9.2 Intent 持久化要求

原始 Intent 应与 started Run event 原子关联。实现可以复用现有 session_run_events.data，也可以通过追加 migration 扩展 session_runs/RunStore；最终必须满足：

- 有稳定 intentID、request fingerprint、attempt 和 retryOf；
- 可引用 Session 中的用户消息和附件，不依赖浏览器 local state；
- 保存有效 mode/source/policy/tool/skill 快照；
- 保存幂等 key 的受控摘要，不保存不必要的明文 token；
- 读取失败时 Run 进入可诊断的 run_persistence_failed，不能静默重建一个不同请求；
- schema 变更只通过 internal/session/migrations.go 追加 migration，禁止业务路径直接建表。

### 9.3 终态错误持久化

Run row 的 Error 字段继续保留兼容摘要，结构化 ErrorInfo 必须同时保存在 canonical terminal event 或结构化 Run metadata 中。任何 adapter 都不能通过解析 Error 字符串推断 retryable。

失败不是 assistant 对话内容：前端可以将 terminal event 投影为错误卡片，但不得把 [Error: ...] 作为下一轮 Agent history 的普通 assistant message。

## 10. WebUI 适配方案

### 10.1 API 层

在 ui/src/lib/api.js 增加 ApiError：

- 保留 HTTP status、error type/code、failure class、retry mode、retry-after、run ID、request ID；
- 所有 JSON 请求统一通过 request()；
- 删除 Chat.svelte 的重复错误解析；
- 区分 HTTP preflight failure、accepted Run 的 terminal failure 和 transport-unknown；
- 对 401/403 支持认证状态恢复，对 429/503 支持等待提示；
- 对提交超时保留 idempotency key，先 reconcile，不直接创建新 Run。

### 10.2 按 Run 管理状态

将当前组件级 streamHadError、单个 lastError 和 busy 改为按 Session/Run 维护：

~~~js
{
  sessionId,
  runId,
  intentId,
  attempt,
  status,
  progress: {
    phase,
    state,
    attempt,
    maxAttempts,
    retryAfterMs
  },
  error: ErrorInfo | null,
  lastConfirmedSeq,
  transport: 'connected' | 'reconnecting' | 'reconciled' | 'unknown'
}
~~~

切换 Session 只切换渲染对象，不清理其他 Session 的 Run 状态。WebSocket、SSE fallback、刷新 replay 都按 sessionId/runId 路由。

### 10.3 友好错误卡片

Run 级错误显示在对应对话或 Run 面板中：

- 标题：由 messageKey 和本地语言映射产生；
- 简短解释：说明发生在哪个阶段；
- 操作：重试、重新加载 Run、登录、修正设置、停止等待中的 Run；
- 技术详情：默认折叠，只展示脱敏摘要和 request ID；
- 状态：自动重试中、已取消、已超时、结果未知等使用不同视觉语义。

全局 Banner 只处理认证、WebSocket 全局断连和服务不可用等跨 Session 问题。Run 级失败不同时插入全局 Banner 和重复 assistant 错误消息。

### 10.4 重试操作

重试按钮只在 Runtime 返回 retryMode=user 或安全的 retryMode=automatic 耗尽后仍允许人工重试时显示。点击后：

1. 调用 POST /api/runs/{runID}/retry；
2. 使用新 Run ID 订阅事件；
3. 把新 attempt 归组到原始用户消息下；
4. 不重复追加用户消息、不复制旧 assistant 文本、不在前端重组 request body；
5. 如果 Runtime 返回 decision_required，显示安全确认或检查工作区动作；
6. 如果 Runtime 返回 retry_forbidden，显示原因和建议，不继续发送请求。

### 10.5 连接和加载恢复

- WS 订阅失败必须进入当前 Session 的可见状态，不能只 console.warn；
- 事件游标丢失或 replay 失败时，先调用 GET /api/runs/{id}，再按最后确认序号补流；
- waitForRunCompletion() 必须有状态查询和超时 fallback，不能无限等待；
- history/runtime/sub-agent API 区分“成功为空”和“加载失败”，保留重试动作；
- 页面刷新后从持久化 Run event 恢复自动重试、失败和取消状态；
- 错误卡片和状态文案同时补充 zh/en 翻译，不把英文 provider 原文作为唯一 UI 文案。

## 11. 其他入口适配

### TUI

TUI 继续渲染 Runtime 的 run_retrying、ErrorInfo 和终态，不在 Bubble Tea model 中判断 provider 字符串。已有 EventRetry 展示逻辑迁移到公共事件语义上，保留终端特有布局和快捷键。

### ACP

ACP 将同一 ErrorInfo 映射到 JSON-RPC error/notification，并在 reconnect 时 replay pending decision 和 Run retry 状态。协议 framing、客户端 capability 和 callback 保留在 ACP adapter。

### Channel

Channel 将同一 retry progress 和 friendly fallback 映射到平台消息/ProgressFunc。平台消息不能自行创建新的 Run 或把临时网络错误转换成重复执行；需要恢复时调用 Runtime 的同一 retry/reconcile 操作。

### WebUI

WebUI 只负责 HTTP、WebSocket、SSE、JSON 和交互渲染。它不能直接依赖 provider 类型、工具名称字符串或本地 retry counter 来决定能否重试。

## 12. 分阶段实施

### Phase 0：契约和架构守卫（P0）

- 在 internal/agentruntime 定义共享 ErrorInfo、ExecutionIntent、RetryPolicySnapshot 和 attempt 关系。
- 明确错误 code/failureClass/phase/retryMode 的稳定枚举。
- 增加架构测试，禁止 WebUI/TUI/ACP/Channel 引入 adapter-owned retry classifier、Run store 或 Agent config assembler。
- 为所有入口建立跨入口 contract test，确保同一模拟错误得到相同的 RetryMode 和终态。

### Phase 1：Core/Runtime 统一重试事件（P0）

- 保留 Agent Core 现有安全重试逻辑，补全统一事件字段。
- ExecutionRuntime 接收并记录原始 Intent，管理 attempt 和 terminalization。
- 将 EventStatus、EventRetry、EventRunFinished 规范化为 canonical Run event。
- 将 RunExecutor 中可共享的事件规范化、重试进度和终态收敛下沉到 ExecutionRuntime；Serve RunExecutor 在迁移期只保留 HTTP/SSE/WebSocket 协议投影和兼容映射，并以有删除条件的迁移桥存在，不能保留自己的生命周期或重试决策。
- 把工具执行副作用、审批和问题等待纳入 RetryMode 决策。

### Phase 2：持久化和后端 API（P0/P1）

- 通过 internal/session/migrations.go 持久化 Intent 引用、attempt、retryOf 和结构化终态错误。
- 统一 ErrorResponse 与 terminal event envelope。
- 提交 API 使用幂等 key，并能在结果未知时返回已有 Run。
- 强化 GET /api/runs/{id} 的进度、错误、attempt 和游标信息。
- 增加 POST /api/runs/{id}/retry，只接收控制命令，不接收完整原始请求。
- 所有终态路径使用 ExecutionRuntime.FinishDurable 或对应 canonical operation，保证一次且可恢复。

### Phase 3：WebUI 友好展示与恢复（P1）

- 增加 ApiError 和统一 request helper。
- 将 Chat 提交、WS、SSE、历史加载统一接入按 Run 的状态 reducer。
- 保存 idempotency key、runID、intentID 和最后确认游标。
- 添加 retrying/failed/canceled/timed_out/unknown 的 UI 状态。
- 添加错误卡片、折叠技术详情、重试/重新加载/认证动作。
- 移除临时错误消息与终态事件重复插入、刷新消失和空数组吞错。

### Phase 4：其他入口收敛（P1）

- TUI、ACP、Channel 改为消费统一 ErrorInfo 和 retry events。
- 删除入口级 retry counter、错误字符串分类和局部 Run retry 分支。
- 补齐跨入口恢复、审批终止、工具副作用和 retry-of 关系测试。

### Phase 5：观测和清理（P2）

- 统计自动重试次数、耗尽原因、人工重试成功率、transport unknown 收敛时间和 retry forbidden 原因。
- 对 provider、tool、policy、persistence failure 分开计数。
- 删除迁移期兼容字段和 adapter 旁路，移除 Serve RunExecutor 的生命周期迁移桥，保留唯一 canonical event/retry path。
- 同步中英文 Serve/配置文档，说明自动重试与安全限制。

## 13. 测试和验收

### 13.1 Agent Core/Runtime

1. provider 429/5xx/网络超时在无可见输出和无工具副作用时按原始 Intent 自动重试。
2. 自动重试事件包含 attempt、maxAttempts、reasonCode、retryAfterMs，并由所有入口得到同一语义。
3. 上下文压缩、空响应、输出截断沿用现有 Core 规则，不重复用户消息。
4. 已执行 mutating tool 或副作用未知时，Runtime 不允许适配层盲目重放。
5. read-only tool 的既有恢复规则继续由 Core/Runtime 决定，不能由 WebUI 复制。
6. cancel、deadline、approval expiry、question cancellation 都能收敛为明确终态并清理 pending Decision。
7. terminal event、Run row、usage/context usage 和 ErrorInfo 幂等写入；重复 finish 不产生第二个终态。
8. Intent 读取失败不会静默使用当前 UI 参数替代。

### 13.2 Serve/API

1. 400/401/403/409/429/502/503/504 的错误字段和友好映射稳定。
2. 提交响应丢失后用同一幂等 key 不会创建重复 Run。
3. GET /api/runs/{id} 能在 WS/SSE 全部断开后恢复终态。
4. POST /api/runs/{id}/retry 创建新 attempt，旧 Run 保持终态，用户消息只出现一次。
5. WS live、SSE fallback、历史 replay 的错误和重试事件完全一致。
6. 未知 Run、越权 Session、已终态审批和冲突 policy 返回结构化错误。

### 13.3 WebUI

1. ApiError 保留 status/type/code/retryMode/retryAfter/runID/requestID。
2. 自动重试显示“第 n/m 次重试”和等待状态，耗尽后只显示一个最终错误块。
3. 取消显示中性状态，超时、失败、不完整和 transport unknown 可区分。
4. 刷新、切换 Session、WS 断线和 SSE 回退不会丢失错误或重复 Run。
5. 重试按钮只调用 Run retry API，不重新拼装 prompt，不重复用户消息。
6. 历史/runtime/sub-agent 加载失败显示错误和重新加载动作，不伪装成空数据。
7. 中英文、移动端布局和认证过期场景均有验收覆盖。

### 13.4 架构验证命令

涉及生产构造、Run persistence、mode/source resolution、decision 或 shutdown 的改动必须运行：

~~~bash
go test ./internal/architecture
go test ./internal/agentruntime/...
go test ./internal/serve/openaiapi/...
go test ./internal/serve/channels/...
go test ./internal/acp/...
go test ./internal/agent/...
cd ui && npm run build
~~~

跨入口行为改变时，再运行完整 make test 和 WebUI e2e。

## 14. 验收标准

本方案只有在以下条件同时满足时才算完成：

- WebUI、TUI、ACP、Channel 对同一个模拟 provider 错误给出一致的 Core RetryMode、终态和 attempt 关系；
- 任何入口都不会因浏览器/协议连接断开而创建重复 Run；
- WebUI 能在自动重试、最终失败、取消、超时和结果未知之间正确显示并恢复；
- 用户重试只通过 Runtime 使用服务端原始 Intent，不能由前端或 adapter 自行重建请求；
- 工具副作用、审批和 Session policy 约束在所有入口一致；
- canonical Run、Decision、Event 和 shutdown 边界未被旁路；
- 刷新/replay 后错误信息仍可解释、可操作、可审计；
- 没有遗留的 adapter-local retry state machine、字符串分类或第二份 durable error store。

## 15. 关键风险与处理

| 风险 | 处理 |
|---|---|
| 重试重复文件修改或外部调用 | 以 tool execution record 和 side effect state 为事实，默认禁止不确定重放 |
| provider 已产生部分输出但连接断开 | 标记 partialOutput，由 Core 决定是否可恢复；adapter 不复制已显示内容 |
| 配置热更新导致重试策略漂移 | 自动重试使用 Intent 的 policy snapshot；新 attempt 由 Runtime 重新校验 binding 和硬安全策略 |
| 错误文案泄露敏感信息 | Core 产生安全 fallback，技术细节只进脱敏日志和 request ID |
| WS/SSE 事件重复或乱序 | 以 canonical seq、runID 和 reducer 去重；查询 Run 作为收敛事实 |
| 旧客户端不认识新增字段 | 保持 OpenAI error 基础字段和旧终态 event alias，新字段可选渐进启用 |
| 多入口迁移期间行为分裂 | 先完成 Core/Runtime contract test，再迁移 adapter；架构测试禁止新增旁路 |

## 16. 最终结论

最优实现路径不是在 WebUI 加一个本地 retry 按钮，而是把“原始请求、错误分类、自动重试、安全恢复、Run attempt 和终态错误”全部收敛到 Agent Core/Agent Runtime。WebUI 只通过统一 API 提交/查询/重试 Run，并把 canonical events 翻译成友好的界面；TUI、ACP、Channel 使用相同的 Runtime 语义，只保留各自协议和交互形式。

实施顺序应固定为：

~~~text
统一 ErrorInfo/ExecutionIntent
  -> Core/Runtime retry event 与安全策略
  -> canonical persistence/Run API
  -> WebUI 错误卡片、reconcile、retry projection
  -> TUI/ACP/Channel 适配收敛
  -> 跨入口验收和旁路清理
~~~

任何实现如果需要在 WebUI、TUI、ACP 或 Channel 新增独立的 retry manager、错误分类器、Run 持久化或完整 Agent 装配，都应视为违反本方案，需要先扩展共享 Runtime。
