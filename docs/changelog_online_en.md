# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.93

### 🐛 Bug Fixes

- **GHCR Image Builds Use Go 1.27**
  - The Docker builder image is now `golang:1.27.0-bookworm`, matching `go.mod`, so GHCR packaging no longer fails with `go.mod requires go >= 1.27 (running go 1.26.1; GOTOOLCHAIN=local)`.

- **Desktop Version Follows Git Tags**
  - Desktop `package.json` keeps a `0.0.0` placeholder instead of a hardcoded release number.
  - Packaging resolves the real version from `MOTHX_VERSION` if set, otherwise the current git tag (`git describe --tags --abbrev=0`), and writes it into `package.json`, `package-lock.json`, and `mothxRuntime.version` at build time.
  - Desktop CI no longer treats a branch name as the version; it uses an explicit tag override or the checkout's git tag.

### 🔧 Improvements

- **CI Test Workflow Cleanup**
  - Removed the per-commit GitHub Actions test workflow so tests no longer run on every push.
