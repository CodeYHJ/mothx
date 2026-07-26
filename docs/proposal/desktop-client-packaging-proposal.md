# MothX 桌面客户端化打包方案调研

> Date: 2026-07-26
> Status: 路线已确认（2026-07-26）
> 参考: `/home/free/src/startvibecoding/mothxwork`（OpenWork fork，代号 Moark）

> **决议**：走 ACP 路线——自研桌面壳，通过 ACP 与 `mothx acp` 通信。mothxwork 仅作为**打包方案参考**（vendored 运行时机制、electron-builder 出包配置、自动更新），不共建、不 fork。ACP 能力差距见 `docs/proposal/acp-capability-gap.md`，共享后端抽象已否决（`acp-serve-shared-backend-proposal.md`）。

## 背景

MothX 目前的分发形态都是 CLI：

- 单 Go binary（`zip` / `tar.gz` / `deb`）
- npm `mothx-installer`（按平台 optionalDependencies 分发预编译二进制）
- PyPI installer
- 自解压脚本安装器（见 `non-developer-desktop-installer-proposal.md`）

UI 入口有三个：TUI、`mothx serve` 内嵌的 Svelte Web UI、`mothx acp`（Agent Client Protocol，stdio JSON-RPC）。

本文调研的目标：**把 MothX 包装成普通用户可以双击安装、双击打开的桌面客户端**（macOS `.app` / dmg、Windows 安装器、Linux AppImage），并参考 mothxwork 仓库已有的做法给出推荐路线。

## mothxwork 调研结果

mothxwork 是 OpenWork（Qwen Code desktop）的 fork，已经完成了"Electron 桌面壳 + vendored MothX 二进制 + ACP 通信"的大部分打通工作。其架构和打包方式如下。

### 仓库结构

```text
mothxwork/
  apps/
    electron/     # Electron 壳（main / preload / renderer），React 渲染层
    cli/          # headless CLI 入口
    webui/        # 浏览器版 UI
    viewer/
  packages/
    shared/       # agent 抽象层，含 ACP 进程管理
    server/       # headless server
    core/ session-tools-core/ messaging-*/ ui/ ...
  scripts/
    vendor-qwen-code.ts        # 下载并 vendor mothx 二进制
    electron-build-*.ts        # main/preload/renderer/resources 构建
    electron-builder-config.ts # 生成 electron-builder.yml
```

### 关键机制 1：vendored MothX 二进制

`scripts/vendor-qwen-code.ts` 直接从 npm 安装 `mothx-installer`（即本仓库发布的包）到 `apps/electron/vendor/qwen-code/`：

- 通过 `npm install --include=optional --install-strategy=nested` 拉取 `mothx-installer`，利用其平台 optionalDependencies 自动得到当前平台的 `bin/mothx`（Windows 为 `mothx.exe`）。
- 支持三种来源：`QWEN_CODE_TARBALL`（本地 tgz）、`QWEN_CODE_VERSION`（npm 版本/dist-tag）、`QWEN_CODE_ROOT`（本地源码构建）。
- 默认版本固定在根 `package.json` 的 `qwenCodeRuntime.version`（当前为 `1.1.76`，与本仓库版本同步）。
- vendor 后会执行 `mothx --version` 做可运行验证。

**结论：本仓库的 npm 分发包天然就是桌面客户端的运行时载体，无需为此新增产物。**

### 关键机制 2：ACP 作为桌面↔代理协议

`packages/shared/src/agent/qwen-agent.ts`：

- 以 `mothx acp`（`args = ['acp']`）启动子进程，stdio 上跑 JSON-RPC。
- 采用**共享进程 + 租约（lease）**模式：一个 ACP 进程托管多个 session（`registerSession` / `unregisterSession`），不是每个会话起一个进程。
- `packages/shared/src/agent/backend/internal/runtime-resolver.ts` 按顺序解析二进制位置：vendored 目录 → node_modules → 系统 PATH 的 `mothx`。
- 模型列表通过 ACP 通道获取（`fetchQwenModelsViaSharedAcp`）。

本仓库 `internal/acp/acp.go` 已实现的服务端能力：

