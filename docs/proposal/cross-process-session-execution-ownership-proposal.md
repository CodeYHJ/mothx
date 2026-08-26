# 跨进程 Session 执行归属、停止与恢复方案

> 状态：Proposal
>
> 日期：2026-08-26
>
> 关联方案：[Session 分叉与消息进度分叉方案](./session-fork-and-message-branch-proposal.md)、[统一 Agent 核心与多入口 Runtime 方案](./agent-core-runtime-unification-proposal.md)、[WebUI 会话终止与审批生命周期方案](./webui-session-stop-and-approval-lifecycle.md)

## 1. 背景与问题

当前可能出现如下用户可见的不一致：Session 列表或聊天页显示“正在运行”，但点击停止后接口返回 `session has no active run`。这并不必然说明列表错误；也可能是**另一个进程**持有该 Session 的有效执行权并仍在运行。

多进程可同时访问同一 Session 是已支持的场景，例如 Serve/WebUI、CLI、TUI、ACP 与 channel 入口。为避免两个进程同时写入同一 Session，系统已经有三层机制：

| 机制 | 已解决的问题 | 不能单独解决的问题 |
|---|---|---|
| 进程内 mutex | 同一 Go 进程中的快速串行化 | 其他进程的所有权、崩溃恢复 |
| SQLite `session_runtime_leases` | 跨进程 lease、TTL、epoch fencing 与持久化裁决 | 向已连接客户端即时推送变化 |
| UDP `SessionLeaseBus` | 提醒同机其他进程尽快重新读取状态 | 可靠所有权、取消命令与恢复裁决 |

`session_runs` 的非终态记录表示“存在尚未完成的 durable Run”；有效的 runtime lease 表示“哪个进程当前被允许继续执行和写入”。两者是不同维度，不能互相替代。

目前 Serve/WebUI 的 runtime snapshot 主要从 active Run 得出 `running`，而停止入口主要查看当前 Serve 进程的 `APISession` 内存对象及其 cancel function。因此：

1. 远端进程持有有效 lease 时，UI 正确显示运行中，但当前进程没有内存 cancel function；
2. orphan Run（进程崩溃、lease 过期、终态落库失败等）会留下非终态 durable Run，但没有本地执行实体，也无法证明存在可恢复的 provider 远端执行；
3. snapshot 没有把“运行事实”“执行归属”“本地可控制性”明确投影给适配器；
4. UDP 收到通知后虽然会触发刷新，却没有把 lease owner 的判定带入统一状态模型。

本方案不把上述情况简单改为“非本进程执行就不显示 running”。那会重新打开跨进程并发提交窗口，违背 lease 的设计目的。正确的产品语义是：**远端有效执行仍显示为运行中，但它不是本进程可停止的运行。**

## 2. 目标与非目标

### 2.1 目标

1. 将 durable Run、SQLite lease 和本进程执行实体统一解析为一个 Runtime-owned 的 Session 执行快照。
2. 区分本地执行、外部执行、远端 provider 执行、非执行占用、孤儿 Run、空闲和暂不可判定状态；所有入口使用同一判定。
3. 保持其他进程持有有效 lease 的 Session 为 running，禁止任何入口在这种情况下提交新的 Run。
4. 将停止语义拆分为本地取消、外部归属冲突、孤儿终结/恢复和状态暂不可用，消除误导性的 `no active run`。
5. 将 lease 的 `run_id` 与 durable Run 建立受 fencing 保护的关联，方便诊断、恢复和状态投影。
6. 保证孤儿恢复先取得 lease 再写入；绝不因一个进程未看到本地内存 runtime 就终结另一个有效进程的 Run。
7. 让终态持久化失败保留为可观察、可重试的生命周期状态，而不是提前从内存和 UI 中“清空”。
8. 为 admission、execution、recovery、mutation、fork 建立明确的 lease purpose，避免把维护操作误报为 Agent 执行。
9. 保持一个 Agent Runtime、一个 Run 生命周期和薄适配器的架构边界。

### 2.2 非目标

1. 本期不实现“任意进程都能强制停止另一个进程”的跨进程取消协议。若将来需要，该能力必须使用 durable control command/ack，不得复用 UDP 广播充当命令通道。
2. 本期不通过 UDP 复制 Agent 内存、tool 状态、审批等待器或 provider stream。
3. 本期不承诺进程崩溃后能从任意 Agent 中间指令继续执行；不能安全 reattach 的 orphan Run 仍按既有恢复策略终结。
4. 本期不改变 mode、审批、sandbox、渠道绑定或 provider 的业务语义。
5. 本期不新增 adapter 私有 Run manager、锁、恢复循环或 SQLite schema 初始化路径。

## 3. 设计原则与不变量

### 3.1 权威性顺序

```text
SQLite durable Run + SQLite runtime lease  ── 事实与裁决
              ↑
       Runtime execution snapshot           ── 唯一业务解释
              ↑
Serve / WebUI / CLI / TUI / ACP / Channel   ── 只做投影与协议映射

UDP、进程内 mutex、内存 APISession           ── 加速、执行或通知，不单独裁决
```

- `session_runs` 是 durable 生命周期事实；非终态 Run 不可被当作可随意覆盖的陈旧 UI 位。
- `session_runtime_leases` 是跨进程执行权的事实；有效性必须由 SQLite 的时间与 fencing 字段裁决，不能由 UDP 是否到达裁决。
- 本地内存 `ExecutionRuntime`、`APISession` 和 cancel function 只证明“当前进程是否能够实际执行本地取消”。它们不能否定外部有效 lease。
- UDP 只用于唤醒订阅者、缩短下一次 SQLite 查询的延迟；丢包、重复、乱序和监听失败都不得改变正确性。
- active Run、lease 和 SQLite 当前时间必须来自同一个只读事务快照；跨多个独立查询拼接出的状态不能用于恢复或控制决策。

