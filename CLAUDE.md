# Unky Mo

Claude Code session orchestrator for MoMA workspace projects.

## Build & Run

```
make install   # Build and install to ~/go/bin/mo
./mo           # Launch TUI (auto-creates tmux session if needed)
./mo list      # List projects
./mo sessions  # List active Claude sessions
./mo hooks install  # Install status hooks into ~/.claude/settings.json (the TUI also does this on every start)
./mo web       # Serve the web dashboard (the TUI auto-starts it in a detached `mo-web` tmux session) (sessions, worktrees, PRs, tickets, usage) + a live chat view per session
```

## Architecture

- **Go + Bubbletea v2** TUI with charmbracelet ecosystem (lipgloss v2, bubbles v2) — imports via `charm.land/*/v2`
- **Cobra** CLI with subcommands
- **tmux** session management — TUI runs as window 0, Claude sessions as sibling windows with sidebar panes
- **Unix domain socket** at `/tmp/unky-mo.sock` for real-time status events from Claude Code hooks
- **Shared state file** at `/tmp/unky-mo-state.json` — main TUI writes, sidebar instances read (1s poll). Includes worktree entries with `parent` field.
- **Session status detection** — hybrid approach: Claude Code hooks (primary, real-time) + fsnotify JSONL watcher (reconciliation) + PID liveness checks (cleanup) + `claude agents --json`'s `waiting`/`input needed` status (backstop for `question` when the PreToolUse hook is missed). Central `status.Manager` is the single source of truth. No time-based heuristics. Status includes a `question` state for sessions blocked on an interactive tool (e.g. `AskUserQuestion`) — **hooks are the only signal that can ever catch this**, since a tool blocked on human input is, by definition, not yet in the JSONL transcript (append-only, completed-turns-only) for any reconciliation pass to find. See `.claude/rules/session-detection.md` for the hook-payload-parsing gotcha this surfaced (`tool_name`/`tool_input` live nested under `hook_input`, never at the top level — don't trust a top-level field without checking what `status-hook.sh` actually sends).
- **Config** at `~/.config/unky-mo/config.toml`
- **Startup checks** (`cmd/mo/setup.go:startupChecks`) — run before every TUI start (including `ctrl+alt+r`), idempotent, never fatal, results shown in the status bar: writes the embedded hook script + ensures the exact V2 hook set, then (unless `[web] disabled`) (re)spawns `mo web --addr <[web] addr, default :7890>` in the detached `mo-web` tmux session via `ops.EnsureWebServer`. Always respawning is deliberate — it's how `ctrl+alt+r` after `make install` picks up web changes. Without a `mo web auth set` login, a non-loopback addr is narrowed to `127.0.0.1` (see below).
- **Web dashboard** (`mo web`) — a separate process, not embedded in the TUI. `status.Manager` and the hook socket are owned exclusively by the running main TUI, so the web process never touches either — session status comes from the shared state file, and the dashboard's own lists (sessions/projects/PRs/tickets) are plain-polled from the browser. The per-session **chat view** is pushed, not polled: it live-tails the session's JSONL transcript (reusing `status.Watcher`'s fsnotify-on-directory mechanism, independent of `status.Manager`) over SSE, and a prompt box injects text into the real session via a literal-mode `tmux send-keys` (gated on the session's state-file status being `idle` *or* `question` — the latter lets you answer a pending `AskUserQuestion` from the browser, surfaced as a banner above the composer since it can't come from the transcript either; genuine `permission` dialogs stay rejected until Claude Code's `PermissionRequest` hook payload shape is confirmed to carry enough to render safely). The web is a **full stand-in for the TUI, destructive actions included**: project details list each checkout's live + recent sessions, and the dashboard can start a session, resume one, create a worktree and launch in it, and stop a session (`POST /api/projects/{name}/sessions`, `DELETE /api/sessions/{windowID}`). It can also remove a worktree and/or delete a branch, and move a session into a new worktree (`POST /api/projects/{name}/cleanup` and `/lift`, `handlers_worktrees.go`; the TUI's `x` and `w`). A live session in the worktree, or uncommitted changes at a lift's source, comes back as a 409 the browser turns into the TUI's confirm menu. When the primary window is busy, the launch returns 409 + choices, which the browser shows as the TUI's switch / replace (park) / run-alongside menu before re-posting. These call the same `ops.*` functions as the TUI, with `NoSwitch` set so the attached tmux client isn't moved (`focus: true` opts back in). The browser never sends a cwd or a command: the cwd comes from project + branch, and the command from the configured `[[agent]]` key. That's injection safety, not a capability limit. Optional HTTP basic auth set via `mo web auth set` — stored as a username + salted PBKDF2-SHA256 hash in `~/.config/unky-mo/web-auth.toml` (0600, never plaintext); `web.BasicAuth` wraps the whole server and caches successful logins in memory so dashboard polling doesn't pay the KDF each request. `mo web` refuses a non-loopback `--addr` without a login configured, since the prompt box can drive a live agent. See `internal/web/`. (An earlier PTY-attach terminal-mirror design was tried and removed — it required resizing the user's real shared tmux pane to fit the browser window, which was a disliked side effect; the chat view replaced it entirely.)

## Key Packages

- `cmd/mo/` — CLI entry point (Cobra). Each `RunE` builds an `ops.Context` and calls an `ops.*` function — thin wrappers by design.
- `internal/ops/` — **Domain operations shared by CLI and TUI.** `Context` + interfaces (`TmuxClient`, `ClaudeReader`) + functions. No bubbletea types here — plain functions, testable against gomock fakes.
- `internal/tui/` — Main Bubbletea TUI (app.go model, styles, delegate, keys). Each `tea.Cmd` closure is a 5-line adapter over an `ops.*` call.
- `internal/tui/sidebar/` — Compact sidebar TUI for tmux panes. Has its own `TmuxClient`/`ClaudeReader` interfaces since it runs in a separate process and needs a different method subset.
- `internal/tmux/` — tmux command wrapper (sessions, windows, panes, popups, splits). Adapted into `ops.TmuxClient` by `ops.NewTmuxClientAdapter`.
- `internal/claude/` — Session detection (live + historical), JSONL parsing, hook management. Adapted into `ops.ClaudeReader` by `ops.NewDefaultClaudeReader`.
- `internal/status/` — **Session status state machine.** `Manager` receives hook events + fsnotify JSONL changes + PID liveness signals. Single source of truth for active/idle/permission/question status. `Watcher` monitors JSONL files via fsnotify. `ReadJSONLStatus` reads JSONL tail without time-based heuristics. `ParseHookPayload` handles both V2 unified and legacy hook formats — `PreToolUse`'s `tool_name`/`tool_input` must be unmarshaled out of the nested `hook_input` blob (status-hook.sh forwards Claude's stdin JSON wholesale there), not read as top-level fields. `isInteractiveTool` special-cases tool names known to block on a human choice (currently just `AskUserQuestion`) into `StatusQuestion` instead of the default `StatusActive`.
- `internal/exec/` — `Commander` interface + gomock mock. Shared shell-out seam used by `internal/github` and `internal/gitfiles`; `ops.Context` also carries one.
- `internal/gitfiles/` — A checkout's changed files (`git status --porcelain=v2 --branch -z` + `git diff HEAD --numstat -z`, plus line counts for untracked files), its full file list (`git ls-files`) and its upstream ahead/behind, for the web chat view's Files panel. Pure `-z` parsers plus `Commander`-driven fetchers that always run at the repo root, since a session's cwd may be a subfolder. The sidebar still has its own older parsing in `sidebar/model.go`.
- `internal/tickets/` — Provider-agnostic ticket model + `internal/tickets/jira/` Atlassian Cloud provider (uses `/rest/api/3/search/jql`, NOT the removed v2 endpoint).
- `internal/web/` — HTTP/JSON dashboard + chat view (`mo web`). Scoped interfaces (`StateReader`, `ProjectLister`, `WorktreeReader`, `PRClient`, `TicketSource`, `PromptSender`, `SessionHistory`, `SessionOps`, `GitFiles`) mirror the `ops.Context` pattern — `SessionOps` is a thin adapter over an `*ops.Context` for launch/resume/park/sibling/worktree/stop/cleanup/lift (`handlers_sessions.go`, `handlers_worktrees.go`), mocked under `internal/web/mocks/`. Small TTL caches (`ttlCache`) decouple browser poll frequency from rate-limited `gh`/Jira calls. The chat view's `transcriptCursor`/`transcriptHub` (`transcript.go`) tail a session's JSONL file and fan out new messages verbatim (as raw JSON) to SSE subscribers — the backend stays schema-agnostic; the frontend (`static/chat.js`) owns all interpretation of the JSONL shape (text/tool_use/tool_result/diffs). The Files panel (`static/files.js`, `handlers_files.go`) polls `/api/sessions/{windowID}/files` and `/tree`, which resolve the checkout from the state file's `path` (never from the request). `/api/state` also fills in the `branch` of live rows the TUI left empty, because the TUI only records branches for strays and the sidebar renders the field whenever it's set. A pending interactive question (status `question`) rides the separate `/api/state` poll instead, since it's never in the JSONL — rendered as a banner above the composer, not a transcript message. Frontend is hand-written vanilla JS/CSS embedded via `go:embed` under `internal/web/static/` — intentionally bare, a design pass is a separate follow-up.

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
- `ctrl+alt+r` restarts TUI + all sidebars (dev workflow — picks up freshly-installed binary). The TUI quits and `syscall.Exec`s itself in place (same PID and pane). Don't go back to running the new binary as a child via `tea.ExecProcess`: that left every old TUI waiting underneath, and quitting brought back the previous one.
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
