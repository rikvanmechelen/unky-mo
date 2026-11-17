// The composer's prompt history, recalled with the up and down arrows as in
// Claude Code. The list comes from GET …/prompt-history (newest first, the
// session's own prompts before the rest of the checkout's). This is only the
// browsing state; chat.js fetches the list and handles the keys.
//
// state: {index, stash, shown}. index -1 is the box's own text (not
// browsing); stash keeps that text while browsing, shown is the entry put
// in the box, so an edit to it ends the browse.

function historyIdle() {
  return { index: -1, stash: "", shown: null };
}

// historyMove steps one entry older (dir -1, the up arrow) or newer (dir 1).
// It returns {state, text} for the box, or null when there's nowhere to go:
// up past the oldest, or down while not browsing.
function historyMove(state, entries, dir, current) {
  const list = entries || [];
  if (dir < 0) {
    if (state.index + 1 >= list.length) return null;
    const index = state.index + 1;
    const stash = state.index === -1 ? current : state.stash;
    return { state: { index, stash, shown: list[index] }, text: list[index] };
  }
  if (state.index === -1) return null;
  const index = state.index - 1;
  const text = index === -1 ? state.stash : list[index];
  return { state: index === -1 ? historyIdle() : { index, stash: state.stash, shown: text }, text };
}

// historyBrowsing: the box still shows the entry recalled, so the arrows
// keep walking the history wherever the caret is.
function historyBrowsing(state, current) {
  return state.index >= 0 && current === state.shown;
}

// An arrow recalls only from the edge of the text, so it still moves
// between the lines of a message being written.
function caretOnFirstLine(value, start, end) {
  return start === end && !value.slice(0, start).includes("\n");
}

function caretOnLastLine(value, start, end) {
  return start === end && !value.slice(end).includes("\n");
}

if (typeof module === "object" && module.exports) {
  module.exports = { historyIdle, historyMove, historyBrowsing, caretOnFirstLine, caretOnLastLine };
}
