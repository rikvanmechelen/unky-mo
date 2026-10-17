// Tests for static/queue-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { composerState, createQueueTracker } = require("../static/queue-model.js");

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

const op = (operation, extra = {}) => ({ type: "queue-operation", operation, ...extra });

test("a queued message is pending until Claude reads it", () => {
  const q = createQueueTracker();
  assert.equal(q.onLine(op("enqueue", { content: "also say pear" })), true);
  assert.deepEqual(q.pending(), ["also say pear"]);
  q.onLine(op("remove", { content: "also say pear", reason: "absorbed_mid_turn" }));
  assert.deepEqual(q.pending(), []);
});

test("dequeue takes the oldest entry, send now's path", () => {
  const q = createQueueTracker();
  q.onLine(op("enqueue", { content: "one" }));
  q.onLine(op("enqueue", { content: "two" }));
  q.onLine(op("dequeue"));
  assert.deepEqual(q.pending(), ["two"]);
});

test("popAll lines drop each message pulled back into the input box", () => {
  const q = createQueueTracker();
  for (const c of ["a", "b\nc"]) q.onLine(op("enqueue", { content: c }));
  q.onLine(op("popAll", { content: "a" }));
  q.onLine(op("popAll", { content: "b\nc" }));
  assert.deepEqual(q.pending(), []);
});

test("task notifications share the queue but aren't shown, and still take their turn", () => {
  const q = createQueueTracker();
  q.onLine(op("enqueue", { content: "<task-notification>\n<task-id>x</task-id>" }));
  q.onLine(op("enqueue", { content: "<agent-message from=\"a\">hi" }));
  q.onLine(op("enqueue", { content: "mine" }));
  assert.deepEqual(q.pending(), ["mine"]);
  q.onLine(op("dequeue")); // the notification starts a turn
  q.onLine(op("dequeue"));
  assert.deepEqual(q.pending(), ["mine"]);
});

test("identical messages are removed one at a time; unrelated lines change nothing", () => {
  const q = createQueueTracker();
  q.onLine(op("enqueue", { content: "same" }));
  q.onLine(op("enqueue", { content: "same" }));
  q.onLine(op("remove", { content: "same" }));
  assert.deepEqual(q.pending(), ["same"]);
  assert.equal(q.onLine({ type: "user", uuid: "u" }), false);
  assert.equal(q.onLine(op("enqueue")), false);
  q.reset();
  assert.deepEqual(q.pending(), []);
});
