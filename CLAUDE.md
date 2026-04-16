# Unky Mo

Claude Code session orchestrator for MoMA workspace projects.

## Build & Run

```
go build -o mo ./cmd/mo
./mo          # Launch TUI (auto-creates tmux session if needed)
./mo list     # List projects
./mo sessions # List active Claude sessions
./mo hooks install  # Install notification hooks into ~/.claude/settings.json
```

## Architecture

- **Go + Bubbletea** TUI with charmbracelet ecosystem (lipgloss, bubbles)
- **Cobra** CLI with subcommands
- **tmux** session management — TUI runs as window 0, Claude sessions as sibling windows with sidebar panes
- **Unix domain socket** at `/tmp/unky-mo.sock` for real-time notifications from Claude Code hooks
- **Shared state file** at `/tmp/unky-mo-state.json` — main TUI writes, sidebar instances read (1s poll)
- **Session detection** — dual approach: PID liveness checks from `~/.claude/sessions/{PID}.json` + JSONL staleness/message-type analysis for idle detection
- **Config** at `~/.config/unky-mo/config.toml`

## Key Packages

- `cmd/mo/` — CLI entry point (Cobra), all subcommands, tmux auto-launch logic
- `internal/tui/` — Main Bubbletea TUI (app.go model, styles, delegate, keys)
- `internal/tui/sidebar/` — Compact sidebar TUI for tmux panes (model, styles, run)
- `internal/tmux/` — tmux command wrapper (sessions, windows, panes, popups, splits)
- `internal/claude/` — Session detection (live + historical), JSONL parsing, hook management
- `internal/notify/` — Unix socket notification server
- `internal/state/` — Shared JSON state file (atomic write/read between TUI and sidebars)
- `internal/config/` — TOML config loading
- `internal/project/` — Project model, workspace scanner, worktree support

## Claude Session Data

- **Live sessions**: `~/.claude/sessions/{PID}.json` — PID, SessionID, CWD, name
- **Session history**: `~/.claude/projects/{encoded-path}/{SessionID}.jsonl` — full conversation
- **Path encoding**: Claude replaces both `/` and `_` with `-` in directory names
  - e.g. `/Users/rvanmech/workspace/mla_wrapper_app` → `-Users-rvanmech-workspace-mla-wrapper-app`
- **Session title**: stored as `{"type":"custom-title","customTitle":"..."}` entries in JSONL (can appear anywhere in file, last one wins)
- **Idle detection**: if JSONL not modified in >60s, session is stalled; also checks if last message type is `assistant` (Claude finished turn)

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
└── ...
```

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

## Commit Messages

- Lowercase first word, imperative mood ("add", "fix", "use", "make")
- No period at the end
- Short single line, ~50-70 chars
- Jira ticket reference at end when applicable (e.g. `OP-175`)
- Examples: `add sidebar terminal split and popup`, `fix idle detection for stale sessions`

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
mo sidebar            # Run sidebar TUI (internal, launched in panes)
mo version            # Print version
```
