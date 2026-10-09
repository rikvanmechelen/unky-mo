// Pure helpers for the editor tab strip's keyboard shortcuts, scratch notes
// and closed-tab history (editor.js), kept apart so Node can test them
// (jstests/tabs_model.test.js).
//
// Browsers keep Ctrl/Cmd+W, Ctrl/Cmd+T and Ctrl/Cmd+Shift+T for their own
// tabs: in a normal browser tab the page never sees them. They do reach the
// page in some app windows (an installed PWA), so they're handled when they
// arrive, and Alt (Option on macOS) with the same letters works everywhere.

const CLOSED_TABS_MAX = 20;

// tabShortcut maps a keydown to "close", "new" or "reopen", or null. mac
// picks Cmd over Ctrl as the primary modifier. Letters are read from e.code
// so Option+W on a Mac (which types "∑") still counts.
function tabShortcut(e, mac) {
  if (e.isComposing) return null;
  const primary = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  const alt = e.altKey && !e.ctrlKey && !e.metaKey;
  if (!(primary && !e.altKey) && !alt) return null;
  const letter = e.code ? e.code.replace(/^Key/, "").toLowerCase() : (e.key || "").toLowerCase();
  if (letter === "w" && !e.shiftKey) return "close";
  if (letter === "t") return e.shiftKey ? "reopen" : "new";
  return null;
}

// tabShortcutLabel names the Alt shortcut for an action, for tooltips.
function tabShortcutLabel(action, mac) {
  const letter = { close: "W", new: "T", reopen: "T" }[action];
  const shift = action === "reopen";
  return mac ? `⌥${shift ? "⇧" : ""}${letter}` : `Alt+${shift ? "Shift+" : ""}${letter}`;
}

// noteTitle names a scratch note after its first non-blank line.
function noteTitle(text) {
  const line = (text || "").split("\n").map((l) => l.trim().replace(/\s+/g, " ")).find(Boolean);
  if (!line) return "Untitled";
  return line.length > 30 ? line.slice(0, 29) + "…" : line;
}

// pushClosed returns the closed-tab history with entry on top: an earlier
// entry for the same tab is dropped, and the oldest past the cap.
function pushClosed(stack, entry, max = CLOSED_TABS_MAX) {
  const same = (a) => a.kind === entry.kind && a.path === entry.path && (a.hash || "") === (entry.hash || "");
  return [...stack.filter((a) => !same(a)), entry].slice(-max);
}

// popClosed takes the newest entry that isn't open any more (isOpen gets
// each entry), returning it and the rest of the history. Entries for tabs
// that are open again are dropped on the way.
function popClosed(stack, isOpen) {
  const rest = stack.slice();
  while (rest.length) {
    const entry = rest.pop();
    if (!isOpen(entry)) return { entry, rest };
  }
  return { entry: null, rest };
}

if (typeof module === "object" && module.exports) {
  module.exports = { tabShortcut, tabShortcutLabel, noteTitle, pushClosed, popClosed, CLOSED_TABS_MAX };
}
