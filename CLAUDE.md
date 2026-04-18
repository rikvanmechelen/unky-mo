# Unky Mo

Claude Code session orchestrator for MoMA workspace projects.

## Build & Run

```
make install   # Build and install to ~/go/bin/mo
./mo           # Launch TUI (auto-creates tmux session if needed)
./mo list      # List projects
./mo sessions  # List active Claude sessions
./mo hooks install  # Install notification hooks into ~/.claude/settings.json
```

## Architecture

- **Go + Bubbletea** TUI with charmbracelet ecosystem (lipgloss, bubbles)
- **Cobra** CLI with subcommands
- **tmux** session management — TUI runs as window 0, Claude sessions as sibling windows with sidebar panes
- **Unix domain socket** at `/tmp/unky-mo.sock` for real-time notifications from Claude Code hooks
- **Shared state file** at `/tmp/unky-mo-state.json` — main TUI writes, sidebar instances read (1s poll). Includes worktree entries with `parent` field.
- **Session detection** — dual approach: PID liveness checks from `~/.claude/sessions/{PID}.json` + JSONL staleness/message-type analysis for idle detection
- **Config** at `~/.config/unky-mo/config.toml`

## Key Packages

- `cmd/mo/` — CLI entry point (Cobra), all subcommands, tmux auto-launch logic
- `internal/tui/` — Main Bubbletea TUI (app.go model, styles, delegate, keys)
- `internal/tui/sidebar/` — Compact sidebar TUI for tmux panes (model, styles, run)
- `internal/tmux/` — tmux command wrapper (sessions, windows, panes, popups, splits)
- `internal/claude/` — Session detection (live + historical), JSONL parsing, hook management
- `internal/github/` — GitHub PR fetching via `gh` CLI
- `internal/notify/` — Unix socket notification server
- `internal/state/` — Shared JSON state file (atomic write/read between TUI and sidebars)
- `internal/sync/` — Encrypted session sync between machines via private git repo
- `internal/config/` — TOML config loading
- `internal/project/` — Project model, workspace scanner, worktree support

## Claude Session Data

- **Live sessions**: `~/.claude/sessions/{PID}.json` — PID, SessionID, CWD, name
- **Session history**: `~/.claude/projects/{encoded-path}/{SessionID}.jsonl` — full conversation
- **Path encoding**: Claude replaces `/`, `_`, and `.` with `-` in directory names
  - e.g. `/Users/rvanmech/workspace/mla_wrapper_app` → `-Users-rvanmech-workspace-mla-wrapper-app`
  - e.g. `/Users/.../unky-mo.worktrees/testing_worktrees` → `-Users-...-unky-mo-worktrees-testing-worktrees`
- **Session title**: stored as `{"type":"custom-title","customTitle":"..."}` entries in JSONL (can appear anywhere in file, last one wins)
- **Idle detection**: checks `stop_reason` on last assistant message — `end_turn` = idle, `tool_use` = still working. Falls back to JSONL staleness >120s for permission prompt edge cases. Do NOT use simple message type checks — `type=assistant` with `stop_reason=tool_use` means Claude is mid-turn.

## tmux Layout

```
tmux session "mo"
├── window 0: "mo" (main TUI — full screen)
├── window 1: "moma-apps-rails"
│   ├── pane 0: Claude Code session
│   ├── pane 1: sidebar (mo sidebar)
│   └── pane 2+: terminal splits (optional, via `t`)
├── window 2: "moma-go"
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 3: "unky-mo@feature-auth" (worktree session)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
└── ...
```

### Terminal Drawer

The sidebar manages a collapsible terminal drawer below the Claude pane (pane `.0`). Only one terminal tab is visible at a time — inactive tabs are stored in hidden tmux windows via `break-pane`/`join-pane` and swapped in on demand. The sidebar tracks terminal pane IDs (`%N` format, stable across moves) and prunes dead panes on each 1s refresh tick.

## TUI Key Handling

Keys in `mo` are handled by **two separate Bubbletea programs**, not one. When debugging a keystroke, first identify which program was focused when the key was pressed.

