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
- `internal/usage/` — Claude rate-limit-window fetcher + per-session token counter

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
├── window 2: "moma-apps-rails [2]"  (concurrent sibling — see Multi-session)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 3: "moma-go"
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 4: "unky-mo@feature-auth" (worktree session)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
├── window 5: "unky-mo@feature-auth [debug-oauth]" (sibling renamed via /rename)
│   ├── pane 0: Claude Code session
│   └── pane 1: sidebar
└── ...
```

## Multi-session (concurrent / park)

A single project or worktree can host more than one Claude session. The first session for a target keeps the bare window name (`project` or `project@branch`) so existing name-based lookups keep working; additional sessions get a bracket suffix (`project [2]`, `project@branch [2]`). When Claude's `/rename` sets a custom title, the bracket content is swapped for the title (`project@branch [debug-oauth]`) on the next 5s tick — **primary windows are never renamed**, only siblings.

- **Key**: `n` in main TUI. If no live session exists at the target, launches immediately. If one exists, opens a `s`/`p`/`c`/`esc` prompt: `s` switch to existing, `p` park current (SIGINT claude + `kill-window` so the sidebar dies with it) then launch fresh in the same primary name, `c` add a concurrent sibling at the next free ordinal.
- **Window composition**: `internal/tmux/naming.go` owns `ComposeWindowName` / `ParseWindowName` / `NextAvailableOrdinal`. Every launch site that could produce duplicates must go through these.
- **Sibling attribution**: a sibling window's Claude session ID is recovered by walking tmux pane PIDs (`tmux.WindowPanePIDs`) and matching `claude.LiveSessions()` via `IsDescendantOf`. See `sessionToWindowMap` in `internal/tui/app.go`.
- **Detail-view `enter`**: `detailRow.tmuxWindow` is populated from that map so selecting a live session lands in its real window instead of recomputing `project@branch`.
- **Sync caveat (known gap)**: `internal/sync/sync.go` hashes the project name to a single directory — pushing a second session for the same project overwrites the first. Multi-session sync is not yet implemented.

## Cleanup (worktree + branch removal)

The `x` key on a branch row in the project detail view removes the worktree and/or the local branch. Refuses on the main-checkout branch. The popup is a two-stage state machine (see `pendingCleanup*` fields in `internal/tui/app.go`):

1. **Kill stage** — only reached when the branch's worktree has live Claude session(s). Shows `⚠ N live session(s) — [k] kill + continue / [esc]`. `k` SIGINTs each session, waits for exit (SIGTERM fallback), and `tmux kill-window`s each enclosing window.
2. **Action stage** — `[w] worktree only / [b] worktree + branch / [esc]` when a worktree exists; `[b] delete branch / [esc]` when the row is a plain branch with no worktree.

Plumbing: `project.RemoveWorktree` (runs `git worktree remove --force`) + `project.DeleteBranch` (runs `git branch -D`, refuses on main). Branch rows carry `Merged` / `RemoteGone` flags populated by `ListBranches` via batched `git branch --merged` + `git for-each-ref` (upstream track), shown as dim `[merged]` / `[gone]` tags next to the branch name. The SIGINT-and-wait loop is shared with `parkAndLaunchPrimary` via the `signalAndWaitExit` helper.

### Terminal Drawer

The sidebar manages a collapsible terminal drawer below the Claude pane (pane `.0`). Only one terminal tab is visible at a time — inactive tabs are stored in hidden tmux windows via `break-pane`/`join-pane` and swapped in on demand. The sidebar tracks terminal pane IDs (`%N` format, stable across moves) and prunes dead panes on each 1s refresh tick.

## TUI Key Handling

Keys in `mo` are handled by **two separate Bubbletea programs**, not one. When debugging a keystroke, first identify which program was focused when the key was pressed.

- **Main TUI** (`internal/tui/app.go`) — runs in tmux window 0. Dashboard has side-by-side layout: project list (left) + active sessions panel (right). Project detail has a **branches list** (left) + PRs (right); each branch row is marked `●` main checkout, `⎇` has worktree, or `·` neither, with sessions nested under it, plus optional `[merged]` / `[gone]` hints. `←`/`→` switch panels. Keys: `enter` (smart resume), `w` (open row's branch as worktree), `m` / `M` (checkout in main; `M` stashes first), `W` (prompt for a brand-new branch name), `n` (new session; prompts switch/park+new/concurrent when one is already running — see Multi-session), `x` (cleanup worktree/branch under cursor — see Cleanup), `a`, `r`, `o`, `c`, `?`, `ctrl+r`, `s` (suspend — tmux detach-client, session keeps running), `esc`, `q`.
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

## State File Schema

`internal/state/state.go` defines the shared JSON state written by the main TUI (atomic temp+rename) and polled by each sidebar every 1s. Path is configurable via `Config.StateFilePath`, defaulting to `/tmp/unky-mo-state.json`.

- **`StateFile`**: `tmux_session` (string), `projects` ([]ProjectState), `updated_at` (time), `usage` (*UsageState, optional).
- **`ProjectState`** — one entry **per tmux window** (multiple entries can share `Name`/`Parent` for concurrent siblings; distinguished by `WindowName` + `SessionID`): `name`, `path`, `window_name`, `status` (`"none"` | `"active"` | `"idle"` | `"permission"` | `"external"`), `parent` (for worktree/sibling rows), `section` (`"projects"` | `"external"`), `branch` + `dirty` (git-backed strays only), `session_id`, `index` (0 = primary, 2+ = sibling ordinal).
- **`UsageState`**: `five_hour_pct`, `seven_day_pct` (ints 0–100), their `*_resets_at` timestamps, `fetched_at`, `stale`, `auth_error`.
- Writer is the main TUI — no sidebar ever writes. Sidebars only read. Re-written on every 5s session-refresh tick, on every notification event, and after any user action that changes state.

## Strays & Import-External Flow

A **stray** is a live Claude session whose CWD doesn't map to any known project in the workspace. They're detected during the 5s session refresh in `internal/tui/app.go` by classifying live sessions against `projectPaths` and the git-root lookup (`project.FindGitRoot`):

- **Git-backed stray** — CWD isn't a known project but *is* inside some git repo. Rendered in the `Projects` section with branch + dirty info, as if it were an ad-hoc project row.
- **Non-git stray** — CWD is outside any git repo (e.g. `~`, `/tmp`). Rendered in a separate `External` section below projects.
- **External flag** — set when the Claude PID is *not* a descendant of any pane in the mo tmux session (`claude.IsDescendantOf` against `tmux.PanePIDs`). These are orphans started outside mo (e.g. from a VS Code terminal). `enter` on an External row opens an import prompt.

`importExternalSession(pid, sessionID, cwd, windowName)` is the takeover: SIGTERM the orphan, poll up to ~2s for exit (flushes JSONL), then `claude --resume <sessionID>` in a fresh tmux window with a sidebar. See `internal/tui/app.go`.

## Sync (`internal/sync/`)

Per-machine encrypted session sync via a private git repo. **Client never pushes plaintext.**

- **Crypto**: AES-256-GCM for session data + HMAC-SHA256 for directory keying. Key file at `~/.config/unky-mo/sync.key` (32 raw bytes, base64-encoded on disk); `UNKY_MO_SYNC_KEY` env var overrides. See `internal/sync/crypto.go`.
- **Repo layout**: each project name is HMAC-SHA256'd (with the `"unky-mo-dir-v1:"` prefix) to a 32-char hex directory. Each directory holds exactly two files: `session.enc` (encrypted JSONL) and `meta.enc` (encrypted `SessionMeta` JSON). No plaintext anywhere.
- **Public API**: `IsConfigured`, `Init(url)` (`git clone`), `Push(projectName, projectPath, syncDir, sessionID)`, `Pull(projectName, localProjectPath, syncDir)`, `PullAll`, `List`, `ListLocal`, `DefaultSyncDir`.
- **Known multi-session gap**: project name hashes to **one** directory, so pushing a second session for the same project **overwrites** the first. Multi-session sync is not yet implemented — track when extending `Push` / the hash key to include session ID.

## Usage (`internal/usage/`)

Tracks Claude rate-limit windows and per-session token counts.

- **Windows**: 5-hour, 7-day (plus per-model 7-day Opus / 7-day Sonnet breakdowns).
- **Source**: Anthropic OAuth `/api/oauth/usage` endpoint via `internal/usage/client.go`. Responses cached to `/tmp/mo-claude-usage.json` with a 60s TTL (see `internal/usage/cache.go`).
- **Who fetches**: **main TUI only**, on a 60s `usageTick`. Sidebars never call the API — they read the cached snapshot from the shared state file (`UsageState`) and render it.
- **Per-session tokens**: `usage.SessionTokens(jsonlPath)` parses a session's JSONL and returns the last turn's input + cache tokens. Cached by file size (JSONL is append-only, so size-change → recompute).

## Config (`internal/config/`)

Loaded from `~/.config/unky-mo/config.toml` (or `$XDG_CONFIG_HOME/unky-mo/config.toml`); `config.Load` returns a fully-defaulted struct when the file is missing — no example file is shipped.

| Field | Type | Default | Purpose |
|---|---|---|---|
| `workspace_dirs` | `[]string` | `[]` | Dirs to scan for projects on startup |
| `tmux_session` | `string` | `"mo"` | tmux session name |
| `socket_path` | `string` | `/tmp/unky-mo.sock` | Unix socket for hook notifications |
| `state_file_path` | `string` | `/tmp/unky-mo-state.json` | Shared state file |
| `scan_on_startup` | `bool` | `true` | Auto-discover projects under `workspace_dirs` |
| `notify_sound` | `bool` | `true` | Enable notification sound |
| `project` | `[]project.Project` | `nil` | Manually-configured projects, merged with discovered set |

## Sidebar Responsibilities

Beyond the keys and session list already documented, each sidebar (one per project window, 1s tick) is also responsible for:

- **Usage strip** — reads `UsageState` out of the shared state file and renders the 5h / weekly bars. No API calls.
- **Per-session token counter** — calls `usage.SessionTokens` against the live session's JSONL; shown next to the session row.
- **Active shells** — `claude.ActiveShellsForSession` lists Claude's Bash-tool subprocesses currently alive, rendered as a sub-section under the session row.
- **Changed files** — `git status --porcelain` + `git diff --numstat` on the window's path; populates the Files section where `enter`/`d`/`v`/`o` operate.
- **Sync status** — read once on init via `moSync.ListLocal` (no network), re-checked after a local push.

Everything above updates on the same 1s `stateTick`.

## Testing

- Unit tests live alongside code: `internal/tmux/naming_test.go`, `internal/sync/crypto_test.go`, `internal/usage/*_test.go`, smoke tests in a couple of packages.
- Run the full suite with `go test ./...`. There is no `make test` target.
- No test for the main TUI (`internal/tui/`) or CLI (`cmd/mo/`) — UI correctness is validated manually.

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
