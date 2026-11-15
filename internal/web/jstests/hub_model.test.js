// Tests for static/hub-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const {
  CORE_RELEASE, normalizeAddress, loadHub, saveHub, learnMachine, mergeAddrs, orderCandidates,
  probe, findMachine, targetPath, forwardDecision,
} = require("../static/hub-model.js");

function memStorage() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)) };
}

const ID = "0123456789abcdef0123456789abcdef";
const OTHER = "fedcba9876543210fedcba9876543210";

test("CORE_RELEASE is a positive integer", () => {
  assert.ok(Number.isInteger(CORE_RELEASE) && CORE_RELEASE > 0);
});

test("normalizeAddress", () => {
  const ok = {
    "10.1.4.7": "https://10.1.4.7:7890",
    "10.1.4.7:8000": "https://10.1.4.7:8000",
    "https://Laptop.local:7890/": "https://laptop.local:7890",
    "laptop.local": "https://laptop.local:7890",
    " https://laptop.tail.ts.net ": "https://laptop.tail.ts.net",
    "[fd00::5]:7890": "https://[fd00::5]:7890",
    "http://localhost:7890": "http://localhost:7890",
  };
  for (const [input, want] of Object.entries(ok)) assert.equal(normalizeAddress(input), want, input);
  for (const bad of ["", "http://10.1.4.7:7890", "https://user:pw@laptop.local", "https://laptop.local/chat",
    "https://laptop.local:99999", "ftp://laptop", "not an address", "javascript:alert(1)", "https://laptop.local?x=1"]) {
    assert.equal(normalizeAddress(bad), null, bad);
  }
});

test("a hub round-trips through storage, and garbage loads as empty", () => {
  const s = memStorage();
  assert.deepEqual(loadHub(s), { machines: {}, last: null });
  s.setItem("mo.hub", "{not json");
  assert.deepEqual(loadHub(s), { machines: {}, last: null });
  const hub = learnMachine(loadHub(s), { machine: ID, name: "buttercup" }, "https://10.0.0.2:7890", 1000);
  saveHub(s, hub);
  assert.deepEqual(loadHub(s), hub);
});

test("learnMachine records the address that answered and the name", () => {
  let hub = { machines: {}, last: null };
  hub = learnMachine(hub, { machine: ID, name: "buttercup" }, "https://10.0.0.2:7890", 1000);
  hub = learnMachine(hub, { machine: ID, name: "buttercup2" }, "https://10.0.0.2:7890", 2000);
  const m = hub.machines[ID];
  assert.equal(m.name, "buttercup2");
  assert.deepEqual(m.candidates, [{ url: "https://10.0.0.2:7890", source: "learned", lastOk: 2000, lastFail: 0 }]);
  assert.equal(hub.last, ID);
  // A machine id that isn't 32 hex characters is ignored.
  assert.deepEqual(learnMachine(hub, { machine: "x", name: "evil" }, "https://1.2.3.4", 3000), hub);
});

test("mergeAddrs adds reported addresses once and never downgrades a learned one", () => {
  let hub = learnMachine({ machines: {}, last: null }, { machine: ID, name: "b" }, "https://10.0.0.2:7890", 1000);
  hub = mergeAddrs(hub, ID, ["https://10.0.0.2:7890", "https://buttercup.local:7890", "http://evil", "https://buttercup.local:7890"]);
  assert.deepEqual(hub.machines[ID].candidates.map((c) => [c.url, c.source]), [
    ["https://10.0.0.2:7890", "learned"],
    ["https://buttercup.local:7890", "reported"],
  ]);
  assert.deepEqual(mergeAddrs(hub, OTHER, ["https://x:1"]), hub, "unknown machine is left alone");
});

test("orderCandidates: last good first, then names, then IPs", () => {
  const m = {
    candidates: [
      { url: "https://192.168.1.20:7890", source: "reported", lastOk: 0, lastFail: 0 },
      { url: "https://buttercup.local:7890", source: "reported", lastOk: 0, lastFail: 0 },
      { url: "https://10.1.4.7:7890", source: "learned", lastOk: 500, lastFail: 0 },
      { url: "https://10.9.9.9:7890", source: "manual", lastOk: 900, lastFail: 0 },
    ],
  };
  assert.deepEqual(orderCandidates(m).map((c) => c.url), [
    "https://10.9.9.9:7890", "https://10.1.4.7:7890", "https://buttercup.local:7890", "https://192.168.1.20:7890",
  ]);
});

