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

1. **Hook events (primary)** — Claude Code hooks (`internal/claude/status-hook.sh`, embedded in the binary and written to `~/.config/unky-mo/hooks/` on every TUI start by `claude.EnsureStatusHookScript`; `claude.EnsureHooksV2` then reinstalls the settings.json entries unless they already match exactly) fire on `UserPromptSubmit`, `Stop`, `PreToolUse`, `PermissionRequest`, `SessionStart`, `SessionEnd`, and `Notification` (idle_prompt/permission_prompt). Each event is sent to the Unix socket, parsed by `ParseHookPayload`, and applied to the manager via `ProcessHookEvent`. Delivery path: the script pipes its message to `mo hooks send` (the installing binary's path is baked into the script as `MO_BIN`; `nc -U` is only a fallback). `notify.Send` compacts it to one line for the socket. `internal/notify` passes any message with `hook_event_name` through as a raw `NotifyHookEvent`, and the TUI's `hookEventFromNotification` turns it into a `HookEvent` via `ParseHookPayload`. The session ID comes from `hook_input.session_id` (Claude's own payload); the envelope's copy is only a fallback.
2. **fsnotify JSONL watcher (reconciliation)** — `status.Watcher` monitors session JSONL files for write events. On change, `ReadJSONLStatus` reads the tail and `ProcessJSONLChange` corrects stale hook state (e.g. if a hook was dropped). JSONL reconciliation does NOT override Permission or Question status — hooks (and the agents signal below, for Question) are authoritative. For Question this matters in practice: Claude keeps appending metadata entries (`ai-title`, `mode`, `attachment`, …) to the JSONL while a menu is open, which would otherwise read as "active" and wipe the pending question.
3. **PID liveness (cleanup)** — The 5s session tick checks PIDs. Dead sessions are removed via `MarkDead`.
4. **`claude agents --json` status (question backstop)** — the 5s tick already shells out to it for discovery; its per-session `status`/`waitingFor` ride along on `claude.Session` into `Manager.ProcessAgentStatus`. Only `"waiting"` + `"input needed"` (what Claude reports while an `AskUserQuestion` menu is open) maps to Question — other `waitingFor` values (`"dialog open"`, `"goal proposal"`, …) are ignored and Permission is never overridden. A hook newer than the snapshot wins. Leaving Question: `"idle"` clears any question; `"busy"` clears only one this signal set itself (`QuestionFromAgent`), since a hook-set question can briefly read "busy" before Claude publishes its waiting state. An agents-sourced question has no `PendingTool`/`PendingInput` — `mo web` shows a generic "answer in the terminal" banner for it.

State transitions:
- `UserPromptSubmit` / ordinary `PreToolUse` → Active
- `SessionStart` → Idle (startup, `--resume` and `/clear` all land at the prompt), except `source: "compact"` → Active (auto-compaction fires mid-turn). `source` is read from the forwarded `hook_input`.
- `PreToolUse` where `ToolName` is a known interactive tool (`isInteractiveTool`, currently just `AskUserQuestion`) → **Question**, with `PendingTool`/`PendingInput` captured from the event (see below)
- `Stop` / JSONL `end_turn` → Idle
- `PermissionRequest` → Permission, **except** when its `tool_name` is an interactive tool (`AskUserQuestion`) → Question (see the gotcha below)
- `Notification(permission_prompt)` → Permission, unless the session is showing a Question (the notification names no tool; it's that question's own prompt notification)
- `Notification(idle_prompt)` → Idle, but never over an open Question or Permission (marking those idle would let `mo web` type into the dialog)
- `SessionEnd` / PID dead → removed

There are **no time-based heuristics**. The old 120s JSONL staleness threshold is gone. `ReadJSONLStatus` returns a pure snapshot of the last meaningful JSONL entry.

### Gotcha: the V2 hook pipeline was silently dead end to end until Oct 2026

Three independent breaks meant no V2 hook event ever reached `status.Manager`, so every status came from JSONL reconciliation and the agents backstop. Each failure was silent:
1. **Delivery.** The script used `nc -U … 2>/dev/null`, and `nc` wasn't installed.
2. **Session ID.** The script read `$CLAUDE_SESSION_ID`, but Claude Code sets `CLAUDE_CODE_SESSION_ID`, so every event was tagged `unknown`.
3. **Socket server.** `internal/notify` only understood the three legacy notification types and dropped every `hook_event_name` message. `ParseHookPayload` had no production caller.

The bufio line limit (64KB default) would also have dropped large `PreToolUse` payloads; it's raised now. If hook-driven status ever looks wrong again, verify the whole path live by piping a synthetic event through the installed script: `echo '{"hook_event_name":"Stop","session_id":"<id>"}' | HOOK_EVENT_NAME=Stop ~/.config/unky-mo/hooks/status-hook.sh`, then watch the state file. Don't trust unit tests of one stage alone.

### Gotcha: `hook_input` wraps the *entire* original Claude payload — don't trust a top-level field

`status-hook.sh` forwards Claude's raw stdin JSON for an event **wholesale** into the outer message's `"hook_input"` field (`printf '{"hook_input":%s,...}'`); it never promotes individual fields (like `tool_name`) up to the outer envelope. For a long time `ParseHookPayload` read `PreToolUse`'s tool name from a top-level `tool_name` field that was never actually sent — meaning `HookEvent.ToolName` was silently empty in production, and the existing unit test fixture encoded the same wrong assumption (so it passed while the real wiring was broken). Fixed by unmarshaling `tool_name`/`tool_input` out of `hook_input` for `PreToolUse` (and later `PermissionRequest`). `Notification`'s type had the mirror-image bug: the parser read camelCase `notificationType`, but Claude Code sends **`notification_type`**, so every V2 Notification hook failed to parse and was dropped until Oct 2026 (the camelCase field is still accepted for the legacy format). **Lesson: when adding a new field read from a hook event, always check what `status-hook.sh` actually sends, not what the Go struct's existing field tags imply — write the test fixture with the field nested under `hook_input`, matching the real wire shape, and if in doubt, verify against a real captured payload rather than assuming.**

### Interactive tool-blocking (e.g. `AskUserQuestion`) has no dedicated hook, and the JSONL can't tell it's blocked

Claude Code's `Notification` hook only has two subtypes wired up (matcher `"idle_prompt|permission_prompt"`, `internal/claude/hooks.go`) — there's no third "now showing an interactive menu" notification. A tool like `AskUserQuestion` fires `PreToolUse` (→ Active, same as any other tool call) and then simply blocks waiting for the human; no further hook ever fires to correct the status, so without special-casing it the session is stuck showing "active" indefinitely even though nothing is happening until a human answers.

Compounding this: **the JSONL can't tell a blocked tool call from a running one.** (Correction, verified on Claude Code 2.1.289: the pending call *is* in the transcript. The assistant line with the `tool_use` is written before the dialog opens, with no `tool_result` after it, and only metadata lines (`last-prompt`, `ai-title`, `mode`, `permission-mode`, `atis-latch`) follow while it waits. An earlier version of this note said it wasn't there at all.) An open `AskUserQuestion` reads exactly like any tool that's still running, so `ReadJSONLStatus`/`IsSessionIdle`-style detection can't decide the *status*: don't attempt a JSONL-staleness fix here (one exists in `internal/claude/session.go`'s now-legacy `IsSessionIdle`, used only by the generic multi-agent `SessionReader` abstraction for non-Claude agents that lack hooks entirely). The status comes from the hook layer, with `claude agents --json`'s `waiting`/`input needed` as a backstop (see signal layer 4; this needs the V2 hooks from `mo hooks install`, since the older V1 install only registers Notification + Stop, so PreToolUse never fires; `mo hooks status` tells you which is installed): capture `tool_name`/`tool_input` on `PreToolUse` and special-case known-blocking tool names (`isInteractiveTool` in `status.go`). The transcript does supply the *content* once the status is known (next paragraph).

