# OpenAI Responses API 完整最终方案

> **状态**：Draft
>
> **修订日期**：2026-08-02
>
> **适用范围**：`internal/provider/openai`、provider 抽象、session、agent loop、serve API、TUI/WebUI 配置
>
> **完成定义**：MothX 的 `openai-responses` 是一个可保真、可恢复、可审计、可演进的 Responses API provider。它完整承载官方 Responses 协议的状态、item、事件和工具语义；兼容网关则通过显式能力配置得到同样确定且安全的行为。

## 1. 设计结论

Responses API 不是 Chat Completions 的字段变体。它以 **response item** 为上下文和结果的基本单位：一条 response 可以包含 assistant message、reasoning、function call、function output、Web/File Search、代码执行、计算机操作、远端 MCP、图片和未知的未来 item。正确的实现必须保存其顺序、关联关系和可回放数据，而不能将它压平为一组 role message 或 `output_text`。

MothX 最终采用以下边界：

1. `agent loop` 继续统一负责本地工具执行、审批、sandbox、会话、子 agent 与 ESM；它不成为 OpenAI 专用循环。
2. OpenAI provider 内部拥有完整的 Responses codec、能力描述、远端状态适配和后台 run 管理。
3. provider 公共层只承载真正跨协议的事件与附件元数据，不暴露 OpenAI 私有 JSON 类型。
4. 本地 session 是会话、审计和恢复的事实来源；远端 response/conversation/run 是可验证的派生状态，不是唯一状态。
5. 所有官方工具以类型化 descriptor 建模，保留其原生输入、输出、权限和生命周期；绝不伪装为普通 function。
6. 所有请求字段和事件处理都由 capability profile 控制。未知 gateway 默认保守，不假定模型名或一次成功请求代表长期能力。

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
- `background`、`store`、`conversation` 和 prompt cache 必须经过隐私策略许可。
- `include` 仅允许 capability 白名单中的值，例如 reasoning encrypted content 或 file search results。
- `metadata` 采用长度、键名和值类型限制；API key、Authorization、MCP secret 和带 token 的 URL 永不进入其中。

### 2.2 Response、item 与保真原则

Response 必须解析并保存 `id`、`status`、`previous_response_id`、`conversation`、`output`、`usage`、`incomplete_details`、`error`、`created_at`、`completed_at` 和可脱敏的原始 JSON。`output_text` 只能作为 SDK 便利字段，不能是解析依据。

每个 input/output item 必须有稳定的内部表示：

```go
// OpenAI provider private types. Raw is retained after a schema-aware decode.
type responseItem struct {
	ID          string
	Type        string
	Status      string
	OutputIndex int
	Raw         json.RawMessage
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
	Raw                json.RawMessage
}
```

codec 必须理解并保真保存以下 item，即使上层尚无专用展示组件：

| item | 必须保留的语义 |
|---|---|
| `message` | role、全部 content part、`output_text` annotations、refusal、status |
| `reasoning` | id、summary、encrypted content、签名/可回放 payload，绝不把密文当展示文本 |
| `function_call` / `function_call_output` | item id、`call_id`、name、原始 arguments/output、status |
| `custom_tool_call` / output | `call_id`、name、原始 input/output、format |
| hosted tool call/result | 原始 item、status、引用、文件、图片、container/action 状态 |
| `item_reference` 和未知 item | type、id、顺序、脱敏 raw JSON、可观测诊断 |

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

`Metadata` 和 `Attachments` 必须只包含经过脱敏与尺寸限制的数据。OpenAI 原始 item 永远保留在 OpenAI 私有 codec/session archive，而不是泄漏为无约束的公共 API。

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

### 3.2 Capability profile

每个 `openai-responses` provider 在构建时解析一个不可变 capability profile。来源优先级为：显式 model compat、vendor adapter、官方 OpenAI profile、明确的 gateway profile、保守默认值。profile 不通过探测请求学习，不依赖模型名称猜测。

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

能力不足的行为必须确定：用户显式要求的功能返回诊断性 capability error；隐式默认功能不发送相应字段并记录原因。不得把一次 gateway 的 400 或成功响应写回配置，从而改变后续请求行为。

## 4. 输入编解码与会话重放

输入转换器是单向、保序且无损的：

