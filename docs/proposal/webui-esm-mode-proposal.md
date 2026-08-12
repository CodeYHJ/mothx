# WebUI ESM 模式生产级方案

> 状态：Proposal（生产目标架构，待评审）  
> 日期：2026-08-12  
> 关联：`enable-supervisor-mode.md`、`webui-background-run-websocket-architecture-proposal.md`、`webui-tui-sync-proposal.md`

## 1. 方案原则

WebUI ESM 不是把 TUI 的 slash command、快捷键、终端面板和自动续跑逻辑搬到浏览器，而是把 ESM 作为一个由服务端托管、由 WebUI 通过图形化控件控制和观察的长期任务产品。WebUI 不提供 `/esm`、`/esm pause` 等命令入口，也不要求用户记忆任何 ESM 快捷键。

最终产品定义：

```text
ESM = 绑定一个 Session 上下文的持久化后台任务
WebUI = ESM 的控制台和观察端
TUI = 同一个 ESM runtime 的另一个客户端
浏览器连接 = 观察关系，不是任务生命周期
```

所有行为必须满足：

- 关闭页面不会停止任务；
- 用户能明确知道任务是否在后台运行、正在消耗什么资源、下一步是什么；
- 任意时刻只能有一个任务执行者拥有该 session 的执行租约；
- WebUI 不在浏览器中实现 ESM 状态机；
- TUI 与 WebUI 共享领域语义和事实状态，但不共享交互形态；
- 任何“完成”都必须经过独立验证，不能只相信模型的自然语言声明；
- 所有状态转换、用量、审查和角色运行均可持久化、重放和审计。

## 2. TUI Review 模式复盘：哪些是核心，哪些不搬

### 2.1 必须共享的 ESM 核心语义

这些不是 TUI 行为，而是 ESM 的领域规则：

1. 一个 session 只有一个当前 objective；
2. objective、预算、用量、进度、剩余工作、审查结果必须持久化；
3. 用户控制 objective 生命周期，模型只能提交进度、完成候选或阻塞报告；
4. `complete` 不是模型直接写入的终态；
5. 完成候选必须经过独立验证；
6. blocked、budget、usage、recovery 都有明确的停止条件；
7. worker、critic、audit 等角色的权限边界必须由服务端保证；
8. 状态变更先持久化，后发布事件。

`internal/esm.Store`、`internal/esm` 的状态模型、prompt 安全规则、模型工具约束和报告结构可以作为共享领域能力。

### 2.2 不直接搬到 WebUI 的 TUI 行为

以下内容属于 TUI 的交互或运行编排，不是 WebUI 的产品要求：

- `/esm`、`/esm pause` 等 slash command；
- `Ctrl+E` 等快捷键；
- footer 短文本；
- Bubble Tea modal 和终端滚动模型；
- 让用户通过命令文本输入 objective、budget 或生命周期操作；
- TUI idle 后立即续跑的事件循环；
- TUI 的 `isThinking`、`agentActivities` 等内存状态；
- 把普通用户输入简单视为“不能在 agent 运行时输入”；
- 直接依赖 TUI 的 `recoverInterruptedESMRole`；
- 将 TUI 的 Now/Progress/Next 文案原样复制到页面。

WebUI 的对应操作必须使用可见的图形化控件：按钮、表单、下拉框、Toggle、Popover、Drawer、Modal、进度条、状态 Badge、确认对话框和通知。

WebUI 只吸收这些行为背后的产品信息：当前执行角色、进度、下一步、审查结论、阻塞原因、恢复状态和用量，然后重新设计成 Web 产品。

## 3. WebUI 的最终产品模型

### 3.1 ESM 是后台任务，不是普通 mode

`plan`、`agent`、`yolo` 是一次执行使用的运行策略；ESM 是跨多次 Run 持续存在的任务生命周期。两者不能放在同一个 mode 下拉框里。

WebUI 应将 ESM 放在：

- Chat 的 Session runtime 控制区；
- 独立的 ESM task drawer；
- 全局后台任务入口/通知中心。

Chat 页面可以显示当前 session 的 ESM 状态，但不能让 ESM 看起来只是“换了一个模型模式”。

### 3.2 Session 与 ESM 的关系

ESM 绑定 session，因为它需要该 session 的：

- 对话上下文；
- workdir；
- model/provider 配置；
- sandbox、approval、skills、MCP 和工具能力；
- transcript 与运行历史。

数据关系：

```text
Session
  └── Current ESM Objective
        ├── Objective lifecycle
        ├── ESM role runs
        ├── Reviews and reports
        └── Usage and audit events
```

ESM 不是浏览器 tab 的属性，也不是当前 Chat 组件的局部状态。

## 4. 用户可见的完整行为

### 4.1 图形化控制原则

