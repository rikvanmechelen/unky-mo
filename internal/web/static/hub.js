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
  const target = safeNext(params.get("next")) || targetPath(location);
  const env = {
    fetch: (u, o) => fetch(u, o),
    setTimeout: (f, ms) => setTimeout(f, ms),
    clearTimeout: (t) => clearTimeout(t),
    timeoutMs: 2500,
    headStartMs: 600,
  };
  const found = {}; // machine id -> url that answered, null when none did
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

  // checkCore installs this address's launcher when it's newer than the
  // one running. Takes effect on the next start.
  async function checkCore() {
    try {
      const res = await fetch("/api/hub/core", { cache: "no-store" });
      if (!res.ok) return false;
      const core = await res.json();
      if (!(core.release > CORE_RELEASE)) return false;
      const put = await fetch("/__mo/install", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(core) });
      return put.ok;
    } catch {
      return false;
    }
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
    await new Promise((resolve) => {
      for (const id of ids) {
        findMachine(id, hub.machines[id], env).then((url) => {
          probing.delete(id);
          found[id] = url;
          const now = Date.now();
          if (url) hub = learnMachine(hub, { machine: id }, url, now);
          save();
          render();
          const d = forwardDecision({ own: null, stay, found, hub });
          if (d.kind === "go") openMachine(hub.last, d.url);
          if (--left === 0) resolve();
        });
      }
    });
    const up = ids.filter((id) => found[id]).length;
    setStatus(up ? `${up} of ${ids.length} machine${ids.length === 1 ? "" : "s"} answer${up === 1 ? "s" : ""} here.` : "None of your machines answer on this network. Add an address below.");
  }

  // whoami says whether this browser is logged in here: "in", "out", or
  // "open" (no login configured on a localhost-only server).
  async function whoami() {
    try {
      const res = await fetch("/api/hub/whoami", { cache: "no-store" });
      if (res.status === 401) return "out";
      return res.ok ? "in" : "open";
    } catch {
      return "open";
    }
  }

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

    hub = learnMachine(hub, own, location.origin, Date.now());
    found[own.machine] = location.origin;
    save();
    render();
    if ((await whoami()) === "out") return showLogin({ id: own.machine, name: own.name, base: "", done: () => openHere(own) });
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
