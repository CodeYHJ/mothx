# OpenAI Responses API 完整最终方案

> **状态**：最终方案；当前实现中
>
> **修订日期**：2026-08-06
>
> **适用范围**：`internal/provider/openai`、provider 抽象、session、agent loop、serve API、TUI/WebUI 配置
>
> **完成定义**：MothX 的 `openai-responses` 是一个可保真、可恢复、可审计、可演进的 Responses API provider。它完整承载本方案范围内的官方 Responses 状态、item、事件和工具语义；兼容网关优先保证可用性，默认按 OpenAI-compatible 能力尝试，并通过明确的错误诊断、状态回退和本地归档保持可恢复性。
>
> **当前实现进度**：基础 Responses 请求路径、SSE function/custom-call 归一化、response item archive、agent loop 级工具执行记录、native replay、`previous_response_id`/`conversation` lineage 失效回退、配置映射、Responses runtime 表结构，以及 WebUI 后台任务的提交、轮询、取消、重启接管、纯文本终态回写、并行本地 function/custom continuation、terminal remote run 恢复、approval 决策恢复、Serve/WebUI/TUI/channels attachment 输出和按模型 compat 解析的 capability profile 已经落地。custom descriptor（text/grammar）、原始 input、`custom_tool_call_output`、hosted item added/done 生命周期和归档恢复已贯通；Code Interpreter 已补充显式 MothX quota/timeout policy，仍未达到完整 hosted 专属安全标准；完整 artifact/多厂商文件下载预览和部分官方能力描述尚未达到本方案验收标准，OpenAI file ref 与 Code Interpreter container file 的受权下载/图片预览已接通。`computer_use` 当前明确不在实现范围内，配置、请求级 hosted descriptor 和远端原生 item 均会在本地拒绝。详细差异见第 13 节。

## 1. 设计结论

Responses API 不是 Chat Completions 的字段变体。它以 **response item** 为上下文和结果的基本单位：一条 response 可以包含 assistant message、reasoning、function call、function output、Web/File Search、代码执行、远端 MCP、图片和未知的未来 item。正确的实现必须保存其顺序、关联关系和可回放数据，而不能将它压平为一组 role message 或 `output_text`。

MothX 最终采用以下边界：

1. `agent loop` 继续统一负责本地工具执行、审批、sandbox、会话、子 agent 与 ESM；它不成为 OpenAI 专用循环。
2. OpenAI provider 内部拥有完整的 Responses codec、能力描述、远端状态适配和后台 run 管理。
3. provider 公共层只承载真正跨协议的事件与附件元数据，不暴露 OpenAI 私有 JSON 类型。
4. 本地 session 是会话、审计和恢复的事实来源；远端 response/conversation/run 是可验证的派生状态，不是唯一状态。
5. 本方案范围内的 hosted tool 以类型化 descriptor 建模，保留其原生输入、输出、权限和生命周期；绝不伪装为普通 function。`computer_use` 暂不支持，配置和请求均必须显式拒绝。
6. 所有请求字段和事件处理都由 capability profile 控制。未知 gateway 默认按 OpenAI-compatible 能力尝试，以保证用户可用性；显式不支持的能力必须在本地拒绝，单次 gateway 失败不得写回配置或改变后续策略。

### 1.1 当前范围排除项

`computer_use` / `computer_use_preview` 不在本期实现范围内。它既不能作为 hosted tool descriptor 被注册，也不能仅因上游或模型 profile 支持就被透传。任何 `responses.hostedTools.computerUse` 配置、等价的 API 输入或原生 computer item 都必须返回稳定的“当前版本不支持 computer use”诊断，并且不发起上游请求。后续单独立项时，才补充 computer call/action、截图和 action output 回传、人工审批、sandbox/egress 控制及 UI 呈现。

这份文档定义单一的最终架构与统一的完成标准。

## 2. 官方协议基线

实现时以锁定的 OpenAI OpenAPI schema 为机器可校验的唯一协议基线，并保留该 schema 的版本、抓取日期和 fixture 来源。以下官方页面是人类可读的补充：

