# OpenAI Responses API 完整最终方案

> **状态**：最终方案；当前实现中
>
> **修订日期**：2026-08-03
>
> **适用范围**：`internal/provider/openai`、provider 抽象、session、agent loop、serve API、TUI/WebUI 配置
>
> **完成定义**：MothX 的 `openai-responses` 是一个可保真、可恢复、可审计、可演进的 Responses API provider。它完整承载官方 Responses 协议的状态、item、事件和工具语义；兼容网关则通过显式能力配置得到同样确定且安全的行为。
>
> **当前实现进度**：基础 Responses 请求路径、SSE function-call 归一化、配置映射、Responses runtime 表结构、background run 查询/取消 API 和 WebUI run 状态消费已经落地；完整 item 归档、agent loop 级工具去重、状态模式自动恢复、hosted tool 生命周期和完整 capability profile 尚未达到本方案验收标准。详细差异见第 13 节。

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

agent loop 通过现有 registry 执行本地调用，但副作用去重必须使用跨协议的本地 idempotency key，而不是直接绑定某个厂商字段。execution record 至少保存非空 `execution_key`、provider/API、session、turn/run identity、tool name、normalized arguments hash，以及 OpenAI `call_id`、Anthropic `tool_use.id`、Chat Completions `tool_call.id` 等厂商字段作为可诊断 metadata。重连或重试可以复用已完成的结果，绝不能再次执行已经确认的副作用。

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
- annotation 必须保留 text offset、title、URL/file id 和 source item id。annotation 不可用时 UI text 必须仍然可读；脱敏后的 citation metadata 必须仍可查询。
- refusal content 与普通 output text 分别建模，text aggregation 过程中不得丢失。

## 8. 会话、持久化与后台运行

