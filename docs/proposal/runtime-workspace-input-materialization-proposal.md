# Runtime 工作区输入文件物化方案

> 状态：最终方案；阶段 1 输入物化与 artifact 私有快照已部分落地
>
> 日期：2026-08-27
>
> 关联方案：[统一 Agent Core 与 Runtime](./agent-core-runtime-unification-proposal.md)、[微信/飞书通道媒体协议与 Artifact 投递](./channel-media-attachments-proposal.md)、[图片处理优化](./multimodal-image-optimization.md)

## 1. 结论

MothX 的用户输入文件（图片、文档、压缩包及其他二进制文件）必须只有一种语义：**Runtime 将字节物化到当前项目的 `.mothx/tmp`，首轮用户消息只声明这些工作区路径和元数据；Agent 自己决定是否调用 `read`、以何种参数读取，或调用 Skill/其他工具解析。**

这条规则同时适用于 TUI、CLI、WebUI/API、ACP、微信和飞书。图片不是特殊的“自动视觉输入”；文件也不是特殊的“自动文本提取输入”。两者首先都是用户交给当前项目的文件。视觉内容仅在 Agent 明确调用标准 `read` 工具读取图片后，作为该工具结果的 rich content 交给 provider。文档的 OCR、PDF/Office 解析、表格处理、解压和其他专门流程同样只能由 Agent 的工具或 Skill 选择触发。

因此：

1. 不在入站 adapter、`BuildUserMessage` 或 provider 请求装配中直接创建 `provider.ImageContent`。
2. 不因当前模型不声明 image input 而拒绝接收图片；是否需要视觉模型由 Agent 选择 `read` 后的实际工具执行决定。
3. 不保留 `read_attachment` 作为入站文件的第二条读取语义；用户输入文件都使用工作区标准路径和现有 `read`/Skill/tool 生态。
4. 不让 TUI `/paste-image` 自己写文件、WebUI 自己保存上传、Channel 自己下载到项目目录。它们只提交来源流，物化、命名、记录、回放和清理由 `internal/agentruntime` 唯一拥有。
5. 不以文件类型、模型能力或 adapter 偏好替 Agent 作“立刻解析”“直接发图”“自动 OCR”的决定。

这不是降低多模态能力：它把多模态能力放回 Agent Core 的显式工具决策中，使所有入口与 `/paste-image` 的路径优先行为完全一致，并避免用户未要求分析图片时产生视觉 token、延迟和成本。

## 2. 目标、范围与非目标

### 2.1 目标

- 一个前端中立的 Runtime 输入资源合同，覆盖文本、图片、文件和协议 content part。
- 任何外部输入文件都落在运行该 Session 的项目 `.mothx/tmp` 中，并以相对于项目根目录的稳定路径提供给 Agent。
- Agent 对每个文件独立决定：不读取、调用 `read`、给 `read` 指定 `imageMode`/`crop`，或调用专门 Skill/工具。
- 统一 session/run 归属、持久化、重放、事件、取消和清理；adapter 不拥有第二份附件状态或本地缓存。
- `/paste-image` 保留“保存文件并把路径带入输入”的交互体验，同时改为 Runtime 而非 TUI 写入。
- 飞书/微信的媒体下载、WebUI/API multipart/base64、ACP resource content、CLI/TUI 本地文件均在相同 Runtime 边界汇合。
- 已显式发布的生成物使用工作目录之外的 Runtime 私有不可变快照和 canonical artifact/delivery 模型，不从助手文本或 adapter 输出目录推断，也不把 Agent 可写项目路径误称为不可变 artifact。

### 2.2 范围

“所有文件”在本方案中指所有由 Runtime 接收、物化、登记和交付的文件，但输入与 artifact 的可见性不同：用户从任何入口提交的文件物化为项目内输入文件；经 `publish_artifact` 发布的生成物复制到 Runtime 私有 artifact store。Agent 在正常工作目录手工创建、尚未作为输入或 artifact 登记的文件仍属于普通项目文件；它们不会被 Runtime 擅自移动或扫描。

