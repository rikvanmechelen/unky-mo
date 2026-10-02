// Plain polling + DOM updates. No build step, no framework — matches the
// convention in chat.js. Status color is the only color in the interface
// (see STATUS below); everything else is black/white/gray.

const STATUS = {
  active:     { label: "Working",          sq: "#00B140", ring: 0 },
  idle:       { label: "Idle",             sq: "#fff",    ring: 2 },
  permission: { label: "Needs permission", sq: "#000",    ring: 0, rowBg: "rgba(255,205,0,0.20)" },
  question:   { label: "Needs input",      sq: "#000",    ring: 0, rowBg: "rgba(255,205,0,0.20)" },
  external:   { label: "External session", sq: "#767676", ring: 0 },
  none:       { label: "No session",       sq: "#ddd",    ring: 0 },
};

const BUCKET = {
  in_progress: { label: "In Progress", sq: "#00B140", ring: 0 },
  blocked:     { label: "Blocked",     sq: "#E4002B", ring: 0 },
  review:      { label: "Review",      sq: "#0057B8", ring: 0 },
  todo:        { label: "To Do",       sq: "#fff",    ring: 1 },
};

const PRIORITY_LABEL = { 0: "—", 1: "Lowest", 2: "Low", 3: "Medium", 4: "High", 5: "Highest" };

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

function statusSquare(sq, ring, extraClass) {
  const span = el("span", { class: "status-sq" + (extraClass ? " " + extraClass : "") });
  span.style.background = sq;
  span.style.boxShadow = ring ? `inset 0 0 0 ${ring}px #000` : "none";
  return span;
}

async function fetchJSON(path) {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}

