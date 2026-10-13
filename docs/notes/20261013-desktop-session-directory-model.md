# Desktop 多会话目录模型（2026-10-13）

- 日期: 2026-10-13
- 状态: 已确认，待实现
- 范围: `desktop/` 的新会话目录选择、ACP 子进程启动 cwd，以及 ACP/Runtime 的 cwd 有效性校验。

## 决策

Desktop 是多 session、多任务、多工作目录并行的 ACP 客户端。目录的权威归属必须按 session 划分；产品不得存在一个会被设置页长期配置、又隐式成为下一次 `session/new` cwd 的“全局默认工作目录”。

1. 已有 session 只使用其持久化 cwd；切换、重启 ACP、切换 Runtime 二进制或修改其他 session 都不能改变它。
2. 用户点击“新建任务”后，发送前必须显式选择一个目录。最近目录可以缩短选择动作，但点击某项只为这一次草稿选择 cwd，绝不写成全局执行默认值。
3. Desktop 刚启动、尚未点击“新建任务”时，首页直接输入允许使用本次启动创建的临时项目目录（quick-start scratch cwd）。它仅服务这个首次快捷入口，不持久化为偏好，也不成为后续新任务的回退目录。
4. ACP 子进程需要一个存在的启动 cwd，但它只是进程技术细节。它可复用本次启动的 scratch cwd，绝不能投影、持久化或回写为任何 session cwd 或新任务偏好。
5. 设置页不提供“默认工作目录”设置。持久化的最近目录/目录选择器初始位置仅是 Desktop 壳层展示状态，不能自动触发 `session/new`。
6. `session/new`、`mothx/session/setWorkDir` 与加载/执行已有 session 的共享 ACP/Runtime 路径必须验证 cwd 是存在的目录；不得持久化或延后使用已删除/普通文件路径。

## 状态模型

渲染器草稿需要表达目录来源，而非仅靠 `dirConfirmed` 布尔值：

- `quick-start`：启动时获得的临时 cwd，可以直接发送一次新会话。
- `new-task`：用户明确点击新建任务后的草稿；没有选目录前不可创建或发送。
- `selected`：用户为本次 `new-task` 草稿明确选择的 cwd。
- `session`：已有 session 的 cwd，来自 ACP/Runtime 持久化记录。

这些状态均是渲染器内存投影；唯一持久的 cwd 事实是 session header。目录历史可以保存在 `desktop-store.json`，但不拥有 session 语义。

## 已识别的旧模型问题

- `lastWorkspace` 同时承担新会话默认和 ACP 进程 cwd；重启会把旧启动 cwd 覆盖用户后来选择的目录。
- 主进程遇到失效 `lastWorkspace` 时改用新目录，渲染器却仍把旧路径当作已确认 cwd，前后端会分裂。
- `CreateSession` 仅检查 cwd 非空，已删除目录可能被写入新 session。
- 初始连接失败后 ACP 自动恢复，`ready` 事件没有补齐新会话目录状态。

## 验收标准

- 点击“新建任务”后的发送一定要求本次显式目录选择；最近目录不自动选中。
- 未点击“新建任务”的首次首页直接发送使用本次启动 scratch cwd。
- ACP 重启、Runtime 二进制切换和应用重启均不改变已有 session cwd，也不制造新的全局默认 cwd。
- 设置页不展示默认工作目录。
- 不存在或非目录的 cwd 在共享 ACP/Runtime 入口被拒绝，且不会创建/迁移 session。
