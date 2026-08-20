# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.89

### 🐛 Fixes

- **Error Message Leak Protection**
  - Model discovery errors in the Serve API no longer reflect upstream response bodies, preventing credentials, private diagnostics, or arbitrary HTML from leaking to the client. The HTTP status code alone is sufficient to explain a model-discovery failure.
  - Preflight error info in run submission now clears the `Detail` field, so raw parser/storage diagnostics are never projected through `DisplayErrorMessage` to the adapter.

### 🔧 Improvements

- **Version Resolution from CI Branch**
  - The `Makefile` now prefers the `GITEE_BRANCH` environment variable for version string resolution, falling back to `git describe` and then `dev`. This ensures CI builds carry the correct release tag.
