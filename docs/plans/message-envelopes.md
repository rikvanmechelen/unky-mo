# Rendering Claude Code's tagged messages (no raw XML in the web)

**Status:** in progress 2026-11-14.

## Problem

Claude Code wraps a lot of what it puts in a session's queue and transcript in XML-ish envelopes. The chat view understands a few (`<command-name>`, `<local-command-stdout>`, `<task-notification>`, `<teammate-message>`), each with its own regex in `chat.js`. Anything else either shows as raw markup or isn't shown at all. Seen in the kids_com sessions: the "Queued (2)" strip shows `<cross-session-message from="uds:/run/user/1…` twice, with the actual message cut off.

What a scan of the local transcripts (`~/.claude/projects/*/*.jsonl`, 2026-11-14) turned up:

| Envelope | Where it shows up | Today |
|---|---|---|
| `<cross-session-message from= from-name= from-mode= hop-chain=>` (another session's `SendMessage`) | queue `enqueue`/`remove` content (139×). A `user` line with `isMeta: true` and `origin: {kind: "peer", name, fromMode, body}` whose text is `Another Claude session sent a message:\n<cross-session-message …>` (97×). An `attachment` `queued_command` line when it's read mid-turn | raw XML in the Queued strip. **Never shown in the transcript**: the `user` line is `isMeta` and so skipped, and `attachment` lines aren't forwarded |
| `<agent-message from=…>` (a background subagent's hand-back) | queue content, and the line it becomes | hidden from the strip; transcript to be checked |
| `<task-notification>` | queue content, `user` line with `origin.kind: "task-notification"` | handled (meta row, hidden from the strip) |
| `<pasted_content id=…>` (a paste in the user's own prompt, `origin.kind: "human"`) | inside the user's prompt text | raw tags in the user's bubble |
| `<command-name>`, `<command-message>`, `<command-args>`, `<local-command-stdout/stderr>`, `<local-command-caveat>`, `<system-reminder>` | `user` lines | handled (meta row or skipped) |
| `<teammate-message …>` | `user` lines | handled (teammate cards) |

The user's own text also contains plenty of tags that are content, not envelopes: pasted HTML (`<div>`, `<script>`), log levels (`<inf>`, `<dbg>`, `<err>`), templates. **So only known envelope tags are ever parsed, and only where Claude Code puts them.** Everything else stays text, shown as typed.

A second gap: a message read mid-turn (`remove` with `absorbed_mid_turn`) is recorded only as an `attachment` line of type `queued_command`, which `transcript.go` forwards as a link stub. That means queued messages, the user's own included, disappear from the strip and never appear in the transcript. Step 1 confirms this.

## Design

### One parser: `static/envelopes.js` (pure, Node-tested)

- `parseEnvelopes(text)` → segments: `{kind: "text", text}`, `{kind: "peer", name, from, mode, body}`, `{kind: "agent", from, body}`, `{kind: "task", summary, status, …}`, `{kind: "teammate", id, color, summary, body}`, `{kind: "pasted", id, body}`, `{kind: "command", name, args}`, `{kind: "output", text, stream}`, `{kind: "hidden"}` for `system-reminder` and `local-command-caveat`.
- Only allowlisted tag names count. Attributes are parsed `name="value"` (unescaping `&quot;`, `&amp;`, `&lt;`, `&gt;`). A tag that doesn't close, nests oddly or isn't on the list stays text. The `Another Claude session sent a message:` lead-in before a peer envelope is dropped.
- `envelopeSummary(segments, max)`: one plain line for chips and tooltips, e.g. `app: GO. The new build (4b5d66f) is installed…`, `Pasted 12 lines`, `Subagent report`.
- `chat.js`'s `tagText`, `parseTeammateMessages` and most of `describeUserString` are rebuilt on it. `describeUserString` keeps its contract (`user` / `meta` / `skip` / `teammates`) plus new kinds `peer` and `agent`, and `user` carries segments so a prompt with a paste in it can render the paste. `overview.js` takes the prompt text through the same function, so the intent trace benefits too.
- **Origin wins over text.** When a line has a structured `origin` (`peer` with `name`/`body`/`fromMode`, `task-notification`), the card is built from it and the text is only a fallback. For `origin.kind: "human"` only `pasted_content` (and the command tags Claude Code itself writes) are parsed, never a peer or agent envelope a user pasted in.
- **Fallback for system lines:** a non-human line that starts with an unknown `<tag …>…</tag>` envelope renders as a neutral card: the tag name humanised as the title, the attributes as small key/values, the body as text. Never as raw markup. On human lines an unknown tag is just text.

### Transcript

- **Peer messages:** a card distinct from the user's bubble, an "incoming" style, for example a left rule in `--ink-3` with "From **app** · prompting" over the body. The body is rendered with `markdown.js` (DOM, never innerHTML) and folded past ~12 lines. Shown for `origin.kind: "peer"` even though the line is `isMeta`.
- **Subagent hand-backs** (`agent-message`): a folded "Subagent report" card. It links to the subagent dialog when the `from` id is in `/subagents`.
- **Pasted content** in a user prompt: the typed text as usual, and each paste as a folded "Pasted text · N lines" block (monospace, expandable), like Claude Code's `[Pasted text #1 +N lines]`.
- **Mid-turn messages:** `transcript.go` forwards `attachment` lines whose `attachment.type` is `queued_command` (verbatim, like the rest; the frontend still owns the schema). The chat renders them where they were read: the user's own as a user bubble tagged "read mid-turn", a peer one as a peer card. Other attachment types keep their link stubs.

### Queued strip

- Each item is `envelopeSummary`: peer items as `app: …` with a small "from" marker, a paste as `Pasted N lines`. The full rendered body appears on click in a small popover (same renderer as the transcript), not only in a tooltip.
- Which entries count as "system" (hidden) moves into `envelopes.js`: `task-notification` and `agent-message` as today. Peer messages are **shown**, since they're messages Claude is about to read, labelled as not yours.

### Not in scope

- Assistant text: XML there is Claude's own output (often code) and stays as `markdown.js` renders it.
- Tool results: stay as they are.

## Steps

Each step gets a detailed section here before it's built, and a commit of its own.

1. **Probe and fixtures.** Collect real, trimmed lines into `internal/web/testdata/envelopes/`: a peer `user` line, `enqueue`/`remove` of a peer message, `queued_command` attachments (user, peer, agent), an `agent-message` hand-back as it lands in the transcript, a prompt with `pasted_content`. Confirm whether an absorbed message also gets a `user` line, so step 4 doesn't double it. No code changes.
2. **`envelopes.js` + `jstests/envelopes.test.js`.** Every fixture, plus the refusals: HTML and log tags in human text, unclosed tags, nested look-alikes, an envelope inside a human prompt. Rebuild `describeUserString` and friends on it with no visible change (the existing tests must still pass).
3. **Transcript cards:** peer, subagent report, pasted blocks and the unknown-envelope fallback; CSS on the tokens (both themes).
4. **Mid-turn messages:** forward `queued_command` attachments in `transcript.go` (Go test: forwarded verbatim, other attachment types still stubs) and render them.
5. **Queued strip:** summaries, the peer marker, the click-to-read popover; `queue-model.js` tests for what's hidden.
6. **Docs:** CLAUDE.md (chat view and testing sections), status line here.

## Step 1 in detail: probe (done)

Scanned every local transcript on 2026-11-14 and checked the plan's assumptions. Trimmed examples are in `internal/web/testdata/envelopes/`, one line each:

- **Mid-turn messages are invisible today.** All 57 `queued_command` attachment lines found have no other line with their `source_uuid`, and no `user` line repeats their prompt (the 5 that seemed to were retyped). So a message read mid-turn exists only as `{type: "attachment", attachment: {type: "queued_command", prompt, commandMode, origin?, humanTurn?}}`. That applies to the user's own (`origin.kind: "human"`, `commandMode: "prompt"`, `humanTurn: true`), a peer's (the `<cross-session-message>`), a subagent's hand-back (`<agent-message>`, no `origin`) and a background task's (`commandMode: "task-notification"`). `prompt` was always a string. Fixtures: `queued-user`, `queued-peer`, `queued-agent`, `queued-task`.
- **A peer message read as a new turn** is a `user` line with `isMeta: true` and `origin: {kind: "peer", from, name, fromMode, body, msg_id, verifiedPeerPid}`. Its text is `Another Claude session sent a message:\n<cross-session-message from= hop-chain= from-name= from-mode=>…</cross-session-message>`. `origin.body` is the message without the envelope (`peer-user`). Queue lines carry the bare envelope (`peer-enqueue`, `peer-remove`).
- **Subagent hand-backs** arrive only as `queued_command` attachments in these transcripts. Their body is a fixed frame (`[Subagent hand-back] … The report follows:`) followed by the report with every line indented by two spaces.
- **`pasted_content`:** the open tag is `<pasted_content>` or `<pasted_content id="e26c">`, and the close tag repeats the id: `</pasted_content id="e26c">`. The paste can contain anything, envelopes included (the user pasted a `<cross-session-message>`), so a paste ends only at the close tag with its own id. It sits in a `user` line with `origin.kind: "human"`, as a text block next to the typed text (`pasted-user`).

## Step 2 in detail: one parser

- New `static/envelopes.js`, loaded before `chat.js` on both pages that render transcripts, and exported for Node. The helpers in `chat.js` that read Claude Code's markup (`isMetaContent`, `tagText`, `stripAnsi`, `parseTeammateMessages`, `describeUserString`) move there unchanged in behavior, so they're Node-testable for the first time.
- `parseEnvelopes(text, {only})`: walks the text for open tags on the allowlist (`cross-session-message` → `peer`, `agent-message` → `agent`, `task-notification` → `task`, `teammate-message` → `teammate`, `pasted_content` → `pasted`, `system-reminder` / `local-command-caveat` → `hidden`). It returns `text` segments and envelope segments in order. An envelope closes at the first matching close tag; a paste closes only at its own id's close tag (or a bare `</pasted_content>` when it had no id). Without a close tag, the open tag stays text. `only` limits the tags: a human prompt only ever parses `pasted`.
  - Attributes: `name="value"`, entity-unescaped.
  - Peer: `{name, from, mode, body}`. The `Another Claude session sent a message:` lead-in right before one is dropped.
  - Agent: `{from, body}`, the hand-back frame cut off and the report's two-space indent removed (if the frame isn't there, the body is kept as is).
  - Task: `{summary, status, taskId, outputFile}`.
- `envelopeSummary(text, max)`: one plain line (peer `app: …`, a paste `[Pasted N lines]`, agent `Subagent report: …`, task its summary), whitespace collapsed and cut with `…`.
- `describeUserString(msg, text)` keeps its kinds and adds:
  - `peer` `{name, mode, body}`, from `origin` when `origin.kind === "peer"` (even though the line is `isMeta`), else from the text.
  - `agent` `{from, body}` and `envelope` `{tag, attrs, body}` (an unknown tag), only when the line isn't human (`origin.kind !== "human"`) and the whole text is one envelope.
  - `user` gains `segments` (text + pastes); `text` stays the full prompt, which the Overview's intent trace uses.
- `describeQueuedCommand(attachment)`: an attachment's `queued_command`, described the same way with `queued: true`.
- Tests (`jstests/envelopes.test.js`) read the step 1 fixtures. They also check the refusals: HTML and log tags in a human prompt stay text, an envelope pasted by a human isn't parsed, unclosed tags stay text, and a paste holding an envelope is one paste. There are no visible changes in this step except that a peer message's line is no longer skipped (it renders as a user bubble until step 3).

## Step 3 in detail: transcript cards

All in `createTranscriptView` (`chat.js`), so the subagent dialog gets the same. `renderMessage` passes every description through one `appendDescribed(d, images, uuid)`:
- `user`: the bubble as before, but its text is built from `segments`: typed text as text, each paste a `<details class="msg-paste">` whose summary reads "Pasted text · N lines" and whose body is the paste in monospace (pre-wrap, height-capped with its own scroll). `queued: true` adds a small "read mid-turn" tag (used from step 4).
- `peer`: `.msg-peer`, a block with a `--blue` left rule over `--surface`: "From **app** · prompting" (plus "read mid-turn" when queued), then the body through `renderMarkdown`. A body over 12 lines is folded (capped height, fade) with a "Show all" / "Show less" button.
- `agent`: a folding tool-card-style row ("Subagent report", its first line as the detail) like the teammate rows; the body renders as markdown on first open. The teammate rows move onto the same helper (`appendFoldRow`).
- `envelope` (unknown): the same row, named from the tag ("relay-note" → "Relay note"), with its attributes as `key: value` lines and the body as plain pre-wrap text.
- `meta`, `teammates`, `skip`: as before.

CSS uses the tokens only (both themes). No Node test: this is DOM wiring over step 2's tested descriptions.

## Step 4 in detail: mid-turn messages

- `transcript.go` (`readNew`): an `attachment` line whose `attachment.type` is `queued_command` is forwarded verbatim instead of as a link stub. The attachment's type is read from a `json.RawMessage`, so an attachment field of any other shape still becomes a stub instead of dropping the line. Every other attachment stays a stub. The line keeps its `uuid`/`parentUuid`, so it sits in the conversation tree exactly where the stub did, and branch switching is unchanged.
- `chat.js` `renderMessage`: `type: "attachment"` → `describeQueuedCommand(msg.attachment)` → `appendDescribed`. The user's own message becomes a user bubble tagged "read mid-turn", a peer's a peer card with the same tag, a subagent's a "Subagent report" row, and a task notification a meta row.
- Not part of the Overview's intent trace: it reads only `user` lines, and a mid-turn message doesn't start a turn.
- Go test: `TestTranscriptCursorFiltersForwardedTypes` gains a `queued_command` attachment (forwarded) next to the existing `hook` attachment (still a stub) and one whose `attachment` is a string (a stub, not dropped).
