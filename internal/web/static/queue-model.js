// queue-model.js — what the composer offers in each session state, and
// (below) the messages Claude Code has queued, with no DOM. Node's tests
// require it (internal/web/jstests).
//
// Claude Code accepts typing while it works: the message is queued and read
// at the next tool boundary, and ctrl+x ctrl+s makes it read it now. The
// server types the message into Claude's pane (and presses the chord for
// "send now"), so the composer only has to know when that's allowed.

// composerState returns {canSend, canSendNow, label, placeholder} for the
// message box and its buttons. in: {status, hasSession, name, pendingTool,
// waitingToStart, held}: status is the state file's status ("none" without
// a session), waitingToStart a just-launched session that hasn't shown up
// yet, held a message already waiting for it.
function composerState(c) {
  const active = c.status === "active";
  const canSend = c.status === "idle" || c.status === "question" || active ||
    (!!c.waitingToStart && !c.held);
  let label;
  if (c.waitingToStart) label = c.held ? "Queued" : "Send when ready";
  else if (active) label = "Queue";
  else if (canSend) label = "Send";
  else if (!c.hasSession) label = "No session";
  else if (c.status === "permission") label = "Waiting for permission";
  else if (c.status === "external") label = "External session";
  else label = "Not available";
  let placeholder;
  if (c.status === "question") {
    placeholder = c.pendingTool === "AskUserQuestion" ? "Answer in the form above…" : "Type a number or your answer…";
  } else if (c.status === "permission") {
    placeholder = "Answer the prompt above, or in the terminal";
  } else if (active) {
    placeholder = "Message Claude (it picks this up after its current step)";
  } else {
    placeholder = c.hasSession ? `Message ${c.name}` : "Message this session";
  }
  return { canSend, canSendNow: active && !c.waitingToStart, label, placeholder };
}

if (typeof module === "object" && module.exports) {
  module.exports = { composerState };
}
