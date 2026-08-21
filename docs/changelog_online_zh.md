# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.90

### ✨ 新功能

- **ACP 会话配置选项**
  - ACP 新增 `session/set_config_option` 和 `session/set_mode` RPC 方法，客户端可在不重建 agent 或 provider 的情况下更改每个会话的模型、模式和思考级别。
  - 每个会话在 `session/new`、`session/load` 和 `session/resume` 结果中携带一致的 `configOptions` 目录，`session/set_config_option` 会通知所有已连接的客户端更新后的选项。
  - 配置持久化到会话历史（`model_change`、`mode_change`、`thinking_level_change` 条目），在会话加载时回放，确保绑定在重启后和跨多个适配器共享同一会话时保持不变。
  - 稳定的流式消息 ID（`agent_message_chunk`、`agent_thought_chunk` 和 `user_message_chunk` 更新上的 `messageId`），将每个 prompt 回合的块分组为逻辑消息。

- **ACP 附加目录支持**
  - `session/new`、`session/load` 和 `session/resume` 接受 `additionalDirectories` 数组（绝对路径的工作区根目录）。工具注册表在这些根目录内解析路径，沙箱将其挂载为只读（严格模式）或可写路径。
  - 目录集作为 `additional_directories` 会话条目持久化，并在重新加载时恢复。
  - 会话列表端点（`session/list`）为每个会话暴露 `additionalDirectories`。

- **ACP 标准 Elicitation 表单协议**
  - 当客户端在能力声明中声明 `elicitation.form` 时，问题请求使用标准 ACP `elicitation/create` 方法，而非遗留的 `_mothx/request_question` 扩展，并带有类型化的 `requestedSchema` 信封。
  - 回放时，若重连客户端未声明表单支持，则优雅回退到旧扩展格式。

- **ACP 文件差异协议投射**
  - 工具调用更新现在包含 `diff` 内容类型，包含 `path`、`oldText` 和 `newText` 字段用于语义差异表示，同时保留现有文本内容。新建文件时 `oldText` 为 `null`。
  - 工具调用更新在存在差异时携带 `locations` 包含受影响文件路径。

### 🔧 改进

- **会话模式与思考级别持久化**
  - 新增 `EntryModeChange` 和 `EntryAdditionalDirectories` 会话条目类型，将模式切换和目录绑定记录到会话历史，实现完整回放和跨会话一致性。
  - `SessionRuntime` 拥有 `Model`、`Mode`、`ThinkingLevel` 和 `AdditionalDirectories` 作为会话级绑定，提供 `ConfigureSession`、`ConfigSnapshot`、`SetConfigOption` 和 `SetAdditionalDirectories` 方法实现原子读写。
  - `BuildAgent` 在适配器未提供覆盖时继承会话级模型、模式和思考级别，确保 ACP、TUI 和 WebUI 间一致的 Agent 构建。

- **ACP 严格 JSON-RPC 2.0 校验**
  - ACP 服务器现在强制要求：`initialize` 必须在任何其他方法之前调用、`initialize` 只能调用一次、空消息静默跳过、通知（空 ID）不发送响应、请求 ID 校验为 JSON-RPC 标量类型。
  - `cancel` 需要 `sessionId` 参数，对未知会话返回错误。

- **ACP 跨连接唯一运行 ID**
  - Prompt 运行 ID 现在包含随机后缀，防止 ACP SDK 请求 ID 在连接间重复时产生运行 ID 冲突。

- **CI 发布说明使用更新日志**
  - GitHub release 工作流现在使用 `docs/changelog_online_en.md` 作为发布正文，而非自动生成的发布说明，确保发布携带精心编写的更新日志。

- **FileDiff 新增 OldText/NewText**
  - `FileDiff` 现在保留完整的文件内容（`OldText` 和 `NewText`），用于需要语义差异而非展示补丁的协议投射。新建文件时 `OldText` 为 `nil`。

- **ContentBlock 文件字段扩展**
  - `FileContent` 新增 `Title`、`Description` 和 `Size` 字段，ACP `resource_link` prompt 类型将这些字段映射到 provider 中立的文件表示。

### 🐛 修复

- **ACP 空 Prompt 主体拒绝**
  - 仅包含空文本的 prompt 现在会被拒绝，返回 `empty prompt` 错误，而非进入 agent 循环。

- **ACP 不稳定测试与 CI 稳定性**
  - 修复了 wrapped thinking text 断言和 CI 中不稳定的 Go 测试。
