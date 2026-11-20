# Unky Mo

Claude Code session orchestrator for MoMA workspace projects: a tmux TUI (`mo`), a sidebar in every Claude window, and a web dashboard (`mo web`) that stands in for both.

This file holds the rules that must not break. **`docs/reference.md` describes every part in detail** (where it lives, how it works, why); read the section for what you're changing first. Plans and probe notes are in `docs/plans/`. Topic rules are in `.claude/rules/` (session detection, lifecycle, TUI keys, notifications and state, tickets, tmux layout, testing).

## Build & Run

```
make install   # Build and install to ~/go/bin/mo
./mo           # Launch TUI (auto-creates tmux session if needed)
./mo list      # List projects
./mo sessions  # List active Claude sessions
./mo restart   # Restart the running TUI, its sidebars and mo web (like ctrl+alt+r) — run after `make install`
./mo hooks install  # Install status hooks into ~/.claude/settings.json (the TUI also does this on every start)
./mo calls [dir] [--base branch|head] [--format text|json|dot] [--fail-on removed-called,…]  # The change's call graph in the terminal
./mo web devices list|revoke <id>  # Browsers logged in to mo web with the login form (per-device tokens)
./mo web       # Serve the web dashboard over HTTPS (the TUI auto-starts it in a detached `mo-web` tmux session)
```

