// Tests for static/sw.js's pure parts, run by `node --test`. The worker's
// event listeners are only attached inside a service worker.
const test = require("node:test");
const assert = require("node:assert/strict");
const sw = require("../static/sw.js");

// fakeCaches is an in-memory CacheStorage keyed by URL path.
function fakeCaches() {
  const stores = new Map();
  const keyOf = (req) => new URL(typeof req === "string" ? req : req.url, "https://o.test").pathname;
  const open = async (name) => {
    if (!stores.has(name)) stores.set(name, new Map());
    const m = stores.get(name);
    return {
      match: async (req) => (m.has(keyOf(req)) ? m.get(keyOf(req)).clone() : undefined),
      put: async (req, res) => { m.set(keyOf(req), res.clone()); },
      keys: async () => [...m.keys()],
    };
  };
  return {
    open,
    keys: async () => [...stores.keys()],
    delete: async (name) => stores.delete(name),
    has: async (name) => stores.has(name),
  };
}

function core(release, extra = {}) {
  return {
    release,
    hash: `h${release}`,
    sw: sw.SW_VERSION,
    manifest: 1,
    protocol: 1,
    entry: "/hub",
    files: {
      "/hub": { type: "text/html; charset=utf-8", body: `<p>core ${release}</p>` },
      "/hub/hub.js": { type: "text/javascript; charset=utf-8", body: `// ${release}` },
    },
    ...extra,
  };
}

const ORIGIN = "https://o.test";
const req = (path, init = {}) => new Request(ORIGIN + path, init);
const nav = (path) => ({ url: ORIGIN + path, method: "GET", mode: "navigate", headers: new Headers() });

function env(caches, fetchImpl) {
  return {
    caches,
    origin: ORIGIN,
    timeoutMs: 5000,
    fetch: fetchImpl || (async () => { throw new TypeError("offline"); }),
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (t) => clearTimeout(t),
  };
}

async function state(e) {
  const res = await sw.handleFetch(req("/__mo/state"), e);
  return res.json();
}

async function install(e, c) {
  return sw.handleFetch(req("/__mo/install", { method: "POST", body: JSON.stringify(c) }), e);
}

test("install sets the first core as current and installed", async () => {
  const e = env(fakeCaches());
  assert.equal((await install(e, core(1))).status, 200);
  const st = await state(e);
  assert.equal(st.sw, sw.SW_VERSION);
  assert.equal(st.pointer.current.release, 1);
  assert.equal(st.pointer.installed, 1);
  assert.deepEqual(st.pointer.current.files.sort(), ["/hub", "/hub/hub.js"]);
  const res = await sw.handleFetch(req("/hub/hub.js"), e);
  assert.equal(await res.text(), "// 1");
  assert.match(res.headers.get("Content-Type"), /javascript/);
});

test("install refuses malformed, not newer, and bad cores", async () => {
  const e = env(fakeCaches());
  await install(e, core(2));
  for (const bad of [
    core(1), core(2), { ...core(3), release: "3" }, { ...core(3), entry: "/missing" },
    { ...core(3), files: {} }, { ...core(3), files: { "/hub": { type: "text/html", body: 5 } } },
    { ...core(3), files: { "hub": { type: "text/html", body: "" }, "/hub": { type: "text/html", body: "" } } },
    { ...core(3), files: { "/__mo/state": { type: "text/html", body: "" }, "/hub": { type: "text/html", body: "" } } },
    { ...core(3), files: { "/theme.js": { type: "text/javascript", body: "" }, "/hub": { type: "text/html", body: "" } } },
  ]) {
    assert.equal((await install(e, bad)).status, 409, JSON.stringify(bad).slice(0, 80));
  }
  const notJSON = await sw.handleFetch(req("/__mo/install", { method: "POST", body: "{" }), e);
  assert.equal(notJSON.status, 400);
  assert.equal((await state(e)).pointer.current.release, 2);
});

test("a navigation goes to the network, and falls back to the core's entry", async () => {
  const caches = fakeCaches();
  const page = new Response("live");
  let e = env(caches, async () => page);
  await install(e, core(1));
  assert.equal(await (await sw.handleFetch(nav("/chat?window=@3"), e)).text(), "live");

  e = env(caches); // offline
  const res = await sw.handleFetch(nav("/chat?window=@3"), e);
  assert.equal(await res.text(), "<p>core 1</p>");
});

test("a navigation that hangs falls back after the timeout", async () => {
  const caches = fakeCaches();
  const e = { ...env(caches, () => new Promise(() => {})), timeoutMs: 20 };
  await install(env(caches), core(1));
  const res = await sw.handleFetch(nav("/"), e);
  assert.equal(await res.text(), "<p>core 1</p>");
});

test("with no core, a failed navigation gets the recovery page", async () => {
  const res = await sw.handleFetch(nav("/"), env(fakeCaches()));
  assert.equal(res.status, 200);
  const html = await res.text();
  assert.match(html, /Reset launcher/);
  assert.match(html, /<input/);
});

test("other requests are left alone", async () => {
  const e = env(fakeCaches());
  await install(e, core(1));
  assert.equal(await sw.handleFetch(req("/api/state"), e), null);
  assert.equal(await sw.handleFetch(req("/hub/hub.js", { method: "POST", body: "x" }), e), null);
  assert.equal(await sw.handleFetch(new Request("https://elsewhere.test/hub/hub.js"), e), null);
});

test("an unconfirmed core is rolled back after three tries; booted confirms", async () => {
  const caches = fakeCaches();
  const e = env(caches);
  await install(e, core(1));
  await sw.handleFetch(req("/__mo/booted", { method: "POST", body: JSON.stringify({ release: 1 }) }), e);
  await install(e, core(2));

  for (let i = 0; i < 3; i++) assert.equal(await (await sw.handleFetch(nav("/"), e)).text(), "<p>core 2</p>");
  assert.equal(await (await sw.handleFetch(nav("/"), e)).text(), "<p>core 1</p>", "fourth try rolls back");
  let st = await state(e);
  assert.equal(st.pointer.current.release, 1);
  assert.deepEqual(st.pointer.bad, [2]);
  assert.equal((await install(e, core(2))).status, 409, "a bad release is never installed again");

  // A core that boots stays.
  await install(e, core(3));
  await sw.handleFetch(nav("/"), e);
  await sw.handleFetch(req("/__mo/booted", { method: "POST", body: JSON.stringify({ release: 3 }) }), e);
  for (let i = 0; i < 5; i++) await sw.handleFetch(nav("/"), e);
  st = await state(e);
  assert.equal(st.pointer.current.release, 3);
  assert.equal(st.pointer.confirmed, true);
});

test("reset goes back to the installed core", async () => {
  const e = env(fakeCaches());
  await install(e, core(1));
  await install(e, core(4));
  const res = await sw.handleFetch(req("/__mo/reset", { method: "POST" }), e);
  assert.equal(res.status, 200);
  assert.equal((await state(e)).pointer.current.release, 1);
  assert.equal(await (await sw.handleFetch(req("/hub/hub.js"), e)).text(), "// 1");
});

test("cleanup keeps only current, previous and installed cores", async () => {
  const caches = fakeCaches();
  const e = env(caches);
  for (const r of [1, 2, 3, 4]) await install(e, core(r));
  await sw.cleanup(caches);
  const names = (await caches.keys()).filter((n) => n.startsWith("mo-core-")).sort();
  assert.deepEqual(names, ["mo-core-1", "mo-core-3", "mo-core-4"]);
});
