# WebUI 视觉刷新、轨迹展示与 Session 日志下载最终实施方案

> 状态：Implemented（已补充 DeepSeek Harness 侧栏源码级调研；侧栏 Shell/rail/内联搜索等结构性变更已按本方案落地）
> 日期：2026-08-21  
> 参考实现：`/home/free/src/deepseek-harness`  
> 目标范围：MothX Serve WebUI（Svelte 5）及配套只读导出接口，不改变 TUI、Agent Core 或 Runtime 的执行语义

> 当前进度：轨迹展示、`session.log` 导出、基础视觉 token 和参考实现级侧栏结构改造已落地；UI 构建、单元测试与浏览器冒烟验收已完成。

## 1. 摘要

本方案定义一次性交付的最终 WebUI 改造目标与实施方法。仅参考 DeepSeek Harness 在布局、质感和配色思想上的做法，不复制其品牌视觉资产：

1. 用语义化设计 token、清晰的层级背景、窄而稳定的间距和可折叠的工具详情，替换当前 `style.css` 中分散的视觉常量。
2. 在 Chat 会话中增加独立的“轨迹（Trajectory）”视图。轨迹不是第二套 Agent 流程，也不是把原始事件全部堆到聊天记录中，而是从现有会话消息、工具事件、Run 事件和能力事件投影出一份按 Run/轮次/步骤组织的可检查事件账本。
3. 在当前 Session 的标题/操作区增加 `session.log` 下载入口。导出由服务端读取现有 Session/Run/Event 数据生成，浏览器只负责直接下载，不在前端拼装大日志。

Logo、产品名称、品牌图形、品牌主色和现有主题色值保持不变；参考项目的蓝色只作为“信息层级和状态语义如何分层”的研究样本，不作为 MothX 的新品牌色或替换色。

最终交付必须同时覆盖：实时流、刷新恢复、历史回放、工具详情、稳定排序、行内时间/耗时信息、长列表虚拟化、响应式详情检查、session.log 导出和完整测试。轨迹不再提供独立的时间轴区域或时间轴操作。所有持久化仍使用现有 `internal/session` 与 `ExecutionRuntime`，不增加独立的轨迹数据库或并行 Run 生命周期。

## 2. 调研结论

### 2.1 DeepSeek Harness 的视觉系统

参考项目的样式职责集中在 `packages/client/ui-theme/src/styles/`，核心特征如下：

- 使用静态色阶加语义别名两层 token。功能组件消费 `--dsw-alias-*`，不在组件 CSS 中复制颜色值。
- 明暗主题通过主题所有方统一覆盖，组件本身不写主题分支。
- 参考项目使用低饱和蓝建立信息层级，中性色负责表面和文字层次，成功、警告、错误分别使用绿色、琥珀色、红色。这里借鉴的是“品牌色、表面色、状态色分离”的配色思想，不采纳其具体色值。
- 字体、代码字体、动效曲线、动效时长、滚动条也纳入全局 token；数字和时间使用等宽或 tabular-nums，便于扫描。
- UI 密度偏高：边框 1px、控件高度约 20–38px、圆角通常 3–12px，主要依靠背景层和分隔线而不是阴影堆叠来建立层级。

参考项目的 `docs/web-styling.md` 明确禁止在功能组件中引入第二套全局主题、组件库或 Tailwind，并要求保留键盘焦点、减少动态效果和代码/终端列结构。

### 2.2 DeepSeek Harness 的布局与交互

`packages/client/ui-layout/src/client/AppFrame.tsx` 与 `AppFrame.module.css` 采用三列布局：可折叠侧栏、中心会话、可调整宽度的详情栏。详情栏在收起时保持挂载但不绘制边框，拖拽时暂停过渡，避免布局跳动。

侧栏本身并不是一个单文件组件，而是一个有明确所有权边界的 Shell：

- `packages/client/ui-sidebar/src/client/SidebarRoot.tsx` 只负责列几何和壳层控制：顶部品牌行、折叠/展开按钮、新会话按钮、侧栏折叠状态机、折叠时的宽度冻结、150ms 淡出后再切换到 56px rail，以及底部固定座位；它不拥有会话树业务数据。
- `SidebarRoot` 通过 `sidebar.workspaces`、`sidebar.settings`、`sidebar.footer.action` 三个 slot 把工作区浏览、设置入口和底部扩展动作注入壳层。壳层只把 `wide` 与 `expandSidebar` 能力传给工作区区域，避免 Shell 与 Session/Workspace 数据耦合。
- `packages/client/ui-workspace/src/client/WorkspaceBrowser.tsx` 才是浏览区：标题/视图选项、可展开的内联搜索、添加 Workspace、Workspace 分组树、平铺 Session 列表、远端搜索结果、拖拽排序、重命名/删除/归档等行为均在此实现。折叠 rail 只保留搜索和添加入口的 36px 图标控制，点击搜索会先展开侧栏，等待 300ms 过渡后再聚焦输入框。
- `WorkspaceBrowser.module.css` 将浏览区拆成固定标题、唯一滚动列表和底部 fade；列表使用 `scrollbar-gutter: stable`、8px 滚动条和右侧 2px 偏移，滚动条显隐不会造成 Session 行横向跳动。会话列表按 Workspace 头 34px、Session 行 32px 的稳定尺寸渲染，单个分组默认先显示 5 条，剩余内容通过展开按钮进入。
- `Rows.tsx`/`Rows.module.css` 使用纯展示行：Workspace 行悬停时才显示 chevron、创建和更多操作；Session 行悬停时才将相对时间替换为更多操作。选中/悬停使用语义背景，不靠厚重阴影；选中背景只比所属表面高一个低对比层级，并用细弱指示线辅助定位，不能使用高对比整块深色填充；状态点优先级为待审批/待回答 > 运行/子 Agent > 完成，状态文本通过 visually-hidden 保留给屏幕阅读器。
- `SidebarRoot` 还以侧栏列的实际几何框判断指针是否仍在侧栏内，离开后保留 2 秒滚动条 linger；这样打开位于侧栏后代但视觉上覆盖全屏的设置面板时，滚动条仍会在真正离开侧栏后隐藏。
- `ui-theme` 集中维护静态色阶、语义 alias、字体、动效和滚动条；侧栏/工作区 CSS 只消费语义变量，不在组件内写明暗主题分支。参考实现的视觉层级主要由 sidebar fill、hover/active surface、1px border、稳定密度和轻量动画构成，而不是依赖多层卡片阴影。