session storage 负责保存 replay、审计和恢复所需的必要且充分的持久化记录。必须在 `internal/session/migrations.go` 的 migrations slice 追加 migration 创建以下保守表；业务代码不得临时执行 `CREATE TABLE`。这些表不使用外键，避免 SQLite 迁移、跨版本恢复和部分损坏数据库场景被强约束放大；每张表必须保留 `session_id` 并建立索引，由 session 删除/保留流程在应用层显式清理。不得把完整 transcript、原始 request/response 或工具输出重复存一份；只保存 lineage、状态、摘要、脱敏 item/provenance 和恢复所需的必要字段。

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
  sanitized_json BLOB NOT NULL,
  created_at DATETIME NOT NULL
);
CREATE INDEX idx_response_items_session_turn ON response_items(session_id, local_turn_id);
CREATE INDEX idx_response_items_response_id ON response_items(response_id);

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
```

archive policy 是一级配置：持久化数据必须在写入前脱敏并控制尺寸；敏感 arguments 可按 session privacy policy 省略、脱敏或加密。secret、authorization 数据和敏感 URL query parameter 永不进入 archive。保留与删除规则由所属 session 的应用层清理流程显式执行；不能依赖数据库外键，也不能遗漏 Responses 表、通用 tool execution 表和 run 表。

`background=true` 必须产生可持久化的 `response_run`，而不是占用普通 `Chat` stream 的 goroutine，也不通过普通 `Provider.Chat` 的 `StreamEvent` 承载完整生命周期。`Chat` 仍是同步 streaming turn 的入口；background run 由 Responses runtime/run manager 创建本地 `local_run_id` 后返回可查询状态。runtime 支持 poll、stream reattach、cancel、重启恢复、ownership check 和终态审计。serve 提供认证后的 query/cancel/reconnect 操作；TUI 和 WebUI 消费同一 run state，不能绕过 sandbox、approval 或 session ownership。

agent loop 对 background run 视为 pending turn。只有 run manager 收到终态、完成必要的本地工具执行/审批/结果回传，并写入对应 session 记录后，该 turn 才能被视为完成。纯文本后台生成可以直接终态归档；包含 function call、computer approval、remote MCP approval 或 hosted tool lifecycle 的后台 run 必须复用同一工具 registry、sandbox、approval 与 execution deduplication 机制。

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
      "computerUse": {},
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
- 跨协议 `execution_key`、厂商 call identity、output ordering、parallel execution 与 side-effect deduplication 在 reconnect 和 retry 后仍然正确。
- `completed`、`incomplete`、`failed`、provider error 与 transport cancellation 可区分、可诊断且被持久化记录。
- replay、`previous_response_id`、conversation 与 background mode 都能从本地 session state 恢复，并受 privacy/capability policy 约束。
- 所有 hosted tool 都有类型化 descriptor、原生 item handling、显式 lifecycle 与工具专属 safety control。
- configuration validation、TUI、WebUI、serve API、agent loop 与 channel dispatch 使用同一个 runtime 和同一个 capability source。
- 所有 database change 都是 migration，所有 archive data 遵循 sanitization/retention rule，且上述测试全部通过。

## 13. 当前实现进度

截至 2026-08-03，代码已经进入可运行的阶段性实现，但尚未达到第 12 节的完整验收标准。当前实现应按“基础路径已接通、完整 runtime 待补齐”理解。

### 13.1 已完成

- `openai-responses` provider 请求入口可用：`POST /responses`、streaming SSE、`model`、`instructions`、`input`、`tools`、`max_output_tokens`、`reasoning`、sampling 参数裁剪、prompt cache、`store`、`conversation`、`truncation`、`include`、`service_tier`、structured output、tool choice、parallel tool calls、max tool calls 均已有编码路径。
- prompt cache 默认开启；`promptCacheEnabled=false` 可关闭；显式 `prompt_cache_key` / `prompt_cache_retention` 会做模型 compat 校验，不再把显式不支持配置静默发给上游。
- Responses SSE decoder 支持标准 SSE field、多行 `data:`、`[DONE]` 和部分兼容 gateway 的 line-delimited event。function-call arguments 按 item identity 和 output index 分片归并。
- `StreamEvent` 已扩展 `ProviderEventType`、`ItemID`、`CallID`、`Metadata`、`Attachments`，可承载 Responses 的跨 provider 事件信息。
- session schema 已通过 migration 增加 `response_turns`、`response_items`、`tool_execution_records`、`response_runs`。这些表不使用外键，保留 `session_id` 与必要索引；删除 session 时会在应用层清理 Responses 表。
- session store API 已提供 response turn、response item archive、tool execution record 和 response run 的保存/查询/更新接口，并具备尺寸限制与敏感键脱敏。
- `ResponsesRunManager` 已支持创建本地 `response_run`、提交 background request、poll 远端状态、cancel、recover 非终态 run。
- serve API 已暴露认证后的 `GET /api/responses/runs/{localRunID}`、`POST /cancel`、`POST /reconnect`，并做 session ownership / workDir 授权检查。
- WebUI 已消费 `responsesRun` runtime snapshot，可显示 busy 状态、阻止同 session 并发提交、轮询 durable run、取消 Responses background run。
- 当前回归验证通过：`go test ./...`、`cd ui && npm run build`。

### 13.2 未完成

- Responses normalizer 产出的 response lineage、canonical item、unknown item、incomplete reason 目前没有在真实 provider/agent 路径写入 `response_turns` 和 `response_items`；这些 store API 目前主要由单测覆盖。
- `tool_execution_records` 尚未接入 agent loop 的工具执行路径；重连、retry、background continuation 下的副作用去重还不能证明。
- `previous_response_id` 目前只是通过 `ResponseOptions.PreviousResponseID` 手工传入并编码到请求；尚未从本地 response lineage 自动选择上一轮 response，也没有远端 404、过期、权限变化后的 replay fallback。
- `conversation` mode 只会发送显式配置的 conversation ID；尚未形成基于 session state 的完整恢复、校验和 fallback 机制。
- `background=true` 的 durable run manager 已存在，但普通 agent loop / WebUI submit 仍主要走现有 session run goroutine。background run 终态后尚未自动归档 response/item，也没有执行 function call、hosted tool lifecycle 或审批 continuation。
- hosted tools 目前主要是 request descriptor：Web Search 有基础 type 编码，File Search、Code Interpreter、Computer Use、Image Generation、Remote MCP 的原生 item 解析、approval、安全策略、artifact/citation attachment 和 UI provenance 未完成。
- capability profile 还不是方案定义的不可变完整 profile。当前主要依赖 `ModelCompat` 的若干布尔字段和本地 validation；`include` 白名单、hosted tool capability、parallel/tool-choice/service-tier/background/conversation 等字段的完整 gating 未完成。
- structured output 仅做基础 JSON schema 有效性检查并编码 `text.format`；没有做 schema 子集校验、provider profile 级限制或端到端 invalid-request 分类。
- annotation、citation、output image、generated file、code artifact、file result 等 attachment/provenance 未形成端到端存储和 UI 展示。
- 缺少 `internal/provider/openai/testdata/responses/` 下的版本化官方 schema/fixture 套件；协议覆盖仍以单测和 mock SSE 为主。
- TUI、channels、messaging 还没有消费同一份 Responses runtime/capability report，也没有完整展示 Responses attachment 和 background run lifecycle。

### 13.3 当前差异判断

当前代码可以支撑基础 Responses 调用、基础 function-call streaming、配置落库/读取、background run 状态查询/取消和 WebUI 可见状态；但离“可保真、可恢复、可审计”的完整 runtime 仍有显著差距。后续优先级应按以下顺序推进：

1. 把 response turn/item archive 接入真实 `Provider.Chat` 和 background run 终态路径。
2. 把 `tool_execution_records` 接入 agent 工具执行，形成跨协议 execution key 和副作用去重。
3. 实现 `replay`、`previous_response_id`、`conversation` 三种状态模式的 session state adapter 与 fallback。
4. 建立完整 Responses capability profile，并让 request codec 对每个字段统一 gating。
5. 完成 hosted tool descriptor registry、原生 item handling、attachment/provenance 和 UI 展示。
6. 增加官方 schema/fixture 驱动的 protocol、recovery 和 end-to-end 测试。

最终的 provider 不是窄型兼容请求路径，而是一个耐久的 Responses runtime：它能够跟随官方 API 演进，同时保持 MothX 的本地事实来源、安全控制和多 provider 架构。