The question's content (tool name + raw `tool_input`, e.g. `AskUserQuestion`'s `{questions:[{question,header,options}]}` shape) is captured on `sessionState.PendingTool`/`PendingInput`, exposed via `Manager.PendingQuestion(sessionID)`, and threaded through `sessionView` → `state.ProjectState.PendingQuestionTool`/`PendingQuestionInput` into the shared state file — `mo web`'s chat view renders it from there (see `internal/web/static/chat.js`) since the web never touches `status.Manager`. The capture is in memory, so a TUI restart while a question is open (or a dropped hook) leaves only the agents signal's content-less question. For that case `Manager.RecoverPendingQuestion` reads it back with `status.ReadPendingQuestion` (`pending.go`). That function finds the newest interactive `tool_use` in the current turn with no `tool_result` after it; a real prompt or an `end_turn` ends the search. A hook-captured question is never replaced. If a future Claude Code stops writing the call early, it falls back to the content-less banner.

### Gotcha: `AskUserQuestion` goes through the permission flow

Captured live from Claude Code 2.1.288 (Oct 2026), an `AskUserQuestion` menu fires, in order: `PreToolUse` (tool `AskUserQuestion`) → **`PermissionRequest` naming the same tool, with the full `tool_input`** → `Notification` (`notification_type: permission_prompt`, message "Claude needs your permission", no tool name). A genuine permission prompt fires the identical sequence, except that `PermissionRequest`'s `tool_name` is the real tool (e.g. `Bash`) and it also carries `permission_suggestions`. Without special-casing, the `PermissionRequest` overwrote Question with Permission, so the TUI showed "perm" and `mo web` showed a permission lock instead of the question banner. `ProcessHookEvent` now maps a `PermissionRequest` for an interactive tool to Question (capturing its content too, so it works even when `PreToolUse` is missed), and ignores the tool-less `permission_prompt` notification while a Question is showing. The fixtures in `internal/status/permission_question_test.go` are trimmed copies of those captured payloads.

To capture raw hook messages again, launch a Claude session with `MO_HOOK_DEBUG_LOG=<file>` in its environment (e.g. `tmux new-window -e MO_HOOK_DEBUG_LOG=/tmp/hooks.log … claude`); `mo hooks send` appends every message it forwards to that file.

**Still open**: `mo web`'s prompt-injection endpoint only accepts input when status is `idle` or `question`, deliberately *not* `permission`. The `PermissionRequest` payload does carry `tool_name`, `tool_input` and `permission_suggestions`, so rendering and answering a genuine permission prompt from the browser is now feasible, but not built.

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
