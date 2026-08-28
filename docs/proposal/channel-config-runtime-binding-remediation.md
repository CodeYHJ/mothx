# Channel 配置、运行时、Session 删除与 Tool 状态整改方案

> 状态：当前工作区实现候选已完成，尚未提交、尚未发布
> 日期：2026-08-04
> 范围：Serve WebUI、微信、飞书、`channels.Dispatcher`、主 `sessions.db`
> 关联提案：`docs/proposal/unified-channel-session-binding-proposal.md`
> 实现基线：当前工作区包含本方案的未提交、未发布实现，见 3.1；本提案按增量收敛执行，不按绿地重写执行

> 重要边界：本文提到的 migration 21/22、channel-tools 字段、config PATCH 语义和管理事件都仍属于未发布变更，可以在本次变更中原位修正；不能把当前工作区状态当成已经对外兼容的协议。

## 1. 背景

微信和飞书已经统一使用主 `sessions.db`，WebUI 也已经能够查看 channel 身份、转移 session 绑定，并为绑定 session 保存工具开关。当前主链路为：

```text
WebUI Channels.svelte
  |-- PATCH /api/serve/config/channels/{platform} -> selected writable serve.json layer
  |                                                   |
  |                                                   +-> Reload Effective + CLI overrides
  |                                                   +-> Dispatcher.ApplyConfig
  |                                                   `-> fingerprinted platform restart
  |
  |-- PUT /api/sessions/{id}/bindings ------------> sessions.channel_type/channel_id
  |                                                   `-> Dispatcher.RefreshBinding
  |
  `-- PUT /api/sessions/{id}/channel-tools --------> session_channel_tools
                                                      +-> session_channel_tool_generations
                                                      `-> Dispatcher.RefreshSessionTools

WebUI Sessions.svelte
  `-- DELETE binding -> DELETE session -----------> active/bound snapshot checks
                                                      +-> DeleteActiveSession / DeleteSession
                                                      `-> Dispatcher refresh

WeChat / Feishu inbound message
  -> Dispatcher.resolveSession(platform, identity)
  -> load bound session + session_channel_tools
  -> build/cache tool Registry
  -> session.LockRuntime(sessionID)
  -> reload session + build/execute Agent run from Dispatcher runRootCtx
  -> persist transcript/run events
  -> PublishExternalSessionUpdate
  -> /ws/runs
  -> WebUI stores + Chat
```

上图描述当前工作区的实现边界。channel PATCH/full PUT 已进入 `ServeConfigState` 的串行 apply/rollback 路径，生命周期写操作已进入 runtime/identity lock，platform 实例由 `PlatformSupervisor` 持有，Responses 竞争和管理事件已有 HTTP/WebSocket 测试。后续维护只应补充故障注入、真实 SDK readiness 或更细的观测，不得重新引入旁路 owner。

这条链路功能上已经贯通，但配置事实来源、platform 生命周期、session 删除和 tool 有效状态仍没有形成一致的事务边界。结果是部分 API 返回成功后，持久化状态、运行时状态和 WebUI 展示可能不一致。

本方案集中解决四类问题：

1. 配置写回层级错误；
2. platform 热更新会影响正在运行的 channel run；
3. 删除或转移 session 后，dispatcher 缓存和数据库可能失效不同步；
4. tool 的 requested、available、registered 三种状态被混为一个 `enabled` 状态。

## 2. 目标与非目标

### 2.1 目标

1. WebUI 只修改用户明确编辑的配置层和字段，不把项目配置或 CLI override 写入全局配置。
2. 修改一个平台的配置只重启该平台，且不取消已经进入 Agent 执行阶段的 run。
3. 删除、解绑、转移绑定和 `/new` 都遵守统一的 session 生命周期锁顺序，最终由同一协调边界更新数据库和 dispatcher 缓存。
4. tool 保存结果必须准确表达实际能否注册；WebUI 不再显示“已启用但 Agent 中不存在”。
5. 所有状态更新都有明确的 API 错误、持久化事实和 WebSocket/刷新恢复路径。
6. 不改变现有 `settings.json` / `serve.json` 字段语义。
7. 不回退已完成的 OpenAI Responses 能力；后台 submit、恢复、取消和 lineage 清理与 session 生命周期保持原子一致。

OpenAI Responses 影响结论：本方案不改变 provider 的请求/流式协议、Responses item、reasoning、`previous_response_id` 或后台状态机；只把 channel/session 管理操作纳入已有 runtime lock，并让 channel-only config PATCH 不触碰 Responses background runtime。若 submit/recover/reconnect 与删除/转移竞争，新增行为是返回明确的 `409 session_run_active`，而不是让远端继续运行、本地 lineage 被删除。

### 2.2 非目标

- 不改变微信凭据文件和飞书 App Secret 的存储方式。
- 不在本方案中支持一个 session 同时绑定多个 channel 身份。
- 不把 channel tool 配置改成全局 tool 配置。
- 不重构 OpenAI-compatible API 的 Agent 执行核心。
- 不迁移旧 `channels/<platform>/<user>/active.db` 数据。

## 3. 问题总表

| 优先级 | 问题 | 当前状态 | 剩余用户影响 | 主要位置 |
|---|---|---|---|---|
| P1 | Effective 配置写回错误层 | 已完成事务路径 | channel PATCH/full PUT 已串行、merge/atomic write、父目录 fsync、apply 失败恢复；原子写边界支持失败注入回归 | `internal/serve/config_state.go`, `run.go` |
| P1 | 任意配置保存重启全部 platform | 已完成主要 owner 收口 | fingerprint、candidate 失败保留旧实例和结构化 status 已覆盖；真实 SDK readiness 仍由 `Start()` 接口语义决定 | `run.go`, `platform_supervisor.go`, `channels/dispatcher.go` |
| P1 | 绑定转移后额外保存整份配置 | 已修复当前 WebUI 路径 | 需用回归测试防止恢复 | `ui/src/views/settings/Channels.svelte` |
| P1 | 删除/绑定 handler 与 dispatcher/API pool 分散编排 | 已完成统一协调器 | handlers、`/new` 已调用统一锁边界；API pool 删除失败保持数据库和事件事实不变并有回归测试 | `run.go`, `session_lifecycle.go`, `openaiapi/session_mgr.go` |
| P1 | session 生命周期检查与修改未持有 runtime lock | 已完成 | DELETE/bind/unbind/transfer/rotate 与 Responses submit/reconnect/cancel/abandon 共享 runtime lock，HTTP 冲突测试已补 | `run.go`, `openaiapi/handler_run_submit.go`, `openaiapi/responses_run_api.go`, `session/runtime_lock.go` |
| P1 | 已解析但仍在等待 runtime lock 的 channel 请求不算 running | 已完成 lease/generation | lease 已覆盖 pending entrant 和 identity revalidate；排队消息长跑只需继续扩展压力测试 | `channels/dispatcher.go` |
| P2 | Session 删除遗漏 channel tool 子数据 | 已完成代码收口 | 删除清单已集中，migration 21/22 升级/重开测试已补，tool/generation/Responses 关联清理有完整性回归 | `session/session.go`, `migrations.go` |
| P2 | UI/接口忽略 tool availability 和输入完整性 | 已完成事实收口 | `ChannelToolDefinition`、validator、registered/willRegister、appliesTo、stale-response 防护和浏览器 E2E 已接入 | `Channels.svelte`, `channels/dispatcher.go`, `run.go` |
| P2 | `rt.cfg` / platform/cron/login 状态无统一所有者 | 已完成主要 owner 收口 | config snapshot、PlatformSupervisor、cronMu、login pointer lock 和目标包 race 覆盖已接入 | `run.go`, `channels_api.go`, `platform_supervisor.go` |