WebUI 的 ESM 操作全部通过图形化交互完成，不暴露 TUI 命令或快捷键：

| 操作 | WebUI 控件 | 交互要求 |
|---|---|---|
| 创建 objective | “启用 ESM”按钮 + 创建表单 | 必填目标、预算、后台运行说明，提交前确认资源消耗 |
| 查看状态 | ESM 状态卡片 / Drawer | 显示状态、阶段、当前 Run、用量、下一步 |
| 编辑目标 | “编辑目标”按钮 + Modal | 显示当前 revision，保存时做版本冲突检查 |
| 暂停 | “暂停任务”按钮 | 二次确认；正在运行时显示优雅停止进度 |
| 恢复 | “恢复任务”按钮 | 显示恢复原因和将要启动的下一步角色 |
| 停止当前 Run | “停止当前运行”按钮 | 只停止当前 Run，不清除 objective |
| 清除 | “清除任务”按钮 + 危险操作确认 Modal | 明确说明历史记录不会被删除 |
| 修改预算 | 预算输入框 / Preset + “保存预算”按钮 | 正整数或“无限制”，预算变化立即显示在状态卡片 |
| 提供指导 | Chat 输入框中的“发送到 ESM”操作或 Guidance Modal | 明确这是任务指导，不是新的并发 Chat Run |
| 审批 | Approval Card 的批准/拒绝按钮 | 显示工具、参数、风险和作用范围 |

按钮状态必须由服务端返回的 snapshot 决定。请求进行中显示 loading/disabled，重复点击不能重复提交。所有危险操作使用明确的确认 Modal，不使用隐藏的命令语法或快捷键代替。

### 4.2 创建 objective

用户在 WebUI 点击“启用 ESM”按钮，打开创建表单，填写：

- objective；
- token budget；
- 可选的最大运行时间预算；
- 执行模式和模型；
- 是否允许需要用户审批的操作等待用户处理。

“启用后台运行”不是一个容易误触的 Toggle；WebUI 默认按后台任务处理，并在表单中以不可忽略的说明和确认项明确告知用户：关闭浏览器不会停止任务。若产品需要允许用户选择不后台运行，应通过清晰的“任务运行策略”单选项表达，而不是把它隐藏在普通设置中。

创建前必须明确展示：

> ESM 会在当前 Run 结束后自动继续执行。关闭浏览器不会停止任务。任务会持续消耗模型额度，直到完成、暂停、阻塞、达到预算或被取消。

用户确认后，服务端原子创建 objective 并排队第一个 ESM Run。不能通过普通聊天消息隐式创建 ESM。

如果当前存在未完成 objective，创建请求必须失败；用户只能编辑、暂停、恢复或清除当前 objective。

### 4.3 后台运行

WebUI ESM 默认是服务端后台任务：

- 浏览器关闭、刷新、切换 session 不会停止任务；
- WebSocket 断开只影响观察，不影响执行；
- 服务重启后从持久化 Run 和 objective 恢复；
- 服务无法安全恢复时，任务进入 `paused` 或 `failed_recovery`，而不是永久保持 `running`；
- 后台任务必须在全局任务入口、session 列表和当前 Chat 中有可见状态。

“允许后台运行”不是浏览器端开关，而是创建时的用户确认和任务策略记录。用户后续可以 Pause 或取消当前 Run。

### 4.4 用户输入与 ESM 的关系

WebUI 不能简单禁止用户在 ESM 运行时发送消息。聊天产品必须支持用户干预，但不允许产生并发执行。

用户输入分为三类：

1. **控制输入**：Pause、Resume、Cancel Run、Edit Objective、Clear；立即执行。
2. **审批输入**：批准/拒绝 pending approval；恢复被审批挂起的 Run。
3. **任务指导输入**：用户对当前 objective 的补充说明、纠正或优先级调整。

任务指导输入进入 ESM 的 `user_guidance` 队列：

- 不直接启动第二个 Run；
- 不覆盖原 objective；
- 服务端在当前角色安全结束后，将 guidance 作为下一次 worker continuation 的用户上下文；
- 用户可以选择“立即停止当前 Run 并应用指导”，此时当前 Run 进入 cancelled/interrupted，随后按恢复策略启动新的 Run；
- guidance 必须持久化、带时间和作者，并在 UI 中可编辑/删除，避免浏览器断线丢失。

普通聊天消息如果不是 ESM guidance，必须显式选择“独立聊天 Run”。独立聊天 Run 与 ESM 不能并发占用同一 session；系统会要求用户先暂停 ESM 或将消息转为 guidance。

### 4.5 Pause、Resume、Cancel、Clear

