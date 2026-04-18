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
- `internal/tickets/` — Provider-agnostic ticket model (Ticket, Bucket, Priority, Provider interface, StatusMap, Group/SortByRelevance, FetchAll)
- `internal/tickets/jira/` — Atlassian Cloud provider. POSTs to `/rest/api/3/search/jql` (API v2 was removed in 2025). Token loaded via env `UNKY_MO_JIRA_TOKEN` or `~/.config/unky-mo/jira.token` (mode 0600)

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

A single project or worktree can host more than one Claude session. The first session for a target starts with the bare window name (`project` or `project@branch`); additional sessions get a bracket suffix (`project [2]`, `project@branch [2]`). Any window — primary or sibling — picks up its session's custom title on the next 5s tick: `/rename foo` turns `project` into `project [foo]` and `project [2]` into `project [foo]`. Clearing the title with `/rename ""` reverts the window to the bare slot if free, otherwise to the next available ordinal.

- **Key**: `n` in main TUI. If no live session exists at the target, launches immediately. If one exists, opens a `s`/`p`/`c`/`esc` prompt: `s` switch to existing, `p` park current (SIGINT claude + `kill-window` so the sidebar dies with it) then launch fresh in the same primary name, `c` add a concurrent sibling at the next free ordinal.
- **Window composition**: `internal/tmux/naming.go` owns `ComposeWindowName` / `ParseWindowName` / `NextAvailableOrdinal`. Every launch site that could produce duplicates must go through these.
- **Sibling attribution**: a sibling window's Claude session ID is recovered by walking tmux pane PIDs (`tmux.WindowPanePIDs`) and matching `claude.LiveSessions()` via `IsDescendantOf`. See `sessionToWindowMap` (main-goroutine) and `resolveSessionWindows` (off-goroutine variant called from the poll) in `internal/tui/app.go`.
- **Status attribution**: the 5s poll (`refreshSessions`) emits one `sessionView` per live session with its own `Status`; `updateProjectStatuses` applies `notifState[sessionID]` overrides and stashes the slice on `m.sessionViews`. Both the dashboard and the state file iterate that slice, so every session — primary, sibling, renamed primary, worktree sibling — carries its own status. `notifState` is keyed by session ID so a permission prompt on one sibling never colors its neighbor.
- **Primary window resolver**: since primaries can now be renamed too, flows that need to act on "the current primary" (`n` menu, `a` attach, `r` resume) call `primaryWindowForTarget(project, branch, cwd)` — it picks the oldest live session at the target and looks up its real window name via `sessionToWindowMap`. Never compose a primary window name from `(project, branch)` alone without this check, or renamed windows won't be found.
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

- **Main TUI** (`internal/tui/app.go`) — runs in tmux window 0. Three screens: `ScreenDashboard` (50/50 split: project list left, sessions-on-top / tickets-below right; right-panel focus has two sub-sections via `dashRightFocus` and up/down crosses the boundary), `ScreenProject` (branches list left, PRs right; branch rows marked `●` main / `⎇` worktree / `·` neither with optional `[merged]`/`[gone]` tags), `ScreenTicket` (ticket detail popup — see Tickets section). `←`/`→` switch panels on dashboard / project detail. Dashboard/project-detail keys: `enter` (smart resume / open ticket detail popup when on tickets section), `w` (worktree), `m`/`M` (checkout in main; `M` stashes first), `W` (new branch prompt), `n` (new session; prompts switch/park+new/concurrent), `x` (cleanup — see Cleanup), `a`, `r`, `o` (open PR or ticket URL), `c`, `?`, `ctrl+r`, `s` (suspend — tmux detach-client), `esc`, `q`. On `ScreenTicket`: `s` = start working, `o` = open URL, `y` = yank branch name, `esc` = back (unwinds remember-prompt → picker → screen).
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

