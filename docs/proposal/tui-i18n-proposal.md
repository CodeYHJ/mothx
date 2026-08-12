# TUI 国际化（中文 / 英文）方案

> 状态：Proposal
> 日期：2026-08-10
> 目标范围：TUI
> 支持语言：中文（zh）、英文（en）

## 1. 背景与目标

当前 TUI 中存在大量直接写入 Go 源码的英文界面文本，包括欢迎信息、状态提示、命令帮助、权限审批、Provider 配置、模型配置、错误提示、工具执行状态和底部快捷键说明等。随着 TUI 功能持续增加，继续直接维护英文字符串会导致以下问题：

- 中文用户需要阅读大量英文固定界面文本；
- 同一语义可能在不同文件中出现多种英文表达，难以统一；
- `/` 命令本身必须保持稳定的英文语法，但命令介绍需要随界面语言切换；
- 新增 TUI 功能时容易遗漏翻译；
- 配置、测试和渲染逻辑与文案耦合，后续扩展其他语言成本较高。

本方案为 TUI 建立完整、可扩展、类型安全且可测试的中英文国际化体系，并新增 `tuilang` 配置：

- `tuilang: "zh"`：强制使用中文；
- `tuilang: "en"`：强制使用英文；
- `tuilang: "auto"`：根据本机当前时区自动选择语言；
- 未配置时默认等价于 `auto`；
- `auto` 模式下，UTC 偏移为 `+08:00` 时使用中文，其余时区使用英文。

最终目标不是简单替换几处文字，而是让所有由 MothX TUI 生成的固定用户界面文本统一经过国际化层处理，同时保持命令语法、数据内容、Agent 输出和工具输出的原始语义不变。

## 2. 设计原则

### 2.1 语言只影响展示，不影响协议和行为

国际化只处理 TUI 固定 UI 文本，不改变以下内容：

- `/help`、`/mode`、`/model` 等 slash 命令名称；
- slash 命令的参数、子命令和解析规则；
- 配置文件字段名、JSON key 和环境变量名称；
- Provider、模型、工具、Skill、文件路径、命令行内容和错误原文；
- 用户输入、AI 回复、Agent 生成的代码、工具输出和项目文件内容；
- 会话数据和历史消息的持久化格式。

例如 `/alloweditpath add <glob>` 始终保持英文命令形式，但其帮助说明可以显示为中文或英文。

### 2.2 所有固定文案必须有稳定的消息 ID

禁止在渲染逻辑中继续散落新的用户可见英文硬编码。每条固定文案都应对应稳定的消息 ID，并在中文、英文 catalog 中同时提供翻译。

消息 ID 使用 Go 常量或结构化 key 管理，避免通过任意字符串在各处拼接，确保：

- 编译期可以发现消息 ID 重命名；
- 测试可以检查中英文 catalog 是否完整；
- 后续新增语言不需要修改渲染层；
- 文案可以集中审校和统一术语。

### 2.3 语言解析与文案渲染解耦

语言解析负责回答“当前 TUI 使用哪种语言”，国际化渲染负责回答“这个消息在当前语言下如何显示”。二者分别设计，避免在业务代码中重复判断 `zh/en`。

### 2.4 中文布局必须按终端显示宽度处理

中文字符通常占两个终端列宽。所有涉及截断、换行、弹窗宽度、表格对齐和状态栏布局的逻辑必须继续使用终端显示宽度计算，而不是直接使用字节长度或 rune 数量。

应复用现有 Lipgloss / `renderutil` 的宽度和换行能力，并补充中英文混排、中文标点、长中文消息和窄终端场景测试。

## 3. 配置设计

### 3.1 配置字段

在现有全局/项目 `settings.json` schema 中新增顶层字段：

```json
{
  "tuilang": "auto"
}
```

字段定义建议：

```go
TUILang string `json:"tuilang,omitempty"`
```

字段名按照需求固定为小写 `tuilang`，不改成 `tuiLang`，避免形成两套配置写法。

### 3.2 合法值

| 值 | 含义 |
|---|---|
| `auto` | 根据本机当前时区的 UTC 偏移自动选择语言 |
| `zh` | 强制中文 |
| `en` | 强制英文 |

配置值比较时建议忽略首尾空白，并统一按小写处理；配置文件中出现其他值时，应给出明确警告并回退到 `auto`，不因界面语言配置错误导致 TUI 无法启动。

