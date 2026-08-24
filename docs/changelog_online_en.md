# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.95

### ✨ New Features

- **Durable CLI Runs**
  - CLI `runPrint` now persists canonical durable runs via `agentruntime.ExecutionRuntime`, aligning the CLI path with WebUI, channels, and ACP lifecycle tracking.

- **UDP Runtime Lease Bus**
  - Added best-effort UDP `SessionLeaseBus` for local-process wake-up on runtime lease and run-state changes.
  - Uses directed loopback broadcast with deduplication; SQLite leases and durable rows remain the sole authority.

### 🔧 Improvements

- **Event Broker Resync**
  - Event broker now exposes `SubscribeWithResync`; subscriber overflow closes the WebSocket so the client reconnects and replays durable SQLite cursors.

- **Runtime Lease Heartbeat**
  - Lease heartbeat now retries transient SQLite failures for a bounded interval and publishes `acquired`/`released`/`lost` notifications.

### 🐛 Bug Fixes

- **Background Run Optimistic Concurrency**
  - Reloaded the shared session manager after durable admission so the background coordinator appends the user message to the new leaf instead of failing its optimistic concurrency check.

### ✅ Tests

- Acquired the runtime lease in `TestResponsesRunAPIAbandonMarksInterruptedToolsWithoutRetry` before inspecting the abandoned tool record, matching the production recovery caller pattern.