**Data flow**: `refreshSessions` classifies each live session and emits one `sessionView` per session (`ProjectPath` + `Parent` + `IsWorktree` flags) → `updateProjectStatuses` applies notification overrides and stashes them on `m.sessionViews` → `writeStateFile` and `refreshDashSessions` iterate the same view list. Worktree sessions have `Parent` set to the parent project's name so the sidebar renders them indented under the parent.

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
- **`ProjectState`** — one entry **per live Claude session**, plus one placeholder per zero-session known project (so the sidebar still lists empty projects with a dim dot). Concurrent siblings / renamed primaries produce separate entries, distinguished by `WindowName` + `SessionID`. Fields: `name`, `path`, `window_name`, `status` (`"none"` | `"active"` | `"idle"` | `"permission"` | `"external"`), `parent` (for worktree/sibling rows), `section` (`"projects"` | `"external"`), `branch` + `dirty` (git-backed strays only), `session_id`, `index` (0 = primary, 2+ = sibling ordinal, -1 = custom-title window).
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

## Tickets (`internal/tickets/`)

Provider-agnostic ticket panel rendered in the bottom half of the dashboard right column. Jira is the only provider today but the `Ticket` / `Bucket` / `Provider` abstractions accept anything (Linear, GitHub Projects) that can return tickets assigned to the authenticated user.

- **Buckets**: `in_progress`, `blocked`, `review`, `todo`, `unmapped`. Raw provider statuses are resolved via `StatusMap` (case-insensitive, whitespace-trimmed). Anything not matched falls into `unmapped` and renders the raw status in red brackets so workflow drift is obvious.
- **Sort within bucket** (`SortByRelevance`): `(InSprint desc, Priority desc, UpdatedAt desc)`. Stable sort — ties preserve provider-returned order.
- **Jira endpoint**: `POST /rest/api/3/search/jql` with `{jql, fields, maxResults}`. The v2 `/rest/api/2/search` endpoint was removed by Atlassian in 2025 (changelog CHANGE-2046); do not roll back to it.
- **Sprint detection**: JQL runs `assignee = currentUser() AND statusCategory != Done`. The dynamic sprint custom field (default `customfield_10020`) is extracted out of band because its ID varies per installation. Configurable via `sprint_field_id`.
- **Priority normalization**: Jira's named priorities (`Highest`/`Blocker`/`Critical`, `High`/`Major`, `Medium`, `Low`, `Lowest`/`Trivial`) map to the 1–5 scale.
- **Auth**: basic auth with `email:API-token`. Token lives at `~/.config/unky-mo/jira.token` (mode 0600, enforced by `LoadToken`); env var `UNKY_MO_JIRA_TOKEN` overrides the file. Tokens **never** go in `config.toml`.
- **Error surfacing**: `extractJiraError` parses `{errorMessages, errors, message}` shapes so the panel and `mo jira fetch` show just the human message, not the JSON blob.
- **Rendering**: panel only appears when a token is present (file or env) OR `[[tickets.jira]]` is configured — controlled by `ticketsShouldRender()` in `internal/tui/tickets.go`. Explicit opt-out via `[tickets] disabled = true`. Overflow cap per bucket is `per_bucket_limit` (default 5); extra rows collapse to `… +N more`.
- **Fetch cadence**: `ticketsTickMsg` in `internal/tui/app.go`, default 5min (config `refresh_seconds`). Initial fetch fires from `Init` so the panel populates on first paint.
- **Multiple instances**: `[[tickets.jira]]` is an array; each instance gets its own `jira.Provider`. A single token is shared across instances today (the token file is per-user, not per-instance) — revisit if multi-org support becomes real.
- **Provider interface**: `Provider{Name, MyTickets, Detail}` — `Detail(ctx, id)` added for the per-ticket popup. Jira's impl lives in `internal/tickets/jira/detail.go` and uses `GET /rest/api/3/issue/{key}?expand=renderedFields` so description comes back as HTML, stripped to plain text by `StripHTML` (paragraph tags → blank line, `<br>`/`</li>` → single newline, `<li>` → `- ` bullet, entities decoded).

### Ticket detail screen (`ScreenTicket`)

Pressing `enter` on a ticket in the dashboard transitions to `ScreenTicket` (full-screen, not a true overlay — matches the existing `ScreenProject`/`ScreenHelp` pattern). The view shows a metadata grid (status, priority, reporter, assignee, sprint, updated, project key, Mo-project mapping), the stripped description, a dynamic `s → …` hint line explaining what start-working will do, and a footer with `s`/`o`/`y`/`esc` bindings.

