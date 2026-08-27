# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.95

### ✨ 新功能

- **新增 Gitee/Moark 模型：`qwen3.8-flash`**
  - 支持 1M 上下文、文本/图片输入，默认不发送 max-token 上限。

- **CLI 持久化运行**
  - CLI `runPrint` 现在通过 `agentruntime.ExecutionRuntime` 持久化规范的 durable run，与 WebUI、消息通道和 ACP 的运行生命周期保持一致。

- **UDP 运行时租约总线**
  - 新增尽力而为的 UDP `SessionLeaseBus`，在运行时租约和运行状态变化时唤醒本地进程。
  - 使用定向回环广播和去重；SQLite 租约与 durable 记录仍是唯一权威。

- **按运行选择厂商/模型（API 与 Web UI）**
  - `POST /v1/responses` 运行新增可选 `provider` 字段；支持解析并校验 `provider/model` 限定 ID，厂商与模型不匹配时返回结构化错误。
  - 运行使用请求厂商的 Agent，通过共享 `SessionRuntime` 构建；运行策略快照与请求指纹记录厂商。
  - `/v1/models` 现在返回每个模型的所属厂商。

### 🔧 改进

- **事件代理重同步**
  - 事件代理新增 `SubscribeWithResync`；订阅者溢出时关闭 WebSocket，让客户端重连并重放 durable SQLite 游标。

- **运行时租约心跳**
  - 租约心跳现在对瞬态 SQLite 失败进行有界重试，并发布 `acquired`/`released`/`lost` 通知。

- **会话能力开关（沙箱/浏览器/联网搜索）**
  - `SessionRuntime` 新增 `CapabilitySnapshot`、`ConfigureCapabilities` 与 `SetCapabilityOption`；浏览器与联网搜索能力通过 `session_capabilities` 持久化并在加载时重放，同时同步核心工具。
  - ACP 会话在运行时租约下恢复持久化能力与额外目录；沙箱仍由进程策略拥有。

- **Web UI 厂商感知的模型选择器**
  - 新增可搜索的 `ModelPicker` 组件，带文本/图片/音频/视频/文件模态图标，替换原有模型菜单。
  - 聊天输入框基于 `/v1/models` 与已配置厂商构建级联模型目录，选择厂商后模型列表随之收窄，并在每次运行提交时携带厂商。

- **ACP 会话扩展方法**
  - 新增 `session/fork` 与 `mothx/session/setTitle` 处理、工作区窗口协商（`cwd`/额外目录）、fork 血缘级联删除以及 `available_commands_update` 通知。
  - 可选的编辑器上下文以有界的不可信上下文块注入；已释放运行时租约的历史会话加载时不再改写持久化绑定。

### 🐛 问题修复

- **后台运行乐观并发**
  - 在 durable admission 之后重新加载共享会话管理器，使后台协调器将用户消息附加到新叶子，而不是因乐观并发校验失败。

- **微信 iLink 入站连接恢复**
  - 微信适配器现在发送 iLink 上线/下线生命周期通知；仅在 `getupdates` 轮询成功后报告已连接；采用服务端建议的长轮询超时，并在重启后恢复 iLink 同步游标。
  - 请求现在携带 MothX 通道版本和 bot agent；iLink 返回畸形响应时会显式报错，不再静默当作空轮询处理。

### ✅ 测试

- 在 `TestResponsesRunAPIAbandonMarksInterruptedToolsWithoutRetry` 中检查废弃工具记录前获取运行时租约，与生产环境的 recovery caller 模式一致。
- 新增 ACP 测试：已释放租约的历史会话加载不得持久化默认值、运行时租约下的目录更新、历史会话标题修改。
- 新增 serve 测试：按运行选择厂商、厂商/模型不匹配、限定模型解析。
