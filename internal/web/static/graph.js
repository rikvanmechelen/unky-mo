// Graph tab of the Files panel: the checkout's commit history drawn as a
// graph, like `git log --graph --oneline`. Commits come from
// /api/sessions/{windowID}/log (parents, refs, unpushed marks, newest first
// in topological order); layoutGraph gives each one a lane here, and every
// row draws its slice of the lines as a small SVG.
//
// "This branch" shows HEAD with its upstream and the default branch;
// "All" every branch, remote branch and tag. Clicking a commit expands
// its message and changed files (/commits/{hash}); a file calls
// onOpenCommitFile(hash, path), which opens a read-only diff tab.
// Ctrl/Cmd-click and Shift-click select commits, and the "Uncommitted
// changes" row (selectionProblem checks they're consecutive); a bar above
// the rows offers to show their change in the Overview (onShowSelection).
// Uses el() from common.js.

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

// selectionProblem says why the selected hashes aren't consecutive commits
// the Overview can show, or "" if nothing the loaded log shows is wrong.
// It mirrors gitfiles.ResolveSelection over the parents in commits (the
// log, newest first); the server has the last word, since commits past the
// log's limit aren't known here. WORKTREE (the uncommitted changes) sits on
// head, so with it selected the newest selected commit must be head.
function selectionProblem(commits, selected, head) {
  const byHash = new Map(commits.map((c) => [c.hash, c]));
  const sel = [...selected].filter((h) => byHash.has(h));
  const short = (h) => h.slice(0, 7);
  if (selected.has(WORKTREE) && sel.length && !sel.includes(head)) return `The uncommitted changes sit on ${short(head)}, which isn't selected.`;
  if (!sel.length) return "";
  const parentsOf = (h) => byHash.get(h)?.parents || [];
  const reach = (from) => {
    const seen = new Set();
    const stack = [from];
    while (stack.length) {
      const h = stack.pop();
      if (seen.has(h)) continue;
      seen.add(h);
      stack.push(...parentsOf(h));
    }
    return seen;
  };
  const isSel = new Set(sel);
  for (const h of sel) if (!parentsOf(h).length) return `${short(h)} is the first commit: there's nothing before it to compare with.`;
  const isParent = new Set(sel.flatMap(parentsOf));
  const tips = sel.filter((h) => !isParent.has(h));
  if (tips.length > 1 && !tips.some((t) => { const r = reach(t); return tips.every((o) => r.has(o)); })) {
    return "These commits are on different branches.";
  }
  // A selected commit built on head (another branch ahead of it) would put
  // the uncommitted changes in the middle.
  if (selected.has(WORKTREE) && !tips.includes(head)) return `The uncommitted changes sit on ${short(head)}, but newer commits are selected.`;
  const boundary = [...new Set(sel.flatMap(parentsOf))].filter((p) => !isSel.has(p));
  for (const b of boundary) {
    const r = reach(b);
    if (sel.some((h) => r.has(h))) return `Not consecutive: ${short(b)} isn't selected.`;
  }
  if (boundary.length > 1) {
    for (const h of sel) {
      const out = parentsOf(h).find((p) => !isSel.has(p));
      if (parentsOf(h).length > 1 && out) return `The merge ${short(h)} brings in ${short(out)}, which isn't selected.`;
    }
  }
  return "";
}

