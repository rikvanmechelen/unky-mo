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

// renderUsage draws the 5-hour / 7-day rate-limit meters into container.
// opts.compact selects the small variant used in the chat view's nav.
function renderUsage(container, usage, opts) {
  container.replaceChildren();
  if (!usage) {
    container.appendChild(el("div", { class: "empty-note", text: "Usage data unavailable." }));
    return;
  }

  const meters = [
    { label: "5-hour window", pct: usage.five_hour_pct },
    { label: "7-day window", pct: usage.seven_day_pct },
  ];
  for (const m of meters) {
    if (m.pct == null) continue;
    const label = usage.stale ? `${m.label} (stale)` : m.label;
    const fill = el("div", { class: "meter__fill" + (m.pct >= 80 ? " is-high" : "") });
    fill.style.width = `${m.pct}%`;
    const track = el("div", { class: "meter__track" }, [fill]);
    container.appendChild(el("div", { class: "meter" + (opts && opts.compact ? " meter--compact" : "") }, [
      el("div", { class: "meter__row" }, [
        el("span", { class: "meter__label", text: label }),
        el("span", { class: "meter__pct", text: `${m.pct}%` }),
      ]),
      track,
    ]));
  }
  if (usage.auth_error) {
    container.appendChild(el("div", { class: "empty-note", text: "Usage fetch failed (auth error)." }));
  }
}
