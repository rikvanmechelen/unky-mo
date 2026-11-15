// The launcher's pure logic (docs/plans/pwa.md): which machines this app
// knows, at which addresses, and finding the one that answers. No DOM;
// hub.js draws it. Tested in jstests/hub_model.test.js.

// CORE_RELEASE is the launcher's release. Raise it whenever a core file
// changes (the Go test TestCoreReleaseTable says so): installed apps only
// take a core with a higher release.
const CORE_RELEASE = 2;

// HUB_PROTOCOL is the hub API and message version this launcher speaks
// (HubProtocol in Go). Machines one version away still work.
const HUB_PROTOCOL = 1;

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

// safeNext is where to go after logging in: a path on this address, never
// another site and never the launcher itself.
function safeNext(next) {
  if (typeof next !== "string" || !next.startsWith("/") || next.startsWith("//") || next.startsWith("/\\")) return null;
  if (next === "/hub" || next.startsWith("/hub?") || next.startsWith("/hub/")) return null;
  return next;
}

// hubDeviceLabel guesses a name for this device ("Chrome on Android"),
// which the person can change before logging in.
function hubDeviceLabel(ua, standalone) {
  const os = /iPhone/.test(ua) ? "iPhone"
    : /iPad/.test(ua) ? "iPad"
    : /Android/.test(ua) ? "Android"
    : /Mac OS X|Macintosh/.test(ua) ? "macOS"
    : /Windows/.test(ua) ? "Windows"
    : /CrOS/.test(ua) ? "ChromeOS"
    : /Linux/.test(ua) ? "Linux"
    : "";
  const browser = /Edg\//.test(ua) ? "Edge"
    : /Firefox\/|FxiOS/.test(ua) ? "Firefox"
    : /Chrome\/|CriOS/.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari"
    : "Browser";
  const ios = os === "iPhone" || os === "iPad";
  const name = standalone ? (ios ? "Home Screen app" : `${browser} app`) : browser;
  return os ? `${name} on ${os}` : name;
}

// The shell keeps a device token per machine (docs/plans/pwa.md step 4),
// never a password.
const TOKENS_KEY = "mo.hub.tokens";

function loadTokens(storage) {
  try {
    const t = JSON.parse(storage.getItem(TOKENS_KEY) || "{}");
    if (!t || typeof t !== "object" || Array.isArray(t)) return {};
    const out = {};
    for (const [id, token] of Object.entries(t)) if (isMachineID(id) && typeof token === "string") out[id] = token;
    return out;
  } catch {
    return {};
  }
}

function saveToken(storage, id, token) {
  if (!isMachineID(id)) return;
  const t = loadTokens(storage);
  t[id] = token;
  try {
    storage.setItem(TOKENS_KEY, JSON.stringify(t));
  } catch {
    // the shell asks for the password again next time
  }
}

function dropToken(storage, id) {
  const t = loadTokens(storage);
  delete t[id];
  try {
    storage.setItem(TOKENS_KEY, JSON.stringify(t));
  } catch {
    // nothing to do
  }
}

const FRAME_MESSAGES = new Set(["mo-ready", "mo-path", "mo-lost", "mo-unauthorized", "mo-machines", "mo-notify"]);

// frameMessage returns a message the embedded machine sent, or null for
// anything from another window or origin, or that the shell doesn't know.
function frameMessage(event, frameWin, frameOrigin) {
  if (!event || event.source !== frameWin || event.origin !== frameOrigin) return null;
  const m = event.data;
  if (!m || typeof m !== "object" || !FRAME_MESSAGES.has(m.type)) return null;
  if (m.type === "mo-path") {
    if (typeof m.path !== "string" || !m.path.startsWith("/") || m.path.startsWith("//")) return null;
    return { type: "mo-path", path: m.path, title: typeof m.title === "string" ? m.title : "" };
  }
  return { type: m.type };
}

// shouldEmbed: the installed app shows another address in a frame, so it
// never leaves its own origin. Its own address, and a browser tab, open
// the page directly.
function shouldEmbed({ shell, url, own }) {
  return Boolean(shell) && url !== own;
}

// coreUpdate says whether to install a core another machine (or this
// address) offers: only from a machine this device trusts, only a valid
// document, only a newer release. Never a downgrade.
function coreUpdate({ core, running, trusted }) {
  if (!trusted || !core || typeof core !== "object") return false;
  if (!Number.isInteger(core.release) || !core.files || typeof core.files !== "object" || !core.files[core.entry]) return false;
  return core.release > running;
}

// baseWarning says whether to tell the person their app's base (worker or
// manifest) is older than what a machine ships, which only reinstalling or
// opening the install address fixes. null when there's nothing to say: no
// newer base, the own address answers (the browser updates it then), or
// this version was dismissed.
function baseWarning({ installed, offered, ownAnswers, dismissed }) {
  if (!installed || !offered || installed.sw == null) return null;
  const newer = offered.sw > installed.sw || offered.manifest > installed.manifest;
  if (!newer || ownAnswers) return null;
  const key = `${offered.sw}:${offered.manifest}`;
  return key === dismissed ? null : { key };
}

// protocolCheck compares a machine's protocol with this launcher's.
function protocolCheck(theirs) {
  if (!Number.isInteger(theirs) || theirs < HUB_PROTOCOL - 1) return "machine-old";
  if (theirs > HUB_PROTOCOL + 1) return "launcher-old";
  return "ok";
}

// keyBytes decodes a VAPID public key (base64url) for pushManager.subscribe.
function keyBytes(b64) {
  const s = (b64 + "=".repeat((4 - (b64.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/");
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

function sameKeyBytes(a, b64) {
  if (!a) return false;
  const x = new Uint8Array(a);
  const y = keyBytes(b64);
  return x.length === y.length && x.every((v, i) => v === y[i]);
}

// pushScope is where the shell registers the worker that receives a
// machine's pushes (docs/plans/pwa.md step 6).
function pushScope(id) {
  return `/hub/push/${id}/`;
}

// requestedMachine reads ?machine=<id>&next=<path>, which a notification
// delivered through the shell opens. null unless the machine is known.
function requestedMachine(params, hub) {
  const id = params.get("machine");
  if (!isMachineID(id) || !hub.machines[id]) return null;
  return { id, next: safeNext(params.get("next")) || "/" };
}

if (typeof module === "object" && module.exports) {
  module.exports = {
    CORE_RELEASE, normalizeAddress, loadHub, saveHub, learnMachine, mergeAddrs, addManual, markFailed,
    orderCandidates, probe, findMachine, targetPath, forwardDecision, isMachineID, safeNext, hubDeviceLabel,
    loadTokens, saveToken, dropToken, frameMessage, shouldEmbed,
    coreUpdate, baseWarning, protocolCheck, HUB_PROTOCOL, pushScope, requestedMachine, keyBytes, sameKeyBytes,
  };
}
