// Tests for static/tabs-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { tabShortcut, tabShortcutLabel, noteTitle, pushClosed, popClosed } = require("../static/tabs-model.js");

const key = (code, mods = {}) => ({ code, key: code.replace(/^Key/, "").toLowerCase(), ctrlKey: false, metaKey: false, altKey: false, shiftKey: false, isComposing: false, ...mods });

test("tabShortcut reads Ctrl off a Mac and Cmd on one", () => {
  assert.equal(tabShortcut(key("KeyW", { ctrlKey: true }), false), "close");
  assert.equal(tabShortcut(key("KeyT", { ctrlKey: true }), false), "new");
  assert.equal(tabShortcut(key("KeyT", { ctrlKey: true, shiftKey: true }), false), "reopen");
  assert.equal(tabShortcut(key("KeyW", { metaKey: true }), false), null);
  assert.equal(tabShortcut(key("KeyW", { metaKey: true }), true), "close");
  assert.equal(tabShortcut(key("KeyT", { metaKey: true, shiftKey: true }), true), "reopen");
  assert.equal(tabShortcut(key("KeyW", { ctrlKey: true }), true), null); // Ctrl+W on a Mac stays the editor's
});

test("tabShortcut takes Alt everywhere, by key position", () => {
  assert.equal(tabShortcut(key("KeyW", { altKey: true }), false), "close");
  assert.equal(tabShortcut(key("KeyT", { altKey: true, shiftKey: true }), false), "reopen");
  // Option+W types "∑" on a Mac: the code still says W.
  assert.equal(tabShortcut({ ...key("KeyW", { altKey: true }), key: "∑" }, true), "close");
});

test("tabShortcut ignores everything else", () => {
  assert.equal(tabShortcut(key("KeyW"), false), null);
  assert.equal(tabShortcut(key("KeyW", { ctrlKey: true, shiftKey: true }), false), null);
  assert.equal(tabShortcut(key("KeyW", { ctrlKey: true, altKey: true }), false), null); // AltGr
  assert.equal(tabShortcut(key("KeyP", { ctrlKey: true }), false), null);
  assert.equal(tabShortcut(key("KeyW", { ctrlKey: true, isComposing: true }), false), null);
});

test("tabShortcutLabel", () => {
  assert.equal(tabShortcutLabel("close", false), "Alt+W");
  assert.equal(tabShortcutLabel("reopen", false), "Alt+Shift+T");
  assert.equal(tabShortcutLabel("new", true), "⌥T");
  assert.equal(tabShortcutLabel("reopen", true), "⌥⇧T");
});

test("noteTitle uses the first non-blank line", () => {
  assert.equal(noteTitle(""), "Untitled");
  assert.equal(noteTitle("\n  \n"), "Untitled");
  assert.equal(noteTitle("\n  fix   the  thing\nmore"), "fix the thing");
  assert.equal(noteTitle("x".repeat(40)), "x".repeat(29) + "…");
});

test("pushClosed keeps one entry per tab, newest last, capped", () => {
  let s = [];
  s = pushClosed(s, { kind: "file", path: "a" });
  s = pushClosed(s, { kind: "diff", path: "a" });
  s = pushClosed(s, { kind: "file", path: "a" });
  assert.deepEqual(s, [{ kind: "diff", path: "a" }, { kind: "file", path: "a" }]);
  s = pushClosed(s, { kind: "commit", path: "a", hash: "1" });
  s = pushClosed(s, { kind: "commit", path: "a", hash: "2" });
  assert.equal(s.length, 4);
  const many = Array.from({ length: 5 }, (_, i) => ({ kind: "file", path: String(i) })).reduce((acc, e) => pushClosed(acc, e, 3), []);
  assert.deepEqual(many.map((e) => e.path), ["2", "3", "4"]);
});

test("popClosed skips tabs that are open again", () => {
  const s = [{ kind: "file", path: "a" }, { kind: "file", path: "b" }, { kind: "file", path: "c" }];
  const { entry, rest } = popClosed(s, (e) => e.path === "c");
  assert.deepEqual(entry, { kind: "file", path: "b" });
  assert.deepEqual(rest, [{ kind: "file", path: "a" }]);
  assert.deepEqual(popClosed(s, () => true), { entry: null, rest: [] });
  assert.deepEqual(popClosed([], () => false), { entry: null, rest: [] });
});
