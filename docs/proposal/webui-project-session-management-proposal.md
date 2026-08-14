# WebUI 项目与 Session 管理方案设计

> 状态: Proposal
> 日期: 2026-08-12
> 目标版本: 待定

## 1. 概述

当前 MothX WebUI 左侧栏以扁平的“历史对话”列表展示 Session。随着 Session 增多，用户无法将同一工作目标下的多个对话归类，也缺少会话置顶、手动重命名和移动归档等基本管理能力。

本提案参考豆包 Web 端当前的会话信息架构，将 WebUI 左栏从单一历史列表调整为以**项目、最近、未归类会话**为核心的可折叠工作区，并为 Session 提供三点菜单管理入口。

本提案仅定义产品交互、持久化模型、API 与实施边界；在方案确认前不应继续功能开发。

## 2. 调研结论：豆包左侧会话管理

已实际检查 `https://www.doubao.com/chat/?from_login=1` 当前页面及其页面结构。

### 2.1 信息架构

豆包不是将所有会话放在一个按时间排序的列表中，而是并列维护三类数据：

1. **项目（projects）**：项目下包含项目内会话；空项目仍保留并显示“暂无对话”。
2. **最近（recent conversations）**：跨项目的最近使用会话快捷入口。
3. **非项目会话（non-project conversations）**：未归属项目的普通会话列表。

页面加载数据中可观察到独立的 `projects`、`recentConversations`、`nonProjectConversations` 字段，说明“最近”并非前端对单一会话列表的临时视觉过滤，而是独立的产品视图。

### 2.2 左栏交互规则

- “项目”和“最近”是独立 section，标题行可点击折叠/展开。
- “项目”标题行悬停时显示“新建项目”按钮；默认不常驻显示，保持界面干净。
- 每个项目可折叠，展开后显示其内部会话；项目行本身有独立管理入口。
- 会话采用紧凑单行布局，点击会话主体进入对话。
- 管理功能不占会话行的常驻空间：会话行 hover 或键盘聚焦时才显示三点菜单。
- section 标题及会话项高度约 32px；图标、文字、辅助操作均使用低对比度风格。

### 2.3 可借鉴而不直接复制的原则

MothX 应沿用上述“分组优先、会话操作延后显现”的方式，但保留自身产品语义：工作目录、运行状态、消息通道绑定、Agent 执行状态与删除保护规则不能丢失。

## 3. 目标与非目标

### 3.1 目标

1. 项目作为 Session 的持久化组织形式。
2. 左栏同时展示项目、最近和未归类会话，并支持独立折叠。
3. Session 支持置顶、重命名、移动至项目、移出项目与删除。
4. 项目支持新建、重命名、折叠和删除。
5. 所有会话管理数据刷新后仍保留，不能仅依赖浏览器内存。
6. 保持现有运行中 Session、通道绑定会话和删除确认行为。
7. 提供键盘可访问的菜单、折叠控件和焦点状态。

### 3.2 非目标

1. 本期不做项目协作、共享、权限或云同步。
2. 本期不把工作目录自动等同于项目；多个不同工作目录的 Session 可以归入同一个项目。
3. 本期不做 Session 拖拽排序；排序规则采用置顶优先、最近更新时间次之。
4. 本期不移除已有 `/sessions` 完整会话管理页面，只使其复用项目和 Session 元数据。
5. 本期不改变 Agent、SessionPool、通道 Session 的执行生命周期。

## 4. 建议的左栏结构

```text
[ 搜索会话                                           ⌘K ]
[ ✎ 新对话                                       ⇧⌘K ]

[ ▾ 项目                                           + ]
  [ ▾ ] MothX Web UI                               …
        [ 📌 ] 为会话补充项目管理                    …
        [    ] WebUI 样式优化                        …
  [ ▸ ] Serve API 重构                              …

[ ▾ 最近                                           ]
      [ 📌 ] 为会话补充项目管理                      …
      [    ] Redis Ring 模式                         …
      [    ] Provider 调试                           …

[ ▸ 全部对话 / 未归类                              ]
```

### 4.1 Section 规则

| Section | 内容 | 默认状态 | 说明 |
|---|---|---:|---|
| 项目 | 全部项目及其项目内 Session | 展开 | 标题悬停显示新建项目按钮 |
| 最近 | 最近使用的 10–15 个 Session，允许包含项目内 Session | 展开 | 快速入口，会话可与项目区重复出现 |
| 全部对话 / 未归类 | `project_id` 为空的 Session | 折叠 | 避免项目区与全部区重复展示 |

- 三个 section 的折叠状态保存到浏览器 `localStorage`。
- 单个项目的折叠状态也保存到 `localStorage`。
- 若当前打开的 Session 所属项目被折叠，切换到该 Session 时自动展开该项目。
- 搜索激活时，左栏仅显示匹配会话及其所在项目路径；搜索结果不重复渲染到“最近”和“未归类”中。

