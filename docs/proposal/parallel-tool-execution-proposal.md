# Parallel Tool Execution Proposal

## Research Findings

Provider APIs expose two different concepts that must remain separate:

- Provider-side parallel-call controls (`parallel_tool_calls` for OpenAI Chat/Responses and the corresponding tool-use behavior for Anthropic/Google) control whether the model may emit multiple function calls in one response. The adapters enable the supported forms by default and honor `supportsParallelToolCalls: false` for incompatible gateways.
- A hosted tool such as Responses `web_search` runs inside the provider service. MothX receives its lifecycle events, but does not own the provider's internal search workers or quota.
- MothX owns the execution of local function/custom tools after a response is received. This is the scope of the local concurrency limit.

References: [Responses API reference](https://platform.openai.com/docs/api-reference/responses), [Responses streaming reference](https://platform.openai.com/docs/api-reference/responses-streaming/response/refusal/delta?lang=curl), and [Tools guide](https://developers.openai.com/api/docs/guides/tools).

## Configuration

Add a top-level `toolExecution` object to `settings.json`:

```json
{
  "toolExecution": {
    "mode": "parallel",
    "maxConcurrency": 10
  }
}
```

`mode` accepts `parallel` or `sequential`. `maxConcurrency` is the maximum number of local tools in flight for one Agent and one provider tool-call batch. The default is `10`; `1` is serial; omitted or non-positive values normalize to `10`. Global and project settings follow the existing settings precedence rules.

## Runtime Design

1. Parse and normalize `toolExecution` in `internal/config`.
2. Resolve the values once in `agentruntime.AgentBuildOptions` and pass them through `AgentFactory` and `AgentLoopConfig`.
3. Execute local tool calls through one ordered, bounded worker helper. Completion events may arrive out of order, while provider continuation messages are restored in original call order.
4. Reuse the same helper for the Responses background local function/custom continuation path.
5. Inherit the resolved values for managed sub-agents and transient agents. Do not use `responses.toolControl.maxCalls`, which has provider-request semantics.

## UI and Compatibility

WebUI exposes `mode` and `maxConcurrency` in its Tools card; TUI exposes them in the Behavior settings view. Missing fields keep the existing parallel behavior and use the default limit, so old settings files remain compatible.

## Verification

- Unit-test default normalization, sparse settings patches, worker peak concurrency, serial mode, and result ordering.
- Run focused agent, runtime, config, TUI, and OpenAI API tests, then `go test ./internal/architecture` and the full race suite when dependencies are available.
- Document that the limit is per Agent/batch, not a server-wide semaphore, and does not throttle provider-hosted search internals.