- `initialize`（声明 `loadSession`、`sessionEvent` 等能力）
- `session/new` / `session/load` / `session/resume` / `session/prompt` / `session/cancel` / `session/close` / `session/delete` / `session/list`
- `session/update` 通知（文本流、工具调用、plan、usage）
- 客户端→服务端反向请求：权限确认（`session/request_permission`）、MCP 回调

mothxwork 根目录的 `acp-research*.mjs` 脚本说明对方仍在摸索 ACP 对接，主要未知点是消息发送/通知的确切形态。

### 关键机制 3：electron-builder 出包

`apps/electron/electron-builder.yml`（由 `scripts/electron-builder-config.ts` 生成）：

| 平台 |  target | 说明 |
| --- | --- | --- |
| macOS | dmg + zip（arm64/x64） | hardenedRuntime、entitlements、可选签名/notarize、`afterPack` hook 处理 macOS 26 图标 |
| Windows | nsis（x64） | per-user 安装、卸载删 appData；vendored 二进制走 `extraResources` 规避 electron-builder EBUSY bug |
| Linux | AppImage（x64） | |

其他值得借鉴的点：

- `asar: false`，避免解压开销和启动延迟。
- 按平台排除其他架构的 vendored 二进制（`files` 里的 `!**/vendor/.../<other-platform>` 规则）。
- electron-updater 自动更新（`main/auto-update.ts`），Windows 未签名渠道显式 `verifyUpdateCodeSignature: false`。
- 版本管理脚本（`bump-desktop-version.ts`、`check-release-version.ts`）保证桌面版本与运行时版本（`qwenCodeRuntime.version`）一致性。

### 对本仓库的依赖事实

- mothxwork 已经把 MothX 当作"运行时 artifact"：桌面源码不包含 agent 逻辑，只 vendor 二进制。
- 桌面 UI 是 mothxwork 自己的 React 工作台（多会话、文件预览、自动化、权限模式），**不是**本仓库的 Svelte Web UI。
- mothxwork 设定"桌面不存第三方 API key"，这与 MothX 多 provider + `settings.json` 存 key 的模式存在产品差异。

## 可选方案

### 方案 A：共建 mothxwork 作为官方桌面客户端（ACP 路线）

直接把 mothxwork 作为 MothX 的桌面客户端来共建/对齐，本仓库继续只做运行时。

架构：

```text
Electron shell (mothxwork)
  ├─ React 工作台 UI（多会话、权限、文件预览）
  ├─ packages/shared agent 层
  │     └─ spawn vendor/qwen-code/.../bin/mothx acp   ← 共享进程 + session 租约
  └─ electron-updater 自动更新
```

优点：

- 桌面 UI、多会话管理、权限交互、自动更新、三平台出包流水线**已经存在**，工作量最小。
- vendoring 已经指向 `mothx-installer`，版本同步机制（`qwenCodeRuntime.version`）已就位。
- ACP 是无状态 stdio 协议，天然隔离：桌面崩溃可以重连/重启代理进程；CLI 升级独立于桌面升级。

缺点/成本：

- UI 是另一套 React 代码库，与本仓库 Svelte Web UI 双轨并存，功能 parity 需要长期维护（skills、cron、ESM、stats 等 serve Web UI 已有的页面）。
- mothxwork 是体量很大的 fork（含 WhatsApp worker、MCP bridge 等我们不需要的部分），需要裁剪或接受冗余。
- ACP 能力有缺口（见下文"ACP 差距清单"），需要本仓库补齐协议。
- 签名 macOS dmg 需要 macOS runner + 证书；未签名可以 Linux 出 zip。

### 方案 B：自研轻量壳 + 复用 `mothx serve` Web UI

桌面壳只负责：启动 `mothx serve --addr 127.0.0.1:<随机端口>` 子进程、开一个指向它的窗口、托盘、自动更新。UI 完全复用 `ui/` 的 Svelte Web UI。

```text
壳（Electron 或 Tauri）
  ├─ spawn mothx serve（sidecar，内嵌 Web UI + token 鉴权）
  └─ BrowserWindow / WebView 加载 http://127.0.0.1:<port>?token=...
```

优点：

