# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.92

### ✨ New Features

- **Session Fork & Message Branching**
  - Sessions can now fork from any message and branch into alternative continuations; execution intent is wired through fork paths with a runtime lock.
  - Session schema and migrations, run/response stores, entry handling, ESM guidance, the background run coordinator, and the dispatcher all support forks; TUI session commands/runtime and Web UI chat/sessions views are updated.

- **WebUI Trajectory View & Session Log Export**
  - A read-only trajectory projection in the Serve Web UI organizes session messages, tool events, and run events by run/turn/step without forking Agent/Runtime state.
  - Server-side session trajectory and export endpoints live under the existing session route, and the session header gains a log download action (optionally including descendant sessions).

- **WebUI Modernization with shadcn-svelte & lucide**
  - Added Tailwind CSS v4 and shadcn-svelte style components (button, badge, card, dialog, input, switch, tabs, tooltip) with the `$lib` alias and `cn()` utility.
  - Replaced text glyphs with lucide-svelte icons across the sidebar, sessions, settings, and list editor views; added a brand logo mark, a workspace filter menu (all/projects/ungrouped), and a Ctrl+Shift+K new-chat shortcut.

- **Authored-commit Co-Author Setting**
  - A new global "authored" setting (default off) appends the MothX co-author trailer to system prompts, guiding the model to include `Co-Authored-By: MothX <harness@mothx.net>` when creating git commits.
  - Wired through config persistence, the TUI settings dialog, and the Web UI settings form with bilingual labels.

- **New Provider Models**
  - DeepSeek (anthropic + openai): added `deepseek-v4-flash-vision-exp` with 1M context, text+image input, and no default max_tokens.
  - Volcengine codingplan: added `doubao-seed-evolving` with 1M context and text+image input.

### 🔧 Improvements

- **Default Mode is YOLO**
  - New installs and empty-mode fallbacks now use `yolo` instead of `agent`: `settings.json` `defaultMode`, Serve/API `DefaultMode`, CLI/TUI/ACP/WebUI, public SDK `Builder`, and `agentruntime` policy resolution.
  - Explicit `--mode`, persisted session mode, and WeChat/Feishu forced `yolo` still take precedence. Existing configs that set `defaultMode: "agent"` are unchanged.

- **Native Directory Picker for Serve**
  - Serve now opens the operating system's native directory picker (macOS, Windows, Unix) for selecting a working directory instead of typing a path.

- **Unified Settings Components**
  - Extracted shared `SettingsField`, `SettingsSection`, `SettingsSwitch`, and `ProviderEditorDetail` components and unified settings view layout/styling across AppSettings, ServeConfig, Channels, Env, Logs, Memory, Overview, SkillHub, and WorkDir.

### 🐛 Bug Fixes

- **WebUI Sidebar & Session ID Fixes**
  - Sidebar collapsed state now persists in localStorage, with collapse/expand labels.
  - Removed the unused trajectory timeline mode, state, translations, and layout helper.
  - Fixed `AllocateSessionID` to treat "not registered in DB" as available after `sessions.db` exists, with a regression test.
