# ACP v1 完整兼容与统一核心 Session 模型方案

> 状态：已实施（共享 Runtime/session config、ACP v1 wire contract、标准 form elicitation 与 Go stdlib 进程级测试已落地；Harbor 兼容验证仍为外部手工门禁）。未声明的 client-owned fs/terminal、URL elicitation 和 authentication 仍按 truthful capability 原则保持关闭。
> 目标：在处理 session 模型/config option 的同时，巩固“一套 Agent Core、一套前端无关 Runtime、多个薄适配层”，并把 ACP 适配器收敛到 ACP v1 的完整、可验证 wire contract。
> 约束：方案阶段已结束；按本方案实施，不引入新的默认测试语言或外部 ACP client 依赖；Harbor 兼容验证保持独立手工门禁。

## 1. 结论摘要

这次工作不应只增加一个 `session/set_config_option` handler，也不应把模型状态实现成 ACP 专属能力。最终架构必须保持一套 Agent Core、一套前端无关的 Agent Runtime，以及 TUI、CLI、WebUI、Channel、ACP 五类薄适配层。所有 Agent、Session、Run、Provider、MCP、Decision、配置解析和关闭流程都由现有 `internal/agentruntime` 统一拥有；各入口只负责输入/输出协议和交互展示。

因此，模型/config option 的共享能力先在 Runtime 定义一次，再由所有入口复用：

- TUI/CLI 负责把本地参数和交互映射为 Runtime 的 session option 请求；
- WebUI/API 和 Channel 负责把 HTTP/消息命令映射为相同的 session option 请求；
- ACP 负责把 JSON-RPC `session/*` 映射为相同的 Runtime 请求，并投影标准事件；
- 任何入口都不能自行组装 `agent.Config`、provider、MCP、Session replay 或 Run/Decision 生命周期。

ACP 层只负责：

- JSON-RPC/NDJSON framing、初始化和能力协商；
- 将 session 请求映射到 `SessionRuntime`；
- 将 Agent Runtime 事件投影成 ACP `SessionUpdate`；
- 将 ACP 的反向 client request 投影到已存在的 permission/question、文件和终端策略。

“完整实现 ACP”在本方案中的定义是：对当前 ACP v1 wire contract，MothX 对所有必需方法、状态转换、错误码和标准消息类型提供正确行为；可选能力只有在运行时确实支持且完成端到端测试后才声明。没有实现的可选能力必须省略相应 capability，不能以返回一个空结果冒充支持。ACP v2 alpha、未稳定 RFD、ACP provider 注册、跨进程文档同步和产品外的身份系统不在本次范围。

模型配置和协议完整性共用一套 session binding：一次 prompt 从任意入口接收、`BuildAgent`、MCP sampling、usage/run 记录到最终事件，都使用同一个 session 模型快照，不能在调用链中再次读取进程级或适配器级模型。

## 2. 官方协议基线

### 2.1 版本与资料

方案以 ACP 官方协议 v1 文档和 `schema/v1` 的最新稳定 artifact 作为 wire fixture。写方案时 release 页面列出的 schema artifact 是 `v1.20.0`；协议 wire 中的 `protocolVersion` 仍是整数 `1`。schema artifact 版本和 wire protocol version 是两个概念：前者可以通过新增字段和 capability 演进，后者只在破坏性变更时递增。

实现阶段应再次确认依赖和 schema 的最新稳定版本，并把实际使用的版本写入测试 fixture；不把 v2 alpha 的字段混入 v1。

主要资料：

