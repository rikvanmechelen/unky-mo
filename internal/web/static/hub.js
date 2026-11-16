// The launcher (docs/plans/pwa.md): finds which of your machines answers
// on this network and opens it. It's the installed app's start page and,
// through the service worker, what any page shows when its address doesn't
// answer. Part of the offline core, so it uses nothing outside /hub.
// The logic is in hub-model.js.

(() => {
  let storage = null;
  try {
    storage = window.localStorage;
  } catch {
    storage = { getItem: () => null, setItem: () => {} };
  }
  let hub = loadHub(storage);
  const params = new URLSearchParams(location.search);
  const stay = params.has("stay");
  // The installed app shows another address in a frame, so it never leaves
  // its own origin (docs/plans/pwa.md step 4). ?embed=1 tries it in a tab.
  const standalone = window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
  const shell = standalone || params.has("embed");
  let target = safeNext(params.get("next")) || targetPath(location);
  // A notification delivered through the shell names its machine
  // (?machine=<id>&next=<page>, docs/plans/pwa.md step 6).
  const wanted = requestedMachine(params, hub);
  if (wanted) {
    target = wanted.next;
    hub = { ...hub, last: wanted.id };
  }
  const env = {
    fetch: (u, o) => fetch(u, o),
    setTimeout: (f, ms) => setTimeout(f, ms),
    clearTimeout: (t) => clearTimeout(t),
    timeoutMs: 2500,
    headStartMs: 600,
  };
  const found = {}; // machine id -> url that answered, null when none did
  let ownUp = false; // this address answered
  const probing = new Set();

  const $ = (id) => document.getElementById(id);
  const save = () => saveHub(storage, hub);
  const setStatus = (text) => { $("hub-status").textContent = text; };

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  // pageAt is the page to open on another address: the launcher itself
  // stands in for the page that was asked for.
  const pageAt = (url) => url + target;

  function note(text, action) {
    const box = $("hub-note");
    box.textContent = "";
    if (!text) return;
    const p = el("p", "hub__note", text);
    if (action) {
      const b = el("button", "btn btn--quiet", action.label);
      b.type = "button";
      b.addEventListener("click", action.run);
      p.append(b);
    }
    box.append(p);
  }

  function render() {
    const list = $("machines");
    list.textContent = "";
    const ids = Object.keys(hub.machines);
    if (!ids.length) {
      list.append(el("li", "machine__empty", "No machines yet. Open Unky Mo once from a machine's address, or add one below."));
      return;
    }
    ids.sort((a, b) => (hub.machines[a].name || a).localeCompare(hub.machines[b].name || b));
    for (const id of ids) {
      const m = hub.machines[id];
      const up = found[id];
      const best = up || (orderCandidates(m)[0] || {}).url || "";
      const li = el("li", "machine");
      const dot = el("span", "machine__dot" + (up ? " is-up" : probing.has(id) ? " is-probing" : ""));
      dot.setAttribute("aria-hidden", "true");
      const text = el("div", "machine__text");
      text.append(el("div", "machine__name", m.name || id.slice(0, 8)));
      const state = up ? best : probing.has(id) ? "looking…" : found[id] === null ? `not answering (${m.candidates.length} address${m.candidates.length === 1 ? "" : "es"} tried)` : best;
      text.append(el("div", "machine__addr", state));
      const open = el("button", up ? "btn" : "btn btn--quiet", up ? "Open" : "Try");
      open.type = "button";
      open.disabled = !best;
      // "Try" opens the best address directly, so a certificate this device
      // doesn't trust shows the browser's own error.
      open.addEventListener("click", () => (up ? openMachine(id, best) : (location.href = pageAt(best))));
      li.append(dot, text, open);
      list.append(li);
    }
  }

  async function learnAddrs(id) {
    try {
      const res = await fetch("/api/hub/addrs", { cache: "no-store" });
      if (!res.ok) return;
      const body = await res.json();
      hub = mergeAddrs(hub, id, body.addrs);
      save();
    } catch {
      // not logged in yet, or offline: the addresses come next time
    }
  }

  // installCore hands a newer core to the worker; it runs from the next
  // start. Reports whether it was installed. trusted is true only for the
  // app's own address, or after the person said to (see fromMachine).
  async function installCore(core, trusted) {
    if (!coreUpdate({ core, running: CORE_RELEASE, trusted })) return false;
    try {
      const put = await fetch("/__mo/install", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(core) });
      return put.ok;
    } catch {
      return false;
    }
  }

  async function fetchCore(base) {
    try {
      const res = await fetch(base + "/api/hub/core", { cache: "no-store", credentials: "omit" });
      return res.ok ? await res.json() : null;
    } catch {
      return null;
    }
  }

  // checkCore takes this address's launcher when it's newer, and notes the
  // manifest this address serves: the browser refreshes the installed
  // manifest (and worker) only from here.
  async function checkCore() {
    const core = await fetchCore("");
    if (!core) return false;
    try {
      storage.setItem("mo.hub.base", JSON.stringify({ manifest: core.manifest }));
    } catch {
      // the base warning then assumes the oldest manifest
    }
    if ("serviceWorker" in navigator) navigator.serviceWorker.getRegistration().then((r) => r && r.update()).catch(() => {});
    return installCore(core, true);
  }

  // fromMachine runs once another machine is embedded: offer its launcher
  // if newer, and say when its base is newer than this app's. The launcher
  // runs at this app's address, which holds every machine's token, so code
  // from another machine is never installed without the person saying so:
  // a machine that was taken over would otherwise take over the app.
  async function fromMachine(base, name) {
    const core = await fetchCore(base);
    if (!core) return;
    if (coreUpdate({ core, running: CORE_RELEASE, trusted: true })) {
      note(`${name} has a newer launcher. Install it only if you trust ${name}: it would run for every machine in this app.`, {
        label: "Install",
        run: async () => {
          if (await installCore(core, true)) note(`The launcher was updated from ${name}.`, { label: "Reload", run: () => location.reload() });
          else note(`The launcher from ${name} couldn't be installed.`);
        },
      });
    }
    let installed = null;
    try {
      const st = await (await fetch("/__mo/state")).json();
      let manifest = 1;
      try {
        manifest = JSON.parse(storage.getItem("mo.hub.base") || "{}").manifest || 1;
      } catch {
        manifest = 1;
      }
      installed = { sw: st.sw, manifest };
    } catch {
      return; // no worker: nothing installed to be old
    }
    let dismissed = null;
    try {
      dismissed = storage.getItem("mo.hub.baseDismissed");
    } catch {
      dismissed = null;
    }
    if (ownUp && core.sw > installed.sw && "serviceWorker" in navigator) {
      navigator.serviceWorker.getRegistration().then((r) => r && r.update()).catch(() => {});
    }
    const w = baseWarning({ installed, offered: { sw: core.sw, manifest: core.manifest }, ownAnswers: ownUp, dismissed });
    if (w) showBaseWarning(w.key, name, base);
  }

  function showBaseWarning(key, name, base) {
    const bar = $("hub-base");
    bar.textContent = "";
    bar.append(el("span", "", `This app's base is older than ${name}'s. Open it once from ${location.origin}, or reinstall it from ${base}.`));
    const b = el("button", "btn btn--quiet", "Dismiss");
    b.type = "button";
    b.addEventListener("click", () => {
      try {
        storage.setItem("mo.hub.baseDismissed", key);
      } catch {
        // shows again next start
      }
      bar.hidden = true;
    });
    bar.append(b);
    bar.hidden = false;
  }

  function go(url) {
    setStatus(`Opening ${url}…`);
    location.replace(url);
  }

  // openMachine opens the page on a machine found at url: in the shell's
  // frame, or directly.
  function openMachine(id, url, path = target) {
    if (shouldEmbed({ shell, url, own: location.origin })) return embedMachine(id, url, path);
    return go(url + path);
  }

  // ── The shell's frame ───────────────────────────────────────────────
  let frame = null;
  let frameOrigin = null;
  let frameMachine = null;
  let framePath = target;
  let relocating = false;

  function closeFrame() {
    if (frame) frame.remove();
    frame = null;
    frameOrigin = null;
    document.body.classList.remove("is-embedded");
    document.title = "Unky Mo";
  }

  async function embedMachine(id, base, path) {
    const name = (hub.machines[id] && hub.machines[id].name) || "this machine";
    const again = () => embedMachine(id, base, framePath);
    // Nothing goes to an address before it says it's this machine.
    const hello = await probe(base, env);
    if (!hello) {
      setStatus(`${name} doesn't answer at ${base}.`);
      return;
    }
    if (hello.machine !== id) {
      setStatus(`Another machine answers at ${base}.`);
      return;
    }
    const proto = protocolCheck(hello.protocol);
    if (proto !== "ok") {
      setStatus(proto === "machine-old" ? `Update mo on ${name}: it's too old for this launcher.` : `This launcher is too old for ${name}. Open ${base} once to update it.`);
      return;
    }
    const token = loadTokens(storage)[id];
    if (!token) return showLogin({ id, name, base, done: again });
    let check = null;
    try {
      check = await fetch(base + "/api/hub/whoami", { headers: { Authorization: "Bearer " + token }, cache: "no-store" });
    } catch {
      check = null;
    }
    if (check && check.status === 401) {
      dropToken(storage, id);
      return showLogin({ id, name, base, done: again });
    }
    if (!check || !check.ok) {
      setStatus(`${name} doesn't answer at ${base}.`);
      return;
    }
    closeFrame();
    framePath = path;
    frameMachine = id;
    frameOrigin = new URL(base).origin;
    hub = { ...hub, last: id };
    save();
    frame = el("iframe", "hub-frame");
    frame.title = name;
    frame.allow = "clipboard-read; clipboard-write; fullscreen";
    frame.src = base + path;
    document.body.append(frame);
    document.body.classList.add("is-embedded");
    fromMachine(base, name);
  }

  // relocate finds the framed machine again after it stopped answering
  // (another network), and reopens the same page at its new address.
  async function relocate() {
    if (relocating || !frame) return;
    relocating = true;
    const id = frameMachine;
    const hello = await probe(frameOrigin, env);
    if (hello && hello.machine === id) {
      relocating = false; // a hiccup: the address still answers
      return;
    }
    const url = await findMachine(id, hub.machines[id], env);
    relocating = false;
    if (url) {
      hub = learnMachine(hub, { machine: id }, url, Date.now());
      save();
      embedMachine(id, url, framePath);
      return;
    }
    closeFrame();
    found[id] = null;
    render();
    setStatus(`${hub.machines[id].name || "The machine"} stopped answering on this network.`);
  }

  window.addEventListener("message", (e) => {
    if (!frame) return;
    const m = frameMessage(e, frame.contentWindow, frameOrigin);
    if (!m) return;
    const id = frameMachine;
    switch (m.type) {
      case "mo-ready": {
        const token = loadTokens(storage)[id];
        if (token) frame.contentWindow.postMessage({ type: "mo-auth", token }, frameOrigin);
        break;
      }
      case "mo-path":
        framePath = m.path;
        if (m.title) document.title = m.title;
        presenceTick();
        break;
      case "mo-notify":
        openNotify(id, frameOrigin);
        break;
      case "mo-unauthorized": {
        const base = frameOrigin;
        dropToken(storage, id);
        closeFrame();
        showLogin({ id, name: hub.machines[id].name, base, done: () => embedMachine(id, base, framePath) });
        break;
      }
      case "mo-machines":
        closeFrame();
        setStatus("Your machines.");
        render();
        break;
      case "mo-lost":
        relocate();
        break;
    }
  });

  async function findAll() {
    const ids = Object.keys(hub.machines);
    if (!ids.length) {
      setStatus("This address doesn't answer, and no other machine is known yet.");
      return;
    }
    setStatus("This address doesn't answer. Looking for your machines…");
    ids.forEach((id) => probing.add(id));
    render();
    let left = ids.length;
    let decided = false; // act on the first decision only: later answers only update the list
    await new Promise((resolve) => {
      for (const id of ids) {
        findMachine(id, hub.machines[id], env).then((url) => {
          probing.delete(id);
          found[id] = url;
          const now = Date.now();
          if (url) hub = learnMachine(hub, { machine: id }, url, now);
          save();
          render();
          const d = forwardDecision({ own: null, stay, found, hub, origin: location.origin });
          if (!decided && d.kind === "go") {
            decided = true;
            openMachine(hub.last, d.url);
          } else if (!decided && d.kind === "own") {
            // This address answered after all, only too slowly the first
            // time: continue as if it had (login form, page, retry guard).
            decided = true;
            ownUp = true;
            hereAnswers({ machine: hub.last, name: hub.machines[hub.last].name });
          }
          if (--left === 0) resolve();
        });
      }
    });
    if (decided && ownUp) return;
    const up = ids.filter((id) => found[id]).length;
    setStatus(up ? `${up} of ${ids.length} machine${ids.length === 1 ? "" : "s"} answer${up === 1 ? "s" : ""} here.` : "None of your machines answer on this network. Add an address below.");
  }

  // whoami says whether this browser is logged in here (loginState), with
  // the status for the "error" case.
  async function whoami() {
    try {
      const res = await fetch("/api/hub/whoami", { cache: "no-store" });
      return { state: loginState(res.status), status: res.status };
    } catch {
      return { state: "error", status: 0 };
    }
  }

  // ── Notifications through the shell ─────────────────────────────────
  // One worker registration per machine, subscribed with that machine's
  // key; the machine keeps the subscription as for any device.
  const pushSupported = window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

  function machineFetch(id, base, path, method = "GET", body) {
    const headers = { Authorization: "Bearer " + (loadTokens(storage)[id] || "") };
    if (body) headers["Content-Type"] = "application/json";
    return fetch(base + path, { method, headers, body: body ? JSON.stringify(body) : undefined, credentials: "omit", cache: "no-store" });
  }

  async function machineJSON(id, base, path, method, body) {
    const res = await machineFetch(id, base, path, method, body);
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `request failed (${res.status})`);
    return data;
  }

  async function pushRegistration(id, create) {
    const scope = new URL(pushScope(id), location.origin).href;
    let reg = await navigator.serviceWorker.getRegistration(scope);
    if (reg && reg.scope !== scope) reg = null; // the app's own registration
    if (!reg && create) reg = await navigator.serviceWorker.register("/sw.js", { scope: pushScope(id) });
    if (!reg) return null;
    const worker = reg.installing || reg.waiting;
    if (!reg.active && worker) {
      await new Promise((resolve) => worker.addEventListener("statechange", () => worker.state === "activated" && resolve()));
    }
    return reg;
  }

  async function pushSubscription(id) {
    const reg = await pushRegistration(id, false);
    return reg ? reg.pushManager.getSubscription() : null;
  }

  // pushState: "on" (with the machine's entry), "off", "blocked",
  // "unsupported", or "disabled" (the machine has push off).
  async function pushState(id, base) {
    if (!pushSupported) return { kind: "unsupported" };
    const info = await machineJSON(id, base, "/api/push");
    if (!info.enabled) return { kind: "disabled" };
    if (Notification.permission === "denied") return { kind: "blocked", info };
    const sub = await pushSubscription(id);
    if (!sub || !sameKeyBytes(sub.options.applicationServerKey, info.publicKey)) return { kind: "off", info };
    const entry = await machineJSON(id, base, `/api/push/subscriptions?endpoint=${encodeURIComponent(sub.endpoint)}`);
    return entry.subscribed ? { kind: "on", info, events: entry.events, sub } : { kind: "off", info };
  }

  async function pushOn(id, base, info, events) {
    if ((await Notification.requestPermission()) !== "granted") throw new Error("Notifications aren't allowed for this app.");
    const reg = await pushRegistration(id, true);
    let sub = await reg.pushManager.getSubscription();
    if (sub && !sameKeyBytes(sub.options.applicationServerKey, info.publicKey)) {
      await sub.unsubscribe(); // made with an older key of this machine
      sub = null;
    }
    if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: keyBytes(info.publicKey) });
    await machineJSON(id, base, "/api/push/subscriptions", "POST", { subscription: sub.toJSON(), events, label: hubDeviceLabel(navigator.userAgent, standalone) });
  }

  async function pushOff(id, base) {
    const sub = await pushSubscription(id);
    if (!sub) return;
    await machineJSON(id, base, "/api/push/subscriptions", "DELETE", { endpoint: sub.endpoint });
    await sub.unsubscribe();
  }

  const PUSH_TEXT = {
    on: "On for this device.",
    off: "Off for this device.",
    blocked: "Notifications are blocked for this app. Allow them in the system or browser settings, then come back here.",
    unsupported: "This app can't receive push notifications here.",
    disabled: "This machine has notifications turned off ([web] disable_push).",
  };

  async function openNotify(id, base) {
    const dialog = $("notify-dialog");
    const name = hub.machines[id].name || "this machine";
    const render = async (msg) => {
      let st;
      try {
        st = await pushState(id, base);
      } catch (e) {
        st = { kind: "off", error: e.message };
      }
      dialog.textContent = "";
      dialog.append(el("h2", "hub__heading", `Notifications from ${name}`));
      dialog.append(el("p", "hub-dialog__text", "Get a notification when a session on this machine needs you, even with the app closed. Tapping it opens the session."));
      dialog.append(el("p", "hub-dialog__state", PUSH_TEXT[st.kind] || ""));
      const row = el("div", "hub-dialog__actions");
      const button = (label, cls, fn) => {
        const b = el("button", "btn" + (cls ? " " + cls : ""), label);
        b.type = "button";
        b.addEventListener("click", async () => {
          for (const x of row.querySelectorAll("button")) x.disabled = true;
          try {
            await render({ ok: await fn() });
          } catch (e) {
            await render({ error: e.message });
          }
        });
        row.append(b);
      };
      if (st.kind === "on" || st.kind === "off") {
        const events = st.events || { input: true, done: false };
        const check = (label, on) => {
          const l = el("label", "hub-dialog__check");
          const i = document.createElement("input");
          i.type = "checkbox";
          i.checked = on;
          l.append(i, el("span", "", label));
          dialog.append(l);
          return i;
        };
        const input = check("A session needs input (a question or a permission prompt)", events.input);
        const done = check("A session is done (turns longer than 15 s)", events.done);
        const chosen = () => ({ input: input.checked, done: done.checked });
        if (st.kind === "on") {
          button("Send test", "btn--quiet", async () => {
            await machineJSON(id, base, "/api/push/test", "POST", { endpoint: st.sub.endpoint });
            return "Sent. It should show up in a few seconds.";
          });
          button("Turn off", "btn--quiet", async () => { await pushOff(id, base); return "Turned off."; });
          button("Save", "", async () => { await pushOn(id, base, st.info, chosen()); return "Saved."; });
        } else {
          button("Turn on", "", async () => { await pushOn(id, base, st.info, chosen()); return "Turned on. Send a test to check it."; });
        }
      }
      const close = el("button", "btn btn--quiet", "Close");
      close.type = "button";
      close.addEventListener("click", () => dialog.close());
      row.prepend(close);
      const text = (msg && (msg.ok || msg.error)) || st.error;
      if (text) dialog.append(el("p", msg && msg.ok ? "hub-dialog__ok" : "add__error", text));
      dialog.append(row);
    };
    if (!dialog.open) dialog.showModal();
    await render();
  }

  // Presence: while the frame shows a session and the app is in front,
  // tell that machine every 15 s, so this device isn't buzzed about the
  // session it's looking at.
  let presence = { machine: null, window: "", at: 0 };
  async function presenceTick() {
    if (!pushSupported || !frame) return;
    const id = frameMachine;
    const base = frameOrigin;
    const shown = document.visibilityState === "visible"
      ? new URLSearchParams(framePath.split("?")[1] || "").get("window") || ""
      : "";
    const now = Date.now();
    if (presence.machine === id && shown === presence.window && (shown === "" || now - presence.at < 15000)) return;
    const sub = await pushSubscription(id).catch(() => null);
    if (!sub) return;
    presence = { machine: id, window: shown, at: now };
    machineFetch(id, base, "/api/push/presence", "POST", { endpoint: sub.endpoint, window: shown }).catch(() => {});
    if (shown) {
      const reg = await pushRegistration(id, false);
      const tag = `${id.slice(0, 8)}-mo-${shown}`;
      for (const n of reg ? await reg.getNotifications({ tag }) : []) n.close();
    }
  }
  setInterval(presenceTick, 5000);
  document.addEventListener("visibilitychange", presenceTick);

  // showLogin asks for the login of a machine: this address's (base "",
  // which sets the cookie) or another one the shell frames (which answers
  // with a token the shell keeps).
  function showLogin({ id, name, base, done }) {
    name = name || "this machine";
    setStatus(`Log in to ${name}.`);
    $("login").hidden = false;
    $("login-heading").textContent = `Log in to ${name}`;
    if (!$("login-label").value) $("login-label").value = hubDeviceLabel(navigator.userAgent, standalone);
    $("login-user").focus();
    $("login-form").onsubmit = async (e) => {
      e.preventDefault();
      const err = $("login-error");
      err.textContent = "";
      const body = { username: $("login-user").value.trim(), password: $("login-pass").value, label: $("login-label").value.trim() };
      let res;
      try {
        res = await fetch(base + "/api/hub/pair", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      } catch {
        err.textContent = "That address stopped answering.";
        return;
      }
      $("login-pass").value = "";
      if (res.status === 401) err.textContent = "Wrong username or password.";
      else if (res.status === 429) err.textContent = "Too many attempts. Wait a minute and try again.";
      else if (!res.ok) err.textContent = (await res.json().catch(() => ({}))).error || `Logging in failed (${res.status}).`;
      else {
        if (base) saveToken(storage, id, (await res.json()).token);
        $("login").hidden = true;
        done();
      }
    };
  }

  // openHere continues on this address once it answers and the browser is
  // logged in: learn the machine's addresses, update the launcher, then
  // open the page.
  async function openHere(own) {
    await learnAddrs(own.machine);
    const updated = await checkCore();
    render();

    const d = forwardDecision({ own, stay, found, hub });
    if (d.kind !== "own") {
      setStatus(`This is ${own.name || "your machine"}.`);
      if (updated) note("The launcher was updated.", { label: "Reload", run: () => location.reload() });
      return;
    }
    if (location.pathname === "/hub") return go(target);
    // The worker showed the launcher because this page didn't load in
    // time, yet the address answers now: try the page once more, then let
    // the person decide, so a slow page can't loop.
    const key = "mo.hub.retried:" + target;
    let retried = false;
    try {
      retried = sessionStorage.getItem(key) === "1";
      sessionStorage.setItem(key, "1");
    } catch {
      retried = true;
    }
    if (!retried) return location.reload();
    setStatus(`${own.name || "This machine"} answers, but the page didn't load in time.`);
    note("Try again?", { label: "Retry", run: () => location.reload() });
  }

  async function main() {
    render();
    const own = await probe(location.origin, env);
    if (!own) return findAll();

    ownUp = true;
    hub = learnMachine(hub, own, location.origin, Date.now());
    found[own.machine] = location.origin;
    save();
    render();
    if (wanted && wanted.id !== own.machine) {
      // A notification from another machine: find it and open the page.
      hub = { ...hub, last: wanted.id };
      probing.add(wanted.id);
      render();
      const url = await findMachine(wanted.id, hub.machines[wanted.id], env);
      probing.delete(wanted.id);
      found[wanted.id] = url;
      render();
      if (url) return openMachine(wanted.id, url, wanted.next);
      setStatus(`${hub.machines[wanted.id].name || "That machine"} doesn't answer on this network.`);
      return;
    }
    return hereAnswers(own);
  }

  // hereAnswers carries on at this address once it answered: the login
  // form when this browser isn't logged in, else the page.
  async function hereAnswers(own) {
    const who = await whoami();
    if (who.state === "out") return showLogin({ id: own.machine, name: own.name, base: "", done: () => openHere(own) });
    if (who.state === "error") {
      const name = own.name || "This machine";
      setStatus(who.status === 429
        ? `${name} refused this browser for a minute after failed logins. It may be sending an old password it remembered: clear this site's data, or restart the browser.`
        : `${name} answers, but checking the login failed${who.status ? ` (${who.status})` : ""}.`);
      note("Try again?", { label: "Retry", run: () => location.reload() });
      return;
    }
    return openHere(own);
  }

  $("add").addEventListener("submit", async (e) => {
    e.preventDefault();
    const input = $("add-input");
    const err = $("add-error");
    err.textContent = "";
    const url = normalizeAddress(input.value);
    if (!url) {
      err.textContent = "That isn't an https address (e.g. laptop.local or 10.1.4.7:7890).";
      return;
    }
    err.textContent = `Asking ${url}…`;
    const hello = await probe(url, env);
    if (!hello) {
      err.textContent = `Nothing answers as Unky Mo at ${url}, or this device doesn't trust its certificate.`;
      return;
    }
    hub = addManual(hub, hello, url, Date.now());
    found[hello.machine] = url;
    save();
    render();
    err.textContent = "";
    input.value = "";
    setStatus(`Added ${hello.name || url}.`);
  });

  // The login form is only ever sent by showLogin's handler: a native
  // submit would put the password in the URL.
  $("login-form").addEventListener("submit", (e) => e.preventDefault());

  // Confirm this core to the worker, so it isn't rolled back.
  fetch("/__mo/booted", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ release: CORE_RELEASE }) }).catch(() => {});
  if (navigator.storage && navigator.storage.persist) navigator.storage.persist().catch(() => {});
  // Inside another shell's frame (a login redirect lands here) the shell
  // takes over: it shows its own login or list. No worker is registered
  // there, since it would answer the frame's navigations.
  if (window.top !== window) {
    window.parent.postMessage({ type: params.has("login") ? "mo-unauthorized" : "mo-machines" }, "*");
    setStatus("");
    return;
  }
  if ("serviceWorker" in navigator && window.isSecureContext) navigator.serviceWorker.register("/sw.js").catch(() => {});
  main();
})();
