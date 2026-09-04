# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.99

### ✨ 新功能

- **WebUI 与 TUI 统一使用服务端解析的模型目录**
  - 新增 `GET /api/models/catalog` 端点，通过 `providerfactory.ResolvedModels`/`SortProviderIDs` 解析全部可选 Provider/模型 —— 与构建 TUI Provider 模型列表使用的是同一套共享逻辑 —— 并返回规范化默认值与排序后的 Provider 列表。即使当前活跃 Provider 来自内置预设或 serve 参数而非 settings 的 providers 映射，也依然可选。
  - `stores.js` 由 `models` 迁移为 `modelCatalog`；Chat 新会话选择器改为直接消费服务端目录，不再在客户端合并原始 settings JSON，移除本地 `buildModelCatalog`/settings 兜底推导。设置页各入口（默认 Provider/模型下拉、Provider 编辑器）消费同一 store，继承的内置预设模型新增专用中英文标签。
  - TUI 认证对话框的 Provider 排序委托给 `providerfactory.ProviderSortPriority`，TUI 对话框与 WebUI 目录共用同一套排序逻辑。

- **Enable Supervisor Mode（ESM）：斜杠命令控制、共享引导与证据追踪**
  - WebUI 的 ESM 控制改为与 TUI 相同的 `/esm` 斜杠命令（`/esm <objective>`、`/esm status|edit|pause|resume|clear|guide`），不再使用专用图形控件 —— 移除 500 行的 `ESMControls` 组件，聊天输入与 ESM REST API 共享同一条服务端 objective 路径。
  - 新增引导（guidance）模块：`/esm guide <text>` 将用户引导排队并打上 objective 当前版本戳；Supervisor 将待处理引导注入每个非恢复角色（role）的提示词，并在角色结果应用后恰好消费一次 —— 由核心统一持有生命周期，TUI 与 WebUI 适配器共享。
  - 新增证据（evidence）模块：共享的 `EvidenceTracker` 按角色运行累积工具调用证据（唯一工具调用 ID、按工具的计数/错误），使 `ApplyWorkerResult`/`ApplyReviewResult` 中“工具支撑证据”校验不会在适配器之间产生分歧。
  - ESM 不再强制 token/时间预算：移除 `budget_limited` 状态、`SetBudget`/预算提示词与 TUI `/esm budget` 子命令；`TokensUsed`/`TimeUsedMS` 仅作可观测性计数。阻塞审计阈值集中为 `BlockedAuditLimit`（连续 3 次运行报告相同 blocker 后置为 blocked）。
  - 无人值守派生运行通过 `agentruntime.ResolveUnattendedMode` 解析执行模式：仅 `os` 从会话模式继承，其余会话模式一律回退为 `yolo`，ESM 角色子代理不会因交互式审批而停摆（高危命令硬防护仍与模式无关）。
  - Supervisor 以基础 Run ID 执行 continuation（角色运行使用由其派生的带后缀 ID），由 Supervisor 统一持有两个适配器的终态 `FinishRun` 调用，并在 continuation 结束时清理更早 continuation 遗留的过期连续计数。

### 🐛 问题修复

- **ESM：失败 objective 转为暂停，杜绝静默重跑**
  - 不可重试的角色失败现在会将 objective 置为暂停，须显式 `/esm resume` 才能再次运行 —— 排队的引导或后续触发器不再静默重跑已失败的任务。超时与可重试的传输错误仍走受 `RecoveryLimit` 约束的恢复路径。
  - Serve 启动时不再重放历史上持久化为 "active" 的 ESM objective：角色可能在进程退出前刚刚失败，因此 `Create`/`Edit`/`ResumeESM` 成为仅有的显式执行入口。
  - `esmCoordinator` 新增带 done 通道与有界等待的 `stop`/`stopAll`；Serve 关闭时取消所有 ESM worker 并等待其释放会话/运行时引用（`SessionRuntime.Shutdown` 仍是最终资源边界），已关闭的协调器拒绝启动新 worker。

- **无桌面服务器上的原生目录选择器**
  - Unix 环境下缺少 `DISPLAY`/`WAYLAND_DISPLAY` 时，原生选择器明确报告不可用而非静默失败，让 Web UI 回退到内置目录浏览器。启动失败且 stderr 带诊断信息的场景现在会作为错误暴露，不再被误判为对话框取消。

### 🔧 改进

- **目录浏览器：Windows 盘符根目录与按路径解析的允许根**
  - `/api/browse` 的允许根（allowed-root）解析现在接收请求路径；Windows 各盘符根目录通过虚拟浏览根列出（盘符根之间没有可供导航的公共父目录）。`DirBrowser` 新增 `initialPath` 属性、一次性打开语义、服务端返回的 `selectable` 标志与刷新支持。

- **工具恢复审计记录**
  - `RequestToolExecutionRecoveryRecords` 记录用户的显式确认并仅返回匹配的中断调用；记录作为审计证据保留，恢复则以全新执行开始。新增 DAO 列表接口（`ListRequestedToolRecoveries`）向 Serve 暴露已请求的恢复记录。终态 Run 绝不会被重新激活来消费这些记录。

### ✅ 测试

- ESM：新增/扩充引导生命周期（版本戳、注入、一次性消费）、证据追踪、不可重试失败即暂停、基础 Run ID continuation、预算移除、`/esm` 斜杠命令一致性，以及协调器 `stop`/`stopAll`（含已关闭协调器拒绝启动）的覆盖。
- Serve：进程级测试断言启动绝不重放历史 ESM objective；browse-root 测试覆盖允许根解析与 Windows 盘符根列出；原生选择器测试覆盖无桌面不可用与 stderr 诊断场景。
- Provider factory：目录解析与 Provider 排序测试；settings：`qwen3.8-max-0902` 预设断言。
