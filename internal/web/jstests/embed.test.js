// Tests for static/embed.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createEmbed } = require("../static/embed.js");

const ORIGIN = "https://10.1.4.7:7890";
const SHELL = "https://192.168.1.20:7890";

function memStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}

// fakeWin is a window inside (or not inside) a frame. fetch answers from
// `routes` (url → status) and records requests.
function fakeWin({ embedded = true, routes = {}, storage = memStorage(), now = 1000 } = {}) {
  const listeners = {};
  const parent = { sent: [], postMessage(msg, target) { this.sent.push({ msg, target }); } };
  const requests = [];
  const win = {
    location: { origin: ORIGIN, pathname: "/chat", search: "?window=@1", hash: "" },
    document: { title: "Chat" },
    sessionStorage: storage,
    parent,
    addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
    setTimeout: (fn, ms) => setTimeout(fn, Math.min(ms, 20)),
    clearTimeout: (t) => clearTimeout(t),
    now: () => now,
    fetch: async (req) => {
      requests.push(req);
      const key = req.method + " " + new URL(req.url).pathname;
      const r = routes[key];
      if (r === "network") throw new TypeError("Failed to fetch");
      if (typeof r === "object") return new Response(JSON.stringify(r.body), { status: r.status || 200 });
      return new Response("{}", { status: r || 200 });
    },
    dispatch: async (type, ev) => { for (const fn of listeners[type] || []) await fn(ev); },
  };
  win.top = embedded ? {} : win;
  return { win, parent, requests, storage };
}

const ticketRoute = { "POST /api/hub/ticket": { body: { ticket: "TICKET", expires: "2099-01-01T00:00:00Z" } } };

test("outside a frame nothing changes", async () => {
  const { win, requests } = fakeWin({ embedded: false });
  const e = createEmbed(win);
  assert.equal(e.embedded, false);
  await e.fetch(ORIGIN + "/api/state");
  assert.equal(requests[0].headers.get("Authorization"), null);
  assert.equal(e.authURL("/api/transcript/@1"), "/api/transcript/@1");
});

test("the handshake: ready asks the parent, auth only from the parent", async () => {
  const { win, parent, requests } = fakeWin({ routes: ticketRoute });
  const e = createEmbed(win);
  assert.deepEqual(parent.sent[0], { msg: { type: "mo-ready" }, target: "*" });

  // Someone else's message is ignored.
  await win.dispatch("message", { source: {}, origin: "https://evil.example", data: { type: "mo-auth", token: "EVIL" } });
  // The parent's is taken.
  await win.dispatch("message", { source: parent, origin: SHELL, data: { type: "mo-auth", token: "TOKEN" } });
  await e.ready;

  await e.fetch(ORIGIN + "/api/state");
  const last = requests.at(-1);
  assert.equal(last.headers.get("Authorization"), "Bearer TOKEN");
  assert.equal(requests[0].headers.get("Authorization"), "Bearer TOKEN", "the ticket request carries the token");

  // Another origin never gets the token.
  await e.fetch("https://elsewhere.example/x");
  assert.equal(requests.at(-1).headers.get("Authorization"), null);

  assert.equal(e.authURL("/api/transcript/@1"), "/api/transcript/@1?mo_ticket=TICKET");
  assert.equal(e.authURL("/ca.crt?as=file"), "/ca.crt?as=file&mo_ticket=TICKET");
  assert.equal(e.authURL("https://elsewhere.example/a"), "https://elsewhere.example/a");

  // Path updates go to the parent's origin, never "*".
  e.postPath();
  const msg = parent.sent.at(-1);
  assert.equal(msg.target, SHELL);
  assert.deepEqual(msg.msg, { type: "mo-path", path: "/chat?window=@1", title: "Chat" });
});

test("401 and network failures are reported to the parent", async () => {
  const { win, parent } = fakeWin({ routes: { ...ticketRoute, "GET /api/state": 401, "GET /api/usage": "network" } });
  const e = createEmbed(win);
  await win.dispatch("message", { source: parent, origin: SHELL, data: { type: "mo-auth", token: "TOKEN" } });
  await e.ready;
  const res = await e.fetch(ORIGIN + "/api/state");
  assert.equal(res.status, 401);
  assert.deepEqual(parent.sent.at(-1), { msg: { type: "mo-unauthorized" }, target: SHELL });
  await assert.rejects(e.fetch(ORIGIN + "/api/usage"));
  assert.deepEqual(parent.sent.at(-1), { msg: { type: "mo-lost" }, target: SHELL });
});

test("a navigation inside the frame keeps the login", async () => {
  const storage = memStorage();
  const first = fakeWin({ routes: ticketRoute, storage });
  const e1 = createEmbed(first.win);
  await first.win.dispatch("message", { source: first.parent, origin: SHELL, data: { type: "mo-auth", token: "TOKEN" } });
  await e1.ready;

  const second = fakeWin({ storage });
  const e2 = createEmbed(second.win);
  await e2.ready;
  assert.equal(second.parent.sent.some((s) => s.msg.type === "mo-ready"), false, "doesn't ask again");
  await e2.fetch(ORIGIN + "/api/state");
  assert.equal(second.requests.at(-1).headers.get("Authorization"), "Bearer TOKEN");
  assert.equal(e2.authURL("/api/transcript/@1"), "/api/transcript/@1?mo_ticket=TICKET");
});

test("without a parent's answer the page goes on after the timeout", async () => {
  const { win } = fakeWin();
  const e = createEmbed(win);
  await e.ready; // resolves through the (shortened) timeout
  assert.equal(e.authURL("/api/transcript/@1"), "/api/transcript/@1");
});

test("the wrapper can replace window.fetch without calling itself", async () => {
  const { win, parent, requests } = fakeWin({ routes: ticketRoute });
  const e = createEmbed(win);
  win.fetch = e.fetch; // what the page does
  await win.dispatch("message", { source: parent, origin: SHELL, data: { type: "mo-auth", token: "TOKEN" } });
  await e.ready;
  const res = await Promise.race([win.fetch(ORIGIN + "/api/state"), new Promise((r) => setTimeout(() => r("hung"), 200))]);
  assert.notEqual(res, "hung");
  assert.equal(requests.at(-1).headers.get("Authorization"), "Bearer TOKEN");
});