// fakeNet answers hello per URL: {machine} after delay ms, or rejects/never.
function fakeNet(table) {
  const timers = [];
  let now = 0;
  const env = {
    setTimeout: (fn, ms) => { const t = { at: now + ms, fn }; timers.push(t); return t; },
    clearTimeout: (t) => { const i = timers.indexOf(t); if (i >= 0) timers.splice(i, 1); },
    fetch: (url, opts) => new Promise((resolve, reject) => {
      const base = url.replace(/\/api\/hub\/hello$/, "");
      const row = table[base];
      if (opts && opts.signal) opts.signal.addEventListener("abort", () => reject(new Error("aborted")));
      if (!row || row.never) return;
      env.setTimeout(() => {
        if (row.fail) reject(new TypeError("Failed to fetch"));
        else resolve({ ok: true, json: async () => ({ app: "unky-mo", machine: row.machine, name: "n", protocol: 1 }) });
      }, row.delay || 0);
    }),
    // advance runs timers due by now+ms, in order, letting promises settle.
    async advance(ms) {
      const end = now + ms;
      for (;;) {
        await new Promise((r) => setImmediate(r));
        timers.sort((a, b) => a.at - b.at);
        const t = timers[0];
        if (!t || t.at > end) break;
        timers.shift();
        now = t.at;
        t.fn();
      }
      now = end;
      await new Promise((r) => setImmediate(r));
    },
  };
  return env;
}

test("probe returns the hello, or null on failure or timeout", async () => {
  const net = fakeNet({ "https://a:1": { machine: ID, delay: 10 }, "https://b:1": { fail: true }, "https://c:1": { never: true } });
  const results = Promise.all(["https://a:1", "https://b:1", "https://c:1"].map((u) => probe(u, { ...net, timeoutMs: 2500 })));
  await net.advance(3000);
  const [a, b, c] = await results;
  assert.equal(a.machine, ID);
  assert.equal(b, null);
  assert.equal(c, null);
});

test("findMachine: the last good address gets a head start, a wrong machine is ignored", async () => {
  const machine = {
    candidates: [
      { url: "https://good:1", source: "learned", lastOk: 100, lastFail: 0 },
      { url: "https://imposter:1", source: "reported", lastOk: 0, lastFail: 0 },
      { url: "https://fast:1", source: "reported", lastOk: 0, lastFail: 0 },
    ],
  };
  const net = fakeNet({
    "https://good:1": { machine: ID, delay: 300 },
    "https://imposter:1": { machine: OTHER, delay: 1 },
    "https://fast:1": { machine: ID, delay: 1 },
  });
  const found = findMachine(ID, machine, { ...net, headStartMs: 600, timeoutMs: 2500 });
  await net.advance(3000);
  assert.equal(await found, "https://good:1", "head start wins while it answers within 600 ms");

  const net2 = fakeNet({
    "https://good:1": { never: true },
    "https://imposter:1": { machine: OTHER, delay: 1 },
    "https://fast:1": { machine: ID, delay: 1 },
  });
  const found2 = findMachine(ID, machine, { ...net2, headStartMs: 600, timeoutMs: 2500 });
  await net2.advance(4000);
  assert.equal(await found2, "https://fast:1");

  const net3 = fakeNet({ "https://imposter:1": { machine: OTHER } });
  const none = findMachine(ID, machine, { ...net3, headStartMs: 600, timeoutMs: 2500 });
  await net3.advance(4000);
  assert.equal(await none, null);
});

test("targetPath: the launcher stands in for the page that was asked for", () => {
  assert.equal(targetPath({ pathname: "/hub", search: "", hash: "" }), "/");
  assert.equal(targetPath({ pathname: "/hub", search: "?stay=1", hash: "" }), "/");
  assert.equal(targetPath({ pathname: "/chat", search: "?window=@3", hash: "#x" }), "/chat?window=@3#x");
});

test("forwardDecision", () => {
  const hub = learnMachine({ machines: {}, last: null }, { machine: ID, name: "b" }, "https://a:1", 1);
  // Own origin answers: go on to the page, unless asked to stay.
  assert.deepEqual(forwardDecision({ own: { machine: ID }, stay: false, found: null, hub }), { kind: "own" });
  assert.deepEqual(forwardDecision({ own: { machine: ID }, stay: true, found: null, hub }), { kind: "stay" });
  // Own origin down: the last machine found elsewhere.
  assert.deepEqual(forwardDecision({ own: null, stay: false, found: { [ID]: "https://b:1" }, hub }), { kind: "go", url: "https://b:1" });
  assert.deepEqual(forwardDecision({ own: null, stay: true, found: { [ID]: "https://b:1" }, hub }), { kind: "stay" });
  assert.deepEqual(forwardDecision({ own: null, stay: false, found: {}, hub }), { kind: "stay" });
});
