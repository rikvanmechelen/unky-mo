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

## Step 1 in detail: probing the dialogs

- A temp git repo in the session scratchpad with one file, a throwaway `claude --setting-sources project --strict-mcp-config` in it on its own tmux server (`tmux -L moprobe`, 140×45), so no unky-mo hook fires and the TUI never sees it. Default permission mode.
- For each dialog: prompt Claude to trigger it, capture the pane (`capture-pane -p`, and `-e` once to see how the cursor row is styled), check the transcript's tail for the unanswered `tool_use`, then answer with a different key each time to learn the keys. The prompts:
  - Bash: `run: echo probe > out.txt` (a write, so not auto-allowed). Answer with `1`.
  - Bash again, answered with `3` ("No, and tell Claude…") to see whether it opens a text field or rejects straight away.
  - Edit on `notes.txt`. Answer with `2` ("don't ask again" for edits), and check what that changes.
  - Write of a new file (if `2` above switched to accept-edits, probe this first).
  - WebFetch of a URL. Answer with Esc.
  - A read outside the cwd (`/etc/hostname`).
  - Plan approval: shift+tab into plan mode, ask for a tiny plan. Answer with `3` plus text, and look for the "shift+tab to approve with this feedback" behavior.
- Save each capture as `internal/web/testdata/permission/<name>.txt` (plus a `.ansi` for one). Write the findings below as "Step 1 findings": layout rules, the key per row type, the text rows, Esc, and whether the transcript had the call.
- Kill the tmux server and delete the temp repo when done. Commit the fixtures and findings.

## Step 1 findings (Claude Code 2.1.289, probed 2026-10-05)

Captures are in `internal/web/testdata/permission/`: `bash`, `bash-amend-yes`, `bash-amend-no`, `edit`, `write`, `webfetch`, `read-outside`, `plan`, `plan-feedback` (`.txt`, plus `bash.ansi` with colours), and `trust-folder` (the startup trust dialog, which must **not** parse as a permission prompt: its rows have no numbers).

### Layout

Every permission dialog is one block at the bottom of the screen:

```
────────────────────────  full-width rule (U+2500) opens the dialog
 <Title>                   "Bash command", "Edit file", "Create file", "Fetch", "Read file", "Ready to code?"
 <subtitle lines>          optional: a tip, the file path, "Claude wants to fetch content from example.com", "Here is Claude's plan:"
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌  dashed rule (U+254C) around the preview: command, numbered diff, url + prompt, Read(path), the plan
 <preview>
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 <question>?               "Do you want to proceed?", "Do you want to make this edit to notes.txt?", …
 ❯ 1. <label>              rows; a long label wraps onto indented lines with no number
   2. <label>
      <hint>               plan's row 3 has "shift+tab to approve with this feedback" under it
 <footer>                  "Esc to cancel · Tab to amend", "Esc to cancel", "ctrl+g to edit in VS Code · <plan file>", or none (WebFetch)
```

The plan dialog has a second full-width rule between the plan and the question; the question block is what follows the **last** rule. Blank lines separate the parts (`capture-pane` keeps them).

### Keys