浏览器工具、截图工具等**工具输出**仍是 Agent 已执行工具后的结果，不属于本方案的用户入站链路。它们继续遵守各自的 ToolResult rich-content 合同；不能借此重新为 WebUI/Channel 的用户上传建立首轮 `ImageContent` 旁路。

### 2.3 非目标

- 不自动判断文件“应该”被 OCR、总结、解压或导入哪一个 Skill。
- 不新增多租户网盘、默认病毒扫描、人工审核、复杂下载令牌、日配额、按身份隔离或自动短 TTL。MothX 的目标部署是用户自有的本机/可信网络服务，这些机制不能妨碍正常地传截图和文档。
- 不改变标准 `read` 工具对图片的图像预处理策略；它仍是 Agent 选择图片读取后的唯一视觉转换位置。
- 不让 adapter 用本地路径、data URL、base64 或平台 URL 直接构造 provider message。
- 不从助手回答中的路径、链接或 Markdown 猜测某文件需要作为 artifact 发送。

## 3. 不变量

```text
任意入口的原始字节/引用
        │
        ▼
薄 adapter：解析协议，提供一次性受权 Reader
        │
        ▼
agentruntime.InputMaterializer：写入 <project>/.mothx/tmp，登记资源
        │
        ▼
SessionRuntime：建立 canonical user entry（文本 + 路径 manifest）
        │
        ▼
Agent Core：自主选择 read / Skill / 其他工具 / 不读取
        │
        ├── read 图片 → imageproc → ToolResult rich image → provider
        └── 发布生成物 → Runtime private snapshot → canonical artifact/delivery
```

下列不变量必须同时成立：

1. **一条输入路径。** 所有入口都以同一个 `InputIngress`/`PreparedInput`/`InputSubmission` Runtime 合同提交。不存在 adapter 专用字符串 runner、图片 runner 或文件 runner。
2. **一处物化。** 只有 `internal/agentruntime` 可以把外部输入写入 `.mothx/tmp`、生成路径、计算哈希、登记 session 资源和清理 Runtime 管理的文件。
3. **首轮只传文本。** `BuildUserMessage` 对入站资源仅生成文本 manifest；它不读取文件字节、不 base64 编码、不进行 MIME→视觉块转换，也不注册特例文件读取工具。
4. **读取是 Agent 行为。** Runtime 只把文件和路径交付给 Agent。`read`、Skill、MCP 或其他被授权工具决定实际解析；Runtime 不提前做 OCR、文档提取、解压、图片缩放或模型能力拒绝。
5. **路径可用且可回放。** manifest 给出的路径一定是当前 Runtime 的项目工作目录内相对路径。session/run 记录保留资源 ID、路径、哈希和元数据；重放时 Runtime 验证物化文件仍存在，而不是凭空重建或丢失路径。
6. **工具语义统一。** 图片通过普通 `read` 读出时，才复用 `imageproc` 和 `provider.ImageContent` ToolResult；非图片仍由普通 `read` 或显式 Skill/工具处理。没有 `read_attachment` 的并行文件工具语义。
7. **输入与 artifact 均为 Runtime 所有，但存储边界不同。** 输入物化到 Agent 可读的 `.mothx/tmp/inputs`；只有 Runtime 可将明确发布的 artifact 快照到工作目录之外的私有 store 并登记投递。adapter 只能上传/发送 Runtime 已授权的 artifact ID，不能扫描或直接发送工作树文件。
8. **轻量可靠性而非惩罚性安全。** 采用流式写入、原子落盘、路径清理、文件名去冲突和可选的本地资源限制，以避免损坏和意外耗尽；默认不按类型拦截、不过期用户仍引用的输入，也不增加登录/审核/公网交付障碍。

## 4. Runtime 输入资源合同

`internal/agentruntime` 定义并唯一拥有以下概念；确切 Go 名称可以随既有命名调整，职责边界不可变化。