### 3.2 运行与控制必须分离

`running` 回答的是“这个 Session 是否有尚未终结的执行”，而不是“当前 HTTP 服务是否持有取消函数”。

`canCancelLocal` 回答的是“调用当前 Runtime 能否对该 Run 执行 `ExecutionRuntime.CancelDurable`”。两者不得复用同一个布尔字段或通过本地 map 推断。

### 3.3 统一 Runtime 边界

新能力必须扩展 `internal/agentruntime` 与 `internal/session` 的既有 Run/lease 边界：

- `internal/session` 负责 lease 和 Run 的原子读取、fencing 校验与事务性关联；
- `internal/agentruntime` 负责判定、取消、重试终结与 orphan recovery 的编排；
- Serve/OpenAI API、WebUI、CLI、TUI、ACP、channel 只消费 snapshot 或调用 Runtime 控制操作；
- `RunManager` 可以继续做事件 fan-out，但不得成为状态、归属或取消结果的第二权威来源。

不得在 `internal/serve/openaiapi` 仅靠 `APISession.IsRunning()` 再实现一套跨进程判定，也不得让 UI 根据 UDP 报文自行决定 Session 是否可提交或可停止。

### 3.4 安全与隐私

适配器和客户端只需要知道归属范围（`local`、`external`、`remote`、`orphaned` 等）和安全的展示信息。不得在 HTTP/SSE/WebSocket 或 UDP 中暴露 lease token、token hash、原始 `owner_instance_id` 或 PID。`owner_kind` 当前只能作为诊断元数据，不能被当成安全身份或产品权限依据。

## 4. 当前实现缺口

| 位置 | 当前行为 | 造成的问题 |
|---|---|---|
| `internal/session/run_store.go` 的 active Run 查询 | 按 non-terminal `session_runs.status` 返回 Run | 能正确发现 durable Run，但不说明谁可控制它 |
| `internal/session/runtime_lock.go` | lease 表已有 `run_id`，但 acquire/reclaim 会写入空字符串 | Run 与其执行 lease 没有可验证关联 |
| `TryLockRuntime` 的现有调用点 | execution、Session mutation、Responses 维护等操作都写入 `purpose=run` | `purpose=run` 当前不能直接解释为 Agent 正在执行 |
| `internal/serve/openaiapi/types.go` 的 `SessionActiveRun` | 只包含 `runId`、`status` | snapshot 无法表达 local/external/orphaned 和可取消性 |
| `runtimeSnapshotFromCapabilities` | 从 durable Run 形成 activeRun | UI 的 running 与本地内存控制状态没有明确关系 |
| `CancelSessionRun` | 查当前进程 `APISession` 和本地 cancel function | 外部活跃 Run 被误报为“没有 active run” |
| `PublishExternalSessionUpdate` | UDP/lease 通知后转发持久化事件 | 刷新时仍缺少归属投影 |
| `RecoverOrphanedRuns` | 以 durable Run 为输入执行恢复 | 必须补上“取得并复核 lease 后才可恢复”的前置条件 |
| `DefaultRunRecoveryPolicy` | 保留仍可恢复的 `responses_background` Run | 无 lease 不一定代表没有远端执行实体，不能全部判成 orphan |
| 若干 Run finalizer | `FinishDurable` 出错后仍可能清理 adapter 内存状态 | durable Run 可保持 active，但当前进程已没有可取消执行实体 |

现有 `session_runtime_leases` 列和 epoch fencing 是本方案的基础，不应为修复本问题另建一张“running session”表或另发一种锁消息。

## 5. 统一执行快照

### 5.1 Runtime contract

在 `internal/agentruntime` 增加面向所有适配器的只读服务，例如 `InspectSessionExecution`。名称可在实现阶段按既有命名调整，但责任边界固定：它必须在一次受控读取中解释 active Run、lease 与本地执行注册表，不能把这三项判断分散到各 adapter。

建议的概念模型如下：

```go
type SessionExecutionState string

const (
    SessionExecutionIdle         SessionExecutionState = "idle"
    SessionExecutionReserved     SessionExecutionState = "reserved"
    SessionExecutionLocal        SessionExecutionState = "local"
    SessionExecutionExternal     SessionExecutionState = "external"
    SessionExecutionDetached     SessionExecutionState = "detached_remote"
    SessionExecutionOrphaned     SessionExecutionState = "orphaned"
    SessionExecutionInconsistent SessionExecutionState = "inconsistent"
    SessionExecutionUnknown      SessionExecutionState = "unknown"
)

type SessionExecutionSnapshot struct {
    SessionID          string
    ActiveRun          *RunSummary
    State              SessionExecutionState
    Phase              string // "reserved", "admitting", "executing", "terminal_persistence", "releasing", "recovering"
    LeaseExpiresAt     *time.Time
    LeasePurpose       string
    LeaseEpoch         int64 // 仅服务端控制操作使用；是否对客户端投影由 API 层决定
    LinkageState       string // "bound", "legacy_unbound", "mismatched", "none"
    CanSubmit          bool
    CanCancelLocal     bool
    CanCancelRemote    bool
    RecoveryAction     string // "none", "reconcile", "retry_terminal_persistence"
    DisplayOwnerScope  string // "local", "external", "remote", "none", "unknown"
}
```

