# Review remediation: security fixes and the big-file refactors

**Status:** plan only, 2026-11-15. Nothing below is built. The findings it works through come from the full architecture, code and security review of `0ba174d` (report kept outside the repo; every finding is restated here with its file and line so this document stands alone).

## Goal

Work through every finding of the review in an order that closes the real exposure first, then take apart the files that have grown past the point where they can be reviewed or tested (`internal/tui/app.go` 5,199 lines with a 1,125-line `Update`, `internal/tui/sidebar/model.go` 2,529, the five `internal/review` scanners, `callDelta`, `rbScan`, `rails_routes.go`, `cmd/mo/main.go`'s `syncCmd`, the web `Server`), without changing what the program does.

## Ground rules

- **One step, one commit.** Each step below is a commit with a one-line message in the repo's style. A step that turns out to need two commits gets two; a commit never spans steps.
- **`make test` after every step**, and `make test-race` after any step that touches `internal/status`, `internal/web` or `internal/notify`. A refactor step must leave the test suite's assertions untouched: if a test has to change in a pure-move step, the step wasn't pure.
- **Refactor steps change no behaviour.** The characterization tests of phase 0 are the proof: their goldens must be byte-identical before and after each step of phases 2 and 3.
- **Security steps come with a regression test** that fails on the old code. Where the fix is in a pane driver, add a capture under `internal/web/testdata/` as the other drivers do.
- **Install and restart between phases, not steps.** `make install && mo restart` once a phase is done, then use the TUI and the web for a day before the next phase. The refactors touch the key routing and the state file; a day of real use catches what the goldens don't.
- **Order within a phase is the order written.** Later steps assume earlier ones.
- Line numbers refer to `0ba174d`. They drift as steps land; the function names don't.

---

## Phase 0: the safety net (before any refactor)

### 0.1 `gofmt` and a `fmt` target
`gofmt -l .` lists 30 files. Add `make fmt` (`gofmt -w .`) and a CI step that fails on `gofmt -l` output. Commit the reformat on its own, so every later diff is readable.

### 0.2 ESLint for the static JS
`internal/web/static/` has 15k lines of hand-written JS with no linter. Add `internal/web/jstests/eslint.config.js` with only `no-undef`, `no-redeclare`, `no-unused-vars` (warn) and the shared globals declared per file (`CM`, `el`, `STATUS`, …), and run it from `jstest_test.go` next to `node --test` (skipped without `node`, like the tests). The duplicate-globals incident recorded in the memory notes would have failed here instead of in headless Chrome.

### 0.3 Pin `mockgen`
CI installs `mockgen@latest`. Use `go run go.uber.org/mock/mockgen@v0.6.0` (the version already in `go.mod`) in the Makefile's `mocks` target so `make mocks-check` can't break on an upstream release.

### 0.4 Characterization goldens for the two TUI views
Before touching `app.go`, pin what it draws. Add `internal/tui/golden_test.go`:
- Build a `Model` through `NewModelWithDeps` with gomock fakes (as `handlers_test.go` and `lift_test.go` already do), at a fixed `width`/`height` (120×40), with a fixture of 3 projects, 4 session views (one per status: active, idle, permission, question), one worktree, two PRs (one expanded), tickets loaded, usage ready.
- Render `dashboardView()`, `projectDetailView()`, `helpView()` and `renderFooter(...)` for each prompt state (one test per `pending*` family, since each changes the footer) and compare to `testdata/golden/*.txt`, updated with `-update`.
- Strip nothing: ANSI included. The goldens are large but they are the whole point: phases 2 and 3 must not change a byte.
- Same for the sidebar: `sidebar.Model.View()` at 40×50 with a fixture state file, drawer open and closed, Files section in both modes.

### 0.5 Key-routing order test
The key-press arm of `Update` (`app.go:552-1297`) is a sequence of `if` guards whose *order* is the priority of the prompts. Record it before changing it: a table test in `internal/tui/keyrouting_test.go` that sets two prompt states at once (e.g. `newProjectInput` set and `pendingCleanupActive` true) and asserts which handler consumed the key, for each pair that can coexist. The order today, top to bottom:
1. a sticky or error `statusMsg` eats the key (`:554`)
2. the list's filter mode
3. `ScreenTicket`: `pickerRememberActive`, then `pickerActive`, then `y`
4. `newProjectInput`
5. `worktreeInput` (on `ScreenProject`)
6. `liftSessionInput` (on `ScreenProject`)
7. `pendingLiftDirtyActive`
8. `pendingNewMenuActive`
9. `pendingCleanupActive` (stage `kill` then `action`)
10. `pendingWTExistsActive`
11. `pendingAgentPickerActive`
12. `pendingSuspendQuitActive`
13. `pendingImportSessionID != ""`
14. the global `key.Matches` switch (Help, Enter, AgentLaunch, Back, New, NewProject, Attach, Resume, Tab, right/l, left/h, OpenInBrowser, Checkout, NewWorktree, NewBranchPrompt, OpenInMain, OpenInMainForce, Cleanup, Refresh, Restart, Suspend, Quit)
15. the per-screen navigation switch (`:1551-1652`)

---

## Phase 1: security fixes

Ordered by exposure. Each step names the finding (S-numbers from the review), the change, and the test.

### 1.1 Same-origin guard on state-changing requests (S1, S10)
Today: `cmd/mo/web.go:136-141` only wraps the server in `RequireLogin` when a login exists; no handler checks `Origin` or `Sec-Fetch-Site`; the device cookie is `SameSite=Lax` (`auth.go:145`). A web page can POST to `127.0.0.1:7890` as a no-preflight request, and with a login any other `localhost:<port>` app can POST with the cookie.

Change, in a new `internal/web/csrf.go`, applied in `Server.ServeHTTP` before routing, in every mode:
- For methods other than `GET`, `HEAD`, `OPTIONS`:
  - If the request carries `Authorization: Bearer` and the path is in `hubCORSPaths`: allow (the installed app's cross-origin shell; a page can't attach that header without a preflight, and CORS is only reflected on those paths).
  - Else require same-origin: `Sec-Fetch-Site` ∈ {`same-origin`, `none`}, or no `Sec-Fetch-Site` and no `Origin` (curl), or `Origin`'s host equal to the request's `Host`. Anything else → 403 `{"error":"cross-site request refused"}`.
- For every request: `Host` (without port) must be in `TLSHosts` plus `[web] extra_hosts`, else 421. Kills DNS rebinding on the `disable_tls` path; the TLS path already can't be reached that way, but the check is cheap and uniform. `mo web` already computes the list for the cert.
- Cookie: `SameSite=Strict`. The installed app never relies on cross-site cookies (cross-origin shells use Bearer), so nothing breaks.
- Basic auth: run `pairLimiter` (5 failures/min/IP) on the Basic path too, and a global semaphore of 4 concurrent PBKDF2 verifications (`auth.go:81-97`).
- Try both credentials: a stale cookie must not shadow a valid Bearer (`auth.go:66-70`).

Tests in `csrf_test.go`: a POST with `Origin: https://evil.example` → 403 in no-login mode, in cookie mode, and 200 with Bearer on a `hubCORSPaths` path; a POST with no fetch metadata → allowed; `Host: evil.example` → 421; `GET` never blocked. `login_test.go`: a sixth bad Basic attempt within a minute → 429.

**Optional follow-up (design only):** no credential-less mode at all. Without `auth set`, `mo web` would mint a 256-bit token at start, keep it 0600 in the state dir, and the TUI's open-in-browser path and `mo web url` would open `/hub?login=1&token=…`, exchanging it for a device cookie once. Worth doing after 1.1 has shipped and been used.

### 1.2 Permission answers bound to the call they were shown for (S2)
`permission.go:108-115`: `permView.sig` hashes the question and rows only. Add to the hash: `v.preview` (the screen lines above the question), `v.planFile`, and the matched `pending_tool`'s command/path/URL when `pendingMatches` found one. In `handleAnswerPermission` (`:391-440`), after the lock and before `answerPermission`, re-read the state row and refuse (409 `the prompt changed`) when `pending_tool` no longer matches the one the browser was shown (send its `tool_use_id`, or a hash of the content, in the POST body as `pending`). In `answerPermission` keep `waitPermission` but compare the sig *including preview* right after it.

Test: `fakePermission` draws Bash prompt A, the browser gets its sig, the fake switches to Bash prompt B with the same rows, POST with A's sig → 409, no keys. Add a second Bash capture to `testdata/permission/` with a different command and the same rows.

### 1.3 Routes parser guards (S3)
`rails_routes.go`: `useConcerns` (`:1161-1174`) has no cycle guard; `stmts`/`stmt` (`:432-481`), `value`/`primary` (`:716-891`) and `block` (`:552-573`) have no depth cap; `%w[…].each` (`:1009-1045`) and `draw` have no evaluation budget.
- `rtEval` gets `depth int` and `budget int` (start 200,000 statements). `stmts` increments depth on entry, returns `errRoutesTooDeep` past 64; every evaluated statement decrements the budget, `errRoutesBudget` at zero. Both are returned up to `railsRoutesOf`, which reports them the way a parse error is reported today (the surface shows "routes not analyzed: …").
- `useConcerns` keeps `inConcern map[string]bool`; a concern already on the stack is skipped (counts as the `skip` branch).
- The parser (`value`/`primary`): a `depth` counter, 256 max, error past it.

Tests: the three inputs the review used (`concern :a do concerns :a end; concerns :a`; `get ` + 100k `[`; 600k `if\n`), each must return an error in under 100 ms. Add `FuzzRailsRoutes` with a 50 ms budget per input.

### 1.4 Linear scanners and a context that is actually checked (S6)
Seven tail copies: `js_scan.go:55`, `kt_scan.go:43`, `swift_scan.go:64,72`, `ruby_scan.go:42,195,200` do `strings.Index(string(b[i:]), …)`. Replace each with `bytes.Index(b[i:], []byte(…))`. Then the other quadratic loops, one commit per language so the fixture tests localize a regression:
- Python (`py_scan.go:83-89`, `pycalls.go:109-115`): join logical lines with a running paren depth and a `strings.Builder`; never recompute `depth(line)` on the accumulated line.
- Ruby (`ruby.go:165-190`): walk the segments once, building the candidate path incrementally; (`rbcalls.go:487-496`) keep the scope as a slice of segments and join once per definition; (`rbcalls.go:807-871`, `rbrails.go:532-556`) cap a macro statement at 64 lines.
- JS (`jscalls.go:1338-1382`): a `visited` set in `exportOf` and a cap of 256 `want` ids; (`:1115-1128`) build chain parts forward and reverse once.
- JVM (`jvmcalls.go:706,775,823-829,861`): `skipAngle` and `match` stop at 4 KB or the next `;`/`{`; chain parts as above.
- Swift (`swiftcalls.go:369,480,550,554,657,868,1375-1384`): bodiless scans stop at the next declaration keyword; `bodies` indexes defs by start line once; protocol conformance via a map from type name to protocols.
- `ctx.Err()` inside the per-file loops of `callDelta`'s callers and `Analyze` (today only at `calls.go:339` and `review.go:330`), and inside `symbolsMany`'s worker loop; `goBudget` checked per file in the Go loader.
- `textChanges` (`surface.go:194-211`): one `git grep -F -f -` with all names on stdin per category instead of up to 60 processes.

Tests: `FuzzScanFile<Lang>` per scanner with a 50 ms budget; a `TestScanLinear` that scans 1×, 2×, 4× a pathological fixture and asserts time grows at most ~2.5× per doubling.

### 1.5 Launcher-core trust and a script CSP (S4)
- `hub.js:115`: `installCore` takes `trusted` from the caller; `fromMachine` passes `false` unless `base` is the app's own origin. A core from another machine is only *offered*: `note("… has a newer launcher", {label: "Install", run})`, and the user's click is what installs it. `hub-model.js` `coreUpdate` already has the `trusted` input; add the test.
- `server.go:113`: the CSP becomes `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self' <paired origins>; frame-ancestors …`. Verify headless (per the memory note) that the chat, Overview, hub and reviewer pages load with no CSP report. `sw.js`'s inline recovery page is worker-generated and unaffected.
- `sw.js`: `/__mo/install` only from a client whose URL path is `/hub` (`event.clientId` → `clients.get`).
- Raise `CORE_RELEASE`.
- Design note for later (not in this plan): pin a CA fingerprint per machine id at pairing, verified through a signed hello nonce.

### 1.6 The composer and the dialog drivers (S7)
- `handlers_prompt.go:71-78`: run `checkInputBoxEmpty` for `idle` as well as `active`. Test: an idle session whose capture shows `/config` → 409.
- In `question` status, refuse `/prompt` unless the dialog's cursor is on "Type something" (read through `parseDialog`), else 409 pointing at `/answer`.
- One `paneMu` per window (a `sync.Map` of mutexes) taken by `/prompt`, `/answer`, `/permission`, `/mode`, `/startup`, `/menu/keys`, replacing `modeMu` and `menuMu` (keep `menuEscOn` under it).
- `sendNow` (`:160-166`): search only the lines between the queued message and the prompt box.
- `stripControl` on the line-input terminal path (`handlers_terminals.go:239-260`); a Tab in a single-line prompt forces the paste path.

### 1.7 `.env` after `git rm --cached` (S8)
`handlers_editor.go:189-219`: before serving or writing a working-tree file that is *only* in the Changes list (not in the tree), run `git check-ignore -q -- <path>` (add `gitfiles.IsIgnored`); if ignored, serve only `rev=HEAD`/`base` and refuse the working copy (404) and the PUT (403). Same in `inBranchOverview`. Test on real git: commit `.env`, `git rm --cached .env`, add it to `.gitignore` → `/file?path=.env` 404, `?rev=HEAD` 200, PUT 403.

### 1.8 The scope check's process (S9)
`review/scope.go:249-252`: cwd becomes a fresh `os.MkdirTemp("", "mo-scope-")` (0700, removed after); the prompt goes on stdin through `Commander.OutputStdin` with argv `claude -p --tools "" --strict-mcp-config --setting-sources "" …`; `cmd.WaitDelay = 2s` in `exec/commander.go`. Tests: `scope_test.go` already checks the argv; extend for stdin and the temp cwd, and a 200 KB prompt.

### 1.9 The hook socket and the state file (S5, S11, S12)
- `notify/server.go:34-43`: socket at `filepath.Join(config.DefaultStateDir(), "hook.sock")` (the dir is created 0700), no `Chmod`; `Start` failure is surfaced as a sticky status line, not swallowed (`app.go:5160-5164`). `acceptLoop`: on Linux, `unix.GetsockoptUcred` on the accepted fd; a uid other than the process's is closed. Cap connections at 64 with a semaphore.
- `status-hook.sh`: the socket path is substituted at install like `__MO_BIN__`; the script passes `--cwd "$PWD" --pane "$TMUX_PANE" --event "$HOOK_EVENT_NAME"` to `mo hooks send` and pipes Claude's stdin JSON through untouched, so Go builds the envelope (`printf` interpolation gone). `v2HookCommand` quotes the script path. `config.SocketPath` either drives both ends or is removed.
- `state/state.go:82` and `usage/cache.go:56`: 0600, under the state dir; the sidebar and `mo web` read the new path from the same `config` function. Keep reading the old `/tmp` path for one release so a restart mid-upgrade still works.
- `claude/hooks.go:386`: write through a temp file and rename, `SetEscapeHTML(false)`, `UseNumber`, refuse a non-object document instead of panicking, keep `settings.json.bak` from before the first rewrite of a session.
- `refreshSessions` (`app.go:2493-2541`): after `liveSIDs` is built, call `statusMgr.MarkDead` and `statusWatcher.UnwatchSession` for every known id not in it.
- `ops/target.go`: resolve every target to `@id` through `ListWindows` (drop the "only if it has a dot" shortcut); `tmux/parse.go:25` uses `\x1f` as the `list-windows` separator.

### 1.10 Web resource bounds (S13)
- `readJSON(w, r, max, &v)` helper in `server.go`; use it in the ten body-reading handlers (`answer.go`, `handlers_prompt.go`, `handlers_sessions.go`, `handlers_worktrees.go`, `mode.go`, `permission.go`, `startup.go`, `handlers_terminals.go`, `handlers_scope.go`, `handlers_menu_keys.go`).
- `http.Server{ReadHeaderTimeout: 10s, IdleTimeout: 120s, MaxHeaderBytes: 64<<10}` on both the TLS and the `disable_tls` path; `signal.NotifyContext` + `Shutdown(5s)` in `cmd/mo/web.go`.
- `gitfiles/excerpt.go:97`: `git cat-file -s` on the revision side and `os.Lstat` on the working-tree side before the diff; `TooLarge` past `MaxContentBytes`.
- `ttlCache.get`: single-flight (copy `fpFlight` from `handlers_calls.go`) and an optional byte cap per cache (`excerptCache`: 64 MB, LRU by `fetchedAt`).
- `transcript.go:393-399`: on a full subscriber channel, close it with a final `{"type":"resync"}` event; `chat.js` reconnects (it already replays from zero). Read the backlog and prime the cursor under the same lock (`:314-347`).
- `handlers_calls.go:155-167`: `defer close(f.done)` and `defer delete(inflight, key)`.

### 1.11 The low findings (S14), one commit each
Jira id through `ticketIDRe` in `handlers_tickets.go` and `cmd/mo/jira.go`; `io.LimitReader` on Jira responses; `@mo_drawer_pane` never pane `.0` (`deps.go:661-672`); cleanup's live check by path prefix after `EvalSymlinks` (`handlers_worktrees.go:68-72`, and the TUI's `pendingCleanupSessions` capture); `mo start -p` through `shellQuote`; `sync.RepairNames` decrypts to `os.CreateTemp` outside the repo; `Pull` compares mtimes; `mo resume` and `mo sync pull` call `ops.LaunchSession`; `GetChanges` gets `--`; `--no-textconv --no-ext-diff` on every `gitfiles` diff; `JSON.parse` guarded in both SSE listeners; `embed.js` persists `parentOrigin` only after the first authenticated response; `sw.js` prefers `openWindow` and aborts the timed-out fetch; PID identity (`/proc/<pid>/comm` is `claude`/`node`) before any signal in `claude.IsAlive`, `SignalAndWaitExit`, `ImportExternalSession`, `LiftSessionToWorktree`, `SuspendAll`.

