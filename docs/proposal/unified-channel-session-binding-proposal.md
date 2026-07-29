# 统一主 Session 与微信/飞书绑定方案

> 状态：Proposal
> 日期：2026-07-28
> 范围：Serve、WebUI、微信通道、飞书通道、主 `sessions.db`

## 1. 背景与目标

当前普通 Serve/WebUI session 使用主 session 数据库（`<sessionDir>/sessions.db`），而微信和飞书通道由 `internal/serve/channels.Dispatcher` 按用户创建独立的 `active.db`：

```text
<sessionDir>/channels/wechat/<user>/active.db
<sessionDir>/channels/feishu/<open_id>/active.db
```

这造成几个问题：

- WebUI 的 session 列表看不到或不能直接管理微信/飞书 session。
- 同一个工作上下文可能被普通 WebUI、微信、飞书分别复制成多个 session。
- 通道用户与主 session 之间没有显式绑定关系。
- 服务重启、迁移、删除和归档需要同时考虑两套存储。
- WebUI 对话框无法展示当前 session 是普通会话、微信绑定会话还是飞书绑定会话。

目标：

1. 微信、飞书对话统一落入主 `sessions.db`。
2. 普通 session、微信 session、飞书 session 使用同一套 session API 和生命周期。
3. 支持多个微信用户、多个飞书用户分别绑定到不同主 session。
4. WebUI 能创建、绑定、解绑、切换和管理通道 session。
5. WebUI Chat 显示当前 session 的绑定状态。
6. 保留现有消息通道的安全隔离、并发串行和 `/new` 语义。

非目标：

- 本提案不改变微信 iLink 登录凭据的存储方式。
- 不让不同用户默认共享同一个 session。
- 不把飞书的 app secret 或微信 bot token 展示到 WebUI。
- 不在第一阶段引入跨用户共享会话或复杂的群聊成员协作模型。

## 2. 当前实现摘要

### 2.1 普通 session

`internal/session` 已经使用共享的 `sessions.db`，核心表包括：

```sql
sessions(id, cwd, timestamp, parent_session, version)
entries(...)
request_stats(...)
session_capabilities(...)
```

session 文件路径目前主要是虚拟路径；真实持久化数据在主数据库中。`session.Manager` 通过 session ID 定位主库中的 `sessions` 和 `entries`。

### 2.2 微信和飞书 session

通道 dispatcher 以以下键作为内存缓存键：

```text
channels/<platform>/<user_id>
```

微信的 `user_id` 是 `wire.FromUserID`，飞书的 `user_id` 是发送者 `OpenID`。首次收到消息时，dispatcher 创建：

```text
<sessionDir>/channels/<platform>/<safe-user-id>/active.db
```

后续消息加载或复用这个 `active.db`。`ChannelSession.Manager` 负责持久化消息，内存 `sessions` map 只负责运行期缓存。

### 2.3 通道平台状态

微信登录凭据仍在：

```text
<configDir>/wechat-credentials.json
```

飞书使用 `serve.json` 的 `app_id` / `app_secret` 建立 WebSocket 长连接，没有单独的登录 session 文件。

微信的 `context_token` 和轮询 cursor、飞书的 WebSocket 连接状态均为运行时状态，不应迁移到 Agent 对话 session。

## 3. 核心数据模型

### 3.1 推荐的两个主 session 字段

为满足“主 session 增加两个字段”，在 `sessions` 表增加以下两个非空字段（不是可空字段；使用默认值兼容旧行）：

```sql
ALTER TABLE sessions ADD COLUMN channel_type TEXT NOT NULL DEFAULT 'local';
ALTER TABLE sessions ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
```

语义：

| `channel_type` | `channel_id` | 含义 |
|---|---|---|
| `local` | `''` | 普通 CLI / TUI / WebUI session |
| `wechat` | 微信 `UserID` | 绑定一个微信用户 |
| `feishu` | 飞书 `OpenID` | 绑定一个飞书用户 |

增加约束（如果 SQLite 的表结构 migration 采用表重建）：

```sql
CHECK (
  (channel_type = 'local' AND channel_id = '') OR
  (channel_type IN ('wechat', 'feishu') AND channel_id <> '')
)
```