`make fmt` / `make fmt-check` (CI fails on unformatted tracked files), `make mocks` after changing a mocked interface (mockgen is pinned to go.mod's version, installed into `.bin/`).

## Layers and seams

- **Go + Bubbletea v2** (`charm.land/*/v2` imports), **Cobra** CLI, **tmux**: the TUI is window 0, each Claude session a sibling window with a sidebar pane (`.1`).
- **`cmd/mo/`** — each `RunE` builds an `ops.Context` and calls an `ops.*` function. Thin wrappers by design. `sync.go` holds `mo sync`.
- **`internal/ops/`** — domain operations shared by CLI, TUI and web: plain functions over `*ops.Context` (`TmuxClient`, `ClaudeReader`, `moexec.Commander`), no bubbletea types, tested with gomock fakes. `ops` may not import `internal/status`.
- **`internal/tui/`** — the main TUI. `app.go` is only the model and `Update`'s dispatch; keys route through `keys_route.go` (`onKey`: open prompt first, in the order `prompts.go` lists, then the screen), each prompt has its own handler (`prompt_keys.go`), each message its own `update_*.go` handler, views are in `view_*.go`. A `tea.Cmd` closure is a ~5-line adapter over an `ops.*` call. Never reach past `ops.TmuxClient`/`ClaudeReader` to the `tmux`/`claude` packages from a `Model` method.
- **`internal/tui/sidebar/`** — a separate process per window, with its own smaller `TmuxClient`/`ClaudeReader`/`GitReader`/`Opener` interfaces (`deps.go`).
- **`internal/exec.Commander`** — the one shell-out seam (`internal/github`, `internal/gitfiles`, `internal/project`, `review`). Add new shell-outs to it rather than calling `exec.Command`. Commands get a `WaitDelay`; `internal/project`'s git calls a 30 s timeout.
- **`internal/web/`** — `mo web`, a separate process. Scoped interfaces in `deps.go` (`StateReader`, `ProjectLister`, `SessionOps`, `GitFiles` = `FileReader` + `HistoryReader` + `ChangeReader`, `ClaudePane`, …), mocked under `internal/web/mocks/`. Caches are one struct (`caches.go`). Frontend is hand-written vanilla JS/CSS under `static/` (no build step except the committed CodeMirror bundle, `make codemirror`), pure logic in `*-model.js` files tested in Node through `go test`.
- **`internal/review/`** — architecture checks and the call graph for Go, Rails, Node, Python, Kotlin/Java and Swift. Scanners must stay linear (`linear_test.go` measures time and allocations as input grows) and check the context (`index.cancelled`).

## Never break these

- **`status.Manager` and the hook socket belong to the running main TUI.** It is the single source of truth for session status: hooks first, the fsnotify JSONL watcher for reconciliation, PID liveness for cleanup, `claude agents --json` as the backstop for `question`/`permission`. No time-based heuristics. The web never touches either, except the write-only `mo_restart` message.
- **Hook payloads:** `tool_name`/`tool_input` live nested under `hook_input`, never at the top level. Write fixtures in the real wire shape (`.claude/rules/session-detection.md`).
- **The state file has one writer, the main TUI.** Sidebars and `mo web` only read it. It, the socket and every secret (TLS CA, push keys, device tokens) live in the state dir (`config.DefaultStateDir()`, kept 0700 by `config.PrivateStateDir`), never in `/tmp` or the config dir. The socket checks its peer.
- **`~/.claude/settings.json` is written atomically** with a `.bak`, following a symlink, and only the `# unky-mo`-marked hook entries are ever touched. Hook commands are shell-quoted. The hook script passes stdin to `mo hooks send`; never build JSON in shell.
- **tmux targets are always resolved** (`ops.resolveTarget`, `tmux.ExactTarget` = `session:=name`, or a `@id`). A bare name prefix-matches another window. Popup titles go through `tmux.EscapeFormat` (`#(…)` would run a command). Text is sent literally (`send-keys -l`, trailing `;` as hex) or as a bracketed paste loaded from stdin.
- **Every pane driver in the web reads the screen before each key and refuses with a 409 if it changed** (a `sig` of what's drawn). Answers, permissions, the trust dialog, menus, the mode and prompts all hold the window's `paneLock`. `/prompt` checks Claude's message box is empty first (a dialog may be open). Control characters are stripped from anything typed.
- **The browser never names a cwd, a command or a revision.** The cwd comes from the state row, the command from the configured `[[agent]]`, revisions from the server's own caches. File reads and writes go through `gitfiles.Resolve` (clean relative path, no symlinks, `O_NOFOLLOW`) and only for paths the Files panel lists; a path listed only as deleted is never read from disk.
- **Unsafe web requests must come from the dashboard's own pages** (`csrf.go`), and with TLS off the `Host` must be the server's. `mo web` refuses a non-loopback address without a login. Scripts load only from `'self'`.
- **The web is a full stand-in for the TUI, destructive actions included.** Mirror the TUI's confirm menus (409 + choices); don't restrict.
- **`claude -p` for the scope check** runs with no tools, no MCP, no user settings, in a fresh temp dir, with the prompt on stdin.
- **Launcher core:** raise `CORE_RELEASE` in `hub-model.js` whenever a core file changes (`TestCoreReleaseTable`). Another machine's core is offered, never installed silently.

## Driving Claude Code's screen

Several web features read Claude Code's pane and press keys, so they depend on its layout. Every parser runs its capture through `cropSidePanel` first. Re-probe these after a Claude Code upgrade (captures under `internal/web/testdata/`):

| Driver | File | Probed on |
|---|---|---|
| AskUserQuestion answers | `answer.go` | 2.1.289 |
| Permission prompts | `permission.go`, `permission_parse.go` | 2.1.289 |
| Folder trust dialog | `startup.go` | 2.1.236 |
| Queued messages, send now, faint prompt suggestions | `inputbox.go`, `handlers_prompt.go` | 2.1.302 (suggestions 2.1.325) |
| Slash-command menus | `menu.go`, `handlers_menu_keys.go` | 2.1.325 |
| Built-in slash commands | `internal/claude/commands.go` | 2.1.289 |
| Pending tool recovery | `internal/status/pending.go` | 2.1.289 |
| Spinner and mode footer | `spinner.go`, `mode.go` | not recorded |

Probe one menu per fresh session: in `/config`, Enter toggles the row under the cursor, so scripted probing has changed real settings.

## Where to read more

Each line names the part and where its full description is.

- **Session status, hooks, pending questions** — `.claude/rules/session-detection.md`; the reference's Architecture section.
- **Multi-session, cleanup, lift** — `.claude/rules/session-lifecycle.md`. A lift moves the JSONL and the session's folder (subagents).
- **TUI keys and prompts** — `.claude/rules/tui-interaction.md`. Every prompt is yes/no (`[y/N]`, enter cancels), a menu (`withCancel`), or free text.
- **Startup checks, web TLS, the installed app** — the reference's Architecture bullets; `docs/plans/pwa.md`.
- **Chat view, composer, attachments, queued messages** — the reference's "Web dashboard" bullet and its "Composer" section; `docs/plans/composer-drafts.md`, `file-attachments.md`, `queued-messages.md`, `transcript-load.md`, `message-envelopes.md`.
- **Editor tabs, review comments** — the reference's "Editor tabs" section; `docs/plans/web-editor.md`.
- **Overview, reviewer view, Git log selection** — the reference's `internal/web/` entry under Key Packages; `docs/plans/change-overview.md`, `overview-explorer.md`, `commit-selection.md`, `inspector-full-file.md`.
- **Architecture checks and call graph** — the sub-bullets of that entry, "Engine" to "Shared rules"; `docs/plans/architecture-languages.md`, `call-graph.md`, `rails.md`.
- **Terminal drawer** — the same entry's terminal bullets; `.claude/rules/tui-interaction.md`.
- **Permission and question banners, slash menus, mode and model chips** — the reference's "Slash commands" and "Slash-command menus" sections; `docs/plans/permission-answers.md`, `slash-menus.md`.
- **Bash-call diffs** — the reference's `internal/bashsnap/` entry; `docs/plans/bash-diffs.md`.
- **Push notifications** — `docs/plans/notifications.md`.
- **The review behind the security rules above** — `docs/plans/review-remediation.md`.

## Testing

- **Always run `make test` after any code change**, and before every commit. It runs `make fmt-check` first (CI fails on unformatted files); fix with `make fmt`, don't bypass it with a bare `go test ./...`. Failing tests are a signal: either the code regressed, or the test encoded a behavior that has legitimately changed. Never skip, delete, or comment out a test without understanding what it asserts. If a test is wrong, fix the test; if the code is wrong, fix the code. Never weaken an assertion to make it pass.
- `make test-race` after touching `internal/status`, `internal/web` or `internal/notify`.
- The TUI and sidebar have golden render tests (`internal/tui/testdata/golden/`, `go test ./internal/tui/... -update` to rewrite them). A refactor must leave them byte-identical.
- Web pages: load them in headless Chrome before calling web work done (a duplicate global once blanked the chat).
- See `.claude/rules/testing.md` for mock patterns, integration tests, the ops and sidebar seams, and what each web test covers.

## Conventions

- Binary name: `mo`
- tmux session name: auto-detected from current session, falls back to `mo` (configurable)
- Hook marker: `# unky-mo` comment in Claude settings hook commands
- Colors tuned for dark terminal backgrounds (~#14191E); the web uses semantic tokens only (`--paper`, `--ink`, …), never literal colors, so both themes work
- All keyboard shortcuts visible in persistent footer bars
- Circular list navigation (wraps top↔bottom)
- `ctrl+r` forces an in-process refresh (re-poll sessions, rebuild detail branches, rewrite state file — no network, no binary reload)
- `ctrl+alt+r` restarts TUI + all sidebars (dev workflow — picks up freshly-installed binary). `mo restart` and the web's "Restart mo" do the same via a `mo_restart` socket message. The TUI `syscall.Exec`s itself in place (same PID and pane). Don't go back to running the new binary as a child via `tea.ExecProcess`: that left every old TUI waiting underneath.
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
