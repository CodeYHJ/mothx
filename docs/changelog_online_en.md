# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.99

### ✨ New Features

- **Unified Server-Resolved Model Catalog Across WebUI and TUI**
  - A new `GET /api/models/catalog` endpoint resolves every selectable provider/model through `providerfactory.ResolvedModels`/`SortProviderIDs` — the same shared logic that builds the TUI's provider model lists — and returns canonical defaults plus the sorted provider list. The active provider stays selectable even when it comes from a built-in preset or a serve flag instead of the settings providers map.
  - `stores.js` migrates `models` → `modelCatalog`; the Chat new-session picker now consumes the server catalog instead of merging raw settings JSON client-side, dropping the local `buildModelCatalog`/settings-fallback derivation. Settings surfaces (default provider/model dropdowns, provider editor) consume the same store, and inherited built-in preset models get dedicated zh/en labels.
  - The TUI auth dialog's provider sort delegates to `providerfactory.ProviderSortPriority`, so TUI dialogs and the WebUI catalog share one ordering logic.

- **Enable Supervisor Mode (ESM): Slash-Command Control, Shared Guidance, and Evidence Tracking**
  - WebUI ESM control moves to the same `/esm` slash command as the TUI (`/esm <objective>`, `/esm status|edit|pause|resume|clear|guide`) instead of dedicated graphical controls — the 500-line `ESMControls` component is removed, and chat input and the ESM REST API share one server-side objective path.
  - New guidance module: `/esm guide <text>` queues user guidance stamped with the objective's current version; the Supervisor injects pending guidance into every non-recovery role prompt and consumes it exactly once after the role result is applied — one core-owned lifecycle shared by the TUI and WebUI adapters.
  - New evidence module: a shared `EvidenceTracker` accumulates tool-call evidence per role run (unique tool-call IDs, per-tool counts/errors), so the "tool-backed evidence" checks in `ApplyWorkerResult`/`ApplyReviewResult` cannot diverge between adapters.
  - ESM no longer enforces token/time budgets: the `budget_limited` status, `SetBudget`/budget prompts, and the TUI `/esm budget` subcommand are removed; `TokensUsed`/`TimeUsedMS` remain observability-only counters. The blocked-audit threshold is centralized as `BlockedAuditLimit` (the objective becomes blocked after 3 consecutive runs report the same blocker).
  - Unattended derived runs resolve their execution mode through `agentruntime.ResolveUnattendedMode`: only `os` is inherited from the session mode and every other session mode falls back to `yolo`, so ESM role sub-agents never stop on interactive approval (hard high-risk-command protections remain mode-independent).
  - The Supervisor now runs continuations against the base Run ID (role runs use suffixed IDs derived from it), owns the terminal `FinishRun` call for both adapters, and clears stale streaks left by earlier continuations when a continuation ends.

### 🐛 Bug Fixes

- **ESM: Failed Objectives Pause Instead of Silently Re-running**
  - A non-retryable role failure now pauses the objective and requires an explicit `/esm resume` before it can run again — queued guidance or a future trigger can no longer silently re-run a failed task. Timeouts and retryable transport errors still take the recovery path bounded by `RecoveryLimit`.
  - Serve startup no longer replays historically persisted "active" ESM objectives: a role may have failed just before the process exited, so `Create`/`Edit`/`ResumeESM` are now the only explicit execution entry points.
  - `esmCoordinator` gains `stop`/`stopAll` with done-channels and bounded waits; Serve shutdown cancels every ESM worker and waits for it to release session/runtime references (`SessionRuntime.Shutdown` remains the final resource boundary), and a closed coordinator refuses to start new workers.

- **Native Directory Picker on Headless Servers**
  - The Unix native picker reports itself unavailable when `DISPLAY`/`WAYLAND_DISPLAY` is absent instead of failing silently, letting the Web UI fall back to its built-in directory browser. Launch failures that print diagnostics on stderr now surface as errors instead of being mistaken for a dialog cancel.

### 🔧 Improvements

- **Directory Browser: Windows Drive Roots and Path-Aware Allowed Roots**
  - `/api/browse` allowed-root resolution now takes the requested path, and Windows drive roots are listed through a virtual browse root (drive roots share no common parent for navigation). `DirBrowser` gains an `initialPath` prop, one-shot open semantics, a server-provided `selectable` flag, and refresh support.

- **Tool Recovery Audit Trail**
  - `RequestToolExecutionRecoveryRecords` records explicit user confirmation and returns only matching interrupted calls; the records are retained as audit evidence while recovery starts as a fresh execution, and a new DAO listing (`ListRequestedToolRecoveries`) exposes requested recoveries to Serve. Terminal Runs are never reactivated to consume these records.

### ✅ Tests

- ESM: new/expanded coverage for the guidance lifecycle (version stamping, injection, one-time consumption), evidence tracking, pause-on-non-retryable-failure, base-Run-ID continuations, budget removal, `/esm` slash-command parity, and coordinator `stop`/`stopAll` (including closed-coordinator start refusal).
- Serve: a process-level test asserts startup never replays historical ESM objectives; browse-root tests cover allowed-root resolution and Windows drive-root listing; native picker tests cover the headless-unavailable and stderr-diagnostic cases.
- Provider factory: catalog resolution and provider ordering tests; settings: `qwen3.8-max-0902` preset assertions.