`RunSummary` 必须来自 canonical Run store，至少含 run ID、durable status、source、mode、开始和更新时间。`DisplayOwnerScope` 是安全投影，不能替代内部 owner/token/epoch；若 source 可安全展示，可使用 Run 的 source 作为辅助文案，而不是将 lease owner ID 暴露给用户。`LeaseEpoch` 是 Runtime 内部控制版本，HTTP 是否返回它由 API 契约决定；客户端不能用它自行取得 lease。

`CanSubmit` 在所有有 active Run、`reserved`、`detached_remote`、`orphaned`、`inconsistent` 和 `unknown` 状态均为 `false`。这保证“看不到本地 runtime”不会被误认为可以创建新的 Run。

### 5.2 判定规则

数据层应提供类似 `ReadSessionExecutionFacts` 的操作，在**同一个 SQLite 只读事务**中使用一次 SQLite 当前时间并读取同一个 Session 的：

1. 该 Session 当前非终态 durable Run；
2. runtime lease 的 purpose、state、expiry、owner、epoch 与 `run_id`；
3. 对 `responses_background` 等可脱离本地进程继续执行的 Run，读取 Runtime-owned 的远端执行记录与可恢复能力。

数据库事务完成后，再将这份不可变事实快照与当前进程注册的 `ExecutionRuntime` binding 比较。本地内存不能加入 SQLite 事务，因此比较失败时必须保守返回非 local 状态；控制操作还要在真正写入前再次校验预期的 run ID、epoch 和 lease identity。

判定按表中顺序应用；`recovery` 等显式 purpose 优先于一般的 execution owner 判断。

| durable Run | SQLite lease | 本地注册执行体 | 快照状态 | UI 语义 | 可提交 / 本地停止 |
|---|---|---|---|---|---|
| 无 | 无有效 run lease | 无 | `idle` | 空闲 | 可 / 不可 |
| 无 | 有效 admission/mutation/fork/recovery lease，或 execution lease 正处于 release 窗口 | 任意 | `reserved` | Session 正被其他操作占用 | 不可 / 不可 |
| 非终态 | 有效 recovery lease，且绑定待恢复 run ID | recovery coordinator | `reserved` / phase=`recovering` | 正在恢复或重新挂接 | 不可 / 不可 |
| 非终态 | 有效 admission/mutation/fork lease | 任意 | `inconsistent` | 非执行操作与 active Run 冲突 | 不可 / 不可 |
| 非终态且 run ID 已绑定 | 有效、owner 为当前进程 | 同一 run、epoch 和 lease identity 已注册 | `local` | 本进程正在执行 | 不可 / 可 |
| 非终态且 run ID 已绑定 | 有效、owner 为其他进程 | 任意 | `external` | 其他进程正在执行 | 不可 / 不可 |
| 非终态、且 Runtime 能证明 provider 远端执行仍可查询/恢复 | 无有效本地 execution lease | 无 | `detached_remote` | 远端 provider 仍在执行或等待重新挂接 | 不可 / 不可 |
| 非终态的本地 Agent Run | lease 缺失、失效或 released/lost | 无或已丢失 | `orphaned` | 遗留执行正在恢复 | 不可 / 不可 |
| 非终态 | 存在有效 lease，但 lease 绑定其他 run ID | 任意 | `inconsistent` | Run/lease 关联冲突 | 不可 / 不可 |
| 非终态且有效 lease 归属当前进程 | 同一 run ID | admission coordinator 证明正在注册 | `reserved` / phase=`admitting` | 正在准备本地执行 | 不可 / 不可 |
| 非终态且有效 lease 归属当前进程 | 同一 run ID | 无匹配 registration，且没有合法过渡记录 | `inconsistent` | 本地执行关联丢失 | 不可 / 不可 |
| 读取/解码/SQLite 暂时失败 | 无法裁决 | 任意 | `unknown` | 状态暂不可用 | 不可 / 不可 |

`cancelling`、`terminalizing`、`waiting_for_approval` 与 `waiting_for_question` 仍是 Run 的 durable status；它们不应另起一套 ownership state。有效外部 lease 加 `cancelling` 仍是 `external`，只是 UI 文案应显示“其他进程正在停止”。

`reserved` 是正常的安全占用，而不是数据损坏：它覆盖短时 Session mutation，也覆盖 acquire lease 到 Run admission、durable terminal commit 到 lease release 的合法过渡窗口。`inconsistent` 只用于无法由合法状态迁移解释的关联冲突。

### 5.3 远端 provider execution

`responses_background` 等 provider-native background Run 可以在 MothX owner 进程退出后继续存在。其状态不能只凭 `Run.Source` 推断；Runtime 必须通过 canonical `response_runs`、provider capability、远端 ID/polling 信息与 recovery policy 解析为以下结果之一：

- `attached`：当前进程持有 execution lease 并运行 monitor，按 `local` 处理；
- `detached_remote`：无本地 owner，但远端 Run 可查询、可取消或可重新挂接；
- `remote_terminal_pending`：远端已终态，本地 transcript/Run 终态尚待 fenced 落库，按 `reserved`/`recovering` 处理；
- `unrecoverable`：无法证明远端仍可恢复，取得 recovery lease 后按 orphan policy 终结。

`RecoveryKeepRemote` 不得只返回“保留 durable Run”然后结束；它必须生成 `detached_remote` snapshot，或在返回前成功建立新的 monitor/lease。否则同一 Run 会在每次 inspect 时重新被判为 orphan。

`detached_remote` 默认 `CanSubmit=false`、`CanCancelLocal=false`。若 provider driver 支持受认证的远端取消，则由 Runtime 投影 `CanCancelRemote=true`；适配器仍不能直接调用 provider driver。

### 5.4 历史未绑定 lease 的兼容判定

发布前已经创建的 lease 可能有 `run_id=''`。对于这类记录：