### 3.1 当前工作区实现基线

本提案不是从零实施。按 2026-08-04 当前工作区的未提交代码，以下能力已经落地到实现候选。后续修正必须在这些代码上收敛，不能另起一套平行 runtime、API 或存储模型。

| 能力 | 当前状态 | 已有实现 | 后续处理 |
|---|---|---|---|
| writable config layer | 已完成 | `ServeConfigState` 已区分 global/project/explicit，带 update mutex、deep snapshot、CLI override replay | 后续只补 legacy full PUT 稀疏字段和故障注入回归 |
| channel config API | 已完成事务路径 | `PATCH /api/serve/config/channels/{wechat,feishu}` 使用 merge-patch；WebUI 已停止完整 channel PUT；项目层、并发和失败回滚均有覆盖 | legacy full PUT 仍保留为兼容入口 |
| 私有原子写 | 已完成 | 同目录临时文件、`0600`、file/parent `fsync`、rename、apply 失败恢复；`writeAtomic` 边界可注入失败 | 不依赖平台特定 syscall mock |
| 微信登录后启用 | 已完成 | 登录成功只调用 selected writable layer 的 `UpdateChannel()`，与并发 PATCH 共用 update mutex | 真实扫码网络流程仍由 SDK 集成测试负责 |
| platform 差异更新 | 已完成主要 owner 收口 | fingerprint 只重启变化平台，`PlatformSupervisor` 接管 map/snapshot/StopAll；内置 transport 通过 `messaging.Readiness` 就绪后切换，候选失败保留旧实例并发布 status 事件 | 第三方未实现 `Readiness` 的 transport 使用兼容的立即切换路径 |
| transport/run context 分离 | 基础已完成 | Dispatcher 已有 `runRootCtx`；WeChat 使用 `context.WithoutCancel`，Feishu 原本使用独立 context | 保留；统一由 Dispatcher 派生 run context，避免 platform 层和 Dispatcher 双重拥有生命周期 |
| active run 延迟失效 | 已完成 lease/generation | `pendingEntrants`、`activeRuns`、generation、identity revalidate 和 Rotate 同一失效路径已接入，并有 stale generation/active lease 回归 | 长跑压力不改变正确性边界 |
| session 删除保护 | 已完成 | `SessionLifecycleService` 在 runtime/data lock 内复查 bound，再调用 API pool 删除并刷新 dispatcher；pool 失败回归已覆盖 | 无 |
| binding 保护 | 已完成 | lifecycle service 使用 identity + runtime locks，transfer 使用稳定排序双锁，`/new` 共用 Rotate，Rotate 会在锁后重读并在转移时重试 | 无 |
| session 子数据清理 | 已完成代码收口 | 删除清单已抽为 `deleteSessionDataTx`，tool/generation/Responses 表共用同一列表并由 migration/删除完整性测试验证 | 无 |
| channel tool API | 已完成主要事实收口 | `ChannelToolDefinition` 驱动 catalog、validator 和 Registry，`GET/PUT /channel-tools` 返回 effective/registered/willRegister；全 catalog available/registered 契约测试已补 | 无 |
| tool generation 持久化 | 已完成 | migration 22 和 `session_channel_tool_generations` 已加入，保存时同事务递增并写入 cached session；stale entrant/active lease 有回归 | 无 |
| WebUI channel/tool 流程 | 已完成 | 已切换 platform PATCH 和 channel-tools GET/PUT，禁用 unavailable tool，并消费 request generation/`appliesTo`/registered 状态 | 无 |
| WebUI session 删除 | 已完成 | 已禁用 running 删除，顺序 unbind + delete；中间失败会提示部分成功并刷新事实，Chromium E2E 同时覆盖成功和 unbind 成功/delete 失败分支 | 无 |
| 管理事件 | 已完成 | `/ws/logs` 发布 config/platform/binding/delete/tool 结构化事件，WebUI 收到事件后刷新事实源 | 局部更新优化不是正确性前置条件 |
| 新行为测试 | 已完成主要覆盖 | 已增加 config layer/atomic failure/lifecycle/API pool/lease/platform readiness/migration、HTTP、Responses conflict、WebSocket E2E、race、UI JS/build 和 Chromium E2E 验证 | 仅第三方 SDK 的真实远端认证不在离线测试范围 |

当前基线已通过：

```bash
go test ./...
go test -race -count=1 ./internal/session ./internal/serve/channels ./internal/serve ./internal/serve/openaiapi
(cd ui && node --test src/lib/*.test.js)
(cd ui && npm run build)
(cd ui && npm run e2e)
```

上述命令已在当前工作区通过，覆盖 config layer/merge/rollback/concurrency、atomic write failure boundary、lifecycle conflict/API pool failure、platform readiness/rollback、lease eviction、Responses submit/reconnect conflict、migration 21/22 upgrade/reopen、WebSocket E2E、Channels/Sessions 成功与部分失败 Chromium E2E 和前端 61 项 JS 测试。本次环境系统未预装 Node，实际使用等价的临时 Node 22 工具链执行前端命令；标准开发环境直接使用 `node`/`npm`。不在离线测试范围内的只有第三方 SDK 远端认证和网络服务本身。

### 3.2 增量改造原则

1. 保留当前工作区已经联通 WebUI 的 channel PATCH 和 channel-tools GET/PUT 路径，不再新增第二套替代 endpoint。它们尚未发布，因此允许在同一变更中同步调整请求/响应字段，不承担外部兼容负担。
2. 保留 `ServeConfigState`、现有 root `sessions.db`、`session.LockRuntime/TryLockRuntime` 和 Dispatcher cache，围绕它们补齐事务与所有权。
3. 已有 `invalidated`、tool generation 和 platform fingerprint 可以演进字段/类型，但不能同时保留新旧两套失效或 generation 逻辑。
4. handler 中已加入的 `sessionRunIsActive()` 只作为响应信息 helper，不再承担并发正确性；真正判定移到 lifecycle lock 内。
5. 完成统一服务后，删除 handler、binding handler、channel `/new` 和微信登录中的旧编排代码，不保留双路径 fallback。
6. 每完成一阶段就增加对应测试，再进入下一阶段；不能先叠加更多状态字段，最后一次性补并发测试。

### 3.3 Migration 21/22 处理决定

已确认当前所有未提交代码均未发布，migration 21/22 没有进入公开构建，也没有被真实用户数据库记录。因此：

1. 直接原位修订 migration 21/22，不追加 23 来修复未发布 migration。
2. migration 21 改为清理 orphan `session_channel_tools` rows；不为移除一个未启用的声明式 foreign key 做整表重建。
3. migration 22 保留 `session_channel_tool_generations` 建表，并在同一 migration 中清理或避免 generation orphan rows。
4. `currentSchemaVersion` 保持 22，除非本方案最终确实引入第三个独立 schema change。
5. fresh DB、从公开版本 20 升级、重复打开已升级 DB 三条路径必须测试。
6. `currentSchemaVersion`、集中删除清单和 migration 测试必须在同一个变更中更新，不能只提升版本号。

同理，本次未发布的新 endpoint、tool response 字段和 config PATCH 语义可以在当前变更中直接修正前后端，不需要 deprecated alias。只对修改前已经发布的旧 API 保留兼容评估。

## 4. 配置写回层级

### 4.1 原问题与当前处理状态

默认启动时，`LoadConfig()` 按以下顺序得到有效配置：

```text
defaults
  <- global ~/.mothx/serve.json
  <- project <cwd>/.mothx/serve.json
  <- CLI flags / RunOptions
```

当前工作区已经用 `ServeConfigState` 修正 writable path 选择，并让 WebUI channel 保存改走字段接口。“项目配置始终写到全局”“WebUI 整份复制 Effective”“并发 PATCH lost update”三个主路径已由 update mutex、writable-layer merge、CLI override replay 和 rollback 处理。