- [Responses API guide](https://developers.openai.com/api/docs/guides/responses)
- [Responses API reference](https://platform.openai.com/docs/api-reference/responses)
- [Streaming responses](https://developers.openai.com/api/docs/guides/streaming-responses)
- [Function calling](https://developers.openai.com/api/docs/guides/function-calling)
- [Tools](https://developers.openai.com/api/docs/guides/tools)
- [Conversation state](https://developers.openai.com/api/docs/guides/conversation-state)
- [Structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [Background mode](https://developers.openai.com/api/docs/guides/background)

标准请求入口为：

```text
POST {baseURL}/responses
Authorization: Bearer <api-key>
Content-Type: application/json
Accept: text/event-stream
```

官方 OpenAI 默认 base URL 为 `https://api.openai.com/v1`。兼容服务可以复用 `/responses`，但只能使用其 capability profile 明确声明支持的字段、item 与 SSE event。

### 2.1 请求合同

最终 request codec 支持完整的可用能力面，并为每个字段执行模型与 provider capability gating：

| 域 | 字段 |
|---|---|
| 身份和上下文 | `model`、`instructions`、`input`、`prompt` |
| 远端状态 | `store`、`previous_response_id`、`conversation`、`truncation` |
| 生成控制 | `max_output_tokens`、`reasoning`、`temperature`、`top_p`、`text` |
| 工具控制 | `tools`、`tool_choice`、`parallel_tool_calls`、`max_tool_calls` |
| 事件和运行 | `stream`、`include`、`background` |
| 路由和缓存 | `prompt_cache_key`、`prompt_cache_retention`、`service_tier` |
| 追踪和安全 | `metadata`、`safety_identifier` |

`omitempty` 不是能力控制。codec 必须在序列化前拒绝冲突参数、剔除未声明支持的字段，并在 debug 元数据中记录裁剪原因。典型约束包括：

- `previous_response_id` 与 `conversation` 不能同时发送。
- reasoning 模型或 compat 禁止 sampling 参数时，不能发送 `temperature` 和 `top_p`。
- `background`、`store` 和 `conversation` 必须经过隐私策略许可。prompt cache 默认开启；它不是远端会话状态开关，但显式 `prompt_cache_key` / `prompt_cache_retention` 仍需经过 capability gating，并遵循 metadata 与 debug redaction 规则。
- `include` 仅允许 capability 白名单中的值，例如 reasoning encrypted content 或 file search results。
- `metadata` 采用长度、键名和值类型限制；API key、Authorization、MCP secret 和带 token 的 URL 永不进入其中。

### 2.2 Response、item 与保真原则

Response 必须解析并保存 `id`、`status`、`previous_response_id`、`conversation`、`output` 的规范化 item、`usage`、`incomplete_details`、`error`、`created_at`、`completed_at` 和可审计的脱敏摘要。`output_text` 只能作为 SDK 便利字段，不能是解析依据。完整原始 request/response 不作为持久化事实来源重复存储；调试日志和临时 decode buffer 也必须经过同一 redaction 与尺寸限制。

每个 input/output item 必须有稳定的内部表示：

```go
// OpenAI provider private types. Canonical is retained after a schema-aware decode.
type responseItem struct {
	ID          string
	Type        string
	Status      string
	OutputIndex int
	Canonical   json.RawMessage
}

type responseEnvelope struct {
	ID                 string
	Status             string
	PreviousResponseID string
	ConversationID     string
	Output             []responseItem
	Usage              *provider.Usage
	IncompleteReason   string
	Error              *responseError
	Summary            json.RawMessage
}
```

codec 必须理解并保真保存以下 item，即使上层尚无专用展示组件：

| item | 必须保留的语义 |
|---|---|
| `message` | role、全部 content part、`output_text` annotations、refusal、status |
| `reasoning` | id、summary、encrypted content、签名/可回放 payload，绝不把密文当展示文本 |
| `function_call` / `function_call_output` | item id、`call_id`、name、原始 arguments/output、status |
| `custom_tool_call` / output | `call_id`、name、原始 input/output、format |
| hosted tool call/result | 原生 item 语义、status、引用、文件、图片、container/action 状态 |
| `item_reference` 和未知 item | type、id、顺序、脱敏规范化 JSON、可观测诊断 |

未知 item 和未知 content part 是前向兼容事件，不是解析失败。只有违反当前 response 必需字段的无效 JSON 才可终止当前 response。

## 3. 最终架构

```text
agent loop
  └── provider.Provider.Chat
        └── openai.Provider
              ├── chat-completions codec
              └── responses runtime
                    ├── request item codec
                    ├── response item codec
                    ├── SSE decoder and event normalizer
                    ├── capability profile resolver
                    ├── local/remote state adapter
                    ├── hosted-tool adapter registry
                    └── background-run manager

background run coordinator
  ├── session.SessionRun（本地用户可见的总任务）
  ├── ResponsesRunManager（远端 response_id 生命周期）
  ├── agent tool execution / approval / sandbox
  └── event publication / recovery ownership

session store
  ├── transcript and tool execution records
  ├── response lineage and item archive
  └── resumable background runs
```

### 3.1 公共 provider 边界

现有 `provider.Provider.Chat` 保持唯一的同步流式入口。Responses 的细节不复制 agent loop，但公共事件必须能够完整传递跨 provider 有意义的数据：

```go
type StreamEvent struct {
	// Existing fields remain unchanged.
	Type           StreamEventType
	TextDelta      string
	ThinkDelta     string
	ThinkSignature string
	ToolCall       *ToolCallBlock
	Usage          *Usage
	Error          error
	StopReason     string
	RetryAttempt   int
	RetryMax       int

	// Protocol-neutral enrichment.
	ProviderEventType string
	ItemID            string
	CallID            string
	Metadata          map[string]any
	Attachments       []Attachment
}

type Attachment struct {
	Kind        string // citation, file, image, artifact, tool_result
	Name        string
	URL         string
	MediaType   string
	Metadata    map[string]any
	ProviderRef string
}
```

`Metadata` 和 `Attachments` 必须只包含经过脱敏与尺寸限制的数据。OpenAI item 的规范化表示只保留在 OpenAI 私有 codec/session archive 中，公共 provider API 不暴露无约束的 OpenAI JSON。

`ChatParams` 增加显式、类型化的跨 provider 协议选项，而不是无边界 `map[string]any`：

```go
type ResponseOptions struct {
	StructuredOutput *StructuredOutputOptions
	ToolChoice       *ToolChoice
	ParallelTools    *bool
	MaxToolCalls     *int
}

type ChatParams struct {
	// Existing fields...
	ResponseOptions *ResponseOptions
}
```

仅在另一个 provider 也能以相同语义实现某字段时，字段才进入公共类型；OpenAI 专属运行状态始终留在 `responsesConfig` 和 runtime。

### 3.2 Background run coordinator

`background=true` 是远端 Responses 任务，而不是 MothX sub-agent。后台协调器以一个持久化的 `session.SessionRun` 作为用户可见总任务，以一个或多个 `response_runs` 记录远端 response 生命周期。它负责提交、poll、stream reattach、取消、重启恢复、session ownership 校验、终态归档与事件发布；`ResponsesRunManager` 是远端 `response_id` 的唯一所有者。

协调器必须复用既有本地执行能力，而不是复制它们：

| 复用对象 | 在后台流程中的职责 |
|---|---|
| `session.SessionRun` 和 serve `RunManager` | 总任务状态、事件订阅、取消入口、会话并发控制与本地恢复视图 |
| agent 工具执行链 | 本地 function/custom tool 的审批、sandbox、execution record、幂等和结果格式化 |
| sub-agent 的事件/审批转发模式 | 仅可作为实现事件桥接的参考；不创建新的子 agent 模型调用 |

禁止把 `AgentManager` 或 `SubAgentSpawnTool` 当作远端任务的状态机：它们的执行状态以进程内存和本地 Agent 生命周期为中心，重启后不能恢复远端 `response_id`，且调用 `Agent.Run` 会产生一轮独立模型请求。协调器可以在终态 response 需要本地 function call 时调用共享的工具执行单元，但不得为此启动完整 sub-agent loop。

目标流程如下：

```text
创建 SessionRun（pending）
  -> ResponsesRunManager.Start（POST /responses, background=true）
  -> 持久化 response_run 与 response_id，发布 queued/in_progress
  -> poll 或 stream reattach，并归档规范化 item
  -> 遇到需要本地执行的 function/custom call：共享工具执行链
  -> 回传 function output，按 response lineage 继续远端 Responses 请求
  -> 写入最终 response/item、usage 与审计记录，完成 SessionRun
```

远端失败、取消、重启恢复和远端状态失效均先更新 `response_runs`，再更新总 `SessionRun`；不得由进程退出、HTTP 连接关闭或 sub-agent timeout 直接判定远端任务失败。

### 3.3 Capability profile

每个 `openai-responses` provider 在构建时解析一个不可变 capability profile。来源优先级为：显式 model compat、vendor adapter、官方 OpenAI profile、明确的 gateway profile、OpenAI-compatible 宽松默认值。未知 gateway 不通过探测请求学习，也不依赖模型名称猜测；默认允许尝试标准 Responses 字段，服务端拒绝时返回可诊断错误，但不得把单次结果写回配置。

```go
type responsesCapabilities struct {
	SupportsResponses             bool
	SupportsPreviousResponseID    bool
	SupportsConversation          bool
	SupportsStore                 bool
	SupportsReasoningSummary      bool
	SupportsEncryptedReasoning    bool
	SupportsStructuredOutput      bool
	SupportsBackground            bool
	SupportsAnnotations           bool
	SupportsAttachments           bool
	SupportsPromptCacheKey         bool
	SupportsPromptCacheRetention   bool
	SupportsServiceTier            bool
	SupportsParallelToolCalls      bool
	SupportsToolChoice             bool
	SupportsHostedTools            map[string]bool
	SupportedInclude               map[string]bool
	SupportedSSEEvents             map[string]bool
}
```

能力不足的行为必须确定：用户显式声明为不支持的功能返回 capability error；未知 gateway 的隐式默认功能允许先发送，以优先保证可用性。服务端返回 400 时保留字段、状态码和安全诊断，不能把一次 gateway 的 400 或成功响应写回配置，从而改变后续请求行为。

## 4. 输入编解码与会话重放

输入转换器是单向、保序且无损的：

```text
provider.Message + response lineage/item archive + tool execution records
  -> Responses input item[]
       ├── message: input_text / input_image / input_file / audio
       ├── prior assistant message and reasoning item
       ├── function_call / function_call_output
       ├── custom_tool_call / custom_tool_call_output
       └── item_reference where profile permits it
```

转换规则：

- 每一个 assistant tool call 与 tool result 通过 `call_id` 精确匹配；不得用 output item `id` 替代 `call_id`。
- 同一 response 的并行调用按接收到的 output item 顺序保存，并将所有已完成的结果作为下一请求的一组输入提交。
- tool error 以明确、结构化的文本或 provider item 回传，保留 `IsError`，不吞掉失败结果。
- reasoning item 的可回放字段、signature 或 encrypted content 必须与所属 assistant 输出一起归档，并且只在 profile 允许时重放。
- 图片、文件、音频和 URL 必须执行媒体策略、尺寸限制、MIME 验证和 capability 检查；不支持时产生可诊断的工具/输入错误。
- 手工管理上下文时，优先重放归档的规范化 item，而不是将其降级重建为普通 assistant 文本；只有归档缺失或隐私策略排除时，才使用确定性 message 退化，并将该事实记录到 lineage。

### 4.1 状态模式

完整最终实现同时支持三种状态机制，并以每个 session 的显式策略选择：

| 模式 | 行为 | 不变量 |
|---|---|---|
| `replay` | 从本地 transcript、item archive 和工具记录生成全部 input | 本地 session 独立可恢复 |
| `previous_response_id` | 使用验证过的 response lineage 续接，并提交新输入/工具输出 | 远端失效立即回退为 replay |
| `conversation` | 使用明确配置的 OpenAI conversation | 不从 MothX session ID 推导；本地仍完整归档 |

`replay` 是默认模式，因为它提供跨 provider、重启、审计和远端清理后的确定性恢复。`previous_response_id` 与 `conversation` 是同等完整的优化模式，必须经过 `store`、隐私策略和 provider capability 验证。任何明确的远端状态失效（404、权限变化、过期、状态不一致或 lineage 不可恢复）都在本轮内切回 replay，不能使本地会话失效；普通 429、5xx、传输错误仍按重试预算和副作用安全性处理。

## 5. SSE 与非流式归一化

SSE decoder 按事件帧解析 `event:`、多行 `data:`、空行边界和 `[DONE]`，而不是只扫描单行 `data: `。scanner buffer 必须支持大 item；畸形帧必须带 event 序号进入 debug 记录。

以下事件均由 schema-aware parser 归一化为 response/item 状态机：

```text
response.created
response.queued
response.in_progress
response.output_item.added
response.output_item.done
response.content_part.added
response.content_part.done
response.output_text.delta
response.output_text.done
response.refusal.delta
response.reasoning_summary_part.added
response.reasoning_summary_text.delta
response.reasoning_summary_text.done
response.reasoning_text.delta
response.function_call_arguments.delta
response.function_call_arguments.done
response.completed
response.incomplete
response.failed
error
```

解析规则：

1. delta 仅用于增量显示和 per-item buffer；最终 item 始终以 `*.done` 或终态 response 归档为准。
2. arguments buffer 以 `item_id` 与 output index 双键隔离，允许多个 function/custom call 交错分片。
3. message、reasoning、annotation、attachment 和 hosted tool item 必须在 added/done 之间维持同一 item identity。
4. `response.completed`、`response.incomplete`、`response.failed`、顶层 `error` 和上下文取消都是明确终态；实现不得依赖 `[DONE]` 或 HTTP EOF 才结束。
5. 不认识的 event 可观测且不失败；已知 event 的无效 payload 是 protocol error，包含 event type、item id 和安全的错误摘要。
6. 非流式 response 与 SSE 最终 response 使用同一 item decoder 和 state transition，保证结果、usage、附件和错误分类一致。

终止原因的统一映射必须保留原始原因：

| Responses 状态 | 统一 stop reason | 必须保留的 metadata |
|---|---|---|
| `completed` | `stop` 或 `tool_calls` | response status、usage |
| `incomplete` | `length`、`content_filter`、`cancelled` 或 `incomplete` | `incomplete_details.reason` |
| `failed` | `error` | response error code/type/message |
| transport/cancel | `error` 或 `aborted` | 本地错误分类与是否已有可见输出 |

## 6. 工具模型

工具由单一 descriptor registry 定义，工具声明、事件转换、审批、执行/回传、session archive 和 UI 呈现均从该 descriptor 获取规则。

```go
type ResponsesToolDescriptor struct {
	Kind              string // function, custom, web_search, file_search, code_interpreter, image_generation, remote_mcp
	Capability        string
	RequestEncoder    func(...) (json.RawMessage, error)
	ItemDecoder       func(responseItem) (ToolObservation, error)
	ExecutionMode     ToolExecutionMode // local, hosted, delegated
	ApprovalPolicy    ApprovalPolicy
	AttachmentPolicy  AttachmentPolicy
	ResumePolicy      ResumePolicy
}
```

### 6.1 本地 function 与 custom tool

Function tool 必须编码 `name`、`description`、`parameters`、`strict`；custom tool 必须保留其 text/grammar format。`tool_choice` 支持 `auto`、`none`、`required` 和指定函数。仅在 capability 支持时发送 `parallel_tool_calls` 与 `max_tool_calls`。

agent loop 通过现有 registry 执行本地调用，但副作用去重必须使用跨协议的本地 idempotency key，而不是直接绑定某个厂商字段。execution record 至少保存非空 `execution_key`、provider/API、session、turn/run identity、tool name、normalized arguments hash，以及 OpenAI `call_id`、Anthropic `tool_use.id`、Chat Completions `tool_call.id` 等厂商字段作为可诊断 metadata。重连或重试可以复用已完成的结果，绝不能再次执行已经确认的副作用。

### 6.2 Hosted tool

完整的 registry 覆盖以下原生工具：

| 工具 | 必需集成 |
|---|---|
| Web Search | request control、search call item、URL citation/annotation、transcript citation 渲染 |
| File Search | vector-store config、result inclusion、file citation/result attachment 渲染 |
| Code Interpreter | container identity/lifecycle、file artifact、timeout、quota、cancel 与 audit trail |
| Image Generation | image-generation call、partial/final image attachment、持久化 artifact reference 与 download policy |
| Remote MCP | server descriptor、approval、SSRF/egress policy、tool availability、connection lifecycle 与 audit record |

hosted tool 输出绝不压平为普通 assistant 文本。界面可以展示文本摘要，但原始 provenance 必须保留在 item archive 和类型化 attachment 中。

`computer_use` 不属于该 registry。codec 在 request build 前拒绝 computer 配置，normalizer 遇到 computer item 时保存脱敏的未知 item 诊断并终止受影响 turn，不执行 action、截图下载或任何续跑；不得把它降级为本地 browser/bash 工具。

## 7. 结构化输出、多模态内容与注释

结构化输出只能通过 `text.format` 表达，支持 `text`、`json_object`、`json_schema` 和具名 strict schema，不复用 function 参数。request builder 在可行时依据支持的子集本地校验 JSON Schema；模型或 provider 拒绝时，归类为 invalid request，并给出字段路径与服务端详情。

所有受支持的多模态输入和输出都必须显式建模：

- `input_text`、`input_image`、`input_file` 与 audio 必须使用类型化 content block。
- image input 只有在 profile 与 media policy 允许时才可使用 data URL、URL 或 file id。
- output image、generated file、code artifact、file citation 与 URL citation 必须生成 `Attachment` record。
- annotation 必须保留 text offset、title、URL/file id 和 source item id。annotation 不可用时 UI text 必须仍然可读；脱敏后的 citation metadata 必须仍可查询。
- refusal content 与普通 output text 分别建模，text aggregation 过程中不得丢失。

## 8. 会话、持久化与后台运行

session storage 负责保存 replay、审计和恢复所需的必要且充分的持久化记录。当前 schema 以 `response_turns`、`response_items`、`tool_execution_records`、`response_runs` 和 `response_session_state` 为稳定边界；后续优先复用这些表，不为单个 hosted tool 继续拆表。任何结构变化仍必须通过 `internal/session/migrations.go` migration 完成，业务代码不得临时执行 `CREATE TABLE`。这些表不使用外键，避免 SQLite 迁移、跨版本恢复和部分损坏数据库场景被强约束放大；每张表必须保留 `session_id` 并建立索引，由 session 删除/保留流程在应用层显式清理。不得把完整 transcript、原始 request/response 或工具输出重复存一份；只保存 lineage、状态、摘要、脱敏 item/provenance 和恢复所需的必要字段。

```sql
CREATE TABLE response_turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  local_turn_id TEXT NOT NULL,
  message_id INTEGER,
  request_id TEXT,
  response_id TEXT,
  previous_response_id TEXT,
  conversation_id TEXT,
  provider TEXT NOT NULL,
  api TEXT NOT NULL,
  model TEXT NOT NULL,
  state_mode TEXT NOT NULL,
  status TEXT NOT NULL,
  incomplete_reason TEXT,
  request_summary_json BLOB,
  response_summary_json BLOB,
  created_at DATETIME NOT NULL,
  completed_at DATETIME,
  UNIQUE(session_id, local_turn_id)
);
CREATE INDEX idx_response_turns_session_id ON response_turns(session_id);
CREATE INDEX idx_response_turns_response_id ON response_turns(response_id);

CREATE TABLE response_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  local_turn_id TEXT NOT NULL,
  response_id TEXT,
  item_id TEXT,
  output_index INTEGER NOT NULL,
  item_type TEXT NOT NULL,
  item_status TEXT,
  item_key TEXT NOT NULL,
  sanitized_json BLOB NOT NULL,
  created_at DATETIME NOT NULL,
  updated_at DATETIME
);
CREATE INDEX idx_response_items_session_turn ON response_items(session_id, local_turn_id);
CREATE INDEX idx_response_items_response_id ON response_items(response_id);
CREATE UNIQUE INDEX idx_response_items_identity ON response_items(session_id, local_turn_id, item_key);

CREATE TABLE tool_execution_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  local_turn_id TEXT NOT NULL,
  execution_key TEXT NOT NULL,
  provider TEXT NOT NULL,
  api TEXT NOT NULL,
  response_id TEXT,
  provider_call_id TEXT,
  tool_kind TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  args_hash TEXT NOT NULL,
  execution_state TEXT NOT NULL,
  result_summary_json BLOB,
  provider_metadata_json BLOB,
  side_effecting BOOLEAN NOT NULL,
  created_at DATETIME NOT NULL,
  completed_at DATETIME,
  UNIQUE(execution_key)
);
CREATE INDEX idx_tool_execution_records_session_turn ON tool_execution_records(session_id, local_turn_id);
CREATE INDEX idx_tool_execution_records_provider_call ON tool_execution_records(provider, api, provider_call_id);

CREATE TABLE response_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  local_run_id TEXT NOT NULL,
  local_turn_id TEXT NOT NULL DEFAULT '',
  message_id INTEGER,
  response_id TEXT,
  provider TEXT NOT NULL,
  api TEXT NOT NULL,
  state TEXT NOT NULL,
  polling_url TEXT,
  last_event_sequence INTEGER,
  cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL,
  UNIQUE(session_id, local_run_id)
);
CREATE INDEX idx_response_runs_session_id ON response_runs(session_id);
CREATE INDEX idx_response_runs_state ON response_runs(state);
CREATE INDEX idx_response_runs_session_turn ON response_runs(session_id, local_turn_id);

CREATE TABLE response_session_state (
  session_id TEXT PRIMARY KEY,
  state_mode TEXT NOT NULL DEFAULT 'replay',
  previous_response_id TEXT,
  conversation_id TEXT,
  provider TEXT NOT NULL DEFAULT '',
  api TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL
);
```

archive policy 是一级配置：持久化数据必须在写入前控制尺寸；当前阶段优先保证可用性和诊断完整性，敏感 arguments 可按 session privacy policy 省略、脱敏或加密，后续逐步扩大 redaction 覆盖。secret、authorization 数据和敏感 URL query parameter 不应进入 archive；若兼容 gateway 返回原始错误体，必须限制尺寸并标记为诊断数据，不得把它当作可回放事实来源。保留与删除规则由所属 session 的应用层清理流程显式执行；不能依赖数据库外键，也不能遗漏 Responses 表、通用 tool execution 表和 run 表。

`background=true` 必须产生可持久化的 `response_run`，而不是占用普通 `Chat` stream 的 goroutine，也不通过普通 `Provider.Chat` 的 `StreamEvent` 承载完整生命周期。`Chat` 仍是同步 streaming turn 的入口；后台提交由 BackgroundRunCoordinator 创建本地 `SessionRun` 和 `local_run_id`，再委托 Responses runtime/run manager 管理远端 response。runtime 支持 poll、stream reattach、cancel、重启恢复、ownership check 和终态审计。serve 提供认证后的 submit/query/cancel/reconnect 操作；TUI、WebUI 和 channels 消费同一 run state，不能绕过 sandbox、approval 或 session ownership。

agent loop 对 background run 视为 pending turn。只有协调器收到终态、完成必要的本地工具执行/审批/结果回传，并写入对应 session 记录后，该 turn 才能被视为完成。纯文本后台生成可以直接终态归档；包含 function call、remote MCP approval 或 hosted tool lifecycle 的后台 run 必须复用同一工具 registry、sandbox、approval 与 execution deduplication 机制。后台协调器不得调用 `Agent.Run` 或 sub-agent 来等待/续跑远端 response。

## 9. 配置与产品界面

`ProviderConfig.responses` 是唯一的持久化配置边界。既有字段含义保持不变，并扩展为以下完整配置：

```jsonc
{
  "api": "openai-responses",
  "responses": {
    "reasoningSummary": "auto",
    "promptCacheEnabled": true,
    "promptCacheKey": "",
    "promptCacheRetention": "",
    "stateMode": "replay",
    "store": false,
    "conversation": "",
    "truncation": "disabled",
    "background": false,
    "include": [],
    "serviceTier": "",
    "structuredOutput": {
      "name": "",
      "description": "",
      "strict": true,
      "schema": {}
    },
    "toolControl": {
      "choice": "auto",
      "parallel": true,
      "maxCalls": 0
    },
    "hostedTools": {
      "webSearch": {},
      "fileSearch": {},
      "codeInterpreter": {},
      "imageGeneration": {},
      "remoteMCP": []
    }
  }
}
```

默认值必须兼顾性能、隐私和确定性：`promptCacheEnabled: true`；`stateMode: replay`；远端存储仅在显式确认后开启；不得从本地 session 推导 conversation；不能仅因 provider 支持就启用 hosted tool。prompt cache 默认开启不等同于启用远端会话状态、`store`、conversation 或 hosted tool。用户显式要求的未支持配置在本地 validation 失败并说明 profile 原因，而不是产生上游 400。

TUI、WebUI 与 serve API 必须消费同一份已验证配置和 capability report。它们只展示兼容的控件，在启用远端持久化前说明 state/retention 影响，将 citation/file/image 展示为类型化 attachment，并从 `response_runs` 渲染 background run state。任何界面不得私自实现另一套 Responses 序列化路径。

## 10. 可靠性、重试与安全

每个失败都必须先完成分类，再决定是否重试：

| Class | Examples | Required behavior |
|---|---|---|
| Transport | DNS, EOF, connect timeout | retry only before visible output or side-effect-bearing call; honor cancellation |
| Rate limit | HTTP 429 | respect `Retry-After`, configured retry budget and deadline |
| Temporary server | HTTP 5xx, transient stream failure | retry only when request replay is safe |
| Invalid request | unsupported field, schema violation | do not retry; identify rejected field and capability mismatch |
| Incomplete | max output, filter, cancellation | persist partial output and reason; no automatic replay |
| Protocol | missing `call_id`, impossible item transition | stop affected turn with diagnostic archive |
| Tool side effect | write, command, browser action, remote MCP | deduplicate through execution record; never blindly replay |

debug output 必须限制尺寸并尽可能复用 session persistence 的字段处理；当前阶段优先保证兼容 gateway 的原始诊断和程序健壮性，完整 redaction 作为后续增强。remote MCP 具有独立的 approval 和 egress check；hosted code execution 具有 container/quota/timeout policy；file 与 image download 具有 SSRF、MIME、size 和 retention control。`computer_use` 配置和 item 在本地拒绝，且不得产生外部 action。用户可见的诊断必须标出操作和安全原因；原始上游错误体仅作为受限诊断数据，不得进入可回放 item archive。

## 11. 测试与验证合同

只有 fixture、unit、integration 和 recovery coverage 共同证明所有路径具有同一最终行为时，实现才算完成。

### 11.1 Protocol fixtures

`internal/provider/openai/testdata/responses/` contains versioned fixtures for request JSON, complete response JSON, every supported item/content type, SSE sequences, unsupported gateway profiles and malformed protocol cases. Each fixture identifies the official schema version and expected normalization result.

### 11.2 Codec and state-machine tests

- string/message/multimodal/file input and exact ordering.
- mixed assistant text, refusal, reasoning, encrypted reasoning, function/custom calls and hosted item output.
- interleaved argument deltas for multiple calls, empty/invalid/large arguments and duplicate item events.
- annotations, citations, files, images, artifacts and future unknown item preservation.
- all terminal states, response error nesting, `incomplete_details`, abort, EOF and `[DONE]` variations.
- request field gating for every capability combination, including sampling/reasoning incompatibility and mutually exclusive state fields.
- non-streaming and streaming equivalence.

### 11.3 End-to-end tests

- `httptest.Server` checks exact JSON, headers and profile-driven field removal.
- agent tool loop proves call -> approval/execution -> deduplicated output -> continuation for serial and parallel calls.
- session restart proves replay, `previous_response_id` continuation, conversation mode, lineage mismatch and automatic replay fallback.
- background runs survive restart, reconnect, cancellation and ownership validation；验证远端任务恢复不依赖 sub-agent 或进程内 `AgentManager` 状态。
- hosted tool adapters prove attachment/provenance persistence and tool-specific security policies.
- TUI, serve API, WebUI and messaging channels consume the same normalized events, attachment summaries and errors.
- official OpenAI and each supported gateway profile have an explicit compatibility matrix; unknown profiles have optimistic OpenAI-compatible expected behavior and must preserve actionable upstream diagnostics.

### 11.4 Regression commands

```bash
go test ./internal/provider/openai ./internal/provider ./internal/agent ./internal/session
go test ./internal/serve/... ./internal/tui/...
go test ./...
```

## 12. 工程完成标准

方案分为“可用版本”和“完整 runtime”两个验收阶段。可用版本先保证基础调用、恢复和用户可见状态，完整 runtime 再覆盖全部 hosted tool 生命周期和跨入口体验。

### 12.1 可用版本验收

- Responses request、response、input/output item 与 SSE contract 的基础路径可用，未知 item 可安全保留。
- text、reasoning、refusal、function/custom call、structured output 和基础 attachment 在同步与后台路径上可见且可归档。
- 跨协议 `execution_key`、厂商 call identity、output ordering、parallel execution 与 side-effect deduplication 在 reconnect 和 retry 后不重复执行已确认的副作用。
- `completed`、`incomplete`、`failed`、provider error 与 transport cancellation 可区分、可诊断且被持久化记录。
- replay、`previous_response_id`、conversation 与 background mode 都能从本地 session state 恢复；明确的远端状态失效可回退 replay。
- configuration validation、Serve/WebUI、agent loop 与现有 channel dispatch 使用同一个 runtime 和 capability source；未接入的入口必须在进度章节明确标记。
- 所有 database change 都是 migration，当前 archive 数据遵循尺寸、保留和必要的敏感字段处理规则，基础回归测试通过。

### 12.2 完整 runtime 验收

- Responses request、response、input/output item 与 SSE contract 均已 schema-verified，且未来 item type 可被安全保留。
- text、reasoning、refusal、multimodal input、function/custom call、output、structured output、attachment 与 annotation 在端到端路径上语义完整。
- 跨协议 `execution_key`、厂商 call identity、output ordering、parallel execution 与 side-effect deduplication 在 reconnect 和 retry 后仍然正确。
- `completed`、`incomplete`、`failed`、provider error 与 transport cancellation 可区分、可诊断且被持久化记录。
- replay、`previous_response_id`、conversation 与 background mode 都能从本地 session state 恢复，并受 privacy/capability policy 约束。
- 所有本方案范围内的 hosted tool 都有类型化 descriptor、原生 item handling、显式 lifecycle 与工具专属 safety control；`computer_use` 被一致拒绝，且不存在隐式降级执行。
- configuration validation、TUI、WebUI、serve API、agent loop 与 channel dispatch 使用同一个 runtime 和同一个 capability source。
- 所有 database change 都是 migration，所有 archive data 遵循 sanitization/retention rule，且上述测试全部通过。

## 13. 当前实现进度

截至 2026-08-06，代码已经达到可用版本的大部分验收项，但尚未达到第 12.2 节的完整 runtime 标准。当前实现应按“基础路径已接通、完整 runtime 待补齐”理解；后续进度以本节的已完成、进行中和未完成条目为准。

本次同步确认：后台 run 的手动 reconnect 已从只读查询改为取得 session runtime lock 后重新挂接 monitor；处于 `cancelling` 或 `terminalizing` 的本地 run 不得恢复，避免取消后继续远端工具生命周期。明确的远端状态失效（包括 `expired`、permission、404/410 和 lineage mismatch）允许当前 turn 回退本地 replay，并通过 `responses_state_transition` run event 记录原因；429/5xx 等普通请求失败不自动触发状态回退。custom tool、hosted attachment 的安全摘要和 `computer_use` 本地拒绝均已纳入现有实现；这些进展不改变第 12.2 节所列的完整恢复、hosted lifecycle、能力矩阵与跨入口 runtime 仍未完成的判断。

### 13.1 已完成

- `openai-responses` provider 请求入口可用：`POST /responses`、streaming SSE、`model`、`instructions`、`input`、`tools`、`max_output_tokens`、`reasoning`、sampling 参数裁剪、prompt cache、`store`、`conversation`、`truncation`、`include`、`service_tier`、structured output、tool choice、parallel tool calls、max tool calls 均已有编码路径。
- prompt cache 默认开启；`promptCacheEnabled=false` 可关闭；显式 `prompt_cache_key` / `prompt_cache_retention` 会做模型 compat 校验，不再把显式不支持配置静默发给上游。
- Responses SSE decoder 支持标准 SSE field、多行 `data:`、`[DONE]` 和部分兼容 gateway 的 line-delimited event。function-call arguments 按 item identity 和 output index 分片归并。
- SSE normalizer 对未知 event 保持前向兼容，同时将去重、限量后的 `unknownEventTypes` 写入终态 metadata 和 response archive summary，便于 gateway 兼容性诊断与重启审计。
- Responses stream 同时支持 `output_text.delta`/`refusal.delta` 和仅有终态 `output_text.done`/`refusal.done` 的 gateway 变体；拒答终态兼容 `text`/`refusal` 字段，已有 delta 时不会重复追加 done 文本。
- reasoning stream 同时支持 summary/text delta 和仅有终态 done 文本的 gateway 变体；已有 delta 时不会重复追加终态内容。
- `StreamEvent` 已扩展 `ProviderEventType`、`ItemID`、`CallID`、`Metadata`、`Attachments`，可承载 Responses 的跨 provider 事件信息；hosted item 生命周期也会透传到 `-P --json` 的 `hosted_item` NDJSON 事件。
- session schema 已通过 migration 增加 `response_turns`、`response_items`、`tool_execution_records`、`response_runs` 和 `response_session_state`。`response_runs` 已包含 `local_turn_id`、`message_id` 及 session-turn 索引；这些表不使用外键，保留 `session_id` 与必要索引；删除 session 时会在应用层清理 Responses 表。
- session store API 已提供 response turn、response item archive、tool execution record 和 response run 的保存/查询/更新接口，并具备尺寸限制与敏感键脱敏；真实 Responses 完成事件会归档 lineage、canonical item、usage 与脱敏 attachment。数值 usage counter 是显式白名单，不会被 credential token 脱敏规则误删。
- agent loop 的本地工具执行已写入通用 `tool_execution_records`，以 execution key/call identity 防止在重连或 continuation 中重复执行已确认的副作用。
- replay 状态模式会优先从本地 response item archive 恢复可重放的 assistant/function item；没有兼容归档时才确定性退化为普通 message。
- `previous_response_id` 模式会从 `response_session_state` 自动选择上一轮 lineage；provider 明确识别远端 404/410、权限变化或 lineage 失效时，当前 turn 一次性回退 native replay，429/5xx 等普通错误不会误触发回退。后台 continuation 和 background poll/recovery 的远端状态读取也复用相同的本地 replay fallback；每个本地 run 最多自动 replay 一次。
- `conversation` 模式会保留显式 conversation 配置；provider 明确识别远端 conversation 失效时，当前 turn 一次性抑制 conversation 并回退 native replay，后续 turn 不永久修改用户配置。
- `ResponsesRunManager` 已支持创建本地 `response_run`、提交 background request、poll 远端状态、cancel、recover 非终态 run；其远端生命周期与普通同步 `Provider.Chat` 分离。
- `BackgroundRunCoordinator` 已接入 WebUI `POST /api/sessions/{sessionID}/runs`：`background=true` 时创建本地 `SessionRun`，提交远端 response、持久化 `response_id`、轮询终态、回写纯文本 assistant message，并复用同一取消入口和事件发布。服务启动时，可恢复的远端 run 不会被普通 orphan 清理误标为失败。
- 后台 response 产生 `function_call` 或 `custom_tool_call` 时，协调器会复用 Agent 的工具 registry、审批、sandbox 和 `tool_execution_records`，按原始顺序记录并回传相应 output，再以新的 archive turn 继续远端 response；custom input 保留原始文本，并在现有本地 registry 边界以 `{"input": "..."}` 传入。工具调用和结果也会写回本地 transcript。
- `computer_use` request descriptor 已移除；`responses.hostedTools.computerUse`（包括空对象）会在 provider 配置阶段被明确拒绝。
- 若上游仍返回 `computer_call` / `computer_call_output`，normalizer 会先保存脱敏 canonical item 和 `computerUseRejected` 诊断，再终止当前 turn；不会执行 action、截图或降级成本地工具。
- Serve RunExecutor 会把主 agent 的 provider-neutral attachment 发布为 transcript `attachments` 事件；WebUI 会将 citation/file/image/artifact 引用合并到 assistant 消息并渲染安全链接或 provider ref。
- serve API 已暴露认证后的 `GET /api/responses/runs/{localRunID}`、`POST /cancel`、`POST /reconnect`，并做 session ownership / workDir 授权检查。
- WebUI 已消费 `responsesRun` runtime snapshot，可显示 busy 状态、阻止同 session 并发提交、轮询 durable run、取消 Responses background run。
- background response archive 会把 usage 与 provider-neutral attachment 写入 response summary；后台终态会将 attachment 重新发布给 transcript/事件 broker，并将 usage/attachment 写入完成 run event。
- 当前 Go 回归验证通过：`go test ./...`；兼容回归覆盖普通 OpenAI Chat、Anthropic、Google 的 Agent 参数路径，证明 Responses 选项不会泄漏到其他协议；WebUI、channels、TUI 的 durable submitter 共用同一 runtime contract，远端生命周期通过 `BackgroundRunDriver` 解耦。Serve submit-run 和 `/v1/chat/completions` 的非流式 `x_background=true` 支持 `Idempotency-Key`，复试请求复用已有本地 run 且不改表结构；chat background 会保留 system/history/采样参数并返回 durable `202`。本轮 WebUI 改动已补充前端单测，但执行环境缺少 `node`/`npm`，`cd ui && npm run build` 尚待具备 Node.js 的环境验证。

### 13.2 未完成

- response archive 已接入同步 Responses provider/agent 和 background coordinator 完成路径；background summary 的 usage 细节和 attachment 已贯通到完成 run event、在线 transcript 及持久化 assistant message，断线后 session 重载可恢复 attachment 数据；带 URL 的 image attachment 已在 WebUI 中预览，citation/file 的安全 HTTPS 链接可在新窗口打开；Responses 归一化与 WebUI 均拒绝非 HTTPS、含凭据、localhost、私网/loopback/link-local attachment URL；新增可选 provider-specific file resolver、session archive/ref 授权的 Serve 下载入口和 WebUI file attachment 下载交互，任意 URL/ref 不会被代理；OpenAI Code Interpreter 的 `container_id`/`file_id` provenance 现在可走容器文件下载接口，普通 file ref 仍走 Files API；Resolver 入口已验证为非 OpenAI provider 可插拔，其他厂商具体 resolver 和更完整的 artifact 下载仍未形成。
- `tool_execution_records` 已接入 background 本地 function continuation；并行 function call 已按原始顺序回传，恢复 monitor 已能处理进程退出时已终态的 remote run 和 function continuation；approval resolution 会按 run/tool call/参数匹配后在恢复时复用，未决请求则重新发起。恢复 monitor 对明确的只读工具（read/grep/find/ls/plan）可重新取得未完成记录并继续执行，副作用工具仍保守停止；事件流会把不确定执行明确标记为 `interrupted`，不会伪装成普通 `failed` 或自动重试，reclaim 还会在现有 provider metadata 中记录 `automatic_read_only` 或 `user_confirmed` 原因。认证后的 `POST /api/responses/runs/{localRunID}/abandon` 提供明确的人工放弃路径；新增 `POST /api/responses/runs/{localRunID}/recover`，要求 `confirm=true` 和明确的 `toolCallIds`，只把选中的中断记录标记为 `retry_requested` 后复用同一 monitor，避免批量或隐式重试。两条路径都取得 session runtime lock，副作用恢复仍需要用户逐 tool call 确认；WebUI submit 和外部 background 入口现在把非敏感请求指纹写入已有 `started` run event，同 key 的不同消息会返回幂等冲突，同 key 的兼容重试仍复用原 run；tool execution claim 还会校验 session/turn/provider/tool/args identity，发现 execution key 碰撞时拒绝复用，结果更新也只允许由仍处于 running/retry_requested 的记录写入，防止旧进程覆盖 abandon 或新恢复状态；跨入口的完整副作用证明仍需继续补齐。
- replay 已能从本地 archive 自动恢复兼容的 item；`previous_response_id` 已从本地 lineage 自动选择，并支持当前 turn 的明确失效回退。远端 state failure 会区分 `expired`、`permission`、`request_failed` 和 lineage mismatch；明确的远端状态失效允许本地 replay，普通请求失败仍按重试策略处理。回退原因会作为 `responses_state_transition` run event 持久化，并由现有 SSE/WebSocket run-event replay 重放。完成的并发分支会在 `response_summary.lineageUpdate=conflict` 中留下审计证据，且不会覆盖主链。
- `conversation` mode 会发送显式配置的 conversation ID，并支持当前 turn 的失效 replay fallback；远端权限变化现在会保留为脱敏失败 turn，并以 `responses_state_transition` 的 failed run event 经现有 SSE/WebSocket 重放展示。
- channels 透传平台提供的可选 `MessageID`；只有存在稳定原生事件 ID 时才生成带平台/用户作用域的 `Idempotency-Key`，没有 ID 时不猜测、不误合并消息。
- WebUI、`/v1/chat/completions` 非流式 `x_background=true` 及 channels 外部消息提交已使用 BackgroundRunCoordinator 完成本地 function/custom tool 的创建、恢复/轮询、取消、终态 archive、并行工具执行和 session 回写；chat background 会保留客户端 system/history/采样配置并返回 `202`。WebUI 请求会在移交时保留现有 session/runtime 配置；channels 通过统一 submitter callback 释放自身 lease 后重新取得 Serve runtime lock，后台完成文本和 provider-neutral attachment 摘要通过原有 progress callback 回传，不复制 Responses 状态机；提交请求、attachment 摘要和远端 start/continue/get/cancel driver 契约已抽到中立的 `internal/serve/runtime`，入口层只做兼容包装。background agent 正确挂接 approval 生命周期，恢复后的 tool call 会重用相同 tool call 的已决 approval，未决时重新建立可处理请求。认证后的 `/api/responses/runs/{localRunID}/reconnect` 现在会真正重新取得 session runtime lock 并挂接同一 monitor（成功返回 `202`），不再只读取远端状态；WebUI 首次观察到非终态 durable run 时会安全地调用该入口，已有 coordinator 持锁时不创建重复 monitor。`cancelling`/`terminalizing` 本地 run 不允许被恢复或 reconnect 重新挂接，防止取消后继续远端工具生命周期。TUI 已增加可注入的同一 `BackgroundSubmitter` 入口；独立 TUI 没有 Serve coordinator 时，`background=true` 会自动降级为同步 Responses 请求以保证可用性。工具执行中途崩溃仍未达到完整恢复标准。
- custom tool 已支持 `type: "custom"` descriptor、text/grammar format、SSE `input.delta`/`input.done`、前后台本地执行、`custom_tool_call_output`（text/image/file content list）、请求级 custom `tool_choice` 和 archive 恢复。hosted tools 已补充 provider-neutral `StreamHostedItem` added/done 生命周期事件；Web Search citation、File Search result 已保留 source item/status、citation offset 和 file score；OpenAI codec 现在由包内 hosted descriptor registry 统一维护已知 hosted item 的 capability、resume 与 attachment policy，未知类型仍只归档不猜测执行。内置 descriptor 会深拷贝配置 map，运行期不会受调用方后续修改影响。通用 citation/file/image/artifact attachment 已从 canonical item 提取，image-generation base64 result 会以受限 SHA-256/大小 provenance 透传而不保存原始 payload，Code Interpreter container ID 会作为非链接 artifact 留存审计。Remote MCP 在配置阶段要求 `server_url` 或 `connector_id`，拒绝非 HTTPS、userinfo、localhost/私网 IP URL，并默认发送 `require_approval: "always"`；现在对 hostname 增加确认式 DNS 私网预检，只有明确解析到私网/loopback/link-local 才拒绝，解析失败/超时仍放行；其嵌套 URL 不会在 UI 中提升为 attachment，上游服务仍是最终 egress 边界。Code Interpreter 现在支持现有 hosted map 内的 MothX 私有 `mothx.maxCalls`/`mothx.timeoutSecs` 策略；私有字段不会发送给上游，超额结果以 `incomplete` 归档，超时由同步/后台 runtime 取消。完整的 UI provenance、上游 Remote MCP DNS 级 egress 和更细配额仍未完成。`computer_use` 已从目标范围移除，且配置阶段已显式拒绝，不再透传。
- capability profile 已从 `ModelCompat` 解析为请求级只读 profile，并对 `include` 白名单、hosted tool、parallel/tool-choice、service tier、background、conversation、previous response 和 structured output 执行显式 gating；Serve `/api/capabilities` 现在暴露当前 Responses provider、API family、模型 profile，并补充 codec 支持的 SSE event、item、annotation、attachment kind、Responses hosted policy 与 provider-neutral `attachmentDownload` resolver 能力清单。Responses `StreamStart.Metadata.responsesRequestDiagnostics` 现在记录实际裁剪的字段与原因（例如 reasoning/compat 导致 sampling omission、远端 state 失效后的 conversation 抑制）；更细的 provider/vendor capability 差异仍待补齐。
- structured output 会做 JSON schema 有效性检查并编码 `text.format`；`strict=true` 时会本地校验 root object、`additionalProperties=false`、required 全量属性以及嵌套 object/array/anyOf 的同类约束。仍缺完整官方 schema 子集、provider profile 级限制和端到端 invalid-request 分类。
- annotation、citation、output image、generated file、code artifact、file result 等 attachment/provenance 已能作为 provider/agent attachment 传递并归档；即使 gateway 只返回 annotation 的 offset/title 而没有 URL 或 file ref，也会保留 annotation type、offset、title 和 source item provenance。Hosted item 的类型/状态摘要同时写入现有 run event，并在 live SSE/transcript projection 中复用同一白名单 provenance 标量和字符串长度限制，断线重连可重放其最新状态。Serve 非流式响应通过 `x_attachments`、流式响应通过 `event: attachments` 暴露，WebUI 在线 transcript、TUI 和 channels 最终文本均能展示安全摘要；OpenAI file ref 已有 session/ref 授权下载和图片预览入口，artifact、其他 provider 的 resolver 和更完整的多厂商预览仍未形成。
- 已增加 `internal/provider/openai/testdata/responses/` 下的版本化 protocol fixture，覆盖 custom tool SSE、hosted output item 与 hosted lifecycle added/done、`response.incomplete` 的 lineage/conversation/incomplete-reason 终态，以及 computer-use 拒绝/脱敏；仍缺官方 OpenAPI schema 镜像、更多 terminal/event 变体和跨 gateway fixture。
- WebUI、TUI、channels、messaging、ACP 和 print/NDJSON 已消费 provider-neutral attachment 摘要；hosted item 生命周期现在沿 provider -> agent -> public SDK bridge 透传，并由 Serve SSE/transcript broker 以 `hosted_item` 事件转发，同时把安全状态摘要记录为现有 run event，重连时可重放；broker 直连的流式 chat 客户端也会收到该事件。WebUI transcript、channels 进度回调、TUI activity/status、ACP 非执行型 tool update 和 CLI `hosted_item` NDJSON 展示受限的类型/状态摘要，入口可以观察 added/done 状态而不重建厂商 codec。TUI 现在会从共享 session DB 发现未终态 `tui` background run，并在终态按提交边界回放新增 assistant 文本，避免重启后只剩“已提交”而没有结果；channels background 的本地工具会通过既有 progress callback 实时输出受限状态，完成事件同时保存 assistant entry ID、待投递标记和受限 tool progress 摘要，dispatcher 在重启后的下一条入站消息中按顺序补投工具状态与 canonical 文本/附件，并记录已投递事件；OpenAI file attachment、Code Interpreter container file 下载和图片预览已接通，WebUI 会消费 capability report 中明确的 attachment download 能力；artifact/多厂商 attachment 交互仍待补齐。
- 正常和重启恢复的 background monitor 都会监听本地取消 context；取消后不会继续等待下一次 poll 或发起新的远端查询。TUI 的共享 durable background 提交入口已接通；独立进程仍依赖 Serve 注入 submitter，未注入时使用同步可用性降级。

### 13.3 当前差异判断

当前代码可以支撑基础 Responses 调用、function/custom-call streaming、配置落库/读取、background run 状态查询/取消和 WebUI 可见状态；但离“可保真、可恢复、可审计”的完整 runtime 仍有显著差距。后续优先级应按以下顺序推进：

1. 补齐工具执行中途崩溃的细分恢复和重复提交幂等审计；已提供只读自动恢复与确认式副作用恢复，但不得以完整 sub-agent loop 替代远端 response 生命周期。
2. BackgroundRunCoordinator 提交入口已扩展到 TUI；TUI 重启后的 durable 文本终态回放和 channels 下一条入站消息触发的完成文本/附件补投已接通，仍需补齐 channels 中间工具事件和更细的运行态事件重放。
   Background Responses 的 `incomplete` 终态现在不会被误报为 `failed`：已归档的部分文本、附件和 `incomplete_reason` 会继续写入本地 transcript/run event，并由外部入口作为可交付的部分结果返回。
3. 完善 `previous_response_id`/`conversation` 的回退事件重放；细分错误分类和并发 lineage 冲突已持久化审计。
4. 继续细化 Responses capability profile 的 provider/vendor 差异和隐式字段裁剪审计；SSE event、item、annotation、attachment kind、hosted lifecycle 和基础诊断已接通，意外 computer item 的显式诊断已完成。
5. 完成 hosted tool descriptor registry 的跨 provider 能力映射、工具专属安全策略（Remote MCP egress、Code Interpreter quota/timeout）以及 artifact/多厂商 attachment 下载/预览交互；当前 OpenAI Responses codec 内的 registry、MothX Code Interpreter quota/timeout、原生 item handling、OpenAI file/container resolver、attachment/provenance 和跨入口状态展示已接通，仍需各厂商具体 resolver、上游 Remote MCP egress 和更完整的 artifact 交互。
6. 增加官方 schema/fixture 驱动的 protocol、recovery 和 end-to-end 测试，覆盖后台任务不依赖 sub-agent 内存状态的恢复语义。

最终的 provider 不是窄型兼容请求路径，而是一个耐久的 Responses runtime：它能够跟随官方 API 演进，同时保持 MothX 的本地事实来源、安全控制和多 provider 架构。
