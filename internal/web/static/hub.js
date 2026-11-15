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
      // "Try" opens the best address anyway, so a certificate this device
      // doesn't trust shows the browser's own error.
      open.addEventListener("click", () => { location.href = pageAt(best); });
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
          if (d.kind === "go") go(pageAt(d.url));
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

  function showLogin(own) {
    setStatus(`Log in to ${own.name || "this machine"}.`);
    $("login").hidden = false;
    $("login-heading").textContent = `Log in to ${own.name || "this machine"}`;
    const standalone = window.matchMedia("(display-mode: standalone)").matches || navigator.standalone === true;
    if (!$("login-label").value) $("login-label").value = hubDeviceLabel(navigator.userAgent, standalone);
    $("login-user").focus();
    $("login-form").onsubmit = async (e) => {
      e.preventDefault();
      const err = $("login-error");
      err.textContent = "";
      const body = { username: $("login-user").value.trim(), password: $("login-pass").value, label: $("login-label").value.trim() };
      let res;
      try {
        res = await fetch("/api/hub/pair", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      } catch {
        err.textContent = "This address stopped answering.";
        return;
      }
      $("login-pass").value = "";
      if (res.status === 401) err.textContent = "Wrong username or password.";
      else if (res.status === 429) err.textContent = "Too many attempts. Wait a minute and try again.";
      else if (!res.ok) err.textContent = (await res.json().catch(() => ({}))).error || `Logging in failed (${res.status}).`;
      else {
        $("login").hidden = true;
        openHere(own);
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
    if ((await whoami()) === "out") return showLogin(own);
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

  // Confirm this core to the worker, so it isn't rolled back.
  fetch("/__mo/booted", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ release: CORE_RELEASE }) }).catch(() => {});
  if (navigator.storage && navigator.storage.persist) navigator.storage.persist().catch(() => {});
  if ("serviceWorker" in navigator && window.isSecureContext) navigator.serviceWorker.register("/sw.js").catch(() => {});
  main();
})();