后续维护只需关注兼容入口的完整 PUT 文档和第三方 SDK 的线上认证观测；核心配置事务、平台切换、生命周期锁和 Responses 隔离已经在当前未发布实现中收口。

### 4.2 配置模型

运行时需要显式区分以下三种值：

```go
type ServeConfigState struct {
    Effective      *Config          // 只供运行时和 GET 展示
    WritablePath   string           // 本次 WebUI 编辑写入的配置层
    WritableLayer  ConfigLayer      // global | project | explicit
    RuntimeOverride RunOptions      // 只应用，不持久化
}
```

可写层选择规则：

1. 显式传入 `--config <path>`：写回该文件；
2. 未显式传入且项目 `.mothx/serve.json` 已存在：写项目文件；
3. 否则：写全局 `~/.mothx/serve.json`；
4. CLI override 永远不写入任何配置文件。

这里的 `WritablePath` 只决定编辑目标，不代表该文件本身包含完整有效配置。

### 4.3 API 方案

WebUI 不再通过完整 PUT 保存 channel。新增字段级接口：

```http
PATCH /api/serve/config/channels/wechat
PATCH /api/serve/config/channels/feishu
```

请求只包含对应 channel 可编辑字段：

```json
{
  "enabled": true,
  "workDir": "/workspace/project",
  "allowedUsers": ["user-a"],
  "autoTyping": true
}
```

接口采用真正的 merge-patch 语义：省略字段表示保留 writable layer 中的原值；显式空字符串或空数组表示覆盖为空。`enabled` 可以单独更新，不要求每次提交完整 platform object。响应中的 `configured` 返回合并后的 writable-layer platform object，而不是原始请求 body。

实现要求：

1. 读取 `WritablePath` 中的原始 JSON object；不存在时从空 object 开始。
2. 只合并 `channels.<platform>` 和兼容需要的 `features.<platform>`。
3. 不将 `Effective` 的其他字段写入目标文件。
4. 使用临时文件、`fsync`、rename 的原子保存流程，文件权限保持 `0600`。
5. 保存前先完成字段校验和 runtime apply 预检。
6. 响应同时返回持久化层和最终有效值：

```json
{
  "layer": "project",
  "path": "/workspace/.mothx/serve.json",
  "configured": { "enabled": true },
  "effective": { "enabled": true, "workDir": "/workspace/project" },
  "restart": { "platform": "wechat", "required": true }
}
```

现有 `PUT /api/serve/config` 暂时保留兼容，但 WebUI 不再调用。后续可将完整 PUT 限定为显式配置文件场景，并在响应中增加 deprecated 提示。

### 4.4 配置更新事务模型

配置更新必须由 `ServeConfigState` 内部的一把 update mutex 串行化。不能让 handler 自行执行“读文件 -> 写文件 -> reload -> apply”，否则微信和飞书并发保存会发生 lost update，且 runtime apply 失败时磁盘已经改变。

当前工作区已经删除旧的公开 `saveChannelConfigPatch()`，`handleChannelConfigPatch()`、legacy full PUT 和 `enableWechatAfterLogin()` 均进入 `ServeConfigState.UpdateChannel/UpdateFull()`。文档中的事务顺序是现行实现的约束，后续修正不得恢复旧的 save -> reload -> apply 旁路。

建议把更新收口为一个操作：

```go
func (s *ServeConfigState) UpdateChannel(
    ctx context.Context,
    platform string,
    patch ChannelConfigPatch,
    prepare func(*Config) (PreparedRuntimeChange, error),
) (*ChannelConfigUpdateResult, error)
```

严格顺序如下：

```text
lock config update mutex
  -> read writable layer bytes and metadata
  -> merge only channels.<platform> / features.<platform>
  -> decode and normalize candidate writable layer
  -> rebuild candidate Effective + reapply CLI overrides in memory
  -> validate credentials/workDir/allowedUsers and construct provider/platform candidates
  -> prepare runtime change without replacing live objects
  -> atomically persist candidate writable layer
  -> commit prepared runtime objects and swap Effective snapshot
  -> publish event
unlock
```

prepare 前先对 Effective 做分类 diff，避免当前 `Dispatcher.ApplyConfig()` 在每次 channel 保存时无条件调用 provider factory：

| Diff 类别 | 允许影响的 runtime |
|---|---|
| `channels.<platform>.enabled/credential/autoTyping` | 对应 platform transport + dispatcher route snapshot |
| `channels.<platform>.workDir/allowedUsers` | dispatcher route/security snapshot + 对应 session generation，不重启 transport |
| provider/model/agent/sandbox/hooks | dispatcher provider/Agent factory + session generation |
| cron/memory/tool feature | 对应 store/scheduler/tool runtime + session generation |
| API/OpenAI Responses background config | OpenAI API Server 自己的 apply path；channel PATCH 不得触碰 |

纯 channel PATCH 不得重建 OpenAI-compatible/Responses provider，也不得调用 `openaiapi.Server.ApplyServeConfig()`。只有 provider/model/API 配置入口才允许更新 Responses background manager；这条隔离保持当前已经完成的 Responses runtime 不受 channel 编辑影响。

约束：

1. `prepare` 不得停止旧 platform、关闭旧 Registry/MCP 或修改 live pointer。
2. 写文件失败时丢弃 prepared objects，runtime 保持原样。
3. runtime commit 必须是不会再失败的指针替换；可能失败的工作必须全部在 prepare 阶段完成。
4. 如果平台 SDK 无法做到无副作用预启动，则先完成静态校验，持久化后启动；启动失败要恢复旧文件并继续保留旧 platform，响应返回明确的 rollback error。
5. 原子写除临时文件 `fsync` 外，还应在 rename 后 `fsync` 父目录，保证崩溃一致性。
6. `Effective` 以 immutable snapshot 发布；所有读路径只能通过 `Snapshot()` 获取，不得直接读写 `rt.cfg`、`rt.platforms` 或内部 slice/map。
7. `PATCH` 必须把请求字段 merge 到 writable layer 的已有 platform object；不得用请求 object 整体替换 `channels.<platform>`。
8. 当前 WebUI 发送完整 platform payload，该请求在 merge-patch 语义下仍然有效，因此后端修正不要求再次改 API 路径或回退前端。
9. 兼容保留的 `PUT /api/serve/config` 也必须进入同一 update mutex 和 prepare/persist/commit 流程；在此之前只能视为 legacy 风险入口，不能因 WebUI 不再调用就忽略。

### 4.5 当前落地文件

- `internal/serve/config_state.go`
  - 已实现 layer/path/override 选择、update mutex、Snapshot、UpdateChannel、UpdateFull 和父目录 fsync；
  - 旧的公开 `saveChannelConfigPatch()` 已删除。
- `internal/serve/run.go`
  - channel PATCH 和 legacy full PUT 均调用 config state transaction；
  - handler 不再自行组合 save/reload/apply；内置 platform 候选 readiness 失败会把错误传回 transaction 并恢复旧 runtime/file。
- `internal/serve/channels_api.go`
  - 保留微信登录流程；登录成功只调用 `UpdateChannel()`。
- `ui/src/views/settings/Channels.svelte`
  - 保留已经完成的分平台 PATCH 和 transfer 不保存 config；
  - 保存后按响应局部刷新 config/status，失败不覆盖已有 store。
- `ui/src/lib/stores.js`
  - 增加结构化 config event 的局部 refresh helper。

### 4.6 验收测试

