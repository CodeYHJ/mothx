# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.96

### ✨ New Features

- **Runtime Workspace Input Materialization**
  - A single front-end-neutral input contract now owns user files for every entry point (CLI, TUI, WebUI/API, ACP, WeChat, Feishu). Adapters submit source streams; `internal/agentruntime` materializes accepted files into the project workspace and the first user message declares file paths plus metadata, letting the Agent decide how and whether to read each file.
  - Images are no longer auto-converted to provider image content on ingest; input resources persist via the `input_resources` table with a Runtime-owned lifecycle (`PrepareInput`/`AttachPreparedInput`, discard/delete/cleanup, `input_resource_events`).
  - TUI `/paste-image` now submits a stream the Runtime writes; the Web UI gained attachment upload/preview in chat; ACP prompt content (text/image/file/audio/video) and WeChat/Feishu inbound media all normalize through this same ingress.

- **Lease-Based Execution Admission and Orphaned Run Recovery**
  - The legacy `TryLockRuntime`/`TryLockRuntimes` path was replaced with explicit, purpose- and run-bound runtime leases across CLI, TUI, ACP, Serve, channels, and cron: ref-counted durable lease guards (`AcquireExecutionAdmission`/`AcquireFork`/`AcquireMutations` and run binding) plus recovery/reconciliation modes for stale or orphaned runs.
  - A new `RecoveryCoordinator` converges orphaned runs lease-first through a startup scan and periodic/wake-driven retries, backed by the `session_run_recoveries` table with durable state, retry accounting, and idempotent replay.
  - The session runtime snapshot surfaces admission/recovery facts (`reserved`, `local`, `external`, `detached_remote`, `orphaned`, `recovery_failed`, `inconsistent`); the Web UI shows matching status badges and disables delete/fork while a session is busy.

- **Durable Delivery Outbox**
  - Delivery intents and ordered operations (`delivery_intents`/`delivery_operations`) with deterministic `PlanDelivery` sequences (caption/upload/send/fallback), a Runtime claim/fence/retry coordinator, terminal-state atomic commit of assistant message/Run/turn/event/intent, and service-start recovery.
  - WeChat (image/video/file) and Feishu (image/file) outbound media deliver natively through frozen transport contexts; published artifacts moved into a private store outside the work directory with integrity verification (size + SHA-256) on open.

- **Idempotent Run Submissions**
  - New `runtime_submissions` table with reconcile-on-conflict handling: submit-key conflicts reuse the existing submission instead of creating duplicates, making Run admission retry-safe.

- **New Volcengine Model: `glm-5.3-flash`**
  - Added `glm-5.3-flash` to the `volcengine`, `volcengine-agentplan`, and `volcengine-codingplan` providers with a 1M context window and text+image input; like `glm-5.3`, no default max_tokens is sent.

### 🔧 Improvements

- **DAO-Only SQL Migration**
  - `internal/db` now owns process-wide SQLite/Bun connection lifecycle and transaction boundaries; all session, cron, stats, ESM, and delivery SQL moved into `internal/dao` persistence objects.
  - Removed the `internal/commondb` compatibility package and the delivery legacy bridge; the architecture guard enforces the DAO-only boundary with a minimal migration-owner allowlist.

- **Web UI Loading and State Stability**
  - Route views (Chat/Sessions/Stats/Cron/Skills/Settings/Login) are lazy-loaded so only the active route's chunk is fetched; lucide/bits-ui/svelte dependencies are grouped into stable vendor chunks.
  - Session runtime state (load/PATCH/polling/mode switching) moved into a unit-testable manager, and loaded history snapshots merge field by field so stale or empty persisted projections never erase live assistant text.

### 🐛 Bug Fixes

- **Cached Input Token Double Charge**
  - Usage accounting now computes uncached input tokens (`UncachedInputTokens`) instead of charging cache reads twice across Anthropic, OpenAI-compatible, and Google wire formats.

- **ListSessionRuns Connection Deadlock**
  - `ListSessionRuns` queried `input_resources` inside the `session_runs` rows loop, blocking forever on the single-connection pool and hanging TUI startup for continued sessions with run records. The outer rows are now drained before a single batched query, with a regression test asserting completion.

- **Durable-Run Terminal Event Stability**
  - `RunExecutor.Finalize` no longer publishes the terminal stream event for durable runs; `FinalizeRun` remains the single publisher after `FinishDurable` commits the assistant message, so WebUI history reloads cannot race the database write. Durable identity is recovered from the canonical Run row when the in-memory marker is gone, and already-closed conversation turns are tolerated so idempotent retries still commit the final entry and terminal event.

### ✅ Tests

- Architecture: `input_contract_guard_test` enforces the single input contract across TUI, CLI, WebUI/API, ACP, and Channel entry points.
- Extended admission/recovery tests for lease-first orphan convergence, execution snapshots, stop handling, idempotency, and cross-process lease behavior; delivery process integration tests for claim/fence/retry and coordinator recovery.
- Regression tests for cached-input-token accounting and the `ListSessionRuns` deadlock.