- **Pause**：不取消已经完成的事实记录；如果当前 Run 正在执行，先请求优雅停止，超过超时后再强制取消。objective 进入 `paused` 后不再自动续跑。
- **Resume**：恢复 objective；如果没有活跃 Run，创建 continuation Run。
- **Cancel current Run**：只取消当前 worker/critic/audit/recovery Run，不清除 objective。active objective 后续按 interruption policy 进入恢复或 paused。
- **Clear**：要求用户确认；清除当前 objective 和未排队的 continuation，但不删除历史 transcript、Run 和审计记录。
- **Edit objective**：保留历史目标和审查记录，创建新的 objective revision；清空不再适用的 remaining work、blocker 和 completion candidate，但不删除历史。

所有控制都必须幂等，重复点击不能重复创建 Run 或改变已完成的终态。

## 5. ESM 状态机

### 5.1 Objective lifecycle

```text
none
  -> active
active
  -> paused
  -> waiting_approval
  -> waiting_user
  -> budget_limited
  -> usage_limited
  -> blocked
  -> complete_candidate
  -> complete
  -> failed_recovery
paused / blocked / waiting_user / failed_recovery
  -> active
budget_limited
  -> active      # 仅提高或移除预算后
usage_limited
  -> active      # 外部限额解除后
complete_candidate
  -> active      # critic/audit 拒绝
complete_candidate
  -> complete    # 独立 audit 通过
any non-terminal
  -> cleared     # 用户 clear，历史仍保留
```

`waiting_approval` 和 `waiting_user` 是 WebUI 必须具备的状态，因为它们代表任务不是失败，也不是普通 active continuation，而是在等待明确的人类动作。

### 5.2 Run lifecycle

所有 ESM 角色复用统一后台 Run：

```text
created -> queued -> running -> waiting_approval
                         -> cancelling -> completed
                         -> failed
                         -> cancelled
```

每个 Run 至少包含：

- `runID`、`sessionID`、`esmID`、objective revision；
- `role`: worker / critic / audit / recovery；
- source、model、mode、started/finished time；
- status、error、usage；
- parent run、report ID、continuation ID。

同一 session 同时只能有一个 active ESM role Run。浏览器连接、SSE subscriber、WebSocket subscription 均不能拥有 Run。

## 6. Review 与完成验证

### 6.1 为什么 WebUI 仍需要独立验证

WebUI 允许后台运行，且目标通常涉及代码、文件和测试。仅依赖 worker 自报完成会导致：

- 未完成的 remaining work 被忽略；
- 测试未运行或失败被隐藏；
- objective 的全部约束没有被检查；
- 页面显示完成，但仓库状态并不满足目标。

因此，生产级 ESM 必须保留独立验证；但 WebUI 不展示 TUI 的“critic/audit 子代理实现细节”，而展示验证阶段和可读报告。

### 6.2 完成流程

```text
worker
  -> 进度报告
  -> completion candidate + evidence
  -> independent critic review
  -> independent audit against objective and repository
  -> complete OR rejection with remaining work
```

规则：

- `update_esm(complete)` 只产生 `complete_candidate`；
- critic 和 audit 使用隔离上下文，不拥有 ESM 工具，不修改仓库；
- audit 必须检查 objective 的每项要求、当前 diff、测试/验证证据和 remaining work；
- 任一验证失败都要写入 review、证据和 remaining work，并回到 active；
- 连续拒绝达到阈值时进入 `paused`，要求用户查看原因后恢复；
- UI 只能展示服务端持久化的 review，不能让浏览器提交“通过”。

### 6.3 验证强度

验证策略由 objective 的风险分类决定，但分类和规则由服务端决定，不能由模型自行降低：

- 代码/配置修改：必须检查 diff，并执行适用的测试或验证命令；
- 只读调查：必须提供来源、检查范围和未解决问题；
- 高风险操作：必须要求用户审批，不能通过 ESM 自动完成；
- 无法验证的外部条件：进入 `waiting_user` 或 `blocked`，不能伪造 complete。

## 7. 阻塞、预算和恢复

### 7.1 Blocked

blocked 不是模型一句话就能设置的终态。服务端需要记录：

- 具体阻塞原因；
- 受影响的 objective requirement；
- 已尝试的动作；
- 需要用户提供的输入或外部条件；
- 关联 Run 和证据。

同一阻塞连续重复且确实无法继续时进入 `blocked`。WebUI 必须提供“查看阻塞原因”和“提交 guidance/resume”的直接入口。

### 7.2 Budget 和 usage

- token/time usage 在 Run finalizer 中统一记录；
- ESM accounting 与 stats dashboard accounting 同时写入，但职责不互相替代；
- 达到 budget 后停止新的模型 Run；
- 页面显示已用、上限、预计剩余和停止原因；
- 提高/移除预算是用户动作，不能由模型或自动续跑覆盖；
- provider usage limit 进入 `usage_limited`，不得伪装成 blocked 或 complete。

