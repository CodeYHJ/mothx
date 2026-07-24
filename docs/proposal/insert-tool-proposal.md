# `insert` 文本插入工具最终方案

> 状态：Proposal  
> 日期：2026-07-24  
> 目标：为已有文本文件提供安全、可预测、原子化的结构化插入能力

## 1. 概述

新增一个名为 `insert` 的内置工具，用于向已有 UTF-8 文本文件的结构位置插入内容。它只处理“增加内容”，与现有 `read`、`write`、`edit` 保持严格职责边界：

- `read`：读取和检查文件。
- `write`：创建文件或整体覆写文件。
- `edit`：基于已有文本的 exact match 进行替换或删除，也可执行多个独立文本变更。
- `insert`：基于文件结构位置执行一次纯插入，不替换、不删除、不整体覆写。

`insert` 支持：

- 文件头部插入；
- 文件尾部追加；
- 指定行之前插入；
- 指定行之后插入。

`insert` 不支持按文本匹配、正则匹配或 occurrence 定位。基于某段文本前后插入，本质上可以由 `edit` 通过 exact replacement 完成，因此归入 `edit`，避免两个工具在模型选择和安全语义上重叠。

## 2. 设计目标与非目标

### 2.1 设计目标

1. **职责单一**：只负责结构化插入，不承担替换、删除和全文写入。
2. **位置明确**：位置只由文件头、文件尾或原文件行号确定。
3. **行为可预测**：行号使用 1-based，行号基于原文件计算。
4. **原内容保护**：除插入点必要的边界换行处理外，不改变已有文本。
5. **文本安全**：拒绝二进制文件和非法 UTF-8 文件。
6. **原子提交**：生成内容失败或写入失败时，原文件保持不变。
7. **边界友好**：默认处理必要的换行，避免内容黏连。
8. **幂等可选**：支持精确、裁剪空白和按行去重。
9. **可预览**：支持 `dry_run`，不修改文件即可返回变更摘要和 diff。
10. **性能良好**：普通文件采用内存处理，大文件使用流式复制，整体时间复杂度为 O(n)。
11. **架构一致**：复用现有工具注册、路径安全、文件锁、审批、diff 和原子写入机制。

### 2.2 非目标

- 不提供任意文本替换；使用 `edit`。
- 不提供文本匹配定位；使用 `edit`。
- 不提供正则定位；使用 `edit` 或先用 `read`/`grep` 定位后使用行号。
- 不提供整文件重写；使用 `write`。
- 不提供 AST 级别修改或自动格式化。
- 不支持一次请求中的多处插入操作。
- 不绕过工作目录、sandbox、文件锁或工具审批限制。

## 3. 与 `edit` 的边界

### 3.1 语义边界

两者底层都可能表现为“读取文件、构造新内容、原子写回”，但输入语义不同：

| 需求 | 工具 | 语义 |
|---|---|---|
| 创建文件 | `write` | 提供完整文件内容 |
| 整体覆写文件 | `write` | 用完整新内容替换文件 |
| 替换已有文本 | `edit` | 指定 `oldText` 和 `newText` |
| 删除已有文本 | `edit` | 将 `oldText` 替换为空文本 |
| 根据文本内容定位后修改 | `edit` | exact match，支持多个非重叠 edit |
| 文件头部插入 | `insert` | 结构位置为文件起点 |
| 文件尾部追加 | `insert` | 结构位置为文件末尾 |
| 指定第 N 行前插入 | `insert` | 结构位置为原文件第 N 行起点 |
| 指定第 N 行后插入 | `insert` | 结构位置为原文件第 N 行终点 |
| 某段文本前后插入 | `edit` | 将匹配文本替换为“原文本 + 插入内容”或反向组合 |
| 多个位置同时修改 | `edit` | 一次请求提交多个 exact、非重叠变更 |

核心判断规则：