- **UI 零新增代码**：chat、sessions、stats、cron、skills、settings 全部现成，且后续只维护一套 UI。
- serve 已有 Bearer token 鉴权，壳可以用随机 token + loopback 保证本机安全。
- 协议是 HTTP/WebSocket，调试简单，channel 架构已支持进度事件推送。

缺点：

- 壳要自己写（窗口管理、进程生命周期、端口冲突、更新器），Electron 方向约等于重写 mothxwork 的壳部分但 UI 不同。
- Tauri 方向体积小（~10 MB 级安装包），但：macOS 出包必须 macOS runner（签名/公证链），Linux 依赖系统 webkitgtk（发行版碎片化），Rust 工具链进入构建。
- Electron 方向安装包 80–200 MB，但出包配置可以直接抄 mothxwork 的 electron-builder.yml。
- serve 是会话单例语义，桌面多窗口/多会话并发的体验不如 ACP 多租约模式（需要验证 serve 对多 session 并发的支持程度）。

### 方案 C：不做壳，桌面快捷方式直达 serve（现状增强）

延续 `non-developer-desktop-installer-proposal.md`：安装器创建 `MothX Serve` 快捷方式，双击启动 `mothx serve` 并自动打开浏览器。

优点：几乎零成本，两个 proposal 可以合并交付。
缺点：不是"应用"体验——没有独立窗口、没有 Dock/任务栏图标、依赖默认浏览器、无自动更新。只能作为过渡。

### 对比汇总

| 维度 | A: mothxwork 共建 | B: 自研壳 + serve UI | C: 快捷方式 |
| --- | --- | --- | --- |
| 桌面 UI | React 工作台（现成） | Svelte Web UI（现成） | 浏览器 |
| 新增 UI 维护 | 双轨（React + Svelte） | 单轨（Svelte） | 单轨 |
| 通信协议 | ACP stdio | HTTP/WS loopback | HTTP/WS loopback |
| 壳/出包工作 | 基本现成 | Electron 可抄配置；Tauri 平台受限 | 无 |
| 安装包体积 | 大（Electron） | Electron 大 / Tauri 小 | 最小 |
| 多会话桌面体验 | 原生支持 | 取决于 serve 并发能力 | 取决于 serve |
| 对本仓库改动 | 补 ACP 能力 | 少量（serve 启动参数、桌面模式） | 仅安装器 |
| 产品契合 | 需解决 key 存储/品牌分叉 | 与 CLI/TUI/serve 完全一致 | 一致 |

## ACP 差距清单（方案 A 需要本仓库补齐）

> 2026-07-26 更新：曾考虑把 ACP 与 serve API 抽象到共享后端（`docs/proposal/acp-serve-shared-backend-proposal.md`），评估后放弃——抽象层会引入更多复杂性。ACP 保持独立实现，本节差距项如桌面客户端确实需要，再逐项单独补齐。

基于 `internal/acp/acp.go` 现状与 mothxwork 的用法对比：

1. **认证/Provider 配置**：ACP `AuthMethods` 目前为空，MothX 的 provider key 在 `settings.json`。桌面 onboarding 要么复用 `/auth` 流程落盘 settings，要么定义 ACP 扩展方法。这是最大的产品差异点（mothxwork 设定为不存第三方 key）。
2. **模型列表**：mothxwork 通过 ACP 拉模型列表（`fetchQwenModelsViaSharedAcp`）。需要确认我们的 `initialize` 响应或扩展方法能否提供 provider/model 清单及 thinking 能力标志。
3. **消息/通知形态对齐**：`acp-research*.mjs` 显示对方对 `session/prompt` 的 content block 格式和 `session/update` 通知序列仍在摸索，需要出一份我们的 ACP 协议文档（方法、参数、通知、错误码），避免双方靠逆向对接。
4. **Windows 细节**：stdio 子进程在 Windows 上的编码/换行、bwrap sandbox 不可用时的降级策略、`mothx.exe` 路径解析，需要端到端验证。
5. **能力协商**：`initialize` 已声明 `loadSession` / `sessionEvent`，建议补充 `session/list`、`session/delete` 等非标准方法的能力标志，便于桌面做特性检测。

