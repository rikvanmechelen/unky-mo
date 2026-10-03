// Session actions shared by the dashboard (app.js) and the chat view
// (chat.js): the modal dialog and stopping a session. Self-contained — each
// page script keeps its own el() helper, so nothing here depends on one.

function dialogNode(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text) e.textContent = text;
  return e;
}

// showDialog resolves with the clicked action's value (undefined on
// cancel/esc). Actions: [{label, value?, danger?}] — an action without a
// value is a plain dismiss.
function showDialog({ title, text, actions }) {
  return new Promise((resolve) => {
    const dialog = dialogNode("dialog", "dialog");
    const done = (value) => { dialog.close(); dialog.remove(); resolve(value); };
    dialog.appendChild(dialogNode("div", "dialog__title", title));
    if (text) dialog.appendChild(dialogNode("div", "dialog__text", text));
    const row = dialogNode("div", "dialog__actions");
    for (const a of actions) {
      const btn = dialogNode("button", "btn" + (a.danger ? " btn--danger" : a.value ? " btn--primary" : ""), a.label);
      btn.type = "button";
      btn.addEventListener("click", () => done(a.value));
      row.appendChild(btn);
    }
    dialog.appendChild(row);
    dialog.addEventListener("cancel", () => { dialog.remove(); resolve(undefined); });
    document.body.appendChild(dialog);
    dialog.showModal();
  });
}

// stopSession confirms, then SIGINTs the session and closes its tmux window
// (DELETE /api/sessions/{windowID}). Resolves true when it was stopped.
async function stopSession(windowID, windowName) {
  const ok = await showDialog({
    title: "Stop session?",
    text: `This interrupts Claude in ${windowName || windowID} and closes its tmux window, like the TUI's kill.`,
    actions: [{ label: "Cancel" }, { label: "Stop session", value: true, danger: true }],
  });
  if (!ok) return false;
  const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}`, { method: "DELETE" });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    await showDialog({ title: "Couldn't stop session", text: data.error || `request failed (${res.status})`, actions: [{ label: "OK" }] });
    return false;
  }
  return true;
}