1. 只有全局配置时，微信修改只更新全局 `channels.wechat`。
2. 项目配置存在时，只更新项目文件，全局文件字节不变。
3. 使用 `--config` 时，只更新显式文件。
4. 使用 `-p` / `-m` / `--work-dir` 后保存 channel，任何 override 都不出现在文件中。
5. 修改微信不会改变飞书、API、security、provider 等兄弟字段。
6. 保存失败时文件和 runtime 均保持旧值。
7. 微信和飞书并发 PATCH 不丢失任一平台更新，且最终 Effective 与文件一致。
8. runtime prepare 失败时 writable file 字节、mtime、live platform 和 Effective snapshot 均不变。
9. `go test -race` 下 config GET/status/platform callback 与 PATCH 并发无 race。

## 5. Platform 与 Run 生命周期

### 5.1 原问题与当前处理状态

当前工作区已经按 platform fingerprint 选择性调用 `restartPlatform()`，Dispatcher 也引入 `runRootCtx`，WeChat/Feishu 已不会直接把 transport cancel 作为 Agent run cancel。原先“任意保存停止全部 platform”和“transport 重启直接取消 Agent run”的主问题已修复。

以上风险已在当前未发布实现中收口：`PlatformSupervisor` 是 platform map 的唯一 owner，内置 candidate 通过 `messaging.Readiness` 完成启动握手，早期失败不会删除健康实例；Dispatcher 按 diff 选择性刷新 provider/session；config、cron、login 和 status 读取均走受保护 snapshot/owner，目标包 race 已通过。第三方 transport 若未实现可选 readiness，则按兼容路径立即切换并沿用其自身错误恢复语义。

### 5.2 生命周期分层

必须把 transport 生命周期和 run 生命周期分开：

```text
Serve process context
  |-- Channel run root context
  |     `-- run context + explicit cancel + timeout
  |
  `-- Platform transport context
        |-- WeChat long poll
        `-- Feishu WebSocket
```

约束：

- platform 重启只取消 transport context；
- 已经接受的消息改用 channel run root context 执行；
- session stop 只取消该 session 当前 run；
- serve shutdown 才取消 channel run root context 下的全部 run；
- platform transport 不拥有 Agent run。

建议由 `channelRuntime` 创建 `runRootCtx`，初始化 dispatcher 时显式传入。`HandleMessage()` 在通过白名单和 session 解析后，从 `runRootCtx` 派生 run context，不再直接使用 platform poll/WebSocket context。

### 5.3 PlatformSupervisor

新增一个小型运行时所有者，集中管理 platform 实例：

```go
type PlatformSupervisor struct {
    mu        sync.RWMutex
    platforms map[string]messaging.Platform
    configs   map[string]PlatformConfigFingerprint
}
```

提供以下操作：

```go
ApplyPlatformConfig(ctx, platform, nextConfig) (changed bool, err error)
Get(platform string) messaging.Platform
Statuses() []channelStatus
StopAll() error
```

更新流程：

1. 比较平台相关配置 fingerprint；
2. 未变化直接返回，不重启；
3. 先验证凭据、workDir 和配置；
4. 构造 candidate，但不在旧 transport 仍收消息时同时启动，避免两个实例重复消费同一平台消息；
5. 在 per-platform operation lock 下停止旧 transport；已接受的 run 因 context 分层继续执行；
6. 在 goroutine 中启动 candidate，并通过 status callback、`Start()` 早期返回和有界 timeout 判断“启动已接受”；`Start()` 本身是阻塞事件循环，不能同步调用等待完成；
7. candidate 启动已接受后在锁内替换 map；如果立即启动失败，尝试用旧 config/旧实例恢复 transport，并按配置事务规则回滚文件与 Effective；
8. 发布一次结构化 `channel_status_changed` 事件，包含 `starting/connected/disconnected/rollback_failed` 状态。

`messaging.Platform.Start()` 仍是阻塞运行接口，因此本方案增加了可选的 `messaging.Readiness`：内置 WeChat/Feishu transport 在本地静态预检和 transport 初始化完成后发出一次性就绪结果，配置更新同步等待该结果；候选早期失败会回滚文件和 Effective/runtime。就绪后的远端异步掉线属于 platform health 状态，不反向回滚已提交配置；未实现可选接口的第三方 transport 使用兼容的立即切换路径，不能在 supervisor 中用 sleep 猜测。

当前工作区已经用 `PlatformSupervisor` 接管实例 map、快照和 shutdown；`restartPlatform()` 会先静态检查凭据，配置无效时保留旧实例，配置禁用时才停止并移除旧实例。candidate 的 `Start()` 异步运行，若早期返回会由 `RemoveIf` 清理并尝试恢复旧实例；后续只在 supervisor 上补受控 readiness 观测，不得增加第三套 platform collection。

微信 fingerprint 至少包含：

```text
enabled, credPath, autoTyping
```

飞书 fingerprint 至少包含：

```text
enabled, appID, appSecret
```

`workDir` 和 `allowedUsers` 属于 dispatcher/session 解析配置，不要求重连平台 transport，但必须让下一次消息使用新值。配置更新时应失效相应 platform 的空闲 session cache；运行中 session 在当前 run 结束后失效。

### 5.4 安全失效而不是立即销毁

当前工作区已让 `ApplyConfig()`、`RefreshBinding()`、`RefreshSessionTools()` 和 `RotateSession()` 统一走 lease/generation 失效路径。这既保护已经进入执行阶段的 run，也保护 resolve 后等待 runtime lock 的请求；MCP/Registry 只在 lease 归零后关闭。

为 `ChannelSession` 增加失效状态：

```go
type ChannelSession struct {
    // existing fields
    generation        uint64
    invalidated       bool
    activeRuns        int
    pendingEntrants   int
}
```

规则：

1. `resolveSession()` 返回 session lease，而不是裸 `*ChannelSession`。lease 创建时在 dispatcher lock 下增加 `pendingEntrants`，确保已经解析、尚在等待 runtime lock 的请求也持有资源引用。
2. 请求取得 session runtime lock 后，必须再次在 dispatcher lock 下比较 cache pointer、binding 和 generation；不一致则释放旧 lease，重新 resolve，禁止继续使用旧 session。
3. 请求正式开始时将 lease 从 `pendingEntrants` 转为 `activeRuns`；结束时释放 active lease。
4. `pendingEntrants == 0 && activeRuns == 0` 才算空闲，才允许立即从 cache 删除并关闭 Registry/MCP。
5. 非空闲 session 只标记 `invalidated=true`；最后一个 lease 释放时负责 evict 和关闭资源。
6. 下一条成功进入执行阶段的消息必须使用新 generation；旧 generation 的排队请求必须重新解析，不能在旧 Registry 上继续运行。
7. 不允许 config/tool/binding 更新关闭任何 lease 正在引用的 Registry 或 MCP client。
8. `invalidated`、generation、lease 计数和 cache 替换必须由同一 dispatcher mutex 保护，不能用分散的 `runID/runCancel` 推断资源是否仍被引用。

建议接口：

```go
type ChannelSessionLease struct {
    Session    *ChannelSession
    Generation uint64
    release    func()
}

