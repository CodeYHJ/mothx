# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.98

### ✨ New Features

- **New Gitee/Moark Model: `qwen3.8-max-0902`**
  - Added `qwen3.8-max-0902` to the `gitee` and `moark` providers with a 1M context window, 128K max tokens, and text+image input.

### 🔧 Improvements

- **TUI: Dead Code Cleanup and Decision Lifecycle Alignment**
  - Removed unused functions (`renderLiveAssistantMessage`, `renderPlanPanel`, `formatPlanForDisplay`, `normalizeHistoryLineEndings`, `resolveESMStoreDir`, `updateViewportContentWithFollow`) and trimmed the associated plan/ESM test coverage.
  - Question requests are now registered through `DecisionService`, so pending questions persist and replay like approvals; duplicate question requests are rejected.
  - Terminal decision status is mapped from the actual run state instead of always marking pending decisions `cancelled` — only an explicit cancellation records `cancelled`, any other terminal outcome records `timed_out`.
  - The deferred print loop gained a `stopPrintLoop` exit path used on quit and reload so queued transcript lines are drained before teardown.
  - The external status-line refresh is deferred to actual renders, coalescing event-dense bursts into a single refresh.
  - `sessionsDel` now resolves the session directory through the defensive `getSessionDir()` helper.

- **WebUI: Settings Model Lists Match the New-Session Picker**
  - The settings "Default Provider / Default Model" dropdowns now reuse the same server-resolved catalog behind the new-session model picker (`GET /api/models/catalog`), so built-in preset providers and their default models stay selectable there too; unsaved form edits are appended after the catalog entries so they can be chosen before saving.
  - The provider settings reuse the same catalog: the sidebar model-count badge shows the resolved count, and the model list additionally renders built-in preset models that are not written to settings as read-only rows (matching the new-session picker; they are not persisted on save — add a model with the same ID to override).

### 🐛 Bug Fixes

- **Serve Responses Recovery Preserves Terminal Runs**
  - Recovering confirmed interrupted tool calls no longer changes a completed or failed durable Run back to `queued` or reattaches its terminal remote Responses task. Serve now submits a fresh, idempotent recovery message through the normal Runtime input path and starts a new local AgentLoop Run; the previous Run remains immutable.
  - Removed the recovery-only terminal-to-active Run-store APIs so future callers cannot bypass the canonical monotonic lifecycle.

### ✅ Tests

- TUI: new tests cover question decision registration (pending kind, persistence, duplicate rejection) and ESM store directory resolution.
- Serve: Responses recovery tests verify that the terminal parent is preserved, a fresh AgentLoop receives the recovery message, and repeated recovery requests reconcile idempotently to the same new Run.