### 1.12 Frontend polling (A7)
A shared `whenVisible(fn)` in `common.js` that skips a tick while `document.hidden` and runs once on `visibilitychange`; convert `pollStatus`, the permission, startup, subagents, terminal-list, notify and app polls from `setInterval(asyncFn)` to `setTimeout` chaining with an in-flight guard (as `spinner.js` and `mode.js` already do). Server side this halves the `capture-pane` rate from backgrounded tabs.

---

## Phase 2: `internal/tui/app.go`

### What is in it today

123 functions, 5,199 lines. The large ones: `Update` 1,126 (`:527-1652`), `projectDetailView` 297 (`:3304`), `refreshSessions` 265 (`:2477`), `dashboardView` 222 (`:1957`), `refreshProjects` 204 (`:157`), `writeStateFile` 126 (`:3095`), `buildDetailRows` 101 (`:3679`). `Model` has 114 fields, of which 56 belong to nine mutually exclusive prompt flows (`pendingImport*`, `pendingNew*`, `pendingAgentPicker*`, `pendingCleanup*`, `pendingWTExists*`, `pendingSuspendQuitActive`, `newProjectInput*`, `worktreeInput`, `liftSession*`/`pendingLiftDirty*`). `Update` shells out to git synchronously at `:897-898`, `:1517-1518`, `:1529-1530`, `:1541-1542`, and two `tea.Cmd`s run `git fetch`/`reset --hard` with the error discarded (`:4095-4112`, `:4455-4462`).

