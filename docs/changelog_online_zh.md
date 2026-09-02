# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.98

### 🔧 改进

- **TUI：死代码清理与决策生命周期对齐**
  - 移除未使用的函数（`renderLiveAssistantMessage`、`renderPlanPanel`、`formatPlanForDisplay`、`normalizeHistoryLineEndings`、`resolveESMStoreDir`、`updateViewportContentWithFollow`），并精简对应的 plan/ESM 测试覆盖。
  - 问题请求现在通过 `DecisionService` 注册，挂起的问题与审批一样可持久化、可重放；重复的问题请求会被拒绝。
  - 终态决策状态根据实际运行状态映射，不再一律将挂起决策标记为 `cancelled` —— 只有显式取消才记录 `cancelled`，其它任何终态记录 `timed_out`。
  - 延迟打印循环新增 `stopPrintLoop` 退出路径，退出与重载前排空已排队的转录内容再清理。
  - 外部状态行刷新推迟到实际渲染时执行，将高密度事件突发合并为一次刷新。
  - `sessionsDel` 改为通过防御性的 `getSessionDir()` 辅助函数解析会话目录。

### ✅ 测试

- TUI：新增问题决策注册（挂起类型、持久化、重复拒绝）与 ESM 存储目录解析测试。