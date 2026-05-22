---
paths:
  - "internal/claude/**"
  - "internal/status/**"
---

# Session Detection

## Claude session data

- **Live sessions**: `~/.claude/sessions/{PID}.json` — PID, SessionID, CWD, name
- **Session history**: `~/.claude/projects/{encoded-path}/{SessionID}.jsonl` — full conversation
- **Path encoding**: Claude replaces `/`, `_`, and `.` with `-` in directory names
  - e.g. `/Users/rvanmech/workspace/mla_wrapper_app` → `-Users-rvanmech-workspace-mla-wrapper-app`
  - e.g. `/Users/.../unky-mo.worktrees/testing_worktrees` → `-Users-...-unky-mo-worktrees-testing-worktrees`
- **Session title**: stored as `{"type":"custom-title","customTitle":"..."}` entries in JSONL (can appear anywhere in file, last one wins)

## Status detection (`internal/status/`)

Session status (active/idle/permission) is managed by `status.Manager` — the single source of truth. Three signal layers feed into it:

1. **Hook events (primary)** — Claude Code hooks (`scripts/status-hook.sh`) fire on `UserPromptSubmit`, `Stop`, `PreToolUse`, `PermissionRequest`, `SessionStart`, `SessionEnd`, and `Notification` (idle_prompt/permission_prompt). Each event is sent to the Unix socket, parsed by `ParseHookPayload`, and applied to the manager via `ProcessHookEvent`.
2. **fsnotify JSONL watcher (reconciliation)** — `status.Watcher` monitors session JSONL files for write events. On change, `ReadJSONLStatus` reads the tail and `ProcessJSONLChange` corrects stale hook state (e.g. if a hook was dropped). JSONL reconciliation does NOT override Permission status — hooks are authoritative for that.
3. **PID liveness (cleanup)** — The 5s session tick checks PIDs. Dead sessions are removed via `MarkDead`.

State transitions:
- `SessionStart` / `UserPromptSubmit` / `PreToolUse` → Active
- `Stop` / `Notification(idle_prompt)` / JSONL `end_turn` → Idle
- `PermissionRequest` / `Notification(permission_prompt)` → Permission
- `SessionEnd` / PID dead → removed

There are **no time-based heuristics**. The old 120s JSONL staleness threshold is gone. `ReadJSONLStatus` returns a pure snapshot of the last meaningful JSONL entry.

## Worktree session detection

Worktrees use the `<project>.worktrees/<branch>` directory convention. Session detection matches CWDs containing `.worktrees/` back to parent projects by stripping the suffix to recover the main project path.

**Data flow**: `refreshSessions` classifies each live session and reads its status from `status.Manager.Status(sessionID)`, emitting one `sessionView` per session (`ProjectPath` + `Parent` + `IsWorktree` flags) → `updateProjectStatuses` stashes them on `m.sessionViews` → `writeStateFile` and `refreshDashSessions` iterate the same view list. Worktree sessions have `Parent` set to the parent project's name so the sidebar renders them indented under the parent.

- **Window naming**: worktree windows are named `<project>@<branch>` (e.g. `unky-mo@feature-auth`)
- **State file entries**: worktree entries have `name: "@branch"`, `parent: "project"`, and their own status
- **Session matching is path-based throughout.** The sidebar's `refreshFromSessions()` fallback must compare `item.Path` (filesystem path) against `session.CWD`, never `item.WindowName` (display name). Mixing these up silently breaks detection.

## Strays & import-external

A **stray** is a live Claude session whose CWD doesn't map to any known project in the workspace. Detected during the 5s session refresh by classifying live sessions against `projectPaths` and the git-root lookup (`project.FindGitRoot`):

- **Git-backed stray** — CWD isn't a known project but *is* inside some git repo. Rendered in the `Projects` section with branch + dirty info.
- **Non-git stray** — CWD is outside any git repo (e.g. `~`, `/tmp`). Rendered in a separate `External` section.
- **External flag** — set when the Claude PID is *not* a descendant of any pane in the mo tmux session (`claude.IsDescendantOf` against `tmux.PanePIDs`). `enter` on an External row opens an import prompt.

`importExternalSession(pid, sessionID, cwd, windowName)`: SIGTERM the orphan, poll up to ~2s for exit (flushes JSONL), then `claude --resume <sessionID>` in a fresh tmux window with a sidebar.
