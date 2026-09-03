# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.98

### 🔧 Improvements

- **TUI: Dead Code Cleanup and Decision Lifecycle Alignment**
  - Removed unused functions (`renderLiveAssistantMessage`, `renderPlanPanel`, `formatPlanForDisplay`, `normalizeHistoryLineEndings`, `resolveESMStoreDir`, `updateViewportContentWithFollow`) and trimmed the associated plan/ESM test coverage.
  - Question requests are now registered through `DecisionService`, so pending questions persist and replay like approvals; duplicate question requests are rejected.
  - Terminal decision status is mapped from the actual run state instead of always marking pending decisions `cancelled` — only an explicit cancellation records `cancelled`, any other terminal outcome records `timed_out`.
  - The deferred print loop gained a `stopPrintLoop` exit path used on quit and reload so queued transcript lines are drained before teardown.
  - The external status-line refresh is deferred to actual renders, coalescing event-dense bursts into a single refresh.
  - `sessionsDel` now resolves the session directory through the defensive `getSessionDir()` helper.

### ✅ Tests

- TUI: new tests cover question decision registration (pending kind, persistence, duplicate rejection) and ESM store directory resolution.