### Target layout

| File | Contents | Expected size |
|---|---|---|
| `app.go` | `Model` (slimmed), `NewModel`, `NewModelWithDeps`, `Init`, `Update` (dispatcher only), `View`, `Run` | ~450 |
| `messages.go` | every `*Msg` type and `Screen`, `SessionStatus`, `dashRightSection` | ~150 |
| `prompts.go` | the `prompt` interface, `promptFlow` helpers, `withCancel`/`yesNoBindings` | ~80 |
| `prompt_newsession.go`, `prompt_cleanup.go`, `prompt_lift.go`, `prompt_worktree.go`, `prompt_import.go`, `prompt_agentpicker.go`, `prompt_suspend.go`, `prompt_newproject.go` | one struct + `handleKey` + `bindings` + `render` each | 60-180 each |
| `keys_dashboard.go`, `keys_project.go`, `keys_ticket.go`, `keys_global.go` | `updateKey<Screen>` and the global keys | 150-300 each |
| `update_sessions.go` | `sessionTickMsg`, `sessionRefreshMsg`, `statusChangeMsg`, `notificationMsg`, `sessionImportedMsg`, `suspendCompleteMsg` handlers; `refreshSessions` split (see 2.7) | ~500 |
| `update_projects.go` | `projectsRefreshMsg`, `newProjectCreatedMsg`, `gitStatusMsg`, `detailLoadedMsg`; `refreshProjects` | ~350 |
| `update_worktrees.go` | `worktreeExistsMsg`, `worktreeCreatedMsg`, `branchesChangedMsg`, `sessionLiftedMsg`; the worktree/PR/lift `tea.Cmd`s | ~450 |
| `update_tickets.go`, `update_usage.go`, `update_prs.go`, `update_sync.go` | the remaining msg handlers and their cmds | 80-200 each |
| `view_dashboard.go`, `view_project.go`, `view_help.go`, `view_footer.go` | the four views, each split into its panels | 150-300 each |
| `state_file.go` | `writeStateFile` → `buildStateFile` (pure) + `write`; `viewToProjectState` | ~180 |
| `windows.go` | `syncWindowTitles`, `sessionToWindowMap`, `resolveSessionWindows`, `orphanedTermSessions`, `installHomeRespawnHooks`, `restartSidebars` | ~300 |