因此，参考项目侧栏的核心不是“增加一个导航卡片”，而是“Shell 几何 + Workspace 浏览插槽 + 固定底部插槽 + 单独的滚动/折叠状态机”。MothX 方案应吸收这个结构原则，而不复制 React、slot runtime 或 DeepSeek 品牌字标。

会话壳 (`packages/client/ui-conversation/src/client/skeleton/ConversationRoot.tsx`) 将输入区作为 resident composer 管理，输入框在空会话和有历史会话之间保持同一棵 DOM 树；会话滚动区会读取 composer 的实时高度，保证底部内容可达。

轨迹视图的整体布局由 `packages/client/ui-trajectory/src/client/views.module.css` 定义：

```text
会话标题/视图切换
    ↓
固定工具栏（折叠、搜索、筛选）
    ↓
主账本（可滚动、可加载更早记录） | 详情面板（桌面端）
    ↓
浮动/停靠 composer
```

它保留了聊天视图的输入体验，但把轨迹内容做成全高、可持续检查的工作区，而不是一组浮在聊天气泡里的卡片。

### 2.3 DeepSeek Harness 的轨迹模型

轨迹实现位于 `packages/client/ui-trajectory/`，值得借鉴的不是具体 React 代码，而是下面的边界：

- `trajectory-*-definition.ts` 从共享 Session Event 窗口组装助手、工具、嵌套子工具、压缩、请求开始/结束和会话结束等业务记录。
- `trajectory-record.ts` 为每条记录定义稳定身份、类型、摘要、输入、输出、错误、时间和 token 用量。
- `layout.ts` 将记录整理为 `turn -> group/step -> cell`，同时合并 partial assistant 与 running tool call。
- `TrajectoryTable` 只呈现 index、event、content；选中后由详情面板展示 Input、Output、schema、usage 和 timing。
- 历史加载使用游标和“加载更早一页”控件；长列表只挂载可视行及少量 overscan，向前插入历史后仍靠语义 key 保持选中项和行高稳定。
- 搜索、筛选、折叠和详情选择均是轨迹视图本地状态，不改变 Chat 的消息快照。

参考实现还明确把 reasoning 作为一种 assistant block 保存和展示。MothX 应只展示 provider 实际提供并已持久化的 reasoning/thinking 内容或摘要，不尝试生成、猜测或暴露未提供的隐藏思维链。

### 2.4 DeepSeek Harness 的 Session log 下载

参考实现位于 `packages/session-query/session-log-export/` 与 `packages/host/apiproxy/src/session-export.ts`，其边界同样值得复用：

- Session Header 提供 `Session log` 下载操作，`/export` 命令复用同一个浏览器控制器；轨迹页面本身不重复放第二个下载入口。
- 前端先以 `HEAD /api/session.export?...` 做准备检查，再把 `GET` URL 交给浏览器下载管理器，JavaScript 不缓冲完整文件。
- 服务端负责流式生成 ZIP、背压和错误语义；导出可以包含根 Session 的 `session.jsonl`、子 Session 及被引用附件。
- 同一 Session 同时只保留一个下载任务，重复点击复用正在进行的操作；界面区分准备中、已开始和失败。
- 导出属于人类操作面，不写入模型上下文，也不创建新的 Agent turn。

MothX 使用 SQLite 作为 canonical Session 存储，不能逐字复制参考项目的 raw JSONL 导出。应复用其“服务端准备并流式输出、浏览器直接下载、Session 级并发折叠”的交互边界，但从现有 Session/Run/Event stores 生成 MothX 自己的稳定格式。

## 3. MothX 当前基线

### 3.1 当前 WebUI 结构

- 入口与路由：`ui/src/App.svelte`、`ui/src/lib/router.js`
- Chat：`ui/src/views/Chat.svelte`
- 全局样式：`ui/src/style.css`
- 全局状态与 Run/WS 游标：`ui/src/lib/stores.js`、`ui/src/lib/session-runs.js`
- 事件 reducer：`ui/src/lib/session-view.js`
- API/SSE：`ui/src/lib/api.js`、`internal/serve/openaiapi/session_stream.go`

当前 Chat 已有消息、工具调用/结果、plan、审批、sub-agent、runtime 控制、回到底部和历史加载；因此轨迹应成为 Chat 的第二种视图，而不是重新实现这些能力。

当前 WebUI 没有统一的 Session 日志导出接口；已有下载能力主要针对 provider 附件（`/api/attachments/{providerRef}`）。因此 `session.log` 需要新增会话级、只读、受权限保护的导出端点，不能让客户端读取 SQLite 文件路径或自行重建服务端日志。

### 3.2 已存在且可复用的事件事实

服务端已经持久化并通过 SSE/WebSocket 投影：

| 事实 | 当前协议/来源 | 轨迹用途 |
|---|---|---|
| 用户、助手、tool call、tool result、plan、附件 | `SessionMessageEntry` / `transcript` | 主账本记录、输入/输出详情 |
| 工具开始、完成、失败、参数、摘要、详情标记 | `ToolStatusEvent` / `tool_event` | 工具跨度、状态、参数预览 |
| Run 开始、重试、完成、失败、取消、错误、usage | `SessionRunEventEntry` / `run_event` | Run/attempt 分组、终态、错误和用量 |
| mode/capability 变化 | `SessionCapabilityEventEntry` / `capability_event` | 能力变更记录 |
| 当前 runtime、active run、pending approval/question | `SessionRuntimeSnapshot` / `runtime_event` | 当前状态条和详情摘要 |
| 历史游标 | `entrySeq`、`runSeq`、`capabilitySeq` | 重连、去重、增量回放 |
| 工具完整输出 | `GET /api/sessions/{id}/tool-result/{toolCallID}` 对应的现有 handler | 详情面板按需加载 |