- 若同一 Session 存在唯一非终态 Run 且 legacy lease 仍有效，外部观察者必须保守投影为 `external`，并标记 `linkageState=legacy_unbound`，不得因为未绑定而恢复或终结它；
- 当前 owner 进程若能确认本地注册的 Run，应在下一次受 fencing 保护的 lifecycle 写入时回填绑定；
- lease 已失效且存在非终态 Run 时，按 `orphaned` 处理；
- legacy lease 有效、`run_id=''` 且没有 active Run 时，按 `reserved` 处理，因为它可能是旧版 mutation 或 admission 窗口；
- 出现有效 lease 绑定其他 run ID、多个异常 Run 或无法唯一匹配时，按 `inconsistent` 处理；读库失败时按 `unknown` 处理。两者均禁止提交新 Run。

兼容桥只在安全的 owner 或 recovery lease 内补齐关联，不能由任意观察者修改活跃 lease。

## 6. lease 与 Run 的原子关联

### 6.1 lease purpose 分类与迁移

当前 `TryLockRuntime` 将所有调用统一写为 `purpose=run`，但其调用者既包括完整 Agent 执行，也包括 Session 删除、绑定、解绑、能力修改、Responses 恢复等短时维护操作。新状态模型不能在这一歧义消除前依赖 `purpose=run` 判断执行归属。

目标 purpose 至少分为：

| purpose | 允许绑定 active `run_id` | 典型使用方 | snapshot 语义 |
|---|---:|---|---|
| `admission` | 否；提交事务内转为 execution 并绑定 | 新 Run 的短时准入 | `reserved` / `admitting` |
| `execution` | 是，必须绑定 | Agent、ESM、Cron、Channel、Responses monitor | `local` / `external` |
| `recovery` | 是，恢复目标确认后绑定 | orphan terminalization、remote reattach | `reserved` / `recovering`，完成后转 execution 或 release |
| `mutation` | 否 | delete、bind、unbind、transfer、capability/config mutation | `reserved` |
| `fork` | 否 | Session fork | `reserved` |

应增加显式 purpose 的 lease API，并将所有 production 调用点迁移完成后，再把无 purpose 的 `TryLockRuntime` 降为有删除条件的兼容桥。新代码不得继续通过通用 `TryLockRuntime` 隐式获得 `purpose=run`。

显式 API 还必须把 active Run 检查放入 lease acquisition 的同一事务：

- `AcquireExecutionAdmission`：只有没有 active Run 时才取得 `purpose=admission`；若发现遗留 active Run，返回 `session_recovery_required`，由 Runtime 转入独立 recovery 操作；
- `AcquireRecovery(expectedRunID)`：只有目标 Run 仍非终态且没有其他有效 owner 时，才取得 `purpose=recovery` 并绑定该 run ID；
- `AcquireMutation` / `AcquireFork`：只有没有 active Run 时才成功，不能先取得 mutation lease 再在事务外检查；
- 所有 API 都以 SQLite 当前时间判断 expiry，并通过 owner/token/epoch CAS 完成接管。

这样 active Run 与 admission/mutation/fork lease 同时存在只可能是历史异常或损坏状态，应投影为 `inconsistent`，而不是正常窗口。

数据库中历史 `purpose=run` 视为 `legacy_unspecified`：只有在 `run_id` 已绑定，或当前 owner 能以完整 lease identity 证明本地 Run 时，才按 execution 解释；否则按 `reserved` 或 `legacy_unbound` 保守处理。

purpose 变更只能由当前 lease owner 在同一 epoch/token 下通过 CAS 完成。`recovery -> execution` 必须在成功 reattach 与注册本地 runtime 的受控交接中发生；不得通过 release 后重新 acquire 制造无保护窗口。

### 6.2 关联不变量

对新建的可执行 durable Run，以下关系必须在 admission 成功前成立：

```text
active session_runs(id = R, session_id = S)
        ↕ 同一 SQLite 事务、同一 fencing 校验
active session_runtime_leases(session_id = S, purpose = execution, run_id = R, epoch = E)
```

具体实现可扩展既有 Run admission 事务，或增加由 `ExecutionRuntime.BeginDurable` 调用的受控 store 方法；不得先创建 Run、返回给 adapter，再异步“尽力写入” lease `run_id`。事务必须同时校验当前 owner、token、epoch、lease 未过期和允许转入 `purpose=execution` 的 admission purpose。

lease release 使用 tombstone 语义时可保留最后一个 `run_id` 作为审计关联，但 `state` 与 expiry 必须明确表明它已不再授予执行权。接管新的 lease 时只允许在 expiry/epoch CAS 成功后替换关联。

### 6.3 关键生命周期

```text
Acquire lease
  -> purpose=admission / state = reserved
  -> Begin durable Run + bind lease.run_id + purpose=execution（同一 fencing 事务）
  -> register local ExecutionRuntime with exact lease binding
  -> execute / heartbeat / persist events
  -> Finish durable Run（失败时保持 terminal-persistence-pending）
  -> unregister local runtime
  -> Release lease tombstone
```

以下顺序约束必须成立：