```go
// adapter 到 Runtime 的一次性、不可持久化交接。
type InputIngress struct {
    Origin       string            // tui, cli, webui, api, acp, channel:wechat, channel:feishu
    EventID      string            // 稳定、可持久化且不含凭据的事件/草稿 ID
    ItemIndex    int               // 同一事件/草稿内资源的稳定序号
    Reference    string            // opaque transport/local reference，仅供本次 Open/受限诊断
    FilenameHint string
    MediaTypeHint string
    SizeHint     int64
    Open         func(context.Context) (InputStream, error)
}

type InputStream struct {
    Reader      io.ReadCloser
    Filename    string
    MediaType   string
    ContentSize int64
}

// Runtime 已物化、可在一次或多次提交中引用的资源。
type InputResource struct {
    ID           string
    SessionID    string
    RelativePath string            // 例如 .mothx/tmp/inputs/<resource-id>/screen.png
    Filename     string
    MediaType    string
    Bytes        int64
    SHA256       string
    Origin       string
    EventID      string
    ItemIndex    int
    ItemKey      string
    RunID        string
    Status       string            // prepared, attached, missing, deleted
    CreatedAt    time.Time
}

type PreparedInput struct {
    ResourceID   string
    Kind         AttachmentKind
    RelativePath string
    Filename     string
    MediaType    string
    Bytes        int64
}

type InputSubmission struct {
    Text           string
    Resources      []PreparedInput
    IdempotencyKey string          // event/draft submission key；admission 事务中唯一
}
```

`InputIngress` 是 adapter 的终点，而不是 session 数据模型。它可来自本地选择器、剪贴板临时流、HTTP multipart、OpenAI-compatible content part、ACP resource、飞书已鉴权下载流或微信 iLink 解密流。adapter 绝不持久化它的 `Reference`、认证 URL、token、加密密钥或字节。resource item key 使用 `session_id + origin + event_id + item_index`；若协议确实只能从敏感引用得到稳定身份，由 Runtime 使用安装级密钥计算 HMAC，禁止保存引用原文。

`PrepareInput` 在 Runtime 内完成流读取、物化和 `InputResource` 建立；`AcceptInput`/统一执行入口把已有的 `InputSubmission` 原子绑定到 Run。`IdempotencyKey` 必须与 user entry、resource/run 绑定、execution intent 和 Run/start event 在同一 admission 事务中唯一；并发重复提交返回已有 canonical Run ID，不能仅因 resources 已去重就再次启动 Run。这样既能让 channel 在收到消息时直接提交，也能让 TUI `/paste-image`、WebUI 文件选择和 ACP 编辑器在“发送”之前先得到一个 Runtime-issued resource ID 与路径。UI 仅保存不透明 resource ID、稳定 draft/submission key 和展示路径，不保存第二份附件事实。

一个 draft 资源可被当前未发送输入反复预览；提交后它变为对应 Run 的 `attached` 资源。取消编辑不会让 adapter 删除文件，Runtime 通过显式 discard/项目清理处理未绑定 draft。重复提交同一资源的归属与事件由 Runtime 以 resource ID 裁决，adapter 不以文件名猜测。

## 5. 输入工作区、私有 artifact 与持久化

用户输入位于 Session 当前项目内，artifact 位于 Agent 工作目录之外的 Runtime 私有存储：

```text
<workDir>/.mothx/tmp/
  inputs/
    <resource-id>/
      <sanitized-original-name>

<sessionDir>/artifacts/
  <artifact-id>/
    content
```

