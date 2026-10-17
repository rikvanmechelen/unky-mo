# Sending messages while Claude is working

**Status:** step 1 (probe) done 2026-10-17; steps 2–6 not built.

## Goal

Today the composer locks ("Claude is working…") while a session is `active`, and `POST /prompt` answers 409 unless the status is `idle` or `question`. In Claude Code's terminal you can keep typing: the message is **queued**, and a shortcut makes Claude take it **now**. The web should have both.

## What Claude Code does (probed on 2.1.302, captures in `internal/web/testdata/queue/`)

- **Queue:** typing + Enter mid-turn queues the text. The screen shows it under the transcript with `ctrl+x ctrl+s to send now`, and the empty input box's placeholder becomes `Press up to edit queued messages`. Multi-line bracketed pastes queue the same way.
- **Delivery:** the message is read at the next tool boundary, inside the running turn (`remove` with `reason: "absorbed_mid_turn"`, then an `attachment` line `queued_command`, rendered to Claude as "The user sent a new message while you were working"). It is not a new turn.
- **Send now = `ctrl+x ctrl+s`:** with a tool running, the tool is moved to the background (not killed) and the message is delivered at once. While Claude is streaming text with no tool, it stops and starts a new turn with the message (`dequeue`). Non-destructive for work in flight, so this is the "send now".
- **Esc:** aborts the running tool (`[Request interrupted by user for tool use]`) and the queued text goes in as a new turn (`dequeue`). Destructive; not offered in v1.
- **Up:** pops *all* queued messages into the input box for editing (one `popAll` line each); the turn goes on. Esc Esc then clears the box. On an idle prompt with text, a single Esc only says "Esc again to clear".
- **Transcript ops:** `{"type":"queue-operation","operation":"enqueue"|"dequeue"|"remove"|"popAll","content":…,"reason"?,"commandUuid"?,"deliveryId"?}`. `dequeue` carries no content (FIFO: it's the oldest queued entry). Background-task `<task-notification>` content goes through the same queue and must be told apart.

## Design

In the composer while `active`:

1. **Queue** (Enter, button reads "Queue"): type the text into Claude's pane with the existing `SendLiteralText` / `SendPastedText` / `SendPrompt`. Only the status gate changes.
2. **Send now** (button, Ctrl/Cmd+Enter): do 1, then press `C-x C-s` in Claude's pane. No Esc, no status polling.
3. **Pending chips** above the composer for queued user messages, from the transcript (`enqueue` adds; `remove`, `dequeue` (oldest), `popAll` drop). Shows messages queued from the terminal too. No per-message Remove in v1: the only key is Up, which pops everything into the input box.

### Server

- `handlePrompt`: accept `active`. Request gains `sendNow` (bool); honoured only while `active`. `permission` stays rejected; `question` unchanged.
- Guard: while active, the pane's message box must be empty, since typing would mix with what the terminal user typed. New `ClaudePane` capture parser (`inputbox.go`, same border scan as `mode.go`): empty = nothing after `❯`, or the `Press up to edit queued messages` / `Try "…"` placeholder. Otherwise 409 "there's text in the terminal's message box". Not applied when idle (existing behavior).
- Send now: after the text is delivered, `ClaudePane.SendKeys(target, "C-x", "C-s")`. If the status turned `idle` meanwhile the keys are skipped (nothing queued to send).

### Browser

- `chat.js` `pollStatus`: `canSend` true for `active` too; labels "Queue" / "Send now"; placeholder "Message Claude (queued until it picks it up)". `permission` keeps its lock.
- Rename the first-run `queued` variable to `heldUntilStart`.
- `queue-model.js` (pure, Node-tested) folds transcript lines into the pending list; chip UI in `chat.js`.
- Review comments ("Send to Claude") use the same path, so no more "Claude is busy" while active.

### Safety

- Nothing is aborted: send now backgrounds a running tool, never kills it. Esc/interrupt is left out of v1.
- Keys are pressed only for a session the state file says is `active`, in Claude's own pane (`<session>:<window>.0`).
- Text rules unchanged: `stripControl`, size caps, attachments by id.

## Steps

Commit per step; `make test` after each.

1. **Probe** — done (above).
2. **Server: queue mode.** Lift the gate for `active`, add the input-box parser + guard, with fixtures from `testdata/queue/`. Tests: active accepted and sends; permission still 409; non-empty terminal box 409 with no keys; placeholder counts as empty.
3. **Server: send now.** `sendNow`, `C-x C-s` through `ClaudePane.SendKeys`. Tests with a fake pane: keys pressed after the text, skipped when idle, ignored when not `active`.
4. **Browser: composer.** Unlock while active, Queue / Send now, shortcut, labels; extract the label/enabled logic as a pure helper with a Node test.
5. **Browser: pending chips** (`queue-model.js`, rebuilt from transcript replay on connect).
6. **Docs + end-to-end check:** update CLAUDE.md (composer "only while idle" statements), then try it on a real session: queue during a long Bash, send now during one, queue from the terminal and see the chip.

## Open questions

- Offer Esc ("Interrupt and send") later as a separate, confirmed action?
- Trust dialog on 2.1.302 shows unnumbered rows (`❯ No, exit` / `Yes, I trust this folder`), which `parseStartupDialog` doesn't parse (documented as probed on 2.1.236). Separate from this feature; worth a look.