1. 启动前，先取得 `purpose=admission` lease；恢复现有 Run 时取得 `purpose=recovery` lease。若 lease 属于其他有效 owner，返回 `session_busy`，不得启动第二个 Agent。
2. durable admission 与 `run_id` binding 成功后，才允许将 Run 投影为可运行；若事务失败，释放刚获得的 lease，且 adapter 不得保留半初始化 active run。
3. 本地 execution registration 必须保存不可伪造的内部 binding：规范化数据库身份、session ID、run ID、owner instance ID、epoch 与 token identity。只有该 binding 与数据库快照完全一致时，snapshot 才可返回 `local` 和 `CanCancelLocal=true`。
4. heartbeat 或任一次 fenced 写返回 lease lost 时，当前 binding 立即失效并取消 `ExecutionRuntime`；旧 owner 绝不继续把事件、终态或工具结果写回该 Session。
5. `FinishDurable` 的非 lease-lost 失败不可被吞掉。Run 必须进入可观察的“终态持久化待重试”路径，保留 active/blocked 投影与 lease，直到终态事务成功或本进程确定失去 lease。
6. 只有 durable 终态提交成功后，adapter 才能清除其本地 run 引用、pending projection 和本地 cancel function；之后才释放 lease。
7. 如果终态提交因 lease lost 失败，当前进程不得假装完成，也不得直接把 Session 置空；它应使旧 binding 失效、停止本地执行，并交由下一次 snapshot/recovery 裁决为 `external`、`detached_remote` 或 `orphaned`。

该顺序尤其禁止“`FinishDurable` 报错但仍调用 `FinalizeRun`”这一类只清内存、不收敛 durable 状态的路径。

## 7. 停止与恢复语义

### 7.1 统一 Runtime 控制操作

新增或扩展 Runtime-owned 的操作，例如 `RequestSessionStop(sessionID)`。它先取得统一 snapshot，但 snapshot 只是控制操作的预期版本，不是永久授权。真正取消、远端控制或恢复时必须携带预期 `runID + lease epoch + linkage state`，在写入前重新执行 fencing/CAS；冲突时重新 inspect 并返回最新结果。HTTP handler、CLI、TUI、ACP 和 channel 不得各自查询 map 后决定取消。

| 快照状态 | 行为 | 结果代码/语义 |
|---|---|---|
| `local` | 调用对应 `ExecutionRuntime.CancelDurable`，持久化 `cancelling` 并保持 snapshot active，直到终态 | `stop_accepted` |
| `external` | 不调用本地 cancel，不改 lease、不伪造 Run 终态 | `session_run_owned_elsewhere` |
| `detached_remote` | 取得 recovery lease并复核远端记录；支持 provider cancel 时由 Runtime 请求远端取消，否则返回明确的不可控制结果 | `remote_stop_accepted`、`remote_stop_unsupported` 或 `session_busy` |
| `orphaned` | 先尝试取得 recovery lease；成功后按用户停止语义终结；失败则重新 inspect | `recovery_started`、`session_busy` 或 recovery error |
| `reserved` | 不抢占、不误报为运行；返回当前操作正在占用 Session | `session_reserved` |
| `idle` | 不做写入 | `no_active_run` |
| `inconsistent` / `unknown` | 不猜测、不清理、不提交新 Run | `session_execution_state_unavailable` |

这使 `no_active_run` 只在 durable 层确实没有非终态 Run 时返回。它不再被用于表达“本进程没有 APISession”。

对于 HTTP `POST /api/sessions/{sessionID}/stop`，建议映射为：

- `local`：接受取消，返回 `202 Accepted` 和 snapshot；客户端等待后续 Run/runtime event 确认终态；
- `external`：返回 `409 Conflict`，结构化错误码 `session_run_owned_elsewhere`，携带安全的 `runId`、durable status、可选 source 和 lease expiry；
- `detached_remote`：Runtime 成功提交 provider-side cancel 后返回 `202 Accepted`；provider 不支持取消时返回 `409 Conflict` 和 `remote_stop_unsupported`；
- `orphaned`：若成功开始用户触发的终结，返回 `202 Accepted` 与 `recovery_started`；恢复完成由事件或刷新确认；
- `reserved`：返回 `409 Conflict` 和 `session_reserved`，客户端根据 snapshot 刷新；
- `idle`：返回 `409 Conflict`，结构化错误码 `no_active_run`；
- `inconsistent` / `unknown`：返回 `503 Service Unavailable` 或等价 retryable 错误，不将未知状态折叠为空闲。

### 7.2 为什么外部 Run 不能由 UDP 停止

UDP 的可靠性、认证和顺序都不满足取消命令要求。把“stop”做成 UDP 广播会出现丢失、误杀、重放和无审计确认问题，也会让 lease fencing 失效。

这里的“外部 Run”指另一个 MothX 进程持有 lease；它与 `detached_remote` 的 provider-native 后台执行不同。后者可在 Runtime 取得 recovery lease 后使用既有 provider driver 控制。

若未来产品确实要求从任意进程停止另一个 MothX 进程持有的 Run，需另立方案并定义：持久化 `session_execution_commands`（或复用正式 control plane）、命令 ID、目标 run ID/epoch、owner 轮询与 ack、幂等性、超时、权限和审计。该协议的 owner 必须在自己的 fenced lease 下执行 `CancelDurable`。本方案明确不把这一能力作为修复当前不一致的前提。

### 7.3 orphan recovery

orphan 不是“看到 active Run 但本地 map 没有它”。只有在 SQLite 可证明没有仍有效的匹配 run lease 时，才可认为其没有 owner。

有效 lease 绑定其他 run ID 时属于 `inconsistent`，不得进入 orphan recovery；可验证的 provider-native 远端执行属于 `detached_remote`，也不得按普通本地 orphan 直接标记失败。

恢复过程必须如下：

```text
find non-terminal durable Run
  -> inspect current SQLite lease
  -> valid external lease: skip，仍投影 external
  -> no valid matching lease: acquire purpose=recovery lease by CAS
  -> re-read Run + lease under acquired epoch
  -> bind/verify run ID，解析 remote disposition
  -> detached remote: safe reattach / provider cancel / 保持 detached
  -> local orphan: 处理 pending Decision 并持久化 terminal state
  -> publish canonical Run state change，release lease
```