### 7.3 中断恢复

恢复是服务端 coordinator 的职责，不是 TUI 的 `recoverInterruptedESMRole` 在 WebUI 中的复制：

- transient transport failure：按 provider 重试策略处理，失败后记录 Run failed；
- worker 超时或未知中断：读取当前仓库状态和最近 durable events，生成 recovery diagnosis；
- 能安全继续则排队新的 worker Run，并将 recovery context 注入；
- 不能判断是否安全继续则进入 `waiting_user`；
- 连续恢复超过策略上限进入 `failed_recovery`/`paused`；
- WebUI 展示恢复原因、尝试次数和用户可执行动作。

## 8. 服务端架构

### 8.1 唯一事实来源

```text
session_esm_objectives       objective 当前状态
session_esm_revisions        objective 编辑历史
session_runs                 每次角色执行
session_events/event broker  transcript、role、review、runtime 事实事件
ESM coordinator              状态转换和调度
RunExecutor                  Agent 事件消费、usage、finalizer
```

不新增 WebUI 专用 ESM 状态表，不让 Svelte store 成为事实来源。

已有 `session_esm_objectives` 如果不足以记录 objective revision、guidance、role run 关联、审查报告和恢复诊断，必须通过正式 migration 增加结构化表或字段；禁止把 JSON 拼接到普通 transcript 中代替领域数据。

### 8.2 ESM coordinator

建议将 TUI 中的 ESM 调度能力收敛到可复用 coordinator：

```go
type Coordinator interface {
    Create(ctx context.Context, req CreateObjectiveRequest) (*Snapshot, error)
    Edit(ctx context.Context, req EditObjectiveRequest) (*Snapshot, error)
    Pause(ctx context.Context, sessionID, actor string) (*Snapshot, error)
    Resume(ctx context.Context, sessionID, actor string) (*Snapshot, error)
    Clear(ctx context.Context, sessionID, actor string) error
    SubmitGuidance(ctx context.Context, req GuidanceRequest) (*Guidance, error)
    CancelRun(ctx context.Context, sessionID, runID, actor string) error
    Reconcile(ctx context.Context, sessionID string) error
}
```

coordinator 负责：

- objective 状态转换；
- continuation 排队；
- role pipeline；
- 单 session lease；
- pause/cancel/clear 与 Run 的竞态；
- review/recovery/budget policy；
- durable event 发布；
- 服务重启 reconcile。

TUI 和 WebUI 只能调用 coordinator，不能各自实现续跑。

### 8.3 Run 与 session runtime lock

RunExecutor 必须使用独立于 HTTP 请求的 context，并沿用统一 finalizer：

- browser disconnect 不取消 Run；
- finalizer 幂等记录终态、usage、错误、approval、runtime snapshot 和事件；
- 结束后释放 session runtime lock；
- coordinator 根据最终状态决定下一个 role 或 continuation；
- orphan Run 在服务启动时被 reconcile，不能永久显示 running。

## 9. WebUI API

### 9.1 Objective API

```http
GET    /api/sessions/{sessionID}/esm
POST   /api/sessions/{sessionID}/esm
PATCH  /api/sessions/{sessionID}/esm
POST   /api/sessions/{sessionID}/esm/pause
POST   /api/sessions/{sessionID}/esm/resume
DELETE /api/sessions/{sessionID}/esm
POST   /api/sessions/{sessionID}/esm/guidance
```

所有响应返回同一份 `ESMSnapshot`，包括：

- objective、revision、status、phase；
- token/time usage 和 budget；
- progress summary、remaining work；
- blocker、review、recovery；
- pending guidance；
- active Run、role、等待原因；
- updatedAt、version。

mutation 必须携带 `version` 或 `updatedAt` 做 optimistic concurrency。冲突返回当前 snapshot，让用户重新确认，而不是静默覆盖 TUI/WebUI 的修改。

### 9.2 Guidance API

```jsonc
{
  "text": "优先修复编译错误，不要先做重构",
  "apply": "next_safe_boundary | stop_current_run"
}
```

guidance 是用户数据，不改变原始 objective。它必须出现在后续 prompt 的明确上下文区，并遵守原 objective、system/developer 和安全规则的优先级。

### 9.3 Runtime snapshot

`GET /api/sessions/{sessionID}/runtime` 返回 ESM 摘要，完整详情由 ESM API 返回：

```jsonc
{
  "esm": {
    "status": "active",
    "phase": "worker",
    "activeRunId": "run-1",
    "waiting": false,
    "version": 12
  }
}
```

## 10. 事件协议与刷新恢复

