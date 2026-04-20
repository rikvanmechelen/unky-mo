# Instance-ID-keyed sidebar + terminal refactor

## Context

Today the sidebar binds to its Claude session through the tmux window — `windowID` (`@N`) is the primary key for matching rows in the state file, naming the terminal parking session (`mo-terms-<windowID>`), and pruning orphans. Window IDs are stable across `/rename`, but semantically they're the wrong identity: a window is a viewport, a session is the thing we actually care about. This shows up as:

- `refreshState` and every tick has to re-resolve window identity before it can match rows.
- `ownWindowSession` has to do PID-descent gymnastics to pick the right session among concurrent siblings sharing a CWD.
- Terminal tabs parked in `mo-terms-<windowID>` are semantically tied to "this window" even though the user thinks of them as "this session's terminals".
- Hot refresh (`ctrl+alt+r`) can't easily kill-and-respawn sidebars because windowID is the sidebar's only anchor to its data — there's no stable "session X's sidebar" concept.

### Why instance ID, not Claude's session ID

An earlier draft proposed using Claude Code's `sessionID` as the primary key. The problem: Claude generates that ID *after* starting, so the sidebar would need a Loading→Ready state machine with a resolution loop (poll `~/.claude/sessions/*.json`, match by PID descent, 200ms retry, 10s timeout). That's the most complex change in the refactor — and unnecessary.

Instead, we **generate our own instance ID** (12-char hex via `crypto/rand`) at window creation time. Benefits:

- **Available immediately** — no resolution loop, no spinner, no Loading state. Sidebar is operational at boot.
- **Decoupled from Claude Code** — not reliant on Claude writing session files. Works if Claude changes session ID semantics or if a different tool runs in the window.
- **Uniform across all launch paths** — fresh, resume, worktree, import all generate an instance ID in `ops.LaunchSession`.
- **Eliminates PID-descent disambiguation** — concurrent siblings at the same CWD get different instance IDs at creation.

Claude's `sessionID` remains in the state file for conversation identity (resume, sync, history) — it just stops being the binding key for runtime components.

## Verified constraints

- **`claude --resume <id>` preserves Claude's session ID.** Confirmed by Anthropic docs and `internal/sync/sync.go`. Our instance ID is independent — a resume creates a *new* instance ID (new window) pointing at the *same* Claude session.
- **`/compact` preserves Claude's session ID.** Synthetic entries appended to the same JSONL file. Instance ID unaffected.
- **`--fork-session` creates a new Claude session ID**, but we don't invoke it. Not a concern.
- **`/clear` likely creates a new Claude session ID.** Instance ID unaffected — the window and its terminals keep the same instance ID; Claude's session ID updates in the state file on the next tick.

## User-chosen design decisions

- **Instance ID format:** 12-char hex string (e.g., `a1b2c3d4e5f6`) via `crypto/rand`. No external dependency. 48 bits of entropy — collision-free for ~30 concurrent windows.
- **Storage:** tmux window user-option `@mo_instance_id` (precedent: `@mo_hidden` for terminal drawer). Readable via `list-windows -F #{@mo_instance_id}`.
- **Terminal parking sessions use instance ID:** `mo-terms-<instance-id>`. Collision-proof and grep-able.

## Approach: test-first, three phases

Given the blast radius, every production change rides behind green tests that encode the *current* behavior first. The sequence is:

1. **Phase A — Characterization tests** on current behavior. All pass on `main`, no production changes. These tests are the safety net for phases B and C.
2. **Phase B — Refactor** production code behind those green tests. Instance ID becomes the primary key; window ID survives as a transitional fallback only where it must.
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

- `TestLaunchSession_ReturnsClaudePaneID` — `LaunchResult.ClaudePaneID` is populated. (This is our hook point for instance ID propagation in Phase B.)
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

### B1 — Instance ID generation and propagation

Core change. This is the foundation everything else builds on.

**New helper** in `internal/tmux/instanceid.go`:
```go
func GenerateInstanceID() string {
    b := make([]byte, 6)
    crypto/rand.Read(b)
    return hex.EncodeToString(b) // 12-char hex, e.g. "a1b2c3d4e5f6"
}
```

