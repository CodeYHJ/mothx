# 微信/飞书通道媒体协议与 Artifact 投递方案

> 状态：当前权威目标方案；已移除被替代的输入附件设计与第三方微信协议基线
>
> 日期：2026-08-28
>
> 范围：微信/飞书媒体协议解析、受权下载、Runtime 输入流交接、已发布 artifact 的平台投递与恢复
>
> 输入资源权威方案：[Runtime 工作区输入文件物化方案](./runtime-workspace-input-materialization-proposal.md)
>
> 实施状态：微信/飞书入站已接入 Runtime 输入物化主链路，首轮 direct image 与 `read_attachment` 已从生产路径移除；artifact 已落到 Runtime 私有快照并做打开时 hash 校验。Runtime durable delivery intent/ordered operations、终态原子提交、Channel 正常 caller、claim/fence/retry coordinator 和服务启动后的主动 recovery 已接入；飞书图片/文件、微信 image/video/file 原生出站已接入，并复用冻结的 transport context 与稳定 provider ID。剩余是真实 Bot provider 语义验证和旧 attachment/delivery 迁移桥清理；本地 OS 子进程 provider-state fixture 已完成。
>
> 设备迁移 checkpoint（2026-08-28）：当前工作树尚未提交。完整 admission、schema 34/35 持久化、Channel 正常 delivery caller、终态 assistant/Run/turn/event/intent 原子提交、WeChat/Feishu staged delivery 和启动恢复协调器均已落地并有 focused behavior tests；换机后只需继续真实平台 provider 语义和旧 schema 30 bridge 收敛，本地 OS 子进程 fixture 已覆盖 provider-state 恢复。详细交接见 10.1。

## 1. 结论

Channel 媒体能力只增加两类薄适配行为：

1. 入站时解析平台媒体引用，并向 Runtime 提供一次性、已鉴权的读取流；Runtime 负责把文件物化到项目 `.mothx/tmp`、登记资源、绑定 Session/Run、生成 manifest、重放和清理。
2. 出站时接收 Runtime 已通过 `publish_artifact` 发布的不可变 artifact ID，按平台能力上传、发送或生成文本降级投影。

本方案不再定义第二套附件输入模型、私有附件读取工具或图片首轮直发语义。以下行为明确禁止：

- Channel adapter 直接构造 `provider.ImageContent`、provider message 或 Agent prompt；
- Channel adapter 将媒体写入项目目录、维护附件数据库、注册 `read_attachment` 或自行清理输入文件；
- 根据助手文本中的路径、adapter 输出目录或工作树扫描结果推断 artifact；
- 为 Channel 建立独立 Agent、Run、Decision、恢复循环或 delivery store；
- 因当前模型不支持图片而在 ingress 阶段拒绝文件。

图片和普通文件具有相同的入站语义：它们首先是 Runtime 管理的项目文件。只有 Agent 显式调用 `read` 读取图片后，图片才作为工具 rich content 进入 provider。

## 2. 所有权与边界

| 能力 | 权威所有者 | Channel adapter 允许做的事 |
|---|---|---|
| 平台事件解析、媒体下载鉴权/解密 | 微信/飞书 transport | 将平台事件转换为不透明引用和一次性 `Open` 流 |
| 输入物化、命名、哈希、资源记录、Run 绑定 | `internal/agentruntime` | 提交 `InputIngress`，消费 Runtime 返回的 resource ID 和相对路径 |
| Session、Run、Decision、执行与关闭 | `internal/agentruntime` | 调用统一执行和控制接口，投影 canonical 状态 |
| 输入资源和 artifact 持久化 | `internal/session` 经 Runtime store | 不直接写表，不创建 adapter 私有记录 |
| artifact 发布与不可变快照 | Runtime `publish_artifact` + Runtime 私有 artifact store | 仅发送 Runtime 授权的 artifact ID |
| 平台上传和消息发送 | 微信/飞书 transport | 执行 Runtime delivery operation，并回报 provider 结果 |
| delivery 状态、重试和恢复 | Runtime durable delivery outbox | 不维护内存权威状态或 adapter 私有恢复循环 |

Channel 绑定的 Session 继续使用持久化 `channel_type`、`channel_id` 和 Session header。微信/飞书强制 `yolo` 只影响有效 Agent mode，不绕过 sandbox、allow rules、通道安全规则或高风险命令保护。

## 3. 平台协议基线

实现必须将平台 JSON fixture、SDK/包版本、发布物完整性、权限清单和人工验证记录固定在测试资料中。未知或互相矛盾的媒体字段必须被拒绝，并只记录不含凭据和下载 URL 的诊断。

### 3.1 飞书

