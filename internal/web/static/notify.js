// Push notifications (docs/plans/notifications.md): the "Notifications"
// button and dialog, the service worker registration, and — on the chat
// page — the presence heartbeat that keeps this device quiet about the
// session it shows. Needs actions.js (dialogNode) and notify-model.js.

const notify = (() => {
  const ua = navigator.userAgent;
  const ios = isIOS(ua, navigator.maxTouchPoints);
  const standalone = window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
  const supported = window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

  let info = null; // GET /api/push
  const registration = supported
    ? navigator.serviceWorker.register("/sw.js").then(() => navigator.serviceWorker.ready).catch(() => null)
    : Promise.resolve(null);

  async function currentSubscription() {
    const reg = await registration;
    return reg ? reg.pushManager.getSubscription() : null;
  }

  async function postJSON(method, url, body) {
    const res = await fetch(url, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error(data.error || `request failed (${res.status})`);
    }
  }

  // state describes this device: "on", "off", "blocked", "unsupported" or
  // "ios-home-screen", plus the server's entry when on.
  async function state() {
    if (!supported) return { kind: ios && !standalone ? "ios-home-screen" : "unsupported" };
    if (Notification.permission === "denied") return { kind: "blocked" };
    const sub = await currentSubscription();
    if (!sub || !sameKey(sub.options.applicationServerKey, info.publicKey)) return { kind: "off" };
    const res = await fetch(`/api/push/subscriptions?endpoint=${encodeURIComponent(sub.endpoint)}`);
    const entry = res.ok ? await res.json() : {};
    return entry.subscribed ? { kind: "on", events: entry.events, sub } : { kind: "off" };
  }

  async function turnOn(events) {
    if ((await Notification.requestPermission()) !== "granted") throw new Error("Notifications aren't allowed for this site in the browser.");
    const reg = await registration;
    if (!reg) throw new Error("The service worker didn't start.");
    let sub = await reg.pushManager.getSubscription();
    if (sub && !sameKey(sub.options.applicationServerKey, info.publicKey)) {
      await sub.unsubscribe(); // made with an older server key
      sub = null;
    }
    if (!sub) {
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToBytes(info.publicKey) });
    }
    await postJSON("POST", "/api/push/subscriptions", { subscription: sub.toJSON(), events, label: deviceLabel(ua, standalone) });
  }

  async function turnOff() {
    const sub = await currentSubscription();
    if (!sub) return;
    await postJSON("DELETE", "/api/push/subscriptions", { endpoint: sub.endpoint });
    await sub.unsubscribe();
  }

  async function sendTest() {
    const sub = await currentSubscription();
    if (!sub) throw new Error("This device isn't subscribed.");
    await postJSON("POST", "/api/push/test", { endpoint: sub.endpoint });
  }

  const STATE_TEXT = {
    on: "On for this device.",
    off: "Off for this device.",
    blocked: "Notifications are blocked for this site. Allow them in the browser's site settings, then come back here.",
    unsupported: "This browser can't receive push notifications here. It needs a secure (trusted HTTPS) page and Web Push support.",
    "ios-home-screen": "On iPhone and iPad, notifications only work from the Home Screen app: in Safari, tap Share → Add to Home Screen, open Unky Mo from there, and turn them on in this dialog.",
  };

  function checkbox(label, checked) {
    const row = dialogNode("label", "notify-dialog__check");
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = checked;
    row.append(input, dialogNode("span", "", label));
    return { row, input };
  }

  async function openDialog() {
    const dialog = dialogNode("dialog", "dialog notify-dialog");
    const close = () => { dialog.close(); dialog.remove(); };
    dialog.addEventListener("cancel", () => dialog.remove());
    document.body.appendChild(dialog);
    dialog.showModal();

    const render = async (note) => {
      const st = await state().catch((e) => ({ kind: "off", error: e.message }));
      dialog.replaceChildren();
      dialog.appendChild(dialogNode("div", "dialog__title", "Notifications"));
      dialog.appendChild(dialogNode("div", "dialog__text",
        "Get a notification on this device when a Claude session needs you, even with the browser closed. Tapping it opens the session."));
      dialog.appendChild(dialogNode("div", "notify-dialog__state" + (st.kind === "on" ? " is-on" : ""), STATE_TEXT[st.kind]));

      const row = dialogNode("div", "dialog__actions");
      const button = (label, cls, fn) => {
        const b = dialogNode("button", "btn" + (cls ? " " + cls : ""), label);
        b.type = "button";
        b.addEventListener("click", async () => {
          for (const x of row.querySelectorAll("button")) x.disabled = true;
          try {
            await render(await fn());
          } catch (e) {
            await render({ error: e.message });
          }
        });
        row.appendChild(b);
      };

      if (st.kind === "on" || st.kind === "off") {
        const events = st.events || { input: true, done: false };
        const input = checkbox("A session needs input (a question or a permission prompt)", events.input);
        const done = checkbox("A session is done (turns longer than 15 s)", events.done);
        const box = dialogNode("div", "notify-dialog__checks");
        box.append(input.row, done.row);
        dialog.appendChild(box);
        const chosen = () => ({ input: input.input.checked, done: done.input.checked });
        if (st.kind === "on") {
          button("Send test", "", async () => { await sendTest(); return { ok: "Sent. It should show up in a few seconds." }; });
          button("Turn off", "", async () => { await turnOff(); return { ok: "Turned off." }; });
          button("Save", "btn--primary", async () => { await turnOn(chosen()); return { ok: "Saved." }; });
        } else {
          button("Turn on", "btn--primary", async () => { await turnOn(chosen()); return { ok: "Turned on. Send a test to check it." }; });
        }
      }
      const closeBtn = dialogNode("button", "btn", "Close");
      closeBtn.type = "button";
      closeBtn.addEventListener("click", close);
      row.prepend(closeBtn);

      if (note && (note.ok || note.error)) {
        dialog.appendChild(dialogNode("div", "notify-dialog__note" + (note.error ? " is-error" : ""), note.ok || note.error));
      }
      if (st.error) dialog.appendChild(dialogNode("div", "notify-dialog__note is-error", st.error));
      dialog.appendChild(row);
    };
    await render();
  }

  // Presence: while this page shows a session (visible and focused), tell
  // the server every 15 s so this device isn't notified about it, and close
  // its notification here.
  function startPresence() {
    let last = { window: "", at: 0 };
    const tick = async () => {
      const sub = await currentSubscription().catch(() => null);
      if (!sub) return;
      const shown = document.visibilityState === "visible" && document.hasFocus()
        ? new URLSearchParams(location.search).get("window") || ""
        : "";
      const now = Date.now();
      if (shown === last.window && (shown === "" || now - last.at < 15000)) return;
      last = { window: shown, at: now };
      fetch("/api/push/presence", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ endpoint: sub.endpoint, window: shown }),
      }).catch(() => {});
      if (shown) {
        const reg = await registration;
        const open = reg ? await reg.getNotifications({ tag: `mo-${shown}` }) : [];
        for (const n of open) n.close();
      }
    };
    setInterval(tick, 5000);
    for (const ev of ["focus", "blur", "popstate"]) window.addEventListener(ev, tick);
    document.addEventListener("visibilitychange", tick);
    tick();
  }

  (async () => {
    try {
      const res = await fetch("/api/push");
      info = res.ok ? await res.json() : { enabled: false };
    } catch {
      info = { enabled: false };
    }
    if (!info.enabled) return;
    for (const btn of document.querySelectorAll("[data-notify-settings]")) {
      btn.hidden = false;
      btn.addEventListener("click", () => openDialog());
    }
    if (supported && location.pathname === "/chat") startPresence();
  })();

  return { openDialog };
})();
