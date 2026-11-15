// The launcher's pure logic (docs/plans/pwa.md): which machines this app
// knows, at which addresses, and finding the one that answers. No DOM;
// hub.js draws it. Tested in jstests/hub_model.test.js.

// CORE_RELEASE is the launcher's release. Raise it whenever a core file
// changes (the Go test TestCoreReleaseTable says so): installed apps only
// take a core with a higher release.
const CORE_RELEASE = 1;

const HUB_KEY = "mo.hub";
const DEFAULT_PORT = "7890";

function isMachineID(s) {
  return typeof s === "string" && /^[0-9a-f]{32}$/.test(s);
}

// normalizeAddress turns what someone typed into an origin
// ("https://host:port"), or null. https only (an installed app needs a
// secure context), except localhost; no user info, path or query.
function normalizeAddress(input) {
  const typed = String(input || "").trim();
  if (!typed) return null;
  // Without a scheme it's a bare host[:port], on mo web's default port.
  const bare = !/^[a-z][a-z0-9+.-]*:\/\//i.test(typed);
  let u;
  try {
    u = new URL(bare ? "https://" + typed : typed);
  } catch {
    return null;
  }
  const local = u.hostname === "localhost" || u.hostname === "127.0.0.1";
  if (u.protocol !== "https:" && !(u.protocol === "http:" && local)) return null;
  if (!u.hostname || u.username || u.password || u.search || u.hash || u.pathname !== "/") return null;
  if (bare && !u.port && !/:\d+$/.test(typed)) u.port = DEFAULT_PORT;
  return u.origin;
}

function loadHub(storage) {
  try {
    const raw = storage.getItem(HUB_KEY);
    const hub = raw ? JSON.parse(raw) : null;
    if (hub && typeof hub.machines === "object" && hub.machines) return { machines: hub.machines, last: hub.last || null };
  } catch {
    // unreadable: start over
  }
  return { machines: {}, last: null };
}

function saveHub(storage, hub) {
  try {
    storage.setItem(HUB_KEY, JSON.stringify(hub));
  } catch {
    // storage full or blocked: the launcher still works for this visit
  }
}

function withMachine(hub, id, fn) {
  const machines = { ...hub.machines };
  const m = machines[id] ? { ...machines[id], candidates: machines[id].candidates.map((c) => ({ ...c })) } : { name: "", candidates: [] };
  fn(m);
  machines[id] = m;
  return { ...hub, machines };
}

function addCandidate(m, url, source) {
  let c = m.candidates.find((x) => x.url === url);
  if (!c) {
    c = { url, source, lastOk: 0, lastFail: 0 };
    m.candidates.push(c);
  } else if (source === "learned" || source === "manual") {
    c.source = c.source === "manual" ? "manual" : source;
  }
  return c;
}

// learnMachine records that url answered as hello's machine.
function learnMachine(hub, hello, url, now) {
  if (!hello || !isMachineID(hello.machine)) return hub;
  const next = withMachine(hub, hello.machine, (m) => {
    if (hello.name) m.name = String(hello.name);
    addCandidate(m, url, "learned").lastOk = now;
  });
  next.last = hello.machine;
  return next;
}

// mergeAddrs adds the addresses a machine reported (GET /api/hub/addrs).
function mergeAddrs(hub, id, urls) {
  if (!hub.machines[id]) return hub;
  return withMachine(hub, id, (m) => {
    for (const raw of urls || []) {
      const url = normalizeAddress(raw);
      if (url && sameOrigin(raw, url)) addCandidate(m, url, "reported");
    }
  });
}

function sameOrigin(raw, origin) {
  try {
    return new URL(raw).origin === origin;
  } catch {
    return false;
  }
}

// addManual adds an address someone typed to a machine that answered there.
function addManual(hub, hello, url, now) {
  if (!hello || !isMachineID(hello.machine)) return hub;
  const next = withMachine(hub, hello.machine, (m) => {
    if (hello.name) m.name = String(hello.name);
    const c = addCandidate(m, url, "manual");
    c.lastOk = now;
  });
  return next;
}

function markFailed(hub, id, url, now) {
  if (!hub.machines[id]) return hub;
  return withMachine(hub, id, (m) => {
    const c = m.candidates.find((x) => x.url === url);
    if (c) c.lastFail = now;
  });
}

function isIPHost(url) {
  const host = new URL(url).hostname;
  return /^\d+\.\d+\.\d+\.\d+$/.test(host) || host.startsWith("[");
}

// orderCandidates: addresses that worked, most recent first; then names
// (.local, VPN, DNS), which survive network changes; then the rest.
function orderCandidates(machine) {
  const rank = (c) => (c.lastOk ? 0 : isIPHost(c.url) ? 2 : 1);
  return [...machine.candidates].sort((a, b) => rank(a) - rank(b) || b.lastOk - a.lastOk);
}

// probe asks url for its hello, giving up after timeoutMs. null when it
// doesn't answer as unky-mo.
async function probe(url, env) {
  const ctrl = typeof AbortController === "function" ? new AbortController() : null;
  let timer;
  const timeout = new Promise((resolve) => {
    timer = env.setTimeout(() => {
      if (ctrl) ctrl.abort();
      resolve(null);
    }, env.timeoutMs);
  });
  const ask = (async () => {
    try {
      const res = await env.fetch(url + "/api/hub/hello", { cache: "no-store", credentials: "omit", mode: "cors", signal: ctrl ? ctrl.signal : undefined });
      if (!res.ok) return null;
      const hello = await res.json();
      return hello && hello.app === "unky-mo" && isMachineID(hello.machine) ? hello : null;
    } catch {
      return null;
    }
  })();
  const out = await Promise.race([ask, timeout]);
  env.clearTimeout(timer);
  return out;
}

// findMachine finds the address machine id answers at: the best candidate
// gets a head start, then every candidate is tried at once. The first
// hello naming the machine wins; another machine at an address (the same
// IP on another network) doesn't count. null when none answers.
function findMachine(id, machine, env) {
  const order = orderCandidates(machine);
  if (!order.length) return Promise.resolve(null);
  return new Promise((resolve) => {
    let pending = order.length;
    let done = false;
    const finish = (url) => {
      if (done) return;
      if (url) {
        done = true;
        resolve(url);
      } else if (--pending === 0) {
        done = true;
        resolve(null);
      }
    };
    const run = (c) => probe(c.url, env).then((hello) => finish(hello && hello.machine === id ? c.url : null));
    const [first, ...rest] = order;
    run(first);
    if (rest.length) env.setTimeout(() => { if (!done) rest.forEach(run); }, order[0].lastOk ? env.headStartMs : 0);
  });
}

// targetPath is the page the launcher stands in for: "/" for the launcher
// itself, else the page that was asked for when this address didn't answer.
function targetPath(loc) {
  if (loc.pathname === "/hub") return "/";
  return loc.pathname + loc.search + loc.hash;
}

// forwardDecision says what the launcher does once it knows what answers:
// "own" (this address works: open the page here), "go" (the last machine
// answers elsewhere) or "stay" (show the list).
function forwardDecision({ own, stay, found, hub }) {
  if (stay) return { kind: "stay" };
  if (own) return { kind: "own" };
  const url = hub.last && found && found[hub.last];
  return url ? { kind: "go", url } : { kind: "stay" };
}

if (typeof module === "object" && module.exports) {
  module.exports = {
    CORE_RELEASE, normalizeAddress, loadHub, saveHub, learnMachine, mergeAddrs, addManual, markFailed,
    orderCandidates, probe, findMachine, targetPath, forwardDecision, isMachineID,
  };
}
