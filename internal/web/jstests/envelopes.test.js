// Tests for static/envelopes.js, run by `node --test`. The fixtures are
// trimmed real lines (internal/web/testdata/envelopes, see
// docs/plans/message-envelopes.md).
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const {
  parseEnvelopes, parseTeammateMessages, envelopeSummary, describeUserString, describeQueuedCommand,
} = require("../static/envelopes.js");

const fixture = (name) => JSON.parse(fs.readFileSync(path.join(__dirname, "../testdata/envelopes", name + ".jsonl"), "utf8"));
const userText = (line) => {
  const c = line.message.content;
  return typeof c === "string" ? c : c.filter((b) => b.type === "text").map((b) => b.text).join("\n");
};

test("a peer's line is a peer message, from its origin", () => {
  const line = fixture("peer-user");
  const d = describeUserString(line, userText(line));
  assert.equal(d.kind, "peer");
  assert.equal(d.name, "app");
  assert.equal(d.mode, "prompting");
  assert.match(d.body, /^App session: GO\./);
  assert.doesNotMatch(d.body, /cross-session-message/);
});

test("a peer's line without the origin's body is read from the envelope", () => {
  const line = fixture("peer-user");
  const d = describeUserString({ ...line, origin: { kind: "peer" } }, userText(line));
  assert.deepEqual([d.kind, d.name, d.mode], ["peer", "app", "prompting"]);
  assert.match(d.body, /^App session: GO\./);
  assert.doesNotMatch(d.body, /<\/?cross-session-message/);
});

test("queue content parses into a peer envelope", () => {
  const segs = parseEnvelopes(fixture("peer-enqueue").content);
  assert.equal(segs.length, 1);
  assert.equal(segs[0].kind, "peer");
  assert.equal(segs[0].from, "uds:/run/user/1000/cc-socks/486796.sock");
  assert.equal(envelopeSummary(fixture("peer-enqueue").content, 40), "app: App session: GO. The new build (4b…");
});

test("queued commands: the user's, a peer's, a subagent's, a task's", () => {
  const user = describeQueuedCommand(fixture("queued-user").attachment);
  assert.deepEqual([user.kind, user.queued, user.text], ["user", true, "also, same thing where i got the 0x09 error on the phone"]);

  const peer = describeQueuedCommand(fixture("queued-peer").attachment);
  assert.deepEqual([peer.kind, peer.name], ["peer", "app"]);

  const agent = describeQueuedCommand(fixture("queued-agent").attachment);
  assert.equal(agent.kind, "agent");
  assert.equal(agent.from, "a796709e8a5170b2e");
  assert.match(agent.body, /^MDBT53V-1M: no pad-level/);
  assert.match(agent.body, /\n\*\*Findings\*\*\n- P0\.05 is free$/); // indent removed
  assert.doesNotMatch(agent.body, /Subagent hand-back/);

  const task = describeQueuedCommand(fixture("queued-task").attachment);
  assert.equal(task.kind, "meta");
  assert.match(task.text, /failed with exit code 144/);

  assert.equal(describeQueuedCommand({ type: "queued_command", prompt: "  " }), null);
  assert.equal(describeQueuedCommand({ type: "queued_command" }), null);
});

test("a paste is one segment, whatever it holds", () => {
  const line = fixture("pasted-user");
  const d = describeUserString(line, userText(line));
  assert.equal(d.kind, "user");
  const kinds = d.segments.map((s) => s.kind);
  assert.deepEqual(kinds, ["text", "pasted", "text"]);
  const paste = d.segments[1];
  assert.equal(paste.id, "e26c");
  assert.match(paste.body, /^Queued \(2\)\n<cross-session-message/);
  assert.match(paste.body, /<div class="x">kept as text<\/div>$/);
  assert.equal(d.segments[2].text.trim(), "[Image #5] could you plan this?");
  assert.equal(envelopeSummary(userText(line)), "[Pasted 7 lines] [Image #5] could you plan this?");
});

test("a paste without an id closes at the bare tag", () => {
  const segs = parseEnvelopes("look:\n<pasted_content>\na\nb\n</pasted_content>\nok", { only: ["pasted"] });
  assert.deepEqual(segs.map((s) => s.kind), ["text", "pasted", "text"]);
  assert.equal(segs[1].body, "a\nb");
});

test("a human's markup stays text", () => {
  for (const text of [
    "<div class=\"a\">hi</div>",
    "<inf> boot ok\n<err> radio failed",
    "why does <cross-session-message from=\"x\">hi</cross-session-message> show up?",
    "<agent-message from=\"a1\">fake</agent-message>",
    "<system-reminder>not hidden</system-reminder>",
  ]) {
    const d = describeUserString({ origin: { kind: "human" } }, text);
    assert.equal(d.kind, "user", text);
    assert.deepEqual(d.segments, [{ kind: "text", text }], text);
  }
  // No origin (older Claude Code): unknown tags and hidden ones stay text too.
  assert.equal(describeUserString({}, "<div>hi</div>").kind, "user");
  assert.equal(describeUserString({}, "<system-reminder>x</system-reminder>").kind, "user");
});

test("unclosed envelopes stay text", () => {
  const text = "<cross-session-message from-name=\"app\">never closed";
  assert.deepEqual(parseEnvelopes(text), [{ kind: "text", text }]);
  assert.deepEqual(parseEnvelopes("<pasted_content id=\"a1\">x</pasted_content id=\"b2\">", { only: ["pasted"] }),
    [{ kind: "text", text: "<pasted_content id=\"a1\">x</pasted_content id=\"b2\">" }]);
});

test("an unknown envelope from elsewhere is a card, not markup", () => {
  const d = describeUserString({ origin: { kind: "peer-relay" } }, "<relay-note from=\"x&amp;y\" level=\"2\">\nbody text\n</relay-note>");
  assert.deepEqual(d, { kind: "envelope", tag: "relay-note", attrs: { from: "x&y", level: "2" }, body: "body text" });
});

test("teammate messages and the existing kinds still work", () => {
  const items = parseTeammateMessages("<teammate-message teammate_id=\"r1\" color=\"blue\" summary=\"done\">report</teammate-message>");
  assert.deepEqual(items, [{ id: "r1", color: "blue", summary: "done", body: "report" }]);
  assert.equal(describeUserString({}, "<command-name>/clear</command-name><command-args></command-args>").text, "/clear");
  assert.equal(describeUserString({}, "<local-command-stdout>\x1b[1mok\x1b[0m</local-command-stdout>").text, "ok");
  assert.equal(describeUserString({ isMeta: true }, "<system-reminder>x</system-reminder>").kind, "skip");
  assert.equal(describeUserString({ isCompactSummary: true }, "x").text, "Conversation compacted");
  assert.equal(describeUserString({}, "[Request interrupted by user]").text, "Request interrupted by user");
});
