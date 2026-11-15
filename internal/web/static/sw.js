// Service worker (docs/plans/pwa.md, docs/plans/notifications.md).
//
// It keeps the launcher (the "core") working offline and shows push
// notifications. It's deliberately dumb: the core's logic lives in the
// core, which can be updated from any machine, while this file can only be
// updated from the address the app was installed from. So the contract
// below (the pointer, the cache names, /__mo/*, the recovery page) only
// changes with a new SW_VERSION, and the app then asks for a reinstall.
//
// Pure functions over an injected `caches`/`fetch` so jstests/sw.test.js can
// run them in Node; the listeners are only attached inside a worker.

const SW_VERSION = 3;

const META = "mo-meta";
const POINTER = "/__mo/pointer";
const CORE_PREFIX = "mo-core-";
const MAX_TRIES = 3; // entry loads of an unconfirmed core before rollback

function emptyPointer() {
  return { current: null, previous: null, installed: null, tries: 0, confirmed: false, bad: [] };
}

async function readPointer(caches) {
  const meta = await caches.open(META);
  const res = await meta.match(POINTER);
  if (!res) return emptyPointer();
  try {
    return { ...emptyPointer(), ...(await res.json()) };
  } catch {
    return emptyPointer();
  }
}

async function writePointer(caches, p) {
  const meta = await caches.open(META);
  await meta.put(POINTER, jsonResponse(p));
}

function jsonResponse(v, status = 200) {
  return new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
}

