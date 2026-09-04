# Changelog (Current Version)

This file contains the changes for the **current version only**. The full history of all versions lives in [docs/en/changelog.md](en/changelog.md).

## v1.2.100

### ✨ New Features

- **WebUI: Slash Command Suggestions in the Chat Composer**
  - Typing `/` in the chat input now shows a suggestion dropdown covering every supported slash command (`/clear`, `/mode`, `/model`, `/defaultModel`, `/models`, `/sessions`, `/status`, `/compact`, `/delegate`, `/alloweditpath`, `/allowautoedit`, `/workflows`, `/skill`, `/skills`, `/rule`, `/esm`, `/help`), with a dedicated subcommand filter for `/esm` (objective/edit/pause/resume/clear/guide).
  - Navigate with ↑/↓, complete with Tab or Enter (Enter sends the prompt when the input already matches the selection), dismiss with Esc, or click an entry; accepting a suggestion places the cursor at the end of the inserted command. The composer keeps proper combobox/listbox ARIA state (`aria-expanded`, `aria-activedescendant`, `aria-selected`).
  - Suggestions are suppressed while a run is active, the API is disabled, or the input spans multiple lines.

### 🐛 Bug Fixes

- **TUI: Prompts Submitted During an Active Run Are Queued Instead of Replacing It**
  - A session allows exactly one foreground execution at a time. Previously, submitting input while a run was active replaced the in-memory run handle, orphaning the active run's terminal cleanup and its runtime lease. Such submissions are now queued in the TUI, and the next queued prompt starts only after the preceding run reaches its canonical terminal state and releases its lease — across every terminal branch (success, failure, incomplete, and cancellation).
  - Queued prompts retain their Runtime-prepared attachments (`agentruntime.PreparedInput`) and re-enter through the same input contract, so attachments survive the delay unchanged.

### ✅ Tests

- TUI: new coverage asserting that input during an active run queues without replacing the lease owner, and that the queued prompt starts only after the cancellation path finalizes the durable run and releases its lease.
