# Dynamic Workflows with Goja JavaScript

> 状态: 已迁移
> 日期: 2026-08-07

## 1. 背景

Dynamic workflows 将复杂任务拆成可执行的 JavaScript 编排脚本，由 workflow runtime 调度 worker agents，并持久化阶段、结果、日志和取消状态。核心能力包括 fan-out / fan-in、阶段依赖、并发限制、失败传播和可复跑状态查询。

MothX 使用 Goja 作为嵌入式 JavaScript runtime。Workflow DSL 是原生 JavaScript：函数调用、对象字面量、数组、表达式、循环和普通 JavaScript 值均由 Goja 解析执行；workflow-specific 能力通过 Go 侧注册的函数提供。

## 2. 当前决策

| 决策 | 结论 |
|------|------|
| DSL | 基于 Goja 的 JavaScript 子集 |
| Runtime | `github.com/dop251/goja` |
| 扩展机制 | 通过 Goja VM 注册 workflow 函数，不修改 JavaScript 语法 |
| 核心节点 | `workflow`、`phase`、`parallel`、`series`、`agent` |
| 运行时表达式 | `result`、`resultKey`、`resultLatest`、`results`、`log` |
| 状态格式 | JSON，仅用于 run state 持久化，不作为 DSL |
| 启用方式 | 独立 `--workflows` 开关，与 `--multi-agent` 隔离 |

## 3. 用户示例

```javascript
workflow("auth-risk-audit", {
  concurrency: 4,
  phases: [
    phase("scan", parallel(
      agent("api", {
        mode: "plan",
        tools: ["read", "grep", "find"],
        prompt: "Audit internal/serve/openaiapi for authentication risks."
      }),
      agent("channels", {
        mode: "plan",
        tools: ["read", "grep", "find"],
        prompt: "Audit internal/serve/channels for webhook risks."
      })
    )),
    phase("verify", agent("cross-check", {
      mode: "plan",
      prompt: "Cross-check these findings:\n\n" + results("scan")
    }))
  ]
});
```

`workflow_run` 接收完整、未经 Markdown code fence 包裹的 JavaScript 源码。`workflow_lint` 可在不运行 worker 的情况下检查源码。重复逻辑 agent 使用固定名称和唯一 `key`，例如 `key: "r" + i`。

## 4. 执行模型

1. `workflow_run` 将源码交给 Goja VM。
2. VM 注册安全的 workflow builtins，并把脚本解析为 workflow 节点。
3. runner 顺序执行 phase，在 `parallel` 中按 `concurrency` 限制 worker 数量。
4. worker 通过现有 `AgentManager` / `AgentFactory` 执行，继续遵守 mode、tools、sandbox、approval 和迭代限制。
5. 结果按 logical key 保存；`result` 读取唯一结果，`resultKey` 读取 keyed 实例，`resultLatest` 读取最新实例，`results` 进行确定性 fan-in 聚合。
6. `workflow_status`、`/workflows` 和 `workflow_cancel` 读取或更新当前进程可用的 workflow 状态。

Goja runtime 的 `Interrupt` 与 Go `context.Context` cancellation 映射，因此取消会终止脚本中的长循环以及正在等待的 worker。并行分支失败时，兄弟分支会被取消，多个非取消错误会聚合返回。

## 5. 安全边界与隔离

Goja workflow VM 不提供文件、shell、网络、动态加载或 Go reflection API。外部副作用只能通过 worker agent 间接发生，并继续经过既有工具和 sandbox 约束。

`--workflows` 只注册 workflow tools 和 workflow prompt；`--multi-agent` 只注册 sub-agent tools 和对应 prompt。Workflow worker 默认不会获得上层编排工具。动态 workflow 状态不会写入 frozen system prompt，而是通过任务 prompt 和 store 传递。

## 6. 状态持久化

默认状态目录为 `.vibe/workflows/runs/<run-id>.json`。状态包含 run、phase、task result、日志、错误和时间戳。文件写入使用临时文件后原子 rename；`Load`、`List` 支持取消上下文并按启动时间倒序列出记录。

## 7. 相关实现

- `internal/workflow/js.go` / `js_test.go`: Goja VM、builtins、表达式解析和 cancellation。
- `internal/workflow/runner.go` / `runner_test.go` / `semantics_test.go`: phase、parallel、semaphore、结果和错误语义。
- `internal/workflow/store.go`: JSON 状态存储。
- `internal/workflow/agent_host.go`: 复用真实 AgentManager 执行 worker。
- `internal/workflow/tools.go`: lint、run、status、cancel 工具。
- `docs/en/workflow.md`、`docs/zh/workflow.md`: 用户参考。