- `resource-id`/`artifact-id` 保证唯一；展示文件名只经 Runtime 清理后保存为元数据，不能影响目录选择或逃逸工作目录/私有 store。
- 写入使用同目录临时文件、流式 hash 和原子 rename。失败时 Runtime 清理不完整临时文件，并把规范错误投影给原入口。
- Agent 收到的永远是正斜杠形式的 `RelativePath`；标准 Registry 从当前 `workDir` 解析它。adapter 不向 Agent 提供绝对路径、平台 URL、私有 storage key 或 token。
- session 层记录 resource/artifact ID、session/run ID、输入相对路径或私有 storage key、原展示名、MIME、字节数、SHA-256、来源、创建/绑定状态和时间。数据库记录是归属、重放和清理索引；`.mothx/tmp/inputs` 中的规范文件是 Agent 可读的实际输入对象，artifact storage key 不进入 prompt 或 adapter payload。
- Runtime 以哈希和路径检查处理崩溃恢复：有记录无文件标记 `missing`；有完成文件无记录的 Runtime 临时项可由显式项目清理收集；不得由 adapter 进行目录扫描或补写记录。
- 不默认删除已被 session entry 或 Run 引用的文件。用户删除项目、显式执行项目 Runtime 清理或明确删除会话时，Runtime 按引用关系删除相应目录和记录。可配置的资源上限属于用户自有部署的资源控制，默认不以文件类型、每日额度或短 TTL 阻断正常使用。

`publish_artifact` 对已完成的普通工作区文件执行复制/原子快照到 `<sessionDir>/artifacts/<artifact-id>/content`；随后才允许 delivery。它拒绝目录、符号链接逃逸和未完成文件，但不会扫描助手文本或 adapter 本地缓存来推断 artifact。每次 WebUI 下载、ACP 投影或 Channel 上传按 artifact ID 打开内容时，都必须重新验证 regular-file、大小和 SHA-256；不匹配时产生 canonical integrity failure，绝不发送变化后的字节。

不得用项目文件权限、`.gitignore` 或仅部分 sandbox mode 的 deny rule 来声称 `.mothx/tmp/artifacts` 不可变。在 `yolo` 或普通工作区写权限下，项目内文件可被 Agent 后续修改，因此目标设计不再创建该目录作为 artifact 权威存储。

## 6. 从接收至 Agent 决策的完整语义

### 6.1 接收与物化

1. adapter 验证自己的平台事件或本地交互，创建只含元数据和 `Open` 函数的 `InputIngress`。
2. Runtime 打开流，确定文件名和 MIME 展示元数据，写入 `.mothx/tmp/inputs`，计算字节数和 SHA-256，建立 session resource record，并发布 canonical `input_resource_prepared` 事件。
3. Runtime 返回 `PreparedInput`。TUI/WebUI/ACP 可显示文件名、大小和相对路径；channel 可直接把它与同一条消息的文本组成提交。
4. Runtime 将 `InputSubmission` 绑定到本次 durable Run，写入 canonical 用户 entry 和 resource/run 关联。失败、重复或取消使用 `ExecutionRuntime`/RunStore 的既有生命周期，不由 adapter 创建另一张状态表。

文件接收只允许做有界的 MIME sniff、图片头/`DecodeConfig` 检查、尺寸与像素上限校验；不得在 ingress 阶段完整解码图片、转写 Office/PDF、展开压缩包或尝试以文件内容改写用户文本。入站文件是数据，不是 prompt 指令。完整图片解码、缩放和转码只在 Agent 明确调用 `read` 或其他图片工具后发生。

### 6.2 首轮消息构造

`SessionRuntime.BuildUserMessage` 生成一个普通文本用户消息。它保留用户文本，附加确定性 manifest，例如：

```text
请检查这些文件的差异。

[Runtime-managed input files for this request]
- path: .mothx/tmp/inputs/rs_01/screenshot.png
  name: screenshot.png
  mediaType: image/png
  bytes: 182034
- path: .mothx/tmp/inputs/rs_02/report.pdf
  name: report.pdf
  mediaType: application/pdf
  bytes: 948122

Decide whether a file needs inspection. Use read for files you choose to read,
or use an appropriate available Skill/tool when specialized parsing is useful.
Do not claim to have examined a file that you did not read.
```

该 manifest 是 Runtime 生成的、不可由 adapter 自由拼接的协议文本。它不含 base64、原始二进制、平台下载地址、凭证或隐藏 storage path。没有资源时，用户消息仍是纯文本，不存在旧的字符串输入旁路。

### 6.3 Agent 的显式读取