func (d *Dispatcher) ResolveLease(platform, userID string) (*ChannelSessionLease, error)
func (d *Dispatcher) PromoteLeaseAfterRuntimeLock(lease *ChannelSessionLease) (valid bool)
```

`PromoteLeaseAfterRuntimeLock()` 还必须通过与 lifecycle service 共享的 identity locker 短暂锁住 `(platform, userID)`，重新读取 canonical binding 后再确认 lease。第一次 resolve 只创建 provisional lease，不在等待 runtime lock 期间持有 identity lock；否则会与管理操作及 Dispatcher mutex 形成锁顺序反转。当前流程是“dispatcher lock 下 capture lease -> 等 runtime lock -> identity lock 下 revalidate/promote”。

### 5.5 验收测试

1. 微信 run 执行时修改飞书配置，微信 run 不取消，微信 platform 不重启。
2. 微信 run 执行时修改微信配置，transport 可重启，但已接受 run 正常结束。
3. 仅修改 `allowedUsers` / `workDir` 时不重连 transport；下一条消息使用新配置。
4. serve shutdown 会取消全部 transport 和 channel run。
5. 并发执行 status、config apply 和 completion push，在 `go test -race` 下无 race。
6. 配置无变化时不得调用 platform `Stop()` / `Start()`。
7. 两条同 identity 消息串行等待 runtime lock 时更新配置：第一条正常结束，第二条必须使用新 generation，旧 MCP 只在 lease 归零后关闭。
8. 请求 resolve 后、设置 runID 前发生 tool/binding refresh，不得关闭该请求持有的 Registry，也不得让其用旧 binding 执行。

## 6. Session 删除、解绑与绑定转移

### 6.1 原问题与当前处理状态

当前工作区已经在 HTTP handler 中增加 bound/running 409，binding 更新后会刷新 dispatcher，`DeleteSession()` 也已加入 channel tools/generation/Responses lineage 清理，`SetChannelTools()` 会先确认 session 存在。实际跨组件编排已迁入 `SessionLifecycleService`。

历史问题是 handler 曾按以下顺序拼接：

```text
query active run snapshot
  -> query binding snapshot
  -> mutate session/binding/delete
  -> refresh dispatcher
```

快照检查和修改之间的窗口现在由 runtime/identity/data locks 关闭；Responses submit/reconnect/cancel/abandon 与 Delete/Transfer 共用同一 runtime lock。`/new`/`/clear` 也经 lifecycle service Rotate，关联清单由 `deleteSessionDataTx` 集中维护并由 migration/完整性测试复用。

### 6.2 统一生命周期服务

不要让 HTTP handler 分别拼接 session、dispatcher 和 run manager 操作。新增 serve 层协调器：

```go
type SessionLifecycleService struct {
    sessions   *openaiapi.Server
    dispatcher *channels.Dispatcher
    sessionDir string
}
```

负责：

```go
Delete(ctx, sessionID, options)
Bind(ctx, sessionID, channelType, channelID)
Unbind(ctx, sessionID)
Transfer(ctx, fromSessionID, toSessionID, identity)
Rotate(ctx, identity)
```

所有操作遵循同一顺序：

```text
preflight
  -> acquire session runtime lock / lifecycle lock
  -> verify active run and binding version
  -> database transaction
  -> invalidate dispatcher cache
  -> update API pool/runtime snapshot
  -> publish binding/session event
```

这里不能只依赖 `GetActiveSessionRun()` 的数据库快照。已有 WebUI/API/Responses 提交路径会先取得 `session.TryLockRuntime()`，随后才写入 `session_runs`；如果生命周期操作先查数据库、后删除，就可能在这个窗口删除一个正在建立远端任务的 session。

统一锁约束：

1. `Delete`、`Bind`、`Unbind`、`Transfer`、`Rotate` 都必须参与 `internal/session/runtime_lock.go` 的同一锁域。
2. 普通管理 API 使用 `TryLockRuntime()`；失败立即返回 `409 session_running`，不得阻塞等待未知时长。
3. 只有在成功取得 runtime lock 后，才允许检查 `session_runs`、binding/version 和 session 是否存在；检查与数据库修改期间持续持锁。
4. `Transfer` 需要同时锁 source 和 target，按规范化 `(sessionDir, sessionID)` 字典序获取，逆序释放，避免死锁。
5. identity 路由还需要共享的 `ChannelIdentityLocks`，由 lifecycle service 和 Dispatcher 使用，键为 `(channelType, channelID)`；为避免 Dispatcher 在 runtime lock 后 promote 时与管理操作互锁，生命周期操作的锁顺序固定为排序后的 session runtime locks -> identity lock -> DB transaction -> dispatcher invalidation。
6. lifecycle service 持锁期间不得等待 platform 网络 I/O、远端 Responses cancel 或 WebSocket 推送；这些动作在 commit 后异步执行或使用有界 context。
7. `DeleteActiveSession()` 不再作为可绕过协调器的公共删除原语。底层仓储删除应要求调用方传入已取得的 lifecycle guard，或只暴露给 `SessionLifecycleService`。
8. 所有 handler、channel `/new`、Responses reconnect/abandon 和未来管理入口必须调用该服务，禁止复制“先查再改”的逻辑。
9. 当前 handler 中已经存在的 bound/running 409 响应格式应保留；只把判定和修改移入 service，避免前端错误处理再次变更。
10. 当前 `session.BindSession()`、`TransferBinding()`、`RotateBoundSession()` 的数据库事务继续作为仓储原语使用；service 负责外围锁、cache/API pool 更新和事件，不复制 SQL。
11. Dispatcher 入站 resolve 不在等待 runtime lock 时长期占用 identity lock，而是在 capture 和 promote 两个短临界区使用同一个 `ChannelIdentityLocks`；promote 失败时重新路由。

### 6.3 删除语义

默认 `DELETE /api/sessions/{id}` 采用保守策略：

| 状态 | 响应 | 行为 |
|---|---|---|
| session 不存在 | `404` | 不修改 |
| session 正在运行 | `409 session_running` | 要求先停止并等待终态 |
| session 有 channel 绑定 | `409 session_bound` | 要求先显式解绑 |
| local 且空闲 | `200` | 原子删除并清理缓存 |

不建议普通 DELETE 隐式解绑，因为用户可能误删唯一的 channel 入口。WebUI 在收到 `session_bound` 时显示明确的“解绑后删除”流程：

```text
DELETE /api/sessions/{id}/bindings
  -> wait binding_changed
