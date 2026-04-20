# Session-ID-keyed sidebar + terminal refactor

## Context

Today the sidebar binds to its Claude session through the tmux window — `windowID` (`@N`) is the primary key for matching rows in the state file, naming the terminal parking session (`mo-terms-<windowID>`), and pruning orphans. Window IDs are stable across `/rename`, but semantically they're the wrong identity: a window is a viewport, a session is the thing we actually care about. This shows up as:

- `refreshState` and every tick has to re-resolve window identity before it can match rows.
- `ownWindowSession` has to do PID-descent gymnastics to pick the right session among concurrent siblings sharing a CWD.
- Terminal tabs parked in `mo-terms-<windowID>` are semantically tied to "this window" even though the user thinks of them as "this Claude session's terminals".
- Hot refresh (`ctrl+alt+r`) can't easily kill-and-respawn sidebars because windowID is the sidebar's only anchor to its data — there's no stable "session X's sidebar" concept.

This refactor makes `sessionID` the primary key: sidebars bind to a session ID at boot, terminal parking sessions are named `mo-terms-<sessionID>`, and state-file matching prefers session ID. Secondary benefits: the 1s tick becomes a flat lookup by one key, hot refresh becomes trivially "kill pane, respawn with `--session-id=X`", and concurrent siblings stop needing PID-descent tricks.

## Verified constraints

- **`claude --resume <id>` preserves the session ID.** Confirmed by Anthropic docs and by `internal/sync/sync.go` (whole module assumes session ID is stable across resume).
- **`/compact` preserves the session ID.** Synthetic user entries are appended to the same JSONL file — if the ID forked the filename would change. Research initially conflated `/compact` with the explicit `--fork-session` flag; the unky-mo code at `internal/claude/session.go:322-328` already relies on `/compact` being in-session.
- **`--fork-session` creates a new session ID**, but we don't invoke it. Not a concern unless we add a feature that does.
- **`/clear` behavior: unverified.** Likely starts a fresh session with a new ID. Handled by the same orphan-pruning logic that handles session termination (see B2 below).
- **Sidebar launches before Claude writes `~/.claude/sessions/{PID}.json`.** Today the sidebar discovers its session ID lazily on the first 1s tick. The refactor makes that discovery explicit and reflected in UI state.

## User-chosen design decisions

- **Sidebar boots with visual continuity.** Split the sidebar pane immediately (no layout jump) and show a loading indicator while the session ID is being resolved in the background. Once resolved, transition to full state.
- **Terminal parking sessions use full session UUID:** `mo-terms-<full-uuid>[-N]`. Collision-proof and grep-able against `~/.claude/projects/*/` JSONL filenames.

## Approach: test-first, three phases

Given the blast radius, every production change rides behind green tests that encode the *current* behavior first. The sequence is:

1. **Phase A — Characterization tests** on current behavior. All pass on `main`, no production changes. These tests are the safety net for phases B and C.
2. **Phase B — Refactor** production code behind those green tests. Session ID becomes the primary key; window ID survives as a transitional fallback only where it must.
3. **Phase C — Migrate tests** to assert the new contract, graduate `sidebarregression` tests into the default build where the refactor fixes them, and delete dead fallback paths.

## Phase A: Characterization tests (lock in current behavior)

All additive. Run `make test` after each — none should fail.

### A1 — Sidebar identity resolution
**File:** new `internal/tui/sidebar/model_bootstrap_test.go`

Tests for `NewModelWithDeps` wiring against a fake `WindowResolver`:
- `TestNewModel_UsesResolverWindowID` — resolver returns `@5`; model's `windowID` is `@5`.
- `TestNewModel_FallsBackToWindowNameOnEmptyID` — resolver returns `("my-window", "")`; model works purely from name.
- `TestNewModel_CapturesWorkingDir` — asserts `windowPath` matches `os.Getwd()`.

(There are no `NewModel` tests today — this section is a pure gap fill.)