### 3.3 默认值

`DefaultSettings()` 中将 `TUILang` 的默认值设为 `auto`。默认配置文件生成逻辑也应写入：

```json
"tuilang": "auto"
```

这样用户可以在生成的配置文件中直接看到该选项，且配置行为清晰可发现。

### 3.4 配置层级与项目级支持

沿用现有 settings 的加载规则，`tuilang` 同时支持全局和项目级配置：

1. 内置默认值：`auto`；
2. 全局 `settings.json`；
3. 项目 `.mothx/settings.json` 覆盖全局配置；
4. 本方案不增加环境变量或命令行参数覆盖。

语言配置属于 TUI 展示偏好，但项目级支持是明确需求，因此当项目配置存在 `tuilang` 时，当前项目使用项目级值；离开该项目后恢复全局值。这样既保留用户全局默认语言，也允许团队或特定项目在项目范围内固定语言。

在 TUI 设置界面修改语言时，必须明确提供保存范围：

- **当前项目**：写入项目 `.mothx/settings.json`，只影响当前项目；
- **全局默认**：写入全局 `settings.json`，作为没有项目覆盖时的默认语言。

如果当前工作目录不是可识别的项目目录，项目级选项应不可用或明确提示原因，不能静默写入错误位置。

### 3.5 配置保存

TUI 内必须提供语言配置入口。语言设置应集成到现有 TUI 设置/配置界面中，而不是新增一个与设置体系平行的配置窗口。界面至少展示：

- 当前配置值：`auto`、`zh` 或 `en`；
- 当前生效语言：中文或英文；
- `auto` 当前依据的 UTC 偏移；
- 保存范围：当前项目或全局默认；
- 语言切换后的即时生效状态。

保存时必须使用现有的 patch 写入能力：

- 全局保存使用 `config.SaveGlobalSettingsPatch(map[string]any{"tuilang": value})`；
- 项目保存使用项目设置对应的稀疏 patch 机制；
- 不将完整默认 Settings 展开写回用户的 sparse 配置文件；
- 写入失败时保留当前配置和界面状态，并显示本地化错误提示。

设置变更成功后，当前 TUI 会话应立即更新 Translator 和所有依赖语言的展示数据，不需要重启 TUI。该操作不重置 Agent、会话、审批队列或其他运行状态。

本方案不新增 `/lang` slash 命令，避免增加命令集合和帮助维护成本；语言设置通过 TUI 设置界面完成。

## 4. 自动语言选择规则

### 4.1 规则定义

当 `tuilang` 为 `auto` 时，使用进程当前本地时区在当前时间点的 UTC 偏移：

- UTC 偏移为 `+08:00`：选择 `zh`；
- 其他任何 UTC 偏移：选择 `en`。

这里判断的是**当前 UTC 偏移**，不是时区名称。这样可以覆盖系统使用 `Asia/Shanghai`、`Asia/Hong_Kong`、`Asia/Taipei`、`Asia/Singapore` 等不同名称的环境，同时避免维护时区名称白名单。

### 4.2 夏令时和动态时区

自动判断必须基于 `time.Now().Zone()` 在运行时得到的当前偏移，而不是固定读取时区名称或手工解析 `/etc/localtime`。因此：

- UTC+08:00 的时区选择中文；
- 当前因夏令时变为其他偏移的时区选择英文；
- TUI 启动时解析一次语言并固定本次会话语言，不在运行过程中因时区变化而突然切换界面。

如果用户希望固定语言，应显式配置 `zh` 或 `en`。在 TUI 内将 `tuilang` 修改为 `auto` 时，应立即按照同一规则重新解析并刷新界面。

### 4.3 时区异常处理

如果系统无法读取本地时区，或 `time.Now().Zone()` 返回异常结果，`auto` 应安全回退到英文，并通过非侵入式诊断日志记录原因。不能因为时区读取失败阻断 TUI 启动。

### 4.4 可测试性

语言解析器不能直接依赖不可替换的系统时间和系统时区。应设计为可注入当前时间/时区的纯函数或测试入口，例如：

```go
Resolve("auto", time.FixedZone("test", 8*60*60)) // zh
Resolve("auto", time.FixedZone("test", 0))        // en
```

测试必须覆盖：`auto`、`zh`、`en`、非法值、UTC+08:00、UTC+07:00、UTC+09:00、UTC、负偏移和时区读取异常。