`SessionMessageEntry.Contents` 已能承载 `text`、`thinking`、`image`、`toolCall` 等 provider 内容块；`ToolStatusEvent` 已有 `Timestamp`，Run/Capability event 也有时间戳。因此第一版可以主要通过前端投影完成，不需要新建轨迹持久化表。

### 3.3 当前差距

1. `style.css` 同时承担 token、组件样式、响应式规则和页面特例，颜色与圆角存在大量散落值，难以统一明暗主题和新增视图。
2. Chat 将 transcript、tool、run、capability 事件分别展示，缺少一份按执行顺序和 Run/步骤关系组织的检查视图。
3. 当前事件 reducer 主要为 Chat 服务，缺少 stable record key、跨流合并、折叠分组和选中详情的纯函数投影。
4. 已有 `seq` 游标可用于增量同步，但 persisted transcript entry 的时间/Run 关联信息需要在投影层明确处理；未知时间必须显示为未知，不能用渲染时间补齐。
5. 桌面端尚无通用第三列详情工作区，移动端也需要单独的条件渲染来承载详情抽屉，不能只依靠 CSS media query 切换交互。
6. 尚无按 Session 导出的 `session.log` 文件格式、文件名和大文件下载策略；需要先固定稳定字段与脱敏边界。

## 4. 设计目标与非目标

### 4.1 目标

- 保持 MothX 现有会话、Run、审批、工具、MCP 和 Runtime 的单一事实来源。
- 让用户可以在 Chat 与 Trajectory 之间切换，并在不中断运行的情况下查看当前或历史轨迹。
- 轨迹在流式更新、刷新、断线重连、切换 Session、加载更早历史后都保持去重、顺序和选择稳定。
- 使用参考项目的 token 化、层级背景、密集表格和详情检查器模式，但保留 MothX 的品牌与现有功能入口。
- 完整保留 MothX 当前 Logo、品牌主色、主题色、品牌资产和主操作色；视觉刷新只调整布局、间距、层级、边框、阴影/质感和状态色的组织方式。
- 对 reasoning、工具参数、工具输出和错误提供清晰的摘要/详情两层展示，并尊重敏感信息与 provider 能力边界。
- 桌面端支持主账本 + 详情栏；窄屏使用同一数据模型的底部/全屏详情抽屉。
- 用户可以从当前 Session 下载可审计的 `session.log`，且下载内容与轨迹使用同一批 canonical 事件。

### 4.2 非目标

- 不复制 DeepSeek Harness 的 React、Cordis plugin、Conversation Definition 或完整 `ui-trajectory` 包。
- 不在 `internal/serve` 或 `internal/agentruntime` 新建第二套 Agent、Run、MCP、Session 或决策生命周期。
- 不新增独立的轨迹 SQLite 表、重复保存 tool result 或另造一套游标。
- 不把所有原始 SSE frame 永久暴露给普通用户；原始 JSON 只放在明确的调试/详情折叠区。
- 不把 provider 未提供的 hidden chain-of-thought 解析、补全或展示为“思考过程”。
- 不直接暴露 SQLite、服务器绝对路径或未脱敏的内部日志；`session.log` 只包含允许导出的 Session 记录。
- 不更换 MothX Logo、产品品牌资产、品牌主色或现有主题主色；不把 DeepSeek 的色值、图形或视觉标识带入产品。
- 不把时间轴拖选、缩放、平移、跨视图深链接、子 Session 导出或附件元数据导出留作后续补丁；轨迹只提供账本行内的时间/耗时信息。

## 5. 视觉刷新方案

### 5.1 Token 分层

将 `ui/src/style.css` 顶部的全局变量整理为三层，保持当前 CSS 技术栈：

1. `--moth-static-*`：现有 MothX 品牌色、中性色、成功绿、警告琥珀、错误红的有限色阶；品牌色从当前主题变量读取，不新增替代色阶。
2. `--moth-color-*`：`surface/base/layer/sidebar/overlay`、`text/secondary/muted`、`border/subtle`、`interactive/active`、`state-*` 等语义别名。
3. 组件局部变量：只承载布局契约，如轨迹工具栏高度、详情栏宽度、composer clearance，不承载主题分支。

默认主题完整沿用当前 MothX 的品牌与主题色值，只重组颜色职责：

- 页面、侧栏、正文、浮层继续使用当前主题中的既有色值，通过 `surface/base/layer/sidebar/overlay` 语义别名明确层级，不引入新的冷暖倾向。
- 品牌、主操作、运行中和链接继续使用当前 MothX 已定义的品牌/交互色；不因参考项目使用蓝色而替换现有品牌色。
- success、warning、error 和审批风险色继续映射当前状态色值，首轮不借视觉刷新修改其色相。
- 选中态统一使用比所属表面仅高一层的低对比语义背景，并配合细弱指示线；悬停态单独使用 hover surface，避免选中项目呈现高对比深色块。
- 深色主题继续使用当前深色主题值；所有功能组件只消费语义 token，不重新推导或替换明暗主题颜色。

这会完整保留 MothX 当前 Logo、品牌主题色、明暗主题和主操作色，同时吸收参考项目在色彩职责、对比度和层级上的思想；不复制 DeepSeek 的静态色值、Logo 或品牌识别元素。若发现现有色值存在可访问性问题，应单独提案评审，不夹带在本次视觉刷新中修改。

### 5.2 Shell 与页面密度