**`tmux.Window` struct** (`client.go:474`): add `InstanceID string`.

**`ListWindows` format** (`client.go:199`): append `#{@mo_instance_id}` to the format string; parse into `Window.InstanceID`.

**`ops.LaunchSession`** (`launch.go`):
1. `id := tmux.GenerateInstanceID()`
2. After `CreateWindow`, call `SetWindowOption(windowTarget, "@mo_instance_id", id)`
3. Thread `id` into the sidebar split command: `mo sidebar --instance-id=<id>`
4. Store `id` on `LaunchResult` (new field `InstanceID string`)

**`cmd/mo/main.go`**: parse `--instance-id` flag on the `sidebar` subcommand; pass to `sidebar.NewModel`.

**`state.ProjectState`** (`state.go`): add `InstanceID string \`json:"instanceId"\``.

**Main TUI `writeStateFile`** (`app.go`): propagate `Window.InstanceID` → `ProjectState.InstanceID`.

**Test additions:**
- `TestGenerateInstanceID_Length` — always 12 chars
- `TestGenerateInstanceID_Unique` — 1000 calls, all distinct
- `TestLaunchSession_SetsInstanceIDWindowOption` — gomock expectation on `SetWindowOption`
- `TestLaunchSession_ThreadsInstanceIDIntoSidebarArgs` — sidebar command contains `--instance-id=<id>`

### B2 — Terminal parking keyed by instance ID

Change `termSession()`:
```go
func (m Model) termSession() string {
    if m.instanceID != "" {
        return ttmux.MoTermsSession + "-" + m.instanceID
    }
    // Transitional fallback (pre-refactor windows without instance ID)
    switch {
    case m.windowID != "":
        return ttmux.MoTermsSession + "-" + strings.TrimPrefix(m.windowID, "@")
    case m.windowName != "":
        return ttmux.MoTermsSession + "-" + sanitizeTermSessionSuffix(m.windowName)
    default:
        return ttmux.MoTermsSession
    }
}
```

**`orphanedTermSessions`** (`app.go`): scan tmux sessions matching `mo-terms-*`. For `mo-terms-<instanceID>` patterns, kill when the instance ID is not in the current live window set. Keep the old `mo-terms-<N>` pattern matching as transitional fallback.

**Migrate A3 tests:** add `TestTermSession_UsesInstanceID` — `instanceID="a1b2c3d4e5f6"` returns `mo-terms-a1b2c3d4e5f6`. Original tests remain as fallback-path coverage.

### B3 — State file row matching by instance ID

Update `itemMatchesOwnWindow` → `itemMatchesOwnInstance`:

```
If sidebar has instanceID and item has InstanceID:
    match on InstanceID only.
Else (pre-refactor rows, transitional):
    fall back to WindowID / WindowName as today.
```

This is the critical simplification: once the instance ID is set, the sidebar's tick does *one* key lookup. Rename of the tmux window no longer affects matching at all.

**Migrate A4 tests:** add `TestItemMatchesOwnInstance_*` cases. Old WindowID-preferred cases become fallback-branch coverage.

### B4 — Hot refresh with instance ID

Replace `restartSidebars` (`app.go`) with:

```go
For each project window:
    instanceID := window.InstanceID  // from ListWindows via @mo_instance_id
    tmux.KillPane(window.1)
    tmux.SplitWindow(window, sidebarWidth, cwd,
        fmt.Sprintf("%s sidebar --instance-id=%s", moBinary, instanceID))
```

The `mo-terms-<instanceID>` parking session is not touched — it outlives the sidebar process, so the fresh sidebar's `refreshTerminals` finds every tab. No more `SendRawKeys "M-C-r"` dance; no more self-exec path in the sidebar.

**Test addition:** `TestRestartSidebars_ReusesInstanceIDFromWindow` (with ops mocks).

### B5 — Simplify `ownWindowSession`

With instance ID matching in B3, `ownWindowSession` no longer needs to disambiguate concurrent siblings every tick. The sidebar knows its own row by instance ID.