### 4.2 Session 排序

每个项目和未归类列表按以下规则排序：

1. 已置顶 Session 优先；
2. 同一置顶状态内按最后使用时间倒序；
3. 最后以 Session ID 作为稳定兜底排序。

“最近”按最后使用时间倒序，最多显示固定条数；置顶不改变“最近”的时间顺序。

## 5. Session 三点菜单

点击会话行的 `…` 后打开定位菜单。点击会话文本区域只打开 Session，不能触发管理操作。

```text
┌────────────────────┐
│ 置顶 / 取消置顶      │
│ 重命名               │
│ ────────────────── │
│ 移动到项目        ›  │
│ 移出项目             │  ← 仅项目内会话
│ ────────────────── │
│ 删除                 │
└────────────────────┘
```

### 5.1 操作语义

| 操作 | 行为 |
|---|---|
| 置顶 / 取消置顶 | 更新持久化 `pinned` 状态；置顶会话显示小图钉 |
| 重命名 | 弹出小型命名对话框或 inline 输入框；手动标题优先于自动生成标题 |
| 移动到项目 | 二级菜单列出现有项目，末尾提供“新建项目…” |
| 移出项目 | 仅清空项目关联，不删除 Session |
| 删除 | 保留现有通道绑定解绑确认逻辑；运行中 Session 禁止删除 |

菜单在以下情形关闭：点击外部、按 `Escape`、选择操作、打开另一个菜单或切换路由。

### 5.2 运行中与通道绑定 Session

- 运行中 Session：允许置顶、重命名、移动、移出项目；删除禁用，并给出明确原因。
- 已绑定 WeChat / Feishu 的 Session：允许管理元数据；删除仍先执行现有解绑确认流程。
- 外部通道生成的 Session 默认可归类，但 UI 应保留通道来源标识。

## 6. 项目操作

### 6.1 项目 Section

“项目”标题行 hover/focus 时显示 `+`：

- 点击后打开“新建项目”轻量对话框；只要求输入项目名称。
- 创建成功后项目显示在项目列表顶部，并可立即将当前会话移动到该项目（可选后续交互）。

### 6.2 单个项目菜单

项目行的 `…` 菜单提供：

```text
重命名
折叠 / 展开
删除项目
```

删除项目的语义必须为：

- 删除项目实体；
- 项目内所有 Session 自动移至“全部对话 / 未归类”；
- 不删除任何 Session 或消息；
- 删除前显示确认文案，明确告知上述行为。

## 7. 持久化数据模型

项目与 Session 管理数据必须存于共享 Session SQLite 数据库，不能仅存 localStorage。

```sql
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE session_metadata (
  session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  project_id TEXT REFERENCES projects(id) ON DELETE SET NULL,
  pinned INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL
);
```

### 7.1 字段规则

| 字段 | 规则 |
|---|---|
| `projects.name` | 去除首尾空白；不能为空；同名项目是否允许由实现阶段明确，建议允许 |
| `session_metadata.project_id` | 可空；设为空即移出项目 |
| `session_metadata.pinned` | 仅控制所属项目或未归类列表的排序 |
| `updated_at` | 用于项目与元数据同步、排序和调试 |

Session 已有持久化标题系统，本提案不重复保存标题字段：

- `session.SessionInfoEntry.Name` 已作为 Session 展示名称持久化；
- 管理 API 已将其返回为 `ActiveSessionInfo.Title`，前端现有 `session.title` 可直接使用；
- 重命名复用既有 `Manager.AppendSessionInfo(name)`，追加最新的 `session_info` 条目；
- 首条用户消息后的自动命名逻辑将与手动重命名一并重构为统一的标题策略；
- 自动命名只能在 Session 尚无标题时调度和写入，避免异步自动标题覆盖用户的手动重命名；
- 无标题时仍按既有回退逻辑展示首条用户消息预览或短 Session ID。

## 8. API 方案

### 8.1 项目 API

```text
GET    /api/projects
POST   /api/projects               { "name": "MothX Web UI" }
PATCH  /api/projects/{id}          { "name": "MothX UI" }
DELETE /api/projects/{id}
```

`DELETE /api/projects/{id}` 成功后返回 `204`，并将关联 Session 的 `project_id` 自动置空。

### 8.2 Session 元数据 API

```text
PATCH /api/sessions/{id}/metadata
{
  "projectId": "可选项目 ID；空字符串表示移出项目",
  "pinned": true
}

POST /api/sessions/{id}/title
{
  "title": "新的会话标题"
}
```

Metadata patch 语义必须区分“字段未提供”与“显式清空”：

- 未提供字段：不修改该字段；
- `projectId: ""`：移出项目；
- `pinned: false`：取消置顶。

