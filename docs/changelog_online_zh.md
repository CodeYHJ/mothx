# 更新日志（当前版本）

本文件仅记录**当前版本**的变更。所有版本的完整历史见 [docs/zh/changelog.md](zh/changelog.md)。

## v1.2.89

### 🐛 修复

- **错误信息泄露防护**
  - Serve API 中的模型发现错误不再透传上游响应体，防止凭据、私有诊断信息或任意 HTML 泄露给客户端。仅 HTTP 状态码足以说明模型发现失败原因。
  - 运行提交中的预检错误信息现已清空 `Detail` 字段，确保原始解析/存储诊断不会通过 `DisplayErrorMessage` 投射到适配层。

### 🔧 改进

- **CI 分支版本解析**
  - `Makefile` 现在优先使用 `GITEE_BRANCH` 环境变量进行版本字符串解析，回退到 `git describe` 再到 `dev`，确保 CI 构建携带正确的发布标签。
