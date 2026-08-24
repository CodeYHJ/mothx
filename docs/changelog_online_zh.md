# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.95

### ✨ 新功能

- **CLI 持久化运行**
  - CLI `runPrint` 现在通过 `agentruntime.ExecutionRuntime` 持久化规范的 durable run，与 WebUI、消息通道和 ACP 的运行生命周期保持一致。

- **UDP 运行时租约总线**
  - 新增尽力而为的 UDP `SessionLeaseBus`，在运行时租约和运行状态变化时唤醒本地进程。
  - 使用定向回环广播和去重；SQLite 租约与 durable 记录仍是唯一权威。

### 🔧 改进

- **事件代理重同步**
  - 事件代理新增 `SubscribeWithResync`；订阅者溢出时关闭 WebSocket，让客户端重连并重放 durable SQLite 游标。

- **运行时租约心跳**
  - 租约心跳现在对瞬态 SQLite 失败进行有界重试，并发布 `acquired`/`released`/`lost` 通知。

### 🐛 问题修复

- **后台运行乐观并发**
  - 在 durable admission 之后重新加载共享会话管理器，使后台协调器将用户消息附加到新叶子，而不是因乐观并发校验失败。

### ✅ 测试

- 在 `TestResponsesRunAPIAbandonMarksInterruptedToolsWithoutRetry` 中检查废弃工具记录前获取运行时租约，与生产环境的 recovery caller 模式一致。
