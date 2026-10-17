// Tests for static/queue-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { composerState } = require("../static/queue-model.js");

const base = { hasSession: true, name: "api", waitingToStart: false, held: false };

test("an idle session sends", () => {
  const s = composerState({ ...base, status: "idle" });
  assert.deepEqual([s.canSend, s.canSendNow, s.label], [true, false, "Send"]);
  assert.equal(s.placeholder, "Message api");
});

test("a working session queues, and can send now", () => {
  const s = composerState({ ...base, status: "active" });
  assert.deepEqual([s.canSend, s.canSendNow, s.label], [true, true, "Queue"]);
  assert.match(s.placeholder, /after its current step/);
});

test("a permission prompt locks the box", () => {
  const s = composerState({ ...base, status: "permission" });
  assert.deepEqual([s.canSend, s.canSendNow, s.label], [false, false, "Waiting for permission"]);
});

test("a question accepts an answer but not send now", () => {
  const s = composerState({ ...base, status: "question", pendingTool: "AskUserQuestion" });
  assert.deepEqual([s.canSend, s.canSendNow], [true, false]);
  assert.equal(s.placeholder, "Answer in the form above…");
});

test("no session and external sessions", () => {
  assert.equal(composerState({ ...base, hasSession: false, status: "none" }).label, "No session");
  assert.equal(composerState({ ...base, status: "external" }).label, "External session");
  assert.equal(composerState({ ...base, status: "external" }).canSend, false);
});

test("a launching session holds one message until it starts", () => {
  const first = composerState({ ...base, hasSession: false, status: "none", waitingToStart: true });
  assert.deepEqual([first.canSend, first.label], [true, "Send when ready"]);
  const held = composerState({ ...base, hasSession: false, status: "none", waitingToStart: true, held: true });
  assert.deepEqual([held.canSend, held.label], [false, "Queued"]);
  assert.equal(held.canSendNow, false);
});