## 出包平台策略（2026-07-26 已确认：三平台全量支持）

桌面客户端**必须完整支持 macOS / Linux / Windows**，因此放弃 CLI 安装器"Linux-only 出包"的约束，接受多平台 CI matrix：

| 平台 | runner | 产物 | 签名 |
| --- | --- | --- | --- |
| Windows x64 | `windows-latest` | **portable 单文件 exe**（双击即用，免安装） | 有证书用 signtool；无证书过渡期出未签名包 + checksums |
| macOS arm64 / x64 | `macos-latest` | dmg + zip | 签名 + notarization（hardenedRuntime + entitlements，配置参考 mothxwork）；证书机密（`CSC_LINK` / `APPLE_ID` / `APPLE_APP_SPECIFIC_PASSWORD` / `APPLE_TEAM_ID`）存 GitHub Secrets |
| Linux x64 | `ubuntu-latest` | AppImage | 不适用（Linux 无签名生态） |

要点：

- vendored mothx 二进制按平台 vendor：每个 runner 上 `desktop-vendor` 从 npm `mothx-installer` 拉对应平台二进制，天然支持跨平台 matrix。
- mothxwork 已验证该 matrix 可行（同样的 electron-builder + 三平台 runner 模式）。
- CLI / npm / PyPI 的 release 仍在 Linux 单 runner 上，不受影响。
- `non-developer-desktop-installer-proposal.md` 的 Linux-only 约束仅适用于 CLI 自解压安装器，不适用于本桌面方案。

## 最终方案（2026-07-26 已确认）

决策记录：

- 走 ACP 路线，自研 Electron 壳；mothxwork 仅作打包参考，不共建、不 fork。
- 壳放**本仓库 `desktop/` 子目录**。
- 渲染层**复用 `ui/` 的 Svelte Web UI**（同一套源码，构建期接入 ACP 传输层）。
- **管理面走双通道**：会话走 ACP；settings/auth/skills/cron/stats/memory 走 serve HTTP API。
- **不干扰 CLI / npm / PyPI 打包**：`desktop/` 完全独立，现有 `Makefile`、`npm/`、`pypi/`、release workflow 零改动。
- **Windows 不走 nsis 安装器**：出 electron-builder `portable` 单文件 exe，双击即用、免安装、可放任意目录。
- 不做临时方案：首期即包含自动更新、版本锁定、三平台出包。

### 总体架构

```text
desktop/                        # Electron 壳（本仓库子目录，独立 package.json）
  main/                         # 主进程
    acp-bridge.ts               # spawn mothx acp，stdio JSON-RPC ↔ IPC 转发
    serve-manager.ts            # spawn mothx serve（127.0.0.1:随机端口 + 随机 token）
    runtime-resolver.ts         # vendored → node_modules → PATH 解析 mothx 二进制
    window.ts / tray.ts / updater.ts
  preload/                      # contextBridge：acp.send/on、serveBaseUrl+token 注入
  renderer/                     # Vite 构建，alias 复用 ui/src 源码 + ACP transport 适配层
  scripts/
    vendor-mothx.ts             # 从 npm mothx-installer vendor 当前平台二进制
    electron-builder-config.ts
  vendor/mothx/                 # vendored 运行时（gitignore）
  package.json                  # name: mothx-desktop；mothxRuntime.version 锁版本

mothx acp      ← 会话通道：session/* 全部方法、流式通知、审批、question
mothx serve    ← 管理通道：settings/auth/skills/cron/stats/memory/sessions 管理 API
```

### 进程模型

- Electron 主进程启动时：解析 mothx 二进制 → spawn `mothx acp`（常驻，单进程托管全部桌面 session，与 mothxwork 共享 lease 模型一致）+ spawn `mothx serve --addr 127.0.0.1:<随机端口>`（随机 token，仅 loopback）。
- 渲染层通过 preload 暴露的 `window.mothx.acp` 收发 JSON-RPC 帧；管理面请求直接打 loopback serve API（token 由 preload 注入，不进入渲染层代码）。
- 两个子进程随壳退出；崩溃自动重启并对 session 做 `session/resume` 恢复。