- Agent 不需要读取文件时，不调用工具，首轮请求不产生视觉 token 或文档解析成本。
- Agent 需要看图片时，调用现有 `read`，可选择 `imageMode=fast|auto|detail|raw`、`maxLongEdge` 或 `crop`。`read` 才调用 `imageproc` 并以标准 ToolResult 的 text + rich image content 返回给 Agent Core/provider。
- Agent 需要看文本、源代码、JSON、CSV 或其他 `read` 支持的文件时，调用同一个 `read`。
- Agent 需要处理 PDF、Office、音视频、压缩包、OCR、结构化表格或领域文档时，自主选择可用 Skill、MCP 或专用工具。Runtime 不把任意格式锁定到内置 extractor，也不伪装成已读内容。
- Agent Core 在所有 ToolResult 合并到下一轮 provider request 前，依据 Runtime 已解析的 provider/model capability 对 rich image content 执行统一门禁。该门禁覆盖 `read`、浏览器截图、图片生成和未来任何图片工具；不支持图片输入时返回同一类明确、可恢复的能力错误，文件和用户请求继续存在，Agent 可以解释限制或改用已安装的本地解析 Skill。不得在接收时提前拒绝整个 Run，也不得在某个 provider adapter 中静默剥离图片。

此处唯一允许的 `provider.ImageContent` 来源是 tool result（或其他 Agent 已显式执行的工具结果），而不是用户入站 resource 的首轮 prompt 装配。

### 6.4 重放、恢复与编辑

session history 持久化输入文本、resource ID、相对路径和不可变元数据，不持久化 provider image block 或重复 base64。重放同一用户 entry 时，Runtime 重新验证其 resource record 和工作区文件：存在则恢复相同 manifest；缺失则以同一 resource ID 显示“文件已不存在”，并禁止 Runtime 或 adapter 虚构内容。运行中断后的 durable Run 使用相同 Resource/Run 关联恢复和终结。

文件路径在用户发送后是事实记录，不能被后续 UI 隐式替换。用户重新上传、重新粘贴或明确编辑输入时，Runtime 创建新 resource ID；这使 transcript、哈希、工具读取和重试可追踪。

## 7. 各入口的薄 adapter 合同

| 入口 | adapter 允许做的事 | 必须调用的 Runtime 行为 | 明确禁止 |
|---|---|---|---|
| TUI `/paste-image` | 从剪贴板取得 PNG 流，显示 Runtime 返回的路径/预览状态 | `PrepareInput`，在发送时以 resource ID 提交 | 直接 `os.WriteFile` 到 `.mothx/tmp`、TUI 清理器、直接图片 provider content |
| TUI/CLI 本地文件 | 选择本地文件并提供受控 `Open` | `PrepareInput` + `InputSubmission` | 把用户给出的绝对路径直接拼进 provider prompt，维护本地附件目录 |
| WebUI/API | 解析 multipart、data URL 或协议 content part 为流 | 同一 `PrepareInput` + `InputSubmission` | handler 保存 upload、创建 `provider.ImageContent`、按模型能力拒绝上传 |
| ACP | 将 resource/content part 解码为流，投影 canonical event | 同一 Runtime 合同 | JSON-RPC 侧自建附件记录或 provider message |
| 微信 | 解析 iLink 媒体引用，提供已鉴权、已解密 Reader | 同一 Runtime 合同 | adapter 写项目文件、把 CDN URL/密钥写入 session 或 prompt |
| 飞书 | 解析资源 key，提供官方受权下载 Reader | 同一 Runtime 合同 | adapter 直接保存文件、自动 OCR/直发视觉内容 |

Channel adapter 的媒体接收和出站 delivery capability 仍可不同：飞书按官方 SDK 上传/发送已发布 artifact；微信按[通道媒体协议方案](./channel-media-attachments-proposal.md)锁定的腾讯 npm 发布物原生发送图片、视频和文件，当前不宣称支持语音出站。类型和平台限制只发生在 Runtime 已产生 canonical artifact 后的 transport 层，绝不能改变入站文件物化和 Agent 读取语义。

## 8. Runtime、工具与 artifact 的实现边界

### 8.1 Runtime 责任

