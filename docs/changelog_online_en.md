# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.95

### ✨ New Features

- **Durable CLI Runs**
  - CLI `runPrint` now persists canonical durable runs via `agentruntime.ExecutionRuntime`, aligning the CLI path with WebUI, channels, and ACP lifecycle tracking.

- **UDP Runtime Lease Bus**
  - Added best-effort UDP `SessionLeaseBus` for local-process wake-up on runtime lease and run-state changes.
  - Uses directed loopback broadcast with deduplication; SQLite leases and durable rows remain the sole authority.

- **Per-Run Provider/Model Selection (API & Web UI)**
  - `POST /v1/responses` runs accept an optional `provider` field; qualified `provider/model` IDs are parsed and validated (with a structured mismatch error when the provider does not own the model).
  - The run executes with the requested provider's agent built through the shared `SessionRuntime`; run policy snapshots and request fingerprints now record the provider.
  - `/v1/models` now reports each model's provider.

### 🔧 Improvements

- **Event Broker Resync**
  - Event broker now exposes `SubscribeWithResync`; subscriber overflow closes the WebSocket so the client reconnects and replays durable SQLite cursors.

- **Runtime Lease Heartbeat**
  - Lease heartbeat now retries transient SQLite failures for a bounded interval and publishes `acquired`/`released`/`lost` notifications.

- **Session Capabilities (Sandbox/Browser/Web Search)**
  - `SessionRuntime` gains `CapabilitySnapshot`, `ConfigureCapabilities`, and `SetCapabilityOption`; browser and web-search capabilities persist via `session_capabilities` and replay on load, with core tools synchronized accordingly.
  - ACP sessions restore persisted capabilities and additional directories under the runtime lease; sandbox remains process-policy-owned.

- **Web UI Provider-Aware Model Picker**
  - New searchable `ModelPicker` component with modality icons (text/image/audio/video/file) replaces the old model menu.
  - Chat composes a provider-cascading model catalog from `/v1/models` plus configured providers, so selecting a provider narrows to its models and submits the provider with each run.

- **ACP Session Extension Methods**
  - Added `session/fork` and `mothx/session/setTitle` handling, workspace-window negotiation (`cwd`/additional directories), a cascade delete across fork lineage, and an `available_commands_update` notification.
  - Optional editor context is injected as a bounded, untrusted context block; loading historical sessions with a released runtime lease no longer rewrites persisted bindings.

### 🐛 Bug Fixes

- **Background Run Optimistic Concurrency**
  - Reloaded the shared session manager after durable admission so the background coordinator appends the user message to the new leaf instead of failing its optimistic concurrency check.

### ✅ Tests

- Acquired the runtime lease in `TestResponsesRunAPIAbandonMarksInterruptedToolsWithoutRetry` before inspecting the abandoned tool record, matching the production recovery caller pattern.
- Added ACP tests: loading a historical session with a released lease must not persist defaults, directory updates under the runtime lease, and title changes on historical sessions.
- Added serve tests for per-run provider selection, provider/model mismatch, and qualified-model parsing.
