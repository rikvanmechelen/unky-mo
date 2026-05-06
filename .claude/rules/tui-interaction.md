---
paths:
  - "internal/tui/**"
---

# TUI Interaction: Keys, Prompts, Terminal Drawer, Sidebar

## Two separate programs

Keys in `mo` are handled by **two separate Bubbletea programs**, not one. When debugging a keystroke, first identify which program was focused when the key was pressed.

### Main TUI (`internal/tui/app.go`)

Runs in tmux window 0. Three screens:

- `ScreenDashboard` — 50/50 split: project list left, sessions-on-top / tickets-below right. Right-panel focus has two sub-sections via `dashRightFocus` and up/down crosses the boundary.
- `ScreenProject` — branches list left, PRs right. Branch rows marked `●` main / `⎇` worktree / `·` neither with optional `[merged]`/`[gone]` tags.
- `ScreenTicket` — ticket detail (see `rules/tickets.md`).

`←`/`→` switch panels. Dashboard/project-detail keys: `enter` (smart resume / open ticket detail), `w` (worktree on branch row; lift-to-new-worktree on `br-session` row), `m`/`M` (checkout in main; `M` stashes first), `W` (new branch prompt), `n` (new session — prompts switch/park+new/concurrent), `x` (cleanup), `a`, `r`, `o` (open PR or ticket URL), `c`, `?`, `ctrl+r`, `s` (suspend — tmux detach-client), `esc`, `q`.

### Sidebar (`internal/tui/sidebar/model.go`)

Runs as pane `.1` in each project window. Two focus sections:

- **Sessions section**: `up`/`down`, `enter` (switch window or focus terminal tab), `t` (toggle terminal drawer), `T` (new terminal tab), `tab`/`shift+tab` (cycle tabs), `x` (close terminal), `` ` `` (popup), `s` (sync push), `ctrl+r` (restart).
- **Files section** (arrow down past sessions): `up`/`down` (navigate files), `enter`/`d` (git diff popup for changed files; open editor for clean files), `v` (open in `$EDITOR` popup), `o` (open in VS Code / default editor), `.` (toggle between "Changed" and "Files" full-tree mode), `enter`/`space` on directory nodes (expand/collapse in full-tree mode).

`t`, `T`, `tab`, `x`, `s`, `d`, `v`, and `` ` `` are handled **only** in the sidebar; the main TUI ignores them. "Fixing" terminal-open behavior in `internal/tui/app.go` won't change anything — edit the sidebar.

## Prompt conventions

Every interactive prompt in the TUI follows one of three shapes. New prompts MUST pick a shape — don't invent bespoke letter schemes.

The capital letter in the `[y/N]` label is the **default**, and a bare `enter` takes that default. Because every yes/no prompt in Mo is destructive (kills sessions, deletes branches, kills external processes), the default is always N — `enter` cancels.

- **Yes/no confirmations** — destructive. Question ends with `[y/N]`. Renderer uses `yesNoBindings("<verb>")`. Handler accepts **only** `y`/`Y` → confirm; `n`/`N`/`enter`/`esc`/`escape` → cancel. No mnemonic letters.
- **Multi-option menus** — 2+ non-cancel outcomes. Renderer wraps action bindings with `withCancel([]footerBinding{...})` so `[n] cancel` is always appended. Handler accepts each mnemonic letter (lower + upper case), and `n`/`N`/`enter`/`esc`/`escape` → cancel.
  - **Destructive menus** (e.g. cleanup worktree `[w]/[b]`) — `enter` cancels.
  - **Non-destructive menus** (e.g. new-session `[s]/[p]/[c]`) — may bind `enter` → safest primary option.
- **Free-text input prompts** (e.g. `W` new branch). `enter` submits, `esc` cancels.

Exception: the ticket picker's remember prompt uses `y` remember / `o` just-this-once / `n` cancel.

## Terminal drawer

The sidebar manages a collapsible terminal drawer below the Claude pane (pane `.0`). Only one terminal tab is visible at a time — inactive tabs are parked in a dedicated mo-terms tmux session scoped per-window via `Model.termSession()` → `mo-terms-<windowID>`. Panes move in and out via cross-session `break-pane`/`join-pane`. The sidebar tracks terminal pane IDs (`%N` format, stable across moves) and prunes dead panes on each 1s refresh tick.

The main TUI runs a parallel sweep on each 5s `sessionTick` (`pruneOrphanedTermSessions` → `orphanedTermSessions`) that kills `mo-terms-<N>` sessions whose window `@N` no longer exists.

Backtick (`` ` ``) opens a floating popup that attaches a nested tmux client to `mo-terms`. `mo-terms` sets its `key-table` to `popup-keys`, where `` ` `` → `detach-client` (closes popup without killing shell). Tab/Shift+Tab pass through to the shell for tab completion. The outer `mo` client stays on `root` table so backticks remain typeable in Claude panes.

## Sidebar responsibilities (1s tick)

- **Usage strip** — reads `UsageState` from state file, renders 5h / weekly bars. No API calls.
- **Per-session token counter** — `usage.SessionTokens` against live session's JSONL.
- **Active shells** — `claude.ActiveShellsForSession` lists Claude's Bash-tool subprocesses.
- **Changed files** — `git status --porcelain` + `git diff --numstat` populates the Files section (changed-only mode). Also feeds `gitStatusMap` for status markers in full-tree mode.
- **Full file tree** — `git ls-files` on a 5s cadence (every 5th tick) builds a `dirNode` tree with expand/collapse state preserved across rebuilds. Toggled with `.` key.
- **Sync status** — read once on init via `moSync.ListLocal` (no network), re-checked after a local push.
