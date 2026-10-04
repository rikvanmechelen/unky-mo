// Graph tab of the Files panel: the checkout's commit history drawn as a
// graph, like `git log --graph --oneline`. Commits come from
// /api/sessions/{windowID}/log (parents, refs, unpushed marks, newest first
// in topological order); layoutGraph gives each one a lane here, and every
// row draws its slice of the lines as a small SVG.
//
// "This branch" shows HEAD with its upstream and the default branch;
// "All" every branch, remote branch and tag. Clicking a commit expands
// its message and changed files (/commits/{hash}); a file calls
// onOpenCommitFile(hash, path), which opens a read-only diff tab. Uses el()
// from common.js.

const GRAPH_SCOPE_KEY = "mo.graphScope";
const GRAPH_LANE_W = 12;
const GRAPH_ROW_H = 26;
const GRAPH_MAX_LANES = 8; // wider graphs are clipped; the text stays readable
const GRAPH_COLORS = ["var(--blue)", "var(--mode-plan)", "var(--mode-accept)", "var(--claude)", "var(--green)", "var(--mode-auto)", "var(--red)", "var(--ink-4)"];
const WORKTREE = "~worktree"; // the "Uncommitted changes" pseudo commit
const SVG_NS = "http://www.w3.org/2000/svg";

// layoutGraph assigns lanes to commits listed newest first, each before its
// parents. A lane holds the hash it's waiting for; a commit takes the lane
// waiting for it (or a free one, as a branch tip), then hands it to its
// first parent. A parent another lane already waits for is joined instead,
// so branches merge back into one line. Each row records the lanes coming
// in (before) and going out (after), with their colors, and its edges.
function layoutGraph(commits) {
  const lanes = [];
  const colors = [];
  let nextColor = 0;
  const freeLane = () => { const i = lanes.indexOf(null); return i < 0 ? lanes.length : i; };
  return commits.map((c) => {
    const before = lanes.slice();
    const beforeColors = colors.slice();
    let col = lanes.indexOf(c.hash);
    if (col < 0) {
      col = freeLane();
      colors[col] = nextColor++ % GRAPH_COLORS.length;
    }
    const color = colors[col];
    // Every lane waiting for this commit ends here.
    for (let j = 0; j < lanes.length; j++) if (lanes[j] === c.hash) lanes[j] = null;
    lanes[col] = null;
    const edges = [];
    (c.parents || []).forEach((p, i) => {
      let k = lanes.indexOf(p);
      if (k < 0) {
        k = i === 0 ? col : freeLane();
        lanes[k] = p;
        if (k !== col) colors[k] = nextColor++ % GRAPH_COLORS.length;
      }
      // A merge's second parent is drawn in that branch's color.
      edges.push({ to: k, color: i === 0 ? color : colors[k] });
    });
    while (lanes.length && lanes[lanes.length - 1] === null) { lanes.pop(); colors.length = lanes.length; }
    return { commit: c, col, color, before, beforeColors, edges, after: lanes.slice(), afterColors: colors.slice() };
  });
}