- **`s` (start working)**: `handleTicketStartWorking` in `internal/tui/ticket_detail.go`. Resolves the project mapping via `resolvedMoProjectForTicket`; if unmapped, opens the picker; if mapped, calls `startWorkOnTicket`. Collides with the dashboard's `s` (suspend) — the handler in `app.go` short-circuits when `m.screen == ScreenTicket`.
- **`o`**: opens `detailTicket.URL` (or the list-level `detailTicketList.URL` if the detail fetch hasn't completed yet) via `openInBrowser`.
- **`y`**: copies `tickets.BranchNameForTicket(id, title)` to the system clipboard via `atotto/clipboard`. Useful for pasting into PR titles / commit messages.
- **`esc`**: `Back` unwinds one layer at a time — remember-prompt → picker → screen → dashboard.

### Project mapping (`internal/tickets/mapping.go`)

Jira project keys (e.g. `OP`) must resolve to a Mo project name (e.g. `moma-apps-rails`) before start-working can do anything. Two sources:

1. **Config map** — hand-authored `[tickets.jira.project_map]` under each `[[tickets.jira]]`. Wins on conflict.
2. **Companion file** — `~/.config/unky-mo/jira-project-map.toml`, auto-managed by the picker (`LoadCompanionProjectMap` / `SaveProjectMapEntry`). Stored as `[[entry]]` rows with `provider` / `jira_key` / `mo_project` fields so future providers (Linear, GitHub Projects) can share the file.

Separate companion file on purpose: round-tripping `config.toml` through BurntSushi/toml would lose comments and reorder blocks, so any UI-driven mutation goes to the companion file while the hand-authored config stays pristine. `MergeProjectMaps(cfgMap, companionMap)` is the auth function — config wins, companion supplements.

### Start-working state machine (`startWorkOnTicket`)

```
resolve Mo project (config + companion)
    │
    ├── missing → startProjectPicker → user picks
    │                → pickerRememberActive prompt
    │                    → `r` SaveProjectMapEntry + reload map
    │                    → `n` in-memory-only for this session
    │                → fall through to branch flow
    └── present → branch flow
                    │
                    ▼
             derive `<id>-<slug>` (tickets.BranchNameForTicket)
                    │
                    ▼
             project dir on disk? (os.Stat)  no → status msg, stay
                    │ yes
                    ▼
             existing worktree for branch? (project.ListWorktrees)
                yes → focusIfExists + launchClaudeInWindow if cold
                no  → claude.SessionForPath(projectPath)?
                         yes → m.createWorktreeAndLaunch(branch)
                         no  → m.openBranchInMain(branch, false)
```

The branch-flow helpers read `m.detailProject`, so `startWorkOnTicket` sets it before returning the Cmd — copy-not-alias to avoid leaking mutations into `m.projects`.

### Project picker (`internal/tui/project_picker.go`)

A `bubbles/list` model with a custom `pickerItem` wrapper (separate from `ProjectItem` so it doesn't need session-status machinery). Fuzzy filter is on by default. Activated via `startProjectPicker(provider, jiraKey)` from the ticket screen. Key routing: while `pickerActive` is true, the main Update forwards keys to `updateProjectPicker` via `handleTicketPickerActive`, except `enter` which confirms the pick and flips to `pickerRememberActive`. `enter` inside an active filter falls through to the list so the filter can apply first.

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
| `tickets.disabled` | `bool` | `false` | Explicit opt-out — hide the panel even when credentials exist |
| `tickets.refresh_seconds` | `int` | `300` | Background fetch cadence |
| `tickets.per_bucket_limit` | `int` | `5` | Max rows per bucket before `… +N more` |
| `tickets.jira` | `[]JiraConfig` | `nil` | One or more Jira instances; see `[[tickets.jira]]` block with `base_url`, `email`, `sprint_field_id`, `status_map.{in_progress,blocked,review,todo}`, `project_map.<JIRA_KEY> = <mo_project>` |

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
mo jira setup         # Interactive wizard: prompts URL/email, reads token without echo, verifies, writes token file + [[tickets.jira]] block
mo jira fetch         # Run one fetch per configured instance (diagnostic; prints ticket count or extracted error)
mo jira issue <KEY>   # Fetch one issue's detail and print metadata + stripped description (diagnostic; shares code path with the TUI popup)
mo jira show-token    # Print the current token (for copying to another machine)
mo debug <project>    # Dump session/worktree debug info for a project
mo sidebar            # Run sidebar TUI (internal, launched in panes)
mo version            # Print version
```
