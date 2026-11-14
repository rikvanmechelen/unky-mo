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

// menuClickPlan is what clicking a row does: move the cursor there. It
// never presses Enter, even on the row the cursor is already on: a second
// tap would then change a /config setting by accident. The cursor row's
// action button (menuEnterLabel) does that. null means nothing to do.
function menuClickPlan(view, index) {
  const row = view && view.rows && view.rows[index];
  if (!row || row.cursor) return null;
  return { goto: { row: index } };
}

// menuEnterLabel is what Enter does here, in the legend's words ("Set as
// default"), for the cursor row's action button; null when Enter does
// nothing the legend mentions.
function menuEnterLabel(view) {
  const k = ((view && view.legend) || []).find((k) => (k.keys || [])[0] === "enter");
  return k ? k.label.charAt(0).toUpperCase() + k.label.slice(1) : null;
}

// commandMenuTag is the tag the slash list shows on a command that opens a
// dialog: null for a plain command, "opens menu" for one the chat view can
// drive, "opens in terminal" for one it can't (c.terminal).
function commandMenuTag(c) {
  if (!c.dialog) return null;
  if (c.terminal) {
    return { text: "opens in terminal", terminal: true,
      title: c.argument_hint ? "Without arguments this opens a dialog in the terminal, which the browser can't answer"
        : "Opens a dialog in the terminal, which the browser can't answer" };
  }
  return { text: "opens menu", terminal: false,
    title: c.argument_hint ? "Without arguments this opens a menu, which you can operate from here"
      : "Opens a menu, which you can operate from here" };
}

// isBareSlashCommand: the message is just "/word" — the shape of a command
// that opens a menu (with arguments it usually doesn't), so the chat view
// looks for one after sending it.
function isBareSlashCommand(text) {
  return /^\/[A-Za-z][\w:.-]*$/.test(text.trim());
}

if (typeof module === "object" && module.exports) {
  module.exports = { menuSections, menuLegendButtons, menuHasKey, menuKeyFromEvent, menuClickPlan, menuEnterLabel, commandMenuTag, isBareSlashCommand };
}
