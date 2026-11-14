// Tests for static/menu-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { menuSections } = require("../static/menu-model.js");

const row = (text, group = "", extra = {}) => ({ text, cols: [text], group, ...extra });

test("menuSections keeps the row indexes the server numbered", () => {
  const rows = [row("a", "G1"), row("b", "G1"), row("c", "G2"), row("d", "")];
  const got = menuSections({ rows });
  assert.deepEqual(got.map((s) => [s.group, s.items.map((i) => i.index)]), [["G1", [0, 1]], ["G2", [2]], ["", [3]]]);
  assert.equal(got[0].items[1].row.text, "b");
});

test("menuSections: no groups is one headerless section, no rows is none", () => {
  assert.deepEqual(menuSections({ rows: [row("a"), row("b")] }).map((s) => s.items.length), [2]);
  assert.deepEqual(menuSections({ rows: [] }), []);
  assert.deepEqual(menuSections(null), []);
});

test("menuSections starts a new section when a group comes back", () => {
  const got = menuSections({ rows: [row("a", "X"), row("b", "Y"), row("c", "X")] });
  assert.deepEqual(got.map((s) => s.group), ["X", "Y", "X"]);
});