- 侧栏按参考项目的职责拆成四个稳定区域：顶部 MothX 品牌/控制区、New Chat 主操作、可滚动的 Session/Project 浏览区、底部固定的统计与偏好操作区。现有 Logo、品牌名称和品牌资产保持原位置与引用，不因为参考项目的 BrandWordmark/FishLogo 而新增或替换 Logo。
- 侧栏壳层只管理布局状态，不再把 Session/Project 业务行为混在折叠逻辑中；MothX 现有 `Sidebar.svelte` 继续作为唯一浏览区实现，已按参考项目的边界整理为“壳层控制、浏览区、底部工具区”三组样式契约。
- 桌面侧栏宽度采用稳定的可读范围，默认目标为 272px；移动端通过 Svelte 条件渲染全屏抽屉，抽屉宽度目标为 304px。不得用 CSS media query 隐藏或替代交互。
- 参考实现的 rail 折叠能力已落地：桌面保留显式折叠/展开入口；折叠后固定为 56px rail，顶部保留 MothX 品牌名称/展开控制与新会话图标，浏览区只显示搜索/添加图标，设置/底部动作保持固定位置；展开时先冻结宽度并淡出，再切换 rail DOM，避免内容重排跳动。移动端仍采用现有 overlay + drawer 语义，不强行复刻桌面 rail。
- Session/Project 浏览区使用单一滚动容器，保留稳定 scrollbar gutter、边缘留白和底部 fade；滚动条只在指针进入/离开 linger 后显现，不能因滚动条出现导致行宽或 composer 布局抖动。
- 搜索采用参考项目的“标题栏内联展开”而不是永久占用大块输入框：初始显示图标，点击后在标题与操作区之间展开输入框；Escape/点击外部且无查询时收起，查询状态保留。折叠 rail 中点击搜索会先恢复宽侧栏再聚焦输入框。
- Session 行保持稳定尺寸和纯展示职责：Workspace 行约 34px、Session 行约 32px；活动状态/运行状态使用状态点和语义背景，悬停才显示更多/创建操作；空白新会话不显示虚假的时间、归档或重命名动作。
- 分组浏览默认显示 Workspace/未归属分组，单组先显示有限条目并提供“展开更多”；同时保留平铺模式、排序模式、Workspace 新建/重命名/删除、Session 重命名/分支/归档以及拖拽排序等真实能力，不把侧栏改成静态装饰。
- 底部区域采用插槽式固定座位的思路：统计、语言、主题、设置等动作始终停在侧栏底部，浏览列表单独滚动；新增底部动作只能通过现有 MothX 组件能力接入，不能让其挤动会话列表。
- Workbench、Topbar、Banner、Page、Chat 统一 1px 分隔线和 8/12/16px 间距节奏。
- Chat 气泡保留用户/助手区分，但减少卡片嵌套；工具、plan、审批使用统一的 disclosure/header 结构。
- 所有 icon-only 操作使用现有 icon 方案或 lucide 等已启用图标，并提供 tooltip/aria-label；不再用 `⊞/⊟` 等字符作为最终图标契约。
- 输入区继续保持底部 resident composer；轨迹视图必须读取 composer 实时高度，为最后一条记录留出可滚动空间。
- Session 标题/操作区提供带下载图标和 tooltip 的 `session.log` 操作；下载进行中、成功和失败状态均可感知，不阻塞正在运行的 Agent。
- 过渡动画只用于侧栏/详情栏/抽屉和轻量状态变化，支持 `prefers-reduced-motion`。

### 5.2.1 参考侧栏到 MothX 的结构映射

| DeepSeek Harness 结构 | MothX 当前承载 | 方案要求 |
|---|---|---|
| `SidebarRoot` Shell | `Sidebar.svelte` 外层与 `style.css` `.sidebar` | 折叠/移动抽屉、顶部控制、底部固定区与浏览区分层；不新增第二套 Session store |
| `sidebar.workspaces` | `Sidebar.svelte` 的导航 + history sections | 保留现有 Project/Recent/Unprojected 数据与操作，增加单滚动容器、搜索展开、稳定行尺寸和 rail 行为 |
| `sidebar.settings` / `sidebar.footer.action` | `.side-utility` + `PreferenceControls` | 统计/语言/主题/设置固定底部，不参与历史滚动 |
| `WorkspaceBrowser` section header | `.side-search`、`.side-nav`、历史标题 | 标题/搜索/视图操作职责分离；搜索不再与新会话按钮争夺固定高度 |
| `ProjectRowItem` / `SessionNodeItem` | `.project-row` / `.session-tree-row` | 悬停操作、活动态、状态点、稳定高度和文本截断；保留现有菜单行为 |
| pointer-aware scrollbar | `.side-history-list` | 保留 scrollbar gutter，滚动条按指针与 linger 控制显隐，不能用 display:none 造成布局变化 |
| 56px rail + 300ms transition | 当前移动 drawer / 桌面侧栏 | 桌面已实现可折叠 rail 和 300ms 搜索展开过渡；移动端只保留条件渲染抽屉，不把两种交互混成一套 CSS 隐藏规则 |

### 5.2.2 侧栏验收标准

- 桌面侧栏在展开、折叠、重新展开时，品牌行、新会话、浏览区和底部固定区不会发生中途重排；折叠过程中内容宽度冻结，动画结束后才切换 rail 内容。
- 侧栏折叠后宽度固定为 56px；搜索、添加 Workspace、新会话、展开侧栏和设置均有可访问名称与 tooltip，且不会显示被压缩的文字。
- 浏览区只有一个垂直滚动上下文；滚动条出现/隐藏前后 Session 行的 x 坐标、文本截断和底部工具位置保持不变。
- Workspace/Session 行的高度、缩进、状态点和 hover 操作可通过浏览器计算样式和截图检查；长标题不会撑破侧栏。
- 移动端侧栏通过 Svelte 条件渲染挂载/卸载，打开时锁定 body 滚动，Escape、点击遮罩、路由切换都能关闭；桌面 rail 样式不得泄漏到移动 drawer。
- 侧栏变更只引用 MothX 现有主题 token；不引入 DeepSeek 蓝色、Logo、字体或品牌图形。当前实现通过 `sidebarCollapsed` 本地偏好保持桌面 rail 状态，移动端仍由 Svelte 条件渲染 drawer。

### 5.3 桌面/移动布局

桌面端 Chat 工作区建议为：

```text
sidebar | session header + view tabs | optional details
        | chat/trajectory scroll      |
        | resident composer            |
```

轨迹详情栏默认宽度建议 320–440px，可拖动调整，最小主表宽度约 280px。移动端不渲染第三列常驻详情，而是在点击记录后用 Svelte `{#if}` 打开抽屉/全屏面板；CSS media query 只调整尺寸与排列，不负责决定交互是否存在。

