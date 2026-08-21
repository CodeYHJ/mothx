# AGENTS.md

Guidance for AI coding agents working in this repository. Read this file before exploring or editing code. Keep changes focused, preserve existing behavior and APIs, and validate the smallest relevant scope.

## Project snapshot

- **Primary language:** Go 1.26.1 (`go.mod`), with a Cobra CLI and Bubble Tea/Lipgloss TUI.
- **Frontend:** Svelte 5 + Vite in `ui/`; the built UI is embedded into the Go binary.
- **Desktop:** Electron + TypeScript in `desktop/`; it packages the source-built `mothx` runtime and the same Serve Web UI.
- **Packaging:** npm installer packages under `npm/` and a Python installer package under `pypi/`.
- **Purpose:** MothX (`mothx`) is a terminal AI coding assistant with provider adapters, streaming agent execution, tools, sessions, sandboxing, skills, workflows, serve/API mode, messaging channels, and SDK support.

## Important directories

- `cmd/mothx/` — Cobra CLI entry point and subcommands (`serve`, `stats`, `a2a`, `doctor`, etc.).
- `agent/` — public Go SDK types and interfaces. Treat changes here as public API changes.
- `bootstrap/` — blank-import wiring that connects public SDK types to internal implementations.
- `example/` — public SDK examples.
- `internal/agent/` — core agent loop, events, context handling, tool execution, sub-agents, and system prompts.
- `internal/agentruntime/` — the authoritative front-end-neutral runtime layer: shared `SessionRuntime`/`Builder` resource assembly, `ResolveSource`/`ResolvePolicy` mode wiring, `ExecutionRuntime` durable run lifecycle, `DecisionService`/`DecisionRecord` replay, MCP lifecycle, and coordinated shutdown for TUI, WebUI, channels, and ACP. See `docs/proposal/agent-core-runtime-unification-proposal.md`.
- `internal/provider/` — provider abstraction and implementations; `anthropic/`, `google/`, and `openai/` contain full providers, while `vendor_*.go` contains vendor detection/defaults.
- `internal/provider/factory/` — shared provider/model construction. Use this from CLI, ACP, serve, and other runtimes.
- `internal/tools/` — built-in tools and tool registration.
- `internal/tui/` — Bubble Tea terminal UI.
- `internal/serve/` — unified server runtime: OpenAI-compatible API, Web UI, channels, hooks, cron, memory, and settings APIs.
- `internal/serve/openaiapi/` — HTTP API handlers, slash commands, and tool-output formatting.
- `internal/architecture/` — static architecture guard tests that forbid production code from reintroducing direct `agent.New`/`agent.NewWithLoopConfig` or bypassing canonical Run persistence. Keep the allowlist minimal and documented; run `go test ./internal/architecture` after touching production call sites.
- `internal/session/` and `internal/commondb/` — SQLite sessions, migrations, and shared DB lifecycle.
- `internal/config/` — `settings.json` schema, defaults, and configuration persistence.
- `internal/contextfiles/`, `internal/skills/`, `internal/workflow/` — project context discovery, reusable skills, and workflow execution.
- `internal/sandbox/`, `internal/mcp/`, `internal/acp/`, `internal/a2a/` — sandboxing and protocol integrations.
- `internal/stats/` — usage statistics dashboard and queries.
- `ui/src/` — Svelte application; `App.svelte` routes views, `lib/stores.js` owns shared stores, `lib/preferences.js` owns `zh`/`en` translations, and `style.css` contains global styles.
- `desktop/` — Electron main/preload code, build scripts, and packaging configuration.
- `docs/en/` and `docs/zh/` — bilingual documentation; `docs/en/changelog.md` and `docs/zh/changelog.md` accumulate release notes for all versions, while `docs/changelog_online_en.md` and `docs/changelog_online_zh.md` hold only the current version's changes.
- `scripts/`, `npm/`, `pypi/`, `packaging/` — build and distribution tooling.
- `bin/`, `dist/`, `ui/dist/`, `ui/node_modules/`, `desktop/node_modules/`, and generated package artifacts are build output; do not hand-edit them.