## 5. 国际化架构

### 5.1 建议目录结构

建议在 TUI 内部增加独立的 i18n 包，避免把翻译逻辑放进 `app.go` 或某一个渲染文件：

```text
internal/tui/i18n/
├── language.go       # Language 类型、配置解析、auto 解析
├── catalog.go        # 消息 ID、Catalog、Translator
├── messages.go       # 中英文固定文案
├── commands.go       # slash 命令帮助定义
└── i18n_test.go      # 语言解析、catalog 完整性、格式化测试
```

如果项目更倾向于减少目录层级，也可以将上述文件放在 `internal/tui` 下，但必须保持清晰的 i18n 边界，不允许由各业务文件自行维护翻译 map。

### 5.2 语言类型

定义受限语言类型，而不是在业务代码中到处传递裸字符串：

```go
type Language string

const (
    LanguageZH   Language = "zh"
    LanguageEN   Language = "en"
    LanguageAuto Language = "auto"
)
```

配置值 `auto` 是用户配置模式；实际渲染时使用的语言只能是 `zh` 或 `en`。因此建议区分：

- `ConfiguredLanguage`：`auto/zh/en`；
- `ResolvedLanguage`：`zh/en`。

### 5.3 Translator 接口

TUI App 创建时，根据 Settings 解析一次有效语言，并将 Translator 放入 `App`：

```go
type Translator interface {
    Language() Language
    Text(id MessageID, args ...any) string
    Format(id MessageID, data any) string
}
```

对于简单文本使用 `Text`，对于包含数量、名称、路径、模型名等变量的文案使用结构化参数的 `Format`。不建议在调用方通过 `fmt.Sprintf` 先拼英文，再交给翻译层处理。

### 5.4 消息定义

固定文案应按功能域分组，例如：

- 通用：确认、取消、返回、保存、退出、启用、禁用、加载中；
- 命令：命令名称保持英文，描述和用法说明支持翻译；
- Agent：运行中、已完成、失败、重试、上下文压缩；
- 工具：工具调用、工具结果、等待审批；
- 审批：批准一次、拒绝、记住命令、记住前缀；
- 会话：新建会话、切换会话、删除会话、无会话；
- Provider/模型：Provider 配置、模型列表、默认模型；
- 设置：设置项名称、当前值、保存成功、保存失败；
- 错误：用户可读的 TUI 错误前缀和恢复提示；
- 快捷键：按键提示和操作说明。

消息 ID 示例：

```go
const (
    MsgWelcomeTitle       MessageID = "welcome.title"
    MsgCommandHelpTitle   MessageID = "command.help.title"
    MsgApprovalApproveOnce MessageID = "approval.action.approve_once"
    MsgAgentRetrying      MessageID = "agent.retrying"
)
```

### 5.5 参数化消息

参数化消息必须保证中英文都能自然调整词序，不能使用英文句子片段拼接。例如不使用：

```go
tr.Text(MsgApproved) + " " + command
```

而使用：

```go
tr.Format(MsgApprovalRememberedCommand, map[string]any{
    "Command": command,
})
```

中文和英文 catalog 分别定义完整句子。数量相关文案应提供明确的 plural 处理策略；对于当前 TUI 的少量数量提示，可先使用结构化的 `count` 分支，不能简单依赖英文单复数后缀。

## 6. `/` 命令国际化设计

### 6.1 命令名和语法保持英文

所有 slash 命令、子命令和参数保持现有英文形式，不提供中文别名，不根据语言切换命令解析。例如以下内容始终不变：

```text
/help
/mode agent
/model
/alloweditpath add <glob>
```

这样可以保证：

- 用户脚本和习惯不受影响；
- 文档、自动化输入和历史记录保持一致；
- 中英文用户都能复用同一套命令；
- 命令解析无需增加中文分支和兼容负担。

### 6.2 命令介绍支持中英文

命令注册信息应拆分为稳定的机器字段和可翻译的展示字段：

```go
type CommandSpec struct {
    Name        string
    Aliases     []string
    Usage       MessageID
    Description MessageID
    Handler     CommandHandler
}
```

命令名、别名和 handler 不参与翻译；`Usage`、`Description`、参数说明、示例和错误提示全部通过 Translator 渲染。

`/help` 的输出建议包含：