DELETE /api/sessions/{id}
```

如果未来需要管理员强制删除，应提供显式参数或独立接口，并定义：取消 run、等待终态、解绑、删除的顺序，不能复用普通 DELETE 的模糊语义。

### 6.4 数据库清理

本项目不启用 SQLite foreign key，也不依赖 `ON DELETE CASCADE`。所有关联完整性通过仓储层事务、集中清理清单和完整性测试保证。

当前实现使用统一的 session 子数据清理 helper：

```go
func deleteSessionDataTx(tx *sql.Tx, sessionID string) error
```

已落地约束：

1. 维护唯一的子表清单，`DeleteSession()`、恢复清理和测试共用该清单；
2. 在删除 `sessions` 主行前，按顺序显式删除 `session_channel_tools`、run/event、capability、entries 等所有子表数据；
3. 任一子表删除失败则回滚整个事务，主 session 和其他子数据均保留；
4. `SetChannelTools()` 在同一写事务内先查询 `sessions`，不存在时返回明确的 `session not found`，不得写入孤儿配置；
5. 新增任何带 `session_id` 的表时，必须同时更新集中清理清单和删除完整性测试；
6. `internal/db` 不增加 `foreign_keys` pragma。

升级时通过 migration 清理现存孤儿数据：

```sql
DELETE FROM session_channel_tools
WHERE session_id NOT IN (SELECT id FROM sessions);
```

迁移只清理历史数据，不增加外键。测试应在删除完成后逐表查询 `session_id`，确保所有关联行均为零；同时构造任一子表删除失败的场景，验证事务完整回滚。

### 6.5 绑定转移语义

绑定仍采用数据库事务，但需补齐生命周期边界：

1. source 必须仍绑定指定 identity；
2. target 必须存在、为 local、且没有 active run；
3. source 有 active run 时返回 `409 session_running`，避免结果发送目标在 run 中途改变；
4. commit 后同时失效 source/target 的 API runtime 和 dispatcher route；
5. 发布 `binding_changed`，包含 old/new session ID；
6. 前端依据响应更新 bindings，不再额外保存 serve config。

### 6.6 Tool 配置归属

当前 tool 配置以 `session_id` 为主键，因此定义为“session 能力”，不是“channel identity 能力”：

- identity 从 Session A 转移到 Session B 后，使用 B 已有 tool 配置；
- A 的 tool 配置保留，因为 A 变成 local 后仍可能用于历史或再次绑定；
- channel `/new` 创建新 session 后使用默认 tool 配置；
- 不自动把 tool 配置随 identity 复制。

如果产品希望 tool 跟随微信/飞书身份，应改成独立 identity policy 表，不能在 transfer 中暗中复制 session 配置。本方案建议保持 session 归属，并在 UI 文案中明确“该 Session 的工具”。

### 6.7 验收测试

1. 删除绑定 session 返回 `409 session_bound`，数据和缓存不变。
2. 删除运行中 session 返回 `409 session_running`，run 不受影响。
3. 解绑后删除成功；下一条 channel 消息创建新的绑定 session。
4. 删除空闲 session 后 dispatcher、API pool、bindings 列表均无旧 ID。
5. 删除后 `session_channel_tools`、run、event、capability 等关联行均为零。
6. transfer 与入站消息并发时只有一种完整结果，不出现半绑定或双绑定。
7. transfer 后完成的 run 不会错误发送给新绑定身份。

### 6.8 OpenAI Responses 兼容性与不变量

本方案不修改 `openai-responses` provider 的请求、流式解析、reasoning、原生 item 回放、`previous_response_id` 或 conversation 状态机，但 session 生命周期与 Responses 后台运行共享主 `sessions.db`，因此必须显式保证：

1. 从取得 session runtime lock 开始，到本地 `session_runs`/`response_runs` 建立并把锁交给后台 coordinator 为止，session 不可删除、解绑或转移。
2. `responses_background` 的 `created`、`queued`、`running`、`cancelling`、`terminalizing` 都属于 active；生命周期操作不得只检查 `running`。
3. 删除成功意味着不存在该 session 的 active local run、active remote Responses run、恢复 coordinator 或待提交 run；远端 cancel 也必须先取得 runtime lock。
4. 普通 DELETE 不隐式发送远端 cancel。管理员强制删除若未来实现，必须按“请求 cancel -> 等待本地和远端终态 -> 清理 lineage -> 删除 session”的显式状态机执行；超时则返回冲突，不得继续删除。
5. `response_runs`、`response_turns`、`response_items`、`response_session_state`、`tool_execution_records` 必须保留在集中删除清单和完整性测试中。
6. serve 重启恢复 Responses run 与生命周期操作竞争时，也必须先取得相同 runtime lock；恢复取得锁则删除返回 409，删除先取得锁则恢复必须发现 session 不存在并停止。
7. channel 使用 `openai-responses` 协议时仍走普通流式 Agent loop；platform transport 重启不得取消该 loop。`responses.background` 仅影响 Serve 后台 run，不应被 channel config PATCH 重建或切换。

新增专项测试：

1. Responses submit 已取得 runtime lock、尚未创建 `session_runs` 时，DELETE/Transfer 返回 409。
2. DELETE 取得 runtime lock 后，Responses submit 返回 session unavailable，且不会调用远端 `POST /responses`。
3. recover coordinator 与 DELETE 并发时只有恢复或删除一个结果成功，不产生孤儿 `response_runs`。
4. cancel/terminalizing 窗口内 DELETE 始终返回 409；终态完成并释放锁后才允许删除。
5. 删除完成后逐表确认所有 Responses lineage 为零，并确认没有活跃的本地 cancel callback/coordinator。

## 7. Tool 状态真实性

### 7.1 原问题与当前处理状态

一个 tool 实际存在三种不同概念：

```text
requestedEnabled  用户为 session 保存的选择
available         当前运行环境是否具备注册条件
registered        当前 ChannelSession Registry 是否实际包含
```

当前工作区已经通过 `ChannelToolDefinition` 统一 catalog、validator 和 Registry builder；A2A availability 按 agent list/feature 判断，未缓存 session 的 `registered` 与 `willRegister` 已分离，generation 已写入 `ChannelSession`，lease 覆盖等待 runtime lock 的请求，WebUI 已消费 `appliesTo`/registered 并丢弃旧请求响应。全 catalog 的 available/registered 契约测试已验证默认与动态工具不会把不可用工具暴露给 Registry。

### 7.2 统一 Tool 描述

catalog 返回明确的默认值、可用性和原因：

```json
{
  "name": "cron",
  "default": true,
  "available": false,
  "unavailableReason": "cron scheduler is disabled"
}
```

session effective endpoint 返回 requested 与实际状态：

```http
GET /api/sessions/{id}/channel-tools
```

```json
{
  "sessionId": "session-1",
  "generation": 7,
  "tools": [
    {
      "name": "cron",
      "requestedEnabled": true,
      "available": false,
      "effectiveEnabled": false,
      "registered": false,
      "willRegister": false,
      "reason": "cron scheduler is disabled"
    }
  ]
}
```

`registered` 始终表示当前 generation 的 Registry 事实；session 未缓存时固定为 `false`。`willRegister` 表示下一次 generation 按当前 requested/availability 构建时的预期结果，不能复用 `registered` 表达推算值。

当前 catalog JSON 已使用 `default`，WebUI 也读取该字段，因此对外继续保留 `default`；内部 `ChannelToolDefinition.DefaultEnabled` 不要求与 JSON 字段同名。`SessionToolStates()` 对未缓存 session 返回 `registered=false`，并以 `willRegister` 表达预期。

### 7.3 保存接口

工具选择是完整集合替换，不应继续使用含义模糊的 PATCH：

```http
PUT /api/sessions/{id}/channel-tools
```

```json
{
  "tools": [
    { "name": "read", "enabled": true },
    { "name": "bash", "enabled": false }
  ]
}
```

服务端校验：

1. session 必须存在；
2. session 必须是当前微信或飞书绑定 session；
3. tool name 必须来自该 platform catalog；
4. tool name 不得重复；
5. 启用 `available=false` 的 tool 返回 `409 tool_unavailable`；
6. 请求必须包含 catalog 的完整集合，避免未提供项在默认工具和动态工具间产生不同语义；
7. 保存和 generation 更新在一个事务中完成；
8. 有 active run 时保存成功但只对 `nextGeneration` 生效，不关闭当前 Registry；响应明确 `appliesTo: next_run`。

未发布的旧 `PATCH /bindings` tool 旁路已删除；`/bindings` 只保留绑定/解绑/转移语义，tool 配置统一使用 `/channel-tools`。

### 7.4 Registry 构建

把 tool catalog 与注册 factory 放在同一事实来源中，避免 UI catalog 和 dispatcher 注册条件漂移：

```go
type ChannelToolDefinition struct {
    Name             string
    DefaultEnabled   func(*ToolRuntime) bool
    Available        func(*ToolRuntime) (bool, string)
    Register         func(*tools.Registry, *ToolRuntime) error
}
```

构建步骤固定为：

```text
load complete requested config
  -> evaluate availability
  -> calculate effectiveEnabled
  -> register only effective tools
  -> capture actual registered set
