# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.91

### ✨ 新功能

- **ACP 会话准入控制**
  - ACP prompt 现在先获取共享的会话运行时锁并检查持久化的活跃运行记录，再开始运行，使 ACP 入口与 TUI、WebUI 和消息通道串行化。
  - 运行时锁在已准入运行的整个生命周期内持有，其他适配器无法用新运行抢占其终态持久化；存在活跃运行的会话会被拒绝，返回 `session already has an active run`。

- **WebUI 服务端分配的会话 ID**
  - WebUI 现在通过 `POST /api/session-id` 从服务器请求会话 ID，而非在浏览器端生成，使会话身份保持规范统一，同时保留延迟创建的"新建对话"体验。
  - 服务端分配会对保留中及已存在的会话 ID 去重，并在 10 分钟后清理过期保留；浏览器端随机 ID 生成仅保留用于运行请求键。

- **会话重复 ID 拒绝**
  - 使用已存在的 ID 创建会话现在会失败并返回 `ErrSessionIDExists`，而不会将新头部静默合并进旧会话导致对话分叉。
  - 自动生成的 ID 在冲突时会重试（最多 8 次）；会话头部写入改用普通 INSERT，从而可靠地检测重复 ID。

### 🔧 改进

- **默认模式改为 YOLO**
  - 新安装和空 mode 回退现在使用 `yolo` 而不是 `agent`：包括 `settings.json` 的 `defaultMode`、Serve/API `DefaultMode`、CLI/TUI/ACP/WebUI、公共 SDK `Builder`，以及 `agentruntime` 的策略解析。
  - 显式 `--mode`、已持久化的会话 mode，以及微信/飞书强制 `yolo` 仍然优先。已有配置中的 `defaultMode: "agent"` 不会被改写。

- **统一会话创建**
  - TUI、serve 和 CLI 现在通过 `agentruntime.CreateSession` 创建会话，取代直接的 `session.New(...).Init()`，将会话创建集中到共享运行时。