No function changes its name in phases 2.1-2.6, so `grep` and the CLAUDE.md references keep working. Renames come last (2.9).

### 2.1 Move the message types out (pure move)
`messages.go` takes lines 38-69, 71-180, 475, 1676, 2283, 2430-2475, 3663, 4004-4034, 4140-4175, 4287, 4667, 4755 (every `type … Msg`/`Screen`/`dashRightSection`/`footerBinding`/`sessionView`/`teammateView`/`detailRow`). Nothing else changes. Goldens identical.

### 2.2 Group the `Model`'s prompt state into structs (mechanical, with the compiler as the guide)
For each family, a struct replaces the flat fields, and a pointer replaces the `Active` bool (nil = inactive). Done one family per commit, top of the routing order first (so the routing test from 0.5 exercises each as it lands):

```go
// prompt_cleanup.go
type cleanupPrompt struct {
    stage        string // "kill" | "action"
    projectPath  string
    projectName  string
    branch       string
    worktreePath string // "" for plain-branch rows
    sessions     []claude.Session
}
// Model: cleanup *cleanupPrompt   (replaces pendingCleanupActive + 6 fields)
```

The families and their new homes:
| Today | Struct | Fields |
|---|---|---|
| `pendingImport*` (5) | `importPrompt` | sessionID, path, window, project, pid |
| `pendingNew*` (8) | `newSessionPrompt` | project, branch, cwd, primaryWin, livePID, liveID, resumeSession |
| `pendingAgentPicker*` (2) | `agentPickerPrompt` | branch |
| `pendingCleanup*` (7) | `cleanupPrompt` | as above |
| `pendingWTExists*` (6) | `worktreeExistsPrompt` | branch, projectPath, projectName, wtPath, prBranch |
| `pendingSuspendQuitActive` | `suspendQuitPrompt` | (empty) |
| `newProjectInput`, `newProjectParentDir` | `newProjectPrompt` | input, parentDir |
| `worktreeInput` | `worktreePrompt` | input |
| `liftSession*` (5), `pendingLiftDirtyActive`, `pendingLiftBranch` | `liftPrompt` | input, sessionID, sourcePath, sourcePID, sourceWindow, dirtyStage bool, branch |