## Architecture notes

- The agent loop constructs prompts, streams provider events, executes tools, records usage, handles compaction, and continues until completion. Reuse it rather than creating parallel agent logic.
- **Target architecture:** one complete Agent Core plus one front-end-neutral Agent Runtime; TUI, WebUI/API, WeChat/Feishu channels, and ACP are thin adapters. Adapters may map protocols, render events, and supply scenario-specific policy/interaction hooks, but must not grow separate Agent/session/tool/MCP/skill/run implementations. This is the implemented boundary, not a future plan; see `docs/proposal/agent-core-runtime-unification-proposal.md` for the migration record and remaining debt.
- Production Agent construction must go through `SessionRuntime.BuildAgent`, `BuildTransientAgent`, or `agentruntime.NewAgentManager`; **do not** add adapter-level complete `agent.Config` assembly or direct `agent.New`/`agent.NewWithLoopConfig`. Registry bootstrap, MCP connection lifecycle, Skill/context bootstrap, Session replay, canonical Run row persistence, and run state machines belong in `internal/agentruntime`. `internal/architecture` enforces this statically.
- Resolve source, mode, capabilities, tools, sandbox, approval, and run policy once in the shared runtime (`ResolveSource`/`ResolvePolicy`). UI display, run records/events, approvals, background/recovery paths, session bind/unbind, and `agent.Config` must use the same resolved values; do not add local fallback/default logic.
- Durable run lifecycle is canonical in `internal/agentruntime`: `ExecutionRuntime.BeginDurable`/`ReattachDurable`/`UpdateDurable`/`CancelDurable`/`FinishDurable` and `RunStore` own run rows, terminal transitions, and start/finish events. Adapters keep protocol/SSE/WebSocket/JSON-RPC projection; use `RunManager.Register` only for in-memory event fan-out on migrated runs.
- `SessionRuntime.Shutdown` cancels the active `ExecutionRuntime` run, waits for terminal state, and releases MCP clients; it must stay idempotent. Adapters should not bypass this for cleanup.
- Pending Approval/Question decisions use `DecisionService`/`DecisionRecord`: persist a request/resolution deadline, and on session load fully replay request/resolution so resolved or expired decisions are not revived; terminalize unrecoverable pending decisions. ACP may re-emit pending request projections after reconnect.
- Reuse persisted session channel bindings (`channel_type`, `channel_id`, and session headers) as the authoritative source for WeChat/Feishu identity. A session bound to WeChat or Feishu has a forced effective mode of `yolo`: request mode, session capability mode, API defaults, `/mode`, WebUI reloads, external/background/recovery paths, sub-agent inheritance, run records, and approval events must not downgrade it to `agent` or `plan`.
- Forced `yolo` controls effective agent mode only. It does not bypass sandbox, allow rules, channel security, or hard high-risk-command protections; model those separately in execution policy.
- Providers stream through the shared provider abstraction. Create providers through `internal/provider/factory`; put vendor-specific behavior in `internal/provider/vendor_*.go` and model compatibility flags, not in CLI/ACP wiring.
- The public SDK boundary is `agent/`. Public packages must not import `internal/`; implementation wiring belongs in `bootstrap/`. Update `example/` when public APIs change.
- Tools should be stateless where practical. Put shared runtime state in managers/registries and pass `context.Context` through execution paths.
- SQLite access must use `internal/commondb`/`internal/session` helpers. Schema changes belong as appended entries in `internal/session/migrations.go`; do not add new direct `CREATE TABLE IF NOT EXISTS` schema setup.
- `settings.json` and `serve.json` are distinct schemas. Preserve existing field meanings. For sparse global settings edits, use `config.SaveGlobalSettingsPatch()` rather than saving a sparse `Settings` struct.
- Serve API and channels reuse the provider factory, agent loop, sessions, tools, sandbox, skills, and MCP. Serve-only configuration belongs in `internal/serve/config.go`.
- In the TUI, completed transcript blocks go to terminal scrollback with `Program.Println`; keep only active streaming content in the managed view. Keep provider/model state synchronized across `App`, settings, and `AgentManager`.
- In the Web UI, use Svelte conditional rendering for interactive mobile behavior (`isMobile`/`sidebarOpen`); reserve CSS media queries for layout. Add translations to both `zh` and `en` maps.