## 6. 轨迹视图方案

### 6.1 入口与视图状态

在当前 Session 标题区域增加两个视图 tab：`对话` 和 `轨迹`。默认保持 `对话`；视图通过现有路由 query 保存为 `view=chat|trajectory`，因此刷新、复制链接和浏览器前进后退都能恢复。轨迹的选中记录、折叠组、筛选和详情栏宽度按 Session 存入前端 UI store，不写入 Agent/Session canonical 数据。切换视图不清空、不重建 Session 消息状态，也不取消 active Run。

轨迹顶部包含：

- 当前 Run/Session 的轻量状态摘要（运行中、重试、完成、失败、取消）。
- 折叠/展开 Turn、折叠/展开工具调用的操作。
- 轨迹内搜索框，搜索记录摘要、tool name、参数预览、结果预览和错误。
- 账本按 canonical record 顺序展示，时间列仅显示事件已有的时间和耗时；缺失时间显示 `—`，不使用渲染时刻补齐。

### 6.2 记录类型与分组

前端建立纯数据投影 `TrajectoryRecord`，推荐字段如下：

```js
{
  id,                 // 稳定 key，不依赖当前数组 index
  sessionId, runId, attempt,
  seq, timestamp,
  turn, step, group,
  kind,               // user | assistant | reasoning | tool | subtool |
                      // run | capability | approval | question | error
  status,             // running | completed | failed | canceled | pending
  summary,
  preview,
  input, output,
  sourceEvent,        // 仅详情/调试使用
  usage, startedAt, completedAt,
  toolCallId, parentToolCallId
}
```

投影规则：

- 用户消息作为 Turn 起点；助手回复和其后的 tool/subtool 归入同一执行链。
- 一次 Run 的重试用 `attempt` 分组，不把重试内容覆盖为同一条结果。
- `tool_event` 的 running/completed/failed 通过 `toolCallId` 合并；没有 call ID 时使用事件 ID/seq，不使用数组位置。
- `toolCallId` 的父子关系若当前协议提供则显示为嵌套 Subtool；没有父关系时平铺为 Tool。
- `run_event`、`capability_event`、审批和问题事件作为独立的窄行/系统记录，置于对应时间位置，不伪装成助手消息。
- `assistant` 的 `Contents` 中 `thinking` 单独投影为可折叠 `reasoning` 记录；默认只显示一行摘要，详情区显示完整 provider 内容。
- 取消或失败时，以收到的最后状态冻结 running record；不根据 UI 当前时间制造完成时刻。

### 6.3 主账本

主账本采用参考实现的密集表格/行式布局，不用多层卡片：

| 列 | 内容 |
|---|---|
| 序号 | 会话内稳定显示编号；展示编号可重排，但 DOM key 使用 `id` |
| 事件 | User / Assistant / Reasoning / Tool / Run / Capability 等图标和状态 |
| 内容 | 单行摘要、tool name、文件/命令目标、错误或结果预览 |
| 时间 | 已知时显示耗时/时间；未知显示 `—` 或“进行中” |

Turn 和 Step 用粗细不同的分隔线/缩进表示。折叠 Turn 后只保留首条记录和“包含 N 个步骤、M 个工具调用”的摘要；折叠 Assistant 工具调用后保留助手行和工具数量摘要。

### 6.4 时间与排序

轨迹不渲染独立时间轴。记录仍按 canonical `(timestamp known first, timestamp, source priority, seq, id)` 规则稳定排序，账本的时间列显示事件时间，第二行显示已有的执行耗时；没有时间或结束时间时显示 `—` 或“运行中”。详情面板继续显示完整的时间字段，导出继续保留原始时间字段，不使用 `Date.now()` 或浏览器渲染时间补齐。

### 6.5 详情面板

选中一条记录后，桌面端打开右侧详情栏，移动端打开抽屉。详情按记录类型显示：

- **通用**：事件类型、状态、Session/Run/Attempt、seq、时间、来源。
- **Assistant/Reasoning**：渲染后的内容、原始 provider block、usage、TTFT/总耗时（数据存在时）。
- **Tool**：tool name、参数 JSON、目标摘要、执行状态、完整结果、错误和 `toolCallId`。
- **Run**：model、mode、retry、errorInfo、usage、context usage、开始/结束时间。
- **Capability/Approval/Question**：变化前后值、actor、风险/摘要、解决状态和关联 Run。
- **调试**：可折叠原始 event JSON；默认不展开，敏感字段遵循现有服务端脱敏边界。

工具完整结果继续按需请求现有 tool-result API，避免首次打开轨迹时把大输出全部加载到内存。

### 6.6 Session log 下载

下载入口放在 Session Header 的右侧 utilities 区域，在 Chat 与 Trajectory 视图中保持同一位置；使用现有图标系统的下载图标，tooltip/aria-label 为“下载 Session 日志”。下载是人类 UI 操作，不写入 transcript，不触发 Agent turn，也不增加 `/export` 伪消息。

最终下载契约：

- `HEAD /api/sessions/{id}/export?format=log&include_descendants=true`：校验 Session、认证、权限、参数、导出快照和附件引用，不返回 body。
- `GET /api/sessions/{id}/export?format=log&include_descendants=true`：以 `application/x-ndjson; charset=utf-8` 流式返回唯一文件 `session.log`，响应使用 `Content-Disposition: attachment`，文件名固定为 `mothx-session-<safe-session-id>.log`。
- 文件内部采用 `schemaVersion: 1` 的 JSON Lines，每行一个完整 JSON object；第一行为 `manifest`，之后按 canonical 顺序写入 root Session 及所有 descendant Session 的 transcript、tool、Run、capability、Decision request/resolution 和 attachment metadata。
- 每条记录必须包含 `sessionId`、`parentSessionId`、`source`、原始 `seq`、`timestamp`、`runId`、`attempt`、`kind`、`status` 和白名单 payload；工具输出、换行、Unicode 均由 JSON 编码，不使用不可解析的自由文本拼接。
- GET 开始时同时截取 `entrySeq`、`runSeq`、`capabilitySeq` 和 Decision high-water cursor；导出只包含不超过边界的已持久化记录，manifest 标记活动 Run 为 `snapshot: true`，不阻塞、flush 或取消 Agent。
- attachment 只导出名称、媒体类型、provider 引用、大小和脱敏后的元数据，不导出二进制；provider token、环境变量、数据库路径、服务器绝对路径、内部凭证和未授权字段一律排除。

