# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.100

### ✨ 新功能

- **WebUI：聊天输入框斜杠命令建议**
  - 在聊天输入中键入 `/` 即弹出建议下拉框，覆盖全部支持的斜杠命令（`/clear`、`/mode`、`/model`、`/defaultModel`、`/models`、`/sessions`、`/status`、`/compact`、`/delegate`、`/alloweditpath`、`/allowautoedit`、`/workflows`、`/skill`、`/skills`、`/rule`、`/esm`、`/help`），并对 `/esm` 提供专门的子命令过滤（objective/edit/pause/resume/clear/guide）。
  - 使用 ↑/↓ 导航，Tab 或 Enter 补全（当输入与选中项完全一致时 Enter 直接发送），Esc 关闭，或点击选中；选中后光标定位到插入命令的末尾。输入框保持完整的 combobox/listbox 无障碍状态（`aria-expanded`、`aria-activedescendant`、`aria-selected`）。
  - 运行进行中、API 被禁用或输入包含换行时不显示建议。

### 🐛 问题修复

- **TUI：运行期间提交的提示词排队执行，不再顶替当前运行**
  - 同一会话同一时刻只允许一个前台执行。此前在运行进行中提交输入会直接替换内存中的运行句柄，导致活跃运行的终态清理与运行时租约被孤立。现在此类提交会在 TUI 中排队，仅当前一个运行到达规范终态并释放租约后，才启动下一个排队提示词 —— 覆盖所有终态分支（成功、失败、未完成与取消）。
  - 排队的提示词保留 Runtime 预制的附件（`agentruntime.PreparedInput`），并通过同一输入契约重新提交，附件在延迟期间保持不变。

### ✅ 测试

- TUI：新增测试断言运行期间提交的输入仅排队而不替换租约持有者，且排队提示词只有在取消流程完成持久化运行终态并释放租约之后才会启动。
