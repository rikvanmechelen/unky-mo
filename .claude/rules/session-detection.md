---
paths:
  - "internal/claude/**"
  - "internal/status/**"
---

# Session Detection

## Claude session data

- **Live sessions**: discovered via `claude agents --json` (shelled out from `internal/claude/agents.go:LiveAgents`, mockable through the `sessionsCommander` seam) — PID, SessionID, CWD, name, Kind, a live Status ("busy"/"idle"). `ReadSessions()` passes `--all` (adds finished background sessions); `LiveSessions()` omits it and defensively re-checks `IsAlive` on each PID. This replaced directly reading `~/.claude/sessions/{PID}.json` (the file the CLI itself still writes per-PID, and what the fake-claude.sh test binary emulates, dual-moded to also answer `agents --json` for integration tests).
- **Session history**: `~/.claude/projects/{encoded-path}/{SessionID}.jsonl` — full conversation
- **Path encoding**: Claude replaces `/`, `_`, and `.` with `-` in directory names
  - e.g. `/Users/rvanmech/workspace/mla_wrapper_app` → `-Users-rvanmech-workspace-mla-wrapper-app`
  - e.g. `/Users/.../unky-mo.worktrees/testing_worktrees` → `-Users-...-unky-mo-worktrees-testing-worktrees`
- **Session title**: stored as `{"type":"custom-title","customTitle":"..."}` entries in JSONL (can appear anywhere in file, last one wins)

## Status detection (`internal/status/`)

Session status (active/idle/permission/question) is managed by `status.Manager` — the single source of truth. Three signal layers feed into it:

1. **Hook events (primary)** — Claude Code hooks (`scripts/status-hook.sh`) fire on `UserPromptSubmit`, `Stop`, `PreToolUse`, `PermissionRequest`, `SessionStart`, `SessionEnd`, and `Notification` (idle_prompt/permission_prompt). Each event is sent to the Unix socket, parsed by `ParseHookPayload`, and applied to the manager via `ProcessHookEvent`.
2. **fsnotify JSONL watcher (reconciliation)** — `status.Watcher` monitors session JSONL files for write events. On change, `ReadJSONLStatus` reads the tail and `ProcessJSONLChange` corrects stale hook state (e.g. if a hook was dropped). JSONL reconciliation does NOT override Permission status — hooks are authoritative for that.
3. **PID liveness (cleanup)** — The 5s session tick checks PIDs. Dead sessions are removed via `MarkDead`.

State transitions:
- `SessionStart` / `UserPromptSubmit` / ordinary `PreToolUse` → Active
- `PreToolUse` where `ToolName` is a known interactive tool (`isInteractiveTool`, currently just `AskUserQuestion`) → **Question**, with `PendingTool`/`PendingInput` captured from the event (see below)
- `Stop` / `Notification(idle_prompt)` / JSONL `end_turn` → Idle
- `PermissionRequest` / `Notification(permission_prompt)` → Permission
- `SessionEnd` / PID dead → removed

There are **no time-based heuristics**. The old 120s JSONL staleness threshold is gone. `ReadJSONLStatus` returns a pure snapshot of the last meaningful JSONL entry.

### Gotcha: `hook_input` wraps the *entire* original Claude payload — don't trust a top-level field

`status-hook.sh` forwards Claude's raw stdin JSON for an event **wholesale** into the outer message's `"hook_input"` field (`printf '{"hook_input":%s,...}'`); it never promotes individual fields (like `tool_name`) up to the outer envelope. For a long time `ParseHookPayload` read `PreToolUse`'s tool name from a top-level `tool_name` field that was never actually sent — meaning `HookEvent.ToolName` was silently empty in production, and the existing unit test fixture encoded the same wrong assumption (so it passed while the real wiring was broken). Fixed by unmarshaling `tool_name`/`tool_input` out of `hook_input` for `PreToolUse`, mirroring how `Notification`'s `notificationType` was already correctly extracted that way. **Lesson: when adding a new field read from a hook event, always check what `status-hook.sh` actually sends, not what the Go struct's existing field tags imply — write the test fixture with the field nested under `hook_input`, matching the real wire shape, and if in doubt, verify against a real captured payload rather than assuming.**

### Interactive tool-blocking (e.g. `AskUserQuestion`) has no dedicated hook — and can never appear in JSONL while pending

Claude Code's `Notification` hook only has two subtypes wired up (matcher `"idle_prompt|permission_prompt"`, `internal/claude/hooks.go`) — there's no third "now showing an interactive menu" notification. A tool like `AskUserQuestion` fires `PreToolUse` (→ Active, same as any other tool call) and then simply blocks waiting for the human; no further hook ever fires to correct the status, so without special-casing it the session is stuck showing "active" indefinitely even though nothing is happening until a human answers.

Compounding this: **the pending tool call is not in the JSONL transcript at all** until it's resolved — JSONL is an append-only *completed*-turn log, and a tool blocked on human input hasn't completed, so there's nothing to tail or reconcile from. This means `ReadJSONLStatus`/`IsSessionIdle`-style JSONL-based detection is *structurally* incapable of ever catching this case, no matter how it's tuned — don't attempt a JSONL-staleness fix here again (one exists in `internal/claude/session.go`'s now-legacy `IsSessionIdle`, used only by the generic multi-agent `SessionReader` abstraction for non-Claude agents that lack hooks entirely — it's a reasonable fallback *there*, but it's not how live Claude-session status works in `status.Manager`). The only viable signal is the hook layer: capture `tool_name`/`tool_input` on `PreToolUse` and special-case known-blocking tool names (`isInteractiveTool` in `status.go`).

The question's content (tool name + raw `tool_input`, e.g. `AskUserQuestion`'s `{questions:[{question,header,options}]}` shape) is captured on `sessionState.PendingTool`/`PendingInput`, exposed via `Manager.PendingQuestion(sessionID)`, and threaded through `sessionView` → `state.ProjectState.PendingQuestionTool`/`PendingQuestionInput` into the shared state file — `mo web`'s chat view renders it from there (see `internal/web/static/chat.js`) since it can't get it from the transcript either, for the same JSONL reason.

**Known gap, not yet extended**: genuine `PermissionRequest` dialogs likely have the same "blocked, nothing in JSONL" problem, and Claude Code's `PermissionRequest` hook payload may *also* carry enough tool name/input to show something useful — but this is **unconfirmed**, not verified against a real payload. `mo web`'s prompt-injection endpoint currently only accepts input when status is `idle` or `question`, deliberately *not* `permission`, until that's checked.

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