前端先发 HEAD 预检，成功后用临时 `<a download>` 把 GET URL 交给浏览器，不使用 `response.blob()` 缓冲整个文件。同一 Session 的重复点击复用一个 in-flight 状态；按钮显示准备中并暂时禁用，成功表示浏览器下载已启动，失败显示可重试错误。切换 Session 或关闭提示不取消服务端正在进行的 GET。

下载状态由 `idle -> preparing -> started -> error` 组成；成功后保留最近一次导出时间和 high-water 摘要，错误保留可重试原因。Session 切换时状态按 Session 隔离，组件销毁时仅取消 HEAD 预检，不取消已交给浏览器下载管理器的 GET。

## 7. 数据与实现边界

### 7.1 前端模块与职责

最终实现拆成明确的 Svelte 组件和纯 JS 数据模块，`Chat.svelte` 只负责会话生命周期、现有运行控制和把状态传给子组件，不在模板里重新解释事件：

| 文件 | 固定职责 |
|---|---|
| `ui/src/components/chat/SessionHeader.svelte` | Session 标题、状态、Chat/Trajectory tab、日志下载按钮、当前 Run 摘要 |
| `ui/src/components/chat/TrajectoryView.svelte` | 轨迹工作区容器，协调工具栏、账本和详情栏 |
| `ui/src/components/chat/SessionLogDownload.svelte` | 预检、下载触发、状态提示、失败重试和 Session 隔离 |
| `ui/src/lib/trajectory/records.js` | canonical event 到 `TrajectoryRecord` 的归一化、白名单和稳定 ID |
| `ui/src/lib/trajectory/reducer.js` | transcript/tool/run/capability/runtime/decision 多流合并与去重 |
| `ui/src/lib/trajectory/layout.js` | Turn/Step 分组、折叠摘要和固定行高虚拟化 |
| `ui/src/lib/trajectory/search.js` | 标题、参数、输出、错误和 record metadata 的增量索引 |
| `ui/src/lib/session-export.js` | HEAD 预检、GET URL 构造、浏览器下载、并发折叠和状态机 |

`stores.js` 增加以 Session ID 为 key 的 `trajectoryState` 和 `sessionExportState`；所有组件通过 store 读写，不把状态散落在 `Chat.svelte`。轨迹使用现有消息、Run、Capability、Runtime、Decision API 和 SSE/WS，不创建第二套事件生产者。

### 7.2 事件合并与历史恢复算法

每个 Session 维护四个独立输入窗口：`transcript(entrySeq)`、`run(runSeq)`、`capability(capabilitySeq)`、`decision(decisionSeq)`。初次进入并行加载最新窗口和所有历史 high-water；SSE/WS 事件先按来源游标去重，再进入同一个 reducer。

稳定 ID 固定按以下优先级生成：`event.id`；`toolCallId + lifecycle`；`decisionId`；`sessionId + source + seq`。同一 tool call 的 running/completed/failed 状态更新原 record，不追加重复行。合并排序使用 `(timestamp known first, timestamp, source priority, seq, id)`，时间未知的记录保持 source/seq 顺序；向前加载历史只在窗口前插入，不改变既有 record ID、折叠状态、选中项或滚动 anchor。

每次 reducer 更新都输出 `recordsByID`、`orderedIDs`、`groups`、`highWater` 和 `errors`；前端 reducer 与 Go exporter 使用对齐的显式字段白名单和稳定 ID 规则，分别通过单元测试校验 UI 与导出语义一致。任何单一流失败都保留其他流已显示内容，并在工具栏显示可重试的局部错误。

### 7.3 后端最终接口与导出实现

后端固定增加以下路由，不再依赖“前端多流合并失败后再决定是否加接口”的分支：

- `GET /api/sessions/{id}/trajectory?before=<cursor>&limit=<n>`：返回统一的只读轨迹窗口、四类 high-water、是否还有更早记录和脱敏后的详情摘要；`before` 是 base64url 编码的 `{entrySeq,runSeq,capabilitySeq,decisionSeq}` 游标对象。该响应由现有 Session/Run/Event/Decision stores 即时组装，不保存第二份 canonical record。
- `HEAD /api/sessions/{id}/export?format=log&include_descendants=true`：完成认证、权限、Session 树、high-water 快照和输出能力检查。
- `GET /api/sessions/{id}/export?format=log&include_descendants=true`：由无状态 exporter 通过 `io.Writer` 流式输出 `session.log`。

实现位置固定为 `internal/serve/openaiapi/handler_session_trajectory.go`（统一承载 trajectory projection 与 session.log exporter），路由在 `internal/serve/run.go` 的现有 Session 路由表面注册。读取必须通过 `internal/session`、`internal/commondb` 和 Runtime 已有 stores，禁止新开 raw SQLite 连接、路径读取或新增轨迹表。

HEAD 与 GET 共用授权、参数校验、Session 树解析和 high-water 快照；不存在返回 404，参数错误返回 400，不支持的方法返回 405。GET 设置 `application/x-ndjson; charset=utf-8`、安全 `Content-Disposition`、`Cache-Control: no-store`、`X-Content-Type-Options: nosniff`，逐条使用 `json.Encoder` 写入并监听 request context。所有可前置错误必须在写 body 前返回；流中失败只终止连接并写受限服务端日志，不泄露数据库路径或底层错误。

### 7.4 稳定身份与游标

稳定 ID 固定按以下优先级生成：

1. canonical `event.id`；
2. `toolCallId + lifecycle`，用于同一工具的 running/completed/failed 更新；
3. `decisionId`；
4. `sessionId + source + seq`；
5. 仅在 fixture/内存事件中使用 index 兜底。