### 渲染层复用方式

- `desktop/renderer` 的 Vite 构建直接 alias 到 `ui/src`，**不复制代码**。
- 聊天数据通路做成 transport 抽象：serve 模式用现有 SSE/WebSocket，desktop 模式用 ACP-over-IPC。transport 选择由构建期 env（`VITE_TRANSPORT=acp`）决定，`ui/` 默认构建（嵌入 Go 二进制的 Web UI）行为完全不变。
- 管理页面（settings/skills/cron/stats/memory）在两种模式下都走 serve HTTP API，零改动复用。
- i18n、stores、router、style.css 全部沿用 `ui/` 现有约定。

### 与 CLI/npm 打包的隔离保证

- `desktop/` 自带 `package.json` 与 lockfile，不进入根 `Makefile` 的任何现有 target；只新增独立的 `desktop-vendor` / `desktop-build` / `desktop-dist` target（互不依赖 `dist`）。
- Go 构建、`ui/embed.go`、`npm/`、`pypi/`、`install.sh` 零改动。
- release workflow 新增独立 job（或独立 workflow `desktop-release.yml`），与现有 CLI release 互不阻塞；desktop 产物单独上传 GitHub Release，命名 `MothX-<ver>-<platform>.<ext>`。
- `desktop/vendor/` gitignore；CI 里 `desktop-vendor` 从 npm `mothx-installer` 拉与 release tag 一致的版本，也可 `MOTHX_TARBALL` / `MOTHX_LOCAL=1`（用 `bin/` 本地构建）覆盖。

### 出包与更新（首期即完整方案）

- electron-builder 三平台 matrix：
  - **Windows**：`portable` 单文件 exe（`windows-latest`）。双击即用，vendored mothx 运行时打在包内。注意两点并给出对策：
    - portable exe 每次启动会解包到 `%TEMP%`，冷启动略慢于安装版——可接受，换来零安装、零卸载残留。
    - electron-updater **不支持 portable 目标的自动替换**。Windows 更新策略：应用内检查新版本 → 后台下载新单文件 → 提示用户“重启完成更新”，退出时用新文件替换旧文件（download-and-replace-on-exit），体验等同自动更新但不依赖安装器。
  - **macOS**：dmg + zip（arm64/x64，签名 + notarization，`macos-latest`），electron-updater 原生自动更新。
  - **Linux**：AppImage（`ubuntu-latest`），electron-updater 原生自动更新。
- `asar: false`；按平台排除异构 vendored 二进制；Windows portable 单文件内嵌全部资源。
- **版本策略：壳与运行时绑定发布**。`desktop/package.json` 的 `mothxRuntime.version` 锁定 mothx 版本，桌面 release 时 bump + check 脚本保证一致。理由：ACP 扩展方法在演进，绑定发布消除协议版本错配；运行时独立升级通道在 ACP 协议稳定后再评估。

### ACP 补齐（本仓库侧，与壳并行开发）

按 `docs/proposal/acp-capability-gap.md`：

- **P0（桌面 MVP 阻塞）**：图片 prompt（content block → `provider.Message`，打开 `promptCaps.Image`）；模型列表 + 会话级 model/mode/thinking/工具开关的扩展方法（`mothx/session/configure`、`mothx/models/list`）；ACP 协议文档落 `docs/`。
- **P1**：核心 slash 子集（`/clear` `/compact`）、工具结果图片映射、sub-agent 状态事件。
- **P2**：管理面一律走 serve HTTP API 双通道，不进 ACP。
- ACP 补齐只新增方法与通知，不改变现有 CLI/TUI/serve 行为。

### 分阶段计划

1. **Phase 1 — ACP P0 + 协议文档**（本仓库）：图片 prompt、`session/configure`、`models/list`、`docs/acp-protocol.md`。
2. **Phase 2 — 壳骨架**（desktop/）：main/preload/renderer 脚手架、vendor 脚本、ACP bridge、serve-manager、最小聊天界面跑通（复用 ui 聊天视图 + ACP transport）。
3. **Phase 3 — 管理面接入**：双通道联调（skills/cron/stats/settings 页面在桌面模式可用）、onboarding（provider key 录入，落 `~/.mothx/settings.json`）。
4. **Phase 4 — 出包**：electron-builder 三平台 CI matrix（windows/macos/ubuntu runner）、Windows portable 单文件 + 应用内换包更新、macOS 签名 + notarization + electron-updater、Linux AppImage + electron-updater、desktop-release workflow、版本 bump/check 脚本。

