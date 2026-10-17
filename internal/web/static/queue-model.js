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

// A queue-operation line is how Claude Code's JSONL records its message
// queue (probed on 2.1.302):
//   enqueue  {content}                 a message joins the queue
//   remove   {content, reason}         read mid-turn ("absorbed_mid_turn")
//   dequeue  {}                        the oldest entry became a new turn
//   popAll   {content}                 pulled back into the input box (once
//                                      per message)
// Background-task notifications and agent messages travel through the same
// queue; they aren't the user's messages.
const SYSTEM_QUEUE_ENTRY = /^\s*<(task-notification|agent-message)\b/;

// createQueueTracker folds queue-operation lines, in file order, into the
// user messages still waiting. The transcript replays from its start on
// every (re)connect, so callers reset() first.
function createQueueTracker() {
  let items = []; // {content, system}, oldest first
  const dropFirst = (content) => {
    const i = items.findIndex((it) => it.content === content);
    if (i >= 0) items.splice(i, 1);
  };
  return {
    reset() { items = []; },
    // Returns true when the pending list may have changed.
    onLine(msg) {
      if (!msg || msg.type !== "queue-operation") return false;
      const content = typeof msg.content === "string" ? msg.content : null;
      switch (msg.operation) {
        case "enqueue":
          if (content === null) return false;
          items.push({ content, system: SYSTEM_QUEUE_ENTRY.test(content) });
          return true;
        case "remove":
        case "popAll":
          if (content === null) items.shift();
          else dropFirst(content);
          return true;
        case "dequeue":
          items.shift();
          return true;
        default:
          return false;
      }
    },
    // The user's queued messages, oldest first.
    pending() { return items.filter((it) => !it.system).map((it) => it.content); },
  };
}

if (typeof module === "object" && module.exports) {
  module.exports = { composerState, createQueueTracker };
}
