# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.91

### ✨ New Features

- **ACP Session Admission Control**
  - ACP prompts now acquire the shared session runtime lock and check the durable active-run row before starting, serializing the ACP entry point with the TUI, WebUI, and channels.
  - The runtime lock is held for the full lifetime of an admitted run so no other adapter can race its terminal persistence with a new run; a session with an active run is rejected with `session already has an active run`.

- **WebUI Server-Allocated Session IDs**
  - The WebUI now requests its session ID from the server (`POST /api/session-id`) instead of generating one in the browser, making session identity canonical while preserving the delayed-creation "new chat" UX.
  - Server-side allocation deduplicates against reserved and existing session IDs and expires stale reservations after 10 minutes; browser-side random ID generation remains only for run request keys.

- **Session Duplicate ID Rejection**
  - Creating a session with an ID that already exists now fails with `ErrSessionIDExists` instead of silently merging the new header into the old session and forking the conversation.
  - Auto-generated IDs retry on collision (up to 8 attempts); the session header write uses a plain INSERT so duplicate IDs are detected reliably.

### 🔧 Improvements

- **Default Mode is YOLO**
  - New installs and empty-mode fallbacks now use `yolo` instead of `agent`: `settings.json` `defaultMode`, Serve/API `DefaultMode`, CLI/TUI/ACP/WebUI, public SDK `Builder`, and `agentruntime` policy resolution.
  - Explicit `--mode`, persisted session mode, and WeChat/Feishu forced `yolo` still take precedence. Existing configs that set `defaultMode: "agent"` are unchanged.

- **Unified Session Creation**
  - TUI, serve, and CLI now create sessions through `agentruntime.CreateSession` instead of constructing `session.New(...).Init()` directly, centralizing session creation in the shared runtime.