```

`Available` 必须表达注册的全部前置条件，而不只是“工具名称理论上存在”。例如：

| Tool | `available=true` 的最低条件 |
|---|---|
| `cron` | cron store 与 scheduler 已初始化 |
| `a2a_dispatch` | `features.a2aMaster` 已启用，agent list 可加载且 manager 可构造 |
| `browser` | browser skill/runtime 可加载；若全局 flag 只是默认选择，则应明确不作为 availability 条件 |
| sub-agent/workflow | AgentManager 可构造，相关 feature policy 允许按 session 启用 |
| `memory` | memory path 可解析且 store 可构造 |

注册约束：

1. `Register` 返回成功后必须再次查询 Registry，只有工具实际存在才设置 `registered=true`。
2. `Available=true` 但 `Register` 没有注册任何工具属于实现错误，不得静默返回成功。
3. 对动态工具组，不能因为组内任意一个 requested=true 就注册整组并把其他 requested=false 的工具短暂暴露；factory 必须逐项注册，或在 Agent 可见前完成确定性过滤。
4. 空闲 session 返回 `registered=false`，并用独立的 `willRegister` 表达预期。不能把推算结果伪装成当前 Registry 的事实。
5. catalog、validator、Registry builder 必须消费同一组 `ChannelToolDefinition`，禁止分别维护 hard-coded 名称和条件。

不要先注册全部默认工具再按配置删除。MCP tools 需要单独定义策略：

- catalog 可按 MCP server/tool 动态扩展；或
- 第一阶段明确 MCP 不属于 WebUI channel tool 开关，由 MCP 配置决定。

无论选择哪种方式，都不能让 UI 声称能够控制实际不在 catalog 中的 MCP tool。

### 7.5 WebUI 行为

- `available=false`：checkbox 禁用，显示具体原因。
- `requested=true/effective=false`：显示“已请求，当前不可用”，不能显示普通 enabled 状态。
- 保存按钮在 catalog 未加载完成时禁用。
- 切换 identity/session 时使用 request generation，丢弃晚到的旧响应，避免 tool 列表串到另一个 session。
- 文案从“通道工具注册”改为“该 Session 的工具”。
- 保存响应若为 `appliesTo: next_run`，显示“将在下一次运行生效”。

### 7.6 验收测试

1. cron unavailable 时 UI 不可勾选，直接 API 启用返回 `409`。
2. 未知、重复和缺失 tool name 返回 `400`，数据库不变。
3. 保存完整配置后，下一次构建的 Registry 与 `effectiveEnabled` 完全一致。
4. 所有默认工具与动态工具采用相同的未配置规则。
5. 运行中修改 tool 不关闭当前 Registry/MCP；下一次 run 使用新 generation。
6. 快速切换两个 identity 时，晚到响应不会覆盖当前 session 的 tool 状态。
7. transfer 后 tool 配置按目标 session 生效，不随 identity 隐式迁移。
8. A2A master 关闭或 agent list 无效时，`a2a_dispatch.available=false`，直接 API 启用返回 409，且 `registered` 不得为 true。
9. 每个 catalog item 都执行“available + requested -> register -> Registry lookup”契约测试，防止 UI 状态与实际 Registry 漂移。

## 8. 前后端事件与状态收敛

配置、绑定、删除和 tool 更新不能只依赖前端随后调用 `refreshAll()`。当前实现通过现有 WebSocket/log 通道发布结构化管理事件：

```text
channel_config_changed
channel_status_changed
binding_changed
session_deleted
channel_tools_changed
```

最小 envelope：

```json
{
  "type": "binding_changed",
  "sessionId": "new-session",
  "data": {
    "channelType": "wechat",
    "channelId": "user-1",
    "fromSessionId": "old-session",
    "toSessionId": "new-session"
  }
}
```

事实来源约束：

| 数据 | 唯一事实来源 | WebUI store |
|---|---|---|
| channel 配置 | 选定配置层 + runtime overrides | `serveConfig` |
| platform connected | PlatformSupervisor 实例状态 | `channels` |
| identity/session 绑定 | `sessions.channel_type/channel_id` | `sessionBindings` |
| tool requested | `session_channel_tools` | session tool state |
| tool effective/registered | dispatcher tool evaluator/current generation | session tool state |
| run/transcript | SQLite run/entries/event tables | `runEvents`, chat state |

事件只负责提示客户端增量更新，SQLite/配置文件仍是刷新恢复的事实来源。WebUI 收到未知 generation 或发生断线时，重新请求对应资源，不从本地状态猜测结果。

## 9. 实施顺序与记录

以下顺序以第 3.1 节的当前工作区为起点，已在本次未发布实现中执行。标为“保留”的代码没有重复实现；旧 helper/handler 编排已在新路径测试通过后删除。

### 阶段一：阻断数据和运行时损坏（已完成）

1. 已按第 3.3 节原位修订未发布的 migration 21/22，保持 schema version 22，并补齐 fresh/20-to-22/reopen 测试。
2. 已保留 `session.LockRuntime/TryLockRuntime` 和 binding 仓储事务，引入 `SessionLifecycleService` 与 identity lock。
3. DELETE、Bind、Unbind、Transfer、Dispatcher `/new`、Responses recover/reconnect/cancel/abandon 已迁入统一 runtime-lock 边界，并配套竞争测试。
4. 已保留 `session_running` / `session_bound` API code，删除 handler 中无锁的 `sessionRunIsActive() -> mutate` 编排。
5. 已抽取集中 session 子表清理 helper，复用 tool/generation/Responses 表清单，并补齐删除完整性测试。
6. 删除成功后已由 service 统一失效 dispatcher、API pool 和 runtime snapshot，再发布事件。

### 阶段二：修正配置保存和 platform 热更新（已完成）

1. 已保留 `ServeConfigState`、writable layer 规则、channel PATCH 路由和 WebUI 调用。
2. update mutex、snapshot、`UpdateChannel()` 和 `UpdateFull()` 已统一 HTTP PATCH、微信登录后启用及 legacy full PUT。
3. 已采用 merge-patch、父目录 `fsync` 和 prepare/persist/commit rollback 约束。
4. 已清理绕过 snapshot 的 `rt.cfg` / `rt.platforms` 访问，并将 cron config apply 与 serve shutdown 纳入 runtime owner。
5. 已保留 fingerprint、bot factory、runRootCtx 和 transport/run 分离逻辑；`PlatformSupervisor` 接管 map/snapshot/stop owner。
6. candidate 启动失败保留旧 platform、配置文件与 Effective 的行为已有单元测试。

### 阶段三：统一 tool 状态（已完成）

1. 已保留 `/channel-tools` GET/PUT、完整集合 validator、generation 表和 WebUI 路径。
2. `ChannelToolDefinition` 已替换 `ToolCatalog()`、`replaceChannelTools()` 和 `resolveSession()` 中的 hard-coded 条件。
3. `a2a_dispatch` 等 availability 已修正，注册后从 Registry 捕获事实，并以 `willRegister` 表达预期。
4. `invalidated bool` 已升级为 lease + cached generation；刷新、Rotate、shutdown 共用 release/evict 路径。
5. persisted tool generation 已写入新建 `ChannelSession`，runtime lock 后复查，旧 generation 会重新 resolve。
6. WebUI 已消费 `appliesTo`、effective/registered/willRegister 和 identity/session request generation。

### 阶段四：事件、观测与兼容清理（已完成主要路径）

1. 已发布结构化管理事件并实现 WebUI 事件触发的事实刷新，覆盖 config/platform/binding/delete/tool 事件。
2. 已增加 platform restart、cache generation、tool apply 日志。
3. WebUI 已覆盖“解绑后删除”的中间失败提示和事实刷新，不会把部分成功显示成完全失败或完全成功。
4. 已删除未发布的 `/bindings` tool GET/PATCH 旁路；legacy full config PUT 保留但复用 `UpdateFull()` 统一事务。
5. 已删除未使用的旧 channel save helper；cache invalidation 只保留 dispatcher owner 内部路径。
6. 本次只更新方案和实现测试；用户文档/changelog 留到发布变更时按最终 API 形态同步，避免把未发布协议写入公开说明。

### 9.1 文件级落地映射

| 文件/模块 | 当前未发布实现中的处理 |
|---|---|
| `internal/serve/config_state.go` | 从数据 holder + save helper 演进为受锁 config owner；实现 Snapshot/UpdateChannel/legacy UpdateFull、merge-patch 和持久化事务 |
| `internal/serve/run.go` | handler 只解析 HTTP/写响应；删除内联 lifecycle/config 编排；platform slice 迁入 supervisor；清除直接 `rt.cfg`/cron 字段访问 |
| `internal/serve/channels_api.go` | 保留登录状态机；登录成功只调用 `UpdateChannel()`，不再自行 save/reload/apply |
| `internal/serve/platform_supervisor.go` | 已新增；接管 candidate prepare、fingerprint、start/swap/stop、status 和 shutdown |
| `internal/serve/session_lifecycle.go` | 已新增；接管 Delete/Bind/Unbind/Transfer/Rotate、共享 `ChannelIdentityLocks`、runtime locks、cache/API pool 更新和事件；identity locker 注入 Dispatcher |
| `internal/serve/openaiapi/session_mgr.go` | `DeleteActiveSession` 降为 lifecycle service 使用的内部 runtime/pool primitive；不得再直接作为 HTTP 删除协调器 |
| `internal/serve/openaiapi/background_run_coordinator.go` | 恢复/reconnect/abandon 接入同一 runtime lock 规则，不修改 Responses provider 协议逻辑 |
| `internal/serve/channels/dispatcher.go` | 在现有 runRoot/invalidated/tool state 上增加 lease、cached generation 和统一 `ChannelToolDefinition`；删除三套 hard-coded tool 条件 |
| `internal/session/runtime_lock.go` | 保留现有锁域；补排序后的多 session try-lock helper，供 Transfer 使用 |
| `internal/session/bindings.go` | 保留 SQL 事务和 generation 同事务递增；不在仓储层引入 serve/dispatcher 依赖 |
| `internal/session/migrations.go` | 按 3.3 决定原位修订未发布的 21/22，不追加补丁版本；补 fresh/upgrade/idempotent migration tests |
| `internal/session/session.go` | 抽取集中删除清单/helper，保留当前已加入的 tool/generation/Responses 表 |
| `internal/messaging/wechat/wechat.go` | 保留消息处理与最终发送不受 poll cancel 影响的行为；Agent run 的最终取消权仍归 Dispatcher runRoot |
| `ui/src/views/settings/Channels.svelte` | 保留新 endpoint；增加请求 generation、真实 tool 状态、`appliesTo` 和局部刷新 |
| `ui/src/views/Sessions.svelte` | 保留 running 禁用和显式确认；增加 unbind 成功/delete 失败的部分成功状态 |

禁止新增独立的 Responses lifecycle manager、第二个 platform map、第二套 tool generation 表或另一组 channel config endpoint。

## 10. 测试矩阵

| 场景 | 单元测试 | HTTP 集成测试 | race/并发测试 | WebUI 测试 |
|---|---:|---:|---:|---:|
| global/project/explicit 配置层写回 | 是 | 是 | 否 | 是 |
| CLI override 不持久化 | 是 | 是 | 否 | 否 |
| 微信登录成功复用 config transaction 且不持久化 Effective/CLI override | 是 | 是 | 是 | 是 |
| legacy full PUT 与 channel PATCH 串行且不丢更新 | 是 | 是 | 是 | 否 |
| 微信/飞书并发 PATCH 不发生 lost update | 是 | 是 | 是 | 否 |
| config prepare/persist/commit 任一步失败保持旧状态 | 是 | 是 | 是 | 否 |
| 单平台 diff restart | 是 | 是 | 是 | 是 |
| candidate platform 启动失败保留旧实例 | 是 | 是 | 是 | 否 |
| restart 不取消 active run | 是 | 是 | 是 | 否 |
| 删除 bound/running session 返回 409 | 是 | 是 | 是 | 是 |
| Responses submit/recover 与 delete/transfer 竞争 | 是 | 是 | 是 | 否 |
| 删除清理 tool 和所有关联行 | 是 | 是 | 否 | 否 |
| binding transfer 与 inbound message 竞争 | 是 | 是 | 是 | 是 |
| 排队 channel 请求跨 config/tool refresh 时重建 generation | 是 | 是 | 是 | 否 |
| tool unavailable/unknown/duplicate | 是 | 是 | 否 | 是 |
| catalog availability 与 Registry 实际注册一致 | 是 | 是 | 否 | 是 |
| tool generation 在下一次 run 生效 | 是 | 是 | 是 | 是 |
| WebUI tool/session 快速切换丢弃晚到响应 | 否 | 否 | 否 | 是 |
| unbind 成功但 delete 失败显示部分成功并刷新事实 | 否 | 是 | 是 | 是 |
| WebSocket 事件断线恢复 | 是 | 是 | 是 | 是 |
| fresh DB / schema 20 升级 / 已升级 DB 重开 | 是 | 否 | 否 | 否 |

聚焦命令建议：

```bash
go test ./internal/session ./internal/serve ./internal/serve/channels ./internal/serve/openaiapi
go test -race ./internal/session ./internal/serve ./internal/serve/channels ./internal/serve/openaiapi
(cd ui && node --test src/lib/*.test.js)
(cd ui && npm run build)
(cd ui && npm run e2e)
```

## 11. 完成标准

以下条件全部满足后，本整改才算完成：

1. 项目配置存在时，WebUI 修改 channel 后重启仍保持修改，且全局文件不变。
2. 修改、启停或重新登录一个 platform 不会重启另一个 platform。
3. platform 重启不会取消已经接受的 Agent run。
4. 不能直接删除 bound 或 running session；合法删除后不存在 dispatcher/API 缓存或数据库孤儿。
5. binding transfer 不写 serve config，也不影响 platform transport。
6. WebUI 展示的 tool effective/registered 状态与 Agent Registry 一致。
7. tool 更新不会关闭 active run 正在使用的 Registry/MCP client。
8. 配置、绑定、删除和 tool 更新在失败时保持旧持久化状态与旧 runtime 状态。
9. Responses 后台 submit、recover、cancel 与 session 生命周期操作之间不存在“远端仍运行、本地 lineage 已删除”的状态。
10. 已 resolve 或等待 runtime lock 的 channel 请求不会使用被失效/关闭的旧 generation。
11. 所有 `rt.cfg`、platform collection 和 config state 访问通过受锁 snapshot/supervisor，race 测试无报告。
12. channel-only 配置更新不会重建 API/Responses background runtime，也不会无条件重建 dispatcher provider。
13. 未发布的 migration 21/22 已按第 3.3 节原位修订，schema version 未无故增加，fresh/upgrade/reopen 测试全部通过。
14. 新服务接管后不存在 legacy handler/save/cache 编排的旁路或双写。
15. 聚焦测试、race 测试、WebUI JS/build 和 Chromium E2E 全部通过。

## 12. 需要产品确认的决策

1. WebUI 默认编辑层是否采用“项目文件存在则编辑项目，否则编辑全局”；本方案建议采用。
2. 删除 bound session 是否始终要求先解绑；本方案建议要求显式解绑。
3. binding transfer 是否允许 source 有 active run；本方案建议拒绝并返回 `409`。
4. tool 是否跟随 session 还是跟随 channel identity；本方案建议继续跟随 session。
5. unavailable tool 是拒绝保存，还是允许保存 requested 状态等待未来可用；第一阶段建议拒绝，后续如有明确需求再支持 desired-state 模式。
6. 是否提供管理员强制删除 Responses active session；本方案第一阶段建议不提供，只允许显式 cancel 并等待终态后普通删除。
