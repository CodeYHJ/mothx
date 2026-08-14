# Agent 核心统一架构方案

本目录记录 MothX 从“共享 Agent 底层能力”向“统一 Agent Runtime、多个薄适配层”演进的架构方案。

- 主方案：[统一 Agent 核心与多入口 Runtime 方案](./agent-core-runtime-unification-proposal.md)
- 状态：实施中。Phase 0–4 的 Session、资源和 Agent 装配迁移已完成当前边界；Phase 5 TUI 第一轮 Runtime/Run/Approval/Question 已完成；Phase 6 DecisionService 收敛已完成当前边界；Phase 7–11 的 DecisionRecord、Run/Event、recovery/replay 和 Channel delivery Runtime 化已完成当前边界；Phase 12 TUI Runtime 资源接入正在进行。
- 最近同步：2026-08-14
- 当前完成：Source/Policy/Mode、SessionRuntime、Context/Skills/Registry/MCP、Agent 创建、Session 生命周期、ExecutionRuntime、Runtime-neutral RunEvent/RunEventSink、Run replay/recovery policy、Durable RunStore、Runtime-neutral delivery replay/event construction，以及 WebUI/API、TUI、ACP、Channel 的 Approval/Question identity、resolver、DecisionRecord 持久化、ReplayDecisions 和跨入口合同测试；CLI 已将共享 SessionRuntime 注入 TUI，TUI 资源别名已与 Runtime 同步。
- 当前缺口：真实进程级启动/停止测试仍受测试入口与 provider 生命周期约束；TUI 的 Registry/MCP/Skills 仍由 CLI 预装配并通过兼容桥接注入，尚未完全迁移到 `SessionRuntime.Builder`；各入口仍保留部分协议 payload、response channel、UI 队列和事件映射。