- **A digit picks its row at once**, for any row that isn't a text row. "No" (and the WebFetch "No, and tell Claude what to do differently (esc)") rejects the call and **ends the turn**: the transcript gets "Interrupted · What should Claude do instead?" and the session goes back to the prompt, so the web composer works again for the "tell Claude" part.
- **Esc** = No.
- **Tab to amend** (Bash, Edit, Write, Read; shown in the footer): on the "Yes" row, Tab turns it into "Yes, and tell Claude what to do next". Text typed then shows in place ("1. Yes, then say done"), and Enter accepts and sends the text as the next prompt. On the "No" row it's "No, and tell Claude what to do differently". Enter rejects, and Claude carries on with the text in the same turn (it retried with printf). Getting there takes moving the cursor (↓ per row from row 1), Tab, the text, then Enter.
- **Plan approval:** rows 1 and 2 are picked by their digit. Row 3 ("Tell Claude what to change") is a text row: its digit only moves the cursor there (like AskUserQuestion's "Type something"). Typed text replaces the label, and Enter sends it as feedback (Claude revises the plan and asks again). **shift+tab** with text approves the plan *with* the text as the next prompt, in row 1's mode (auto mode).
- Picking "Yes, and switch to accept edits/auto mode" changes the session's mode, as in the terminal. The web's mode chip follows on its next poll.

### Transcript

- Bash, Edit, Write, Read and WebFetch: the assistant line with the `tool_use` is on disk while the dialog is open (last non-metadata line), as with AskUserQuestion. Recovery after a restart works for these.
- **ExitPlanMode isn't reliably there.** In the probe its `tool_use` (`{plan, planFilePath}`) was written only once the dialog was answered: while it was open (checked for about a minute), the transcript's last call was the `ToolSearch` that loaded it. But in a real session (moma-chatbot, step 2's check) the open ExitPlanMode call *was* the transcript's last line, so `ReadPendingTool` recovered it. What decides this is unknown (timing, or the probe loading the tool through ToolSearch in the same turn). Treat it as "maybe". After a restart, the plan's content comes from the plan file named in the dialog's footer (`~/.claude/plans/<name>.md`). The hook's `tool_input` carries the same `{plan, planFilePath}` while TUI and hooks are up.

### Changes to the plan

- `ReadPendingTool` is not always enough for plan approval. When there's no pending content and the screen's footer names a plan file, `/permission` reads it, but only a `*.md` directly in `~/.claude/plans/`, opened with `O_NOFOLLOW` and capped like editor files, and returns it as `{tool: "ExitPlanMode", input: {plan}}`.
- The POST takes `{sig, row, text?, approveWithText?}`:
  - A plain row without text: its digit.
  - Text on a Yes/No row of a dialog with "Tab to amend": ↓ to the row (checking the cursor on screen after each step), Tab, check the label changed to "… tell Claude …", type the text, check it's shown, Enter.
  - Text on the plan's text row: its digit (moves there), type, check, then Enter (feedback) or BTab (`approveWithText`, approve in auto mode).
  - A row's text is allowed only where the dialog offers it, which `rows[].text` tells the browser: the footer has "Tab to amend" (rows whose label starts with "Yes" or "No"), or the row has the shift+tab hint.
- No separate Esc action: it does what the No row does.

## Step 2 in detail: status and state file

### `status.Manager` (`status.go`)

- `sessionState.PendingTool`/`PendingInput`: set iff the status is Question **or Permission** (when known).
- `EventPermissionRequest` for a non-interactive tool: `StatusPermission` with `evt.ToolName`/`evt.ToolInput` as the pending tool. For ExitPlanMode that's `{plan, planFilePath}`.
- `EventNotificationPerm` (no tool name) while the session is already Question or Permission keeps the pending tool. Today the common tail clears it, since `pendingTool` is empty. The early return for a question stays. For a permission, the event carries the current content over, so `LastHookAt` still moves.
- `PendingQuestion` → `Pending(sessionID) (tool, input, ok)`: ok for Question and Permission.
- Clearing is unchanged: any status change away from those two empties the content (the common tail and `ProcessAgentStatus`). A permission ends with the next PreToolUse, UserPromptSubmit or Stop, as today. There's no PostToolUse hook.

### Transcript recovery (`pending.go`)

- `ReadPendingQuestion(path)` → `ReadPendingTool(path, interactiveOnly bool)`. The same backwards walk, returning the newest unanswered `tool_use` of the current turn, of an interactive tool or (for a permission) of any tool. For ExitPlanMode it finds the call only when Claude Code has written it (see step 1); otherwise step 3 reads the plan file named on screen. *(Corrected after building: moma-chatbot's open plan was recovered from its transcript.)*
- Parallel calls: the newest unanswered call wins. Step 3 only shows the content when the dialog's preview agrees with it (the command or path appears on screen), so a wrong guess shows no content rather than the wrong content. That replaces "return all candidates" from the first draft. One candidate keeps the state file simple, and the screen check is needed anyway.
- `RecoverPendingQuestion` → `RecoverPending(sessionID, read func(SessionStatus) (…))`: called for Question and Permission with no content. `read` gets the status so the caller passes `interactiveOnly` = (status == Question).

### State file and its readers

- `state.ProjectState`: `PendingQuestionTool`/`PendingQuestionInput` (`pending_question_tool`/`_input`) → `PendingTool`/`PendingInput` (`pending_tool`/`pending_input`), the same in `tui`'s `sessionView`. `app.go` fills them for both statuses.
- `answer.go` reads the new fields (still only for `question`). `chat.js` reads `pending_tool`/`pending_input` for the question banner. The permission banner stays as it is until step 4.

### Tests

- `permission_question_test.go`: the captured Bash `PermissionRequest` keeps `{command, description}`, a following permission notification keeps it, and a PreToolUse or Stop clears it. AskUserQuestion is unchanged.
- `status_test.go`: renamed calls. An agents-sourced permission has no content.
- `pending_test.go`: `ReadPendingTool` with `interactiveOnly=false` finds an open Bash call, ignores answered ones, picks the newest of two parallel open calls, finds nothing for ExitPlanMode's transcript (only an answered ToolSearch), and `interactiveOnly=true` still skips Bash. `RecoverPending` reads for a permission too.
- `answer_test.go`: fixture field names.