如果采用 `ALTER TABLE ADD COLUMN` 而不重建 `sessions`，则不能声称已经有上述表级 `CHECK`；必须由绑定仓储层做同等校验，并在 migration 后增加约束校验测试。第一版更推荐不为新增字段做表重建，而是使用默认值 + 应用层校验 + 部分唯一索引，避免触碰已有 session 外键和数据。

建议增加唯一索引：

```sql
CREATE UNIQUE INDEX idx_sessions_wechat_binding
ON sessions(channel_type, channel_id)
WHERE channel_type = 'wechat' AND channel_id <> '';

CREATE UNIQUE INDEX idx_sessions_feishu_binding
ON sessions(channel_type, channel_id)
WHERE channel_type = 'feishu' AND channel_id <> '';
```

这样可以支持：

```text
wechat/user-a  -> session-1
wechat/user-b  -> session-2
feishu/openid-a -> session-3
feishu/openid-b -> session-4
```

同一平台身份最多绑定一个主 session，避免消息随机进入多个上下文；多个身份可以绑定多个不同 session。第一阶段的应用层规则还应禁止一个 session 从 `local` 直接同时变成多个通道绑定，或同时出现跨平台绑定；如果未来需要多对一绑定，应改用独立 `session_bindings` 表，而不是依靠这两个字段扩展。

### 3.2 为什么不直接增加 `wechat_id` 和 `feishu_id`

直接在 `sessions` 中增加 `wechat_id`、`feishu_id` 看似直观，但会产生以下问题：

- 一个 session 可能同时出现两个平台身份，语义不清晰。
- 后续增加 Telegram、钉钉、Slack 等平台需要继续改表。
- 查询和唯一性约束需要为每个平台重复实现。
- `channel_type + channel_id` 可以明确“一行代表一个外部绑定”。

如果产品最终要求“一个主 session 同时绑定多个微信/飞书身份”，则两个字段不足以表达多对一关系，应在第二阶段升级为独立 `session_bindings` 表；不建议用逗号分隔或 JSON 存储多个身份。

### 3.3 可选的展示字段

当前 session 标题存储在 `entries` 的 `session_info` 中，不建议为了 UI 再修改 header。API 返回时增加派生字段：

```json
{
  "id": "abc12345",
  "title": "项目会话",
  "channelType": "wechat",
  "channelId": "wxid_xxx",
  "channelLabel": "微信 · wxid_xxx",
  "isBound": true
}
```

`channelId` 对 WebUI 可做脱敏展示，但后端操作必须使用真实值。

## 4. 迁移策略

本节的“迁移”仅指主 `sessions.db` 的数据库表结构 migration，不包含旧通道 session 数据迁移。旧 `active.db` 不在本次方案的数据范围内。

### 4.1 数据库迁移

项目规则要求 schema 变更追加 migration，不直接在新代码中使用 `CREATE TABLE IF NOT EXISTS` 或启动时散落 `ALTER TABLE`。应新增一次 migration：


```text
sessions.channel_type
sessions.channel_id
唯一索引
```

旧数据统一得到：

```text
channel_type = 'local'
channel_id = ''
```

迁移必须具备：

- 幂等执行
- 事务包裹
- 旧数据库可回滚失败
- 对已有 session、entries、stats、cron 关系无影响
- 为重复的非空绑定返回明确错误，而不是静默覆盖

### 4.2 本次不迁移旧通道数据

本方案只做主 `sessions.db` 的表结构 migration，不迁移历史通道数据：

- 不扫描旧的 `channels/wechat/*/active.db` 或 `channels/feishu/*/active.db`。
- 不将旧 `active.db` 的 header、entries 或 seq 导入主库。
- 不删除、重命名或改写旧 `active.db`。
- 不做旧 session ID 重映射，也不建立旧数据导入 marker。

旧通道数据作为历史遗留数据保留在原位置，不纳入新的主库 session 列表和绑定关系。新版本通道只对新创建的绑定 session 使用主 `sessions.db`。如果未来需要恢复旧通道历史，应另立独立的数据导入方案，不能混入本次 schema migration。

### 4.3 新旧运行时边界