### A2 — Own-window session disambiguation
**File:** extend `internal/tui/sidebar/helpers_test.go`

Extract the concurrent-sibling logic from `ownWindowSession` (lines 778-800) into a pure helper `pickOwnSession(sessions []claude.Session, paneTree []int, reader ClaudeReader) *claude.Session` and test:
- `TestPickOwnSession_SingleSession` — one candidate, returns it.
- `TestPickOwnSession_TwoSiblings_PicksPaneDescendant` — two candidates at same CWD, only one's PID descends from the window's panes; picks that one.
- `TestPickOwnSession_NoMatchingDescendant_ReturnsNil` — safety.

### A3 — Terminal parking session naming (current)
**File:** new `internal/tui/sidebar/term_session_test.go`

Lock in the current `termSession()` derivation so we notice when we change it:
- `TestTermSession_UsesWindowID` — model with `windowID="@5"` returns `mo-terms-5`.
- `TestTermSession_FallsBackToSanitizedName` — empty windowID, name `"my:proj.foo"` → `mo-terms-my-proj-foo`.
- `TestTermSession_BareFallback` — both empty → `mo-terms`.

### A4 — State file row matching
**File:** extend `internal/tui/sidebar/state_test.go`

- `TestItemMatchesOwnWindow_PrefersWindowIDWhenBothPresent` — item has `WindowID=@5, WindowName="foo"`; model has `windowID=@5, windowName="bar"`; matches.
- `TestItemMatchesOwnWindow_FallsBackToNameWhenIDMissing` — both IDs empty, names equal; matches.
- `TestItemMatchesOwnWindow_WindowIDMismatchFails` — IDs differ; doesn't match even if names match.

### A5 — Launch flow observable surface
**File:** extend `internal/ops/launch_test.go`

- `TestLaunchSession_ReturnsClaudePaneID` — `LaunchResult.ClaudePaneID` is populated. (This is our hook point for session-ID discovery in Phase B — we need the pane ID so we can query `#{pane_pid}`.)
- `TestLaunchSession_SidebarSplitHappensAfterClaudeExec` — ordering assertion via gomock `InOrder`.

### A6 — Hot-refresh: terminal parking survives
**File:** new `internal/tui/sidebar/hot_refresh_test.go` (pure unit, no tmux)

- `TestTerminals_RediscoveredFromTmuxAfterModelRebuild` — given a fake tmux that returns three live panes in `mo-terms-<key>`, `refreshTerminals` on a fresh model repopulates `terminals`. This encodes the "in-memory state is disposable" contract.

Plus an integration variant under `//go:build integration`:
- `TestHotRefresh_TabsSurviveSidebarRestart` — real tmux via isolated socket; open window with sidebar, create 2 terminal tabs, kill the sidebar pane, respawn, verify tabs are re-linked.

### A7 — Orphan pruning
**File:** extend tests around `internal/tui/app.go` `orphanedTermSessions` / `pruneOrphanedTermSessions` (1391-1437).

Lock in the pattern match (`mo-terms-<windowID>` → kill when `@N` is gone). Tests here are light today; add table-driven cases covering windowID patterns and "leave alone when window still exists."

**Exit criteria for Phase A:** `make test` green. No production code touched.

## Phase B: Refactor behind green tests

### B1 — Sidebar boot with Loading → Ready state

Split the sidebar pane immediately (preserves current layout, no visual jump). Boot into `sessionState = Loading`.

**New field on `Model`:** `sessionID string`, `sessionState sessionState` (`Loading|Ready|Error`), `sessionResolveStart time.Time`.

**Resolution loop** (new `resolveSessionID` Bubbletea `tea.Cmd`):
- Query Claude's pane 0 PID: `tmux display-message -t <window>.0 -p '#{pane_pid}'`.
- Poll `~/.claude/sessions/*.json` for a Session whose `PID` is that pane_pid or a descendant (`claude.IsDescendantOf`).
- Retry every 200ms, timeout 10s → `sessionState = Error`.
- On match: set `sessionID`, flip to `Ready`, trigger one full `refreshState`.

