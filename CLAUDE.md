# Unky Mo

Claude Code session orchestrator for MoMA workspace projects.

## Build

```
go build -o mo ./cmd/mo
```

## Run

```
./mo          # Launch TUI
./mo list     # List projects
./mo sessions # List active Claude sessions
./mo hooks install  # Install notification hooks
```

## Architecture

- **Go + Bubbletea** TUI with charmbracelet ecosystem
- **tmux** session management — TUI runs as window 0, Claude sessions as sibling windows
- **Unix domain socket** at `/tmp/unky-mo.sock` for real-time notifications from Claude Code hooks
- **Session detection** via `~/.claude/sessions/{PID}.json` with PID liveness checks
- Config at `~/.config/unky-mo/config.toml`

## Key Packages

- `cmd/mo/` — CLI entry point (Cobra)
- `internal/tui/` — Bubbletea TUI (app model, styles, delegate, keys)
- `internal/tmux/` — tmux command wrapper
- `internal/claude/` — Session detection, hook management
- `internal/notify/` — Unix socket notification server
- `internal/config/` — TOML config loading
- `internal/project/` — Project model, workspace scanner, worktree support

## Conventions

- Binary name: `mo`
- tmux session name: `mo` (configurable)
- Hook marker: `# unky-mo` comment in Claude settings