表结构 migration 完成后，新的微信/飞书身份没有主库绑定时，创建新的主库 session；不会自动读取或合并旧 `active.db`。为避免同一身份出现两个写入目标，本次切换必须明确：

- 新通道路由只写主 `sessions.db`。
- 旧 `active.db` 只保留为离线历史文件，不再由新 dispatcher 打开写入。
- 不允许主库 session 与旧 `active.db` 双写。
- 若产品需要继续访问旧历史，必须通过单独的只读查看/导入工具实现。

## 5. Dispatcher 运行时设计

### 5.1 统一 session 解析

将通道 dispatcher 的 `resolveSession(platform, userID)` 改为：

```text
resolveBoundSession(platform, identity, workDir)
```

流程：

1. 校验平台和用户白名单。
2. 规范化身份：
   - 微信：`FromUserID`
   - 飞书：`OpenID`
3. 查询主库：
   ```sql
   SELECT id FROM sessions
   WHERE channel_type = ? AND channel_id = ?
   ```
4. 如果找到，加载该主 session，放入内存缓存。
5. 如果没有，按策略创建或要求先绑定：
   - 通道默认采用“自动创建并绑定”，保持当前行为。
   - WebUI 手动绑定时采用“绑定已有 session”或“创建并绑定”。
6. 将 `ChannelSession.ID` 改为主 session ID，不再使用 `channels/<platform>/<user>` 作为伪 ID。
7. `ChannelSession.Platform` 和 `UserID` 保留作为运行时路由信息。

同一主 session 如果被错误配置成多个身份绑定，后端必须在绑定查询和消息执行处都按锁/事务保护，不能依赖内存 map 防止竞争。

### 5.2 并发与路由

- 单个主 session 仍然只能串行处理 Agent 请求。
- 绑定同一 session 的多个通道身份如果未来被允许，必须共享同一个 session 锁。
- 第一阶段唯一索引保证一个平台身份只有一个 session；由于两个字段位于 `sessions` 单行内，一个 session 只能保存一个当前绑定。绑定事务必须拒绝覆盖已有通道绑定，避免跨平台或多身份共享同一 session。
- `context_token`、飞书 reply message ID、WebSocket 连接句柄仍只在通道运行时保存，不写入主 session。
- 回复消息仍使用入站事件的原始 `ChatID`，不能从 session 绑定字段推断聊天目标。

### 5.3 `/new` 语义

由于本阶段只有 `channel_type` / `channel_id` 两个字段，没有独立的 `active` 或 `archived` 字段，绑定字段应表示“当前有效绑定”，不表示历史来源。

通道发送 `/new` 时：

1. 在同一主库事务中校验当前身份仍绑定当前 session。
2. 将旧 session 的 `channel_type` 重置为 `local`、`channel_id` 重置为空；旧对话历史保留，但历史列表不再显示为当前微信/飞书绑定。
3. 创建新的 `channel_type/channel_id` session。
4. 将当前身份唯一绑定到新 session。
5. 更新内存 runtime registry，确保下一条消息只进入新 session。

如果产品要求旧 session 在历史列表中永久显示“曾经绑定微信/飞书”，两个字段不足以表达“当前绑定”和“历史来源”，应另加 session binding history 表或事件；本阶段不隐式扩展字段。

WebUI 发送“新建会话”时，默认创建 `local` session；如果从通道绑定管理入口创建，则创建对应平台类型的 session。

## 6. WebUI API 设计

现有 `/api/sessions` 返回普通 session 列表，需要扩展为包含通道字段。保持现有字段兼容，新增：

```json
{
  "id": "abc12345",
  "workDir": "/workspace/project",
  "title": "项目会话",
  "channelType": "local",
  "channelId": "",
  "channelLabel": "普通",
  "bound": false
}
```

### 6.1 Session 列表

```http
GET /api/sessions?scope=all
```

可选过滤：

```text
channelType=local|wechat|feishu
bound=true|false
```

### 6.2 查询绑定

```http
GET /api/session-bindings
```

返回：

```json
{
  "bindings": [
    {
      "sessionId": "abc12345",
      "channelType": "wechat",
      "channelId": "wxid_xxx",
      "displayId": "wxid_…xxx",
      "workDir": "/workspace/project",
      "title": "微信项目会话",
      "active": true
    }
  ]
}
```