- 命令名：保持英文；
- 用法：命令语法保持英文，外围说明按当前语言显示；
- 描述：按当前语言显示；
- 参数说明：按当前语言显示；
- 示例中的 slash 命令：保持英文；
- 用户输入的原始命令和参数：原样显示。

### 6.3 命令错误和 Usage

命令错误不能直接写死英文字符串。以下内容都应进入 catalog：

- 未知命令；
- 参数不足；
- 参数非法；
- 当前模式不允许执行；
- 功能未启用；
- 命令执行失败；
- 命令 Usage。

具体命令收到工具、Provider 或文件系统返回的错误时，错误本体保持原文，仅对固定前缀和恢复建议做翻译，避免破坏底层诊断信息。

## 7. TUI 覆盖范围

国际化范围应覆盖所有 TUI 用户可见的固定文本，而不是只翻译 `/help`。主要包括：

### 7.1 主界面

- 欢迎页和首次启动提示；
- 输入区 placeholder；
- 底部快捷键说明；
- 当前模式、Provider、模型、工作目录等标签；
- 加载中、思考中、执行中、已完成、取消和退出提示；
- 上下文、Token、耗时、费用等统计标签；
- 状态栏和活动面板标题。

### 7.2 Agent 和工具事件

- 工具开始、工具完成、工具失败；
- 子 Agent 启动、运行、完成、失败和切换提示；
- 重试、输出上限、上下文压缩、后台运行提示；
- 固定的事件类型名称和展示标题；
- 固定错误提示和操作建议。

工具参数、命令内容、文件路径、代码块、工具输出和 Agent 输出保持原文，不进行机器翻译。

### 7.3 权限审批

- 审批弹窗标题；
- 工具名称外围说明；
- 批准一次、拒绝、记住当前命令、记住命令前缀；
- 超时、异步、命令、路径等固定标签；
- 审批成功、失败和队列数量提示。

实际 shell 命令、路径和规则文本保持原文，并使用现有安全展示和截断逻辑。

### 7.4 Provider、模型和认证界面

- Provider 列表标题与说明；
- API key、Base URL、Vendor、模型能力等字段名称；
- 新增、编辑、保存、取消、返回、完成等操作；
- 配置校验失败和保存结果；
- 模型参数和兼容性选项说明。

Provider ID、模型 ID、URL 和用户输入的配置值不翻译。

### 7.5 会话、统计、Skill、Workflow 和其他面板

- 会话列表、会话切换、新建和删除提示；
- Stats 面板标题、指标名称和空状态；
- SkillHub、Skill、Workflow、Browser、Delegate 等 TUI 面板中的固定文本；
- 所有 overlay、dialog、modal 的标题、说明和按钮。

## 8. App 生命周期与状态管理

### 8.1 初始化

在创建 `App` 时完成以下步骤：

1. 从已加载的 `config.Settings.TUILang` 获取配置模式；
2. 解析 `auto/zh/en`；
3. 得到本次会话的有效语言 `zh/en`；
4. 创建 Translator；
5. 将 Translator 注入所有 TUI 组件或通过 App 上下文提供；
6. 在命令建议、命令帮助、弹窗和渲染函数中使用同一个 Translator。

不能让不同 overlay 自己重新解析语言，否则会出现同一界面中语言不一致的问题。

### 8.2 TUI 内语言配置与即时切换

语言配置必须在 TUI 内可完成，入口集成到现有设置/配置界面。用户可以选择 `auto`、`zh` 或 `en`，并选择保存到当前项目或全局默认配置。当前生效语言、自动判断依据和配置来源应在界面中明确展示，避免用户无法判断项目配置是否覆盖了全局配置。

设置变更成功后立即刷新当前会话：

- `zh` / `en`：立即切换到指定语言；
- `auto`：立即依据当前时区重新解析；
- 当前项目范围保存：立即更新当前 TUI，并影响后续从该项目启动的 TUI；
- 全局范围保存：立即更新当前 TUI，并作为无项目覆盖时的默认值；
- 已经写入终端滚动区的历史固定文本不回溯重绘；
- 当前 managed view、弹窗、命令建议和状态栏立即使用新语言；
- 正在显示的 Agent 输出、工具输出和用户输入不翻译。

为减少状态复杂度，语言切换只更新 App 的 Translator 和需要重建的命令展示数据，不重启 Agent、不重置会话、不改变任何执行状态。本方案不新增 `/lang` slash 命令。

### 8.3 组件注入

优先采用显式注入：