```text
provider.Message + response archive + tool execution records
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
- 手工管理上下文时，优先重放归档的原始 item，而不是将其降级重建为普通 assistant 文本；只有归档缺失或隐私策略排除时，才使用确定性 message 退化，并将该事实记录到 lineage。

### 4.1 状态模式

完整最终实现同时支持三种状态机制，并以每个 session 的显式策略选择：

| 模式 | 行为 | 不变量 |
|---|---|---|
| `replay` | 从本地 transcript、item archive 和工具记录生成全部 input | 本地 session 独立可恢复 |
| `previous_response_id` | 使用验证过的 response lineage 续接，并提交新输入/工具输出 | 远端失效立即回退为 replay |
| `conversation` | 使用明确配置的 OpenAI conversation | 不从 MothX session ID 推导；本地仍完整归档 |

`replay` 是默认模式，因为它提供跨 provider、重启、审计和远端清理后的确定性恢复。`previous_response_id` 与 `conversation` 是同等完整的优化模式，必须经过 `store`、隐私策略和 provider capability 验证。任何 404、权限变更、过期、状态不一致或不可恢复远端错误都在本轮内切回 replay，不能使本地会话失效。

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
	Kind              string // function, custom, web_search, file_search, code_interpreter, computer, image_generation, remote_mcp
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

agent loop 通过现有 registry 执行本地调用，但必须以 `(session_id, response_id, call_id)` 和 execution record 去重。重连或重试可以复用已完成的结果，绝不能再次执行已经确认的副作用。

### 6.2 Hosted tool

完整的 registry 覆盖以下原生工具：

| 工具 | 必需集成 |
|---|---|
| Web Search | request control、search call item、URL citation/annotation、transcript citation 渲染 |
| File Search | vector-store config、result inclusion、file citation/result attachment 渲染 |
| Code Interpreter | container identity/lifecycle、file artifact、timeout、quota、cancel 与 audit trail |
| Computer Use | computer-call/action item、screenshot/action-output return path、pending safety check 与显式 approval |
| Image Generation | image-generation call、partial/final image attachment、持久化 artifact reference 与 download policy |
| Remote MCP | server descriptor、approval、SSRF/egress policy、tool availability、connection lifecycle 与 audit record |

hosted tool 输出绝不压平为普通 assistant 文本。界面可以展示文本摘要，但原始 provenance 必须保留在 item archive 和类型化 attachment 中。

## 7. 结构化输出、多模态内容与注释

结构化输出只能通过 `text.format` 表达，支持 `text`、`json_object`、`json_schema` 和具名 strict schema，不复用 function 参数。request builder 在可行时依据支持的子集本地校验 JSON Schema；模型或 provider 拒绝时，归类为 invalid request，并给出字段路径与服务端详情。

所有受支持的多模态输入和输出都必须显式建模：

- `input_text`、`input_image`、`input_file` 与 audio 必须使用类型化 content block。
- image input 只有在 profile 与 media policy 允许时才可使用 data URL、URL 或 file id。
- output image、generated file、code artifact、file citation 与 URL citation 必须生成 `Attachment` record。
- annotation 必须保留 text offset、title、URL/file id 和 source item id。annotation 不可用时 UI text 必须仍然可读；raw citation metadata 必须仍可查询。
- refusal content 与普通 output text 分别建模，text aggregation 过程中不得丢失。

## 8. 会话、持久化与后台运行

session storage 负责保存 replay、审计和恢复所需的持久化记录。必须在 `internal/session/migrations.go` 的 migrations slice 追加 migration 创建以下规范化表；业务代码不得临时执行 `CREATE TABLE`。

```sql
CREATE TABLE response_turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  message_id INTEGER,
  request_id TEXT,
  response_id TEXT,
  previous_response_id TEXT,
  conversation_id TEXT,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  state_mode TEXT NOT NULL,
  status TEXT NOT NULL,
  incomplete_reason TEXT,
  request_json BLOB,
  response_json BLOB,
  created_at DATETIME NOT NULL,
  completed_at DATETIME
);

CREATE TABLE response_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  response_turn_id INTEGER NOT NULL REFERENCES response_turns(id),
  item_id TEXT,
  output_index INTEGER NOT NULL,
  item_type TEXT NOT NULL,
  item_status TEXT,
  sanitized_json BLOB NOT NULL,
  created_at DATETIME NOT NULL
);