标题接口规则：

- `title` 去除首尾空白后不能为空；
- 写入时复用 `AppendSessionInfo(title)`；
- 后续列表读取最新 `session_info` 条目，因此新标题会立即成为 Session 的 `title`；
- 首条自动命名逻辑必须通过统一标题策略判断是否允许执行与写入，防止覆盖用户刚完成的重命名。

### 8.3 Sidebar 聚合数据

建议新增：

```text
GET /api/sessions?view=sidebar
```

响应建议：

```json
{
  "projects": [
    {
      "id": "project-id",
      "name": "MothX Web UI",
      "sessions": []
    }
  ],
  "recentSessions": [],
  "unprojectedSessions": []
}
```

优势：

- 由服务端统一计算标题、置顶排序、最近列表和运行状态；
- 前端无需通过多个请求拼装交叉分组；
- 避免项目区、最近区、未归类区的分页和排序不一致；
- 保留现有 `/api/sessions?limit=&offset=` 供完整会话页做分页查询。

## 9. Session 标题策略重构

当前标题生成发生在首轮运行完成后：服务端在首轮任务结束时异步调用模型，再通过 `AppendSessionInfo(name)` 追加一条 `session_info`。这会产生竞态：用户可能在自动标题请求返回之前完成手动重命名，延迟返回的自动标题仍可能覆盖用户输入。

本提案将标题逻辑收敛为一个明确的服务端策略层，而不是在调用点分散判断。

### 9.1 标题来源和优先级

| 来源 | 说明 | 优先级 |
|---|---|---:|
| 手动重命名 | 用户通过 Session 三点菜单指定的标题 | 最高 |
| 自动命名 | 首条用户消息成功完成后，由模型生成 | 仅无任何标题时允许 |
| 回退标题 | 首条用户消息预览或短 Session ID | 仅展示时计算，不持久化 |

现有 `SessionInfoEntry` 没有来源字段。重构时应扩展其持久化内容，或在同一条目中补充兼容字段：

```go
type SessionInfoEntry struct {
    EntryBase
    Name   string `json:"name"`
    Source string `json:"source,omitempty"` // "manual" | "auto"
}
```

旧记录的 `Source` 为空，兼容地视为既有标题；不得因迁移而修改历史 entry 数据。

### 9.2 统一标题服务

新增内部标题策略接口（名称以实现时最终代码为准），集中负责：

1. `GetTitleState(sessionID)`：读取最新标题及来源；
2. `SetManualTitle(sessionID, title)`：验证非空，写入 `source=manual` 的最新标题 entry；
3. `ScheduleAutoTitle(sessionID, firstUserMessage)`：仅在没有任何标题时排队；
4. `SetAutoTitleIfUntitled(sessionID, title)`：在写入前重新读取标题状态；只有仍无标题才写入 `source=auto`。

自动命名应使用“生成完成前后均检查”的双重保护：

```text
首轮完成
  → 检查是否无标题
  → 无标题才调用标题模型
  → 模型返回后再次读取最新标题
  → 仍无标题才持久化自动标题
```

第二次检查必须在与 Session 元数据写入兼容的同步边界内完成，避免“检查后、写入前”仍被并发手动重命名穿插。实现可复用现有按 Session 的身份锁/运行时锁，或在 session 存储层提供原子条件写入；不能仅依赖前端时序。

### 9.3 API 与事件

- `POST /api/sessions/{id}/title` 调用统一的 `SetManualTitle`，不直接在 HTTP handler 中追加 entry。
- 自动命名和手动重命名完成后均发布既有 `title_updated` Session 事件；可增加 `source` 字段帮助前端调试，但前端展示不依赖它。
- 列表读取沿用“最新 `session_info` entry”的规则，因此无需额外标题表或 title overlay 字段。
- 已有标题的历史 Session 不会在后续发送消息时重新触发自动命名。

### 9.4 验收标准

1. 没有标题的新 Session 在首条成功完成后最多自动命名一次。
2. 用户在自动标题模型请求期间手动重命名后，自动结果不能覆盖手动标题。
3. 用户重命名后刷新页面、重启 Serve、重新打开 Session，标题保持不变。
4. 自动标题失败、超时或返回空值时不影响 Session 运行和现有回退展示。
5. 旧 Session 的标题仍按现有记录读取，不要求数据迁移。
6. WebSocket 的 `title_updated` 能使侧栏、Sessions 页面和当前聊天页同步更新。

## 10. 前端组件与状态设计

### 9.1 涉及文件

