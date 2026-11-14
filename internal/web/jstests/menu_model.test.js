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

const { menuLegendButtons, menuKeyFromEvent, menuClickPlan, menuHasKey, menuEnterLabel } = require("../static/menu-model.js");

test("menuLegendButtons: single keys, alternatives, pairs and typing", () => {
  const legend = [
    { keys: ["enter"], label: "set as default" },
    { keys: ["s"], label: "use this session only" },
    { keys: ["left", "right"], label: "adjust" },
    { keys: ["enter", "space"], label: "change" },
    { keys: ["up", "down"], label: "navigate" },
    { keys: ["enter", "down"], label: "select" },
    { keys: [], typing: true, label: "search" },
    { keys: ["C-t"], label: "disable" },
    { keys: ["esc"], label: "cancel" },
  ];
  const got = menuLegendButtons(legend);
  assert.deepEqual(got.map((g) => g.buttons.map((b) => b.key)), [
    ["enter"], ["s"], ["left", "right"], ["enter"], ["up", "down"], ["enter"], [], ["C-t"], ["esc"],
  ]);
  assert.deepEqual(got[2].buttons.map((b) => b.text), ["←", "→"]);
  assert.equal(got[7].buttons[0].text, "Ctrl+T");
  assert.equal(got[6].typing, true);
  assert.equal(got[0].label, "set as default");
});

test("menuHasKey looks through the legend", () => {
  const view = { legend: [{ keys: ["enter", "space"] }, { keys: ["esc"] }] };
  assert.equal(menuHasKey(view, "space"), true);
  assert.equal(menuHasKey(view, "tab"), false);
  assert.equal(menuHasKey(null, "esc"), false);
});

const ev = (key, extra = {}) => ({ key, shiftKey: false, ctrlKey: false, altKey: false, metaKey: false, isComposing: false, ...extra });

test("menuKeyFromEvent maps navigation keys", () => {
  const view = { legend: [] };
  assert.equal(menuKeyFromEvent(ev("ArrowDown"), view), "down");
  assert.equal(menuKeyFromEvent(ev("ArrowUp"), view), "up");
  assert.equal(menuKeyFromEvent(ev("ArrowLeft"), view), "left");
  assert.equal(menuKeyFromEvent(ev("ArrowRight"), view), "right");
  assert.equal(menuKeyFromEvent(ev("Enter"), view), "enter");
  assert.equal(menuKeyFromEvent(ev("Escape"), view), "esc");
  assert.equal(menuKeyFromEvent(ev("Tab"), view), "tab");
  assert.equal(menuKeyFromEvent(ev("Tab", { shiftKey: true }), view), "btab");
  assert.equal(menuKeyFromEvent(ev("Backspace"), view), "backspace");
  assert.equal(menuKeyFromEvent(ev("PageUp"), view), "pgup");
  assert.equal(menuKeyFromEvent(ev("PageDown"), view), "pgdn");
  assert.equal(menuKeyFromEvent(ev("Home"), view), "home");
  assert.equal(menuKeyFromEvent(ev("End"), view), "end");
  assert.equal(menuKeyFromEvent(ev(" "), view), "space");
});

test("menuKeyFromEvent sends printable characters and ignores the rest", () => {
  const view = { legend: [{ keys: ["C-t"] }] };
  assert.equal(menuKeyFromEvent(ev("s"), view), "s");
  assert.equal(menuKeyFromEvent(ev("S", { shiftKey: true }), view), "S");
  assert.equal(menuKeyFromEvent(ev("/"), view), "/");
  assert.equal(menuKeyFromEvent(ev("é"), view), "é");
  assert.equal(menuKeyFromEvent(ev("F5"), view), null);
  assert.equal(menuKeyFromEvent(ev("Shift"), view), null);
  assert.equal(menuKeyFromEvent(ev("a", { isComposing: true }), view), null);
  // browser shortcuts stay the browser's
  assert.equal(menuKeyFromEvent(ev("r", { metaKey: true }), view), null);
  assert.equal(menuKeyFromEvent(ev("w", { ctrlKey: true }), view), null);
  // ...except a Ctrl key the legend asks for
  assert.equal(menuKeyFromEvent(ev("t", { ctrlKey: true }), view), "C-t");
  assert.equal(menuKeyFromEvent(ev("c", { ctrlKey: true }), { legend: [{ keys: ["C-c"] }] }), null);
});

test("menuClickPlan only ever moves the cursor", () => {
  const view = {
    sig: "s1",
    legend: [{ keys: ["enter"], label: "select" }, { keys: ["esc"], label: "cancel" }],
    rows: [{ text: "a", cursor: true }, { text: "b" }],
  };
  assert.deepEqual(menuClickPlan(view, 1), { goto: { row: 1 } });
  // A second tap on the same row must not press Enter: that changes a
  // /config setting with one tap. The row's action button does.
  assert.equal(menuClickPlan(view, 0), null);
  assert.equal(menuClickPlan(view, 5), null);
});

test("menuEnterLabel names what Enter does, from the legend", () => {
  assert.equal(menuEnterLabel({ legend: [{ keys: ["enter"], label: "set as default" }] }), "Set as default");
  assert.equal(menuEnterLabel({ legend: [{ keys: ["enter", "space"], label: "change" }] }), "Change");
  assert.equal(menuEnterLabel({ legend: [{ keys: ["enter", "down"], label: "select" }] }), "Select");
  assert.equal(menuEnterLabel({ legend: [{ keys: ["esc"], label: "close" }] }), null);
  assert.equal(menuEnterLabel(null), null);
});

const { commandMenuTag, isBareSlashCommand } = require("../static/menu-model.js");

test("commandMenuTag: menus the web drives vs dialogs that need the terminal", () => {
  assert.equal(commandMenuTag({ name: "compact" }), null);
  assert.deepEqual(commandMenuTag({ name: "model", dialog: true }), { text: "opens menu", terminal: false,
    title: "Opens a menu, which you can operate from here" });
  assert.equal(commandMenuTag({ name: "model", dialog: true, argument_hint: "[model]" }).title,
    "Without arguments this opens a menu, which you can operate from here");
  const t = commandMenuTag({ name: "login", dialog: true, terminal: true });
  assert.equal(t.text, "opens in terminal");
  assert.equal(t.terminal, true);
});

test("isBareSlashCommand: a lone /word may open a menu", () => {
  for (const ok of ["/model", "  /config  ", "/plugin\n", "/eng:review"]) assert.equal(isBareSlashCommand(ok), true, ok);
  for (const no of ["", "model", "/model opus", "/", "//x", "/a/b", "hello /model", "/model\nmore"]) assert.equal(isBareSlashCommand(no), false, no);
});
