# 微信/飞书通道媒体附件完整支持方案（输入语义已替代）

> 状态：输入资源设计已由 [Runtime 工作区输入文件物化方案](./runtime-workspace-input-materialization-proposal.md) 替代；平台协议和出站 delivery 资料仍可参考本文
> 日期：2026-08-26
> 范围：`internal/agentruntime`、`internal/session`、provider 公共附件事件，以及 TUI、CLI、WebUI/API、ACP、微信/飞书 channel 的薄适配器

> 重要：本文原先提出的“入站图片在首轮转换为 `provider.ImageContent`”“普通文件以私有 storage + `read_attachment` 暴露”“短 TTL 和 ingress image-capability 拒绝”不再是有效设计。所有用户输入文件现在以项目 `.mothx/tmp` 路径和 Runtime manifest 交给 Agent，读取由 Agent 的 `read`/Skill/工具自主选择。本文的 iLink/飞书协议解析、受权下载和 artifact 平台投递内容必须实现为该新 Runtime 合同的薄 adapter。

## 1. 结论、目标与边界

本方案让 MothX 的 channel 能够安全地处理用户发来的图片和文件，并在平台允许时将 Agent 明确产出的图片/文件作为媒体消息回传。

平台能力与产品行为必须分开表达：飞书开放平台支持图片和文件的上传、发送、接收和下载；微信 iLink Bot 的媒体能力用于入站附件处理，但 Bot 侧不具备可依赖的图片/文件出站发送链路。因此，“完整支持”在微信上的定义是**完整入站处理 + 明确、可访问的文本降级交付**，而不是伪造一个不存在的媒体发送能力。

| 能力 | 飞书 | 微信 iLink Bot |
|---|---|---|
| 用户发送图片 | 下载、校验、以图片输入交给 Agent | 下载、校验、以图片输入交给 Agent |
| 用户发送文件 | 下载、校验、作为受控附件交给 Agent | 下载、校验、作为受控附件交给 Agent |
| Agent 发送图片 | 上传后发送 `image` 消息 | 不可用；发送摘要和已授权的下载链接（若可用） |
| Agent 发送文件 | 上传后发送 `file` 消息 | 不可用；发送摘要和已授权的下载链接（若可用） |
| 文本 | 保持现有行为 | 保持现有行为 |

目标：

1. 同一份图片/文件在 TUI、CLI、WebUI/API、ACP、微信和飞书中采用同一份 canonical input、session 附件事实记录和生命周期语义。
2. 图片能以 `provider.ImageContent` 进入支持视觉输入的模型；没有视觉能力时给出稳定的用户提示，绝不静默丢弃。
3. 非图片文件能作为受限的、可追溯的本地附件供 Agent 读取；不把未检查的二进制内容直接拼进 prompt。
4. 飞书可发送由 Agent 或 provider 明确产出的图片和文件；微信对同一结果采用安全的文本降级，而不是 adapter 私有的第二套结果模型。
5. 所有下载、临时文件、文件名、MIME 和解析均经过统一的可靠性边界。附件仍遵守既有 sandbox 与工作目录规则。

非目标：

- 不支持语音、视频、表情、富文本卡片或任意聊天平台媒体类型；这些需要独立设计。
- 不把平台原始媒体 URL、下载 token、飞书 app secret、微信 token 或附件二进制写入 transcript / run event / 普通日志。
- 不把微信降级链接默认暴露到公网；没有明确配置的安全交付端点时，只反馈“产物已生成但该平台不支持发送附件”。
- 不为 channel 新建独立 Agent、Session、Run、Decision 或附件持久化实现。所有会话和 durable run 继续使用 `internal/agentruntime` 与 `internal/session`。

## 2. 官方协议基线与版本锁定

实现必须将平台协议的 JSON fixture、SDK 版本、权限清单和人工验证记录锁定到测试资料中。飞书以官方 SDK/文档为依据；微信 iLink 没有可用的官方 Bot 媒体协议文档，必须以多个可审计的开源对接实现交叉验证，并将观察到的兼容性契约固定为本仓库 fixture 与测试。实现不得把该契约表述为腾讯官方保证。

### 2.1 飞书