### 6.3 绑定已有 session

```http
POST /api/sessions/{id}/bindings
Content-Type: application/json

{
  "channelType": "wechat",
  "channelId": "wxid_xxx"
}
```

规则：

- `local` session 不能携带 `channelId`；绑定已有普通 session 时，服务端在事务内把该 session 从 `local/''` 更新为指定的 `wechat/<id>` 或 `feishu/<id>`。
- `channelType` 只能是 `wechat` 或 `feishu`。
- 相同平台和身份已经绑定其他 session 时返回 `409 Conflict`。
- 目标 session 正在运行时允许绑定，但必须保证下一条消息不会绕过新的映射。
- 绑定接口不能接受微信 token、飞书 app secret 等凭据。

### 6.4 解绑

```http
DELETE /api/sessions/{id}/bindings/{channelType}/{channelId}
```

解绑只移除映射，不删除对话历史。解绑后原 session 仍可在 WebUI 中打开。

### 6.5 切换/转移绑定

建议提供原子接口，避免“先解绑再绑定”造成消息短暂落入自动新建 session：

```http
POST /api/session-bindings/transfer

{
  "channelType": "feishu",
  "channelId": "ou_xxx",
  "fromSessionId": "old-id",
  "toSessionId": "new-id"
}
```

服务端在同一事务中校验旧绑定、目标 session 和唯一性，然后完成转移。

## 7. WebUI 设计

### 7.1 Session 列表

Session 列表增加“来源/绑定”列或 badge：

- `普通`
- `微信`
- `飞书`
- `微信 · wxid_…xxx`
- `飞书 · ou_…xxx`

过滤器增加：

```text
全部 / 普通 / 微信 / 飞书 / 已绑定 / 未绑定
```

默认隐藏完整外部 ID，详情或复制操作才显示完整值；不显示任何凭据。

### 7.2 Chat 对话框

Chat 顶部 session 信息区域显示：

```text
项目会话 · 普通
项目会话 · 微信 · wxid_…xxx
项目会话 · 飞书 · ou_…xxx
```

建议使用颜色和图标区分，但同时保留文字，避免仅依赖颜色：

- 普通：终端/对话图标
- 微信：微信图标或 `WX` 文本标识
- 飞书：飞书图标或 `FS` 文本标识

未绑定的 WebUI session 显示“普通 · 未绑定”。

### 7.3 Session 管理入口

Session 详情抽屉或菜单提供：

- 修改标题
- 查看绑定状态
- 绑定微信身份
- 绑定飞书身份
- 解除绑定
- 转移到另一个 session
- 创建新的通道 session
- `/new` 后保留历史 session
- 删除 session（删除前明确提示会话历史是否永久删除）

新增 UI 文案必须同时加入 `ui/src/lib/preferences.js` 的中文和英文语言表。

### 7.4 通道身份来源

WebUI 不能猜测或枚举任意微信/飞书身份。推荐身份来源：

1. 当前服务已经收到过的安全审计记录/已知身份列表；或
2. 管理员手动输入完整 `OpenID` / 微信 `UserID`；或
3. 通道发送一次带有一次性绑定码的命令，例如 `/bind <code>`。

推荐使用一次性绑定码：

- WebUI 创建短期绑定码。
- 用户在微信/飞书通道发送 `/bind <code>`。
- 服务端验证身份和过期时间后完成绑定。
- 绑定码单次使用，短期过期，不能在日志中打印。

这样避免管理员手工输入错误身份，也避免任意知道 ID 的人抢占绑定。

## 8. 权限和安全

必须区分以下权限：

- WebUI/API 管理员：可以管理 session 和绑定。
- 通道用户：只能使用自身绑定的 session，不能查看其他用户绑定关系。
- 通道消息中的 `/sessions`：只返回当前身份可见的 session，不返回全局 session 列表。

安全要求：