`AttachmentService` 演进为以工作区物化为事实的 `InputMaterializer`/resource service，继续由 `SessionRuntime` 持有。它负责：流读取、原子文件写入、文件名规范化、hash/metadata、resource 持久化、draft 归属、Run 绑定、重放核验、显式 discard/项目清理，以及 canonical resource/artifact 事件。

它不负责判断文件价值、读取内容、图片预处理、技能选择或直接 provider 转换。所有这些决定属于 Agent Core 运行时的工具调用。

### 8.2 Agent 和工具责任

标准 `read` 是工作区路径的基础读取入口，并保持既有图像处理能力。图片读取结果以 rich content 进入 Agent 的后续 provider 回合，正是 `/paste-image` 已建立的路径优先模型。任何需要支持更多格式的解析能力应以普通工具或 Skill 提供；它们接受 manifest 给出的路径，输出各自的 canonical ToolResult。Agent Core 对所有工具产生的 rich image content 使用同一个 capability gate；工具可以做前置提示，但不能各自成为最终能力判断的事实源。

删除入站文件专用 `read_attachment` 注册、prompt guideline、provider conversion 和 adapter 兼容调用。它会产生两种路径、两种权限提示和两种文件解释语义，与“一个 Agent Runtime”不兼容。资源记录用于回放和生命周期，不成为给 Agent 读取文件的私有 API。

### 8.3 Artifact 和 delivery 责任

`publish_artifact` 是唯一从普通工作树文件变为可交付文件的 Runtime 操作。它在工作目录之外的 Runtime 私有 artifact store 生成不可变快照、计算元数据、建立 canonical artifact record，并产出 artifact event。任何下载或 delivery projection 都按 artifact ID 打开私有内容并重新验证 regular-file、大小与 SHA-256。Runtime 只向平台 adapter 发出“发送这个 artifact ID”的操作；飞书上传/发送、微信官方 iLink 媒体上传/发送、WebUI 下载/展示和 ACP 投影都不复制 artifact 存储或投递状态。

## 9. 用户体验与错误语义

- 用户把图片或文件拖入、粘贴或发送后，入口立即展示 Runtime 返回的名称、大小和 `.mothx/tmp/...` 路径；这表示文件已经进入当前项目，而不表示模型已经看过它。
- Agent 回答文件内容前必须实际调用 `read` 或其他解析工具。若不需要读取，直接完成没有错误。
- 图像模型能力不足只在 Agent 请求图像视觉结果时提示，错误说明可选的下一步（更换模型、安装/调用解析 Skill 或由用户提供文字），不会删除资源或终止不相关文本任务。
- 网络下载、解密、读取、写入和落盘失败以入口可呈现的规范错误结束本次准备；不把半个文件、平台 URL 或密钥写进记录或对话。
- 不对受信任的自部署用户增加默认审核、验证码、强制身份绑定、自动扫描或短期过期。路径清理、原子写入和选配资源上限仅保证正确性和可用性。

## 10. 必须一并完成的代码收敛

以下是本方案完成时必须同时成立的代码边界，不存在“某个入口暂时直传图片”的兼容终点：