- [ACP 官方文档索引](https://agentclientprotocol.com/llms.txt)
- [ACP v1 schema 文档](https://agentclientprotocol.com/protocol/v1/schema)
- [ACP v1 schema JSON](https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/main/schema/v1/schema.json)
- [ACP releases](https://github.com/agentclientprotocol/agent-client-protocol/releases)
- [ACP Community Libraries](https://agentclientprotocol.com/libraries/community)

实现审计时按主题阅读官方 v1 文档，而不是只依赖 SDK 类型：

- [Initialization](https://agentclientprotocol.com/protocol/v1/initialization)：protocol version、client/agent capabilities、agent info；
- [Session setup](https://agentclientprotocol.com/protocol/v1/session-setup)：new/load/resume、cwd、MCP servers 和 session result；
- [Session config options](https://agentclientprotocol.com/protocol/v1/session-config-options)：model/mode/thought level、全量返回和 update；
- [Prompt turn](https://agentclientprotocol.com/protocol/v1/prompt-turn) 与 [Content](https://agentclientprotocol.com/protocol/v1/content)：prompt、content block、chunk 和 stop reason；
- [Cancellation](https://agentclientprotocol.com/protocol/v1/cancellation)：session cancel、request cancel 和 terminal state；
- [Elicitation](https://agentclientprotocol.com/protocol/v1/elicitation)：form/url client capability 与双向 request；
- [Coder Go SDK releases](https://github.com/coder/acp-go-sdk/releases)：作为社区实现参考，核对 schema 覆盖范围；本次不将其加入默认依赖。

### 2.2 Harbor ACP client 基线

`/home/free/src/harbor` 是本方案要适配验证的实际 ACP 互操作方，而不是只作为 `HARBOR_ACP_REQUESTED_MODEL` 的来源。它不进入 MothX 默认依赖、`make test` 或默认 CI。显式进行适配验证时，记录 Harbor `HEAD`（当前审计值为 `c3ce0c60bbd2fd1888b327efcc880dbd86d8b7cf`），避免把一次本地互操作结果误认为永久兼容保证。

Harbor 当前 generic ACP runner 的行为约束如下：

| Harbor 行为 | MothX 必须满足的 ACP contract |
| --- | --- |
| 通过 stdio 启动 ACP agent，`initialize` 使用 `protocolVersion=1` | 支持 stdio NDJSON、并发 request/notification、标准错误和顺序保证 |
| client capabilities 声明 `fs.readTextFile=true`、`fs.writeTextFile=true`、`terminal=true`，`auth.terminal=false` | 正确解析 typed client capabilities；只有确实需要 client-owned resource 时才发起对应反向 request |
| `session/new` 发送绝对 `cwd` 和 `mcpServers` | 接受 cwd 与 MCP server 列表，复用 Runtime 管理 session/MCP 生命周期 |
| `HARBOR_ACP_REQUESTED_MODEL` 由 Harbor 的 `model_name` 原样注入 | 解析 `provider/model`，保留 model ID 中首个 slash 后的内容，并用同一 catalog 校验 |
| `HARBOR_ACP_MCP_SERVERS_JSON` 映射 stdio、SSE、streamable HTTP | 兼容 Harbor 的 stdio payload，以及 `type=sse`/`type=http`、`url`、空 headers 等标准字段；能力关闭时明确拒绝 |
| `HARBOR_ACP_PERMISSION_MODE=allow/deny` | `session/request_permission` 使用标准 options/outcome；allow 能选择 allow_once/allow_always，deny 能返回 cancelled/denied |
| `HARBOR_ACP_AUTH_POLICY=auto/explicit/disabled` | `authMethods` 为空时不要求 authenticate；将来声明 auth 时必须提供可完成的 authenticate/logout/error 路径 |
| `session/new` 后若有 requested model，必须找到模型选择机制 | `configOptions` 至少返回 category=`model` 的 option；未知模型必须在 set option 时返回标准错误，不能继续 prompt |
| Harbor 同时兼容旧 `session.models` 和现代 `configOptions`，有旧 models 时优先旧 `set_session_model` | MothX 的目标实现只返回现代 `configOptions`，不返回 legacy `models`，不实现 deprecated `session/set_model`，避免 Harbor 走旧路径 |
| `session/set_config_option` 后继续 prompt，并记录 `ConfigOptionUpdate`/`SessionInfoUpdate`/usage/tool/message 事件 | 设置成功返回完整 options；后续 prompt、MCP sampling、usage、events 使用同一 session binding |
| Harbor 的 client 实现 `fs/read_text_file`、`fs/write_text_file`、`terminal/create/output/wait_for_exit/kill/release`，路径要求绝对 | MothX 若调用这些反向服务，必须匹配标准参数、`-32002` resource not found、`-32602` invalid params 和 terminal exit/output 语义；不需要时不得伪造调用 |

因此，Harbor 兼容的最小 session result 不是只有 `sessionId`，而是必须包含可被 Harbor 发现的现代 model config option。模型 option 的 value 应优先使用完整 canonical `provider/model`，使 Harbor 无需猜测 providerless alias；若 catalog 只能提供 providerless ID，也必须保证请求映射是确定且无歧义的。

Harbor 现有 client 会把 ACP event 保存为 `acp-events.jsonl` 并从 `usage_update`、`session_info_update`、tool/message/thought/plan/config/mode update 构建 trajectory。MothX 不需要生成 Harbor 私有文件，但必须保证这些标准事件的 schema、顺序、message ID 和终态可被该 runner 消费。

MothX 不引入 Harbor 的 Python ACP runtime。默认真实测试使用标准库 Go wire client；另提供一个显式运行的 Harbor compatibility validation：在临时 workspace 启动 Harbor `acp_runner.py` 作为 client、MothX ACP 作为 child agent，记录 Harbor commit/ACP SDK 版本，覆盖 model selection、MCP payload、permission、反向 fs/terminal、事件和失败摘要。该验证只用于确认 MothX 适配 Harbor 的行为符合预期，不成为 MothX 的默认测试依赖或发布门禁。

### 2.3 “完整”与“可选”

ACP 的能力模型要求 Agent 对 capability 做 truthful advertisement。下表是本次目标边界：

| 范围 | 目标 | 说明 |
| --- | --- | --- |
| JSON-RPC、NDJSON、初始化、错误、取消 | 必做 | 所有 ACP Agent 都依赖；包括请求/通知区分、ID、顺序和并发反向请求 |
| session/new、load、resume、list、close、delete | 必做 | 由 `SessionRuntime` 和 `Session` store 承担生命周期 |
| session/set_mode | 兼容必做 | legacy mode 仍是 v1 wire；和 config option 共用内部 setter |
| session/set_config_option | 必做 | 至少 model、mode、thought level；返回完整 options |
| model/config option update | 必做 | 新建、加载、切换模型以及 Agent 发出的 `config_option_update` |
| 文本、resource_link | 必做 | 是所有 Agent 的 baseline content；不能再把 URI 直接当普通文本丢失语义 |
| image、audio、embedded context | 能力门控 | 只有 provider/runtime 和测试证明可用才声明；未声明时按标准拒绝 |
| tool call、diff、terminal、location、plan、slash command、usage | 必做投影 | 无对应事件时可以不发送，但已发送的 payload 必须符合 schema |
| `messageId` | 必做 | 同一条流式消息的所有 chunk 共享 ID |
| additional directories | 必做 | 若声明 capability，必须验证、持久化并在 list/load/resume 中保持语义 |
| MCP stdio | 必做 | ACP agent 的 baseline；HTTP/SSE 仅在能力声明后开放 |
| MCP HTTP/SSE | 能力门控 | 复用现有 MCP lifecycle，不在 ACP 层另建连接池 |
| elicitation | 能力门控，form 已实现 | client 声明 form 时走标准 `elicitation/create` 并复用 DecisionService；老 client 保留扩展兼容；当前没有 URL workflow，因此不伪造 URL elicitation |
| fs/read/write、terminal/* | 能力门控 | 只有 Agent 真正委托 client 执行本地资源时才需要反向调用 |
| authentication/logout | 当前不声明 | MothX 当前没有 ACP 身份认证；空 `authMethods` 是诚实行为，不实现假认证 |
| v2 alpha、未稳定扩展、ACP provider registry | 不做 | 单独 proposal，不与 v1 兼容工作混合 |

## 3. 当前 MothX ACP 审计

审计对象是 `internal/acp/acp.go` 及其相邻测试；旧的 `docs/proposal/acp-capability-gap.md` 可作为历史记录，但其中关于 `session/list`、旧模型接口和能力范围的判断已经过时，本方案以当前 v1 schema 为准。

### 3.1 已有能力

- stdio line-delimited JSON-RPC 主循环；
- `initialize`、`session/new`、`session/load`、`session/resume`、`session/list`、`session/close`、`session/delete`、`session/prompt`；
- `session/cancel` 和 `$/cancel_request` 的基本取消路径；
- `SessionRuntime`、`ExecutionRuntime`、`DecisionService`、MCP connection lifecycle；
- 标准 `session/request_permission` 的基本投影和 MothX question 扩展；
- Provider factory 和 `SessionRuntime.BuildAgent` 已具备复用条件；
- tool call、plan、mode update、usage 等部分事件投影。

### 3.2 关键缺口

1. 进程级 `s.m`、`s.providerName` 和 `s.mode` 被多个 session 共用，session 没有模型绑定；prompt、MCP sampling、run/event snapshot 可能读到不同模型。
2. 没有解析 `HARBOR_ACP_REQUESTED_MODEL`，Harbor 启动参数里的 `provider/model` 无法稳定映射到 provider factory。
3. `session/new`/`load`/`resume` 没有返回 `configOptions`、modes 和 additional directories；没有 `session/set_config_option`。
4. initialize capability 没有完整反映 `loadSession`、additional directories、配置选项、typed client capability 和真实 MCP 支持；部分能力是无条件硬编码。
5. NDJSON/JSON-RPC 错误、初始化前请求、ID、未知通知、响应顺序和反向请求并发缺少标准化处理；解析错误当前可能被包装成非标准错误。
6. prompt content 只支持文本；`resource_link` 被降级为文本，未按标准保留 URI/name/mime；image/audio/embedded context 的拒绝和 capability 没有统一策略。
7. 流式消息没有稳定 `messageId`；stop reason 没有覆盖 `max_turn_requests` 等完整枚举；部分 tool/plan/update payload 未经过统一 schema 映射。
8. additional directories 没有运行时、session list 和 load/resume 的语义；当前 `cwd` 过滤不能代替该能力。
9. elicitation、标准 session usage、logout/auth 和 client-side fs/terminal 的边界没有明确，容易出现“声明支持但请求永远失败”的兼容性问题。

## 4. 统一核心与适配层边界

### 4.1 目标架构

```text
TUI   CLI   WebUI/API   Channel   ACP
 \     |       |          |       /
       thin adapters / protocol projections
                    |
             SessionRuntime + ExecutionRuntime
                    |
                 Agent Core
```

`SessionRuntime` 是所有入口的唯一 session/resource owner，`ExecutionRuntime` 是所有 durable run 的唯一生命周期 owner，Agent Core 是唯一的 prompt、stream、tool、usage 和 continuation 实现。适配层可以拥有连接、请求、渲染和协议状态，但不能拥有第二份业务状态。

共享 Runtime 至少提供以下语义，而不是只提供 ACP handler 能调用的零散方法：

- `SessionBinding`：provider、model、mode、thought level、capabilities、directories 和 execution policy 的不可变快照；
- session option catalog、读取、校验、设置和 replay；
- 基于 binding 的 `BuildAgent`、MCP sampling、Run/Usage/Event 投影和 child-agent inheritance；
- session close、run cancel、decision replay、MCP shutdown 的统一生命周期；
- 供 TUI、CLI、WebUI、Channel、ACP 共用的标准错误、无效状态和配置变更语义。

适配层之间不得通过互相调用来复用业务逻辑。例如 ACP 不调用 WebUI handler，Channel 不调用 CLI command；它们都直接调用同一个 Runtime API，再各自转换输入和输出。

| 入口 | 可以拥有的状态 | 必须委托给共享 Runtime 的状态 |
| --- | --- | --- |
| TUI | 窗口、键盘焦点、流式渲染 | provider/model/mode、Agent、Session、Run、Decision、MCP |
| CLI | Cobra 参数、进程启动选项、命令输出 | provider/model 解析、Session、Agent、Run、policy |
| WebUI/API | HTTP 请求、SSE/WebSocket 连接、页面投影 | session binding、Agent、Run、approval/question、MCP |
| Channel | 平台消息、身份映射、投递重试 | session/channel binding、mode policy、Agent、Run、Decision |
| ACP | JSON-RPC ID、NDJSON transport、capability 和 wire projection | session binding、Agent、Run、Decision、MCP、shutdown |

### 4.2 ACP adapter 不拥有 Agent

每个 ACP session 只持有一个 `*agentruntime.SessionRuntime` 和协议状态。创建 Agent 必须调用：

```text
ACP session -> SessionRuntime.BuildAgent -> existing Agent Core
```

ACP 不得新增 `agent.Config` 组装、provider factory、registry/MCP/skill bootstrap、Session replay、Run row 或第二套 cancellation/recovery state machine。新的模型和 config option 能力应扩展 `SessionRuntime` 的解析/快照接口，之后由 ACP 传入 source 和 session policy。

### 4.3 连接状态与 session 状态

连接级状态：

1. `Created`：只接受 `initialize`；
2. `Initialized`：记录双方 protocol/capabilities/info，之后才接受 session 请求；
3. `Closing`：拒绝新 prompt，等待或取消活动 run；
4. `Closed`：关闭 stdio 和所有 runtime。

Session 级状态由现有 Runtime/Run state 作为权威：`new` 创建、`load` 恢复历史、`resume` 恢复可继续执行的 session、`close` 释放运行资源但保留记录、`delete` 删除持久化 session。active run、pending decision 和 MCP client 的清理必须通过 `SessionRuntime.Shutdown`/`ExecutionRuntime` 完成。

### 4.4 单次 prompt 的模型快照

每个 prompt 开始时解析一次 `SessionBinding`：

```text
provider (process) + model (session) + mode/config options + capabilities + policy
```

该快照必须同时传给：

- `SessionRuntime.BuildAgent`；
- durable run begin/update/finish 和 usage；
- MCP sampling callback；
- agent event/session update；
- child agent/sub-agent inheritance；
- cancellation、recovery 和 reconnect 后的事件投影。

`set_config_option` 更新 session 的下一次 prompt 默认值。当前 prompt 使用已冻结的 binding snapshot，不热切换 provider/model；ACP 允许在生成期间修改配置，因此活动 run 中的合法请求也会被接受、持久化并返回完整的 `configOptions`，同时产生一次 `config_option_update`。下一次 prompt 使用新 binding。

## 5. Harbor 模型解析

### 5.1 输入与优先级

新增启动解析 `HARBOR_ACP_REQUESTED_MODEL`。值格式是 `provider/model`，例如 `anthropic/claude-opus`。解析结果必须经过同一个 provider factory/model catalog，不复制 vendor 判断。

建议优先级：

1. CLI 显式 `--provider` 与 `--model`（两者都显式时最高）；
2. CLI 只显式一项时，另一项从 `HARBOR_ACP_REQUESTED_MODEL` 补齐；
3. 完整的 Harbor env；
4. settings 默认 provider/model。

如果显式 provider 与 env provider 冲突，启动明确失败并报告 provider 冲突；不能静默把一个 provider 的模型交给另一个 provider。显式 CLI model 覆盖 env model，但仍必须在最终 provider 的 catalog 中存在。

### 5.2 解析规则

- 两侧都不能为空；首个 `/` 分隔 provider 和 model，model ID 中其余 `/` 保留；
- provider 名称按 factory 注册表做 canonicalization；
- env 值使用严格 catalog 校验，未知 provider/model 在启动时返回可诊断错误；
- 不泄露 API key 或完整环境变量到错误和 ACP event；
- 解析后的 provider 进程绑定，model 只作为 session 初始 binding，不写回全局 settings；
- Harbor env 不存在时保持现有 settings 行为，避免破坏普通 CLI/serve 启动。

### 5.3 启动验收

测试必须验证：env 生效、CLI 覆盖、provider/model 冲突、空值、未知 provider、未知 model、多进程启动隔离，以及启动失败不创建半初始化 session。示例 Harbor runner 应通过真实 ACP client 完成 initialize/new，并从 `configOptions` 看到解析后的模型。

## 6. Session config options 与 model binding

### 6.1 对外 options

`session/new`、`session/load`、`session/resume` 的 result 都返回完整 `configOptions`；如果需要兼容旧 client，同时返回 `modes`，但二者必须指向同一个内部配置，不允许两个状态源。ACP v1 的字段名要按消息类型区分：初始 modes 使用 `currentModeId`/`availableModes`，`current_mode_update` notification 使用 `currentModeId`，不能把两者混用。

第一期建议公开：

| `configId` | 类型 | 值 | 来源 |
| --- | --- | --- | --- |
| `model` | select | 当前 provider 可用模型 | provider factory/catalog，保留 model ID |
| `mode` | select | `agent`、`plan`、`yolo` 等有效模式 | Runtime policy；channel 强制 yolo 仍由 policy 生效 |
| `thought_level` | select | 当前 provider 支持的思考级别 | provider/model capability；不支持时不暴露 |

`model_config` 只在存在稳定、可持久化且 provider-neutral 的选项时暴露；不要为了“看起来完整”把 vendor-specific JSON 随意放进去。ACP v1 没有 `sessionCapabilities.configOptions` 能力字段，选项能力应通过 `session/new`/`load`/`resume` 的 `configOptions` 结果以及后续 `config_option_update` 真实声明；boolean option 只有在 Runtime 确实能校验、持久化并应用时才暴露。

### 6.2 设置接口

实现标准 `session/set_config_option`：

1. 校验 session 存在、连接拥有权和 `configId`/`value`；
2. 校验 option 属于该 session 当前 provider/model 的 catalog；
3. 活动 run 中允许合法变更，但只影响下一次 prompt；
4. 写入 session config/model change entry；
5. 更新 Runtime 的下一 prompt snapshot；
6. 返回完整 options；
7. 向 client 发送对应 `config_option_update`，顺序先于下一次 prompt 的事件。

同时保留标准 `session/set_mode`，其实现直接调用相同的 mode setter。deprecated 的 `session/set_model` 不实现；未知请求返回 `-32601`，而不是继续保留旧私有语义。

### 6.3 持久化与恢复

当前 `session.ModelChangeEntry{Provider, ModelID}` 是可复用的历史记录。需要补齐“读取最新 binding”的 Runtime API，并规定：

- 新 session 记录初始 provider/model；
- 每次成功切换追加 entry，失败不写入；
- replay 只从 session store 恢复最后一个有效 binding；
- provider 进程固定，恢复时若 entry provider 与进程 provider 不同，fail closed 并给出可诊断错误；
- 删除的模型或不再可用的模型不能静默回退到全局默认；
- session header/metadata 的缺失字段对旧 session 向后兼容，按启动时默认模型初始化一次并记录新 binding。

模型 binding 不复制到 ACP 独有的数据库表；run/usage/event 继续走现有 canonical stores。

## 7. ACP v1 wire contract 路线

### 7.1 JSON-RPC 与 transport

- 保持 stdio NDJSON，一行一个 JSON-RPC message；单行过大、非法 JSON、非法 JSON-RPC version、缺失 method/id 等分别映射标准 parse/invalid request 错误；
- request 必须有唯一 ID，notification 不返回 response；`id: null` 按 JSON-RPC 规则处理；
- initialize 是唯一允许的首个请求；协议版本不兼容时明确失败，不偷偷降级为未知行为；
- request handler 和 reverse client request 可并发，但 writer 必须串行化，确保一条 response/notification 不被交错；
- 保留 `$/cancel_request` 和 session cancellation，取消后返回 `-32800`（cancelled）或将 prompt 终止原因映射为 `cancelled`；
- 未知 notification 忽略，未知 request 返回 `-32601`；参数解析失败返回 `-32602`；内部错误不泄露 provider secret/堆栈；
- 记录 request ID 与 session ID 的诊断日志，但不把凭据写入日志。

### 7.2 initialize 与 capability

initialize 需要使用 typed request/result（不再用无约束 map 作为长期状态），保存 client capabilities、client info、protocol version，并根据真实实现声明：

- `loadSession`；
- `sessionCapabilities.list/delete/close/resume`；additional directories 已由共享 Runtime 完成校验、持久化和生命周期管理，因此可以声明；
- prompt content capabilities（只声明已支持的 image/audio/embedded context）；
- MCP `stdio` baseline，HTTP/SSE 按实际支持；
- `agentInfo` 使用 MothX 的真实名称和版本；
- 当前没有 auth method 时 `authMethods` 为空，不声明 authenticate/logout。

能力必须被 handler 真正使用：例如 client 未声明 boolean config option 时，Agent 不发送 boolean option；未声明 elicitation 时，question 走兼容扩展或以标准错误拒绝，而不是无条件调用。

### 7.3 Session 方法与 additional directories

所有 session result 使用统一 session projection：`sessionId`、cwd、可用 modes、完整 configOptions、必要的 `SessionInfo`。

additional directories 的语义：

- 只接受绝对、规范化、允许访问的路径；去重并保持稳定顺序；
- `new` 的请求路径作为该 session 初始列表；
- `load`/`resume` 如果请求字段省略或为空，表示本次激活不使用额外目录；非空列表表示完整替换本次激活列表，不是增量追加；
- `session/list` 的 `SessionInfo.additionalDirectories` 返回持久化的权威列表；
- 运行时把 roots 传入 context discovery、sandbox/allow policy、MCP 和 Agent snapshot；ACP 层不自行读取文件；
- 旧 session 没有该 metadata 时视为空列表；路径无效时返回 `-32602`，不创建半激活 runtime。

cursor 对外视为 opaque token；内部可以继续使用数字 cursor，但不能要求 client 理解其格式。分页、cwd filter 和 session ordering 需要稳定测试。

### 7.4 Prompt、content 和停止原因

baseline prompt content 需要保留 typed 语义：

- text 原样进入 Agent Core；
- `resource_link` 保留 URI、name、title、description、mimeType 和 size 等可用字段，交给统一 resource resolver；不能只拼成文本；
- image/audio/embedded context 在 capability 未声明时返回 invalid params；声明后映射到 provider 支持的 `ContentBlock`，并为不支持的 provider 做显式错误；
- 用户消息 chunk 和 agent message/thought chunk 使用稳定 `messageId`，同一条消息的所有 chunk 相同，消息结束不重复生成新 ID；
- tool call content 使用标准 `ContentBlock`、diff、terminal、location；不把内部 event JSON 直接透传；
- stop reason 覆盖 `end_turn`、`max_tokens`、`max_turn_requests`、`refusal`、`cancelled`，未知内部原因映射要有单元测试和可诊断日志。

### 7.5 Session updates

统一事件投影层，覆盖当前 v1 schema 中 MothX 能产生的类型：

- `user_message_chunk`、`agent_message_chunk`、`agent_thought_chunk`；
- `tool_call`、`tool_call_update`，状态只使用 pending/in_progress/completed/failed；
- `plan`；
- `available_commands_update`（slash command catalog 变化时发送）；
- `current_mode_update`；
- `config_option_update`；
- `session_info_update`（title/updatedAt 等 session metadata 变化；cwd/additional directories 以 setup/list/load/resume 的 `SessionInfo` 为权威）；
- `usage_update`。

每种 update 都从 canonical Agent/Runtime event 生成。ACP 不再在 handler 内拼装第二套 run 状态；重连/恢复时只重放 Runtime 已持久化的语义，不能重复产生 usage 或 tool completion。

### 7.6 反向 client request、permission 与 elicitation

- `session/request_permission` 继续使用 `DecisionService`，保持 deadline、first-response-wins、replay 和过期 terminalization；
- 将现有 `_mothx/request_question` 抽象成统一 question decision；当 client 声明标准 elicitation form/url capability 时，优先发送标准 `elicitation/create`，将 resolution 映射回 DecisionService；不支持时保留 `_mothx` 扩展并标注兼容路径；
- fs/read、fs/write、terminal/create、terminal/output/release/wait/kill 只在 Agent runtime 明确需要 client-owned resource 时启用。MothX 本地工具默认不需要这些反向调用，因此不应无条件伪造 client capability；
- 所有反向 request 使用独立 JSON-RPC ID，允许多个 pending request 并发，response 必须按 ID 匹配，session close 时全部取消并 terminalize。

### 7.7 MCP

- stdio MCP server 是 ACP baseline，复用 `internal/mcp` 的 server config、连接生命周期和 shutdown；
- HTTP/SSE 只有在 initialize 的 agent MCP capability 真实声明时接受；能力关闭时返回 `-32602`；
- MCP 连接建立失败不能留下半个 session；关闭、delete、runtime shutdown 必须释放所有 client；
- sampling 使用本次 prompt 的 model/provider snapshot，禁止回读进程级默认模型；
- MCP server 配置和 ACP session 生命周期只由 Runtime 管理，ACP 仅做协议转换。

### 7.8 Auth、logout 与扩展

MothX 当前不提供 ACP authentication，因此 initialize 返回空 `authMethods`，不声明需要 auth 的路径。若以后增加认证，必须同时实现标准 authenticate、logout 和 auth error 状态，不把 provider API key 当作 ACP auth method。

`_mothx/*` 扩展（request question、session event 等）继续支持已有 client，但扩展不得替代已有标准方法。每个扩展必须有 namespace、版本/兼容说明、错误行为和独立测试。

## 8. Go wire client 与依赖策略

不把 Python client 或第三方 ACP SDK 引入 MothX 默认依赖。实现阶段评估过 [`coder/acp-go-sdk`](https://github.com/coder/acp-go-sdk) 等社区库，但 ACP schema 演进速度、额外 module 依赖和 Harbor 测试隔离要求使标准库 wire client 更适合作为默认真实测试 client：它直接验证 MothX 的 NDJSON/JSON-RPC framing、响应顺序、通知和错误，而不是把行为藏在 SDK 适配层中。

最终策略如下：

1. 用 Go 标准库 `os/exec`、`bufio`、`encoding/json` 建立真实子进程 stdio client，覆盖 initialize/new/set-config/resume/close 和多 session 隔离；
2. 对 schema 中稳定但测试 client 不需要建模的字段使用最小 raw JSON contract fixture，不复制第二套 ACP runtime；
3. 不为了默认测试引入 Python、第二个 runtime 或新的 ACP wire implementation；外部 Harbor 适配验证可以调用 Harbor 自己已有的 Python 环境，但不把它加入 MothX 依赖；
4. 若未来引入第三方 SDK，必须作为明确的独立依赖评审，并保留标准库 raw wire contract 作为协议基线。

## 9. 测试与验收矩阵

### 9.1 真实 Go wire client process test

沿用 ACP 现有 subprocess helper，在临时目录启动 MothX ACP server，使用标准库 Go wire client 通过 stdio 完成：

1. initialize/version/capability negotiation；
2. 设置 `HARBOR_ACP_REQUESTED_MODEL` 后 `session/new`，断言返回 model config option；
3. 多 session 各自设置不同 model/mode，交替 prompt，验证 provider capture、MCP sampling、usage 和 events 不串；
4. `session/set_config_option` 成功返回全量 options，并在下一次 prompt 生效；
5. 非法 model、错误 configId、错误类型、空值返回标准错误且不改变旧 binding；活动 run 中的合法配置修改成功返回完整 options，并只影响下一次 prompt；
6. load/resume/list/close/delete 和 reconnect；
7. prompt cancellation、`$/cancel_request`、反向 permission/elicitation response；
8. server 关闭时活动 run、pending decision、MCP client 和 reverse request 都 terminalize。

### 9.2 Wire contract test

不依赖 SDK 的 raw JSON 测试覆盖：

- 空行/超长行/非法 JSON/非法 JSON-RPC/重复 ID/notification；
- initialize 前后状态机、未知 request/notification、标准 error code；
- response 与 notification 并发写入不交错，reverse request ID 正确匹配；
- `configOptions` 全量返回、boolean capability gate、mode legacy compatibility；
- text/resource_link/image/audio 的 capability gate 和字段保留；
- messageId 在连续 chunk 中稳定，tool/plan/update 状态和 stop reason 完整；
- additional directories 的绝对路径、空/省略/替换、list/load/resume 语义；
- MCP stdio baseline、HTTP/SSE capability gate；
- `session/delete` active refusal、close 幂等、delete 后 load 失败、分页 cursor opaque。

### 9.3 Runtime/architecture regression

- `go test ./internal/acp/...`、`go test ./internal/agentruntime/...` 和相关 session/provider tests；
- 修改 Agent construction、Run persistence 或 shutdown 后运行 `go test ./internal/architecture`；
- race test 覆盖多 session、并发 prompt、cancel、reverse request 和 shutdown；
- provider fake 必须记录 model/provider，但测试输出不得包含 secrets；
- schema fixture 测试固定 artifact/revision，升级 schema 时显式更新 proposal/test 记录。

### 9.4 必须拒绝的错误场景

下列情况不得静默降级：

- Harbor env 指向未知 provider/model；
- session config option 不属于当前 provider/model catalog；
- 当前 prompt 热切换 model/provider（合法的活动 run 配置修改只影响下一次 prompt）；
- client 没有声明对应能力却发送 boolean config、image/audio、HTTP MCP 或 elicitation；
- additional directory 非绝对路径、不可访问或规范化后越界；
- resume/load 恢复到已删除或 provider 不匹配的模型；
- session lifecycle 已关闭仍接收 prompt；
- JSON-RPC ID 不匹配或 response 重复。

### 9.5 Harbor baseline compatibility test

这组验证不是把 Harbor 的 Python SDK 引入 MothX，而是把 `/home/free/src/harbor` 的 generic ACP runner 当作独立的真实互操作 client。它只在本地或专用适配验证环境中显式运行，测试 fixture 记录 Harbor commit、其 `agent-client-protocol` 包版本和 MothX launcher；不加入 `make test`、默认 CI、Go module 依赖或发布门禁。Harbor 不可用时，默认 Go process test 和 raw wire test 不应失败，该互操作验证标记为未运行即可。

显式执行该适配验证时至少包含以下场景：

1. Harbor 通过 stdio 启动 MothX child，完成 `initialize(protocolVersion=1)`；断言 MothX 接受 Harbor 声明的 `fs.readTextFile`、`fs.writeTextFile`、`terminal` 和 `auth.terminal=false`，且只声明自身真实支持的反向服务。
2. Harbor 注入 `HARBOR_ACP_REQUESTED_MODEL=provider/model`、`HARBOR_ACP_PERMISSION_MODE`、`HARBOR_ACP_AUTH_POLICY` 和 `HARBOR_ACP_MCP_SERVERS_JSON`；断言 model 只绑定该 session，MCP stdio/SSE/streamable HTTP payload 按 capability 处理，其他入口和全局 settings 不被改写。
3. `session/new` 返回现代 `configOptions` 中的 model option，并且不返回 legacy `session.models`；Harbor 必须实际调用 `session/set_config_option` 后再 prompt。完整 provider/model、providerless alias、未知模型分别覆盖成功、唯一映射和失败不 prompt 三种结果。
4. 在同一连接创建两个 session，分别设置不同模型和 mode，交替 prompt；检查 provider/model capture、MCP sampling、usage、message/tool/plan/config/session-info update 不串 session，且每个 session 的恢复仍使用自己的 binding。
5. 让 MothX 触发 permission、标准 elicitation（若双方 capability 开启）、`fs/read_text_file`/`fs/write_text_file` 或 `terminal/*` 反向请求；Harbor 返回 allow_once/allow_always、deny/cancelled、绝对路径和 terminal output/exit，断言 ID 匹配、错误码、deadline 和关闭时 terminalization。
6. 让 prompt 正常结束、取消、非法模型失败和 provider 错误失败；Harbor 的 `acp-events.jsonl`、summary、trajectory 能解析 MothX 的标准 update、usage、stop reason 和失败信息，且没有重复终态或隐式 fallback。
7. 不设置 `HARBOR_ACP_REQUESTED_MODEL` 时重复一次最小 prompt，确认普通 ACP client 仍沿用 settings/default binding；该场景不能因为 model option 存在而强制发送 set-config request。

Go 标准库 wire client 是 MothX 的默认、可重复单元/进程测试 client；Harbor runner 只作为外部适配验证手段，不能替代 MothX 自有的 Go/wire 测试，也不会阻塞默认测试流程。

## 10. 一次性目标下的实施顺序

本方案不是先交付一个 ACP 子集、再逐步把其他入口迁移过来。下面的顺序只是同一项架构变更中的依赖关系：最终验收必须同时满足统一 Runtime、五类薄适配层和 ACP v1 完整 wire contract。任何中间提交都不得新增第二套模型、Session、Run、Decision 或 Agent 实现，也不得把不完整能力声明给客户端。

### 10.1 先扩展共享 Runtime 契约

- 在 `internal/agentruntime` 定义 session option catalog、`SessionBinding`、读取/设置/校验/replay 和统一错误语义；
- 将 Harbor env、CLI/settings、TUI/WebUI/Channel 请求和 ACP 请求都归一到同一个 source/policy resolver；
- 让 `BuildAgent`、MCP sampling、Run/Usage/Event、child-agent inheritance、cancel/recovery/shutdown 只读取 binding snapshot；
- 在 session store 中复用并补齐 model/config metadata replay，不新增 ACP 专属持久化表。

### 10.2 同步迁移全部入口适配层

- TUI、CLI、WebUI/API、Channel、ACP 都改为调用共享 Runtime API；
- 删除或禁止入口级 provider/model 默认值、Agent config 组装、Session replay、Run persistence 和 cancellation fallback；
- 各入口只保留自己的输入解析、连接/窗口/消息状态、协议编码和事件渲染；
- 为每个入口增加同一组 cross-entry contract test，证明相同 session option 和 policy 得到相同 binding、Agent 配置和生命周期结果。

### 10.3 在共享契约之上完成 ACP v1 adapter

- 实现 typed initialize、capability negotiation、session lifecycle、config options、JSON-RPC error/ID/order/cancel 和已声明能力的标准 update projection；additional directories 已由共享 Runtime 完整拥有并声明，未实现的 client-owned service 仍不声明；
- 使用标准库 Go wire client 完成多 session、非法模型、配置变更、恢复和关闭测试；
- 用 raw JSON schema fixtures 覆盖 wire contract，不让协议正确性依赖 SDK 版本；
- 只有 handler、Runtime 行为和端到端测试全部就绪后，才声明对应 capability；
- `_mothx/*` 仅作为兼容扩展，不能成为标准功能的替代实现。

### 10.4 一次性验收与发布

- 对照官方 v1 schema 逐项检查 method、notification、capability、content、update、错误和生命周期；
- 运行 TUI/CLI/WebUI/Channel/ACP 的统一 contract、race、architecture 和真实进程测试；
- Harbor compatibility validation 作为显式的外部适配检查单独运行，不加入 `make test`、默认 CI 或发布门禁；
- 同步双语文档、changelog、诊断信息和旧 session/旧 client migration note；
- 明确 v2 alpha 和未稳定扩展的独立边界，不在 v1 实现中留下隐式承诺。

## 11. 预计涉及文件

这里只列实现阶段的边界。重点是一次共享 Runtime 变更，而不是 ACP 单包改造：

- `internal/agentruntime/`：session binding/config snapshot、option catalog、additional-directory 解析和统一错误/生命周期 API；
- `internal/session/`：复用并补齐 model/config metadata replay，必要时追加 migration；
- `internal/provider/factory/`：只复用现有 catalog/compatibility，不在 ACP 层复制；
- `internal/tui/`、`cmd/mothx/`：TUI/CLI 的模型、模式和 session option 统一接入 Runtime；
- `internal/serve/`、`internal/serve/openaiapi/`：WebUI/API 统一接入 Runtime，不保留 serve 专属模型 binding；
- `internal/serve/channels/` 及相关 channel adapter：消息命令、强制 mode 和后台 run 统一接入 Runtime；
- `internal/acp/acp.go`：只保留协议状态、typed capabilities、session handlers、projection 和 transport；
- `internal/acp/*_test.go`：unit、wire、subprocess/real Go client tests；
- `go.mod`/`go.sum`：本次不增加 ACP SDK 或 Harbor/Python 测试依赖；真实协议测试使用标准库 Go wire client，schema 较新的字段用 raw JSON fixture；
- Harbor compatibility validation：只依赖外部 `/home/free/src/harbor` 及其 Python 环境，作为显式手工/专用环境检查，不进入默认测试 target、CI 或 Go module；
- `scripts/validate-harbor-acp.sh`：显式手工互操作入口。它只检查 Harbor runner、MothX binary 和外部 Python 环境，输出 Harbor 的 `acp-summary.json`/`acp-events.jsonl`，不被任何默认测试或 CI 调用；
- `docs/en`、`docs/zh` 及 changelog：实现完成后同步用户可见协议/配置行为。

不修改生成目录，不新增 Python runtime，不新增 ACP 专用 Agent/Run/Decision/MCP store，也不新增第二个前端无关 Runtime。

## 12. 待确认的产品决策

以下是本方案直接采用的默认决策；它们不是能力分阶段开关。如果评审没有提出相反约束，实施按这些规则一次性完成：

1. `HARBOR_ACP_REQUESTED_MODEL` 严格按首个 `/` 分隔 provider，后续 `/` 保留在 model ID 中，并经过统一 catalog 校验。
2. `thought_level` 在共享 option catalog 中一次性实现，只有 provider/model 明确支持时才出现在对外 options；不支持时由同一 catalog 省略。
3. additional directories 已由共享 Runtime 完整校验、持久化并在 list/load/resume 中保持语义，因此 initialize 会声明该 capability；client-owned fs/terminal、URL elicitation 和 authentication 仍不声明，不以空字段冒充支持。
4. 标准 elicitation 优先映射到 `DecisionService`，`_mothx/request_question` 仅作为旧 client 的兼容兜底；两者不能形成两套 decision state。
5. 默认真实测试使用标准库 Go wire client，schema 较新的稳定字段用最小 raw JSON fixture；Harbor Python runner 只在外部适配验证时使用，不引入仓库依赖或第二套 client 实现。

## 13. 方案验收标准（已采用）

本次实现按以下判断验收：

- 目标是 ACP v1 的完整 wire compatibility，不是无条件实现所有可选 client service；
- 能力声明必须与实际 handler 和端到端测试一致；
- session model 是 session 级配置，provider 仍是 ACP 进程级绑定；
- TUI、CLI、WebUI、Channel、ACP 都只是薄适配层；所有 Agent construction、Run、Decision、MCP、shutdown、配置解析和 policy resolution 继续走同一套 `internal/agentruntime`；
- Go 标准库 wire client 负责默认真实协议测试，schema 较新的部分用 raw JSON contract fixture 补齐；Harbor Python runner 仅作为不纳入默认测试/CI 的外部适配验证手段；
- 以一次性最终架构为验收对象，实施顺序只表达依赖关系，不发布“部分迁移”的架构状态；
- 旧 session、旧 client、legacy `session/set_mode` 和 `_mothx` 扩展都有明确兼容行为，不靠隐式回退。

## 14. 实施档案

2026-08-21 的实施已经按上述边界推进，当前落地内容如下：

- `internal/agentruntime` 增加 session 配置快照、model/mode/thinking-level option catalog、统一校验和持久化；`internal/session` 增加 mode/config replay；provider factory 增加严格的 `provider/model` 解析与 catalog 校验。对应 Runtime、session、provider 单元测试已加入默认 Go 测试集合。
- ACP 进程级解析 `HARBOR_ACP_REQUESTED_MODEL`，新建、加载和恢复 session 返回现代 `configOptions` 与 `modes`；`session/set_config_option`、标准 `session/set_mode` 都委托 `SessionRuntime`，设置后返回完整 option catalog，并由 `SessionRuntime.BuildAgent` 接入既有 Agent Core。
- ACP prompt、MCP sampling、usage/run intent 和事件投影都读取同一 session binding；多 session 可独立切换模型，非法模型在启动或设置时拒绝，不会静默 fallback。流式 message chunk 使用稳定共享 `messageId`，`config_option_update`、`current_mode_update` 和 `max_turn_requests` 按 v1 wire contract 投影。
- 标准 `elicitation/create` form 已接入 question decision：client 声明 form capability 时使用标准请求和 `accept/decline/cancel` 结果，超时、取消、持久化、重放和 first-response-wins 仍由同一 `DecisionService` 管理；未声明 form 的旧 client 继续收到 `_mothx/request_question`。
- session option/mode 持久化变更后发送标准 `config_option_update`、`current_mode_update` 和 `session_info_update`；session metadata 的 cwd/additional directories 仍以 setup/list/load/resume 返回值为权威来源。
- prompt 启动时显式冻结 provider/model/mode/thinking 快照并传入 `SessionRuntime.BuildAgent`；活动 run 的 usage/cost 也使用该快照，运行中设置下一次 session option 不会改变当前 turn 的模型语义。HARBOR requested model 与显式 CLI provider/model 冲突时 fail closed。
- 增加不引入第三方 ACP client 的 Go stdio process test，覆盖真实 initialize、requested model、两个 session 的隔离、模型切换和非法模型；raw wire 测试覆盖 typed client capabilities、JSON-RPC parse/ID 错误、opaque cursor、resource_link、结构化 diff/location 和显式 `oldText: null`。
- JSON-RPC writer 已统一抑制无 ID notification response，并对非法 request 返回 `id: null`；请求 ID 严格接受 integer/string/null，拒绝对象、数组和小数。
- additional directories 已接入共享 Runtime、Session metadata、Registry path resolution、sandbox read/write policy 和 ACP `session/list`/`load`/`resume`；initialize 仅声明已完成端到端语义的 capability。
- Harbor Python runner 仍只作为 `/home/free/src/harbor` 外部适配验证，使用 `scripts/validate-harbor-acp.sh` 主动运行，不进入 `make test`、CI、Go module 或默认发布门禁。
- 2026-08-21 最终验证：`go test ./...`、`go test -race ./internal/acp ./internal/agentruntime`、`go test ./internal/architecture`、未缓存 ACP 进程测试、`bash -n scripts/validate-harbor-acp.sh` 和 `git diff --check` 全部通过；Harbor runner 未纳入默认测试，需在具备其外部 Python 环境和模型凭据的适配环境中按脚本手工运行。

后续扩展仅限于更多 raw JSON schema fixture、反向 request/取消生命周期断言，以及在共享 Runtime 具备完整 owner 后再评估 URL elicitation 和 client-owned fs/terminal。当前已声明能力不得扩展为“空实现”；协议版本保持 v1，不把 v2 alpha 或未稳定字段混入实现。
