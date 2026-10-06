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

// TRUST_STEPS are the per-platform steps for trusting mo web's local CA,
// as `mo web tls trust` prints them.
const TRUST_STEPS = {
  ios: {
    title: "iPhone / iPad",
    href: "/ca.crt",
    steps: [
      "Open this page in Safari and tap Download; allow the configuration profile.",
      "Settings → General → VPN & Device Management → the profile → Install.",
      "Settings → General → About → Certificate Trust Settings → turn on full trust for it. Without this step HTTPS still fails.",
      "Close this tab and open the page again.",
    ],
  },
  android: {
    title: "Android",
    href: "/ca.crt?as=file",
    steps: [
      "Tap Download (Android won't install a CA straight from the browser).",
      "Settings → Security & privacy → More security settings → Encryption & credentials → Install a certificate → CA certificate, and pick unky-mo-ca.crt. Search Settings for \"CA certificate\" if the path differs.",
      "Force stop Chrome (Settings → Apps → Chrome → Force stop) and reopen this page: until it restarts, Chrome keeps treating the site as untrusted. Firefox needs \"Use third party CA certificates\" in its secret settings.",
    ],
  },
};

// showTrustDialog offers the local CA for download (GET /ca.crt), with its
// fingerprint and the steps for this device's platform first.
async function showTrustDialog(info) {
  const ua = navigator.userAgent;
  const ios = /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1);
  const order = ios ? ["ios", "android"] : /Android/.test(ua) ? ["android", "ios"] : ["ios", "android"];

  const dialog = dialogNode("dialog", "dialog trust-dialog");
  const close = () => { dialog.close(); dialog.remove(); };
  dialog.appendChild(dialogNode("div", "dialog__title", "Trust on another device"));
  dialog.appendChild(dialogNode("div", "dialog__text",
    "Open this dashboard on the phone or tablet itself (the certificate warning you get until this is done is expected) and install mo web's local CA from here."));

  const fp = dialogNode("div", "trust-dialog__fp");
  fp.appendChild(dialogNode("div", "trust-dialog__label", `SHA-256 fingerprint of ${info.name}`));
  fp.appendChild(dialogNode("code", "trust-dialog__hash", info.fingerprint));
  fp.appendChild(dialogNode("div", "trust-dialog__note",
    "Check it against `mo web tls trust` on your machine, or in the certificate's details on the device: until the CA is trusted, this page's connection isn't verified."));
  dialog.appendChild(fp);

  order.forEach((key, i) => {
    const p = TRUST_STEPS[key];
    const block = dialogNode("details", "trust-dialog__platform");
    if (i === 0) block.open = true;
    block.appendChild(dialogNode("summary", "trust-dialog__summary", p.title));
    const ol = dialogNode("ol", "trust-dialog__steps");
    for (const step of p.steps) ol.appendChild(dialogNode("li", "", step));
    block.appendChild(ol);
    const dl = dialogNode("a", "btn btn--small btn--primary", `Download for ${p.title}`);
    dl.href = p.href;
    dl.setAttribute("download", "unky-mo-ca.crt");
    block.appendChild(dl);
    dialog.appendChild(block);
  });

  const row = dialogNode("div", "dialog__actions");
  const done = dialogNode("button", "btn", "Close");
  done.type = "button";
  done.addEventListener("click", close);
  row.appendChild(done);
  dialog.appendChild(row);
  dialog.addEventListener("cancel", () => dialog.remove());
  document.body.appendChild(dialog);
  dialog.showModal();
}

// The "Trust on another device" buttons only show when there's a local CA
// to hand out (not with TLS off or a cert from [web] cert_file).
(async () => {
  const btns = document.querySelectorAll("[data-trust-device]");
  if (!btns.length) return;
  let info;
  try {
    const res = await fetch("/api/tls");
    if (!res.ok) return;
    info = await res.json();
  } catch {
    return;
  }
  if (!info.ca) return;
  for (const btn of btns) {
    btn.hidden = false;
    btn.addEventListener("click", () => showTrustDialog(info));
  }
})();