任何一次 acquire、复核或终结写入失败，都不能把 Run 直接标记成功/失败；本地遗留 Run 应保留为 `orphaned`/`unknown`，可验证的远端 Run 应保留为 `detached_remote`，并在受控调度中重试。恢复尝试应记录 recovery reason、旧 epoch 和时间，但不泄露 token。

orphan terminalization 的终态和原因必须区分触发源：

| 触发源 | Run 终态 | 原因 |
|---|---|---|
| 服务启动或周期性自动恢复 | `failed` | `owner_lost` / `process_terminated_while_run_active` |
| 用户对 orphan 点击停止 | `cancelled` | `cancelled_by_user_after_owner_loss` |
| provider 远端取消成功 | `cancelled` | `remote_run_cancelled_by_user` |
| provider 已自行失败或过期 | `failed` / `expired` | 采用 canonical provider terminal reason |

恢复终态操作必须复用 `DecisionService` 和 Runtime-owned store，在同一受 fencing 保护的收敛操作中完成：Run terminal row、terminal event、开放 ConversationTurn、pending Approval/Question 的终态 DecisionRecord。若现有 store 尚不能原子完成，应先扩展共享 Runtime transaction contract；不得由 adapter 先清内存 pending map 再单独终结 Run。

恢复应在启动阶段执行，并可由 UDP lease notification、读取到 orphan snapshot 或周期性轻量协调触发。触发只是优化；每次操作均必须重新读取 SQLite。多个进程同时发现 orphan 时，只有一个能抢到 recovery lease，其余进程投影为外部/处理中而不是并发恢复。

## 8. UDP 与本地内存的定位

### 8.1 UDP

现有 `LEASE_ACQUIRED`、`LEASE_RELEASED`、`LEASE_LOST` 与 `RUN_STATE_CHANGED` 通知仍保留。接收方处理改为：

1. 以 `sessionID` 使本地 execution snapshot cache 和事件 cursor 失效或唤醒刷新；
2. 用 `InspectSessionExecution` 重新读取 SQLite Run 和 lease；
3. 将新的 canonical snapshot 投影到 SSE/WebSocket/HTTP 读取者；
4. 对重复、乱序或来源为自身的通知保持幂等。

UDP 可以在后续增加可选的 `runID` 供诊断/去重，但不得作为与 SQLite `run_id` 绑定等价的权威信息，也不得携带 lease token。

### 8.2 本地执行注册表

每个进程可以在 `internal/agentruntime` 内维护本地 execution registry，用于 `local` 判定和实际取消。注册键不能只有 run ID；必须使用规范化数据库身份、session ID、run ID 和 lease epoch，并在注册值中保留不对外暴露的 owner/token identity。它必须满足：

- 注册仅在 durable admission 与 lease/run binding 成功后发生；
- 注册的 lease identity 必须与 SQLite snapshot 的 owner、epoch 和 token hash 全部相同，才能投影 `CanCancelLocal=true`；
- 同一进程重新取得更高 epoch 时，旧 epoch 的注册项即使尚未清理也不能匹配为 local；
- lease `Lost` 通道关闭时立即使注册 binding 无效，再触发执行取消；
- deregister 仅在 durable terminal state 成功或确认 lease lost 后发生；
- 进程内记录丢失、启动重启或 adapter unload 不得把 durable Run 判定为空闲；
- 注册表不是跨进程锁、不是 durable recovery store，也不允许被 UI 直接查询为权威状态。

Serve 中的 `APISession` 可以成为该注册表的一个实现细节，但不再是 `CancelSessionRun` 的唯一事实来源。

## 9. 适配器与用户界面投影

### 9.1 API

现有 `SessionActiveRun` 应向后兼容地扩展，或由同一 runtime snapshot 新增字段。旧客户端仍可只读 `activeRun.runId` 与 `activeRun.status`；新客户端必须使用 `execution.state` 和 `execution.canCancelLocal` 决定操作。

建议的 JSON 投影：

```json
{
  "activeRun": {
    "runId": "run_...",
    "status": "running",
    "source": "webui"
  },
  "execution": {
    "state": "external",
    "phase": "executing",
    "ownerScope": "external",
    "canSubmit": false,
    "canCancelLocal": false,
    "canCancelRemote": false,
    "linkageState": "bound",
    "leaseExpiresAt": "2026-08-26T10:00:15Z"
  }
}
```

`leaseExpiresAt` 仅用于状态提示和刷新策略，不能被浏览器用作可自行接管的时钟。对于 `unknown`，Run 与 owner 相关字段应保守返回，不可把上一份缓存伪装成事实。

### 9.2 WebUI

WebUI 的 `busy` 应继续对 `reserved`、`local`、`external`、`detached_remote`、`orphaned`、`inconsistent` 和 `unknown` 保守为真，避免同一 Session 被再次提交。交互文案则按状态区分：

| execution state | 输入区 | 停止按钮 | 建议文案 |
|---|---|---|---|
| `local` | 禁止发送 | 可用；点击后显示 stopping | 正在此进程执行 |
| `external` | 禁止发送 | 禁用或替换为提示 | 正在另一进程执行 |
| `detached_remote` | 禁止发送 | provider 支持取消时可用 | 远端任务仍在执行，等待重新连接 |
| `reserved` | 禁止发送 | 禁用 | Session 正被维护或准备执行 |
| `orphaned` | 禁止发送 | 禁用；显示恢复进度 | 正在恢复遗留执行 |
| `inconsistent` | 禁止发送 | 禁用 | 正在确认执行状态 |
| `unknown` | 禁止发送 | 禁用并允许刷新/重试 | 无法确认执行状态 |
| `idle` | 可发送 | 隐藏 | 空闲 |