`ownWindowSession` can be simplified to: find the state row matching our instance ID, read its `SessionID` field (Claude's session ID) to compute tokens and status. The PID-descent logic is deleted.

**Migrate A2 tests:** the `pickOwnSession` helper is no longer needed for the hot path. Keep the tests as documentation of the old behavior, or delete if they test dead code.

## Phase C: Test migration + cleanup

Once Phase B is green:

- Graduate `//go:build sidebarregression` tests that the refactor fixes:
  - `TestDrawerTargetsSurviveImmediateRename` → default build (drawer no longer keys on window name).
  - `TestRefreshSyncStatusSurvivesWindowRename` — verify whether the same change fixes it; if yes, graduate; if no, leave gated with an updated comment.
- Delete `itemMatchesOwnWindow` WindowID-only path once all rows carry InstanceID for live sessions (placeholder rows still need name-match — keep that branch).
- Delete the sidebar's `alt+ctrl+r` self-exec handler and the main TUI's `SendRawKeys "M-C-r"` plumbing.
- Remove transitional fallbacks in `termSession()` and `orphanedTermSessions` (or keep for one release cycle).
- Update `CLAUDE.md` sections: "Terminal Drawer", "TUI Key Handling", "State File Schema", the hot-refresh note under Conventions.

## Edge cases

- **`/clear` creates a new Claude session ID** (likely): instance ID is unaffected — window keeps the same `@mo_instance_id`, terminals stay bound. Claude's `SessionID` updates in the state file on the next tick.
- **Claude crashes before writing the session file:** irrelevant to instance ID — sidebar is already operational. Session status shows as "none" until the main TUI detects a live session.
- **Concurrent siblings at the same CWD:** each `ops.LaunchSession` call generates a distinct instance ID. `mo-terms-<idA>` and `mo-terms-<idB>` are naturally disjoint. No PID-descent needed.
- **Old sidebar process still running during hot refresh:** killing pane `.1` via tmux delivers SIGHUP to the sidebar; it exits cleanly. No risk of two sidebars fighting because B4 kills before respawning.
- **Pre-refactor windows (no `@mo_instance_id` set):** `ListWindows` returns empty `InstanceID`. State matching falls back to WindowID/WindowName. `termSession()` falls back to windowID. Seamless transition.

## Critical files

| Path | Change |
|---|---|
| `internal/tmux/instanceid.go` (new) | `GenerateInstanceID()` helper |
| `internal/tmux/client.go` | `InstanceID` on `Window`, `#{@mo_instance_id}` in format, parse |
| `internal/ops/launch.go` | Generate instance ID, set window option, thread into sidebar args |
| `internal/state/state.go` | Add `InstanceID` field to `ProjectState` |
| `internal/tui/sidebar/model.go` | Accept `instanceID`, use for state matching + `termSession()` |
| `internal/tui/sidebar/deps.go` | Possibly simplify interfaces (no PID resolution needed on hot path) |
| `internal/tui/app.go` | `restartSidebars` rewrite, `orphanedTermSessions` by instance ID, `writeStateFile` includes `InstanceID` |
| `cmd/mo/main.go` | Parse `--instance-id` flag on sidebar subcommand |

## Verification

- **Every phase**: `make test`, `make test-race`, `make test-expectfail` all green. `make mocks-check` passes.
- **Manual after Phase B:**
  1. `make install && ./mo` → open a project, verify sidebar is immediately functional (no spinner).
  2. `tmux show-window-option @mo_instance_id` on a project window — returns 12-char hex.
  3. Open a terminal with `T`, verify `tmux list-sessions | grep mo-terms-` matches the instance ID.
  4. `/rename foo` in the Claude pane — sidebar stays bound, terminal drawer still works.
  5. Start a second concurrent session on the same project, verify each sidebar binds to its own instance ID and drawers are independent.
  6. `ctrl+alt+r` — terminals preserved across restart, sidebar immediately functional.
  7. Kill Claude in the pane — window closes, `orphanedTermSessions` prunes the parking session on next 5s tick.
- **Integration test**: `TestHotRefresh_TabsSurviveSidebarRestart` under `//go:build integration` exercises step 6 end-to-end.
