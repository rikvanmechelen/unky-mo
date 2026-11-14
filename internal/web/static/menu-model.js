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

if (typeof module === "object" && module.exports) {
  module.exports = { menuSections };
}
