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

// promptDialog asks for one line of text. Resolves with the trimmed text, or
// undefined when cancelled or left empty.
function promptDialog({ title, text, placeholder, confirmLabel }) {
  return new Promise((resolve) => {
    const dialog = dialogNode("dialog", "dialog");
    const form = dialogNode("form", "dialog__form");
    const input = dialogNode("input", "text-input dialog__input");
    input.type = "text";
    input.placeholder = placeholder || "";
    input.autocomplete = "off";
    const done = (value) => { dialog.close(); dialog.remove(); resolve(value); };
    form.appendChild(dialogNode("div", "dialog__title", title));
    if (text) form.appendChild(dialogNode("div", "dialog__text", text));
    form.appendChild(input);
    const row = dialogNode("div", "dialog__actions");
    const cancel = dialogNode("button", "btn", "Cancel");
    cancel.type = "button";
    cancel.addEventListener("click", () => done(undefined));
    const ok = dialogNode("button", "btn btn--primary", confirmLabel || "OK");
    ok.type = "submit";
    row.append(cancel, ok);
    form.appendChild(row);
    form.addEventListener("submit", (e) => { e.preventDefault(); done(input.value.trim() || undefined); });
    dialog.appendChild(form);
    dialog.addEventListener("cancel", () => { dialog.remove(); resolve(undefined); });
    document.body.appendChild(dialog);
    dialog.showModal();
    input.focus();
  });
}

// restartMo does what ctrl+alt+r does in the TUI: restart the TUI, every
// sidebar and this web server, picking up a freshly-installed binary. The
// server is replaced too, so it waits for /api/boot to report a new id and
// then reloads the page (new static files; editor drafts live in
// localStorage, so nothing unsaved is lost).
async function restartMo() {
  const ok = await showDialog({
    title: "Restart mo?",
    text: "Restarts the TUI, all sidebars and the web server, like ctrl+alt+r. Run make install first to pick up changes.",
    actions: [{ label: "Cancel" }, { label: "Restart", value: true }],
  });
  if (!ok) return;

  const bootID = async () => {
    const res = await fetch("/api/boot", { cache: "no-store" });
    if (!res.ok) throw new Error(`status ${res.status}`);
    return (await res.json()).boot;
  };
  let before;
  try {
    before = await bootID();
  } catch (e) {
    await showDialog({ title: "Couldn't restart", text: `The web server didn't answer (${e.message}).`, actions: [{ label: "OK" }] });
    return;
  }
  const res = await fetch("/api/restart", { method: "POST" });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    await showDialog({ title: "Couldn't restart", text: data.error || `request failed (${res.status})`, actions: [{ label: "OK" }] });
    return;
  }

  // A modal that esc can't dismiss while the servers come back.
  const overlay = dialogNode("dialog", "dialog");
  overlay.appendChild(dialogNode("div", "dialog__title", "Restarting mo…"));
  const note = dialogNode("div", "dialog__text", "Waiting for the web server to come back.");
  overlay.appendChild(note);
  overlay.addEventListener("cancel", (e) => e.preventDefault());
  document.body.appendChild(overlay);
  overlay.showModal();

  const deadline = Date.now() + 30000;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 500));
    try {
      if ((await bootID()) !== before) {
        location.reload();
        return;
      }
    } catch {
      // The old server is gone and the new one isn't listening yet.
    }
  }
  note.textContent = "The web server didn't come back within 30s. Check it with: tmux attach -t mo-web";
  const row = dialogNode("div", "dialog__actions");
  const close = dialogNode("button", "btn", "Close");
  close.type = "button";
  close.addEventListener("click", () => { overlay.close(); overlay.remove(); });
  row.appendChild(close);
  overlay.appendChild(row);
}

for (const btn of document.querySelectorAll("[data-restart-mo]")) {
  btn.addEventListener("click", () => restartMo());
}
