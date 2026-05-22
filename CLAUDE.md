# Unky Mo

Claude Code session orchestrator for MoMA workspace projects.

## Build & Run

```
make install   # Build and install to ~/go/bin/mo
./mo           # Launch TUI (auto-creates tmux session if needed)
./mo list      # List projects
./mo sessions  # List active Claude sessions
./mo hooks install  # Install status hooks into ~/.claude/settings.json
```

## Architecture

- **Go + Bubbletea v2** TUI with charmbracelet ecosystem (lipgloss v2, bubbles v2) — imports via `charm.land/*/v2`
- **Cobra** CLI with subcommands
- **tmux** session management — TUI runs as window 0, Claude sessions as sibling windows with sidebar panes
- **Unix domain socket** at `/tmp/unky-mo.sock` for real-time status events from Claude Code hooks
- **Shared state file** at `/tmp/unky-mo-state.json` — main TUI writes, sidebar instances read (1s poll). Includes worktree entries with `parent` field.
- **Session status detection** — hybrid approach: Claude Code hooks (primary, real-time) + fsnotify JSONL watcher (reconciliation) + PID liveness checks (cleanup). Central `status.Manager` is the single source of truth. No time-based heuristics.
- **Config** at `~/.config/unky-mo/config.toml`

## Key Packages

- `cmd/mo/` — CLI entry point (Cobra). Each `RunE` builds an `ops.Context` and calls an `ops.*` function — thin wrappers by design.
- `internal/ops/` — **Domain operations shared by CLI and TUI.** `Context` + interfaces (`TmuxClient`, `ClaudeReader`) + functions. No bubbletea types here — plain functions, testable against gomock fakes.
- `internal/tui/` — Main Bubbletea TUI (app.go model, styles, delegate, keys). Each `tea.Cmd` closure is a 5-line adapter over an `ops.*` call.
- `internal/tui/sidebar/` — Compact sidebar TUI for tmux panes. Has its own `TmuxClient`/`ClaudeReader` interfaces since it runs in a separate process and needs a different method subset.
- `internal/tmux/` — tmux command wrapper (sessions, windows, panes, popups, splits). Adapted into `ops.TmuxClient` by `ops.NewTmuxClientAdapter`.
- `internal/claude/` — Session detection (live + historical), JSONL parsing, hook management. Adapted into `ops.ClaudeReader` by `ops.NewDefaultClaudeReader`.
- `internal/status/` — **Session status state machine.** `Manager` receives hook events + fsnotify JSONL changes + PID liveness signals. Single source of truth for active/idle/permission status. `Watcher` monitors JSONL files via fsnotify. `ReadJSONLStatus` reads JSONL tail without time-based heuristics. `ParseHookPayload` handles both V2 unified and legacy hook formats.
- `internal/exec/` — `Commander` interface + gomock mock. Shared shell-out seam used by `internal/github`; `ops.Context` also carries one.
- `internal/tickets/` — Provider-agnostic ticket model + `internal/tickets/jira/` Atlassian Cloud provider (uses `/rest/api/3/search/jql`, NOT the removed v2 endpoint).

## Testing

- **Always run `make test` (or `go test ./...`) after any code change.** Failing tests are a signal — either the code regressed, or the test encoded a behavior that has legitimately changed. Never skip, delete, or comment out a test without first understanding what it's asserting. If a test is wrong, fix the test; if the code is wrong, fix the code. Do not "fix" a failing test by weakening its assertion to make it pass.
- See `.claude/rules/testing.md` for mock patterns, integration tests, ops testing, and the sidebar testing seam.

## Conventions

- Binary name: `mo`
- tmux session name: auto-detected from current session, falls back to `mo` (configurable)
- Hook marker: `# unky-mo` comment in Claude settings hook commands
- Colors tuned for dark terminal backgrounds (~#14191E)
- All keyboard shortcuts visible in persistent footer bars
- Circular list navigation (wraps top↔bottom)
- `ctrl+r` forces an in-process refresh (re-poll sessions, rebuild detail branches, rewrite state file — no network, no binary reload)
- `ctrl+alt+r` restarts TUI + all sidebars (dev workflow — picks up freshly-installed binary)
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
