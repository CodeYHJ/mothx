# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.90

### ✨ New Features

- **ACP Session Config Options**
  - ACP now supports `session/set_config_option` and `session/set_mode` RPC methods, enabling clients to change model, mode, and thinking level per session without rebuilding the agent or provider.
  - Each session carries a consistent `configOptions` catalog in `session/new`, `session/load`, and `session/resume` results, and `session/set_config_option` notifies all connected clients of the updated options.
  - Configuration is persisted to the session history (`model_change`, `mode_change`, `thinking_level_change` entries) and replayed on session load, so bindings survive restarts and survive across multiple adapters sharing the same session.
  - Stable streamed message IDs (`messageId` on `agent_message_chunk`, `agent_thought_chunk`, and `user_message_chunk` updates) group chunks into logical messages per prompt turn.

- **ACP Additional Directories**
  - `session/new`, `session/load`, and `session/resume` accept an `additionalDirectories` array of absolute workspace roots. The tool registry resolves paths within these roots, and the sandbox mounts them as read-only (strict mode) or writable paths.
  - Directory sets are persisted as `additional_directories` session entries and restored on reload.
  - The session list endpoint (`session/list`) exposes `additionalDirectories` per session.

- **ACP Standard Elicitation Form Protocol**
  - When the client advertises `elicitation.form` in client capabilities, question requests use the standard ACP `elicitation/create` method instead of the legacy `_mothx/request_question` extension, with a typed `requestedSchema` envelope.
  - Replay gracefully falls back to the legacy extension shape when a reconnecting client does not advertise form support.

- **ACP FileDiff Protocol Projection**
  - Tool call updates now include a `diff` content type with `path`, `oldText`, and `newText` fields for semantic diff representation, alongside the existing text content. `oldText` is `null` for newly created files.
  - Tool call updates carry `locations` with the affected file path when a diff is present.

### 🔧 Improvements

- **Session Mode and Thinking Level Persistence**
  - New `EntryModeChange` and `EntryAdditionalDirectories` session entry types record mode switches and directory bindings to the session history, enabling full replay and cross-session consistency.
  - The `SessionRuntime` owns `Model`, `Mode`, `ThinkingLevel`, and `AdditionalDirectories` as session-scoped bindings, with `ConfigureSession`, `ConfigSnapshot`, `SetConfigOption`, and `SetAdditionalDirectories` methods for atomic read/write.
  - `BuildAgent` inherits session-level model, mode, and thinking level when the adapter omits overrides, ensuring consistent agent construction across ACP, TUI, and WebUI.

- **ACP Strict JSON-RPC 2.0 Validation**
  - The ACP server now enforces: `initialize` must be called before any other method, `initialize` may only be called once, empty messages are silently skipped, responses to notifications (empty ID) are suppressed, and request IDs are validated as JSON-RPC scalar types.
  - `cancel` requires a `sessionId` parameter and returns an error for unknown sessions.

- **ACP Unique Run IDs Across Connections**
  - Prompt run IDs now include a random suffix, preventing run ID collisions when ACP SDK request IDs repeat across connections.

- **CI Release Notes from Changelog**
  - GitHub release workflows now use `docs/changelog_online_en.md` as the release body instead of auto-generated release notes, ensuring releases carry the curated changelog.

- **FileDiff Enriched with OldText/NewText**
  - `FileDiff` now retains the complete file contents (`OldText` and `NewText`) for protocol projections that require a semantic diff rather than a display patch. `OldText` is `nil` when the write created a previously absent file.

- **ContentBlock File Fields Extended**
  - `FileContent` now carries `Title`, `Description`, and `Size` fields, and the ACP `resource_link` prompt type maps these fields into the provider-neutral file representation.

### 🐛 Fixes

- **ACP Missing Prompt Body Rejection**
  - Prompts with only empty text are now rejected with an `empty prompt` error instead of proceeding to the agent loop.

- **ACP Flaky Test and CI Stability**
  - Fixed wrapped thinking text assertion and flaky Go tests in CI.