事件流始终分别维护 `entrySeq`、`runSeq`、`capabilitySeq`、`decisionSeq`。重连时以服务端返回的 high-water 和客户端已确认游标做 replay，先去重后合并；任何流恢复失败都保留已显示记录、标记局部错误并允许原位重试。

## 8. 一次性交付实施清单

这是一个完整交付，不拆分为试验版、首版或后续阶段。实施顺序可以按依赖关系执行，但每一项都是最终验收范围：

1. **视觉基线与 Token 迁移**：记录现有 Logo、品牌主色、主题色、状态色的 computed-style 和桌面/移动截图；在 `ui/src/style.css` 增加语义别名，逐项映射到现有值，统一边框、表面层级、间距、圆角、阴影、focus 和 reduced-motion，迁移后逐项比较基线。
2. **Shell 与 Chat 拆分**：从 `Chat.svelte` 提取 `SessionHeader`、`TrajectoryView`、`SessionLogDownload`；TrajectoryView 内部保持账本与详情的局部组合，保留现有 composer、运行控制、审批、MCP、技能和附件行为，Shell 只负责布局与状态传递。
3. **完整轨迹数据层**：实现四流窗口加载、SSE/WS 增量、稳定 ID、状态合并、Turn/Step/Subtool 分组、搜索索引、历史向前分页和 scroll anchor；用统一 reducer 驱动 Chat 摘要与 Trajectory 账本。
4. **完整轨迹工作区**：实现行内时间/耗时、虚拟账本、过滤/搜索/折叠、键盘导航、关联记录跳转、工具结果按需加载和原始 JSON 折叠查看；不渲染独立时间轴。
5. **响应式与可访问性**：桌面为 Sidebar + Session Header + 主账本 + 可拖拽详情栏；窄屏使用条件渲染的全屏详情抽屉；所有图标按钮具备 tooltip/aria-label，焦点顺序、Escape 关闭、键盘方向键和 reduced-motion 完整可用。
6. **Session log 服务端**：注册 trajectory 与 export 路由，使用 canonical stores 生成 high-water 快照和脱敏 JSONL，支持 root/descendant Session、Decision、工具输出元数据和附件元数据，流式返回并正确处理认证、权限、取消、错误和缓存头。
7. **Session log 浏览器流程**：Session Header 单一下载入口，HEAD 预检后由浏览器直接 GET；同一 Session 并发请求折叠，状态显示 preparing/started/error，切换 Session 隔离，下载不进入 transcript 和模型上下文。
8. **验证与发布门槛**：完成 Go handler/store 测试、trajectory reducer 测试、Svelte 组件测试、Chromium/CDP 交互和截图 smoke，执行 `go test ./internal/architecture`、受影响 Go 包测试、`cd ui && npm run build` 及完整 UI smoke test；任一品牌色、Logo、轨迹排序、下载脱敏或移动交互回归都不得交付。

## 9. 测试与验收

### 9.1 单元测试

- 事件多流合并、稳定 ID、重复 replay 去重、乱序/缺字段归一化。
- tool running -> completed/failed 的生命周期合并。
- Run retry/attempt、取消冻结、capability/approval/question 终态投影。
- 向前加载历史后的 index、折叠、selection 和 scroll anchor 保持。
- reasoning block 仅展示 provider 已提供内容，不从普通文本误识别隐藏思维链。
- `session.log` exporter 的 schema version、稳定排序、root/descendant Session、活动 Run 快照、空 Session、Unicode/换行转义、大工具输出、Decision 重放和敏感字段排除。
- 导出 handler 的 HEAD/GET 一致性、404/400/405、取消传播、安全文件名及流中失败行为。
- `/api/sessions/{id}/trajectory` 的 cursor、limit、high-water、局部流失败和脱敏详情契约。

### 9.2 UI/E2E 测试

- 空 Session、单轮文本、多工具、多 sub-agent、失败/取消、审批等待、后台 Run。
- SSE/WS 断线后使用四个游标恢复，不产生重复记录或错误终态；单流失败可局部重试。
- 桌面端 Chat/Trajectory/详情栏切换；移动端抽屉打开、关闭、返回、Escape 和焦点恢复。
- 账本时间/耗时列对已知、缺失和运行中状态的稳定显示，不用渲染时间补齐未知值。
- 虚拟账本在长 Session 中保持稳定行高、选择、折叠状态和向前分页 scroll anchor。
- Header 下载按钮的 loading/disabled/success/error、重复点击折叠、切换 Session 隔离，以及浏览器直接 GET 而非前端 Blob 缓冲。
- 明暗主题、窄窗口、键盘 focus、`prefers-reduced-motion`；对比改造前后的 Logo、品牌主色、主题主色和状态色截图/计算值。
- `cd ui && npm run build`；轨迹完成后增加针对关键状态的 Chromium/CDP 截图/交互 smoke test。

### 9.3 验收标准

1. 用户可以在不中断 active Run 的情况下切换 Chat 与 Trajectory。
2. 同一条 tool call 在 running、completed、failed 和刷新回放后始终是同一条记录。
3. 事件重复、跨流到达顺序变化或历史前缀补载不会造成重复行、跳序或详情错配。
4. 详情栏可查看工具参数与完整结果，且大结果按需加载。
5. 真实时间不可用时界面明确显示未知，不制造虚假的耗时或完成状态。
6. 轨迹展示不引入新的 Agent/Run/Decision/MCP 生命周期，也不改变现有 API 的执行语义。
7. MothX 当前 Logo、品牌资产、品牌主色、明暗主题主色和主操作色均保持不变；移动端和现有 Chat 功能不回归。
8. 用户可下载当前 Session 的 `session.log`；文件可逐行解析、顺序稳定、活动 Run 明确标记为快照，且不包含凭证、绝对路径或未授权附件内容。

## 10. 风险与固定决策

### 风险

