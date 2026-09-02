# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.97

### ✨ New Features

- **Online Model Discovery for Providers**
  - Provider model discovery moved into `internal/provider` as shared helpers (`ModelsEndpoint`, `ResolveSecretRef`, `DiscoverModels`) that fetch and normalize a provider's `/models` listing into `DiscoveredModel`s. The OpenAI-compatible `/v1/provider/models` and model-test endpoints now call these shared helpers instead of duplicating probe plumbing.

- **TUI: Fetch and Search Online Models in the Auth Dialog**
  - A new "Fetch Online Models" entry in the provider model list and model settings views runs discovery against the draft provider's Base URL / API type on a background command and opens an "Add Model · Online List" view where fetched models can be added to or removed from the draft. Nothing is persisted until the provider is saved, and stale results after closing the dialog or switching providers are dropped, with loading/empty/error states and zh/en labels.
  - Typing in the online list filters fetched models, ranked exact > prefix > substring across model ID and display name while preserving discovery order within the same score; Esc clears the query, and a "No models match." hint shows when nothing matches.

### 🔧 Improvements

- **Public SDK Boundary: `agent/` Stays Internal-Free**
  - The provider bridge moved from the public `agent/` package into `bootstrap/` (which external modules already blank-import); it registers the provider resolution hook plus the concrete provider factories at init time.
  - `agent.Builder` no longer pre-resolves the platform session directory (the internal builder resolves the default at Build time) and reports a clear error when a hook is unregistered; the examples now blank-import `bootstrap` instead of internal packages.

- **Session Store Integrity Hardening**
  - `DeleteSession` now prunes child tables without a `session_id` column (`delivery_operations`, `attachment_deliveries`) through their session-owned parents, so deleting a session leaves no orphaned rows.
  - Schema migrations apply in ascending version order instead of slice order; forward table references no longer depend on FK enforcement being disabled.
  - Non-terminal/terminal Run status sets are centralized in `run_store.go` as the single source of truth; SQL literals, partial unique indexes, fork, trajectory, and recovery paths all derive from them.
  - `EndConversationTurn` is idempotent for already-closed turns; conversation entry IDs grow to 64-bit; `IdentityLocks` delegates to a ref-counted lock registry, and the runtime lease bus closes its UDP listener once the last handler unsubscribes.

### 🐛 Bug Fixes

- **TUI: Instant Submit for a Lone Enter**
  - A queued Enter was treated as line-break evidence, so every quick type-then-Enter send waited the full 120ms split-paste coalescing window. The extended idle window now applies only when the queue carries real paste evidence (newline-bearing rune chunks or Enter adjacent to text); a lone deferred Enter keeps the normal 16ms window and submits immediately.

- **Missing Error Reason on Abandoned Durable Runs**
  - A background run abandoned after interrupted tool execution could reach a terminal status without persisting the reason. A dedicated annotation boundary (`RunDAO.UpdateErrorIfEmpty` → `session.AnnotateSessionRunError` → `agentruntime.AnnotateDurableRunError`) now sets the error only while it is still empty — without changing run status, reviving terminal runs, or touching active runs, keeping the first recorded reason authoritative. The Responses API abandon path persists the reason through this boundary.

- **Duplicate User Entry in the Background Run Coordinator**
  - The durable user entry appended during admission is already in replay state after the manager reload; the coordinator now matches the deterministic `RunUserEntryID` and reuses it as the continuation message instead of appending a duplicate to the transcript and the provider request. The check stays idempotent across retries, recovery, and process restarts.

- **Channel Rotation Lease Target**
  - `AcquireRuntimeForRotate` now takes the session directory explicitly from the lifecycle owner (falling back to the dispatcher's configured directory when empty), so the mutation lease and the forced-release wait target the authoritative Session instead of whatever directory the dispatcher holds.

### ✅ Tests

- Architecture: `public_sdk_boundary_test` fails if the public `agent/` package or `example/` modules import internal packages again.
- New session tests: delete integrity (no orphaned rows), ascending migration order, Run status-set consistency, ref-counted lock registry, and lease-bus listener cleanup; widened orphan-recovery timing margins under `-race`.
- Regression tests for the fast lone-Enter submit path (split-paste continuation still protected) and durable-run error annotation.