ESM 事件统一通过现有 EventBroker 和 `/ws/runs` 发布，事件先持久化后广播：

```jsonc
{
  "stream": "esm",
  "event": "esm.updated",
  "sessionId": "sess-1",
  "runId": "run-1",
  "seq": 42,
  "data": {
    "snapshot": { "status": "complete_candidate", "phase": "critic" },
    "version": 13
  }
}
```

事件类型：

- `esm.snapshot`；
- `esm.updated`；
- `esm.role_started` / `esm.role_finished`；
- `esm.review`；
- `esm.guidance_added`；
- `esm.waiting_approval` / `esm.waiting_user`；
- `esm.recovery`；
- `esm.budget_limited`；
- `esm.continuation_queued`；
- `esm.completed` / `esm.paused` / `esm.failed`。

WebUI session 切换和刷新流程：

1. GET runtime snapshot；
2. GET ESM snapshot；
3. 订阅 session 的 ESM/run stream；
4. 用持久化 cursor replay；
5. 通过 reducer 应用事件；
6. 收到版本低于当前 snapshot 的事件时丢弃；收到版本跳跃时重新 GET snapshot。

前端不通过事件到达顺序自行推导“是否完成”，也不通过本地计时器推导运行时长。

## 11. WebUI 交互设计

### 11.1 与现有 WebUI 样式和结构的兼容原则

ESM 不新增一条独立的全局顶部栏，也不改造现有 `Topbar.svelte` 的页面标题布局。当前 WebUI 的结构是：

```text
App
  └── Topbar.svelte
        ├── 页面标题/副标题
        └── 当前 Chat session binding

Chat.svelte
  └── chat composer toolbar
        ├── model picker
        ├── runtime controls（mode + approval summary）
        ├── skill picker
        ├── tool menu
        ├── MCP
        ├── workdir
        └── stop/send controls
```

因此 ESM 必须增量嵌入现有 Chat composer toolbar 的 `runtime-controls` 区域，复用现有的：

- `runtime-controls` 容器；
- `runtime-toggle` 按钮样式；
- `runtime-panel` Popover；
- `runtime-label`、`runtime-chevron`、`runtime-badge`；
- 现有 `ghost`、`ghost sm`、`primary`、`stop-btn` 按钮语义；
- 现有 Modal/Drawer、spacing、border、shadow、responsive breakpoint 和 i18n 体系。

不允许：

- 新增与现有 Topbar 平行的第二套顶部工具栏；
- 改变 Chat composer 的整体高度、左右布局和 send/stop 按钮位置；
- 用 ESM 状态挤压或替代 model、skills、tools、MCP、workdir 控件；
- 重新定义全局颜色、按钮圆角、字体、阴影或弹层行为；
- 在页面顶部重复显示 session 标题。

ESM 在现有 runtime controls 中作为独立的“持续任务”入口，不能放进 `plan/agent/yolo` mode switcher。无 objective 时显示一个紧凑的 `ESM` 图形化按钮；有 objective 时显示 `ESM` 状态 Badge。详细操作进入现有 Popover/Modal/Drawer，不把多个操作按钮长期铺在 composer toolbar 上。

### 11.2 WebUI 线框与控件布局

以下线框描述的是生产目标布局，不是视觉稿。具体颜色、字体和图标沿用现有 WebUI 设计系统；线框中的按钮和区域必须具备明确的状态映射。

#### 11.2.1 Chat Composer Runtime Controls 增量线框

该区域不是新增顶部栏，而是现有 Chat composer toolbar 中 `runtime-controls` Popover 的增量内容。现有 model picker、mode、skills、tools、MCP、workdir 和 send/stop 控件保持原位置。

```text
现有 Chat composer toolbar：
┌──────────────────────────────────────────────────────────────────────────────┐
│ [模型 ▼] [Runtime: agent ▼] [Skills ▼] [Tools ▼] [MCP] [Workdir] ... [Stop][送出]│
└──────────────────────────────────────────────────────────────────────────────┘

点击现有 [Runtime: agent ▼] 后：
┌──────────────────────────── Session Runtime ────────────────────────────────┐
│ Mode                                                                       │
│ [plan] [agent] [yolo]             ← 保持现有 mode switcher                 │
│                                                                            │
│ ESM 持续任务                                                               │
│ [未启用]                                           [启用 ESM]              │
│                                                                            │
│ 或：                                                                       │
│ [● 运行中] Worker · 12.5K / 50K tokens              [查看任务]             │
│                                                                            │
│ 待处理审批：2                                      [查看审批]              │
└────────────────────────────────────────────────────────────────────────────┘
```

ESM 只在现有 `runtime-panel` 中增加一个分组，不扩大默认 toolbar。只有 active objective 或 pending ESM action 时，才在 runtime toggle 上显示紧凑 Badge。