Session 列表、聊天页、审批视图和重连 SSE 应消费同一 snapshot。外部 Run 的 pending approval 不能被本地 UI 当成可直接响应的本地 waiter；是否跨进程响应审批属于独立 control-plane 问题。

### 9.3 其他入口

- CLI/TUI：显示“由另一进程执行”而不是允许新输入后因锁失败；本地取消只针对本进程的 Run。
- ACP：保持既有协议 projection，但其 run/cancel/recovery 决策改走共享 Runtime snapshot。
- Channel：继续用绑定 Session 与 runtime lease 防止污染；收到外部状态后不复制 Run 生命周期。
- OpenAI-compatible API：保留现有响应格式的兼容字段，并通过结构化 error code 表达 external ownership 和 unknown state。

## 10. 实施分期

### Phase 0：合同与观测基线

1. 为当前“active durable Run / external lease / current-process APISession”组合补充诊断日志和测试夹具。
2. 枚举所有 `TryLockRuntime`/`LockRuntime` 调用点，明确 admission、execution、recovery、mutation、fork purpose，并将通用 `purpose=run` 视为待删除兼容桥。
3. 明确并固化 non-terminal Run status 集合、lease valid 判定、合法 admission/release 过渡窗口和 remote execution disposition。
4. 在架构测试中禁止新增 adapter-local 的 Run/lease 状态判定。

### Phase 1：数据层与 Runtime snapshot

1. 在 `internal/session` 增加同一只读事务中的 execution facts snapshot，以及受控 lease/run binding transaction helper；不向 adapter 暴露 token。
2. 引入显式 lease purpose API，先迁移 mutation/fork/recovery 调用点，再使 execution admission 独占 `purpose=execution` 语义。
3. 将 durable admission 中的 Run 创建、`lease.run_id` binding 与 purpose transition 纳入同一 fenced transaction。
4. 在 `internal/agentruntime` 实现 `InspectSessionExecution`、带完整 lease identity 的本地 execution registration，以及 detached remote resolver。
5. 为历史 `purpose=run`、`run_id=''` 租约实现保守兼容路径。

### Phase 2：生命周期收敛

1. 让 `BeginDurable`、`ReattachDurable`、`UpdateDurable`、`CancelDurable`、`FinishDurable` 与 `SessionRuntime.Shutdown` 使用统一 binding/registration 顺序。
2. 移除或改造忽略 `FinishDurable` 错误后仍清理 adapter 状态的路径。
3. 将 orphan recovery 改为 lease-first、re-read、fenced terminalization，并按触发源收敛 Decision/Turn/Run 终态；启动恢复和后续协调复用同一操作。
4. 将 Responses background 的 keep/reattach/cancel 解析为 Runtime-owned `detached_remote` lifecycle，不再让 adapter 私有状态决定是否 orphan。

### Phase 3：适配器投影和停止接口

1. 由 shared Runtime snapshot 驱动 Serve `runtimeSnapshot`、Session 列表和外部 session 更新。
2. 将 `CancelSessionRun` 降为 Runtime 控制操作的薄包装，按统一结果映射 HTTP 错误。
3. 更新 WebUI busy、停止按钮、错误文案和 SSE refresh；再依次迁移 CLI/TUI/ACP/Channel 投影。
4. 保持旧字段和现有成功响应兼容一个发布周期；新增字段与结构化错误码不改变旧客户端的读取能力。

### Phase 4：清理与强化

1. 删除仅靠 `APISession.IsRunning()` 判断 active Run 的兼容分支。
2. 删除 production 调用点对无显式 purpose 的 `TryLockRuntime` 依赖，以及未绑定新 Run 的正常 execution 路径；保留历史兼容读取直到迁移窗口结束。
3. 补充跨进程故障恢复、race、架构守卫和全入口合同测试。

每个阶段完成后都必须保持：没有任何 adapter 能绕过 `ExecutionRuntime` 创建、取消、终结或恢复 durable Run。

## 11. 测试与验收标准

### 11.1 数据层和 Runtime 测试

1. **原子 admission**：Run、started event、lease `run_id` 与 epoch fencing 要么同时可见，要么全部不可见。
2. **外部 owner 投影**：子进程持有有效 lease 并运行时，父进程 snapshot 为 `external`，且 `CanSubmit=false`、`CanCancelLocal=false`。
3. **本地 owner 投影**：同一进程匹配 lease/run/registered runtime 时 snapshot 为 `local`，停止进入 `cancelling`，最终终结。
4. **epoch 精确绑定**：同一进程丢失 epoch N 并取得 epoch N+1 后，epoch N 的旧 ExecutionRuntime 不得投影为 local，也不能取消新 Run。
5. **purpose 隔离**：delete/bind/unbind/config mutation/fork 的有效 lease 投影为 `reserved`，不显示为 running 或 `inconsistent`；新 execution 才使用并绑定 `purpose=execution`。
6. **合法过渡窗口**：lease acquire 到 Run admission、Run terminal 到 lease release 的窗口显示 `reserved/admitting/releasing`，不触发告警或 orphan recovery。
7. **lease/run mismatch**：有效 lease 绑定其他 run ID 时返回 `inconsistent`，任何 observer 都不能自动 terminalize。
8. **事务快照**：在 Begin/Finish/release 并发点反复 inspect，结果只能是事务中真实存在的组合，不能拼接出伪 orphan。
9. **detached remote**：可恢复 Responses background Run 在无本地 lease 时投影为 `detached_remote`；reattach、provider cancel 和不可恢复降级分别收敛到规定状态。
10. **lease expiry**：持有者被杀死或心跳停止后，在 TTL 前仍显示 external；TTL 后只允许一个竞争者获得 recovery lease。
11. **fencing**：旧 owner 在被接管后不能写 Run event、终态、Decision 或 transcript；其本地执行收到 lease lost 后取消。
12. **legacy unbound lease**：有效外部空 `run_id` lease 不会被错误终结；失效后才可进入 orphan recovery。
13. **finish persistence failure**：模拟 terminal event/store 失败时，不能提前 deregister 或让 snapshot 变 idle；重试成功后才清理。
14. **recovery race**：两个进程同时发现 orphan，只允许一个写终态/attach；另一个重新读取并停止操作。
15. **orphan 终态语义**：自动恢复写 `failed/owner_lost`，用户停止写 `cancelled/cancelled_by_user_after_owner_loss`，且 Decision、Approval/Question、ConversationTurn 与 Run event 完整收敛。