## Anti-fragmentation rules (hard architectural invariants)

The repository must maintain **one Agent Core, one front-end-neutral Agent Runtime, and thin adapters**. “Reuse” means reusing the same runtime path and lifecycle, not merely calling the same low-level Agent loop from multiple independent orchestrators.

- **One construction path:** production code must construct Agents only through `internal/agentruntime` (`SessionRuntime.BuildAgent`, `BuildTransientAgent`, or `agentruntime.NewAgentManager`). Never add a new adapter-local `agent.Config` assembler, `agent.New`, `agent.NewWithLoopConfig`, provider factory, registry builder, MCP/Skills loader, or session replay path.
- **One execution/lifecycle path:** all durable runs must use `ExecutionRuntime`/`RunStore` and its canonical begin, reattach, update, cancel, finish, recovery, and terminalization operations. Adapters may project events and protocol payloads, but may not create a competing run state machine, persistence path, cancellation path, or recovery policy.
- **One source-of-truth resolver:** source, effective mode, capabilities, tools, sandbox, approval/question policy, MCP policy, and run policy are resolved once by the shared Runtime (`ResolveSource`/`ResolvePolicy` and related resolvers). Do not compute display defaults, request defaults, background defaults, recovery defaults, or approval behavior independently in TUI, WebUI, ACP, Channel, CLI, or tests.
- **One session/resource owner:** session binding, replay, Context/Skills/Rules, Registry, MCP clients, Agent resources, and shutdown ownership belong to `SessionRuntime`/`Builder`. Adapters may supply policy and protocol hooks; they must not duplicate resource ownership or cleanup. `SessionRuntime.Shutdown` is the coordinated, idempotent shutdown boundary.
- **One decision model:** Approval/Question pending state, identity, deadlines, first-response-wins, replay, rehydration, expiry, and terminalization use `DecisionService`/`DecisionRecord`. Protocol callbacks and UI rendering remain adapter concerns, but an adapter must not invent a second decision store or revive resolved/expired decisions.
- **One event semantic model:** Agent/Runtime events and terminal states are canonical. SSE, WebSocket, JSON-RPC, Bubble Tea, and platform messages may use different wire formats, but must be projections of the same event/run semantics rather than parallel event producers.
- **Policy, not forks:** entry-point differences must be expressed through `RuntimeSource`, `ExecutionPolicy`, capabilities, and explicit adapter hooks. Do not fork the Agent loop, copy a Session Runtime, add “temporary” adapter defaults, or create a parallel package that will later become a second runtime.
- **No compatibility bypasses:** legacy code may remain only behind a named, documented migration bridge with a clear owner and an architecture test/allowlist entry. New callers must not use the bridge. Every bridge must have a removal condition; “temporary” duplication without an exit condition is prohibited.
- **Canonical persistence boundary:** use `internal/session`/`internal/commondb` for session/database access and the Runtime-owned Run/Decision/Delivery stores for their respective records. Do not add direct schema setup, adapter-owned canonical Run rows, or duplicate durable records to make one entry point work.
- **Required guardrails:** when moving or adding production construction, run persistence, mode/source resolution, decision handling, or shutdown code, update/run `go test ./internal/architecture` and add a focused cross-entry contract test where behavior could drift. Keep architecture allowlists minimal and explain every exception inline.

When a proposed change appears to require a new runtime, manager, lifecycle, resolver, or durable store, stop and first extend the existing `internal/agentruntime` abstraction. If the shared abstraction is genuinely insufficient, change it once and migrate all affected adapters; do not solve the problem separately per entry point.