飞书使用仓库已依赖的官方 Go SDK `github.com/larksuite/oapi-sdk-go/v3`。实现使用其 IM v1 能力：

- 入站 `im.message.receive_v1`：按 `message_type` 识别 `image` 与 `file`，从内容取 `image_key` / `file_key` 等 provider 引用；
- 入站下载：调用官方受权的图片/文件下载接口，权限不足时返回可理解的错误而不是退回到任意 URL 请求；
- 出站上传：`Im.Image.Create` 或 `Im.File.Create` 取得 key；
- 出站消息：`Im.Message.Create` 或回复接口以 `msg_type: image` / `file` 和相应 key 创建消息。

飞书 app 必须在发布前申请并验证消息接收、图片/文件上传和下载所需 scopes。测试 tenant 与生产 tenant 都必须做 capability preflight；配置存在不代表权限已获批。

### 2.2 微信 iLink Bot

微信 iLink 的 `item_list` 包含 text/image/file 等 item 类型。媒体下载涉及协议给出的 CDN 媒体引用及 AES 加密元数据；现有 `internal/messaging/wechat/crypto.go` 可复用 AES 编解码辅助函数，但本身不代表媒体已经接通。

没有可引用的官方 iLink 媒体协议文档时，兼容性证据来自至少两个独立维护的开源客户端及其测试/示例，例如 [`corespeed-io/wechatbot`](https://github.com/corespeed-io/wechatbot) 与 [`photon-hq/wechat-ilink-client`](https://github.com/photon-hq/wechat-ilink-client)。二者一致显示：

- 图片使用 `item_list[].image_item.media`，并可用 `image_item.aeskey`（十六进制）覆盖 `media.aes_key`；
- 文件使用 `item_list[].file_item.media` 与 `file_name` / `len`；
- CDN 以 `encrypt_query_param` 组成下载请求，媒体使用 AES-128-ECB + PKCS#7 解密，`aes_key` 可能是原始 16 字节的 base64，也可能是十六进制文本再 base64；
- 部分实现观察到服务端给出 `full_url`，它仍只可在 platform adapter 内作为事件授权下载地址使用，绝不写入 session、prompt、日志或外部响应。

这些字段是兼容性实现而非稳定承诺：每次升级 iLink adapter 时，维护者必须复核两个来源的锁定 revision、更新本地 JSON fixture 并用真实自有 Bot 做入站图片和文件回归验证。遇到未知或互相矛盾的字段时，拒绝该附件并保留不含秘密的诊断，而不是根据用户文本或未认证 URL 猜测下载方式。

本方案锁定以下契约：

- `getupdates` 中出现的图片/文件 item 解析为 platform reference，不依赖猜测的公开 CDN URL；
- 仅使用经过上述开源实现交叉验证、并已固化为本地 fixture 的下载方法、字段和密钥格式；
- `sendmessage` 的受支持 Bot 输出保持文本。实现不得构造未经官方支持的 image/file payload，更不能把本地路径或 data URL 伪装成媒体 item；
- 微信 transport capability 必须来自锁定的协议契约；当前固定为 `SendImage: false, SendFile: false`，不得由 adapter 猜测开启。

任何一个平台的 fixture 解析失败时，保留未知 item 的受限元数据并记录不含秘密的诊断；不得自动下载未知 URL。

## 3. 单一附件模型与所有权

附件不是平台 adapter 的临时字节切片，也不是 provider-specific URL。它是 session 的受控资源，并有明确的来源、存储、访问和投递状态。

### 3.1 领域对象

在 `internal/agentruntime` 增加前端无关的 `AttachmentService`。类型所有权必须唯一且没有“可放在任一包”的模糊边界：

- `internal/messaging.PlatformAttachment` 只表示微信/飞书 transport 的原始媒体引用；它不是 Agent 输入或持久化模型。
- `agentruntime.RunInput`、`agentruntime.InputAttachment`、`agentruntime.AttachmentIngress` 是 TUI、CLI、WebUI/API、ACP 和 channel 共用的 canonical 输入合同。
- `agentruntime.SessionAttachment` 是 runtime 领域对象；其 store 由 `internal/session` 实现并由 `SessionRuntime` 持有。
- `provider.ImageContent` 只在 Agent Core 准备 provider request 时从 canonical attachment 转换，不允许 adapter 直接构造。

```go
type AttachmentKind string

const (
	AttachmentImage AttachmentKind = "image"
	AttachmentFile  AttachmentKind = "file"
)

// PlatformAttachment 是 transport 给共享 runtime 的不可变引用，绝不含密钥或下载 URL。
type PlatformAttachment struct {
	Platform    string
	Reference   string
	Kind        AttachmentKind
	NameHint    string
	MediaType   string
	SizeHint    int64
	MessageID   string
}

// RunInput 是所有前端进入 SessionRuntime 的唯一用户输入。
type RunInput struct {
	Text        string
	Attachments []InputAttachment
}

// InputAttachment 只引用已经由 AttachmentService 接收的规范附件。
type InputAttachment struct {
	AttachmentID string
	Kind         AttachmentKind
	Filename     string
	MediaType    string
	Bytes        int64
}

// SessionAttachment 是附件服务持久化后的规范记录。
type SessionAttachment struct {
	ID          string
	SessionID   string
	RunID       string
	Origin      string // channel:feishu, channel:wechat, provider:openai, tool
	Kind        AttachmentKind
	Filename    string
	MediaType   string
	Bytes       int64
	SHA256      string
	StorageKey  string // 私有对象/文件存储键，不是 URL
	Status      string // received, accepted, rejected, expired, generated
	CreatedAt   time.Time
}
```

`InboundMessage` 扩展为 `Attachments []PlatformAttachment`。`Text` 继续只代表用户文本；同一消息可以没有文本而只有附件。平台 adapter 只负责认证事件、抽取最小引用并提供受权读取器。Dispatcher 将 transport envelope 映射为 `AttachmentIngress`，交给 `SessionRuntime.AttachmentService` 接收和持久化，再与文本组成 `RunInput`。adapter 不能自行写 session、下载到工作目录、构造 `provider.ImageContent` 或调用 provider。

平台下载能力通过统一接口显式化：

```go
type AttachmentFetcher interface {
	FetchAttachment(ctx context.Context, ref PlatformAttachment) (AttachmentStream, error)
}

type AttachmentStream struct {
	Reader      io.ReadCloser
	Filename    string
	MediaType   string
	ContentSize int64
}
```

`AttachmentFetcher` 只能接受自己平台的 opaque reference。Dispatcher 根据 `InboundMessage.Platform` 查找 fetcher；不会把用户文本中的任意 URL 当作平台附件下载。

### 3.2 统一输入与薄适配器合同

所有入口最终调用同一个 `SessionRuntime.Run(ctx, RunInput)`，并共享同一个附件接收、session entry、Agent construction、tool registry、provider request、Run lifecycle 和 replay 路径。入口只负责协议转换和交互呈现：

| 入口 | 允许的职责 | 禁止的职责 |
|---|---|---|
| TUI / CLI | 将命令行或交互选择的本地附件提交为 `AttachmentIngress`；渲染 canonical event/artifact | 自建附件目录、直接拼 provider content、扫描输出路径 |
| WebUI / API | 接收 HTTP/SSE 输入并映射为 `RunInput`；展示 canonical attachment/artifact | 自建上传持久化、独立 Run 或附件状态机 |
| ACP | 将协议 content part 映射为 `RunInput`；将 canonical event 投影为 JSON-RPC | 构造 provider request、维护第二套 artifact 记录 |
| 微信 / 飞书 | 解析平台媒体引用、提供 fetch/upload/send transport；投影消息 | 写 session、决定附件生命周期、创建 Agent 或恢复 Run |

前端是否提供附件选择控件不改变其架构合同：它始终调用同一 `RunInput` 入口并消费同一事件模型，不能保留或新增只接受字符串的平行执行路径。

### 3.3 存储、持久化与清理

附件服务将流式内容保存到受 session ownership 保护的私有附件存储，例如：

```text
<sessionDir>/attachments/<session-id>/<attachment-id>/content
```

该目录不属于 Agent 默认工作目录。文件名只作为展示元数据；磁盘路径始终由随机 attachment ID 组成。`StorageKey` 只表达 runtime-owned 存储位置，不向 adapter 或用户暴露真实路径。

在 `internal/session/migrations.go` 追加 migration，建立（名称可按现有风格调整）：

```text
session_attachments(
  id, session_id, run_id, origin, kind, filename, media_type,
  byte_size, sha256, storage_key, status, created_at, expires_at, metadata
)
attachment_deliveries(
  id, attachment_id, run_id, platform, target_id, status,
  provider_message_id, failure_code, created_at, updated_at
)
```

`attachment_deliveries` 是 canonical durable delivery record；不要把“已发送”仅保留在飞书 adapter 的内存 map。它与 `ExecutionRuntime` 的 Run event 关联，确保重启后能够判断应重试、已投递、不可重试降级或终态失败。

保留期默认短于 session：未被用户显式导出的入站附件在可配置的 TTL 后删除私有内容并将记录转为 `expired`，保留必要的大小、哈希、来源和审计状态。清理由 runtime 单点任务完成，不能由每个平台自行删除文件。

## 4. 本地部署的可靠性边界

MothX channel 的典型部署是单个用户或其可信团队在本地/内网运行，不是多租户文件托管服务。本方案不引入默认的恶意文件扫描、人工审核、按日配额、复杂的身份绑定下载令牌或公网对象存储；这些都会让“发一张截图、传一个文档”变得笨重，且超出本功能的必要范围。

`AttachmentService.AcceptInbound` 只保留保证正确性和避免意外资源耗尽的最小步骤：

1. 以平台给出的 reference 下载附件，并以 `platform + message_id + reference` 去重。
2. 流式保存，设置合理且可配置的单文件上限；达到上限时告诉用户文件过大，不下载半个文件继续运行。
3. 根据内容识别图片 MIME，用于正确选择视觉输入和预览；文件扩展名仅用于展示，不参与路径构造。
4. 计算 SHA-256，保存到 session 附件记录，用于去重、诊断与重启恢复；它不是安全审计或用户权限系统。
5. 原样保存普通文件；压缩包不自动展开。图片解码限制像素总数，避免一个损坏图片让服务耗尽内存。

默认值只提供单文件上限：图片 20 MiB、普通文件 50 MiB。用户可按自己的模型、硬件和网络在 `serve.json` 中调整或关闭限制；不设置每 session/每天配额。

附件保持在 session 私有目录，主要是为了不污染项目工作区和避免文件名冲突，而不是为了建立多租户隔离。入站文件仍是用户内容，Agent 读取时通过附件 manifest 明确其来源；这有助于模型区分用户说明与文件内容，不改变单用户自部署的信任模型。

微信的文本降级默认说明产物已生成，并提示用户从同一 MothX WebUI session 下载。显式配置 `publicDelivery` 后，runtime 为 artifact 生成带过期时间的 opaque 下载 token，并发送由 `baseURL` 组成的链接。下载 handler 只按 token 查询 attachment ID，不接受本地路径参数；token 在 TTL 内允许重复下载，不增加一次性次数或聊天身份绑定。

## 5. 统一执行路径

TUI、CLI、WebUI/API、ACP 和 channel 的执行全部通过 `SessionRuntime`、`BuildAgent`、`ExecutionRuntime.BeginDurable/FinishDurable` 与 `DecisionService`。附件只是在 canonical `RunInput` 上增加内容，不得创建任何入口专用 run。

```text
前端输入 / 平台事件
  -> 薄 adapter：协议内容 -> AttachmentIngress + 文本
  -> SessionRuntime.AttachmentService：接收并持久化附件
  -> SessionRuntime.Run(RunInput)：建立 canonical user entry
  -> BuildAgent / ExecutionRuntime：以同一 run 执行
  -> Agent Core：文本、图片内容块、受控附件工作区
  -> ArtifactService：登记明确的生成物
  -> DeliveryProjector：按平台能力投递或降级
```

### 5.1 进入 Agent 的方式

删除 Dispatcher 只接受 `string` 的私有 `runAgent` 执行入口，由 shared runtime 提供唯一的 `Run(ctx, RunInput)`。TUI、CLI、WebUI/API、ACP 和 channel 全部调用该入口；禁止任何 adapter 复制 `agent.Config`、直接运行 agent loop 或手工发送 provider request。

- 文本保持文本 block。
- 已接受图片经尺寸与像素策略处理后转换为 `provider.ImageContent`。模型不支持图片时，run 在调用模型前以稳定错误完成，并保留附件记录；不降级为“假装已理解图片”。
- 普通文件通过受控附件目录暴露给 Agent：用户消息包含一份不含内容的 manifest（名称、MIME、大小、附件 ID 和只读逻辑路径），注册表提供 `read_attachment`。它只能读取本 run 已授权的 attachment ID，并按文件类型返回内容。
- `read_attachment` 完整支持 UTF-8/常见文本编码、JSON/YAML/CSV、PDF 文本、DOCX、XLSX 和 PPTX 的结构化提取；图片仍走视觉内容块。压缩包返回目录清单，并在 Agent 明确请求具体成员时按路径和大小限制读取。无法提取的二进制文件返回元数据和只读附件路径，Agent 可交给现有 sandbox 工具处理。所有提取器由同一个 `AttachmentService` 注册，保留原附件 ID、页码/工作表/幻灯片等来源信息。

持久化的用户 entry 写入附件 ID 和受限摘要，不写 base64 或本地存储路径。session replay 通过 attachment ID 重建权限；内容过期后应以明确的“附件已过期，需重新上传”状态重放，不能产生虚构输入。

### 5.2 Agent 生成物

“文件看起来像路径”不是可投递 artifact。工具、provider hosted item 或生成图片必须明确报出 artifact，`ArtifactService` 才可登记为 `SessionAttachment{Origin: tool/provider, Status: generated}`。

新增内部 `ArtifactSink`，由 `SessionRuntime` 在构建 agent 时以 `publish_artifact` 工具注入注册表。Agent/内置工具必须显式调用它登记受 sandbox 允许的完成文件；provider 附件先由受权 resolver 下载、验证后登记。注册行为必须：

- 限制来源到工作目录或 runtime-managed 临时目录，拒绝任意绝对路径和符号链接逃逸；
- 复制/原子移动至私有附件存储后才允许投递，避免 Agent 后续改写同一文件；
- 以 ID 而不是原路径写入 event 和 delivery；
- 对每个 artifact 保存 MIME、大小、哈希和生成 run ID。

这取代当前 channels 将 `provider.Attachment` 仅格式化为文本 URL/ref 的做法。现有 `provider.Attachment` 仍是 provider 事件元数据，但在真正发送前必须通过 `ArtifactService` 成为可验证的 session artifact。

## 6. 投递投影与平台实现

`DeliveryProjector` 是 runtime 所有的唯一决策点：它接收 canonical assistant text、artifact IDs 和已解析 `ExecutionPolicy`，根据 transport capability 得到一组投递操作。平台 adapter 只执行操作，不决定“是否可投递”、链接策略或 retry 状态。

Agent Core 和 Runtime 只产生一套 canonical text、attachment、artifact、tool、usage、decision 与 terminal event。Bubble Tea、CLI 文本/NDJSON、SSE/WebSocket、ACP JSON-RPC、微信和飞书消息都是这些事件的无状态或可重放投影；任何 adapter 都不得扫描工具输出、解析路径来补造 artifact、修改 canonical ID/哈希/Run 归属，或产生第二套完成/失败事件。

```go
type DeliveryCapability struct {
	Text          bool
	SendImage     bool
	SendFile      bool
	PublicLink    bool
	MaxImageBytes int64
	MaxFileBytes  int64
}

type DeliveryOperation struct {
	Kind         string // text, image, file, link
	AttachmentID string
	Text         string
}
```

### 6.1 飞书投递

1. 文本使用当前回复/创建消息语义；进度仍保持文本。
2. 每个合格图片 artifact 调用图片上传接口，取得 `image_key`，再发 `image` 消息。文件同理，取得 `file_key` 后发 `file` 消息。
3. 每次上传与消息发送都要记录 delivery state；网络超时后依据 provider 返回的 message/upload 标识执行幂等确认，不盲目重复上传或发送。
4. 入站回复优先沿用原始 message ID；后台完成或 webhook delivery 使用 chat ID。两者是 adapter 投影差异，不能改变 canonical run。
5. 平台文件类型、大小或权限拒绝时，记录 `delivery_failed`，并给用户补发简短的文本解释；不得把私有本地路径发给用户。

### 6.2 微信投递

微信 capability 固定为 `Text: true, SendImage: false, SendFile: false`。对 artifact 的策略：

1. 若显式启用了 `channelMedia.publicDelivery` 且能为 artifact 生成下载链接，则发送文本摘要和链接。
2. 否则发送文本：说明文件/图片已生成、微信 iLink 协议不支持媒体出站，并给出可用方式（例如在 WebUI 同一 session 下载，或在支持媒体投递的飞书会话请求）。
3. 绝不将未经受权的 `StorageKey`、绝对路径或 `file://` URL 作为降级链接。

这样微信限制被建模为 policy/capability，而不是在 dispatcher 内临时 `if platform == "wechat"` 拼文案。

### 6.3 背景任务、重启与去重

在 `ExecutionRuntime.FinishDurable` 写入 assistant entry 后，runtime 生成待投递记录；`RunManager` 仅继续负责内存事件扇出。服务重启时，现有 channel background recovery 从 `attachment_deliveries` 和 canonical run event 恢复未终态投递：

- 已有 provider message ID 的记录不重发；
- 上传成功但消息未确认时先查询或以 provider idempotency contract 恢复；
- 微信链接操作若已生成则重放同一未过期 token；过期后生成新 token，并使 delivery record 指向当前 token；
- 永久失败转为终态并投影一条文本诊断一次，避免每次重启反复骚扰用户。

## 7. 配置与用户可见行为

在 `serve.json` 的 channels 下新增版本兼容的 `media` 子项；通过现有 Config State 原子保存和 Runtime Policy resolver 读取。不要修改 `settings.json` 的语义，也不要让 adapter 自行读取未解析 JSON。

```json
{
  "channels": {
    "media": {
      "enabled": true,
      "maxImageBytes": 20971520,
      "maxFileBytes": 52428800,
      "retentionHours": 168,
      "publicDelivery": {
        "enabled": false,
        "baseURL": "",
        "ttlHours": 24
      }
    }
  }
}
```

`enabled: false` 保持当前只收发文本的行为：收到纯媒体时给出明确短提示（而不是沉默），但不下载附件。媒体功能不依赖外部扫描服务。

Serve/WebUI 的 channel 状态 API 返回已解析的 capabilities、限制和不可用原因，不返回下载地址、token、存储键或平台密钥。WebUI 在同一 session transcript 展示附件元数据、预览/下载状态和 delivery status；授权判断仍在后端。

## 8. 完整实施清单

以下项目共同构成完整交付；缺少任意一项都不能宣称 channel 已完整支持图片和文件。

### 8.1 协议与 transport

- 锁定飞书 SDK 版本、所需 scope、image/file 入站、下载、上传和出站 fixture。
- 锁定微信 iLink image/file 入站 fixture、CDN 解密下载 fixture、过期/权限错误 fixture，以及 text-only 出站契约。
- 扩展 `InboundMessage`，为微信、飞书实现 `AttachmentFetcher` 和统一 transport capability。
- 实现飞书图片/文件上传、发送、回复、后台投递和 provider 错误归一化。

### 8.2 Runtime、Session 与 Agent

- 在 `internal/session/migrations.go` 追加附件与 delivery migration。
- 实现 `AttachmentService`、`ArtifactService`、私有存储、TTL 清理、下载 token 和下载 endpoint。
- 在 shared runtime 支持多模态 user content、附件 manifest、`read_attachment`、文档结构化提取和 archive 成员读取。
- 将 TUI、CLI、WebUI/API、ACP、channel 的用户输入统一到 `agentruntime.RunInput` 和同一个 `SessionRuntime.Run`，删除入口私有的字符串执行旁路，保持唯一 Agent construction、Run、Decision、replay 和 shutdown 路径。
- 将工具产物、provider hosted attachment、生成图片统一登记为 artifact；将当前纯文本 attachment summary 降为投递失败或仅有远端引用时的展示投影。

### 8.3 投递、恢复与配置

- 实现 `DeliveryProjector`、durable delivery record、上传/发送幂等、后台完成、重启恢复和终态错误投影。
- 实现微信 capability 驱动的文本降级、WebUI session 下载提示，以及启用 `publicDelivery` 时的 TTL 下载链接。
- 完整接入 `serve.json`、Config State、Runtime Policy resolver、channel 状态 API 和 WebUI transcript 展示。
- 同步更新中英文 channel 配置文档、功能文档和 changelog。

### 8.4 验证

- 覆盖 hash、MIME、大小、文件名、图片像素限制、幂等、TTL 清理、文档提取和 archive 读取的单元测试。
- 覆盖模型视觉能力拒绝、文件过期 replay、session binding、cancel、background recovery 和 shutdown 的集成测试。
- 增加跨入口 contract tests：相同 `RunInput` 经 TUI、CLI、WebUI/API、ACP、channel adapter 后必须得到一致的 session entry、attachment ID、artifact event、Run 归属与终态；差异只能存在于协议投影。
- 在真实飞书 tenant 验证图片/文件完整收发，在真实微信 iLink 环境验证图片/文件入站与产物降级交付。
- 运行受影响包测试、`go test ./internal/architecture`、跨入口 contract tests 和完整 `go test ./...`。

## 9. 测试与验收标准

必须新增 focused tests，并在触及 runtime construction、run persistence、source/mode 或 shutdown 时运行 `go test ./internal/architecture`。

| 场景 | 必须验证 |
|---|---|
| 飞书图片/文件入站 | 从 event 正确取得引用；下载权限、MIME、哈希、session entry 与 Agent input 正确 |
| 微信图片/文件入站 | item 解析、CDN 解密、过期 key、未知 item 和无文本消息均有确定结果 |
| 异常输入 | 超限、伪 MIME、损坏图片、下载失败不会让当前 run 卡死或写入错误附件 |
| 模型能力 | 图片模型正常收到 image block；文本模型在调用 provider 前稳定失败且附件不丢失 |
| 飞书出站 | 上传后正确发送 image/file；重复完成、超时和重启不重复投递 |
| 微信出站 | 永不尝试不受支持的媒体 API；有/无公开交付配置均产生正确文本 |
| 生命周期 | `/stop`、`/new`、session 重放、后台完成和 `SessionRuntime.Shutdown` 不泄露临时文件或留下 pending delivery |
| 本地文件边界 | 附件不会覆盖项目工作区文件；下载端点不会依据任意路径读取本地文件 |
| 跨入口一致性 | TUI、CLI、WebUI/API、ACP、channel 使用同一 `RunInput`、附件 store、Agent 构建、Run lifecycle 和 canonical event；adapter 只改变 wire/rendering |

完成定义：飞书可在真实 tenant 中收发图片及所有支持的普通文件；微信可在真实 iLink 环境中接收、保存并让 Agent 使用图片和文件，并对产物执行配置确定的 WebUI 下载提示或公开链接降级；文本、图片、文件、artifact 和 delivery 均能在 canonical session/run/attachment record 中回放和恢复。文档提取、后台任务、重启、重复事件、取消、过期清理和平台错误全部达到本节验收标准；任何一个平台不可用、权限不足或媒体处理失败时，文本通道仍保持可用。

## 10. 明确禁止的实现捷径

- 不在 `internal/messaging/wechat` 或 `internal/messaging/feishu` 直接创建 session DB、`agent.Config`、Agent 或 durable run。
- 不在 adapter 内把下载字节直接 base64 塞入 prompt，也不为文件内容临时添加无 sandbox 的本地路径。
- 不在 channel dispatcher 猜测工具输出路径或把任意 URL 作为可发送附件。
- 不新增 adapter-owned media 表、独立清理 goroutine 或第二套 delivery retry 状态机。
- 不因微信不支持出站媒体而跳过 artifact 校验或把本地文件路径泄露给用户。
- 不将“模型可以看图片”与“平台可以发送图片”混为一个 capability；两者由不同的 runtime policy 分别解析。
- 不在 TUI、CLI、WebUI/API、ACP 或 channel 中增加私有 `RunInput`、附件模型、上传存储、artifact 扫描、tool registry、provider content builder、delivery store 或 recovery loop。