1. `internal/agentruntime` 提供资源准备、提交、manifest、回放、cleanup 和私有 artifact snapshot 的唯一实现；`internal/session` 提供对应的 resource/artifact 归属记录与迁移。
2. `SessionRuntime.BuildUserMessage` 只生成资源路径 manifest；删除入站图片 direct content、模型 image-capability ingress validation、`read_attachment` 注入和私有附件存储路径提示。
3. TUI `/paste-image` 改为 Runtime-backed staged resource：剪贴板代码仅取得流，Runtime 返回显示路径和 opaque resource ID；TUI 不再写入或按年龄删除 `.mothx/tmp` 文件。
4. CLI、WebUI/API、ACP、微信、飞书的所有附件分支均转换为 `InputIngress`，并在同一 `SessionRuntime` execution entry 提交；现有 legacy request image/message builder 仅可作为有明确删除条件的命名迁移桥，新的调用者不得使用。
5. 标准 `read` 明确支持 materialized image path 的 rich ToolResult；Skill/tool registry 的路径解析与当前 Runtime `workDir` 对齐。文档/压缩/专用格式不在输入 Runtime 中自动解析。
6. `publish_artifact`、artifact record 和 delivery projector 使用同一个工作目录之外的 Runtime 私有快照；当前实现已将生成物写入 `artifacts/<artifact-id>/content`，每次读取都验证 regular-file、大小和 SHA-256。channel adapter 仅执行平台上传/发送及 Runtime policy 明确要求的降级投影。
7. 所有日志、session entry、SSE/WebSocket/ACP/channel event 只保留 canonical IDs、路径元数据和非秘密诊断；不记录二进制、data URL、平台凭证或下载 URL。
8. Agent Core 在所有工具结果合并到 provider request 前使用 resolved provider/model capability 做统一 rich-image gate；删除 provider adapter 静默丢图和工具级分叉判断。
9. resource item 与 submission/run 分别使用稳定幂等键；同一 admission 事务原子写入 user entry、resource/run 绑定、execution intent、Run 和 start event，并发重复请求返回既有 Run ID。
10. 删除或替换与本方案冲突的测试、说明和旧兼容桥，确保没有入口保留“图片首轮直发、文件走私有 `read_attachment`”的行为。

当前进度：`internal/agentruntime.InputMaterializer`、`input_resources` migration、TUI/CLI/WebUI/API/ACP/Channel 的输入入口已统一到项目 `.mothx/tmp/inputs` 和文本 manifest；resource item key 已支持并发物化去重。`publish_artifact` 已使用 Runtime 私有 `artifacts/<artifact-id>/content` 快照，并在打开时校验大小与 SHA-256。已实现 canonical user message、InputResource 绑定、intent、Run、conversation turn、started event 和 schema 33 `runtime_submissions` reservation 的同事务 admission，并覆盖成功顺序与失败回滚。用户条目采用 `run-user-<runID>` 确定性 ID；Agent Core 复用 Runtime 已写入的条目，不再二次落库；linked retry 复用原请求消息。

