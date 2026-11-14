// Pure helpers for the slash-command menu dialog (menu.js), run in Node by
// jstests/menu_model.test.js. The view is what GET /api/sessions/{id}/menu
// returns (see menu.go's menuView).

// menuSections splits a menu's rows into runs that share a group heading,
// keeping each row's index in view.rows (the cursor and every key the
// browser sends are relative to that numbering).
function menuSections(view) {
  const out = [];
  for (const [index, row] of ((view && view.rows) || []).entries()) {
    const group = row.group || "";
    let cur = out[out.length - 1];
    if (!cur || cur.group !== group) {
      cur = { group, items: [] };
      out.push(cur);
    }
    cur.items.push({ row, index });
  }
  return out;
}

const GLYPHS = { enter: "⏎", esc: "Esc", up: "↑", down: "↓", left: "←", right: "→", tab: "Tab", btab: "⇧Tab", space: "Space" };

function keyGlyph(key) {
  if (GLYPHS[key]) return GLYPHS[key];
  const m = /^C-([a-z])$/.exec(key);
  return m ? "Ctrl+" + m[1].toUpperCase() : key;
}

// menuLegendButtons turns the legend ("Enter to select · ←/→ to adjust ·
// Type to search") into buttons. ←/→ and ↑/↓ are two keys that undo each
// other, so they become two buttons; "Enter/Space" are alternatives for one
// action, so they become the first; "Type to …" has no key (the dialog's
// input takes it).
function menuLegendButtons(legend) {
  return (legend || []).map((k) => {
    const keys = k.keys || [];
    const pair = keys.length === 2 && ((keys.includes("left") && keys.includes("right")) || (keys.includes("up") && keys.includes("down")));
    const use = k.typing ? [] : pair ? keys : keys.slice(0, 1);
    return { label: k.label, typing: !!k.typing, buttons: use.map((key) => ({ key, text: keyGlyph(key) })) };
  });
}

// menuHasKey says whether the legend offers key (any of its alternatives).
function menuHasKey(view, key) {
  return !!view && (view.legend || []).some((k) => (k.keys || []).includes(key));
}

const EVENT_KEYS = {
  ArrowUp: "up", ArrowDown: "down", ArrowLeft: "left", ArrowRight: "right",
  Enter: "enter", Escape: "esc", Tab: "tab", Backspace: "backspace",
  PageUp: "pgup", PageDown: "pgdn", Home: "home", End: "end", " ": "space",
};

// menuKeyFromEvent maps a keydown to the server's key vocabulary, or null
// when it isn't the menu's. Printable characters go through as themselves;
// Ctrl combinations only when the legend asks for one (browser shortcuts
// stay the browser's) and never Ctrl+C/D/Z, which the server refuses too.
function menuKeyFromEvent(e, view) {
  if (e.isComposing) return null;
  if (e.key === "Tab" && e.shiftKey) return "btab";
  if (EVENT_KEYS[e.key]) return e.metaKey || e.ctrlKey || e.altKey ? null : EVENT_KEYS[e.key];
  if (e.metaKey || e.altKey) return null;
  if (e.ctrlKey) {
    if (!/^[a-z]$/i.test(e.key) || /^[cdz]$/i.test(e.key)) return null;
    const key = "C-" + e.key.toLowerCase();
    return menuHasKey(view, key) ? key : null;
  }
  return [...e.key].length === 1 ? e.key : null;
}

// menuTextDelta is the key presses that turn the text a menu's input shows
// (prev) into what the browser's input now holds (next): backspaces to the
// common prefix, then the new tail typed.
function menuTextDelta(prev, next) {
  const a = [...prev], b = [...next];
  let i = 0;
  while (i < a.length && i < b.length && a[i] === b[i]) i++;
  const keys = [];
  for (let n = a.length - i; n > 0; n--) keys.push({ key: "backspace" });
  if (i < b.length) keys.push({ text: b.slice(i).join("") });
  return keys;
}

// menuClickPlan is what clicking a row does: move the cursor there, or —
// when it's already there — press Enter if the legend offers it. null means
// nothing to do.
function menuClickPlan(view, index) {
  const row = view && view.rows && view.rows[index];
  if (!row) return null;
  if (!row.cursor) return { goto: { row: index } };
  return menuHasKey(view, "enter") ? { keys: [{ key: "enter" }] } : null;
}

if (typeof module === "object" && module.exports) {
  module.exports = { menuSections, menuLegendButtons, menuHasKey, menuKeyFromEvent, menuTextDelta, menuClickPlan };
}