### 11.2 API 与 WebUI 测试

1. Session 列表与聊天 runtime snapshot 对同一 external Run 都显示 running，且不提供错误可用的 Stop。
2. external Run 的停止请求返回 `session_run_owned_elsewhere`，而不是 `no_active_run`；Run、lease 和外部执行均不受影响。
3. local Run 的停止请求返回 `202`，在 durable terminal event 后才恢复输入。
4. detached remote Run 显示为远端执行；支持 provider cancel 时 Stop 返回 `202`，不支持时返回 `remote_stop_unsupported`。
5. mutation lease 与 admission/release 窗口显示 reserved，不显示“另一进程正在执行 Agent”。
6. orphan Session 显示恢复中，不能重新提交；恢复成功后变 idle 或安全 reattach 后继续显示 active。
7. SQLite 读失败、lease 解码失败等 unknown 状态不应把 UI 解锁；刷新恢复后应回到正确状态。
8. Stop inspect 后发生 epoch takeover 时，旧请求的 CAS 失败并重新投影最新 owner，不能取消新 Run。
9. UDP 通知丢失时，轮询/重连后的 SQLite snapshot 仍能收敛；重复和乱序通知不改变最终状态。
10. 同一 Session 的 WebUI、CLI/TUI、ACP、channel 投影具有相同 Run ID、状态、归属范围与提交禁止语义，仅协议/UI 格式不同。

### 11.3 架构验证

除 `internal/session`、`internal/agentruntime` 的明确 allowlist 外，静态架构测试应拒绝：

- 适配器直接读取 `session_runtime_leases` 后自行决定 local/external/orphaned；
- 适配器通过本地 `IsRunning()` 直接把 durable Run 解释为空闲；
- production 调用点继续用无显式 purpose 的 lease API 创建新的隐式 `purpose=run`；
- 新增 adapter-owned recovery loop、run store 或取消 state machine；
- UDP handler 直接改写 Run、lease 或执行取消。

实现阶段至少运行相关 `internal/session`、`internal/agentruntime`、`internal/serve/openaiapi`、`internal/acp`、`internal/serve/channels` 与 `internal/architecture` 测试；跨进程场景使用真实子进程和独立 temp Session DB，不以 mock UDP 代替 lease/fencing 验证。

## 12. 失败模式与可观测性

应为每次 snapshot 判定和状态转移提供结构化诊断（不含 token）：

```text
session_id, run_id, run_status,
execution_state, owner_scope, lease_purpose, lease_epoch,
lease_expires_at, linkage_state, remote_disposition,
transition_reason, recovery_attempt
```

建议增加计数器：

- `session_execution_snapshot_total{state}`；
- `session_execution_stop_total{result}`；
- `session_execution_orphan_recovery_total{result}`；
- `session_execution_terminal_persistence_retry_total`；
- `session_execution_lease_run_linkage_total{state}`；
- `session_execution_remote_total{disposition,result}`。

告警和日志应重点关注：长期 `orphaned`、排除合法 admission/release 窗口后仍存在的 `inconsistent`、terminal persistence retry 持续失败、lease lost 后仍有旧 epoch 写入尝试，以及 external 状态被错误映射成 `no_active_run`。`reserved` 和有进展的 `detached_remote` 是正常业务状态，不应直接作为异常计数。

## 13. 开放问题

1. `owner_kind` 是否应由当前泛化的 `process` 提升为稳定的 Runtime source 分类，需要与 source/policy resolver 的既有枚举对齐后再决定；不应让 UI 依赖进程类型字符串。
2. 是否提供管理员级跨进程停止能力，应单独评审其认证、审计、幂等与 owner ack 合同；在此之前 external stop 明确返回冲突。
3. snapshot 缓存的时长和 SQLite 读压策略可在实现时压测决定，但缓存失效只能导致重新读取，不能在安全上把 external、detached_remote 或 orphaned 降级为 idle。
4. detached remote 的具体 reattach/cancel capability 由各 provider driver 实现，但 capability 判定、状态机和持久化结果必须进入共享 Runtime contract；不得在 Serve adapter 留下第二套判断。

## 14. 结论

本问题的根因不是 UDP、SQLite 或内存锁中某一个单点“失效”，而是已有三层机制没有被投影为同一个执行语义：durable Run 表示运行事实，SQLite lease 表示跨进程执行归属，本地内存表示本进程控制能力。把它们压缩成单一 `running` 或单一 `APISession.IsRunning()` 都会产生误判。

本方案通过 Runtime-owned 事务快照、显式 lease purpose、带 epoch/token identity 的 lease/run 关联、detached remote 状态、lease-first orphan recovery 和按归属分类的停止结果，保留“外部有效执行仍是 running”这一正确并发保护，同时使用户和调用方获得可解释、可恢复且不互相污染的行为。