function createGraphView({ onOpenCommitFile, onShowChanges, onMention, onSelectionChange, onShowSelection } = {}) {
  const scopeBtns = ["branch", "all"].map((s) => el("button", { class: "graph-scope__btn", type: "button", "data-scope": s, text: s === "branch" ? "This branch" : "All branches" }));
  const rowsEl = el("div", { class: "graph-rows" });
  const selbar = el("div", { class: "graph-selbar", "aria-live": "polite", hidden: "" });
  const root = el("div", { class: "graph" }, [el("div", { class: "graph-scope", role: "group", "aria-label": "Which commits" }, scopeBtns), selbar, rowsEl]);

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
  const selected = new Set(); // selected commit hashes
  let anchor = null; // the row a Shift-click selects from
  let renderedKey = null;

  const base = () => `/api/sessions/${encodeURIComponent(windowID)}`;

  function renderIfChanged() {
    const key = JSON.stringify([note, scope, etag, changedCount, [...open], [...details.keys()], [...selected]]);
    if (key !== renderedKey) render(key);
  }

  // showsWorktree: the "Uncommitted changes" row is shown (there are
  // uncommitted files, and HEAD is in the log).
  const showsWorktree = () => !!log && changedCount > 0 && log.commits.some((c) => c.hash === log.head);

  // order is the selectable rows in display order (newest first).
  const order = () => (log ? [...(showsWorktree() ? [WORKTREE] : []), ...log.commits.map((c) => c.hash)] : []);

  // selection lists the selected rows newest first, with subjects.
  function selection() {
    if (!log) return [];
    return [
      ...(selected.has(WORKTREE) && showsWorktree() ? [{ hash: WORKTREE, subject: "Uncommitted changes" }] : []),
      ...log.commits.filter((c) => selected.has(c.hash)).map((c) => ({ hash: c.hash, subject: c.subject })),
    ];
  }

  function selectionChanged() {
    renderIfChanged();
    onSelectionChange?.(selection());
  }

  function toggle(hash) {
    if (selected.has(hash)) selected.delete(hash); else selected.add(hash);
    anchor = hash;
    selectionChanged();
  }

  // selectTo selects the rows from the anchor to hash (adding to the
  // selection, or replacing it).
  function selectTo(hash, add) {
    const o = order();
    const i = o.indexOf(anchor), j = o.indexOf(hash);
    if (!add) selected.clear();
    if (i < 0) { anchor = hash; selected.add(hash); }
    else for (const h of o.slice(Math.min(i, j), Math.max(i, j) + 1)) selected.add(h);
    selectionChanged();
  }

  function clearSelection() {
    if (!selected.size) return;
    selected.clear();
    selectionChanged();
  }

  function renderSelbar() {
    const sel = selection();
    selbar.hidden = !sel.length;
    if (!sel.length) { selbar.replaceChildren(); return; }
    const short = (h) => h.slice(0, 7);
    const wt = sel[0].hash === WORKTREE;
    const cs = wt ? sel.slice(1) : sel;
    const range = !cs.length ? "" : cs.length === 1 ? short(cs[0].hash) : `${short(cs[cs.length - 1].hash)}..${short(cs[0].hash)}`;
    const count = `${cs.length} ${cs.length === 1 ? "commit" : "commits"}`;
    const label = [
      document.createTextNode(wt ? (cs.length ? `Uncommitted + ${count} · ` : "Uncommitted changes") : `${count} · `),
      ...(range ? [el("span", { class: "graph-selbar__range", text: range })] : []),
    ];
    const problem = selectionProblem(log.commits, selected, log.head);
    const actions = [];
    if (onShowSelection) {
      const show = el("button", { class: "graph-detail__action", type: "button", text: "Show in Overview", title: "Show what these commits changed in the Overview tab" });
      show.disabled = !!problem;
      show.addEventListener("click", () => onShowSelection(selection()));
      actions.push(show);
    }
    const clear = el("button", { class: "graph-detail__action", type: "button", text: "Clear", title: "Clear the selection (Esc)" });
    clear.addEventListener("click", clearSelection);
    actions.push(clear);
    selbar.replaceChildren(
      el("div", { class: "graph-selbar__line" }, [el("span", { class: "graph-selbar__label" }, label), el("span", { class: "graph-selbar__actions" }, actions)]),
      ...(problem ? [el("div", { class: "graph-selbar__problem", text: problem })] : []),
    );
  }

  function render(key) {
    renderedKey = key;
    scopeBtns.forEach((b) => b.classList.toggle("is-active", b.dataset.scope === scope));
    renderSelbar();
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

    // Rows are rebuilt: keep keyboard focus on the same commit.
    const focused = rowsEl.contains(document.activeElement) ? document.activeElement.dataset?.hash : null;
    const out = [];
    for (const row of rows) {
      const c = row.commit;
      const pseudo = c.hash === WORKTREE;
      const isOpen = open.has(c.hash);
      const isSel = selected.has(c.hash);
      const btn = el("button", {
        class: "graph-row" + (pseudo ? " is-pseudo" : "") + (c.hash === log.head ? " is-head" : "") + (isOpen ? " is-open" : "") + (isSel ? " is-selected" : ""),
        type: "button",
        "data-hash": c.hash,
        title: pseudo ? "Show the changed files\nCtrl/Cmd-click or Shift-click to select" : `${c.hash.slice(0, 7)} — ${c.author}, ${new Date(c.time * 1000).toLocaleString()}${c.unpushed ? " — not pushed" : ""}\n${c.subject}\nCtrl/Cmd-click or Shift-click to select`,
        ...(pseudo ? { "aria-pressed": String(isSel) } : { "aria-expanded": String(isOpen), "aria-pressed": String(isSel) }),
      }, [
        rowLanes(row, width, { head: c.hash === log.head, pseudo }),
        el("span", { class: "graph-row__text" }, [
          ...refChips(c.refs || []),
          ...(c.unpushed ? [el("span", { class: "graph-row__unpushed", title: "Not pushed", text: "↑" })] : []),
          el("span", { class: "graph-row__subject", text: c.subject }),
        ]),
        el("span", { class: "graph-row__age", text: pseudo ? "" : relAge(c.time, now) }),
      ]);
      // Modified clicks select without the browser selecting text.
      btn.addEventListener("mousedown", (e) => { if (e.shiftKey || e.ctrlKey || e.metaKey) e.preventDefault(); });
      btn.addEventListener("click", (e) => {
        const mod = e.ctrlKey || e.metaKey;
        if (pseudo && !mod && !e.shiftKey) { anchor = c.hash; onShowChanges?.(); return; }
        if (e.shiftKey) { selectTo(c.hash, mod); return; }
        if (mod) { toggle(c.hash); return; }
        anchor = c.hash;
        if (open.has(c.hash)) open.delete(c.hash);
        else { open.add(c.hash); loadDetail(c.hash); }
        renderIfChanged();
      });
      out.push(btn);
      if (isOpen) out.push(detailBlock(row, width));
    }
    if (log.truncated) out.push(el("div", { class: "files-pane__note", text: `Showing the newest ${log.commits.length} commits.` }));
    rowsEl.replaceChildren(...out);
    if (focused) [...rowsEl.querySelectorAll(".graph-row")].find((r) => r.dataset.hash === focused)?.focus({ preventScroll: true });
  }

  // Keyboard: ↑/↓ walk the rows (with Shift, selecting from the anchor),
  // Ctrl/Cmd+Space toggles a row, Esc clears the selection.
  rowsEl.addEventListener("keydown", (e) => {
    const row = e.target.closest?.(".graph-row");
    if (!row) return;
    const hash = row.dataset.hash;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      const rows = [...rowsEl.querySelectorAll(".graph-row")];
      const next = rows[rows.indexOf(row) + (e.key === "ArrowDown" ? 1 : -1)];
      if (!next) return;
      e.preventDefault();
      next.focus();
      if (e.shiftKey) {
        if (!anchor) anchor = hash;
        selectTo(next.dataset.hash, false);
      }
    } else if (e.key === " " && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      toggle(hash);
    }
  });
  root.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && selected.size) { e.stopPropagation(); clearSelection(); }
  });

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
      // Commits the log no longer has (a rebase, another scope) drop out.
      const known = new Set(order());
      const dropped = [...selected].filter((h) => !known.has(h));
      dropped.forEach((h) => selected.delete(h));
      if (dropped.length) selectionChanged();
      else renderIfChanged();
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
    clearSelection,
    setChangedCount(n) {
      changedCount = n;
      if (!log) return;
      // Everything committed: the Uncommitted row, and its selection, go.
      if (selected.has(WORKTREE) && !showsWorktree()) { selected.delete(WORKTREE); selectionChanged(); return; }
      renderIfChanged();
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
      anchor = null;
      if (selected.size) { selected.clear(); onSelectionChange?.([]); }
      renderIfChanged();
    },
  };
}