控件逻辑：

| 控件 | 显示条件 | 点击行为 |
|---|---|---|
| `启用 ESM` | 没有当前 objective | 打开“创建 ESM 任务”Modal；不直接创建 |
| 状态 Badge | 存在 objective | 打开 ESM Task Drawer；Badge 文案来自服务端 status |
| `查看任务` | 存在 objective | 打开 ESM Task Drawer，并定位到当前状态卡片 |
| `暂停` | `active`、`waiting_approval`、`waiting_user` 或有 active Run | 打开暂停确认；确认后调用 pause API |
| `恢复` | `paused`、`blocked`、`failed_recovery`、`usage_limited` | 打开恢复确认；必要时先展示预算/阻塞原因 |
| `停止当前运行` | 有 active Run | 打开危险操作确认；只取消当前 Run，不清除 objective |
| `更多` | 存在 objective | 展开“编辑目标、预算、清除任务、查看历史”菜单 |

现有 Runtime controls 不显示 `/esm` 文本，也不绑定 `Ctrl+E`。移动端保持现有 toolbar 的折叠方式；ESM 只显示紧凑状态入口，详细操作统一放入 Drawer。

#### 11.2.2 创建 ESM 任务 Modal

```text
┌────────────────────────────── 创建 ESM 任务 ───────────────────────────────┐
│ 目标                                                                       │
│ ┌────────────────────────────────────────────────────────────────────────┐ │
│ │ 例如：完成 API 迁移，运行测试并修复所有回归问题                         │ │
│ └────────────────────────────────────────────────────────────────────────┘ │
│                                                                            │
│ 执行模型       [继承当前模型 ▼]     执行模式       [agent ▼]              │
│ Token 预算     [ 50000        ]     时间预算       [ 不限制 ▼]             │
│                                                                            │
│ 后台执行说明                                                               │
│ [✓] 关闭浏览器后继续执行                                                   │
│                                                                            │
│ ⚠ 任务会跨多个 Run 持续执行并消耗模型额度。                                │
│   任务会在完成、暂停、阻塞、达到预算或被停止时结束。                        │
│                                                                            │
│                         [取消]  [创建并开始]                               │
└────────────────────────────────────────────────────────────────────────────┘
```

表单逻辑：

- 目标为空、预算不是正整数、模式不可用时禁用“创建并开始”；
- 当前已有未完成 objective 时不显示创建表单，改为提示“编辑当前任务”或“清除当前任务”；
- 提交时按钮进入 loading，Modal 不可重复提交；
- 成功后关闭 Modal，打开 Task Drawer，并显示“已排队”；
- 发生版本/并发冲突时保留用户输入，重新读取 snapshot；
- 服务端创建 objective 和首个 Run 必须是可恢复的幂等操作。

#### 11.2.3 ESM Task Drawer

```text
┌──────────────────────────── ESM 任务 ──────────────────────── [×] ─────────┐
│ [● 运行中]  Worker 执行中                         Run: run-123  [停止运行] │
│                                                                            │
│ 目标                                                                       │
│ 完成 API 迁移，运行测试并修复所有回归问题                    [编辑目标]    │
│ revision 4 · 最近更新 12 秒前                                             │
│                                                                            │
│ ┌─ 当前进度 ─────────────────────────────────────────────────────────────┐ │
│ │ Worker ━━━━━━━━━━━●━━ Critic ───── Audit ───── 完成                    │ │
│ │ 已完成：API 路由迁移、基础测试                                           │ │
│ │ 剩余 2 项：补充回归测试；运行完整测试                                    │ │
│ └────────────────────────────────────────────────────────────────────────┘ │
│                                                                            │
│ 用量                                                                       │
│ Tokens 12.5K / 50K   时间 8m   [调整预算]                                │
│                                                                            │
│ [任务指导] [暂停] [停止当前运行] [更多操作 ▼]                             │
│                                                                            │
│ [进度与报告] [验证审查] [运行历史] [操作记录]                              │
└────────────────────────────────────────────────────────────────────────────┘
```

Drawer 内固定包含四个 Tab：

1. **进度与报告**：objective、progress summary、remaining work、当前角色和下一步；
2. **验证审查**：completion candidate、critic/audit 结果、证据、拒绝原因；
3. **运行历史**：worker/critic/audit/recovery Run、状态、用量、错误和开始/结束时间；
4. **操作记录**：用户创建、编辑、pause、resume、guidance、budget、clear 等审计记录。

Drawer 不把所有内容塞在一个长滚动面板中；当前需要用户动作的内容必须置顶为 Action Card。

#### 11.2.4 状态 Action Card

