---
paths:
  - "internal/ops/**"
  - "internal/tui/app.go"
---

# Session Lifecycle: Multi-session, Cleanup, Lift

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

## Lift session into a new worktree

The `w` key on a `br-session` row on the project detail screen moves the selected session — live or historical — to a fresh new branch + worktree off the source's HEAD. Plain `branch` / `br-empty` / `br-remote` rows keep today's `w` behavior (worktree for that existing branch, immediate launch, no prompt). State fields and helpers live in `internal/tui/app.go` (`liftSession*`, `pendingLiftDirtyActive`, `pendingLiftBranch`).

Flow (see `startLiftSessionPrompt` → `decideLiftDirty` → `runLiftSession`):

1. **Branch-name input** — free-text prompt, same shape as `W`. `enter` submits, `esc` cancels.
2. **Dirty-state prompt** — only shown when the source cwd has uncommitted changes. `[s] stash + pop / [l] leave in source / [n] cancel`. Non-destructive menu, so `enter` defaults to `s`.
3. **Execution** — `ops.LiftSessionToWorktree` (`internal/ops/lift.go`): optional stash at source → `project.CreateNewBranchWorktree` off source HEAD → optional pop in new worktree → SIGTERM live PID + wait → `tmux kill-window` → **rename the JSONL** from `~/.claude/projects/<encoded-sourceCwd>/<id>.jsonl` to `~/.claude/projects/<encoded-newCwd>/<id>.jsonl`. No auto-launch. User presses `enter` on the relocated `br-session` row to resume via the regular `resumeInDir` path.

### Key invariants

- **No auto-launch.** Earlier versions ran `claude --resume <id>` in a fresh tmux window as the last step. That hid launch errors behind the `pane-exited` hook (the window closed before the user could read the red flash), and raced with claude's own JSONL write at the new encoded-cwd. The simpler "move the JSONL, let the user resume" flow reuses the known-good resume path.
- **SIGTERM before the move.** Claude must release its file handle before `os.Rename` — otherwise writes race with the rename.
- **New branch must not already exist.** `project.CreateNewBranchWorktree` pre-flights with `git rev-parse --verify` and refuses up front. Stash is rolled back if the pre-flight fails so no user work is orphaned.
- **Missing JSONL is non-fatal.** `LiftResult.MovedJSONL` reports whether a JSONL was found and moved; false is acceptable (e.g. synced-but-not-pulled rows).
- **Old worktree is left in place.** Cleanup is an explicit `x` afterwards.
- **Source PID is recovered at prompt entry.** `RecentSession` doesn't carry PID, so `startLiftSessionPrompt` iterates `LiveSessions()` to match by SessionID. PID=0 and `SourceWindow=""` on historical rows mean the SIGTERM/kill-window steps are skipped.
