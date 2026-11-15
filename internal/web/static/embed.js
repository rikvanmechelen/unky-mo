// The dashboard inside the installed app's shell (docs/plans/pwa.md step 4).
// When a page runs in an iframe, the shell hands it the device's token over
// postMessage; every same-origin fetch then carries it, and what can't send
// a header (EventSource, <img>, downloads) gets a URL ticket through
// authURL. Loaded first on every page, before anything fetches. Outside a
// frame it changes nothing.

function createEmbed(win) {
  const origin = win.location.origin;
  // The browser's own fetch, kept now: the page replaces window.fetch with
  // the wrapper below, which must not end up calling itself.
  const nativeFetch = win.fetch;
  const realFetch = (req) => nativeFetch.call(win, req);
  const embedded = win.top !== win;
  if (!embedded) {
    return { embedded: false, ready: Promise.resolve(), fetch: (input, init) => realFetch(new Request(input, init)), authURL: (u) => u, postPath: () => {}, post: () => {} };
  }

  const KEY = "mo.embed";
  const store = win.sessionStorage;
  let token = null;
  let ticket = null;
  let parentOrigin = null;
  let resolveReady;
  const ready = new Promise((r) => { resolveReady = r; });

  const post = (msg) => {
    if (parentOrigin) win.parent.postMessage(msg, parentOrigin);
  };
  const persist = () => {
    try {
      store.setItem(KEY, JSON.stringify({ token, ticket, parentOrigin }));
    } catch {
      // the next page in the frame asks the shell again
    }
  };

  async function getTicket() {
    try {
      const res = await realFetch(new Request(origin + "/api/hub/ticket", { method: "POST", headers: { Authorization: "Bearer " + token } }));
      if (res.ok) ticket = (await res.json()).ticket || null;
    } catch {
      // streams and images then fail like any other unauthenticated load
    }
  }

  function postPath() {
    const loc = win.location;
    post({ type: "mo-path", path: loc.pathname + loc.search + loc.hash, title: (win.document && win.document.title) || "" });
  }

  let saved = null;
  try {
    saved = JSON.parse(store.getItem(KEY) || "null");
  } catch {
    saved = null;
  }
  if (saved && typeof saved.token === "string" && typeof saved.parentOrigin === "string") {
    ({ token, ticket, parentOrigin } = saved);
    if (ticket) resolveReady();
    else getTicket().then(() => { persist(); resolveReady(); });
  } else {
    win.parent.postMessage({ type: "mo-ready" }, "*"); // nothing secret in it
    win.setTimeout(resolveReady, 4000);
  }

  win.addEventListener("message", async (e) => {
    if (e.source !== win.parent || token) return;
    const m = e.data;
    if (!m || m.type !== "mo-auth" || typeof m.token !== "string") return;
    token = m.token;
    parentOrigin = e.origin;
    await getTicket();
    persist();
    resolveReady();
    postPath();
  });

  async function fetchWithToken(input, init) {
    await ready;
    const req = new Request(input, init);
    if (token && new URL(req.url).origin === origin) req.headers.set("Authorization", "Bearer " + token);
    let res;
    try {
      res = await realFetch(req);
    } catch (err) {
      post({ type: "mo-lost" });
      throw err;
    }
    if (res.status === 401 && token) post({ type: "mo-unauthorized" });
    return res;
  }

  function authURL(u) {
    if (!ticket) return u;
    const url = new URL(u, origin);
    if (url.origin !== origin) return u;
    url.searchParams.set("mo_ticket", ticket);
    return url.pathname + url.search + url.hash;
  }

  return { embedded: true, ready, fetch: fetchWithToken, authURL, postPath, post };
}

if (typeof module === "object" && module.exports) {
  module.exports = { createEmbed };
}

// In a page: wrap fetch, report navigations, and send /hub links to the
// shell rather than opening the launcher inside the frame.
const moEmbed = typeof window === "object" && typeof module !== "object" ? createEmbed(window) : null;

// authURL adds a URL ticket inside the shell (EventSource, <img>, links).
function authURL(u) {
  return moEmbed ? moEmbed.authURL(u) : u;
}

if (moEmbed && moEmbed.embedded) {
  window.fetch = moEmbed.fetch;
  document.documentElement.classList.add("is-embedded");
  for (const name of ["pushState", "replaceState"]) {
    const orig = history[name].bind(history);
    history[name] = (...args) => {
      const out = orig(...args);
      moEmbed.postPath();
      return out;
    };
  }
  window.addEventListener("popstate", () => moEmbed.postPath());
  window.addEventListener("load", () => moEmbed.postPath());
  document.addEventListener("click", (e) => {
    const a = e.target.closest && e.target.closest("a[href]");
    if (!a) return;
    const url = new URL(a.getAttribute("href"), location.href);
    if (url.origin === location.origin && (url.pathname === "/hub" || url.pathname.startsWith("/hub/"))) {
      e.preventDefault();
      moEmbed.post({ type: "mo-machines" });
    }
  }, true);
}