- **Main TUI** (`internal/tui/app.go`) — runs in tmux window 0. Dashboard has side-by-side layout: project list (left) + active sessions panel (right). Project detail has a **branches list** (left) + PRs (right); each branch row is marked `●` main checkout, `⎇` has worktree, or `·` neither, with sessions nested under it. `←`/`→` switch panels. Keys: `enter` (smart resume), `w` (open row's branch as worktree), `m` / `M` (checkout in main; `M` stashes first), `W` (prompt for a brand-new branch name), `n` (new session), `a`, `r`, `o`, `c`, `?`, `ctrl+r`, `d` (detach — leaves tmux session running), `esc`, `q`.
- **Sidebar** (`internal/tui/sidebar/model.go`) — runs as pane `.1` in each project window. Has two focus sections:
  - **Sessions section**: `up`/`down`, `enter` (switch window or focus terminal tab), `t` (toggle terminal drawer), `T` (new terminal tab), `tab`/`shift+tab` (cycle tabs), `x` (close terminal), `` ` `` (popup), `s` (sync push), `ctrl+r` (restart).
  - **Files section** (arrow down past sessions): `up`/`down` (navigate files, skip directory nodes), `enter`/`d` (git diff popup), `v` (open in `$EDITOR` popup), `o` (open in VS Code / default editor).

`t`, `T`, `tab`, `x`, `s`, `d`, `v`, and `` ` `` are handled **only** in the sidebar; the main TUI ignores them. "Fixing" terminal-open behavior in `internal/tui/app.go` won't change anything — edit the sidebar.

## tmux Gotchas

- **`split-window` without `-c` does not inherit the target pane's cwd.** Modern tmux (3.2+) uses the session's launch directory instead. Always pass `-c <path>` explicitly when creating panes that need a specific cwd. See `internal/tmux/client.go:SplitWindow`.
- **Format strings in `-c` don't expand against the `-t` target.** `tmux split-window -t foo:bar -c "#{pane_current_path}"` expands the format against whatever pane tmux considers "current" server-wide (typically the most recently active pane of the attached client), **not** against the target pane. Always pass a literal path string to `-c`.
- **`tmux display-message -p ...` without `-t` uses the attached client's focused pane**, not the calling pane. From a subprocess (like `mo sidebar`), use `TMUX_PANE` for pane-specific queries: `tmux display-message -t "$TMUX_PANE" -p '#{window_name}'`. See `internal/tui/sidebar/model.go:NewModel`.

## Worktree Session Detection

Worktrees use the `<project>.worktrees/<branch>` directory convention. Session detection matches CWDs containing `.worktrees/` back to parent projects by stripping the suffix to recover the main project path.

**Data flow**: `updateProjectStatuses()` detects worktree CWDs → stores in `m.activeWorktrees` → `writeStateFile()` emits state entries with `Parent` field → sidebar reads and renders them indented under the parent.

- **Window naming**: worktree windows are named `<project>@<branch>` (e.g. `unky-mo@feature-auth`)
- **State file entries**: worktree entries have `name: "@branch"`, `parent: "project"`, and their own status
- **Session matching is path-based throughout.** The sidebar's `refreshFromSessions()` fallback must compare `item.Path` (filesystem path) against `session.CWD`, never `item.WindowName` (display name). Mixing these up silently breaks detection.

## Notification Flow

```
Claude hooks → notify-hook.sh → Unix socket → Main TUI → state file → Sidebars
```

- Hooks installed in `~/.claude/settings.json` with `# unky-mo` marker comment
- Notification types: `idle_prompt`, `permission_prompt` (from Claude), `session_stop` (from Stop hook)
- State file written atomically (temp + rename) on every 5s poll and on notification events

## Conventions

- Binary name: `mo`
- tmux session name: auto-detected from current session, falls back to `mo` (configurable)
- Hook marker: `# unky-mo` comment in Claude settings hook commands
- Colors tuned for dark terminal backgrounds (~#14191E)
- All keyboard shortcuts visible in persistent footer bars
- Circular list navigation (wraps top↔bottom)
- `ctrl+r` restarts TUI + all sidebars (for dev workflow — picks up new binary)
- Mouse support enabled automatically on tmux session creation
- `exec claude` used in panes so windows auto-close when Claude exits (pane-exited hook)
- Error messages in TUI persist until keypress; success messages auto-clear after 4s
- Left/right arrow keys switch between panels (dashboard sessions, project detail PRs)
- Always use `make install` (not just `go build`) so `ctrl+r` picks up the new binary everywhere
- `git diff --color=always` for colored diffs in popups (piped to `less -R`)

## Commit Messages

- Lowercase first word, imperative mood ("add", "fix", "use", "make")
- No period at the end
- Short single line, ~50-70 chars
- Jira ticket reference at end when applicable (e.g. `OP-175`)
- Examples: `add sidebar terminal split and popup`, `fix idle detection for stale sessions`
- Never add Co-Authored-By lines

## CLI Commands

```
mo                    # Launch TUI (auto-creates/attaches tmux session)
mo list               # List all discovered projects
mo sessions           # List active Claude sessions with PIDs
mo start <project>    # Start Claude session in tmux window + sidebar
mo resume <project>   # Resume most recent session (live or historical)
mo attach <project>   # Switch to project's tmux window
mo scan               # Re-scan workspace directories
mo hooks install      # Install notification hooks
mo hooks uninstall    # Remove hooks
mo hooks status       # Check hook installation
mo sync init <url>    # Connect to private GitHub repo for session sync
mo sync push <proj>   # Push a session (encrypted) to sync repo
mo sync pull [proj]   # Pull sessions from sync repo
mo sync list          # List available synced sessions
mo debug <project>    # Dump session/worktree debug info for a project
mo sidebar            # Run sidebar TUI (internal, launched in panes)
mo version            # Print version
```