## Build, test, run, and lint

```bash
make build                         # bin/mothx for the current platform
make run                           # build and run the TUI
make serve                         # build ui/dist, build binary, start serve mode
make install                       # go install the CLI
make test                          # go test -v -race ./...
go test ./internal/tools/...       # focused package tests
go test -run TestName ./path/...   # focused test
make lint                          # golangci-lint run ./...
make fmt                           # gofmt and goimports
make fuzz                          # internal/esm, internal/mcp, internal/util
```

Web UI:

```bash
make ui-install                    # cd ui && npm ci
make ui-build                      # cd ui && npm run build
make ui-dev                        # backend 127.0.0.1:7872 + Vite dev server
make ui-preview                    # preview ui/dist
(cd ui && npm run e2e)              # channel/settings smoke test
```

Desktop:

```bash
make desktop-vendor                # source-build and vendor the runtime
make desktop-build                 # build Electron shell
cd desktop && npm run start         # build/start locally
make desktop-dist-dev-linux        # analogous mac/win targets exist
```

Use focused tests first, then `make test` when the change crosses packages or affects concurrency. Run `make ui-build` for UI changes. Run provider tests (`go test ./internal/provider/...`) after provider/vendor changes. Run `go test ./internal/architecture` after moving production call sites of Agent construction or Run persistence. Real process-boundary tests live with their packages (e.g. `internal/agentruntime`, `internal/acp`, `internal/serve`) and use the subprocess-helper pattern; keep them isolated with temp dirs and localhost addresses.

Release and publishing targets (`make dist*`, `make build-all`, npm/PyPI publish targets, checksums) are not normal development commands; run them only when explicitly requested.

## Coding conventions and working rules

- Read relevant files and nearby tests before editing; preserve unrelated user changes.
- Prefer small, maintainable changes over broad refactors. Follow nearby naming, layout, and error-handling patterns.
- In Go, return errors instead of panicking for normal control flow, pass contexts, keep interfaces stable, and format with `gofmt`/`goimports`.
- Add or update tests when changing behavior. Keep tests deterministic and scoped to the affected package.
- Preserve meaningful trailing spaces in approval command prefixes such as `go `; do not normalize them as comma-separated values.
- When adding a provider/model, update `internal/config/settings.go` defaults and `docs/provider-model-list.md`.
- When adding a Web UI view, register it in `ui/src/App.svelte` and add navigation in `ui/src/components/Sidebar.svelte` as appropriate.
- Keep bilingual user-facing docs synchronized. Append changelog entries for all versions to `docs/en/changelog.md` and `docs/zh/changelog.md`; keep `docs/changelog_online_en.md` and `docs/changelog_online_zh.md` holding only the current version's changes (replace their content with each new release).
- Do not add license headers unless the surrounding file/project already uses them.
- Do not create commits, tags, or pushes unless explicitly requested.

## Agents must not

- Do not expose, print, or commit secrets from `.env`, credentials, keys, tokens, or private configuration.
- Do not rewrite shared remote history or use force-push equivalents.
- Do not use privilege escalation (`sudo`, `su`, `doas`, `pkexec`).
- Do not run destructive cleanup, resets, database drops, or bulk deletion without explicit approval.
- Do not hand-edit generated output under `bin/`, `dist/`, `ui/dist/`, `node_modules/`, `npm/packages/`, or `pypi/.venv-build/`.
- Do not change the `settings.json`/`serve.json` schema or existing field semantics without a deliberate compatibility change.
- Do not bypass `internal/provider/factory` or put vendor behavior in CLI/ACP glue.
- Do not open raw SQLite connections in new code; use the shared DB/session helpers.
- Do not introduce an external HTTP framework into serve code; use the standard `net/http` stack.
- Do not use CSS media queries to toggle interactive Web UI elements.
- Do not import `internal/` packages from the public `agent/` package.
