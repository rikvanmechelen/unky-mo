// Plain polling + DOM updates. No build step, no framework — intentionally
// basic for v1; a design pass will replace this once the data plumbing is
// proven out.

async function fetchJSON(path) {
  const res = await fetch(path);
  if (!res.ok) throw new Error(`${path}: ${res.status}`);
  return res.json();
}

function renderUsage(usage) {
  const el = document.getElementById("usage");
  if (!usage) {
    el.textContent = "";
    return;
  }
  el.textContent =
    `5h: ${usage.five_hour_pct}%` +
    (usage.seven_day_pct != null ? ` · 7d: ${usage.seven_day_pct}%` : "") +
    (usage.stale ? " (stale)" : "");
}

function renderSessions(projects) {
  const tbody = document.querySelector("#sessions tbody");
  tbody.innerHTML = "";
  for (const p of projects || []) {
    if (!p.session_id && p.status === "none") continue;
    const tr = document.createElement("tr");
    tr.innerHTML = `<td>${p.name}</td><td>${p.window_name}</td><td>${p.branch || ""}</td><td>${p.status}</td>`;
    tbody.appendChild(tr);
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

async function loadProjectDetail(name) {
  const detail = document.getElementById("project-detail");
  detail.innerHTML = "Loading…";

  const [branches, prs] = await Promise.all([
    fetchJSON(`/api/projects/${encodeURIComponent(name)}/worktrees`).catch(() => []),
    fetchJSON(`/api/projects/${encodeURIComponent(name)}/prs`).catch(() => []),
  ]);

  const branchRows = (branches || [])
    .map((b) => `<li>${b.Name}${b.IsMain ? " (main)" : ""}${b.WorktreePath ? " [worktree]" : ""}</li>`)
    .join("");
  const prRows = (prs || [])
    .map((pr) => `<li>#${pr.number} ${pr.title} (${pr.state})</li>`)
    .join("");

  detail.innerHTML =
    `<h3>${name}</h3>` +
    `<h4>Branches</h4><ul>${branchRows || "<li>none</li>"}</ul>` +
    `<h4>PRs</h4><ul>${prRows || "<li>none</li>"}</ul>`;
}

async function pollProjects() {
  try {
    const projects = await fetchJSON("/api/projects");
    const ul = document.getElementById("projects");
    ul.innerHTML = "";
    for (const p of projects || []) {
      const li = document.createElement("li");
      const a = document.createElement("a");
      a.href = "#";
      a.textContent = p.Name || p.name;
      a.onclick = (e) => {
        e.preventDefault();
        loadProjectDetail(p.Name || p.name);
      };
      li.appendChild(a);
      ul.appendChild(li);
    }
  } catch (e) {
    console.error(e);
  }
}

async function pollTickets() {
  try {
    const data = await fetchJSON("/api/tickets");
    const tbody = document.querySelector("#tickets tbody");
    tbody.innerHTML = "";
    for (const t of data.tickets || []) {
      const tr = document.createElement("tr");
      tr.innerHTML = `<td>${t.ID}</td><td>${t.Title}</td><td>${t.Bucket}</td><td>${t.Priority}</td>`;
      tbody.appendChild(tr);
    }
  } catch (e) {
    console.error(e);
  }
}

pollState();
pollProjects();
pollTickets();
setInterval(pollState, 2000);
setInterval(pollProjects, 60000);
setInterval(pollTickets, 60000);