Each commit: add the struct, replace the fields, fix every use (the compiler lists them), run the routing test and goldens. The `render`/footer code for the family moves into the same file in the same commit (`agentPickerBindings` at `:2410` is the model).

### 2.3 The `prompt` interface and one `activePrompt` field
Once 2.2 is done, every family has the same shape. Introduce:

```go
// prompts.go
type prompt interface {
    // handleKey consumes a key. handled=false means the key falls through
    // to the screen's own handling (only the ticket picker's `y` does that today).
    handleKey(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd, bool)
    bindings(m Model) []footerBinding
}
```

`Model` keeps the nine pointers (they hold the flow's data) but gains `func (m Model) activePrompt() prompt`, which returns the first non-nil one **in the routing order of 0.5**. The key arm's fifteen guards collapse to:

```go
if p := m.activePrompt(); p != nil {
    if m2, cmd, ok := p.handleKey(m, msg); ok { return m2, cmd }
}
```

The order lives in one function, tested by 0.5. The ticket picker (`pickerActive`, `pickerRememberActive`) joins as `ticketPickerPrompt` in `tickets.go`, where its list already lives.

### 2.4 Split the key arm by screen
`Update`'s `tea.KeyPressMsg` arm becomes:

```go
case tea.KeyPressMsg:
    if m.statusMsg != "" && (m.statusSticky || isErrorStatus(m.statusMsg)) { … }   // unchanged
    if m.list.FilterState() == list.Filtering { … }                                // unchanged
    if p := m.activePrompt(); p != nil { … }                                       // 2.3
    if m2, cmd, ok := m.updateKeyGlobal(msg); ok { return m2, cmd }                // Help, Refresh, Restart, Suspend, Quit, Back
    switch m.screen {
    case ScreenDashboard: return m.updateKeyDashboard(msg)
    case ScreenProject:   return m.updateKeyProject(msg)
    case ScreenTicket:    return m.updateKeyTicket(msg)
    case ScreenHelp:      return m.updateKeyHelp(msg)
    }
```

The global switch at `:836-1297` is keyed on `key.Matches` and many arms branch on `m.screen` inside. Split rule: an arm that checks `m.screen` is cut along that check into the two screen files; an arm that doesn't (Help, Refresh, Restart, Suspend, Quit) goes to `keys_global.go`. The `Enter` arm (`:845-989`, 144 lines) becomes `enterDashboard` (project list vs sessions panel vs tickets panel, by `dashFocusLeft`/`dashRightFocus`) and `enterProject` (a `switch row.kind` over `detailRow` kinds: branch, br-session, br-empty, br-remote, PR panel). The navigation switch (`:1551-1652`) merges into the same per-screen functions as their `default` arm.

Each per-screen function is a flat `switch { case key.Matches(...) }`; no arm longer than ~25 lines, anything bigger becomes a named method (`openInBrowser`, `checkoutInMain`, `startCleanup`).

### 2.5 Move the other message arms into domain files
The 27 non-key arms (`:1298-1550`) each become `func (m Model) on<Msg>(msg <Msg>) (Model, tea.Cmd)` in the `update_*.go` file of their domain, and `Update` is a dispatcher:

```go
case sessionRefreshMsg: return m.onSessionRefresh(msg)
```

Pure move; the `tea.Cmd` closures each handler returns move with it. After 2.4 and 2.5, `Update` is under 120 lines.

### 2.6 Git out of `Update`
- `detailWorktrees`/`detailBranches` are loaded synchronously at `:897-898`, `:1517-1518`, `:1529-1530`, `:1541-1542`. Add `detailLoadedMsg{projectPath string; worktrees []project.Worktree; branches []project.Branch; err error}` and `func (m Model) loadDetail(path string) tea.Cmd`. The four sites become `return m, m.loadDetail(path)`; `onDetailLoaded` ignores a result for a project that's no longer `m.detailProject` (the user moved on), sets the slices and calls `buildDetailRows`. The view shows the previous rows until the message lands (already the behaviour for a refresh); `buildDetailRows` must tolerate nil slices (it does).
- `createWorktreeFromPR` (`:4084`) and `removeAndRecreateWorktree` (`:4449`) run `git fetch origin <branch>` and `git reset --hard origin/<branch>` with `_ =`. Add `ops.CreateWorktreeFromRemote(ctx, projectPath, branch string) error` in `internal/ops/worktree.go` (fetch, create, reset, each error returned), with a 60 s context, and surface its error as a `statusMsgEvent`. The TUI's two cmds become 5-line adapters. Test in `ops` against a bare remote (the `gitfiles` tests have the pattern).
- `project.GitStatus` is already a cmd (`gitStatusMsg`); leave it.

### 2.7 Split the three long non-UI functions
- `refreshSessions` (`:2477-2741`, 265 lines) does: poll `LiveSessions`, map them to windows (`resolveSessionWindows`), enrich with team info, read JSONL status for each, detect strays and externals, build `sessionView`s, dedupe. Split into `pollSessions(tmux, claude) (polled, error)` (I/O only) and `buildSessionViews(polled, prev externals) sessionRefreshMsg` (pure, table-tested with the four statuses, a renamed sibling, a stray, an external). The `tea.Cmd` is the composition.
- `refreshProjects` (`:157-360`, 204 lines): same cut, `scanProjects` (I/O) and `mergeProjects` (pure; `mergeRefreshedProjects` at `:2981` already exists, fold the rest into it).
- `writeStateFile` (`:3095-3220`): `buildStateFile(m) state.File` (pure, tested with the golden fixture of 0.4) and `write(path, file)` (temp+rename, 0600 per 1.9).

### 2.8 Split the views
- `dashboardView` (`:1957-2178`): `renderProjectList`, `renderSessionsPanel`, `renderTicketsPanel`, `renderUsageStrip` (exists), joined by the 50/50 layout code that stays in `dashboardView`.
- `projectDetailView` (`:3304-3600`): `renderBranchRows` (uses `renderSessionRow`, exists), `renderPRPanel` (uses `renderPRDetail`, exists), `renderRecap`, plus the layout.
- Footer: `renderFooter` and every `*Bindings()` into `view_footer.go`; with 2.3 the prompt-specific bindings come from `activePrompt().bindings(m)`.
Goldens identical after each.

### 2.9 Names and receivers
- `Model` keeps value receivers (the Bubbletea idiom; the copy is 114 words of pointers and slices, not the data). Methods that only read and are called from hot paths (`renderSessionRow`, `buildDetailRows`) can take `*Model` if a profile ever shows the copy; not in this plan.
- `rawTmux` (`:339`): add `SetWindowRemainOnExit`/`UnsetSessionHook` to `ops.TmuxClient`, regenerate mocks, delete the field.
- Raw `claude.ProjectsDirForPath` calls (`:2526,2540,2555,2774`) go through `m.claude.ProjectsDirForPath` (it exists on the interface, `:3619` already uses it).

### 2.10 Tests that become possible
With prompts as values and per-screen key handlers: a table test per prompt (`cleanupPrompt`: `k` on stage kill → SIGINT cmd; `w`/`b`/`esc` on stage action), per-screen key tests for `enter` on each `detailRow` kind, and `buildSessionViews`/`buildStateFile` as pure tests. Target: `internal/tui` coverage from the helpers-only ~25% to over 60% of statements, measured with `go test -cover`.

---

## Phase 3: `internal/tui/sidebar/model.go`

2,529 lines; `View` 328 (`:407`), `Update` 241 (`:166`), `refreshState` 128, 13 raw `exec.Command` sites, its own porcelain parser, and a stale-state fallback that marks every live cwd "active" (`:1114`).

### 3.1 Route the shell-outs through the seams
- `TmuxClient` (`sidebar/deps.go`) gains `DisplayMessage(target, format string) (string, error)` (replaces `:871`, `:905`) and `DisplayPopup(args ...string) error` (replaces `:2165`, `:2183`, `:2206`, `:2300`). Regenerate mocks; the popup tests set exact expectations.
- `git status`/`diff --numstat`/`ls-files` (`:1141`, `:1160`, `:1187`) → `gitfiles.GetChanges` and `gitfiles.Tree` through a `GitReader` interface in `deps.go` (two methods). Delete `parseGitStatus` (`:1495`) and `refreshChangedFiles`'s parsing; keep `annotateTreeWithStatus`.
- `code`/`open` (`:2225-2240`) → an `Opener` interface with one method, so tests don't launch editors.

### 3.2 Split by section
| File | Contents |
|---|---|
| `model.go` | `Model`, `NewModelWithDeps`, `Init`, `Update` dispatcher, `View` dispatcher, `stateTick`, `refreshState` |
| `sessions.go` | sessions section: rows, cursor, `switchToSelected`, `refreshSyncStatus`, `renderSessions` |
| `files.go` | Files section: `refreshDirTree`, `buildDirTree`, `flattenDirTree`, `buildFileTreeLines`, `renderFileTree`, mode toggle, expand/collapse (all already pure helpers) |
| `drawer.go` | terminals: `refreshTerminals`, `adoptTerminals`, `openDrawer`, `switchTerminalIdx`, `closeTerminal`, `termSession` |
| `popups.go` | `openPopup`, `showShellOutput`, diff/editor popups |
| `layout.go` | `computeLayout`, `handleMouseClick` |
| `view.go` | `View`'s panels: `renderHeader`, `renderUsageLine`, `renderSessionsSection`, `renderFilesSection`, `renderDrawerTabs`, `renderFooter` |

`Update` splits by section focus (`updateKeySessions`, `updateKeyFiles`, `updateKeyDrawer`) the way 2.4 does; `View` composes the panels.

### 3.3 The fallback status
`refreshFromSessions` (`:1114`) stops inventing "active": a session with no state-file row renders with status `unknown` (new `StatusUnknown` glyph, dim). The main TUI writes `branch` for every row so `mo web` stops patching it in `/api/state` (`handlers_state.go:34`).

---

## Phase 4: `internal/review`

### 4.1 One blanker
`internal/review/blank.go`: `blank(src []byte, t blankTable) []byte`, linear, one pass, keeping newlines so line numbers hold. `blankTable{lineComment []string; blockComment [][2]string; strings []stringRule; rawStrings []stringRule; heredoc func(...)}`; a `stringRule` has open/close delimiters, an escape byte, whether it may span lines, and whether `#{…}`/`${…}` interpolation re-enters code (Ruby, JS templates, Kotlin, Swift). Tables for js/ts, kotlin/java, swift, python, ruby (+ ERB/HAML via the existing `erbCode`/`hamlCode` pre-pass). Replace `jsCode`, `pyCode`, `rubyCode`, `swiftBlank`, `jvmScan`'s blanking and `blankKeepLines` one language per commit; each language's `*_scan_test.go` (comments, strings, heredocs, templates, regex literals must yield no references) is the acceptance test, plus the linearity test from 1.4.

### 4.2 `callDelta` (`calls.go:364-710`, 339 lines)
It already reads as seven commented stages. Make them functions on a `deltaBuilder{before, after *callSet; cl callLang; cg *CallGraph}`:
1. `changedFuncs(oldPaths, newPaths) (oldFns, newFns map[string]*fn, status map[string]string)` (`:366-430`, the unparsed-file rule included)
2. `renames(added, removed) (renamedFrom, oldToNew)` (`:431-464`)
3. `edgeDelta(oldFns, newFns, rename) (oldEdges, newEdges, labels)` (`:466-497`)
4. `buildGraph(status, oldEdges, newEdges)` (`:498-560`, the `graphBuilder` calls)
5. `implementations()` (`:546-560`)
6. `callers(after)` (`:562-580`)
7. `findings(gone, hasTests, …)` → `removedCalled`, `signatureCallers`, `untested`, `testNotUpdated` as four functions (`:618-710`)
Each takes and returns plain maps; `callDelta` becomes the 20-line sequence. The existing `calls_test.go` cases don't change; add one unit test per stage with hand-built sets (renames with a body hash collision across units, a rename whose old name is still called).

### 4.3 `rbScan` (`rbcalls.go:332-747`, 412 lines)
The loop body at `:372-586` handles, per line: blanking, `def`/`class`/`module` opening, `end` accounting, class-body macros (`:586-640`), callbacks, AASM blocks, block parameters, assignments, call sites. Make `rbScanner` a struct holding the scan state (`defs`, `stack`, `indentStack`, `pending`) with one method per concern: `openDef`, `openClass`, `closeScope`, `macroLine`, `callbackRefs`, `aasmBlock`, `lineCalls`; `rbScan` becomes the line loop calling them. The three post-passes (`:730-747`: own-line hashes, namespace folding, order) become `finish()`. `rbcalls_test.go` and the Rails fixtures are the acceptance; `FuzzRbChains` and friends keep running.

### 4.4 `rails_routes.go` (1,833 lines)
Beyond the guards of 1.3: split into `rt_lex.go` (`rtTokens`, 192 lines → a `rtLexer` with `ident`, `string`, `symbol`, `percentLiteral` methods), `rt_parse.go` (`stmts`/`stmt`/`value`/`primary`/`block`), `rt_eval.go` (`rtEval`: resources, nesting, shallow, concerns, draw, each), `rt_routes.go` (the route set, naming rules, diff by identity). Names unchanged; `rails_routes_test.go` unchanged.

### 4.5 The other hotspots
`swiftScanner.decls` (190), `jsScanner.findDefs` (174), `edgeDelta` (164), `pyCalls.scanFile` (158): same treatment as 4.3 (a scanner struct with one method per declaration kind). Hoist the 56 in-function `regexp.MustCompile`s to package vars (`rbrails.go` has 30). `callLangOf` (`calls.go:247`) becomes a registration map like the architecture languages.

### 4.6 A shared class resolver (optional, after 4.1-4.5 have settled)
`lookupName`/`classAt`/`baseOf`/`construct` are rewritten per language with the same shape. `hClassResolver` with hooks `ctorName() string` (`__init__`, `constructor`, `<init>`, `initialize`, `init`) and `importFile(spec) string`; py/js/jvm first, Swift and Ruby keep their own (protocol extensions and singleton classes don't fit). Medium risk; the per-language fixtures catch regressions.

---

## Phase 5: `internal/web`

### 5.1 `readJSON` and typed status
The helper from 1.10 everywhere; `status.SessionStatus` constants instead of the 11 string compares (`"idle"`, `"active"`, …) in the handlers. `ops` is denied `internal/status` by the rules file, but `web` isn't.

### 5.2 Caches as one struct
`Server`'s 15 `*ttlCache` fields → `caches struct{ pr, tickets, files, tree, log, overview, arch, excerpt, selection, localRef, fetch, branch, shells, agents, cmds *ttlCache; calls *fingerprintCache }` built by `newCaches(now, ttls)`; single-flight inside `ttlCache.get` (1.10). `Server` drops to ~12 fields.

### 5.3 `GitFiles` split
The 21-method interface → `FileReader` (ReadFile, ReadHEAD, WriteFile, Tree, Changes), `HistoryReader` (GetLog, GetCommit, GetCommitFile, ResolveSelection, HeadCommit), `OverviewSource` (GetOverview*, Annotate, ResolveBranch, ResolvePR, Fetch*). `Deps` keeps one `Git` field of a type embedding all three, so handlers that need one aspect declare only that. Regenerate mocks; tests unchanged.

### 5.4 Handler files over 400 lines
`handlers_sessions.go` (517): launch, resume, stop, and the 409 choice menus → `handlers_launch.go` + `handlers_sessions.go`. `permission.go` (545): the parser and `pendingMatches` → `permission_parse.go`; the driver stays. `menu.go` (558): `parseMenu` and its row classifier → `menu_parse.go` + `menu_rows.go`.

---

## Phase 6: core plumbing and the CLI

### 6.1 `internal/project` on the `Commander` seam
20 raw `exec.Command("git", …)` in `branch.go`, `worktree.go`, `git.go`. Add `project.Git{cmd moexec.Commander}` with the same functions as methods, 30 s contexts, `--` before every path, and `--no-textconv`. `ops.Context` carries it (it already has a Commander). Keep the package-level functions as thin wrappers over a default `Git` for one release, then delete them. `ops` tests for lift/cleanup can then run on gomock; the real-git tests in `project/` stay.

### 6.2 `cmd/mo/main.go` (972 lines)
`syncCmd` (`:455-806`, 351 lines, nine subcommands inline) → `cmd/mo/sync.go` with one `func sync<Name>Cmd() *cobra.Command` per subcommand; `pull`'s 93-line `RunE` (`:566-659`) calls `ops.LaunchSession` instead of composing the window by hand (1.11). `main.go` keeps `main`, the root command and `startCmd`.

### 6.3 Delete dead code
`internal/agents` (381 lines, no importer), `claude.IsSessionIdle`/`isUserToolResult`/`isUserSlashCommand` and their tests, `claude.InstallHooks` (V1) and `agents.HookInstaller`, `web.BasicAuth`, `claude.SessionStatus` (use `status.SessionStatus`), `hub-model.js` `markFailed`/`lastFail`, `agents/generic.go:168`'s `init`. `LiftSessionToWorktree` also moves `<id>/subagents`.

---

## Phase 7: documentation

### 7.1 CLAUDE.md to invariants
Cut to ~3k words: build/run, the layer rules, the seams (`ops.Context`, `Commander`, the web `Deps` interfaces), the never-touch rules (status manager ownership, the hook socket, the state file, tmux literal sends, path containment), the testing rules, commit style. Each feature paragraph becomes two lines plus a link to its `docs/plans/*.md`, which already exist for 19 features; the three that don't (menus' probe notes, the drawer, the composer) get one. One table "re-probe on Claude Code upgrade" lists the version each driver was probed against.

### 7.2 This plan's status line
Update the header as phases land, the way the other plan docs do.

---

## Finding → step

| Finding | Step |
|---|---|
| S1 no-login CSRF, Host, rebinding | 1.1 |
| S2 permission sig | 1.2 |
| S3 routes parser crash | 1.3, 4.4 |
| S4 launcher core trust, CSP | 1.5 |
| S5 socket/state file perms, forged events, dead `socket_path` | 1.9 |
| S6 quadratic scanners, ctx, single-flight | 1.4, 1.10, 4.1 |
| S7 blind `/prompt`, no pane lock | 1.6 |
| S8 `.env` after `rm --cached` | 1.7 |
| S9 scope cwd and argv | 1.8 |
| S10 `SameSite`, Basic throttle, secret locations, cookie shadowing | 1.1 (secrets to the state dir: 1.9) |
| S11 `settings.json` write, hook script JSON | 1.9 |
| S12 `MarkDead`, prefix targets | 1.9 |
| S13 excerpt buffering, caches, SSE drops, body caps, timeouts | 1.10 |
| S14 the low findings | 1.11 |
| A1 `app.go` | 2.1-2.10 |
| A2 `ops` boundary, `project` seam, sidebar execs | 3.1, 6.1, 2.6, 2.9 |
| A3 three status derivations | 3.3 |
| A4 web `Server`, caches, status strings, shutdown | 1.10, 5.1-5.4 |
| A5 review duplication and hotspots | 4.1-4.6 |
| A6 dead code | 6.3 |
| A7 tests, lint, mockgen, polling | 0.1-0.5, 1.12, 2.10 |
| A8 CLAUDE.md | 7.1 |

## Rough effort

Phase 0: 1-2 days (the goldens are most of it). Phase 1: 4-6 days, 1.1-1.5 in the first two. Phase 2: 4-5 days with the goldens; 2.2 is the long one (nine commits). Phase 3: 2 days. Phase 4: 4-6 days (4.1 and 4.3 dominate). Phase 5: 1-2 days. Phase 6: 1-2 days. Phase 7: 1 day. Phases 4-7 can run after phase 2 in any order; 1.3 and 1.4 should not wait for phase 4.
