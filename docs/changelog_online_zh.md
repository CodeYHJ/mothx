# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.97

### ✨ 新功能

- **Provider 在线模型发现**
  - 模型发现能力下沉到 `internal/provider`，提供共享辅助函数（`ModelsEndpoint`、`ResolveSecretRef`、`DiscoverModels`），可拉取并规范化 provider 的 `/models` 列表为 `DiscoveredModel`。OpenAI 兼容的 `/v1/provider/models` 与 model-test 接口改为调用这些共享实现，不再各自重复探测逻辑。

- **TUI：在认证对话框中拉取并搜索在线模型**
  - provider 模型列表与模型设置视图新增 "Fetch Online Models" 入口：以后台命令对草稿 provider 的 Base URL / API 类型执行发现，并打开 "Add Model · Online List" 视图，可将拉取到的模型加入或移出草稿。保存 provider 之前不会持久化任何内容，关闭对话框或切换 provider 后的过期结果会被丢弃，加载中/空/错误状态均有 zh/en 文案。
  - 在在线列表中输入即可过滤拉取到的模型，按模型 ID 与显示名以 精确 > 前缀 > 子串 排序，同分保持发现顺序；Esc 清空查询，无匹配时显示 "No models match." 提示。

### 🔧 改进

- **公共 SDK 边界：`agent/` 不再依赖 internal 包**
  - provider 桥接从公共 `agent/` 包移入 `bootstrap/`（外部模块本来就通过空白导入使用它），在 init 时注册 provider 解析钩子与具体工厂。
  - `agent.Builder` 不再预先解析平台会话目录（由内部 builder 在 Build 时解析默认值），钩子未注册时给出明确错误；示例改为空白导入 `bootstrap` 而非 internal 包。

- **会话存储完整性加固**
  - `DeleteSession` 现在通过其会话归属的父表级联清理没有 `session_id` 列的子表（`delivery_operations`、`attachment_deliveries`），删除会话后不再残留孤儿行。
  - schema 迁移按版本号升序执行，不再按切片顺序；前向建表引用不再依赖 FK 强制关闭。
  - Run 的非终态/终态状态集合集中在 `run_store.go`，作为唯一事实来源；SQL 字面量、部分唯一索引、fork、轨迹与恢复路径全部从中派生。
  - `EndConversationTurn` 对已关闭的回合幂等；会话条目 ID 扩展为 64 位；`IdentityLocks` 委托给引用计数的锁注册表；运行时租约总线在最后一个处理器退订后关闭 UDP 监听。

### 🐛 问题修复

- **TUI：单独回车即时提交**
  - 排队中的 Enter 曾被当作换行证据，导致每次快速输入后回车发送都要等满 120ms 的分片粘贴合并窗口。现在扩展空闲窗口仅在队列中存在真实粘贴证据（含换行的字符块或紧邻文本的 Enter）时启用；单独的延迟 Enter 保持正常 16ms 窗口并立即提交。

- **被放弃的 Durable 运行缺失错误原因**
  - 工具执行被中断后放弃的后台运行可能在进入终态时未持久化原因。现在通过专用注解边界（`RunDAO.UpdateErrorIfEmpty` → `session.AnnotateSessionRunError` → `agentruntime.AnnotateDurableRunError`）仅在错误仍为空时写入——不改变运行状态、不复活终态运行、不触碰活跃运行，首个记录的原因保持权威。Responses API 的放弃路径经该边界持久化原因。

- **后台运行协调器重复用户条目**
  - 准入阶段原子追加的用户条目在管理器重载后已存在于重放状态中；协调器现在按确定性 `RunUserEntryID` 匹配并复用该条目作为续接消息，不再向会话转录与 provider 请求追加重复条目。该检查在重试、恢复与进程重启间保持幂等。

- **通道轮换租约目标目录**
  - `AcquireRuntimeForRotate` 现在显式从生命周期属主获取会话目录（为空时回退到 dispatcher 配置的目录），使变更租约与强制释放等待作用于权威的 Session，而非 dispatcher 持有的任意目录。

### ✅ 测试

- 架构：新增 `public_sdk_boundary_test`，公共 `agent/` 包或 `example/` 模块再次导入 internal 包时失败。
- 新增会话测试：删除完整性（无孤儿行）、迁移升序执行、Run 状态集合一致性、引用计数锁注册表、租约总线监听器清理；`-race` 下放宽孤儿恢复的时序余量。
- 新增快速单独回车提交（分片粘贴续接仍受保护）与 durable 运行错误注解的回归测试。
