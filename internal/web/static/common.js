// Helpers shared by the dashboard (app.js) and the chat view (chat.js).
// Loaded first via a plain <script> tag — no modules, no build step.

// Status color is the only color in the interface; everything else is
// black/white/gray. `short` is the compact label used in the chat nav, and
// `navSq` overrides `sq` there (the badge's black-on-yellow square would
// vanish on the nav's gray, so the nav uses the yellow itself);
// `bg`/`border` style the chat view's status badge; `rowBg` tints the
// dashboard's session row.
const STATUS = {
  active:     { label: "Working",          short: "working",    sq: "#00B140", ring: 0, bg: "#fff",    border: "#DDDDDD" },
  idle:       { label: "Idle",             short: "idle",       sq: "#fff",    ring: 2, bg: "#fff",    border: "#DDDDDD" },
  permission: { label: "Needs permission", short: "permission", sq: "#000", navSq: "#FFCD00", ring: 0, bg: "#FFCD00", border: "#FFCD00", rowBg: "rgba(255,205,0,0.20)" },
  question:   { label: "Needs input",      short: "input",      sq: "#000", navSq: "#FFCD00", ring: 0, bg: "#FFCD00", border: "#FFCD00", rowBg: "rgba(255,205,0,0.20)" },
  external:   { label: "External session", short: "external",   sq: "#767676", ring: 0, bg: "#fff",    border: "#DDDDDD" },
  none:       { label: "No session",       short: "",           sq: "#ddd",    ring: 0, bg: "#fff",    border: "#DDDDDD" },
  // Chat-view only: a just-launched window with no state row yet, and a
  // session whose row disappeared.
  starting:   { label: "Starting…",        short: "starting",   sq: "#fff",    ring: 1, bg: "#fff",    border: "#DDDDDD" },
  ended:      { label: "Ended",            short: "ended",      sq: "#ddd",    ring: 0, bg: "#fff",    border: "#DDDDDD" },
};

function el(tag, attrs, children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else if (k === "onClick") e.addEventListener("click", v);
    else e.setAttribute(k, v);
  }
  for (const child of children || []) e.appendChild(child);
  return e;
}

// statusSquare draws the status indicator. ringColor defaults to black; the
// chat nav passes white for the selected (black-background) row.
function statusSquare(sq, ring, extraClass, ringColor) {
  const span = el("span", { class: "status-sq" + (extraClass ? " " + extraClass : "") });
  span.style.background = sq;
  span.style.boxShadow = ring ? `inset 0 0 0 ${ring}px ${ringColor || "#000"}` : "none";
  return span;
}

// formatResetIn mirrors usage.FormatResetIn (the TUI sidebar's countdown):
// "2h05m", "42m", "30s", "now", or "" when the reset time is unknown. Go
// marshals a zero time.Time as year 1, so anything before 1971 counts as unset.
function formatResetIn(resetsAt) {
  const t = Date.parse(resetsAt || "");
  if (isNaN(t) || t < Date.UTC(1971, 0, 1)) return "";
  const secs = Math.floor((t - Date.now()) / 1000);
  if (secs <= 0) return "now";
  if (secs >= 3600) {
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return `${h}h${String(m).padStart(2, "0")}m`;
  }
  if (secs >= 60) return `${Math.floor(secs / 60)}m`;
  return `${secs}s`;
}

// formatTokensShort mirrors usage.FormatTokensShort: 1234567 -> "1.2M",
// 12345 -> "12.3k", 567 -> "567".
function formatTokensShort(n) {
  n = Math.max(0, n || 0);
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

// renderUsage draws the 5-hour / 7-day rate-limit meters into container.
// opts.compact selects the small variant used in the chat view's nav, which
// shows only the 5-hour meter, followed (like the TUI sidebar) by
// opts.tokens, the open session's context token count, when it's known.
function renderUsage(container, usage, opts) {
  container.replaceChildren();
  if (!usage) {
    container.appendChild(el("div", { class: "empty-note", text: "Usage data unavailable." }));
    return;
  }

  const meters = [
    { label: "5-hour window", pct: usage.five_hour_pct, resetIn: formatResetIn(usage.five_hour_resets_at), tokens: opts && opts.tokens },
    { label: "7-day window", pct: usage.seven_day_pct },
  ];
  const compact = !!(opts && opts.compact);
  if (compact) meters.length = 1;
  for (const m of meters) {
    if (m.pct == null) continue;
    let label = usage.stale ? `${m.label} (stale)` : m.label;
    if (m.resetIn === "now") label += " · resetting";
    else if (m.resetIn) label += ` · ${m.resetIn} left`;
    const fill = el("div", { class: "meter__fill" + (m.pct >= 80 ? " is-high" : "") });
    fill.style.width = `${m.pct}%`;
    const track = el("div", { class: "meter__track" }, [fill]);
    let pct = `${m.pct}%`;
    if (m.tokens > 0) pct += ` · ${formatTokensShort(opts.tokens)} tok`;
    container.appendChild(el("div", { class: "meter" + (compact ? " meter--compact" : "") }, [
      el("div", { class: "meter__row" }, [
        el("span", { class: "meter__label", text: label }),
        el("span", { class: "meter__pct", text: pct }),
      ]),
      track,
    ]));
  }
  if (usage.auth_error) {
    container.appendChild(el("div", { class: "empty-note", text: "Usage fetch failed (auth error)." }));
  }
}
