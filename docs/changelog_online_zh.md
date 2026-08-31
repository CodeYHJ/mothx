# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.96

### ✨ 新功能

- **运行时工作区输入物化**
  - 现在所有入口点（CLI、TUI、WebUI/API、ACP、微信、飞书）的用户文件统一由一个前端无关的输入契约负责。适配器提交源流，`internal/agentruntime` 将接收的文件物化到项目工作区，首条用户消息声明文件路径与元数据，由 Agent 决定是否以及如何读取每个文件。
  - 图片不再在摄入时就自动转换为厂商图片内容；输入资源通过 `input_resources` 表持久化，并拥有 Runtime 托管的完整生命周期（`PrepareInput`/`AttachPreparedInput`、丢弃/删除/清理、`input_resource_events`）。
  - TUI 的 `/paste-image` 现在提交由 Runtime 写入的流；Web UI 新增聊天附件上传/预览；ACP 的提示内容（文本/图片/文件/音频/视频）与微信/飞书入站媒体都统一走同一入口。

- **基于租约的执行准入与孤儿运行恢复**
  - 旧的 `TryLockRuntime`/`TryLockRuntimes` 路径被显式、按用途和运行绑定的运行时租约取代，覆盖 CLI、TUI、ACP、Serve、通道和定时任务：带引用计数的 durable 租约守卫（`AcquireExecutionAdmission`/`AcquireFork`/`AcquireMutations`、运行绑定）以及针对陈旧/孤儿运行的恢复与协调模式。
  - 新增 `RecoveryCoordinator`，通过启动扫描和周期/唤醒驱动的重试以租约优先方式收敛孤儿运行，并由 `session_run_recoveries` 表提供持久化状态、重试计数与幂等重放。
  - 会话运行时快照现在暴露准入/恢复事实（`reserved`、`local`、`external`、`detached_remote`、`orphaned`、`recovery_failed`、`inconsistent`）；Web UI 显示对应状态徽标，并在会话忙碌时禁用删除/fork。

- **Durable 投递发件箱**
  - 新增投递意图与有序操作（`delivery_intents`/`delivery_operations`），支持确定性的 `PlanDelivery` 序列（caption/上传/发送/回退）、Runtime 的 claim/fence/重试协调器、助手消息/Run/回合/事件/意图的终态原子提交，以及服务启动恢复。
  - 微信（图片/视频/文件）与飞书（图片/文件）出站媒体通过冻结的传输上下文原生投递；发布产物移动到工作目录之外的私有存储，打开时校验完整性（大小 + SHA-256）。

- **幂等的运行提交**
  - 新增 `runtime_submissions` 表，冲突时进行对账处理：submit-key 冲突复用已有提交而不是创建重复记录，使 Run 准入具备重试安全性。

- **火山引擎新增模型：`glm-5.3-flash`**
  - `volcengine`、`volcengine-agentplan` 和 `volcengine-codingplan` 三个提供商均新增 `glm-5.3-flash`，支持 1M 上下文窗口与文本/图片输入；与 `glm-5.3` 一致，默认不发送 max_tokens。

### 🔧 改进

- **仅 DAO 的 SQL 迁移**
  - `internal/db` 现在统一管理进程级 SQLite/Bun 连接生命周期与事务边界；会话、定时任务、用量统计、ESM 与投递相关 SQL 全部迁移到 `internal/dao` 持久化对象。
  - 移除 `internal/commondb` 兼容包与投递遗留桥接；架构守卫以最小迁移归属 allowlist 强制 DAO-only 边界。

- **Web UI 加载与状态稳定性**
  - 路由视图（Chat/Sessions/Stats/Cron/Skills/Settings/Login）改为懒加载，只拉取当前路由的 chunk；lucide/bits-ui/svelte 依赖合并为稳定的 vendor chunk。
  - 会话运行时状态（加载/PATCH/轮询/模式切换）抽成可单测的管理器；历史快照逐字段合并，陈旧的持久化投影不会覆盖实时助手文本。

### 🐛 问题修复

- **缓存输入 token 重复计费**
  - 用量统计现在通过 `UncachedInputTokens` 正确计算未缓存输入 token，避免在 Anthropic、OpenAI 兼容与 Google 三种线上格式中重复收取缓存读取费用。

- **ListSessionRuns 连接死锁**
  - `ListSessionRuns` 在 `session_runs` 行循环内查询 `input_resources`，在单连接池（MaxOpenConns(1)）下永久阻塞，导致带运行记录的会话在 TUI 启动时挂起。现在先耗尽外层行再一次性批量查询，并新增断言其正常完成的回归测试。

- **Durable 运行终态事件稳定性**
  - `RunExecutor.Finalize` 不再为 durable 运行发布终态流事件；`FinalizeRun` 在 `FinishDurable` 提交助手消息后保持唯一发布者，避免 WebUI 历史重载与数据库写入竞争。当内存标记已清除时从规范 Run 行恢复 durable 身份；终态化期间容忍已关闭的会话回合，幂等重试仍能提交最终条目与终态事件。

### ✅ 测试

- 架构：`input_contract_guard_test` 在 TUI、CLI、WebUI/API、ACP 与 Channel 入口点强制单一输入契约。
- 扩展准入/恢复测试：租约优先的孤儿收敛、执行快照、停止处理、幂等性与跨进程租约行为；投递进程集成测试覆盖 claim/fence/重试与协调器恢复。
- 新增缓存输入 token 计费与 `ListSessionRuns` 死锁的回归测试。