飞书使用仓库已依赖的官方 Go SDK `github.com/larksuite/oapi-sdk-go/v3`：

- 入站 `im.message.receive_v1`：根据 `message_type` 解析 `image`、`file`，从内容取得 `image_key`、`file_key` 和可用文件名；
- 入站读取：使用官方受权资源下载接口，不能把 key 拼成任意 URL；
- 出站上传：图片调用 `Im.Image.Create`，文件调用 `Im.File.Create`，取得 `image_key` 或 `file_key`；
- 出站消息：使用 `Im.Message.Create` 或回复接口发送 `image` / `file` 消息；
- 消息发送必须使用预先持久化的稳定 UUID，并在重试时复用同一 UUID。

发布前必须验证消息接收、资源下载、图片/文件上传和消息发送所需 scopes。配置存在不代表 tenant 已授予权限。

### 3.2 微信 iLink Bot

微信协议只以腾讯发布的 npm 包 [`@tencent-weixin/openclaw-weixin`](https://www.npmjs.com/package/@tencent-weixin/openclaw-weixin) 为调查和兼容性基线，不再引用第三方兼容仓库。本文锁定 `2.4.6`：npm 元数据的 author 为 Tencent，`latest` 指向 `2.4.6`，发布物 integrity 为 `sha512-qw9k3PLTiMWGNjjsknHgcTManH1w4j+Ji1ArWIaYLKCq3aFRsVwcqnPi127bvOoVMJGW4dbyJ8NECEMgoO+iRw==`。实现和测试应记录精确版本与 integrity，不能只跟随浮动的 `latest`。

该发布物的 `README.zh_CN.md`、`src/api/types.ts`、`src/api/api.ts`、`src/media/media-download.ts`、`src/cdn/*` 和 `src/messaging/*` 共同构成当前协议证据。MothX 用 Go 实现相同 transport 合同，不把 OpenClaw 插件引入为运行时依赖，也不照搬其 framework 级资源存储、临时路径或执行生命周期。

官方包定义的消息与入站媒体合同如下：

- 所有后端 API 均为 HTTP JSON `POST`，使用 `AuthorizationType: ilink_bot_token`、`Authorization: Bearer <token>` 和 `X-WECHAT-UIN`；token 与 UIN 由微信 transport 持有；
- `WeixinMessage.item_list` 的类型为 `1 TEXT`、`2 IMAGE`、`3 VOICE`、`4 FILE`、`5 VIDEO`，回复时回传事件中的 `context_token`；
- 图片读取 `image_item.media`，优先使用 `image_item.aeskey`，缺失时回退到 `media.aes_key`；语音、文件和视频分别读取各自 item 的 `media`；
- CDN 引用可携带 `full_url` 或 `encrypt_query_param`。优先使用服务端返回的 `full_url`；只有锁定版本和真实 Bot 验证允许时，才按官方包的 `/download?encrypted_query_param=...` 规则回退拼接；
- 媒体使用 AES-128-ECB 与 PKCS#7 padding。`media.aes_key` 需要兼容 `base64(raw 16 bytes)` 和 `base64(32 字节 ASCII hex)` 两种编码；图片的 `image_item.aeskey` 是 16 字节 key 的 hex 表示；
- 官方实现支持图片、语音、文件、视频入站，也会读取引用消息 `ref_msg.message_item` 中的媒体。MothX 必须处理同一 `item_list` 中的每个可支持资源，不继承官方参考实现“只取第一个媒体项”的宿主限制；
- `full_url`、`encrypt_query_param`、AES key 和 token 只存在于 transport 的受权读取闭包或 Runtime-owned delivery 恢复状态，不进入 prompt、普通日志或对外事件。

官方包 `2.4.6` 已实现图片、视频和文件原生出站，因此微信 capability 必须以这份官方能力矩阵为准：

1. 生成稳定 `filekey` 和 16 字节 AES key，计算明文大小、明文 MD5 与 PKCS#7 后的密文大小；
2. 调用 `ilink/bot/getuploadurl`，其中 `media_type` 为 `1 IMAGE`、`2 VIDEO` 或 `3 FILE`，并传入 `to_user_id`、大小、MD5、AES key 等字段；
3. 优先使用响应的 `upload_full_url`，否则按官方包规则用 `upload_param + filekey` 构造上传 URL；上传 AES-128-ECB 密文，并从响应头 `x-encrypted-param` 取得下载引用；
4. 调用 `ilink/bot/sendmessage`。每次请求的 `item_list` 只放一个 item，携带 `to_user_id`、稳定 `client_id`、`message_type=BOT`、`message_state=FINISH`、`context_token` 和 `run_id`；
5. 图片 item 使用 `type=2`、`image_item.media` 和密文大小；视频使用 `type=5`、`video_item.media` 和密文大小；文件使用 `type=4`、`file_item.media`、规范文件名和明文长度。

因此，锁定版本下 capability 至少为 `ReceiveImage/ReceiveVoice/ReceiveFile/ReceiveVideo: true`、`SendImage/SendFile/SendVideo: true`。`UploadMediaType` 虽包含 `VOICE=4`，但官方包没有对应的语音发送实现，所以不得据此宣称 `SendVoice: true`。

官方 README 与包内实现存在两处必须显式验收的差异：README 描述 CDN 使用 `PUT` 且图片/视频需要缩略图，`2.4.6` 源码实际使用 `POST` 并传 `no_need_thumb: true`。当前兼容目标以该精确发布物的可执行实现为起点，但合入前必须用真实自有 Bot 固化 HTTP method、缩略图策略、大小限制和错误码 fixture；验证结果应写入测试，不得靠第三方仓库投票决定。

## 4. 入站媒体交接

### 4.1 薄 adapter 合同

Channel adapter 将每个媒体项映射为 Runtime 的 `InputIngress`。字段名称可随最终 Go API 调整，职责不得变化：

```go
type InputIngress struct {
    Origin        string // channel:wechat 或 channel:feishu
    EventID       string // 平台事件/消息的稳定非秘密 ID
    ItemIndex     int    // 同一事件内媒体项的稳定序号
    Reference     string // 不透明平台引用，仅供本次 Open/受限诊断，禁止原文持久化
    FilenameHint  string
    MediaTypeHint string
    SizeHint      int64
    Open          func(context.Context) (io.ReadCloser, error)
}
```

`Open` 必须通过平台官方授权或已验证的 iLink 解密流程读取内容。它不得接受用户提供的任意 URL，也不得把 token、AES key、临时下载 URL 或原始凭据装入 `InputIngress` 的可持久化字段。

同一平台事件中的文本与资源 ID 通过统一 `InputSubmission` 提交给 `SessionRuntime`。纯媒体事件也必须产生正常的 Runtime 输入；不得依赖非空文本才启动 Run。

幂等必须分成两个边界：

1. resource item key 使用 `session_id + origin + event_id + item_index`，保证同一事件内每个媒体项只物化一次；
2. submission key 使用 `session_id + origin + event_id`，并在 user entry、resource 绑定、execution intent 和 Run/start event 的 admission 事务中唯一，保证并发重放只创建一个 Run；重复提交返回已经存在的 canonical Run ID。

`Reference`、`full_url`、`encrypt_query_param`、AES key 和飞书资源下载凭据不得直接进入上述键或数据库。平台缺少可直接持久化的稳定 event ID 时，adapter 必须提供不含秘密的稳定协议 ID；确实只能依赖敏感引用时，由 Runtime 使用安装级密钥计算 HMAC 作为 item key，原文仍只存在于一次性 `Open` 闭包。

### 4.2 Runtime 物化要求

所有入口共享 [Runtime 工作区输入文件物化方案](./runtime-workspace-input-materialization-proposal.md) 的同一路径：

```text
平台事件
  -> adapter 解析引用并提供受权 Open 流
  -> Runtime PrepareInput
  -> <workDir>/.mothx/tmp/inputs/<resource-id>/<canonical-name>
  -> InputResource + canonical event
  -> InputSubmission 原子绑定 Run
  -> 首轮用户消息只包含路径和元数据 manifest
  -> Agent 按需调用 read / Skill / MCP / 其他工具
```

Runtime 接收流时必须：

1. 使用上述 resource item key 和 submission key 两级唯一约束，分别裁决资源物化与 Run admission；resource 去重不得被误当成 user submission 去重；
2. 流式写入、计算 SHA-256、限制可配置的单文件大小，并使用同目录临时文件加原子 rename；
3. 根据内容而不是扩展名识别 MIME；名称提示只用于生成清理后的展示文件名；
4. 当图片名称缺少扩展名或扩展名与内容不符时，根据识别出的 MIME 生成规范后缀，保证标准 `read` 能进入图片处理路径；
5. 图片 ingress 只允许有界 MIME sniff、`DecodeConfig`/头部检查和像素上限验证，不执行完整 decode、首帧提取、resize 或 transcode；普通文件保持原样，不在 ingress 中自动 OCR、解压或提取文档；
6. 只向 Agent 暴露项目内相对路径，不暴露平台引用、下载 URL、密钥或 adapter 缓存路径。

输入资源的保留、缺失重放、用户删除和项目清理由 Runtime 统一处理。Channel 配置不得重新定义一套私有附件 TTL 或清理任务。

## 5. Artifact 发布

可投递文件只能来自 Runtime `publish_artifact`：

1. Agent 或工具显式提交一个 sandbox 允许的完成文件；
2. Runtime 拒绝符号链接逃逸、目录、未完成文件和任意越界绝对路径；
3. Runtime 将内容复制到 Agent 工作目录之外的 Runtime 私有 artifact store，例如 `<sessionDir>/artifacts/<artifact-id>/content`，并使用同目录临时文件和原子 rename；
4. Runtime 记录 artifact ID、Session/Run、MIME、大小、SHA-256、原始生成来源和 canonical event；
5. delivery 只引用 artifact ID，不直接引用可变工作树路径；每次打开投递流都验证 regular-file、大小和 SHA-256 与记录一致，不一致则终止投递并产生 canonical integrity failure。

`.mothx/tmp/inputs` 是 Agent 可读取的项目输入区域，不是 artifact 不可变存储。不得依赖文件权限、`.gitignore` 或仅在部分 sandbox mode 中生效的 deny rule，把项目内路径称为不可变快照。WebUI 下载、ACP 投影和 Channel 上传都通过 Runtime artifact ID 打开私有内容，不接收真实 storage path。

Provider hosted attachment 也必须先经受权 resolver 下载和验证，再注册为同一种 artifact。助手文本中的路径、URL 或“文件已生成”字样永远不能触发投递。

## 6. Durable delivery outbox

### 6.1 原子性

对需要 Channel 投递的终态 Run，Runtime 必须在提交 assistant entry 和 durable terminal state 的同一事务中，建立一个 run-level delivery intent，并为 assistant caption、每个已发布 artifact 的上传/发送以及必要的 fallback 建立有序 operations。不得先完成 `FinishDurable`，再异步尽力创建 delivery rows。

如果现有事务边界暂时无法容纳 delivery intent，应先扩展 Runtime-owned terminal transaction。恢复协调器仍应以 canonical terminal/artifact event 做防御性对账，并以唯一键幂等补齐历史缺失记录；这不能替代正常路径的原子提交。

概念记录至少包含：

```text
delivery_intents(
  id, session_id, run_id, platform, target_id, reply_message_id,
  transport_context, status, created_at, updated_at
)

delivery_operations(
  id, intent_id, operation_key, artifact_id, operation_kind,
  sequence, depends_on, idempotency_key, payload_digest, status,
  provider_asset_id, provider_message_id, provider_state,
  attempt_count, next_attempt_at, failure_code,
  lease_owner, lease_epoch, lease_expires_at,
  created_at, updated_at
)

UNIQUE(run_id, platform, target_id)                 -- intent
UNIQUE(intent_id, operation_key)                    -- caption/upload/send/fallback
```

`transport_context` 在 intent 创建时冻结本次投递的准确协议上下文。微信至少持久化触发本 Run 的 `context_token`，不得在恢复时改用会话中“最近有效”的 token；飞书持久化原始 reply message ID，后台投递则明确记录已选择的 chat target。它们都是受限 transport state，不进入 prompt、普通日志或对外事件。

`provider_state` 保存从上传到发送之间恢复所需的 operation 私有状态。微信至少需要保存 `filekey`、AES key、`x-encrypted-param`、明/密文大小和该消息 operation 的 `client_id`；飞书保存已取得的资源 key。caption、upload、media send 和 fallback 使用不同的稳定 `operation_key`，`sequence`/`depends_on` 明确顺序，因此多个 artifact 也不会重复 run-level caption。`payload_digest` 绑定实际文本或 artifact hash，恢复时不得在相同幂等 ID 下改变 payload。

实际 schema 只能通过 `internal/session/migrations.go` 追加，并由 Runtime store 访问；adapter 不得直接操作数据库。

### 6.2 状态与幂等

推荐状态机：

```text
pending
  -> uploading
  -> uploaded
  -> sending
  -> delivered

任一非终态阶段失败
  -> retry_wait -> 回到对应阶段
  -> failed（永久失败）

发送请求结果不明
  -> uncertain -> 经 provider 查询/已验证幂等重放后进入 delivered 或 retry_wait
```

- `idempotency_key` 在第一次网络调用前持久化，重试和进程重启后保持不变；
- 飞书创建/回复消息时使用该稳定 UUID，不能只依赖响应成功后才得到的 message ID；
- 微信每个 `sendmessage` 请求都使用预先持久化的稳定 `client_id`；caption 与 media 按官方包行为拆成两个请求时，必须是两个有顺序、各自可恢复的 operation，不能在媒体重试时重复 caption；
- operation 只有在其 `depends_on` 已确认完成后才能 claim；同一 intent 的 run-level caption 只建立一次，不能按 artifact 数量复制；
- 腾讯包把 `client_id` 当作返回的 message ID，但其 README 没有承诺重复 `client_id` 的服务端去重语义。真实 Bot 测试确认前，已写出请求但响应丢失的微信 delivery 必须进入 `uncertain`，不得换新 `client_id` 盲重发；
- 上传成功后先持久化 `provider_asset_id`，再发送消息；已保存 asset ID 的重试不得重新上传；
- 请求结果不明确时，优先使用 provider 查询或幂等合同确认；无法确认且幂等窗口已过时，进入明确的人工可诊断状态，不能无限盲重试；
- `delivered` 和永久 `failed` 是终态；`uncertain` 是可诊断的持久状态，不由定时器自动重发；同一失败或不确定诊断最多投影一次。

### 6.3 恢复

启动恢复、后台完成和 UDP 唤醒都只能触发 Runtime delivery coordinator：

1. 读取非终态 delivery intents/operations；
2. 对照 canonical Run/artifact event 幂等补齐缺失 intent；
3. 通过数据库 compare-and-swap 取得单条 operation 的 lease，递增 `lease_epoch`，设置 owner/expiry，并要求所有后续状态写入携带相同 epoch；过期 owner 的迟到写入必须被 fence；
4. 从已持久化阶段继续，而不是从头上传和发送；
5. 保存 provider 结果并投影 canonical delivery event。

多个进程可以同时发现同一待投递项，但只能有一个通过数据库 claim 执行网络副作用。

## 7. 平台投影

### 7.1 飞书

1. 图片 artifact 上传取得 `image_key`，再发送 `image` 消息；文件同理使用 `file_key` 和 `file` 消息。
2. 入站消息触发的 Run 优先回复原始 message ID；后台任务或无法回复时使用绑定 chat ID。
3. 平台拒绝类型、大小或权限时，delivery 进入永久失败，并通过同一会话补发一次简短文本诊断。
4. 任何失败响应都不得包含本地路径、storage key、下载凭据或原始平台密钥。

### 7.2 微信

微信以官方包 `2.4.6` 的原生媒体合同投影已发布 artifact：

1. Runtime 根据经过内容检测的 MIME 选择 `IMAGE`、`VIDEO` 或 `FILE` operation；adapter 只读取 Runtime 授权的不可变 artifact，不接受助手文本路径、任意远程 URL 或普通工作树文件。
2. 在第一次网络调用前持久化 `filekey`、AES key、明文 MD5/大小、密文大小和稳定 operation ID；调用 `getuploadurl` 后保存上传参数，CDN 上传成功后保存 `x-encrypted-param`，再进入消息发送阶段。
3. 构造与官方包一致的 image/video/file item，并使用 delivery intent 在 Run admission/terminal transaction 中冻结的 `context_token`；`run_id` 使用 canonical Run ID，`client_id` 使用该消息 operation 的稳定 ID。恢复不得改用绑定会话后来收到的 token。
4. caption 按官方包行为作为独立 TEXT 请求先发送；caption 和 media 分别记录状态，因此任一步骤恢复都不会重发已确认的前置消息。
5. 官方包未实现的语音出站、平台拒绝的类型/大小或经真实验证不可用的能力，才进入明确降级：若启用 `channels.media.publicDelivery`，发送 Runtime 生成的受控下载链接；否则发送一次文本诊断并指向同一 Session 的 WebUI artifact。
6. 不得把 `.mothx/tmp` 路径、绝对路径、`file://` URL、上传 URL、AES key、`encrypt_query_param` 或未经授权的 storage key 当作消息内容或降级链接。

公开下载 token 的生成、过期、替换和授权属于 Runtime artifact delivery，不属于微信 adapter。原生上传/发送失败也不能由 adapter 私自创建公开链接，必须回报 Runtime 由统一 policy 决定是否降级。

## 8. 配置

媒体开关和传输上限位于 `serve.json` 的 `channels.media`，通过现有 Config State 原子保存并由 Runtime policy resolver 读取：

```json
{
  "channels": {
    "media": {
      "enabled": true,
      "maxImageBytes": 20971520,
      "maxAudioBytes": 104857600,
      "maxVideoBytes": 104857600,
      "maxFileBytes": 52428800,
      "publicDelivery": {
        "enabled": false,
        "baseURL": "",
        "ttlHours": 24
      }
    }
  }
}
```

`enabled: false` 时，纯媒体消息返回明确短提示，但不下载内容。这里的大小限制是 Runtime ingress/delivery policy，不授权 adapter 建立独立存储路径；adapter 仍须在读取流和上传前执行 Runtime 下发的同一上限。官方包当前使用 100 MiB 入站保护值，但这不是已声明的微信服务端上限；最终默认值和各类型服务端限制必须由真实 Bot 验证固定。输入资源保留策略继续由共享 Runtime 定义；`ttlHours` 只适用于显式公开的 artifact 下载链接。

Channel 状态 API 可以返回解析后的 capability、大小限制和不可用原因，不返回下载凭据。WebUI 消费 canonical resource、artifact 和 delivery event，不读取 adapter 私有状态。

## 9. 生命周期与错误语义

- 下载或解密失败发生在 Run admission 前时，Runtime 将 ingress 标为失败并向用户返回确定错误，不创建半成品 active Run；
- InputResource 与 Run 的绑定必须和 user entry、execution intent、Run/start event 一起满足统一 admission 原子性；
- `/stop`、`/new`、Session replay、后台恢复和 `SessionRuntime.Shutdown` 复用共享 Runtime 生命周期；
- Channel 断线不取消已进入 Runtime 的 durable Run；结果通过 durable delivery outbox 恢复；
- 文本通道必须在媒体权限不足、下载失败、处理失败或投递失败时继续可用。

## 10. 实施顺序

1. 锁定飞书官方 SDK；锁定腾讯 npm 包 `@tencent-weixin/openclaw-weixin@2.4.6` 及 integrity，提取脱敏 JSON/HTTP fixture，并用真实 Bot 解决 README 与实现关于 POST/PUT、缩略图和大小限制的差异。
2. 将微信/飞书媒体分支收敛为 `InputIngress.Open`，接入统一 `PrepareInput` / `InputSubmission`。
3. 删除 Channel 侧 direct image content、旧私有 input attachment store、`read_attachment`、输入 TTL 和模型 ingress 拒绝路径；保留并建立工作目录之外的 Runtime 私有 artifact store。
4. 确保 Runtime 物化时对无扩展名图片生成可由标准 `read` 识别的规范文件名。
5. 将生成物统一迁移到 `publish_artifact` 和 Runtime 私有 artifact store；投递打开时验证大小与 SHA-256。
6. 扩展 Runtime admission transaction，实现 resource item key 与 submission key 两级幂等，确保同一平台事件只产生一个 user entry 和 Run。
7. 扩展 Runtime terminal transaction 和 session migration，实现 delivery intent + ordered operations、冻结的 reply context、稳定 UUID/`client_id`、`uncertain` 语义、lease epoch fencing 和分阶段恢复。
8. 接入飞书原生图片/文件投递，以及微信 `getuploadurl`、CDN 加密上传和 image/video/file `sendmessage`；公开链接只作为未支持类型或明确失败后的 policy 降级。
9. 更新 Channel/WebUI 状态投影、中英文文档和 changelog。

补充进度：Agent Core rich image capability gate 已接入统一执行链路；Channel 入站仍只负责提供受权流，text-only 模型不会在 ingress 阶段失败，工具读取图片后的能力错误由 Agent Core 统一产生。

请求级图片 payload admission 已在 Agent Core 前置执行，Channel 不再自行计算图片体积或模型能力；WebUI 的提交键已参与 Runtime 输入 item 去重，InputResource 到 Run 的绑定已与 intent/started event 同事务。Channel 已将正常结果接入 Runtime `PlanDelivery`、终态 outbox 和 coordinator 投影；微信原生媒体与进程重启后的主动恢复均已实现，真实 provider 语义验证仍待补充。

Channel 输入的无扩展名图片由 Runtime 内容识别并生成规范后缀，adapter 不需要维护 MIME/扩展名分支。

WebUI 与 Channel 的稳定事件键已同时进入 Runtime resource item key 和 schema 33 `runtime_submissions`；canonical user entry、submission hash/scope/fingerprint、intent、Run、turn、resource 绑定和 started event 同事务写入，同 key 可返回既有 Run，不同 fingerprint 明确冲突并整体回滚。用户条目使用 `run-user-<runID>` 确定性 ID，Agent Core 复用该条目而不重复持久化；linked retry 复用原请求消息。

Runtime `FindIdempotentRun` 已被 Channel 同步入口和 Responses background 复用；同步入口在 admission 锁内命中同一 `channel` scope 时不再重复启动 Agent。helper 优先读取 durable submission reservation，started event 仅为 schema 33 之前历史记录的命名兼容桥。

Run 回放会从 `input_resources.run_id` 重建资源 ID 列表，Channel 重启恢复不会因 SessionRun 查询缺少输入归属而重新下载或重复物化媒体。

### 10.1 设备迁移交接 checkpoint（2026-08-28）

当前工作树没有 commit；换机时必须连同未跟踪的新文件一起迁移，不能只复制已跟踪文件。当前事实如下。

已完成并验证：

1. Runtime 输入物化、项目相对路径 manifest、无扩展名图片识别、私有 artifact 快照和打开时大小/SHA-256 校验已接入生产入口。
2. schema 32 `input_resources` 与 schema 33 `runtime_submissions` 已落地。canonical user entry、resource 绑定、execution intent、Run、conversation turn、started event 和 submission reservation 在一个 admission transaction 中写入；用户 entry ID 为 `run-user-<runID>`。
3. Agent Core 在 Runtime 已持久化用户消息时复用该 entry，不再重复 append；linked retry 继续复用原始用户消息。CLI、TUI、WebUI/API、ACP、Channel 已传递同一个 Runtime-built message。
4. focused tests 已验证 admission 成功顺序、resource ownership、重复/冲突 submission 裁决和失败整体回滚：

```bash
go test ./internal/agentruntime -run 'Test(InputResourcesBindAtomicallyWithRunAdmission|FindIdempotentRun|RuntimeSubmissionReservation)' -count=1
```

已落地并验证：

1. schema 34 已追加 `delivery_intents` 与 `delivery_operations`；schema 35 追加 Runtime 输入事件表。两张 delivery 表包含 operation key、顺序、依赖、稳定 idempotency key、payload digest、provider state、retry 和 lease epoch 字段。
2. `internal/session/delivery_store.go` 已建立 plan 校验、幂等 create/get、artifact/Run ownership 检查、依赖满足判断、lease epoch fencing、终态 CAS 和 retry/uncertain 状态更新。
3. `internal/agentruntime/delivery.go` 已加入确定性的 run-level caption、逐 artifact upload/send 和 fallback planner；Channel 正常路径按 `PlanDelivery` -> `ExecutionRuntime.SetDeliveryPlan` -> `FinishDurableWithRetry` 写入 outbox，再由 transport projection claim/complete。
4. assistant entry、conversation turn、terminal event、Run 状态和 delivery plan 在同一个终态事务中提交；确定性 entry/event ID、重复 finish 幂等和无效 artifact 整体回滚已有 focused behavior tests。
5. `DeliveryCoordinator` 已提供 claim、lease epoch fencing、依赖顺序、指数 `retry_wait`、重试耗尽、provider checkpoint 保留和 `uncertain` 语义；依赖 operation 的永久失败/不确定结果会在同一事务级联收敛，避免遗留 pending 行。WeChat/Feishu 文本现在按 `TextDeliveries` 分别投影 caption/fallback，Feishu 原生图片/文件和 WeChat 原生 image/video/file projection 已使用同一操作记录。入站微信语音/视频/引用消息也已转为 Runtime `InputIngress`。Channel 正常投影已不再写 schema 30 `attachment_deliveries`，旧 API 已集中到命名迁移桥文件。
6. `serve` 启动时会立即并周期性调用 `DeliveryCoordinator.ReconcileDue`；恢复请求从冻结的 intent/operation/provider state 重建，不读取后来消息的 context。recovery fixture 已覆盖 caption、artifact reader、provider state 和终态更新；新增 OS 子进程 fixture 覆盖 lease 过期接管、已上传 asset 复用及稳定 client ID。
7. `internal/messaging/wechat/testdata/tencent-2.4.6/` 已固化 npm registry integrity、Git head 和脱敏的官方 JSON/HTTP contract fixture；测试会读取这些文件验证入站 item、AES key 编码和 `getuploadurl` POST 字段。fixture 明确不宣称真实 Bot 已验证。
8. 最近验证包括 session/agentruntime/agent/channel/WeChat/Feishu focused tests、跨入口输入测试和 `go test ./internal/architecture`；`git diff --check` 通过。受限环境下全包测试的 UDP/IPv6 listener 失败属于环境限制。

下一台设备继续：

1. 按锁定的腾讯官方包和真实自有 Bot 固化 `getuploadurl`、加密上传及 image/video/file `sendmessage` 的 method、缩略图和大小限制；在确认服务端重复 `client_id` 语义前，模糊结果继续进入 `uncertain`，不得盲重发。当前已完成 source-derived JSON/HTTP fixture，真实 Bot 结论仍待有凭据环境。
2. OS 子进程 recovery/provider-state fixture 已完成；仍需在真实 provider 环境补一轮跨进程测试，确认服务端 asset/client-id 重复语义。
3. 迁移仍读取 schema 30 `attachment_deliveries` 的外部 embedders；确认新 coordinator 投影和恢复测试覆盖后，删除 `internal/agentruntime/delivery_legacy_bridge.go`（含 `AttachmentService.BeginDelivery`/`FinishDelivery`/`ProjectDeliveries`）并更新 architecture guard。
4. 完整测试在受限环境可能因跨进程 UDP/localhost socket 被拒而失败；此类失败需与 Go 编译和 focused behavior 结果分开记录。

工作树注意事项：`docs/provider-model-list.md` 与 `internal/config/settings.go` 含用户原有改动，不属于本方案，迁移和继续开发时必须保留，不得回退。`.tmp-go-cache/` 是本地生成缓存，不需要迁移。

## 11. 验收标准

阶段进度：已验证 Runtime 输入流、resource item 去重、项目相对路径 manifest、canonical user entry 与 InputResource/intent/Run/turn/start event/submission reservation 的原子写入及失败回滚、durable submission reservation 的重复/冲突裁决、text-only 模型接收不在 ingress 失败、`publish_artifact` 私有快照篡改检测，以及 terminal/assistant/turn/delivery intent/ordered operations 的原子提交、回滚和幂等。冻结 transport context、claim/lease fencing、retry/uncertain、provider checkpoint、依赖失败级联、WeChat 原生 image/video/file、Feishu 图片/文件、分 operation 文本投影、serve 启动恢复和 OS 子进程 provider-state fixture 已有本地行为测试；真实 Bot 语义验证与 schema 30 迁移桥清理仍待完成。

| 场景 | 必须验证 |
|---|---|
| 微信协议来源 | fixture 只来自锁定的腾讯 npm 发布物和真实自有 Bot；版本、integrity 与证据文件可追踪；无第三方仓库协议依赖 |
| 飞书图片/文件入站 | 官方引用被受权下载；adapter 不落盘；Runtime 生成统一 resource、路径、哈希和事件 |
| 微信媒体入站 | 图片/语音/文件/视频、引用消息、多 item、两种 AES key 编码、`full_url`/回退、过期引用、未知 item 和纯媒体消息均有确定结果 |
| 无扩展名图片 | MIME 由内容识别，物化文件可被标准 `read` 作为图片读取 |
| 重复事件 | item key 并发重放只生成一个 resource；event submission key 只生成一个 user entry、execution intent 和 canonical Run；敏感 `Reference` 原文不持久化 |
| 输入语义 | 首轮 provider 消息只有 manifest；没有 direct image block、`read_attachment` 或 ingress 模型能力拒绝 |
| Artifact 边界 | 只有 `publish_artifact` 的 Runtime 私有快照可投递；项目内输入/普通文件不能冒充不可变 artifact；投递前 hash 校验通过 |
| 原子 delivery | terminal commit 与 run-level intent/ordered operations 不存在丢失窗口；故障注入后可幂等对账 |
| 飞书模糊超时 | 稳定 UUID 在重试和重启后复用，不产生重复消息 |
| 微信原生出站 | `getuploadurl`、AES-128-ECB 上传、`x-encrypted-param` 和 image/video/file item 与官方 `2.4.6` fixture 一致；POST/PUT 与缩略图差异有真实 Bot 结论 |
| 微信消息身份 | intent 冻结触发 Run 的 `context_token`，canonical `run_id` 和每个 operation 的稳定 `client_id` 正确；恢复不读取后来 token |
| 分阶段恢复 | caption/upload/send 的依赖与顺序稳定；已上传资源不重复上传；已确认消息不重发；过期 lease owner 被 epoch fence；永久失败或 `uncertain` 只诊断一次 |
| 微信降级 | 仅语音出站等未支持能力或明确失败进入文本/受控链接降级；不会把本地路径或 transport 凭据发给用户 |
| 生命周期 | cancel、shutdown、后台完成、进程重启和多进程竞争不留下无 owner 的 pending delivery |
| 跨入口一致性 | TUI、CLI、WebUI/API、ACP、微信、飞书只在协议解码和输出投影上不同，canonical resource、Run、artifact 和终态一致 |

实现完成后必须运行相关 `internal/agentruntime`、`internal/session`、`internal/messaging`、`internal/serve/channels` 测试、真实子进程/provider 恢复测试、跨入口 contract tests 和 `go test ./internal/architecture`；当前本地 recovery fixture 已覆盖协调器、跨进程 provider state 与协议投影，真实 Bot fixture 是唯一外部验证项。

完成定义：微信和飞书的媒体引用都通过薄 adapter 进入同一 Runtime 工作区输入合同，并以 event/item 两级幂等只创建一个资源集合和 canonical Run；Agent 只在显式读取时产生图片 rich content；所有出站文件均来自 `publish_artifact` 的 Runtime 私有、投递前验哈希快照；微信按腾讯官方包协议原生发送图片、视频和文件；delivery 以冻结协议上下文和有序 operations 在崩溃、超时、重试和多进程竞争下恢复，对 provider 未承诺去重的模糊结果停止盲重发并给出明确状态；任何媒体失败都不破坏文本会话。
