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

// ── Session actions (dialogs + stop live in actions.js) ───────
let AGENTS = [];

function chatURL(windowID, starting) {
  return `/chat?window=${encodeURIComponent(windowID)}` + (starting ? "&new=1" : "");
}

function agentPicker() {
  if (AGENTS.length < 2) return null;
  const select = el("select", { class: "agent-select", "aria-label": "Agent" });
  for (const a of AGENTS) {
    const opt = el("option", { value: a.key, text: a.name });
    if (a.default) opt.selected = true;
    select.appendChild(opt);
  }
  return select;
}

// startSession posts a launch/resume and lands on the session's chat. A busy
// primary window comes back as 409 + choices — the TUI's switch / park+new /
// concurrent menu — which we show and re-post with the picked mode.
async function startSession(projectName, body, button) {
  const label = button ? button.textContent : "";
  if (button) { button.disabled = true; button.textContent = "Starting…"; }
  try {
    for (;;) {
      const res = await fetch(`/api/projects/${encodeURIComponent(projectName)}/sessions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const data = await res.json().catch(() => ({}));
      if (res.ok) {
        location.href = chatURL(data.window_id, body.mode !== "switch");
        return;
      }
      if (res.status === 409 && data.choices) {
        const mode = await chooseMode(data);
        if (!mode) return;
        if (mode === "switch" && data.primary) {
          location.href = chatURL(data.primary.window_id, false);
          return;
        }
        body = { ...body, mode };
        continue;
      }
      await showDialog({ title: "Couldn't start session", text: data.error || `request failed (${res.status})`, actions: [{ label: "OK" }] });
      return;
    }
  } finally {
    if (button) { button.disabled = false; button.textContent = label; }
  }
}

function chooseMode(conflict) {
  const name = conflict.primary ? conflict.primary.window_name : "the primary window";
  const options = {
    switch:  { label: "Switch to running session", value: "switch" },
    replace: { label: `Replace it (stops ${name})`, value: "replace", danger: true },
    sibling: { label: "Run alongside", value: "sibling" },
  };
  return showDialog({
    title: "A session is already running here",
    text: conflict.error,
    actions: [...conflict.choices.map((c) => options[c]).filter(Boolean), { label: "Cancel" }],
  });
}


function relativeTime(iso) {
  const t = Date.parse(iso);
  if (!t || t < 0) return "";
  const mins = Math.round((Date.now() - t) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
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
        ? [
            el("a", { href: chatURL(p.window_id), text: "Open chat" }),
            ...(p.session_id ? [el("button", {
              class: "link-btn link-btn--danger",
              type: "button",
              text: "Stop",
              onClick: async () => { if (await stopSession(p.window_id, p.window_name)) pollState(); },
            })] : []),
          ]
        : []),
    ]);
    if (meta.rowBg) row.style.background = meta.rowBg;
    body.appendChild(row);
  }
}

// ── Projects ──────────────────────────────────────────────────
// renderCheckouts lists each checkout (main + worktrees) with the sessions
// live there and recent ones to resume — the TUI's project detail rows.
function renderCheckouts(projectName, checkouts, reload) {
  const list = el("div", { class: "checkout-list" });
  for (const c of checkouts || []) {
    const picker = agentPicker();
    const tag = c.is_main
      ? el("span", { class: "branch-tag branch-tag--main", text: "Main checkout" })
      : el("span", { class: "branch-tag branch-tag--worktree", text: "Worktree" });
    const newBtn = el("button", { class: "btn btn--primary btn--small", type: "button", text: "New session" });
    newBtn.addEventListener("click", () =>
      startSession(projectName, { branch: c.branch, agent: picker ? picker.value : "" }, newBtn));

    const rows = [];
    for (const l of c.live) {
      rows.push(el("div", { class: "checkout-session" }, [
        el("span", { class: "checkout-session__title", text: l.window_name }),
        el("span", { class: "checkout-session__meta", text: (STATUS[l.status] || STATUS.none).label }),
        el("span", { class: "checkout-session__actions" }, [
          el("a", { href: chatURL(l.window_id), text: "Open chat" }),
          el("button", {
            class: "link-btn link-btn--danger", type: "button", text: "Stop",
            onClick: async () => { if (await stopSession(l.window_id, l.window_name)) reload(); },
          }),
        ]),
      ]));
    }
    for (const r of c.recent) {
      if (r.live && r.window_id) continue; // already listed as live above
      const resumeBtn = el("button", { class: "link-btn", type: "button", text: r.live ? "Running elsewhere" : "Resume" });
      if (r.live) resumeBtn.disabled = true;
      resumeBtn.addEventListener("click", () =>
        startSession(projectName, { branch: c.branch, resume_id: r.session_id, agent: picker ? picker.value : "" }, resumeBtn));
      rows.push(el("div", { class: "checkout-session is-recent" }, [
        el("span", { class: "checkout-session__title", text: r.title, title: r.summary || "" }),
        el("span", { class: "checkout-session__meta", text: relativeTime(r.last_active) }),
        el("span", { class: "checkout-session__actions" }, [resumeBtn]),
      ]));
    }
    if (rows.length === 0) rows.push(el("div", { class: "empty-note", text: "No sessions yet." }));

    list.appendChild(el("div", { class: "checkout" }, [
      el("div", { class: "checkout__head" }, [
        el("span", { class: "branch-row__name", text: c.branch }),
        tag,
        el("span", { class: "checkout__spacer" }),
        ...(picker ? [picker] : []),
        newBtn,
      ]),
      ...rows,
    ]));
  }
  return list;
}

// renderWorktreeStarter: branches that aren't checked out anywhere, plus a
// free-text new branch — both create a worktree and launch in it (the TUI's
// `w` / `W`).
function renderWorktreeStarter(projectName, branches) {
  const input = el("input", { class: "text-input", type: "text", placeholder: "Branch name", list: `branches-${projectName}` });
  const datalist = el("datalist", { id: `branches-${projectName}` },
    (branches || []).filter((b) => !b.IsMain && !b.WorktreePath).map((b) => el("option", { value: b.Name })));
  const picker = agentPicker();
  const btn = el("button", { class: "btn btn--small", type: "submit", text: "Create worktree & start" });
  const form = el("form", { class: "worktree-form" }, [input, datalist, ...(picker ? [picker] : []), btn]);
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const branch = input.value.trim();
    if (branch) startSession(projectName, { branch, agent: picker ? picker.value : "" }, btn);
  });
  return form;
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

    const loadDetail = async () => {
      const enc = encodeURIComponent(name);
      const [checkouts, branches, prs] = await Promise.all([
        fetchJSON(`/api/projects/${enc}/sessions`).catch(() => []),
        fetchJSON(`/api/projects/${enc}/worktrees`).catch(() => []),
        fetchJSON(`/api/projects/${enc}/prs`).catch(() => []),
      ]);
      detail.textContent = "";
      detail.className = "project__detail";
      detail.appendChild(el("div", { class: "project__block" }, [
        el("div", { class: "project__sub-heading", text: "Sessions" }),
        renderCheckouts(name, checkouts, loadDetail),
      ]));
      detail.appendChild(el("div", { class: "project__block" }, [
        el("div", { class: "project__sub-heading", text: "New worktree" }),
        renderWorktreeStarter(name, branches),
      ]));
      detail.appendChild(el("div", { class: "project__block" }, [
        el("div", { class: "project__sub-heading", text: "Open pull requests" }),
        renderPRs(prs),
      ]));
    };

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
        await loadDetail();
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

fetchJSON("/api/agents").then((a) => { AGENTS = a || []; }).catch(() => {}).finally(pollProjects);
pollState();
pollTickets();
setInterval(pollState, 2000);
setInterval(pollProjects, 60000);
setInterval(pollTickets, 60000);