- API 绑定接口沿用 Serve Bearer token 认证。
- 绑定/解绑/转移操作记录审计日志，至少包含操作者、平台、脱敏身份、旧 session、新 session、时间和结果。
- 外部身份作为不可信输入，必须复用现有安全路径组件规范化逻辑；数据库查询使用参数化 SQL。
- 禁止通过 `channel_id` 拼接文件路径。
- 删除 session 前检查是否仍有绑定；默认要求先解绑或执行显式“删除并解除绑定”。
- 绑定变更不应暴露 app secret、bot token、微信 context token 或飞书连接凭据。

## 9. 实施阶段

### Phase 1：主库字段和 session API

- 添加 migration。
- 扩展 `Header`/`SessionDetail`/`ActiveSessionInfo` 的通道元数据读取和返回。
- 增加主库绑定查询、创建、更新、删除和原子转移接口。
- 为普通 session 明确返回 `channelType=local`。

### Phase 2：通道统一落主库

- 抽取通用的 `ResolveOrCreateBoundSession`。
- 微信、飞书 dispatcher 改为使用主库 session。
- 不迁移旧 `active.db` 数据；旧文件保留，不读取、不写入、不删除。
- 更新 `/new`、`/clear`、`/sessions` 行为。
- 增加多用户、多平台、并发和重启测试。

### Phase 3：WebUI 管理

- `/api/session-bindings` 及绑定/解绑/转移接口。
- Sessions 列表增加来源和绑定状态。
- Chat 顶部显示来源 badge。
- Session 详情增加绑定管理。
- 中文/英文翻译同步更新。

### Phase 4：绑定码和管理收尾

- 增加一次性绑定码流程。
- 提供绑定状态和冲突操作的管理接口。
- 旧 `channels/*/active.db` 不纳入新 session 列表；如需历史恢复，另立独立只读/导入方案。

## 10. 测试计划

### 数据库

- 空库执行 migration。
- 已有主库执行 migration，所有旧 session 默认为 `local`。
- 相同微信身份重复绑定返回冲突。
- 相同飞书 OpenID 重复绑定返回冲突。
- 解绑后可重新绑定。
- 转移接口事务失败时旧绑定不丢失。
- 并发绑定同一身份只有一个成功。

### 通道

- 微信用户 A/B 分别进入不同主 session。
- 飞书 OpenID A/B 分别进入不同主 session。
- 新主库 session 可创建并正确写入消息。
- 服务重启后继续使用原主库 session。
- 旧 `active.db` 不被自动读取、导入或删除。
- 微信 context token 不写入主库。
- 飞书回复仍使用原始 `ChatID`。
- `/new` 归档旧 session 并更新绑定。
- 同一主 session 请求仍然串行。

### WebUI/API

- Session 列表显示 local/wechat/feishu。
- Chat 选择不同 session 时 badge 正确刷新。
- 绑定、解绑、转移的成功和 `409` 错误提示正确。
- 移动端 session 详情和绑定操作可用。
- 中文和英文文案完整。
- 无 token、secret、context token 泄露。

## 11. 未决问题与建议决策

### Q1：是否允许一个 session 同时绑定微信和飞书？

建议第一阶段禁止。一个 session 只允许一种 `channel_type`，避免普通用户在微信和飞书看到混合上下文，也使权限和审计更清晰。未来如果确实需要跨平台共享，再引入 `session_bindings` 多对一表。

### Q2：是否允许多个身份绑定同一个 session？

建议第一阶段禁止。用户需求中的“多飞书/多微信”解释为多个外部身份分别绑定多个不同 session。若未来需要共享，应使用独立绑定表并增加用户级隔离策略，而不是继续扩展两个字段。

### Q3：WebUI 是否允许手动输入身份 ID？

可以作为管理员兜底能力，但默认推荐一次性绑定码，以减少误绑和冒用风险。

### Q4：普通 WebUI session 是否可转换为通道 session？

可以。转换本质上是一次带唯一性校验的字段更新；但应在 UI 中明确提示：该 session 后续会成为指定通道身份的上下文，不再是普通默认会话。

## 12. 实现前必须解决的接口与一致性问题

本提案不能只增加 SQL 字段；`session.Manager` 当前加载和创建 session 的 SQL 也必须同步扩展，否则字段会存在于表中但不会进入 `Header`/运行时对象：