**UI during Loading:**
- Session row shows a spinner (`bubbles/v2/spinner` is already a dep) + `resolving session…`.
- `T` key handler returns a transient status: `terminal unavailable until session resolved`.
- Popup (`) also gated.
- Footer shows `s syncing ·` dot as `·` (neutral) instead of session-dependent status.

**Test additions:**
- `TestResolveSessionID_FindsMatchingPID`
- `TestResolveSessionID_TimesOut` (with controllable clock)
- `TestSidebarBoot_BlocksTerminalCreationUntilReady`
- `TestSidebarBoot_TransitionsToReadyOnResolve`

### B2 — Terminal parking session keyed by sessionID

Change `termSession()`:
```
Ready:   mo-terms-<full-uuid>
Loading: returns error / empty — callers must not invoke
```

No windowID fallback in `termSession()` — enforcing the invariant "terminals only exist once the session ID is known" is what makes the 1s tick simple.

**`orphanedTermSessions`** (`internal/tui/app.go`): scan tmux sessions for the `mo-terms-<uuid>` pattern, kill when UUID is not in the current live-session set. The current windowID-based scan is deleted; UUIDs are less ambiguous and don't require the `@N`-parse step.

**Migrate A3 tests:** `TestTermSession_UsesSessionID` — `sessionID="abc-123-…"` returns `mo-terms-abc-123-…`. Delete or invert the windowID-fallback assertions.

### B3 — State file row matching prefers SessionID

`ProjectState.SessionID` already exists. Update `itemMatchesOwnWindow` → `itemMatchesOwnSession`:

```
If sidebar has sessionID and item has SessionID:
    match on SessionID only.
Else (placeholder rows, Loading state):
    fall back to WindowID / WindowName as today.
```

This is the critical simplification: once Ready, the sidebar's tick does *one* key lookup. Rename of the tmux window no longer affects matching at all.

**Migrate A4 tests:** rename to `TestItemMatchesOwnSession_*`. The old WindowID-preferred cases become the "Loading / placeholder" branch.

### B4 — `ops.LaunchParams` gains optional SessionID

Add `SessionID string // optional — known for --resume flows`. When passed, `ops.LaunchSession` threads it into the sidebar command as `mo sidebar --session-id=<uuid>`, so the sidebar boots directly into `Ready`. For fresh launches (no resume), SessionID is empty and the sidebar falls back to B1's resolution loop.

**Callers to update:** `ops.ResumeInDir` passes `p.SessionID`. `ops.ImportExternalSession` passes the imported session's ID. Fresh launches (`LaunchSession`, `CreateWorktreeAndLaunch`, `OpenBranchInMain`, `ParkAndLaunch`) leave it empty — Claude generates the ID.

**Test addition:** `TestLaunchSession_ThreadsSessionIDIntoSidebarArgs` — gomock expectation on `SplitWindow` that the command contains `--session-id=<uuid>`.

### B5 — Hot refresh = kill + respawn (cleaner model)

Replace `restartSidebars` (app.go:3114-3134) with:

```go
For each project window:
    sessionID := state.SessionIDForWindow(windowID)
    tmux.KillPane(window.1)
    tmux.SplitWindow(window, sidebarWidth, cwd,
        fmt.Sprintf("%s sidebar --session-id=%s", moBinary, sessionID))
```

The `mo-terms-<sessionID>` parking session is not touched — it outlives the sidebar process, so the fresh sidebar's `refreshTerminals` finds every tab. No more `SendRawKeys "M-C-r"` dance; no more self-exec path in the sidebar (can delete that branch in `sidebar/model.go:274-279`).

**Test addition:** `TestRestartSidebars_ReusesSessionIDFromStateFile` (with ops mocks).

## Phase C: Test migration + cleanup

Once Phase B is green:

- Graduate `//go:build sidebarregression` tests that the refactor fixes:
  - `TestDrawerTargetsSurviveImmediateRename` → default build (drawer no longer keys on window name).
  - `TestRefreshSyncStatusSurvivesWindowRename` — verify whether the same change fixes it; if yes, graduate; if no, leave gated with an updated comment.
- Delete `itemMatchesOwnWindow` WindowID-only path once all rows carry SessionID for live sessions (placeholder rows still need name-match — keep that branch).
- Delete the sidebar's `alt+ctrl+r` self-exec handler and the main TUI's `SendRawKeys "M-C-r"` plumbing.
- Update `CLAUDE.md` sections: "Terminal Drawer", "TUI Key Handling", "State File Schema", the hot-refresh note under Conventions.

## Edge cases

- **`/clear` creates a new session ID** (likely): old `mo-terms-<old-uuid>` gets pruned by `orphanedTermSessions` on the next 5s tick; new session ID triggers a fresh parking namespace. User loses their parked shells — arguably correct, since `/clear` is a hard reset.
- **Claude crashes before writing the session file:** sidebar stays in Loading for 10s, transitions to Error with a "session never started — close this pane" message. Pane still closable by the user; window's `pane-exited` hook kills the window when Claude's pane dies.
- **Concurrent siblings at the same CWD:** both sidebars resolve distinct session IDs via PID descent at boot, so `mo-terms-<uuidA>` and `mo-terms-<uuidB>` are naturally disjoint. The whole `ownWindowSession` disambiguation helper collapses from "runs every tick" to "runs once at boot."
- **Old sidebar process still running during hot refresh:** killing pane `.1` via tmux delivers SIGHUP to the sidebar; it exits cleanly. No risk of two sidebars fighting over the same `mo-terms` session because B5 kills before respawning.

## Critical files

| Path | Role |
|---|---|
| `internal/tui/sidebar/model.go` | Add `sessionID`/`sessionState` fields; resolution loop; Loading UI; `termSession` rewrite |
| `internal/tui/sidebar/deps.go` | Extend `TmuxClient` if needed (`PanePID` may already be there via `WindowPanePIDs`) |
| `internal/tui/sidebar/helpers_test.go` + new test files | A1–A7 characterization tests |
| `internal/ops/launch.go` | Add `LaunchParams.SessionID`; thread into sidebar args |
| `internal/ops/resume.go` | Pass `SessionID` through to `LaunchParams` |
| `internal/ops/import.go` | Same for imports |
| `internal/tui/app.go` | `restartSidebars` rewrite (B5); `orphanedTermSessions` UUID pattern |
| `internal/state/state.go` | No schema change — `SessionID` field already exists |
| `cmd/mo/main.go` | Parse `--session-id` flag on `mo sidebar` |
| `CLAUDE.md` | Doc updates (Phase C) |

## Verification

- **Every phase**: `make test`, `make test-race`, `make test-expectfail` all green. `make mocks-check` passes (we'll need to regenerate after extending `TmuxClient` if `PanePID` is added).
- **Manual after Phase B:**
  1. `make install && ./mo` → open a project, verify sidebar shows spinner briefly, then switches to full state.
  2. Open a terminal with `T`, verify it lands in `tmux list-sessions | grep mo-terms-` matching the session UUID.
  3. `/rename foo` in the Claude pane — sidebar stays bound, terminal drawer still works (this was broken before).
  4. Start a second concurrent session on the same project (`n` → `c`), verify each sidebar binds to its own UUID and drawers are independent.
  5. `ctrl+alt+r` — terminals preserved across restart, sidebar shows brief Loading then Ready.
  6. Kill Claude in the pane — window closes, `orphanedTermSessions` prunes the parking session on next 5s tick (verify via `tmux list-sessions`).
- **Integration test**: the new `TestHotRefresh_TabsSurviveSidebarRestart` under `//go:build integration` exercises 5 end-to-end with a real tmux server.