// ── Usage meters ──────────────────────────────────────────────
function renderUsage(usage) {
  const container = document.getElementById("usage");
  container.innerHTML = "";
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
    container.appendChild(el("div", { class: "meter" }, [
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

// ── Sessions ──────────────────────────────────────────────────
function sessionNote(p) {
  if (p.team_role === "lead") return `Team lead (${p.team_name})`;
  if (p.team_role === "teammate") return `Teammate (${p.team_name})`;
  if (p.index > 0) return "Concurrent sibling";
  if (p.section === "external") return "External session";
  return "";
}

function renderSessions(projects) {
  const body = document.getElementById("sessions-body");
  const count = document.getElementById("sessions-count");
  body.innerHTML = "";

  const rows = (projects || []).filter((p) => p.session_id || p.status !== "none");
  count.textContent = String(rows.length);

  for (const p of rows) {
    const meta = STATUS[p.status] || STATUS.none;
    const row = el("div", { class: "session-row" }, [
      el("span", { class: "status-chip" }, [
        statusSquare(meta.sq, meta.ring),
        document.createTextNode(meta.label),
      ]),
      el("span", { class: "session-row__project", text: p.name }),
      el("span", { class: "session-row__window", text: p.window_name || "" }),
      el("span", { class: "session-row__branch", text: p.branch || "" }),
      el("span", { class: "session-row__note", text: sessionNote(p) }),
      el("span", { class: "session-row__action" }, p.window_id
        ? [el("a", { href: `/chat?window=${encodeURIComponent(p.window_id)}`, text: "Open chat" })]
        : []),
    ]);
    if (meta.rowBg) row.style.background = meta.rowBg;
    body.appendChild(row);
  }
}

// ── Projects ──────────────────────────────────────────────────
function branchTagClass(b) {
  if (b.IsMain) return { cls: "branch-tag branch-tag--main", label: "Main checkout" };
  if (b.WorktreePath) return { cls: "branch-tag branch-tag--worktree", label: "Worktree" };
  return { cls: "branch-tag", label: "Branch only" };
}

function renderBranches(branches) {
  const list = el("div", { class: "branch-list" });
  for (const b of branches || []) {
    const tag = branchTagClass(b);
    list.appendChild(el("div", { class: "branch-row" }, [
      el("span", { class: "branch-row__name", text: b.Name }),
      el("span", { class: tag.cls, text: tag.label }),
    ]));
  }
  return list;
}

function renderPRs(prs) {
  const list = el("div", { class: "pr-list" });
  if (!prs || prs.length === 0) {
    list.appendChild(el("div", { class: "pr-row" }, [
      el("span", { class: "pr-row__num", text: "—" }),
      el("span", { class: "pr-row__title", text: "No open pull requests" }),
      el("span", { class: "pr-row__state" }),
    ]));
    return list;
  }
  for (const pr of prs) {
    const state = (pr.state || "").toUpperCase();
    const stateEl = el("span", { class: "pr-row__state", text: pr.state });
    stateEl.style.color = state === "OPEN" ? "#000" : "#666";
    list.appendChild(el("div", { class: "pr-row" }, [
      el("span", { class: "pr-row__num", text: `#${pr.number}` }),
      el("span", { class: "pr-row__title", text: pr.title }),
      stateEl,
    ]));
  }
  return list;
}

function renderProjects(projects) {
  const container = document.getElementById("projects-list");
  const count = document.getElementById("projects-count");
  container.innerHTML = "";
  count.textContent = String((projects || []).length);

  for (const p of projects || []) {
    const name = p.Name || p.name;
    const detail = el("div", { class: "project__detail-loading", text: "" });
    detail.style.display = "none";
    let loaded = false;

    const action = el("span", { class: "project__action", text: "Details" });
    const toggle = el("button", {
      class: "project__toggle",
      onClick: async () => {
        const open = detail.style.display !== "none";
        if (open) {
          detail.style.display = "none";
          action.textContent = "Details";
          return;
        }
        detail.style.display = "flex";
        action.textContent = "Close";
        if (loaded) return;
        loaded = true;
        detail.textContent = "Loading…";
        detail.className = "project__detail-loading";
        const [branches, prs] = await Promise.all([
          fetchJSON(`/api/projects/${encodeURIComponent(name)}/worktrees`).catch(() => []),
          fetchJSON(`/api/projects/${encodeURIComponent(name)}/prs`).catch(() => []),
        ]);
        detail.textContent = "";
        detail.className = "project__detail";
        detail.appendChild(el("div", { style: "display:flex;flex-direction:column;gap:12px;" }, [
          el("div", { class: "project__sub-heading", text: "Branches" }),
          renderBranches(branches),
        ]));
        detail.appendChild(el("div", { style: "display:flex;flex-direction:column;gap:12px;" }, [
          el("div", { class: "project__sub-heading", text: "Open pull requests" }),
          renderPRs(prs),
        ]));
      },
    }, [
      el("span", { class: "project__name", text: name }),
      action,
    ]);

    container.appendChild(el("div", { class: "project" }, [toggle, detail]));
  }
}

// ── Tickets ───────────────────────────────────────────────────
function renderTickets(tickets) {
  const body = document.getElementById("tickets-body");
  const count = document.getElementById("tickets-count");
  body.innerHTML = "";
  count.textContent = String((tickets || []).length);

  for (const t of tickets || []) {
    const bucketMeta = BUCKET[t.Bucket];
    const bucketLabel = bucketMeta ? bucketMeta.label : `[${t.RawStatus || t.Bucket}]`;
    const sq = statusSquare(bucketMeta ? bucketMeta.sq : "#fff", bucketMeta ? bucketMeta.ring : 1);
    const bucketEl = el("span", { class: "ticket-row__bucket" }, [sq, document.createTextNode(bucketLabel)]);
    if (!bucketMeta) bucketEl.style.color = "#E4002B";

    const priority = el("span", {
      class: "ticket-row__priority",
      text: PRIORITY_LABEL[t.Priority] || String(t.Priority),
    });
    priority.style.fontWeight = t.Priority >= 4 ? "700" : "400";

    body.appendChild(el("div", { class: "ticket-row" }, [
      el("span", { class: "ticket-row__id", text: t.ID }),
      el("span", { class: "ticket-row__title", text: t.Title }),
      bucketEl,
      priority,
    ]));
  }
}

async function pollState() {
  try {
    const state = await fetchJSON("/api/state");
    renderUsage(state.usage);
    renderSessions(state.projects);
  } catch (e) {
    console.error(e);
  }
}

async function pollProjects() {
  try {
    renderProjects(await fetchJSON("/api/projects"));
  } catch (e) {
    console.error(e);
  }
}

async function pollTickets() {
  try {
    const data = await fetchJSON("/api/tickets");
    renderTickets(data.tickets);
  } catch (e) {
    console.error(e);
  }
}

document.getElementById("subtitle").textContent = `mo web — ${location.host || "localhost"}`;

pollState();
pollProjects();
pollTickets();
setInterval(pollState, 2000);
setInterval(pollProjects, 60000);
setInterval(pollTickets, 60000);