设备迁移 checkpoint（2026-08-27）：schema 34 `delivery_intents`/`delivery_operations`、session plan store、确定性 Runtime planner、`ExecutionRuntime.SetDeliveryPlan` 和终态 store 映射已写入且 compile-only 通过，但没有 schema 34 focused behavior test，也没有生产 caller 调用 `SetDeliveryPlan`。Channel 仍走旧 `ProjectDeliveries`/`attachment_deliveries`；assistant entry 与 terminal/delivery 仍非同事务，claim/fencing/recovery/transport context 也未实现。因此 delivery 只能标记为“基础代码进行中”。换机后的权威续接清单见 [Channel 方案 10.1](./channel-media-attachments-proposal.md#101-设备迁移交接-checkpoint2026-08-27)。

补充进度：Agent Core 已统一拦截不支持视觉模型的 rich image ToolResult，避免静默丢图；该门禁覆盖新工具执行和恢复重放路径。

请求级图片 admission 基础版已接入：provider 调用前对最终图片编码体积/数量执行已知供应商硬上限检查，并按已解析 vendor 覆盖 OpenAI-compatible 网关；各 provider 精确 wire-format 预算仍待补齐。

Runtime 图片物化已补充无扩展名 WebP 的内容识别和规范后缀生成，标准 `read` 可直接按项目相对路径读取。

WebUI `/runs` 已将客户端 `Idempotency-Key` 映射为不含凭据的 Runtime 输入事件 ID，用于 resource item 去重，并将其 hash/scope/request fingerprint 写入 Runtime submission reservation；canonical user entry、InputResource、intent、Run、turn、started event 和 submission row 已具备跨表原子性。

Runtime `FindIdempotentRun` 现在优先查询 `runtime_submissions`，并要求 WebUI/Responses background 在取得 session/runtime admission 锁后再次检查；schema 33 之前的 started-event 扫描被明确标为待删除历史桥。数据库唯一约束和 admission transaction 已同时覆盖 submission reservation 与 canonical user entry。

Run 查询/回放会从 `input_resources.run_id` 重新装载 `InputResourceIDs`，因此重启后的 Run、恢复协调器和审计读取不会丢失资源归属。

## 11. 验收合同与测试矩阵

测试必须证明各入口的差别仅是协议解码和输出投影，而不是输入/资源/Agent 语义。除 focused package tests 外，变更必须运行 `go test ./internal/architecture`。

| 合同 | TUI | CLI | WebUI/API | ACP | 微信 | 飞书 |
|---|---:|---:|---:|---:|---:|---:|
| 输入流仅由 adapter 提供，文件由 Runtime 写入 `.mothx/tmp` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 同一资源模型、session/run 绑定、哈希与相对路径 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 同一 resource item key 去重；同一 submission key 只启动一个 Run | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 首轮 provider 用户消息只有文本 manifest，没有 image/data URL/base64 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Agent 调用 `read` 图片后才出现 rich image ToolResult | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Agent 可对文档选择 Skill/工具，Runtime 未自动解析 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 无视觉模型时接收成功；任意工具 rich image 在 Agent Core 得到能力错误 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 重放保留 resource ID/path；文件缺失得到确定状态 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 不存在 adapter-owned upload/cache/cleanup 或 `read_attachment` 旁路 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

还必须覆盖以下断言：

- 同一 fixture 文件从 TUI、CLI、WebUI/API、ACP、微信、飞书进入后，Runtime record 的路径形式、hash、resource/run ownership 和 canonical events 一致；允许 `Origin` 和 transport event envelope 不同。
- 同一 transport event 的同一 item 并发重放只建立一个 resource；同一 submission 并发进入只建立一个 canonical user entry、execution intent、Run/start event，并全部返回相同 Run ID。
- `/paste-image` 插入的路径正是 Runtime materializer 返回的 `RelativePath`，其发送后与 WebUI 上传 PNG 的首轮 Agent message 具有相同 manifest 结构。
- `read` 的图片测试验证完整 `imageproc` 解码只在工具执行后运行；在 Agent 未调用 `read` 的 case 中 provider request 不包含 image block。浏览器截图、图片生成和未来图片 ToolResult 与 `read` 通过相同 Agent Core capability gate。
- 清理测试验证 Runtime 只删除无引用或用户明确删除的 `.mothx/tmp` resource；adapter 不能删除其他入口创建的资源。
- Agent Core rich image capability gate 已有 focused coverage；browser screenshot、图片生成和未来图片 ToolResult 复用同一 gate，仍需补充各工具端到端 fixture。
- artifact 测试验证只有 `publish_artifact` 复制到 Runtime 私有 store 且 hash 验证通过的快照可被飞书/微信原生 delivery 或 WebUI 下载投影；工作树源文件后续变化不能改变已发布字节，私有内容被篡改时必须产生 canonical integrity failure。微信未支持类型的降级同样只引用该 artifact；助手文本中的路径不能触发发送。
- 架构守卫禁止 TUI、CLI、WebUI/API、ACP、微信和飞书直接写 Runtime 管理目录、构造入站 `provider.ImageContent`、注册 `read_attachment` 或建立第二份 resource persistence。

## 12. 与既有提案的关系

本方案是用户输入文件物化与 Agent 读取语义的唯一权威设计；[微信/飞书通道媒体协议与 Artifact 投递方案](./channel-media-attachments-proposal.md) 是平台协议、受权下载/解密和 artifact delivery 的权威设计。两者按所有权边界组合：Channel 提供 transport stream 或执行平台投递，Runtime 独占资源、Run、artifact 与 delivery 生命周期，不保留旧附件模型作为兼容终点。

`multimodal-image-optimization.md` 继续约束 Agent 已选择图片读取后的 `read`/`imageproc` 行为；它不再授权任何用户入站入口绕过工作区路径，将图片直接放入首轮 provider message。

最终产品语义可以用一句话概括：**用户把文件交给项目；Agent 决定何时、如何读取它；Runtime 负责让所有入口交付的是同一个项目文件事实。**
