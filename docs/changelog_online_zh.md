# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.92

### ✨ 新功能

- **会话分叉与消息分支**
  - 会话现在可以从任意消息分叉并扩展出替代分支；执行意图通过带运行时锁的分叉路径传递。
  - 会话 schema 与迁移、运行/响应存储、条目处理、ESM 指引、后台运行协调器和 dispatcher 均支持分叉；TUI 会话命令/运行时与 WebUI 聊天/会话视图同步更新。

- **WebUI 轨迹视图与会话日志导出**
  - Serve WebUI 新增只读轨迹投影，将会话消息、工具事件和运行事件按运行/回合/步骤组织，不复制 Agent/Runtime 状态。
  - 服务端会话轨迹与导出端点位于现有会话路由下，会话头部新增日志下载操作（可选包含子会话）。

- **WebUI 现代化改造（shadcn-svelte 与 lucide）**
  - 引入 Tailwind CSS v4 和 shadcn-svelte 风格组件（button、badge、card、dialog、input、switch、tabs、tooltip），并新增 `$lib` 别名与 `cn()` 工具函数。
  - 用 lucide-svelte 图标替换侧边栏、会话、设置和列表编辑视图中的文本符号；新增品牌标志、工作区筛选菜单（全部/项目/未分组）以及 Ctrl+Shift+K 新建对话快捷键。

- **署名提交 Co-Author 设置**
  - 新增全局 "authored" 设置（默认关闭），在系统提示中附加 MothX 共同作者标记，引导模型创建 git 提交时包含 `Co-Authored-By: MothX <harness@mothx.net>`。
  - 已接入配置持久化、TUI 设置对话框和 WebUI 设置表单，并提供双语标签。

- **新增提供商模型**
  - DeepSeek（anthropic + openai）：新增 `deepseek-v4-flash-vision-exp`，支持 1M 上下文、文本+图片输入，默认不发送 max_tokens。
  - 火山引擎 codingplan：新增 `doubao-seed-evolving`，支持 1M 上下文和文本+图片输入。

### 🔧 改进

- **默认模式改为 YOLO**
  - 新安装和空 mode 回退现在使用 `yolo` 而不是 `agent`：包括 `settings.json` 的 `defaultMode`、Serve/API `DefaultMode`、CLI/TUI/ACP/WebUI、公共 SDK `Builder`，以及 `agentruntime` 的策略解析。
  - 显式 `--mode`、已持久化的会话 mode，以及微信/飞书强制 `yolo` 仍然优先。已有配置中的 `defaultMode: "agent"` 不会被改写。

- **Serve 原生目录选择器**
  - Serve 现在通过操作系统原生目录选择器（macOS、Windows、Unix）选择工作目录，无需手动输入路径。

- **统一设置组件**
  - 抽取共享的 `SettingsField`、`SettingsSection`、`SettingsSwitch` 和 `ProviderEditorDetail` 组件，统一 AppSettings、ServeConfig、Channels、Env、Logs、Memory、Overview、SkillHub 和 WorkDir 的设置视图布局与样式。

### 🐛 问题修复

- **WebUI 侧边栏与会话 ID 修复**
  - 侧边栏折叠状态现在持久化到 localStorage，并补充折叠/展开标签。
  - 移除未使用的轨迹时间线模式、状态、翻译和布局辅助函数。
  - 修复 `AllocateSessionID`：`sessions.db` 已存在但 ID 未注册时视为可用，并补充回归测试。
