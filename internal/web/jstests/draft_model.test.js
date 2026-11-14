// Tests for static/draft-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { saveDraft, loadDraft, dropDraft, DRAFT_TTL_MS } = require("../static/draft-model.js");

function memStorage() {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
    size: () => m.size,
  };
}

test("a draft round-trips, keeping only uploaded images", () => {
  const s = memStorage();
  saveDraft(s, "@3", "half a thought", [
    { id: "a.png", name: "shot.png", state: "ready" },
    { id: null, name: "big.png", state: "uploading" },
    { id: null, name: "bad.png", state: "error" },
  ], 1000);
  assert.deepEqual(loadDraft(s, "@3", 2000), { text: "half a thought", attachments: [{ id: "a.png", name: "shot.png" }] });
  assert.equal(loadDraft(s, "@4", 2000), null);
});

test("images alone are a draft; nothing at all removes it", () => {
  const s = memStorage();
  saveDraft(s, "@3", "", [{ id: "a.png", state: "ready" }], 1000);
  assert.deepEqual(loadDraft(s, "@3", 1000), { text: "", attachments: [{ id: "a.png", name: "" }] });
  saveDraft(s, "@3", "  \n", [], 1000);
  assert.equal(s.size(), 0);
});

test("an old draft is dropped", () => {
  const s = memStorage();
  saveDraft(s, "@3", "hi", [], 0);
  assert.equal(loadDraft(s, "@3", DRAFT_TTL_MS + 1), null);
  assert.equal(s.size(), 0);
});

test("dropDraft removes it", () => {
  const s = memStorage();
  saveDraft(s, "@3", "hi", [], 0);
  dropDraft(s, "@3");
  assert.equal(loadDraft(s, "@3", 0), null);
});

test("malformed drafts and storage that throws are no draft", () => {
  const s = memStorage();
  s.setItem("mo.composerDraft.@3", "{not json");
  assert.equal(loadDraft(s, "@3", 0), null);
  s.setItem("mo.composerDraft.@3", JSON.stringify({ text: 1, attachments: [] }));
  assert.equal(loadDraft(s, "@3", 0), null);
  s.setItem("mo.composerDraft.@3", JSON.stringify({ text: "x", attachments: [{ id: 5 }, null, { id: "b.png" }], at: 0 }));
  assert.deepEqual(loadDraft(s, "@3", 0), { text: "x", attachments: [{ id: "b.png", name: "" }] });

  const broken = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("full"); }, removeItem() { throw new Error("blocked"); } };
  assert.equal(loadDraft(broken, "@3", 0), null);
  saveDraft(broken, "@3", "hi", [], 0);
  dropDraft(broken, "@3");
});