- `Header` 增加只读的通道元数据，或由独立的 session metadata 查询返回；不能让 `Header.Version` 承担通道类型含义。
- `initWithIDLocked()` 注册新 session 时，必须在同一事务中写入 `channel_type`、`channel_id`。
- `load()` / `openSessionFromDB()` 必须读取通道字段，并校验 NULL、平台值和 ID 空值规则。
- `ListForDir`、`ListAll`、`ListAllDetailed` 及 Serve 的 `ActiveSessionInfo` 必须从主库返回通道元数据。
- 新增 `NewBound`/`InitWithBinding` 或等价的 session 包 API，让 dispatcher 不需要直接写 `sessions` 表。
- 绑定已有 `local` session、解绑、转移和 `/new` 必须使用 session 包提供的事务 API，不能由 WebUI 或 dispatcher 拼接 SQL。
- session handle 仍是兼容层；主库 session 解析必须支持没有物理 handle 文件的通道 session，且不能再创建 `channels/<platform>/<user>/active.db`。

### 12.1 绑定语义必须固定

本阶段两个字段表示“当前唯一绑定”，不是绑定历史：

- `local/''`：普通 session。
- `wechat/<id>` 或 `feishu/<id>`：当前通道绑定 session。
- 解绑后恢复为 `local/''`，不保留旧平台 badge。
- `/new` 先原子地解除旧 session，再创建并绑定新 session；旧 session 保留历史消息但显示为普通 session。
- 一个 session 不能同时绑定两个平台，也不能同时绑定多个身份。若要支持多对一，必须改成独立绑定表。

WebUI 的绑定已有 session 只能绑定符合目标状态的 session：普通 session `local/''` 可以转为单个通道绑定；已有通道绑定的 session 必须先解绑，不能静默覆盖。
## 13. 并发写入与主库序列号约束（补充要求）

统一到主 `sessions.db` 后，不能把“每个通道一个 `active.db`”简单替换成多个 goroutine 直接写同一个数据库。现有实现已经包含重要的 SQLite 保护，方案必须复用而不是绕过：

- `cachedDB()` 按规范化数据库路径缓存单一 `*sql.DB`。
- `openSQLiteDB()` 设置 `SetMaxOpenConns(1)`，当前进程内同一主库使用单连接串行化 SQL 操作。
- SQLite DSN 使用 `busy_timeout(10000)`、`_txlock=immediate` 和 WAL。
- `Manager` 的写入方法先持有自身 mutex，再通过 `withDB()` 写库。
- `writeEntry()` 使用事务，并校验当前 leaf，防止旧 Manager 覆盖其他写入。

### 13.1 不破坏现有写入路径

通道 session 改造必须继续使用 `session.Manager` 的公开写入方法：

```text
AppendMessage
AppendModelChange
AppendThinkingLevelChange
AppendCompaction
AppendSessionInfo
AppendLabel
RecordUsage / session event API
```

禁止在 dispatcher、WebUI handler 或迁移器中直接向 `entries` 写入普通消息。禁止为每个消息重新 `sql.Open` 一个主库连接。禁止将主库连接池改成多连接，除非先完成全局 SQLite 并发设计和测试。

`ChannelSession` 和 `APISession` 最终指向同一个 `session.Manager`/session ID 时，必须共享同一个进程内 session 锁。不能只依赖两个不同 Manager 各自的 mutex；否则它们可能都读到同一个旧 leaf，并产生 `ErrSessionModified` 或重复/乱序请求。

推荐新增进程级 `session.RuntimeRegistry`（名称可调整）：

```text
key: session root + session ID
value: shared runtime record
       - *session.Manager
       - request/agent mutex
       - active users/channels
```

普通 WebUI、OpenAI API、微信、飞书解析到 session ID 后，都从 registry 获取同一个 runtime record。已有 `SessionPool` 和 channels dispatcher 的缓存需要通过统一 registry 或适配层合并；不能让每个入口各维护一份相互不知道的锁。

在统一 registry 完成前，过渡实现至少要在 session 包提供按 canonical session ID 的锁表，所有入口在执行一次 Agent turn 前锁住该 key。锁必须覆盖：