function svgEl(tag, attrs) {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

const laneX = (j) => j * GRAPH_LANE_W + GRAPH_LANE_W / 2 + 2;

// lanePath is a straight line, or an S-curve between two lanes.
function lanePath(x1, y1, x2, y2) {
  if (x1 === x2) return `M${x1} ${y1}V${y2}`;
  const m = (y1 + y2) / 2;
  return `M${x1} ${y1}C${x1} ${m} ${x2} ${m} ${x2} ${y2}`;
}

// rowLanes draws one row's slice of the graph.
function rowLanes(row, width, opts) {
  const H = GRAPH_ROW_H;
  const mid = H / 2;
  const svg = svgEl("svg", { class: "graph-lanes", width, height: H, viewBox: `0 0 ${width} ${H}`, "aria-hidden": "true" });
  const line = (d, color, dashed) => svg.appendChild(svgEl("path", { d, stroke: GRAPH_COLORS[color], class: dashed ? "is-dashed" : "" }));
  const x = laneX(row.col);
  row.before.forEach((h, j) => {
    if (h === null || h === undefined) return;
    if (h === row.commit.hash) line(lanePath(laneX(j), 0, x, mid), row.beforeColors[j]);
    else line(lanePath(laneX(j), 0, laneX(j), H), row.beforeColors[j]);
  });
  for (const e of row.edges) line(lanePath(x, mid, laneX(e.to), H), e.color, opts.pseudo);
  const dot = svgEl("circle", { cx: x, cy: mid, r: opts.head ? 4.5 : 3.5, class: "graph-dot" + (opts.pseudo ? " is-pseudo" : "") + (opts.head ? " is-head" : "") });
  dot.style.setProperty("--lane", GRAPH_COLORS[row.color]);
  svg.appendChild(dot);
  return svg;
}

// gutterLanes continues the lanes going out of a row through its expanded
// details, stretched to whatever height they take.
function gutterLanes(row, width) {
  const svg = svgEl("svg", { class: "graph-lanes graph-lanes--gutter", width, height: 10, viewBox: `0 0 ${width} 10`, preserveAspectRatio: "none", "aria-hidden": "true" });
  row.after.forEach((h, j) => {
    if (h === null || h === undefined) return;
    svg.appendChild(svgEl("path", { d: `M${laneX(j)} 0V10`, stroke: GRAPH_COLORS[row.afterColors[j]], "vector-effect": "non-scaling-stroke" }));
  });
  return svg;
}

// relAge is a compact "how long ago" for a Unix time: 5m, 3h, 2d, 4w, 3mo, 2y.
function relAge(t, now = Date.now() / 1000) {
  const s = Math.max(0, now - t);
  if (s < 60) return "now";
  const steps = [[3600, 60, "m"], [86400, 3600, "h"], [86400 * 14, 86400, "d"], [86400 * 61, 86400 * 7, "w"], [86400 * 365, 86400 * 30, "mo"]];
  for (const [limit, unit, label] of steps) if (s < limit) return Math.floor(s / unit) + label;
  return Math.floor(s / (86400 * 365)) + "y";
}

function refChip(ref) {
  const cls = "graph-ref graph-ref--" + ref.kind + (ref.head ? " is-head" : "");
  return el("span", { class: cls, title: (ref.kind === "remote" ? "remote branch " : ref.kind === "tag" ? "tag " : ref.kind === "head" ? "detached HEAD" : "branch ") + (ref.kind === "head" ? "" : ref.name), text: ref.name });
}

// refChips shows the most relevant ref (the checked-out branch, else a
// local branch, else the first) and folds the rest into "+N", so the
// subject keeps its room.
function refChips(refs) {
  if (!refs.length) return [];
  const rank = (r) => (r.head || r.kind === "head" ? 0 : r.kind === "branch" ? 1 : r.kind === "tag" ? 2 : 3);
  const sorted = [...refs].sort((a, b) => rank(a) - rank(b));
  const chips = [refChip(sorted[0])];
  if (sorted.length > 1) {
    chips.push(el("span", { class: "graph-ref graph-ref--more", title: sorted.slice(1).map((r) => r.name).join("\n"), text: `+${sorted.length - 1}` }));
  }
  return chips;
}

function createGraphView({ onOpenCommitFile, onShowChanges, onMention } = {}) {
  const scopeBtns = ["branch", "all"].map((s) => el("button", { class: "graph-scope__btn", type: "button", "data-scope": s, text: s === "branch" ? "This branch" : "All branches" }));
  const rowsEl = el("div", { class: "graph-rows" });
  const root = el("div", { class: "graph" }, [el("div", { class: "graph-scope", role: "group", "aria-label": "Which commits" }, scopeBtns), rowsEl]);

  let scope = "branch";
  try { if (localStorage.getItem(GRAPH_SCOPE_KEY) === "all") scope = "all"; } catch (_) { /* best effort */ }
  let windowID = null;
  let log = null; // last /log body
  let etag = null;
  let note = "Loading…";
  let changedCount = 0; // uncommitted files, from the Files panel's poll
  let gen = 0;
  const open = new Set(); // expanded commit hashes
  const details = new Map(); // hash -> /commits/{hash} body, or {error}
  let renderedKey = null;

  const base = () => `/api/sessions/${encodeURIComponent(windowID)}`;

  function renderIfChanged() {
    const key = JSON.stringify([note, scope, etag, changedCount, [...open], [...details.keys()]]);
    if (key !== renderedKey) render(key);
  }

  function render(key) {
    renderedKey = key;
    scopeBtns.forEach((b) => b.classList.toggle("is-active", b.dataset.scope === scope));
    if (note) { rowsEl.replaceChildren(el("div", { class: "files-pane__note", text: note })); return; }
    if (!log.commits.length) { rowsEl.replaceChildren(el("div", { class: "files-pane__note", text: "No commits yet." })); return; }

    const headIdx = log.commits.findIndex((c) => c.hash === log.head);
    const commits = changedCount && headIdx >= 0
      ? [{ hash: WORKTREE, parents: [log.head], refs: [], subject: `Uncommitted changes · ${changedCount} ${changedCount === 1 ? "file" : "files"}` }, ...log.commits]
      : log.commits;
    const rows = layoutGraph(commits);
    const lanes = Math.min(GRAPH_MAX_LANES, Math.max(1, ...rows.map((r) => Math.max(r.before.length, r.after.length, r.col + 1))));
    const width = lanes * GRAPH_LANE_W + 4;
    const now = Date.now() / 1000;

    const out = [];
    for (const row of rows) {
      const c = row.commit;
      const pseudo = c.hash === WORKTREE;
      const isOpen = open.has(c.hash);
      const btn = el("button", {
        class: "graph-row" + (pseudo ? " is-pseudo" : "") + (c.hash === log.head ? " is-head" : "") + (isOpen ? " is-open" : ""),
        type: "button",
        title: pseudo ? "Show the changed files" : `${c.hash.slice(0, 7)} — ${c.author}, ${new Date(c.time * 1000).toLocaleString()}${c.unpushed ? " — not pushed" : ""}\n${c.subject}`,
        ...(pseudo ? {} : { "aria-expanded": String(isOpen) }),
      }, [
        rowLanes(row, width, { head: c.hash === log.head, pseudo }),
        el("span", { class: "graph-row__text" }, [
          ...refChips(c.refs || []),
          ...(c.unpushed ? [el("span", { class: "graph-row__unpushed", title: "Not pushed", text: "↑" })] : []),
          el("span", { class: "graph-row__subject", text: c.subject }),
        ]),
        el("span", { class: "graph-row__age", text: pseudo ? "" : relAge(c.time, now) }),
      ]);
      btn.addEventListener("click", () => {
        if (pseudo) { onShowChanges?.(); return; }
        if (open.has(c.hash)) open.delete(c.hash);
        else { open.add(c.hash); loadDetail(c.hash); }
        renderIfChanged();
      });
      out.push(btn);
      if (isOpen) out.push(detailBlock(row, width));
    }
    if (log.truncated) out.push(el("div", { class: "files-pane__note", text: `Showing the newest ${log.commits.length} commits.` }));
    rowsEl.replaceChildren(...out);
  }

  function detailBlock(row, width) {
    const c = row.commit;
    const d = details.get(c.hash);
    let body;
    if (!d) body = [el("div", { class: "graph-detail__note", text: "Loading…" })];
    else if (d.error) body = [el("div", { class: "graph-detail__note", text: d.error })];
    else {
      const short = c.hash.slice(0, 7);
      const copy = el("button", { class: "graph-detail__action", type: "button", title: "Copy the full hash", text: "Copy hash" });
      copy.addEventListener("click", async () => {
        try { await navigator.clipboard.writeText(c.hash); copy.textContent = "Copied"; } catch (_) { copy.textContent = "Couldn't copy"; }
        setTimeout(() => { copy.textContent = "Copy hash"; }, 1500);
      });
      const mention = el("button", { class: "graph-detail__action", type: "button", title: "Add this commit to your message", text: "Mention in prompt" });
      mention.addEventListener("click", () => onMention?.(`${short} ("${c.subject}")`));
      const [, ...rest] = d.message.split("\n");
      const bodyText = rest.join("\n").trim();
      body = [
        el("div", { class: "graph-detail__meta" }, [
          el("span", { class: "graph-detail__hash", text: short }),
          document.createTextNode(` ${d.author} · ${new Date(d.time * 1000).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}`),
          ...(d.parents.length > 1 ? [document.createTextNode(` · merge, compared with ${d.parents[0].slice(0, 7)}`)] : []),
        ]),
        ...(bodyText ? [el("div", { class: "graph-detail__body", text: bodyText })] : []),
        el("div", { class: "graph-detail__actions" }, [copy, mention]),
        ...(d.files.length ? d.files.map((f) => commitFileRow(c.hash, f)) : [el("div", { class: "graph-detail__note", text: "No file changes." })]),
        ...(d.truncated ? [el("div", { class: "graph-detail__note", text: "File list truncated." })] : []),
      ];
    }
    return el("div", { class: "graph-detail" }, [gutterLanes(row, width), el("div", { class: "graph-detail__main" }, body)]);
  }

  function commitFileRow(hash, f) {
    const { dir, name } = splitPath(f.path);
    const row = el("button", { class: "file-row graph-file", type: "button", title: (f.oldPath ? `${f.oldPath} → ` : "") + `${f.path} — show this commit's changes` }, [
      el("span", { class: "file-row__mark " + (FILE_MARK_CLASS[f.status] || ""), text: f.status }),
      el("span", { class: "file-row__name" }, [
        el("span", { class: "file-row__dir", text: dir }),
        el("span", { class: "file-row__base", text: name }),
      ]),
      el("span", { class: "file-row__counts" }, fileCounts(f)),
    ]);
    row.addEventListener("click", () => onOpenCommitFile?.(hash, f.path));
    return row;
  }

  async function loadDetail(hash) {
    if (details.has(hash)) return;
    const g = gen;
    let d;
    try {
      const res = await fetch(`${base()}/commits/${hash}`);
      const data = await res.json().catch(() => ({}));
      d = res.ok ? data : { error: `Couldn't read the commit: ${data.error || res.status}` };
    } catch (err) {
      d = { error: `Couldn't read the commit: ${err.message}` };
    }
    if (g !== gen) return;
    details.set(hash, d);
    renderIfChanged();
  }

  async function refresh() {
    if (!windowID) return;
    const g = gen;
    const s = scope;
    try {
      const res = await fetch(`${base()}/log?scope=${s}`, { headers: etag ? { "If-None-Match": etag } : {}, cache: "no-store" });
      if (g !== gen || s !== scope) return;
      if (res.status === 304) return;
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || `request failed (${res.status})`);
      if (!data.repo) { note = "Not a git repository."; etag = null; renderIfChanged(); return; }
      log = data;
      etag = res.headers.get("ETag");
      note = "";
      renderIfChanged();
    } catch (err) {
      if (g !== gen) return;
      if (!log) note = `Couldn't read the history: ${err.message}`;
      renderIfChanged();
    }
  }

  scopeBtns.forEach((b) => b.addEventListener("click", () => {
    if (b.dataset.scope === scope) return;
    scope = b.dataset.scope;
    try { localStorage.setItem(GRAPH_SCOPE_KEY, scope); } catch (_) { /* best effort */ }
    log = null;
    etag = null;
    note = "Loading…";
    gen++; // drop an in-flight poll for the other scope
    renderIfChanged();
    refresh();
  }));

  return {
    root,
    refresh,
    setChangedCount(n) {
      changedCount = n;
      if (log) renderIfChanged();
    },
    setWindow(id) {
      if (id === windowID) return;
      gen++;
      windowID = id;
      log = null;
      etag = null;
      note = "Loading…";
      open.clear();
      details.clear();
      renderIfChanged();
    },
  };
}