- `App` 持有 Translator；
- 需要渲染文本的组件接收 Translator 或一个只读的本地化视图模型；
- 纯数据层不依赖 i18n；
- 不使用全局可变语言变量，避免测试相互污染和并发问题。

对于目前由独立函数直接返回 UI 字符串的代码，应逐步调整为接收 Translator 或从 App 方法中调用，确保消息来源统一。

## 9. 代码改造边界

### 9.1 应改造的内容

- 所有固定用户可见字符串；
- 命令建议项的 description；
- `/help` 命令的命令说明和 Usage；
- 所有弹窗、状态提示、错误前缀、按钮和标签；
- 需要根据语言变化的宽度、截断和布局测试；
- 配置 Settings、默认值、稀疏保存和配置测试。

### 9.2 不应改造的内容

- Agent、Provider、工具返回的动态原文；
- shell 命令和命令输出；
- 文件路径、URL、模型 ID、Provider ID、Skill ID；
- session 数据结构和数据库 schema；
- slash 命令解析器的命令名和参数；
- WebUI、Serve API 或 SDK 的默认语言行为，除非另有独立需求。

### 9.3 防止遗漏的机制

建议增加静态检查或测试约束：

- catalog 双语 key 集合必须完全一致；
- 所有 catalog 消息 ID 不允许出现空翻译；
- `/help` 注册的每个命令必须具备 Usage 和 Description；
- 新增 TUI 用户可见文本时，在 code review 中要求说明其 MessageID；
- 可在测试中扫描关键 TUI 文件的新增裸字符串，但不以脆弱的全仓库字符串扫描替代人工审查。

## 10. 测试方案

### 10.1 配置测试

覆盖：

- 默认 `TUILang == "auto"`；
- 全局配置正确读取 `tuilang`；
- 项目配置可以覆盖全局配置；
- `zh`、`en`、`auto` 行为正确；
- 非法值回退到 `auto` 并产生诊断；
- sparse settings patch 只更新 `tuilang`，不展开其他默认字段；
- 默认配置文件包含 `tuilang: "auto"`。

### 10.2 时区解析测试

使用可注入时区，不依赖测试机器实际时区，覆盖：

- UTC+08:00 返回中文；
- UTC+07:00、UTC+09:00、UTC 返回英文；
- 负偏移返回英文；
- 强制 `zh` / `en` 不受时区影响；
- 无效配置和时区异常能够安全回退。

### 10.3 Catalog 测试

- 中英文 catalog 的 MessageID 集合相同；
- 所有消息都有非空文本；
- 参数占位符集合一致；
- 缺少参数、额外参数和错误类型能够被测试发现；
- 格式化后的文本不产生 `%!` 等 fmt 错误。

### 10.4 命令测试

- 命令解析在 zh/en 下完全一致；
- `/help` 的命令名、语法和参数仍为英文；
- `/help` 的描述在 zh/en 下分别正确显示；
- 未知命令、Usage、参数错误提示按语言切换；
- 命令建议的 command value 不变，展示说明随语言切换。

### 10.5 渲染测试

对关键界面分别使用中文和英文 Translator 渲染，检查：

- 文案确实切换；
- 中文没有乱码或截断错误；
- 窄终端下弹窗、状态栏和命令帮助不溢出；
- 中英文混排宽度计算正确；
- 动态命令、路径、模型名和工具输出仍保持原文；
- 语言切换不会改变 Agent、session 或审批状态。

### 10.6 回归测试

至少执行：

```bash
go test ./internal/config/...
go test ./internal/tui/...
go test ./internal/tui/...
```

完成全部改造后执行项目规定的完整 Go 测试：

```bash
make test
```

## 11. 文案与术语规范

建立中英文术语表，避免同一概念多种译法。建议初始规范如下：

| 英文概念 | 中文建议译法 |
|---|---|
| Agent | Agent / 智能代理（首次说明可使用“Agent”） |
| Tool | 工具 |
| Approval | 审批 |
| Session | 会话 |
| Workspace / Working Directory | 工作目录 |
| Provider | Provider / 服务商 |
| Model | 模型 |
| Skill | Skill / 技能 |
| Workflow | Workflow / 工作流 |
| Delegate | 委派 |
| Sub-agent | 子 Agent |
| Thinking | 思考 |
| Context | 上下文 |
| Token | Token |
| Retry | 重试 |
| Command | 命令 |