```text
如果变更需要描述“找到哪段旧文本，并把它替换成什么”，使用 edit。
如果变更只需要描述“在文件的哪个结构位置增加内容”，使用 insert。
```

### 3.2 为什么不支持 `before_match` / `after_match`

按文本匹配前后插入与 `edit` 的职责高度重叠。例如：

```text
在 `func main() {` 后插入一行
```

使用 `edit` 可以将：

```text
func main() {
```

精确替换为：

```text
func main() {
    init()
```

如果 `insert` 同时提供 `after_match`，两个工具都会成为合理选择，模型选择会变得不稳定；同时 `match`、`regex` 和 `occurrence` 会使 `insert` 逐渐变成一个重复的搜索替换工具。

因此最终方案明确规定：

- `insert` 不接受 `match`、`match_mode`、`regex`、`occurrence` 参数；
- 需要按文本定位时使用 `edit`；
- 如果已知目标行号，可使用 `read`/`grep` 获取行号后调用 `insert`；
- `insert` 每次调用只产生一个基于原文件结构的插入点。

## 4. 最终接口设计

```json
{
  "path": "internal/example.go",
  "content": "\tdefer cleanup()\n",
  "position": {
    "type": "after_line",
    "line": 20
  },
  "create_if_missing": false,
  "ensure_newline": true,
  "dedupe": {
    "enabled": false,
    "mode": "exact"
  },
  "dry_run": false
}
```

### 4.1 参数定义

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---:|---|---|
| `path` | string | 是 | - | 目标文本文件路径 |
| `content` | string | 是 | - | 要插入的非空 UTF-8 文本 |
| `position` | object | 是 | - | 插入位置描述 |
| `position.type` | enum | 是 | - | `head`、`tail`、`before_line`、`after_line` |
| `position.line` | integer | 条件 | - | 行号，从 1 开始，仅用于行位置类型 |
| `create_if_missing` | boolean | 否 | `false` | 文件不存在时是否创建 |
| `ensure_newline` | boolean | 否 | `true` | 是否自动修复插入边界换行 |
| `dedupe` | object | 否 | disabled | 是否在重复时跳过插入 |
| `dedupe.enabled` | boolean | 否 | `false` | 启用去重 |
| `dedupe.mode` | enum | 否 | `exact` | `exact`、`trimmed` 或 `line` |
| `dry_run` | boolean | 否 | `false` | 只生成结果和 diff，不写文件 |

### 4.2 参数校验

- `path` 不得为空。
- `content` 不得为空字符串，且必须是合法 UTF-8。
- `position.type` 必须是四种支持的类型之一。
- `before_line` 和 `after_line` 必须提供 `line`，且 `line >= 1`。
- `head` 和 `tail` 不得提供 `line`，避免无效参数被静默忽略。
- 不得提供未定义的 `match`、`match_mode` 或 `occurrence` 参数。
- `dedupe.mode` 只能是 `exact`、`trimmed` 或 `line`。

## 5. 位置语义

### 5.1 `head`

插入到文件 offset `0`。空文件也允许使用。

### 5.2 `tail`

插入到文件末尾。空文件也允许使用。原文件非空且没有末尾换行时，在插入内容前补一个换行（当 `ensure_newline=true`）。

### 5.3 `before_line`

插入到指定行的起始位置。行号从 `1` 开始。`before_line: 1` 等价于 `head`。

### 5.4 `after_line`

插入到指定行结束位置之后。若目标行没有换行符，先补换行，再插入内容。`after_line: totalLines` 表示在最后一行之后插入。

行号和插入 offset 都基于原文件计算。插入内容不会影响本次请求对行号的解释。

### 5.5 行索引

内部使用轻量行索引：

```go
type LineRange struct {
    Number int
    Start  int
    End    int // 包含行尾换行符时，指向换行符之后
}
```

例如文件 `aaa\nbbb\nccc`：

```text
line 1: [0, 4)
line 2: [4, 8)
line 3: [8, 11)
```

- `before_line(N)` 使用 `LineRange.Start`；
- `after_line(N)` 使用 `LineRange.End`；
- 行扫描只需一次，时间复杂度为 O(n)；
- 没有末尾换行的最后一行仍然算作一行；
- 空文件没有可定位的行，只允许 `head` 和 `tail`。

## 6. 换行策略

`ensure_newline` 默认开启，只处理插入点相邻的必要边界，不对原文件整体格式化。

### 6.1 `ensure_newline=false`

完全按计算出的字节 offset 插入 `content`，不添加或删除任何换行。

### 6.2 `ensure_newline=true`

- `head`：原文件非空且插入内容不以换行结尾时，在插入内容末尾补 `\n`。
- `tail`：原文件非空且原文件不以换行结尾时，在插入内容前补 `\n`；插入内容末尾保持调用方提供的形式。
- `before_line`：插入内容不以换行结尾时，在末尾补 `\n`。
- `after_line`：目标行没有换行时，先在插入内容前补 `\n`；插入内容不以换行结尾时，在末尾补 `\n`。

工具只自动识别和生成 `\n`。原文件中的 CRLF 内容保持原样，不进行全文件换行风格转换。

## 7. 去重策略

去重默认关闭。启用后，如果命中去重条件，工具返回成功但不写文件。

### 7.1 `exact`

判断原文件是否已经包含完整的 `content` 字节序列。适用于严格幂等的块插入。

### 7.2 `trimmed`

对原文件和待插入内容仅做首尾空白裁剪后判断内容是否存在，不修改文件中的原始空白。

### 7.3 `line`

按行判断待插入内容是否已经存在。适合尾部追加配置行。对于多行内容，检查每一行并保持未存在行的原始顺序；如果所有行都已存在，则不写文件。

去重判断在插入前基于原文件执行。去重命中时不生成新的文件，不触发原子替换，也不改变文件修改时间。

## 8. 文件安全与路径规则

- 目标路径通过现有 `Registry.ResolvePath` 解析。
- 遵循项目既有工作目录限制、sandbox 和审批机制。
- 文件必须是普通文件；目录直接报错。
- 默认要求目标文件已存在。
- 文件不存在且 `create_if_missing=false` 时返回错误。
- `create_if_missing=true` 时只允许 `head` 或 `tail`；此时创建空文件并插入内容。
- 文件内容必须是合法 UTF-8。
- 文件采样或完整内容包含 NUL 字节时，拒绝按文本文件修改。
- 使用现有文件锁机制防止同一进程内并发写入。
- `dry_run` 也必须执行路径、文件类型和位置校验，但不得写文件。

## 9. 执行模型

执行流程固定为：

```text
校验参数
  -> 解析路径
  -> 获取文件锁
  -> stat / 读取文件
  -> 文本与 UTF-8 检测
  -> 建立行索引
  -> 计算唯一插入 offset
  -> 执行去重判断
  -> 规范化插入内容
  -> 生成变更内容和 diff
  -> dry_run 返回，或原子提交
  -> 释放文件锁
```

所有位置最终统一转换为基于原文件的字节 offset：

```go
newData := make([]byte, 0, len(oldData)+len(inserted))
newData = append(newData, oldData[:offset]...)
newData = append(newData, inserted...)
newData = append(newData, oldData[offset:]...)
```

普通文件应尽量只构造一次结果缓冲区，避免多次字符串拼接。

## 10. 性能与大文件处理

### 10.1 普通文件

文件大小不超过 `32 MiB` 时，使用 `os.ReadFile` 载入内存，完成行索引、去重、diff 和结果生成。时间复杂度 O(n)，额外空间 O(n)。

### 10.2 大文件

超过 `32 MiB` 时使用流式处理：

1. 打开原文件并完成采样检查；
2. 创建同目录临时文件；
3. 将插入 offset 之前的原内容复制到临时文件；
4. 写入规范化后的插入内容；
5. 将 offset 之后的原内容复制到临时文件；
6. flush、sync、关闭并原子 rename。

大文件路径的额外内存为 O(1)，时间复杂度仍为 O(n)。需要完整内容的去重和完整 diff 如果无法在受控内存内完成，应返回明确错误，不得退化为非原子写入。

为保持行为一致，所有位置类型都使用临时文件 + rename，不为 `tail` 单独采用直接 append 快路径。

## 11. 原子写入与并发保护

提交变更必须满足：

1. 临时文件创建在目标文件同一目录，确保 rename 具备原子性；
2. 临时文件继承原文件权限位；
3. 完整写入后调用 `Sync`，再关闭文件；
4. 使用 `os.Rename` 替换目标文件；
5. 任意错误时清理临时文件，原文件保持不变；
6. 尽量保留原文件权限，不要求复制 atime/mtime；
7. 读取后、提交前检测目标文件是否发生外部变化；检测到并发修改时放弃提交并返回冲突错误。

这里的原子性是文件提交原子性，不代表跨多个文件的事务；`insert` 一次只操作一个文件、一个插入点。

## 12. 返回结果

成功执行后返回结构化结果和简短 diff：

```json
{
  "path": "README.md",
  "changed": true,
  "dry_run": false,
  "inserted_bytes": 128,
  "position": {
    "type": "after_line",
    "line": 20,
    "offset": 512
  },
  "deduped": false,
  "diff": "@@ ..."
}
```

去重导致不写入时：

```json
{
  "path": "README.md",
  "changed": false,
  "deduped": true,
  "reason": "content already exists"
}
```

`dry_run=true` 时返回同样的计划和 diff，但不改变文件：

```json
{
  "path": "README.md",
  "changed": true,
  "dry_run": true,
  "inserted_bytes": 128,
  "diff": "@@ ..."
}
```

结果中的 `offset`、行号等定位信息均指向原文件。

## 13. 错误设计

错误必须说明失败原因和必要的修复动作，至少覆盖：

- `file does not exist`
- `path is a directory`
- `refusing to modify binary file`
- `file is not valid UTF-8`
- `content must not be empty`
- `invalid position type`
- `line is required`
- `line out of range`
- `create_if_missing only supports head or tail`
- `concurrent modification detected`
- `atomic write failed`

失败后不得留下目标文件的部分写入内容。

## 14. 工具描述与模型选择边界

注册给模型的工具描述应明确说明：

```text
Insert content into an existing UTF-8 text file at a structural position.

Use this tool to add text at the beginning or end of a file, or before or after a specific 1-based line number.

This tool only inserts content at one structural position. It does not search for text, replace text, delete text, or overwrite a whole file. Use edit for exact text replacements or deletions, and use write for creating or overwriting a whole file. Changes are validated and committed atomically.
```

参数 schema 只暴露四种位置类型和对应字段。不得在 schema 中保留 `match`、`regex` 或 `occurrence`，避免模型误以为 `insert` 是另一个搜索替换工具。

## 15. Go 数据结构

建议在 `internal/tools/insert.go` 使用以下数据结构：

```go
type InsertParams struct {
    Path            string         `json:"path"`
    Content         string         `json:"content"`
    Position        InsertPosition `json:"position"`
    CreateIfMissing bool           `json:"create_if_missing,omitempty"`
    EnsureNewline   *bool          `json:"ensure_newline,omitempty"`
    Dedupe          *InsertDedupe  `json:"dedupe,omitempty"`
    DryRun          bool           `json:"dry_run,omitempty"`
}

type InsertPosition struct {
    Type string `json:"type"`
    Line int    `json:"line,omitempty"`
}

type InsertDedupe struct {
    Enabled bool   `json:"enabled"`
    Mode    string `json:"mode,omitempty"`
}

type LineRange struct {
    Number int
    Start  int
    End    int
}
```

建议拆分为可单元测试的函数：

```go
func validateInsertParams(p InsertParams) error
func readTextFile(path string) ([]byte, os.FileInfo, error)
func buildLineIndex(data []byte) []LineRange
func computeInsertOffset(data []byte, pos InsertPosition) (int, error)
func normalizeInsertContent(data []byte, offset int, content []byte, typ string, ensure bool) []byte
func shouldDedupe(data []byte, content []byte, d InsertDedupe) bool
func atomicWriteFile(path string, data []byte, mode os.FileMode) error
```

`insert` 的执行入口应遵循现有工具的路径解析、文件锁、结果 diff 和错误包装模式，不重复实现已有基础设施。

## 16. 测试要求

### 16.1 位置

- 空文件 head/tail；
- 普通文件 head/tail；
- before/after 第一行、中间行、最后一行；
- 有末尾换行和无末尾换行的文件；
- 单行和多行内容插入；
- 行号非法、越界和缺失；
- `create_if_missing` 的 head/tail 行为及其他位置拒绝。

### 16.2 换行

- 插入内容有换行和无换行；
- 原文件有换行和无换行；
- `ensure_newline=true/false`；
- CRLF 原文件保持原样；
- 不产生意外内容黏连。

### 16.3 安全与错误

- 文件不存在；
- 目录路径；
- 二进制文件；
- 非 UTF-8 文件；
- 原子写入失败时原文件不变；
- 并发修改检测；
- 路径安全、文件锁和审批行为与其他写入工具一致。

### 16.4 幂等与预览

- exact、trimmed、line 三种去重模式；
- 去重命中时不修改文件；
- dry-run 不修改文件但返回正确 diff 和摘要；
- 启用 dedupe 后重复执行结果稳定。

### 16.5 边界隔离

增加工具选择测试，确认：

- 头部、尾部和行位置插入使用 `insert`；
- exact 文本替换和删除使用 `edit`；
- 按文本定位的前后插入不在 `insert` schema 中出现；
- `insert` 不接受 `match`、`match_mode`、`regex` 或 `occurrence` 参数。

### 16.6 性能

- 普通源码文件单次插入；
- 接近内存阈值的文件；
- 超过内存阈值的大文件流式写入；
- 大文件处理不因文件大小线性增加额外内存。

## 17. 典型调用示例

### 文件头部

```json
{
  "path": "main.go",
  "content": "// Code generated by MothX. DO NOT EDIT.\n\n",
  "position": { "type": "head" }
}
```

### 文件尾部并按行去重

```json
{
  "path": ".gitignore",
  "content": ".mothx/tmp/\n",
  "position": { "type": "tail" },
  "dedupe": { "enabled": true, "mode": "line" }
}
```

### 指定行之前

```json
{
  "path": "README.md",
  "content": "## Quick Start\n\nRun the command below.\n\n",
  "position": { "type": "before_line", "line": 10 }
}
```

### 指定行之后

```json
{
  "path": "internal/example.go",
  "content": "\tdefer cleanup()\n",
  "position": { "type": "after_line", "line": 20 }
}
```

## 18. 最终结论

`insert` 的最终定位是：

```text
基于文件结构位置的单点纯插入工具
```

最终能力只有：

- `head`；
- `tail`；
- `before_line`；
- `after_line`；
- 可选去重；
- 可选 dry-run；
- UTF-8 与二进制保护；
- 自动换行边界处理；
- 小文件内存路径与大文件流式路径；
- 临时文件、fsync、原子 rename；
- 并发修改保护；
- 结构化结果与 diff。

它与 `edit` 的分工最终明确为：

```text
insert：我知道结构位置，只增加内容。
edit：我知道旧文本，通过 exact match 修改或删除内容。
write：我提供完整内容，创建或整体覆盖文件。
```

通过移除文本匹配和正则定位能力，`insert` 不再是 `edit` 的另一种搜索替换接口，而成为一个边界清晰、模型易选、行为可预测的专用结构化插入工具。