```text
┌─ 需要你的操作 ─────────────────────────────────────────────────────────────┐
│ 状态：等待审批                                                             │
│ Worker 请求执行：go test ./...                                             │
│ 风险：可能执行耗时命令；工作目录：/workspace/project                      │
│                                                                            │
│ [查看完整参数]       [拒绝]  [仅本次批准]  [记住并批准此类操作]             │
└────────────────────────────────────────────────────────────────────────────┘
```

不同状态的 Action Card：

| 状态 | Card 内容 | 主要按钮 |
|---|---|---|
| `waiting_approval` | 工具、完整参数、风险、来源、工作目录 | 拒绝、仅本次批准、记住规则 |
| `waiting_user` | 等待原因、需要用户提供的信息 | 提供指导、恢复、暂停 |
| `blocked` | 阻塞原因、已尝试动作、所需外部条件 | 提供指导、恢复、编辑目标、暂停 |
| `budget_limited` | 已用预算、当前上限、停止原因 | 调高预算、移除预算、保持暂停 |
| `complete_candidate` | 验证进行中、当前 reviewer、待验证项目 | 查看审查，不显示完成按钮 |
| `paused` | 暂停原因、最近进度 | 恢复、编辑目标、清除 |
| `failed_recovery` | 中断原因、恢复尝试、风险说明 | 查看详情、恢复、暂停 |
| `complete` | 最终摘要、验证证据、审查报告 | 查看报告、查看历史、关闭 |

#### 11.2.5 Guidance Modal

```text
┌──────────────────────────── 提供任务指导 ──────────────────────────────────┐
│ 这条说明会加入 ESM 的下一次执行，不会创建并发聊天 Run。                   │
│                                                                            │
│ ┌────────────────────────────────────────────────────────────────────────┐ │
│ │ 优先修复编译错误，不要先做重构                                         │ │
│ └────────────────────────────────────────────────────────────────────────┘ │
│                                                                            │
│ 应用方式：                                                                 │
│ (•) 当前 Run 安全结束后应用                                               │
│ ( ) 先停止当前 Run，再应用                                                 │
│                                                                            │
│                         [取消]  [提交指导]                                 │
└────────────────────────────────────────────────────────────────────────────┘
```

提交后显示 pending guidance；服务端返回 accepted/rejected/conflict 时更新状态。Guidance 不修改原 objective 文本，编辑 objective 必须通过“编辑目标”Modal。

#### 11.2.6 编辑目标、预算和清除确认

```text
编辑目标 Modal：
┌──────────────────────────── 编辑 ESM 目标 ─────────────────────────────────┐
│ 当前 revision: 4                                                          │
│ [多行目标文本.............................................................] │
│ ⚠ 保存后将重新计算剩余工作和验证上下文，历史记录仍会保留。                 │
│                                              [取消] [保存新版本]            │
└────────────────────────────────────────────────────────────────────────────┘

预算 Modal：
┌──────────────────────────── 调整执行预算 ──────────────────────────────────┐
│ 已用：12.5K tokens                                                         │
│ 新预算：[ 100000 ]  [不限制]                                                │
│ ⚠ 提高预算会允许任务继续消耗模型额度。                                     │
│                                              [取消] [保存预算]              │
└────────────────────────────────────────────────────────────────────────────┘

清除确认 Modal：
┌──────────────────────────── 清除 ESM 任务 ──────────────────────────────────┐
│ 将停止后续自动执行并删除当前 objective。历史 Run、报告和审计记录会保留。   │
│ 请输入 CLEAR 确认： [          ]                                           │
│                                              [取消] [清除任务]              │
└────────────────────────────────────────────────────────────────────────────┘
```

危险操作不能通过单击立即执行。目标编辑、预算保存和清除都必须处理服务端 version 冲突。

### 11.4 按钮状态矩阵

按钮是否显示、是否可用和点击后行为必须由服务端 `ESMSnapshot.status`、`activeRun`、`pendingApproval`、`version` 决定，不能仅由前端本地布尔变量决定：

| Objective 状态 | 顶部主要按钮 | Drawer 操作 | 自动执行 |
|---|---|---|---|
| `none` | 启用 ESM | 无 | 无 |
| `active` | 查看任务、暂停 | 指导、停止 Run、编辑、预算、清除 | 允许 continuation |
| `waiting_approval` | 查看任务 | 批准/拒绝、暂停、停止 Run | 等待批准，不启动新 Run |
| `waiting_user` | 查看任务、恢复 | 提供指导、暂停、编辑、清除 | 等待用户 |
| `paused` | 恢复、查看任务 | 编辑、预算、清除 | 禁止 continuation |
| `blocked` | 恢复、查看任务 | 提供指导、编辑、暂停、清除 | 禁止 continuation |
| `budget_limited` | 查看任务 | 调整预算、清除 | 禁止 continuation |
| `usage_limited` | 查看任务 | 恢复、暂停、清除 | 外部限额解除前禁止 |
| `complete_candidate` | 查看任务 | 查看验证，不允许用户标记完成 | 只运行验证角色 |
| `failed_recovery` | 恢复、查看任务 | 查看详情、暂停、清除 | 禁止 continuation |
| `complete` | 查看报告 | 查看历史、关闭任务视图 | 终止 |