| 文件 | 变更 |
|---|---|
| `ui/src/components/Sidebar.svelte` | 替换扁平历史列表为项目/最近/未归类树 |
| `ui/src/views/Sessions.svelte` | 支持展示项目、置顶状态与管理操作 |
| `ui/src/lib/stores.js` | 增加 sidebar 聚合数据、项目刷新和元数据更新方法 |
| `ui/src/lib/preferences.js` | 补充中英文文案 |
| `ui/src/style.css` | 实现紧凑 section、折叠和上下文菜单样式 |
| `internal/session/` | 新迁移、项目与置顶元数据仓储、测试；复用既有 Session 标题条目 |
| `internal/serve/` | 项目 CRUD 与 Session metadata 管理 API |

### 9.2 折叠状态

折叠状态属于单设备视觉偏好，可使用浏览器本地存储：

```text
mothx.webui.sidebar.projectsCollapsed
mothx.webui.sidebar.recentCollapsed
mothx.webui.sidebar.unprojectedCollapsed
mothx.webui.sidebar.project.<projectId>.collapsed
```

项目、归属、标题和置顶状态属于用户数据，必须进入后端 SQLite。

### 9.3 可访问性

- section 与项目折叠按钮使用 `aria-expanded`。
- 三点菜单使用 `aria-haspopup="menu"`、`aria-expanded`。
- 菜单项支持键盘上下键、Enter、Escape；最小要求为 Tab、Enter、Escape 可用。
- 仅 hover 显示的操作，在 `:focus-within` 时也必须显示。
- 图标按钮必须有可翻译的 `aria-label` 和 tooltip。

## 11. 视觉规范

参考豆包紧凑风格，但使用 MothX 现有变量和设计体系：

- section 标题和项目行高度：30–32px；
- 会话行高度：30–32px；
- 项目内会话左缩进：14–16px；
- hover 使用低对比 `--bg-hover`，选中项使用 `--bg-active`；
- 三点、加号、折叠箭头默认低对比或隐藏，仅 hover/focus 显示；
- 置顶图标尺寸约 12px，不新增额外行；
- 展开/折叠使用不超过 150ms 的透明度与高度过渡；尊重 `prefers-reduced-motion`；
- 搜索框和新对话维持现有紧凑样式，但导航与会话工作区之间使用小间距分组，而不是大块卡片。

## 12. 测试与验收标准

### 11.1 后端测试

1. 创建、列出、重命名、删除项目。
2. 删除项目后关联 Session 自动移出项目且不删除消息。
3. Session 重命名追加新的 `session_info` 标题条目，并在列表/API 中返回最新标题。
4. 自动标题不会覆盖先完成的手动重命名。
5. Session 置顶、取消置顶、移动项目、移出项目。
6. Sidebar 聚合接口对项目、最近、未归类、置顶排序的返回正确。
7. 运行中 Session 删除保护、绑定 Session 的既有删除/解绑行为不回归。
8. 数据库迁移可从既有 schema 升级，旧 Session 无元数据时可正常展示。

### 11.2 前端测试与手工验收

1. 新建项目后立即出现在项目 section。
2. 项目、最近、未归类 section 与单个项目均能独立折叠并在刷新后保留折叠状态。
3. 点击会话主体进入聊天；点击三点不会打开会话。
4. 三点菜单的置顶、重命名、移动、移出、删除均正确刷新左栏和 Sessions 页面。
5. 项目内会话仍会在“最近”出现，但不会在“未归类”重复出现。
6. 搜索只显示匹配会话及其路径，不重复显示同一会话。
7. UI 在桌面和移动抽屉模式下均可用；菜单不被侧栏 overflow 裁切。
8. `go test` 覆盖受影响包，`npm run build` 通过。

## 13. 实施顺序

1. 在 `internal/session` 重构统一标题策略，补充标题来源、原子条件写入与竞态测试。
2. 在 `internal/session` 增加追加式数据库迁移、项目和 Session metadata 仓储及单测。
3. 新增项目 CRUD、Session metadata patch、Session 手动命名和 sidebar 聚合 API，并覆盖 HTTP 测试。
4. 更新前端 store 和双语文案。
5. 重构 Sidebar 为可折叠项目会话树。
6. 实现 Session/项目三点菜单、新建与重命名交互。
7. 更新 Sessions 页面以使用相同元数据。
8. 运行 Go 聚焦测试、UI build，使用浏览器验证交互与视觉效果。

## 14. 待确认事项

1. “全部对话”是否只显示未归类会话（本提案建议如此），还是显示所有会话？
2. 新建项目后，是否需要提供“同时移动当前 Session 到此项目”的快捷选择？
3. 项目是否需要关联默认工作目录？本提案建议第一期不关联，以避免把项目与文件目录强耦合。
4. 是否需要支持项目颜色/图标？本提案建议第一期不做，先使用统一文件夹图标。
5. 是否需要允许在项目内直接新建 Session？本提案建议第二期考虑；第一期通过新建对话后在三点菜单移动即可。