CREATE TABLE response_tool_executions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  response_id TEXT,
  call_id TEXT NOT NULL,
  tool_kind TEXT NOT NULL,
  execution_state TEXT NOT NULL,
  result_item_json BLOB,
  side_effecting BOOLEAN NOT NULL,
  created_at DATETIME NOT NULL,
  completed_at DATETIME,
  UNIQUE(session_id, response_id, call_id)
);

CREATE TABLE response_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  response_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  state TEXT NOT NULL,
  polling_url TEXT,
  last_event_sequence INTEGER,
  cancel_requested BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
```

archive policy 是一级配置：raw request/response 和 item 数据必须在持久化前脱敏；敏感 arguments 可按 session privacy policy 省略、脱敏或加密。secret、authorization 数据和敏感 URL query parameter 永不进入 archive。保留与删除规则从所属 session 级联执行。

`background=true` 必须产生可持久化的 `response_run`，而不是占用普通 `Chat` stream 的 goroutine。runtime 支持 poll、stream reattach、cancel、重启恢复、ownership check 和终态审计。serve 提供认证后的 query/cancel/reconnect 操作；TUI 和 WebUI 消费同一 run state，不能绕过 sandbox、approval 或 session ownership。

## 9. 配置与产品界面

`ProviderConfig.responses` 是唯一的持久化配置边界。既有字段含义保持不变，并扩展为以下完整配置：

```jsonc
{
  "api": "openai-responses",
  "responses": {
    "reasoningSummary": "auto",
    "promptCacheEnabled": true,
    "promptCacheKey": "optional-stable-key",
    "promptCacheRetention": "optional",
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
      "computerUse": {},
      "imageGeneration": {},
      "remoteMCP": []
    }
  }
}
```

默认值必须兼顾隐私和确定性：`stateMode: replay`；远端存储仅在显式确认后开启；不得从本地 session 推导 conversation；不能仅因 provider 支持就启用 hosted tool。用户显式要求的未支持配置在本地 validation 失败并说明 profile 原因，而不是产生上游 400。

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

所有 debug output 都必须经过与 session persistence 相同的 redactor。computer use 与 remote MCP 具有独立的 approval 和 egress check；hosted code execution 具有 container/quota/timeout policy；file 与 image download 具有 SSRF、MIME、size 和 retention control。用户可见的诊断必须标出操作和安全原因，但不得暴露 secret 或私有 tool payload。

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
- background runs survive restart, reconnect, cancellation and ownership validation.
- hosted tool adapters prove attachment/provenance persistence and tool-specific security policies.
- TUI, serve API, WebUI and messaging channels consume the same normalized events, attachment summaries and errors.
- official OpenAI and each supported gateway profile have an explicit compatibility matrix; unknown profiles have conservative expected behavior.

### 11.4 Regression commands

```bash
go test ./internal/provider/openai ./internal/provider ./internal/agent ./internal/session
go test ./internal/serve/... ./internal/tui/...
go test ./...
```

## 12. 工程完成标准

仅当以下条件同时满足时，方案才可验收：

- Responses request、response、input/output item 与 SSE contract 均已 schema-verified，且未来 item type 可被安全保留。
- text、reasoning、refusal、multimodal input、function/custom call、output、structured output、attachment 与 annotation 在端到端路径上语义完整。
- `call_id` identity、output ordering、parallel execution 与 side-effect deduplication 在 reconnect 和 retry 后仍然正确。
- `completed`、`incomplete`、`failed`、provider error 与 transport cancellation 可区分、可诊断且被持久化记录。
- replay、`previous_response_id`、conversation 与 background mode 都能从本地 session state 恢复，并受 privacy/capability policy 约束。
- 所有 hosted tool 都有类型化 descriptor、原生 item handling、显式 lifecycle 与工具专属 safety control。
- configuration validation、TUI、WebUI、serve API、agent loop 与 channel dispatch 使用同一个 runtime 和同一个 capability source。
- 所有 database change 都是 migration，所有 archive data 遵循 sanitization/retention rule，且上述测试全部通过。

最终的 provider 不是窄型兼容请求路径，而是一个耐久的 Responses runtime：它能够跟随官方 API 演进，同时保持 MothX 的本地事实来源、安全控制和多 provider 架构。
