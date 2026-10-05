# Answering permission prompts from the web

**Status:** planned, 2026-10-05. Nothing built yet. The status fix it builds on (a permission prompt that was open across a TUI restart now reads `permission` through `claude agents --json`) is committed as `f9868e6`.

## Goal

When a session waits on a permission prompt, the chat view shows **what Claude wants to do** (the Bash command, the edit as a diff, the plan, the URL…) and **the dialog's own choices** as buttons, and a click answers it in Claude's terminal. Today the web only shows a static "needs permission, answer in the terminal" banner.

## The approach: content from Claude, choices from the screen

There are two halves, read from different places:

- **Content (what is being asked):** the tool name and input. The `PermissionRequest` hook carries both (`tool_name`/`tool_input` under `hook_input`, already parsed into `HookEvent.ToolName`/`ToolInput`, then thrown away for anything that isn't a question). After a TUI restart, or when the hook was missed, the transcript has them too: Claude Code writes the assistant line with the `tool_use` before it opens the dialog (verified for `AskUserQuestion`, see `.claude/rules/session-detection.md`; to check for the others in step 1).
- **Choices (what you can answer):** read from a capture of Claude's pane, never assumed. The dialogs differ by tool and by situation. The 2.1.289 binary has more than 20 variants of the "yes" row alone: "Yes, and don't ask again for `<prefix>` commands in `<dir>`", "Yes, and always allow access to `<dir>`", "Yes, allow reading from `<dir>`", "Yes, and use auto mode", "Yes, manually approve edits", "Yes, and switch to `<mode>`", …. A hand-kept table of buttons per tool would go stale quietly and press the wrong key when it does. The screen shows exactly what pressing a digit will do.

The server only ever presses a key **after re-reading the screen and checking it still shows the dialog the browser answered** (same question line, same options), the way `answer.go` drives `AskUserQuestion`. If the terminal user answered first, or the dialog changed, the answer is refused with a 409 and nothing is typed.

## What the dialogs look like

Plan approval (ExitPlanMode), captured from a live session on 2.1.289:

```
  (the plan, in a scroll box ending in ↓)
  ─────────────────────────────────────────────
   Claude has written up a plan and is ready to execute. Would you like to proceed?

   ❯ 1. Yes, and use auto mode
     2. Yes, manually approve edits
     3. Tell Claude what to change
        shift+tab to approve with this feedback

   ctrl+g to edit in VS Code · ~/.claude/plans/<name>.md
```

Tool prompts ("Do you want to proceed?" / "Do you want to make this edit to `<file>`?" / "Do you want to allow …") follow the same pattern from the binary's strings: a box with the tool's own preview (command, diff, URL), a question line, numbered rows with a `❯` cursor, then a footer ("Esc to cancel", hints). Rows that take text ("No, and tell Claude what to do differently", "Tell Claude what to change") open or use a text field. **Their exact keys aren't known yet. Step 1 probes them.**

## Server

### status: keep the permission's content

- `ProcessHookEvent`: `EventPermissionRequest` for a non-interactive tool keeps `evt.ToolName`/`ToolInput` as the pending tool, like a question does. `sessionState.PendingTool`/`PendingInput` become "set iff Status is Question or Permission". `EventNotificationPerm` (no tool name) keeps whatever is pending.
- `Manager.PendingQuestion` becomes `Pending(sessionID) (tool, input, ok)` for both states. `RecoverPendingQuestion` becomes `RecoverPending`.
- `ReadPendingQuestion` becomes `ReadPendingTool(path, interactiveOnly bool)`: the same backwards walk, returning the newest unanswered `tool_use` in the current turn, of an interactive tool or of any tool. **Parallel calls:** with several unanswered `tool_use`s (parallel Bash calls), the one on screen isn't known from the transcript alone. It returns them all (newest first), and the web picks the one the dialog's preview names (the command or file path appears on screen). If none matches clearly, the banner shows the choices without content.
- State file: `pending_question_tool`/`pending_question_input` are renamed to `pending_tool`/`pending_input` (Go: `PendingTool`/`PendingInput` on `state.ProjectState` and `sessionView`) and filled for both statuses. The TUI and `mo web` are restarted together, so there's no compatibility shim. The sidebar doesn't read them (check while building).

### web: read the dialog, answer it

- `permission.go`:
  - `parsePermissionDialog(screen) permissionView`: finds the bottom-most numbered-row block that has a `❯` cursor, with its question line above (the last non-empty line above the first row, back to the `───` rule) and the footer below. It returns `question`, `rows []{n, label, text bool}` (`text` = the row takes text, from what step 1 learns) and `cursor`, and leaves out `AskUserQuestion`'s dialog (tab line), the prompt box and todo lists. Continuation lines under a row (the "shift+tab to approve with this feedback" hint, wrapped labels) attach to the row above.
  - `sig`: a hash of the question plus the rows' numbers and labels. It names "this dialog" between the GET and the POST.
- `GET /api/sessions/{windowID}/permission` → `{tool, input, dialog: {question, rows, sig} | null}`. 404 for a window without a session, 409 unless the state file says `permission`. The pane target comes from the state file (`claudeTarget`), as with `/spinner` and `/mode`. With several candidate tools (parallel calls), `tool`/`input` is the candidate the screen's preview names.
- `POST /api/sessions/{windowID}/permission` `{sig, row, text?}`: under `modeMu`, captures again, refuses (409) unless the status is `permission` and the dialog's `sig` matches, then presses the row's digit (or, for a text row, the keys step 1 finds), reads the screen back and checks the dialog moved on. `text` is `stripControl`ed, newlines become spaces, and it's capped like `maxAnswerText`. A row number must be one the dialog shows. Esc ("Esc to cancel") is offered as its own action `{sig, cancel: true}` if step 1 shows it differs from the "No" row.
- `ClaudePane` already has `Capture` and `SendKeys`, so there are no new interfaces.

## Browser

- The permission banner (`#permission-banner`, today a static line) becomes a rich banner like the question banner, rebuilt only when `(session, tool, input, sig)` changes:
  - **Header:** the screen's question line ("Do you want to proceed?").
  - **Content by tool:** Bash: the command (mono) and its `description`. Edit/MultiEdit/Write: a diff, reusing the transcript's Edit card renderer, plus the path with "Open". ExitPlanMode: the plan through `markdown.js`, in a scroll box with "expand". WebFetch/WebSearch: the URL/query. Anything else (MCP, …): the tool name and pretty-printed input, truncated.
  - **Choices:** one button per row, in the dialog's order and wording, with the row number. Rows that take text get a text field that sends with that row. "Don't ask again" / "always allow" rows are marked, since they change settings. Shortcuts: digits while the banner has focus.
  - **No dialog parsed:** the content plus "Answer in the terminal". **No content:** the choices alone.
- It polls `/permission` while the state poll says `permission` (once a second, like the spinner), so a dialog that redraws (a different command after a parallel one) updates. On a 409 it shows the error and refetches.
- The composer stays locked in `permission` (unchanged): typing into the dialog is only through the banner.

## Tests

- `status`: the `PermissionRequest` content kept and cleared (Stop, PostToolUse, the agents signal), `Pending` for both states, `ReadPendingTool` (one unanswered call, parallel calls, answered, ended turn, interactive-only).
- `permission_test.go`: `parsePermissionDialog` on real captures saved under `internal/web/testdata/permission/` (plan approval, Bash, Edit, Write, WebFetch, an outside-directory read, plus an `AskUserQuestion` screen and a todo list that must not parse). Handler tests with the fake-pane pattern from `answer_test.go`: a matching sig presses the digit, a stale sig or another status is a 409 with no keys, unknown rows and bad text are a 400 with no keys, and the text row types its text.
- `jstests`: if the banner's content selection grows logic (picking the renderer, truncation), put it in a small model file and test it in Node, like `question-model.js`.

## Build order

1. **Probe the dialogs** (needs your go-ahead to run a throwaway `claude` in an isolated tmux server, with `--setting-sources project` so unky-mo's hooks don't fire). For Bash, Edit, Write, WebFetch, a read outside the cwd, and plan approval, capture the screen, check that the `tool_use` is in the transcript while the dialog is open, and find the keys: digit per row, the text rows, Esc. Save the captures as test fixtures. Write the findings into this doc.
2. **status + state file:** keep and recover the permission's content, rename the fields. Commit.
3. **web endpoints:** `parsePermissionDialog`, GET/POST `/permission`, tests. Commit.
4. **Browser banner.** Commit.
5. **Docs** (`CLAUDE.md`, `session-detection.md`, `testing.md`) and a check against a real prompt from the web. Commit.

Per the usual rule, each step gets a "Step N in detail" section here before it's built.

## Later

- The transcript's tool card for the pending call could show "waiting for permission" with the same buttons, instead of only the banner.
- Answering from the dashboard's session list (a compact Allow / Deny for Bash) once the banner has proven itself.
