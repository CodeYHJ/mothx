# Agent 核心统一架构方案

本目录记录 MothX 从“共享 Agent 底层能力”向“统一 Agent Runtime、多个薄适配层”演进的架构方案。

- 主方案：[统一 Agent 核心与多入口 Runtime 方案](./agent-core-runtime-unification-proposal.md)
- 状态：实施中（Phase 0–4 核心迁移已完成，Phase 5/TUI 与统一 Approval/Question/完整跨入口 ExecutionRuntime 待实施）
- 最近同步：2026-08-13
- 当前完成：Source/Policy/Mode、SessionRuntime、Context/Skills/Registry/MCP、Agent 创建、Session 生命周期，以及 Channel/ACP 基础 ExecutionRuntime 已接入。
- 当前缺口：WebUI/API 的完整 Run/Approval 生命周期、跨入口 Approval/Question service、TUI Runtime 迁移。