### 11.5 操作请求状态

所有按钮统一经历以下前端状态：

```text
idle -> submitting -> accepted -> snapshot/event applied
                 \-> conflict -> reload snapshot -> ask user
                 \-> forbidden/error -> keep drawer open and show error
                 \-> timeout -> query operation status, never blindly retry mutation
```

Mutation 请求应携带 `requestId`、当前 objective `version` 和 actor。网络超时后前端先 GET snapshot 或查询 operation status，再决定是否重试，禁止因不确定结果重复创建任务或 Run。

### 11.6 通知

以下状态必须产生可见通知，并可在页面重连后补回：

- objective started；
- waiting approval/user；
- review rejected；
- budget/usage limited；
- recovery paused；
- completed；
- failed。

通知只提醒，不改变任务状态，也不能代替持久化 timeline。

## 12. 权限、安全与并发

- API 和 WebSocket 都必须校验 session/workdir/auth；
- objective、guidance、review 原文均视为不可信用户数据；
- 角色权限由服务端构造，critic/audit/recovery 不得通过 WebUI 参数获得写权限；
- approval 必须绑定 session、Run、tool call 和版本，重复响应幂等；
- edit/pause/resume/clear/cancel 与 coordinator 状态转换原子化；
- 同一个 objective 不允许两个 coordinator 实例同时续跑；
- 所有自动运行都有可见的 usage/budget 和停止控制；
- 清除 objective 不删除审计记录；
- 服务器关闭、网络中断、浏览器关闭都不能制造永久 running 或丢失控制权。

## 13. 完整验收标准

### 产品行为

- 用户明确创建 ESM 后，能看到后台运行和费用提示；
- 浏览器关闭后任务仍按服务端策略运行；
- 用户 guidance 不会和 ESM 并发执行；
- 用户可随时 Pause、停止当前 Run、Resume、Edit、Clear；
- pending approval、waiting user、blocked、budget limited 都能区分；
- complete candidate 在独立验证前不会显示完成；
- 完成后能查看 objective、证据、review、Run 和最终状态。

### 一致性

- TUI/WebUI 同一 session 看到同一个 objective、revision、status、phase、usage 和 review；
- TUI/WebUI 不会重复启动 continuation；
- 一端的修改会通过事件通知另一端；
- optimistic concurrency 能阻止静默覆盖。

### 可靠性

- WebSocket 断线、页面刷新、服务重启均可恢复；
- 每个 Run 最终都会进入 completed/failed/cancelled；
- coordinator 崩溃后可 reconcile；
- finalizer 幂等；
- budget、usage、approval、recovery、review 结果不丢失；
- 事件持久化顺序和 cursor replay 不会造成状态倒退或重复执行。

### 安全性

- objective/guidance 不能提升 prompt 优先级；
- WebUI 不能伪造 audit 通过；
- WebUI 不能让 critic/audit/recovery 写文件；
- 用户无权访问的 session 无法读取或控制 ESM；
- 不存在浏览器断开即取消、重复请求重复启动、旧页面覆盖新状态等竞态。

## 14. 最终决定

本方案不把 TUI 当作 WebUI 的界面规范。最终共享边界只有：

```text
共享：objective、状态机、预算、用量、完成验证、角色权限、持久化事件
不共享：快捷键、slash command、footer、终端 panel、TUI 内存事件循环
```

WebUI 的用户操作必须通过图形化控件完成：

```text
启用 ESM       -> 按钮 -> 创建表单 -> 确认 Modal
查看进度       -> 状态卡片 -> ESM Drawer
暂停/恢复      -> 操作按钮 -> 状态确认
停止当前 Run   -> 危险按钮 -> 二次确认
编辑目标/预算  -> Modal 表单 -> 版本校验
提供任务指导   -> Chat 操作或 Guidance Modal
审批工具调用   -> Approval Card -> 批准/拒绝按钮
```

任何 WebUI 文档、组件或验收标准都不应要求用户输入 `/esm`、`/esm pause` 等命令，也不应要求用户记忆 `Ctrl+E` 等快捷键。

WebUI 的生产级 ESM 必须是服务端托管的后台任务，支持人类 guidance、审批等待、优雅暂停、显式取消、独立完成验证、恢复和审计。浏览器只是一个可靠的控制台，不是 ESM 的执行器，也不是状态来源。
