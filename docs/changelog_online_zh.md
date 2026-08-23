# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.93

### 🐛 问题修复

- **GHCR 镜像构建改用 Go 1.27**
  - Docker 构建镜像现在使用 `golang:1.27.0-bookworm`，与 `go.mod` 一致，避免 GHCR 打包时出现 `go.mod requires go >= 1.27 (running go 1.26.1; GOTOOLCHAIN=local)`。

- **桌面端版本跟随 Git Tag**
  - 桌面端 `package.json` 只保留 `0.0.0` 占位符，不再写死发行号。
  - 打包时从 `MOTHX_VERSION`（如已设置）或当前 git tag（`git describe --tags --abbrev=0`）解析真实版本，并在构建时写入 `package.json`、`package-lock.json` 和 `mothxRuntime.version`。
  - 桌面端 CI 不再把分支名当作版本，而是使用显式 tag 覆盖或检出仓库的 git tag。

### 🔧 改进

- **ACP 安装诊断**
  - `initialize.agentInfo` 现在报告真实的 MothX 身份和构建版本。
  - 新增无需会话的 `mothx/doctor`、`mothx doctor --json` 和结构化 `MOTHX_ACP_ERROR` 启动诊断，检查结果不包含密钥原文。

- **清理 CI 测试工作流**
  - 移除了每次提交都会运行的 GitHub Actions 测试工作流，推送不再自动跑测试。
