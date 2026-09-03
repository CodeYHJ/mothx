# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.98

### ✨ 新功能

- **Gitee/Moark 新增模型：`qwen3.8-max-0902`**
  - `gitee` 和 `moark` 两个提供商均新增 `qwen3.8-max-0902`，支持 1M 上下文窗口、128K 最大输出与文本/图片输入。

### 🔧 改进

- **TUI：死代码清理与决策生命周期对齐**
  - 移除未使用的函数（`renderLiveAssistantMessage`、`renderPlanPanel`、`formatPlanForDisplay`、`normalizeHistoryLineEndings`、`resolveESMStoreDir`、`updateViewportContentWithFollow`），并精简对应的 plan/ESM 测试覆盖。
  - 问题请求现在通过 `DecisionService` 注册，挂起的问题与审批一样可持久化、可重放；重复的问题请求会被拒绝。
  - 终态决策状态根据实际运行状态映射，不再一律将挂起决策标记为 `cancelled` —— 只有显式取消才记录 `cancelled`，其它任何终态记录 `timed_out`。
  - 延迟打印循环新增 `stopPrintLoop` 退出路径，退出与重载前排空已排队的转录内容再清理。
  - 外部状态行刷新推迟到实际渲染时执行，将高密度事件突发合并为一次刷新。
  - `sessionsDel` 改为通过防御性的 `getSessionDir()` 辅助函数解析会话目录。

- **WebUI：设置页模型列表与新会话选择器保持一致**
  - 设置页的“默认 Provider / 默认模型”下拉改为复用新会话模型选择器背后的服务端解析目录（`GET /api/models/catalog`），因此内置预设 Provider 及其默认模型也可在此选择；表单中尚未保存的编辑会追加在目录之后，可在保存前直接选用。
  - 供应商设置同样复用该目录：左侧模型数量徽标显示解析后的模型数，模型列表额外以只读行展示未写入配置的内置预设模型（与新会话选择器一致，保存时不会持久化；添加相同 ID 的模型即可覆盖）。

### 🐛 问题修复

- **Serve Responses 恢复保持 Run 终态不变**
  - 恢复经用户确认的中断工具调用时，不再把已完成或失败的 durable Run 改回 `queued`，也不再重新接管其已终止的远端 Responses 任务。Serve 现在通过正常 Runtime 输入路径提交一条新的、幂等的恢复消息，并启动新的本地 AgentLoop Run；原 Run 保持不可变。
  - 移除仅供恢复使用的“终态转活跃”Run 存储 API，防止后续调用方绕过规范的单调生命周期。

### ✅ 测试

- TUI：新增问题决策注册（挂起类型、持久化、重复拒绝）与 ESM 存储目录解析测试。
- Serve：Responses 恢复测试覆盖原终态 Run 保持不变、新 AgentLoop 收到恢复消息，以及重复恢复请求幂等返回同一个新 Run。