1. 读取当前消息状态；
2. Agent 执行期间的 append/compaction/event 写入；
3. 更新运行时状态；
4. 释放前的最后一次持久化。

锁不能覆盖跨 session 的绑定管理事务，也不能用用户 ID 作为锁 key；绑定到同一个 session 的不同身份必须竞争同一个 session 锁。

### 13.2 SQLite 连接与事务边界

绑定关系变更、创建通道 session、归档旧 session 的数据库部分应使用主库事务：

```text
BEGIN IMMEDIATE
  查询/校验身份唯一绑定
  查询/校验目标 session
  INSERT/UPDATE channel_type、channel_id
COMMIT
```

绑定 API 不应持有 Agent session mutex 后再等待数据库长事务，避免形成锁顺序反转。统一锁顺序为：

```text
绑定/路由锁 -> 主库事务 -> session runtime 锁（仅在确实需要时）
Agent 写入：session runtime 锁 -> 主库短事务
```

实际实现应避免同时持有两类锁；如必须同时持有，加入死锁测试和明确的锁顺序。

SQLite 的 `seq INTEGER PRIMARY KEY AUTOINCREMENT` 是主库全局 `entries` 序列，不是每个 session 独立序列。统一到主库后必须接受：

- 一个 session 的 entry `seq` 可能不连续；
- 微信、飞书、WebUI 并发写入时，seq 按数据库提交顺序分配；
- 不能用 `seq` 推断同一 session 的用户/助手消息连续性；
- `parent_id` / leaf 才是 session 分支和写入一致性的逻辑依据；
- WebUI 增量读取必须使用 `seq > cursor`，并正确处理其他 session 插入造成的空洞。

本次不迁移旧 `active.db` 数据，也不复制旧库 seq。新建的主库通道 session 由 SQLite 正常分配全局 seq。

### 13.3 写入失败和重试

- `ErrSessionModified` 不能盲目重试同一个已构造的 entry；必须重新打开/刷新 Manager 状态，再由上层决定是否重新执行 Agent turn。
- SQLite `BUSY`/`LOCKED` 使用现有 busy timeout；若增加重试，只能对明确的事务边界重试，且必须保证 Agent 工具副作用不会重复执行。
- Agent turn 的消息 append 和 usage/event 写入失败必须向调用方报告，不能仅记录日志后继续发送看似成功的通道回复。
- 通道收到重复事件时，不能依据主库 seq 去重；应使用平台事件 ID 的独立去重机制，后续可增加 channel event id 表或短期去重缓存。

## 14. 数据库迁移顺序、版本和序列号（补充要求）

### 14.1 先确认当前代码的迁移基线

当前 `internal/session` 的实际实现是：空数据库由 `EnsureCurrentSchema()` 初始化；已有数据库通过 `requiredSchema` 校验字段，缺字段会报 incompatible。当前代码树没有可直接追加业务 migration 的 `migrations.go` / `ApplyMigrations()` 实现，因此本功能不能假设“追加 migration”已经存在。

实施前必须先做一个独立的 migration 基础设施改造，或严格按项目最新 migration 规范接入；不能在 `openSQLiteDB()` 中散落 `ALTER TABLE`，也不能依靠提高 `CurrentVersion` 让旧数据库自动升级。`Header.Version` 是 session 数据格式版本，不应充当数据库 schema migration 版本。

特别要固定数据库打开顺序：

```text
打开 SQLite 连接
→ 仅对空库执行基础 schema 初始化
→ 执行 schema migrations（按版本升序、事务内）
→ 执行 requiredSchema / 结构校验
→ 对外提供 session.Manager 和业务读写
```

不能先对旧库执行包含新字段的 `requiredSchema` 校验，否则旧库会在 migration 之前直接失败；也不能让任何业务 goroutine 在 migration 完成前打开或写入 session。空库的基础 schema 应直接包含最新字段，旧库则由对应 migration 补字段，避免空库被重复 migration。

### 14.2 推荐的严格迁移顺序

按以下顺序实施，步骤之间每一步都可单独回滚/验证：

**Migration N：建立/接入 schema migration runner**