// validCore checks a /api/hub/core document: an integer release, files
// under /hub (the core's namespace, so it never shadows a dashboard file)
// with string bodies, and an entry among them.
function validCore(core) {
  if (!core || typeof core !== "object" || !Number.isInteger(core.release) || core.release <= 0) return false;
  if (!core.files || typeof core.files !== "object") return false;
  const paths = Object.keys(core.files);
  if (!paths.length || !paths.includes(core.entry)) return false;
  return paths.every((p) => {
    const f = core.files[p];
    return isCorePath(p) && !/[?#]/.test(p) && f && typeof f.body === "string" && typeof f.type === "string";
  });
}

function isCorePath(path) {
  return path === "/hub" || path.startsWith("/hub/");
}

async function installCore(caches, core) {
  if (!validCore(core)) return jsonResponse({ error: "not a core" }, 409);
  const p = await readPointer(caches);
  if (p.bad.includes(core.release)) return jsonResponse({ error: "release marked bad" }, 409);
  if (p.current && core.release <= p.current.release) return jsonResponse({ error: "not newer" }, 409);

  const cache = await caches.open(CORE_PREFIX + core.release);
  for (const [path, f] of Object.entries(core.files)) {
    await cache.put(path, new Response(f.body, { headers: { "Content-Type": f.type } }));
  }
  const entry = { release: core.release, entry: core.entry, files: Object.keys(core.files) };
  const first = !p.current;
  await writePointer(caches, {
    ...p,
    previous: p.current,
    current: entry,
    installed: p.installed || core.release,
    tries: 0,
    confirmed: first,
  });
  return jsonResponse({ release: core.release });
}

async function coreFile(caches, core, path) {
  const cache = await caches.open(CORE_PREFIX + core.release);
  return (await cache.match(path)) || null;
}

// entryPage answers a navigation the network couldn't: the current core's
// entry, counting a try while it's unconfirmed and rolling back to the
// previous core after MAX_TRIES. With no core, the recovery page.
async function entryPage(caches) {
  let p = await readPointer(caches);
  if (p.current && !p.confirmed) {
    if (p.tries >= MAX_TRIES && p.previous) {
      p = { ...p, bad: [...p.bad, p.current.release], current: p.previous, previous: null, tries: 0, confirmed: true };
    } else {
      p = { ...p, tries: p.tries + 1 };
    }
    await writePointer(caches, p);
  }
  if (p.current) {
    const res = await coreFile(caches, p.current, p.current.entry);
    if (res) return res;
  }
  return recoveryPage();
}

function recoveryPage() {
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>Unky Mo</title>
<style>body{font:16px system-ui,sans-serif;margin:0;padding:24px 16px;background:#fff;color:#111}
@media (prefers-color-scheme:dark){body{background:#111;color:#eee}}
form{display:flex;gap:8px;margin:16px 0}input{flex:1;font:inherit;padding:8px}button{font:inherit;padding:8px 12px}</style>
</head><body><h1>Unky Mo</h1>
<p>This address doesn't answer, and the app has no saved launcher.</p>
<form id="go"><input id="addr" placeholder="laptop.local or 10.1.4.7:7890" autocomplete="off" autocapitalize="off"><button>Open</button></form>
<button id="reset" type="button">Reset launcher</button>
<script>
document.getElementById("go").addEventListener("submit", function (e) {
  e.preventDefault();
  var v = document.getElementById("addr").value.trim();
  if (!v) return;
  if (!/^[a-z]+:\\/\\//i.test(v)) v = "https://" + v + (/:\\d+$/.test(v) ? "" : ":7890");
  // Only a web address (http just for this machine), like the launcher's own rule.
  if (!/^https:\\/\\//i.test(v) && !/^http:\\/\\/(localhost|127\\.0\\.0\\.1)(:|\\/|$)/i.test(v)) return;
  location.href = v + "/hub?stay=1";
});
document.getElementById("reset").addEventListener("click", function () {
  fetch("/__mo/reset", { method: "POST" }).then(function () { location.reload(); });
});
</script></body></html>`;
  return new Response(html, { headers: { "Content-Type": "text/html; charset=utf-8" } });
}

async function api(request, path, env) {
  const { caches } = env;
  if (request.method === "GET" && path === "/__mo/state") {
    return jsonResponse({ sw: SW_VERSION, pointer: await readPointer(caches) });
  }
  if (request.method !== "POST") return jsonResponse({ error: "not found" }, 404);
  if (path === "/__mo/install") {
    // Only the launcher installs a core. A dashboard page shares this
    // origin, so without the check a script there could make itself the
    // launcher for good.
    const from = env.clientPath ? await env.clientPath() : null;
    if (from === null || !(from === "/hub" || from.startsWith("/hub/"))) {
      return jsonResponse({ error: "only the launcher installs a core" }, 403);
    }
    let core;
    try {
      core = await request.json();
    } catch {
      return jsonResponse({ error: "bad JSON" }, 400);
    }
    return installCore(caches, core);
  }
  if (path === "/__mo/booted") {
    const body = await request.json().catch(() => ({}));
    const p = await readPointer(caches);
    if (p.current && body.release === p.current.release) await writePointer(caches, { ...p, confirmed: true, tries: 0 });
    return jsonResponse({ ok: true });
  }
  if (path === "/__mo/reset") {
    const p = await readPointer(caches);
    if (p.installed && (await caches.has(CORE_PREFIX + p.installed))) {
      const cache = await caches.open(CORE_PREFIX + p.installed);
      const files = (await cache.keys()).map((k) => (typeof k === "string" ? k : new URL(k.url).pathname));
      const entry = files.includes("/hub") ? "/hub" : files[0];
      await writePointer(caches, { ...p, previous: null, current: { release: p.installed, entry, files }, tries: 0, confirmed: true });
    }
    return jsonResponse({ ok: true });
  }
  return jsonResponse({ error: "not found" }, 404);
}

// fetchWithTimeout rejects when the network takes longer than ms: a dead
// address on another network can hang until the TCP timeout.
function fetchWithTimeout(env, request, ms) {
  return new Promise((resolve, reject) => {
    const t = env.setTimeout(() => reject(new Error("timeout")), ms);
    env.fetch(request).then(
      (res) => { env.clearTimeout(t); resolve(res); },
      (err) => { env.clearTimeout(t); reject(err); },
    );
  });
}

// isTopNavigation is a navigation of the app's own window. A navigation
// inside a frame (the shell's iframe of another address, or this address
// framed by another shell) is never touched: the fallback is for the app's
// window, and re-fetching a frame's navigation here would lose what makes
// the server serve it to a frame.
function isTopNavigation(request) {
  return request.mode === "navigate" && (request.destination === "document" || request.destination === undefined || request.destination === "");
}

// handlesRequest says, without reading the cache, whether handleFetch can
// answer a request: the worker's own API, the core's files, and top-level
// navigations. Everything else never passes through the worker.
function handlesRequest(request) {
  const path = new URL(request.url).pathname;
  return isTopNavigation(request) || path.startsWith("/__mo/") || (request.method === "GET" && request.mode !== "navigate" && isCorePath(path));
}

// handleFetch answers a request, or returns null to let the browser handle
// it: /__mo/* is this worker's API, the current core's files come from its
// cache, and a navigation falls back to the core's entry when the network
// fails. Everything else (the API, SSE, the dashboard's files) is live.
async function handleFetch(request, env) {
  const url = new URL(request.url);
  if (url.origin !== env.origin) return null;
  if (url.pathname.startsWith("/__mo/")) return api(request, url.pathname, env);
  if (request.method !== "GET") return null;
  if (request.mode === "navigate" && !isTopNavigation(request)) return null;

  const p = await readPointer(env.caches);
  if (p.current && p.current.files.includes(url.pathname) && request.mode !== "navigate") {
    return coreFile(env.caches, p.current, url.pathname);
  }
  if (request.mode === "navigate") {
    if (p.current && url.pathname === p.current.entry) return entryPage(env.caches);
    try {
      return await fetchWithTimeout(env, request, env.timeoutMs);
    } catch {
      return entryPage(env.caches);
    }
  }
  return null;
}

// cleanup deletes core caches that are neither current, previous nor the
// installed one.
async function cleanup(caches) {
  const p = await readPointer(caches);
  const keep = new Set([p.current, p.previous].filter(Boolean).map((c) => CORE_PREFIX + c.release));
  if (p.installed) keep.add(CORE_PREFIX + p.installed);
  for (const name of await caches.keys()) {
    if (name.startsWith(CORE_PREFIX) && !keep.has(name)) await caches.delete(name);
  }
}

// machineFromScope is the machine a registration delivers pushes for: the
// shell registers one per machine under /hub/push/<id>/ (docs/plans/pwa.md
// step 6). null for the app's own registration.
function machineFromScope(scope) {
  const m = /\/hub\/push\/([0-9a-f]{32})\/$/.exec(new URL(scope).pathname);
  return m ? m[1] : null;
}

// notificationFor turns a push payload into showNotification's arguments.
// A machine's push (through the shell) opens the shell at that machine,
// and its tag is the machine's, so two machines' windows never replace
// each other. Never silent: "done" and "needs you" both buzz.
function notificationFor(msg, scope) {
  msg = msg && typeof msg === "object" ? msg : {};
  const path = typeof msg.url === "string" && msg.url.startsWith("/") && !msg.url.startsWith("//") ? msg.url : "/";
  const machine = machineFromScope(scope);
  const options = {
    body: typeof msg.body === "string" ? msg.body : "",
    icon: "/icons/icon-192.png",
    badge: "/icons/badge.png",
    data: { url: machine ? `/hub?machine=${machine}&next=${encodeURIComponent(path)}` : path, window: msg.window || "" },
  };
  if (msg.tag) {
    options.tag = machine ? `${machine.slice(0, 8)}-${msg.tag}` : String(msg.tag);
    options.renotify = true; // a replacing notification buzzes again
  }
  return { title: typeof msg.title === "string" && msg.title ? msg.title : "Unky Mo", options };
}

// installFromOrigin installs the own origin's core when the app has none.
async function installFromOrigin(env) {
  const p = await readPointer(env.caches);
  if (p.current) return;
  const res = await env.fetch(new Request("/api/hub/core", { cache: "no-store" }));
  if (res.ok) await installCore(env.caches, await res.json());
}

if (typeof module === "object" && module.exports) {
  module.exports = { SW_VERSION, handleFetch, handlesRequest, cleanup, installCore, readPointer, notificationFor, machineFromScope };
} else if (typeof ServiceWorkerGlobalScope !== "undefined" && self instanceof ServiceWorkerGlobalScope) {
  const env = () => ({
    caches: self.caches,
    origin: self.location.origin,
    timeoutMs: 5000,
    fetch: (r) => self.fetch(r),
    setTimeout: (fn, ms) => self.setTimeout(fn, ms),
    clearTimeout: (t) => self.clearTimeout(t),
  });

  self.addEventListener("install", (event) => {
    self.skipWaiting();
    event.waitUntil(installFromOrigin(env()).catch(() => {}));
  });
  self.addEventListener("activate", (event) => event.waitUntil(cleanup(self.caches).catch(() => {}).then(() => self.clients.claim())));

  self.addEventListener("fetch", (event) => {
    const url = new URL(event.request.url);
    if (url.origin !== self.location.origin) return;
    // Only these can ever be answered here; everything else isn't touched,
    // so the dashboard's polls and streams never pass through the worker.
    if (!handlesRequest(event.request)) return;
    const e = env();
    e.clientPath = async () => {
      const c = event.clientId ? await self.clients.get(event.clientId) : null;
      return c ? new URL(c.url).pathname : null;
    };
    event.respondWith((async () => (await handleFetch(event.request, e)) || self.fetch(event.request))());
  });

  self.addEventListener("push", (event) => {
    // Always show something: iOS revokes a subscription whose pushes show
    // nothing, and Chrome shows its own "updated in the background" instead.
    let msg = {};
    try {
      msg = event.data ? event.data.json() : {};
    } catch {
      msg = {};
    }
    const { title, options } = notificationFor(msg, self.registration.scope);
    event.waitUntil(self.registration.showNotification(title, options));
  });

  self.addEventListener("notificationclick", (event) => {
    event.notification.close();
    const url = new URL((event.notification.data && event.notification.data.url) || "/", self.location.origin);
    event.waitUntil((async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      const same = windows.find((c) => c.url === url.href);
      if (same) return same.focus();
      // Reuse only a window already showing the launcher or the same page
      // (another session's chat, say): navigating some other tab away could
      // lose what's open in it (review comments, a terminal line).
      for (const c of windows) {
        const at = new URL(c.url);
        if (at.origin !== url.origin || !("navigate" in c)) continue;
        if (!(at.pathname === url.pathname || at.pathname === "/hub" || at.pathname.startsWith("/hub/"))) continue;
        try {
          const nav = await c.navigate(url.href);
          if (nav) return nav.focus();
        } catch {
          // Not controlled by this worker (opened before it): try the next one.
        }
      }
      return self.clients.openWindow(url.href);
    })());
  });
}