具体术语应在实现前集中确定，并在所有 catalog 中保持一致。技术标识、命令、模型名和工具名不做中文化，确保用户复制、搜索和排查问题时保持一致。

## 12. 文档与用户可发现性

需要同步更新：

- 中文配置文档：说明 `tuilang` 的三个取值和 `auto` 规则；
- 英文配置文档：说明 `tuilang` 的三个取值和 `auto` 规则；
- 默认 Settings 示例；
- TUI 使用文档中的 `/help` 示例；
- 版本变更记录中的新增配置项说明。

文档中的 slash 命令始终使用英文命令形式；命令说明在中英文文档中分别本地化。

## 13. 兼容性与错误处理

### 13.1 老配置兼容

没有 `tuilang` 的旧配置自动使用 `auto`，不需要迁移脚本，不影响已有用户。

### 13.2 非法配置兼容

对于非法值，程序应：

1. 不阻止 TUI 启动；
2. 记录明确的配置警告；
3. 按 `auto` 解析；
4. 不自动改写用户配置，避免启动阶段产生意外文件变更；
5. 在配置设置界面或诊断命令中提供纠正建议。

### 13.3 缺失翻译处理

运行时不允许因翻译缺失崩溃。若开发阶段遗漏 MessageID，Translator 应回退到英文 catalog；若英文也缺失，则回退到 MessageID，并在测试或开发日志中暴露问题。正式发布前必须通过 catalog 完整性测试，避免生产环境依赖回退行为。

## 14. 实施后的架构结果

完成后，TUI 的语言链路应为：

```text
settings.json
    ↓
ConfiguredLanguage: auto / zh / en
    ↓
ResolveLanguage(current timezone)
    ↓
ResolvedLanguage: zh / en
    ↓
Translator
    ↓
App / dialogs / command help / status / approval / panels
    ↓
终端渲染
```

命令链路应保持独立：

```text
用户输入 /help
    ↓
英文命令解析
    ↓
CommandSpec（Name/Usage/Description）
    ↓
Translator 渲染 Usage 和 Description
    ↓
中文或英文帮助输出
```

这样可以同时满足：命令协议稳定、界面语言可切换、配置简单明确、文案集中管理、自动语言行为可预测、未来扩展其他语言不需要重写 TUI 业务逻辑。

## 16. 已确认的产品决策

以下决策已确定并作为实现约束：

1. `tuilang` 支持全局配置和项目级配置；项目 `.mothx/settings.json` 的值覆盖全局 `settings.json` 的值。
2. 必须支持在 TUI 内配置语言，并集成到现有设置/配置界面。
3. TUI 内配置必须支持选择保存范围：当前项目或全局默认。
4. 语言设置保存成功后当前 TUI 立即生效，不需要重启，也不影响 Agent、会话和审批状态。
5. `auto` 严格依据当前 UTC 偏移判断：恰好 UTC+08:00 使用中文，其他时区使用英文。
6. slash 命令名称、别名、参数和解析语法始终保持英文；命令介绍、Usage、参数说明和错误提示支持中英文切换。
7. `Agent`、`Provider`、`Skill`、`Workflow` 等技术词汇保留英文，不强制翻译成中文术语；外围交互说明使用当前语言。
8. 不新增 `/lang` slash 命令，语言设置通过 TUI 设置界面完成。


满足以下条件后，方案视为完成：

1. `settings.json` 支持 `tuilang: "auto" | "zh" | "en"`，默认值为 `auto`；
2. `auto` 在 UTC+08:00 显示中文，其他 UTC 偏移显示英文；
3. `zh` 和 `en` 能够覆盖自动判断并稳定显示对应语言；
4. 所有固定 TUI 文本统一通过 i18n 层生成；
5. `/` 命令名称、别名、参数和解析行为完全保持英文不变；
6. `/help`、命令建议、Usage、命令错误和命令介绍支持中英文切换；
7. Agent 输出、工具输出、命令内容、路径、模型名和动态外部错误原文保持不变；
8. 中文终端布局经过显示宽度、换行、截断和窄窗口验证；
9. 配置、时区、catalog、命令和关键渲染路径具备自动化测试；
10. 老用户没有 `tuilang` 配置时无需迁移即可正常运行；
11. 中英文文档和配置示例同步更新；
12. `make test` 通过，且没有引入 WebUI、Serve API、SDK 或 session schema 的非必要变化。