### 验收标准

- `make build && make dist`、npm/PyPI 发布流程与现状完全一致（desktop 不介入）。
- 桌面端：新建/恢复 session、流式对话、工具审批、plan/usage 展示、图片发送、模型与工具开关切换，全部经 ACP 完成。
- 管理页面（settings/skills/cron/stats）在桌面端可用，经 loopback serve API。
- 三平台产物（Windows portable 单文件 exe、macOS 签名 dmg、Linux AppImage）在各自 runner 的 CI matrix 产出；应用内更新可用（macOS/Linux 自动更新，Windows 换包式更新）。

### 从 mothxwork 借鉴的打包资产（已并入上述方案）

1. **运行时 vendoring**：npm 安装 `mothx-installer`（`--include=optional --install-strategy=nested`），平台 optionalDeps 自动带出正确二进制；版本锁在壳 `package.json` 的 `mothxRuntime.version`；vendor 后 `mothx --version` 验证。对应 `scripts/vendor-qwen-code.ts`。
2. **二进制解析顺序**：vendored 目录 → node_modules → 系统 PATH。对应 `runtime-resolver.ts`。
3. **electron-builder.yml 要点**：`asar: false`；按平台排除其他架构 vendored 二进制；Windows 二进制走 `extraResources` 规避 EBUSY；per-user nsis；mac hardenedRuntime + entitlements；artifactName 统一命名。
4. **自动更新**：electron-updater，Windows 未签名渠道 `verifyUpdateCodeSignature: false`。
5. **版本一致性脚本**：壳版本与 `mothxRuntime.version` 的 bump/check 脚本（`bump-desktop-version.ts`、`check-release-version.ts`）。

（原"ACP 补齐"小节已并入上方最终方案。）

## 待讨论问题

已决：

- ~~与 mothxwork 的关系~~ → 不共建、不 fork，仅参考其打包方案。
- ~~通信协议~~ → ACP（`mothx acp`，单进程多 session）。
- ~~共享后端抽象~~ → 否决，ACP 独立补齐（见 gap 清单）。
- ~~壳位置~~ → 本仓库 `desktop/` 子目录。
- ~~UI 技术选型~~ → 复用 `ui/` Svelte 源码，构建期接入 ACP transport。
- ~~管理面通道~~ → 双通道：会话走 ACP，管理面走 serve HTTP API。
- ~~打包隔离~~ → `desktop/` 独立，CLI/npm/PyPI 流程零改动。
- ~~版本策略~~ → 壳与 vendored 运行时绑定发布（`mothxRuntime.version`）。
- ~~出包平台~~ → 三平台全量支持，CI matrix（windows/macos/ubuntu runner）；macOS 签名 + notarization。
- ~~Windows 安装器~~ → 不走 nsis，portable 单文件 exe 双击即用；更新走应用内下载换包。

待决：

1. **onboarding 细节**：provider key 录入界面复用 WebUI settings 页，还是桌面做专属首启向导？
2. **证书采购**：Apple Developer 证书与 Windows 代码签名证书的申请时间线（不阻塞开发，阻塞首个正式 release）。

## 参考

- mothxwork vendoring: `scripts/vendor-qwen-code.ts`
- mothxwork ACP 进程管理: `packages/shared/src/agent/qwen-agent.ts`（`args = ['acp']`，共享 lease）
- mothxwork 二进制解析: `packages/shared/src/agent/backend/internal/runtime-resolver.ts`
- mothxwork 出包: `apps/electron/electron-builder.yml`、`scripts/electron-builder-config.ts`
- 本仓库 ACP 实现: `internal/acp/acp.go`
- 本仓库 serve Web UI: `internal/serve/`、`ui/`
- 相关 proposal: `docs/proposal/non-developer-desktop-installer-proposal.md`
