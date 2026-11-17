// Tests for static/history-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { historyIdle, historyMove, historyBrowsing, caretOnFirstLine, caretOnLastLine } = require("../static/history-model.js");

test("up walks older, down walks back to the draft", () => {
  const list = ["newest", "middle", "oldest"];
  let s = historyIdle();
  let r = historyMove(s, list, -1, "my draft");
  assert.equal(r.text, "newest");
  s = r.state;
  assert.ok(historyBrowsing(s, "newest"));
  r = historyMove(s, list, -1, "newest");
  s = r.state;
  r = historyMove(s, list, -1, "middle");
  assert.equal(r.text, "oldest");
  s = r.state;
  assert.equal(historyMove(s, list, -1, "oldest"), null, "nothing past the oldest");
  r = historyMove(s, list, 1, "oldest");
  assert.equal(r.text, "middle");
  s = historyMove(r.state, list, 1, "middle").state;
  r = historyMove(s, list, 1, "newest");
  assert.equal(r.text, "my draft", "the draft comes back");
  assert.deepEqual(r.state, historyIdle());
  assert.equal(historyMove(r.state, list, 1, "my draft"), null, "down does nothing unbrowsed");
});

test("an empty history goes nowhere", () => {
  assert.equal(historyMove(historyIdle(), [], -1, ""), null);
  assert.equal(historyMove(historyIdle(), null, -1, ""), null);
});

test("an edited entry is no longer browsing", () => {
  const s = historyMove(historyIdle(), ["a"], -1, "").state;
  assert.ok(historyBrowsing(s, "a"));
  assert.ok(!historyBrowsing(s, "a!"));
  assert.ok(!historyBrowsing(historyIdle(), ""));
});

test("the caret recalls only from the first or last line", () => {
  const v = "one\ntwo\nthree";
  assert.ok(caretOnFirstLine(v, 2, 2));
  assert.ok(!caretOnFirstLine(v, 5, 5));
  assert.ok(!caretOnFirstLine(v, 0, 2), "a selection keeps the arrow");
  assert.ok(caretOnLastLine(v, 9, 9));
  assert.ok(!caretOnLastLine(v, 5, 5));
  assert.ok(caretOnFirstLine("", 0, 0) && caretOnLastLine("", 0, 0));
});