1. 在主库事务内创建 `schema_migrations` 表（仅迁移基础设施负责）。
2. 每个 migration 使用单调递增的整数 ID 和固定名称。
3. 启动时按 ID 升序执行未完成 migration。
4. 每个 migration 在一个事务内完成；成功提交后再记录 migration ID。
5. 已记录的 migration 不重复执行；执行中进程退出时，下次从未记录的 ID 重试。
6. migration runner 在任何 session 读写前完成，避免半升级 schema 被业务线程使用。

**Migration N+1：增加 session 通道字段**

1. 获取主库迁移锁/写锁。
2. 在事务中增加 `channel_type`、`channel_id`，默认值为 `local` / 空字符串。
3. 在事务中创建两个部分唯一索引。
4. 提交后更新 `requiredSchema` 检查列表。
5. 只有该 migration 成功后，业务代码才允许读写通道字段。
6. migration 完成后执行数据校验：所有旧行必须是 `local/''`，字段值不允许 NULL。

本阶段不要求重建 `sessions` 表。因为 SQLite 的 `ALTER TABLE ADD COLUMN` 很难为已有表安全补充复杂 `CHECK`，第一版采用非空默认值、绑定仓储层校验和部分唯一索引；如果未来必须把 CHECK 放入数据库，再单独设计表重建 migration。

**代码发布/路由切换（不是数据库 migration）**

在 N+1 成功后，再发布使用通道字段的 dispatcher 和 API 代码：

1. 新建微信/飞书身份时，在主库 `sessions` 中创建新 session 并写入绑定字段。
2. 新 dispatcher 只打开主库 session，不读取或写入旧 `active.db`。
3. 旧 `active.db` 保留为未迁移的历史文件；如果未来要导入，另立独立的数据迁移方案。
4. 切换时保证单个身份只有一个写入目标，不允许主库和旧 `active.db` 双写。

### 14.3 迁移期间的序列号与校验

本次 schema migration 不导入旧通道数据，因此不涉及旧 `active.db` 的 seq 重排或复制。需要校验的是：

- 空库基础 schema 包含两个新字段和索引；
- 旧主库执行字段 migration 后，`sessions` 行数、`entries` 行数、`entries.seq` 最大值和已有引用关系不变；
- 旧 session 全部得到 `channel_type=local`、`channel_id=''` 的兼容默认值，且两个字段不为 NULL；
- 新建通道 session 的 entry 由主库自动分配全局 seq；
- `seq` 不作为单个 session 的连续性判断依据；
- WebUI 增量读取继续使用 `seq > cursor`，允许跳过其他 session 的记录；
- 新增唯一索引建立后，不存在重复的非空平台身份绑定。

如果未来另行实现旧数据导入，必须使用独立 proposal 和独立数据迁移任务，不能追加到本次 schema migration。

### 14.4 与现有处理的兼容边界

本方案不改变以下既有语义：

- 主库仍由 `session.OpenRootDB`/`cachedDB` 管理；
- `SetMaxOpenConns(1)`、WAL、busy timeout 和 `_txlock=immediate` 保留；
- session entry 仍通过 `Manager` 写入并校验 leaf；
- `seq` 仍是主库全局自增游标；
- WebUI 的增量事件游标继续按 `seq` 查询并允许跳过其他 session 的行；
- session 数据格式 `Header.Version` 不因本数据库改造随意提升；
- 普通 CLI/TUI/API session 的创建、恢复、删除和 compaction 流程不改；
- 通道的微信 context token、飞书 ChatID/reply ID 等运行时状态不写入 session 表。

任何无法满足以上兼容边界的实现，应先拆分为独立 proposal，而不是在通道绑定功能中顺手重构 session 存储。
## 15. 推荐结论

推荐采用“主 `sessions.db` + `sessions.channel_type/channel_id` 两字段 + 单身份唯一约束”的第一阶段方案，但必须以第 12、13、14 节的接口、并发和迁移顺序为前置条件：


- 改动范围小，与现有主库结构和 WebUI session API 兼容。
- 不再维护通道专用 `active.db`。
- 可通过后续 `session_bindings` 表平滑演进到多身份绑定同一 session。
- 所有绑定关系、历史消息和 WebUI 管理入口统一进入主 session 生命周期。