- 历史 transcript entry 的 timestamp/run 关联可能不完整，账本和导出都必须允许部分记录无时间，并明确显示未知；不得用渲染时刻补齐。
- 当前 `Chat.svelte` 体量较大，轨迹投影若直接塞入页面会继续放大耦合；应先抽纯函数和独立 Svelte 组件。
- 工具输出、参数和 reasoning 可能包含敏感数据；详情/原始 JSON 必须沿用服务端已有权限和脱敏边界。
- 虚拟列表与 composer 浮层同时存在时容易出现底部不可达或滚动跳动，需要专门的滚动 anchor 测试。
- 大 Session 的导出可能持续较久；必须流式输出、响应 request cancellation，并限制单 Session 重复任务，避免浏览器内存峰值和无界并发。
- 流式响应开始后无法再可靠返回结构化 HTTP 错误；应尽量在写 header 前准备数据句柄，并用服务端日志记录中途失败。
- Token 迁移容易在 fallback、hover、disabled 或深色主题分支中意外改变现有色值；实施前需要建立当前 computed-color/截图基线，迁移后逐项比对。

### 已确认约束

- 保持 MothX 当前 Logo、品牌名称、品牌图形和资产引用不变。
- 保持当前品牌主色、主题主色、主操作色及明暗主题色值不变。
- DeepSeek Harness 仅用于参考布局结构、界面密度、层级质感和配色职责的组织思想，不复制其 Logo、蓝色值或品牌识别。

### 固定决策

- 轨迹入口固定放在当前 Session Header 的 `对话/轨迹` tab，默认打开 `对话`；下载入口固定放在同一 Header utilities 区域。
- reasoning 只展示 provider 已持久化的 `thinking` block，账本默认折叠，详情面板支持明确的原文查看；不推断隐藏思维链。
- 轨迹不提供独立时间轴；账本按稳定 canonical 顺序显示事件，时间/耗时列对未知值显示 `—`，详情和导出保留原始时间字段。
- 轨迹接口、四游标 replay、服务端 exporter 和前端下载控制器一次性实现并共享 canonical record 语义。
- `session.log` 固定为 UTF-8、版本化 JSON Lines，包含 root/descendant Session 和附件元数据，不包含二进制或未授权敏感字段。
- Logo、品牌资产、品牌主色、主题主色、主操作色和现有状态色值冻结；任何色值变更必须另立变更，不属于本方案。

## 11. 结论

MothX 不需要复制 DeepSeek Harness 的品牌视觉、前端框架、轨迹插件或 raw JSONL 存储。最稳妥的落地方式是：完整保留当前 Logo、品牌主题色和品牌资产，仅参考其布局结构、层级质感与配色职责；再在现有 Session/Run/Event 数据之上增加纯前端轨迹投影和独立视图，同时由只读服务端 exporter 流式生成 `session.log`。这样可以吸收参考项目在密度、详情检查、历史稳定性和下载边界上的成熟经验，同时继续遵守 MothX 的品牌边界和“一套 Agent Core、一套 front-end-neutral Runtime、适配器只做投影”的架构边界。

## 12. 实施进度

- [x] 大项 1：视觉基础与品牌冻结。已在 `ui/src/style.css` 增加只映射现有颜色值的 MothX 语义 Token，统一工作区表面、间距、滚动、composer clearance、focus-visible 和 reduced-motion；Sidebar 已完成品牌行、分组导航、历史层级、活动会话状态点、桌面 rail、内联搜索、指针滚动条和移动抽屉。Logo、品牌主色、主题色和主操作色未替换。
- [x] 大项 2：轨迹数据层、Trajectory 视图、行内时间/耗时、虚拟账本与详情面板。已接入服务端统一 trajectory window、前端实时流合并、虚拟账本、搜索筛选、分组折叠和工具详情加载；未新增 Agent 或 Session 数据源，也不渲染独立时间轴。
- [x] 大项 3：`/api/sessions/{id}/trajectory`、`session.log` 流式导出和 Header 下载流程。已实现 base64url 游标/高水位响应、root/descendant Session 解析、HEAD 预检、GET NDJSON 流式导出、脱敏稳定文件名和 Header 下载控制器。
- [x] 大项 4：侧栏结构改造后的最终验收。`ui` 单元测试 `112/112`、Vite production build、trajectory smoke 和 channel/settings smoke 均通过；trajectory smoke 覆盖桌面 272px、折叠后 56px rail、rail 搜索展开聚焦、移动 304px drawer 与遮罩关闭。

### 验收记录

- 浏览器截图已在 `/tmp/mothx-trajectory-desktop.png`、`/tmp/mothx-trajectory-rail.png`、`/tmp/mothx-trajectory-mobile.png` 生成并检查：桌面显示账本 + 详情栏，rail 显示 56px 图标列且无压缩文字，移动端显示全屏详情抽屉。
- 服务端持久化 assistant 只有在确实包含 provider `thinking` block 时才投影 reasoning；纯 tool-call 消息保持既有消息数量和 Chat 行为。
- 前端与 Go 端轨迹 source priority 已统一为 transcript/tool/run/decision/capability；未知时间记录不补造渲染时间。
- transcript 向前分页使用 canonical `transcript` 游标；tool call/result 与 live tool status 统一为 `tool:<session>:<toolCallId>`，approval/question 统一使用 `decision:<session>:<eventId>`，避免刷新或 replay 重复行。
- 不存在的 Session 轨迹请求返回 404；导出流中途异常只记录受限服务端日志并终止，不把底层存储路径或错误文本写入响应。
- Header 下载预检按 Session/descendant 参数共享 in-flight HEAD；组件切换或销毁只取消 HEAD，不取消已交给浏览器的 GET。
- Sidebar 当前已落地参考实现级结构：顶部品牌/折叠控制、标题栏内联搜索、单一滚动会话树、稳定 32/34px 行高、状态点、悬停菜单、150ms 宽内容淡出后 56px rail、rail 搜索展开和底部固定工具区；桌面宽度为 272px，移动抽屉为 304px；未增加或替换 Logo。
- 修复 Serve 设置页 Token/CORS 列表的不可变更新，完整 settings smoke 可稳定添加、保存和删除配置项。
- Chat 与 Trajectory 共用同一个 resident composer，切换视图不打断输入、停止和发送控制；服务端与前端均按 toolCallId 合并工具生命周期。
- 未修改 MothX Logo、品牌资产、`--primary`/主题主色及现有主操作色；新增语义 token 均映射到既有主题变量。
