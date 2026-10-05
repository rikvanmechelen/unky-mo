// The chat view's Overview tab: the shape of a branch's change at a glance.
// It shows how many lines are logic and how many are noise (tests,
// generated code, docs, whitespace), and how far the change spreads across
// the repo, as a treemap of areas and their files. A file list grouped by area opens branch
// diffs. An architecture section draws the package imports the change adds
// or drops (red when they break .unky-mo/architecture.toml), and a contract
// section lists changed exported API, routes, flags, config keys, env vars,
// dependencies and migrations. An intent trace maps files to the prompts
// whose edits touched them, from the transcript chat.js streams. A strip
// above the composer flags a change that breaks a layer rule or spreads
// wide. "Check scope" asks Claude (headless, on request) which files drift
// from the ticket and the prompts. Data comes from /api/sessions/{windowID}/overview and
// /architecture, polled with If-None-Match: every 3 s while the tab is
// visible, every 15 s otherwise (for the strip). Loaded after editor.js
// (storageGet/storageSet), files.js (file-row helpers) and graph.js
// (svgEl).

const OVERVIEW_POLL_MS = 3000;
const OVERVIEW_HIDDEN_KEY = "mo.overview.hidden";
const OVERVIEW_MODE_KEY = "mo.overview.mode";

// Kinds in bar order: what needs reading first, then the noise.
const OVERVIEW_KINDS = [
  { kind: "logic", label: "Logic" },
  { kind: "test", label: "Tests" },
  { kind: "generated", label: "Generated" },
  { kind: "docs", label: "Docs" },
  { kind: "format", label: "Whitespace only" },
  { kind: "renamed", label: "Renamed" },
];
const OVERVIEW_KIND_LABEL = Object.fromEntries(OVERVIEW_KINDS.map((k) => [k.kind, k.label]));
const OVERVIEW_NOISE = new Set(["generated", "format", "renamed"]);
const OVERVIEW_ALL_IMPORTS_KEY = "mo.overview.allImports";
// Whether the architecture section shows packages or functions (calls.js).
const OVERVIEW_ARCH_VIEW_KEY = "mo.overview.archView";

// Contract surface categories, in display order.
const SURFACE_KINDS = [
  { key: "exports", label: "Exported Go API" },
  { key: "routes", label: "HTTP routes" },
  { key: "flags", label: "CLI flags" },
  { key: "config", label: "Config keys" },
  { key: "env", label: "Env vars" },
  { key: "deps", label: "Dependencies" },
  { key: "migrations", label: "Migrations" },
  { key: "permissions", label: "Permissions" },
];
const SURFACE_SHOWN = 40; // entries shown per category before "N more"
const LANG_LABEL = { go: "Go", ruby: "Ruby", node: "JavaScript/TypeScript", python: "Python", kotlin: "Kotlin/Java", swift: "Swift" };
const OVERVIEW_BG_POLL_MS = 15000; // while the tab is hidden, for the strip
const TRACE_COLUMNS = 20; // turns shown as columns; older ones scroll
const AGENT_REFRESH_MS = 15000; // a running subagent's transcript is re-read this often
const EDIT_TOOLS = new Set(["Edit", "MultiEdit", "Write", "NotebookEdit"]);
const STRIP_AREAS = 4; // a change this spread out shows in the strip
const SCOPE_KEY = "mo.overview.scope."; // + windowID: the last scope check
const TICKET_RE = /[A-Z][A-Z0-9]+-\d+/;

// scopeRequestFrom builds a scope check's turns from the trace: every
// prompt (those without edits too: they say what the task is) with the
// listed files its edits touched, then (turn 0) the files changed outside
// the conversation or before its first prompt.
function scopeRequestFrom(files, trace, root) {
  const { groups } = buildTraceRows(files, trace.edits, root);
  const byTurn = new Map();
  for (const g of groups) {
    for (const r of g.rows) {
      const turns = r.turns.size ? [...r.turns] : [0];
      for (const n of turns) {
        if (!byTurn.has(n)) byTurn.set(n, []);
        byTurn.get(n).push(r.f.path);
      }
    }
  }
  const out = trace.turns.map((t) => ({ n: t.n, prompt: t.text, files: byTurn.get(t.n) || [] }));
  if (byTurn.has(0)) out.push({ n: 0, prompt: "", files: byTurn.get(0) });
  return out;
}

// createIntentTrace folds transcript lines into prompts ("turns") and the
// file edits made in each. describeUser is chat.js's describeUserString, so
// a prompt here is exactly what the transcript shows as one. Lines are
// folded in file order whatever branch they're on: an edit made on an
// abandoned branch still changed the file. Subagent lines are added with
// addAgent and land on the turn whose Agent call spawned them.
function createIntentTrace(describeUser) {
  let turns, edits, agentTurns, seen, lastNote, version;
  function reset() {
    turns = []; // [{n, uuid, text}]
    edits = []; // [{turn, path (absolute), tool, id, note, agent, hunks}]
    agentTurns = new Map(); // Agent tool_use id → turn
    seen = new Set(); // line uuids already folded (a reconnect replays them)
    lastNote = new Map(); // "" (main) or agent id → the last assistant text
    version = 0;
  }
  reset();
  const turnNow = () => turns.length; // 0 before the first prompt

  function fold(msg, agent, agentTurn) {
    const key = (agent || "") + ":" + (msg.uuid || "");
    if (msg.uuid) {
      if (seen.has(key)) return;
      seen.add(key);
    }
    const content = msg.message?.content;
    if (msg.type === "user") {
      if (Array.isArray(content)) {
        for (const b of content) {
          if (b.type === "tool_result" && b.is_error) {
            const n = edits.length;
            edits = edits.filter((e) => e.id !== b.tool_use_id);
            if (edits.length !== n) version++;
          } else if (b.type === "tool_result" && msg.toolUseResult && typeof msg.toolUseResult === "object") {
            // Which lines the edit wrote: [newStart, newLines, oldStart,
            // oldLines] per hunk; a created file is all of it.
            const e = edits.find((x) => x.id === b.tool_use_id);
            const r = msg.toolUseResult;
            if (e && Array.isArray(r.structuredPatch)) {
              e.hunks = r.type === "create" ? [[1, Infinity, 1, 0]]
                : r.structuredPatch.map((h) => [h.newStart, h.newLines, h.oldStart, h.oldLines]);
              version++;
            }
          }
        }
      }
      if (agent) return;
      if (Array.isArray(content) && content.length && content.every((b) => b.type === "tool_result")) return;
      const text = typeof content === "string" ? content
        : Array.isArray(content) ? content.filter((b) => b.type === "text").map((b) => b.text).join("\n") : "";
      const hasImage = Array.isArray(content) && content.some((b) => b.type === "image");
      if (!text && !hasImage) return;
      if (describeUser(msg, text).kind !== "user") return;
      turns.push({ n: turns.length + 1, uuid: msg.uuid, text });
      lastNote.set("", "");
      version++;
      return;
    }
    if (msg.type !== "assistant" || !Array.isArray(content)) return;
    const noteKey = agent || "";
    for (const b of content) {
      if (b.type === "text" && b.text.trim()) lastNote.set(noteKey, b.text.trim());
      if (b.type !== "tool_use") continue;
      if (!agent && (b.name === "Agent" || b.name === "Task")) agentTurns.set(b.id, turnNow());
      const p = EDIT_TOOLS.has(b.name) ? (b.input?.file_path ?? b.input?.notebook_path) : null;
      if (typeof p !== "string") continue;
      edits.push({ turn: agent ? agentTurn : turnNow(), path: p, tool: b.name, id: b.id, note: lastNote.get(noteKey) || "", agent: agent || null });
      version++;
    }
  }

  return {
    reset,
    add: (msg) => fold(msg, null, 0),
    // addAgent folds a subagent's lines; turn is that of its Agent call.
    addAgent: (agentID, turn, lines) => { for (const m of lines) fold(m, agentID, turn); },
    agentTurn: (toolUseID) => agentTurns.get(toolUseID),
    hasAgents: () => agentTurns.size > 0,
    get turns() { return turns; },
    get edits() { return edits; },
    get version() { return version; },
  };
}

// turnForRange is the turn whose edit last wrote any of lines from..to of
// file rel (repo-relative) as it is now, from the edits' hunks. A hunk's
// lines move with every later edit to the same file above it, so each is
// shifted by those edits' line deltas first: approximate, but right for the
// usual run of edits. Null when no edit with hunks touched those lines.
function turnForRange(trace, root, rel, from, to) {
  const abs = root.replace(/\/$/, "") + "/" + rel;
  const edits = trace.edits.filter((e) => e.path === abs && e.hunks);
  let best = null;
  edits.forEach((e, i) => {
    for (const [start, count] of e.hunks) {
      let a = start, b = count === Infinity ? Infinity : start + Math.max(count, 1) - 1;
      for (const later of edits.slice(i + 1)) {
        for (const [ns, nl, os, ol] of later.hunks) {
          if (nl === Infinity) { a = -1; break; } // rewritten whole: these lines are gone
          if (os + ol <= a) { const d = nl - ol; a += d; if (b !== Infinity) b += d; }
        }
      }
      if (a > 0 && a <= to && b >= from) best = e;
    }
  });
  return best ? trace.turns[best.turn - 1] || null : null;
}

// buildTraceRows lays an overview's files against the turns that edited
// them: columns are the turns with an edit to a listed file (the last
// TRACE_COLUMNS), groups are files by the turn that first edited them, with
// the files no edit in this conversation touched last (turn null).
function buildTraceRows(files, edits, root) {
  const listed = new Map(files.map((f) => [f.path, f]));
  const byPath = new Map();
  const prefix = root.replace(/\/$/, "") + "/";
  for (const e of edits) {
    if (!e.path.startsWith(prefix)) continue;
    const rel = e.path.slice(prefix.length);
    if (!listed.has(rel)) continue;
    if (!byPath.has(rel)) byPath.set(rel, []);
    byPath.get(rel).push(e);
  }
  const turnSet = new Set();
  for (const es of byPath.values()) for (const e of es) turnSet.add(e.turn);
  const columns = [...turnSet].sort((a, b) => a - b).slice(-TRACE_COLUMNS);
  const groups = new Map();
  for (const f of files) {
    const es = byPath.get(f.path) || [];
    const first = es.length ? Math.min(...es.map((e) => e.turn)) : null;
    if (!groups.has(first)) groups.set(first, []);
    groups.get(first).push({ f, edits: es, turns: new Set(es.map((e) => e.turn)) });
  }
  const order = [...groups.keys()].sort((a, b) => (a === null) - (b === null) || a - b);
  return { columns, groups: order.map((turn) => ({ turn, rows: groups.get(turn) })), traced: byPath.size };
}

// oneLine collapses whitespace and cuts text to max characters.
function oneLine(text, max) {
  const t = String(text).replace(/\s+/g, " ").trim();
  return t.length > max ? t.slice(0, max - 1) + "…" : t;
}

// summarizeOverview groups an overview's files by area, biggest first. An
// area's lines count every file at least once, so a rename or a binary file
// still gets a tile.
function summarizeOverview(o) {
  const byArea = new Map();
  for (const f of o.files || []) {
    let a = byArea.get(f.area);
    if (!a) byArea.set(f.area, (a = { area: f.area, added: 0, removed: 0, weight: 0, files: [], noise: true }));
    a.added += f.added;
    a.removed += f.removed;
    a.weight += Math.max(1, f.added + f.removed);
    a.files.push(f);
    if (!OVERVIEW_NOISE.has(f.kind)) a.noise = false;
  }
  const areas = [...byArea.values()].sort((x, y) => y.weight - x.weight || (x.area < y.area ? -1 : 1));
  return { areas, logicLines: o.kinds?.logic?.lines || 0 };
}

// layoutTreemap places items ({weight}) in the w×h box as a squarified
// treemap (Bruls et al.): rows of tiles along the shorter side, each row
// kept as square as it can be. Items must be sorted by weight, largest
// first. Returns [{item, x, y, w, h}].
function layoutTreemap(items, w, h) {
  const out = [];
  let x = 0, y = 0;
  let rest = items.filter((it) => it.weight > 0);
  let total = rest.reduce((s, it) => s + it.weight, 0);
  while (rest.length && w > 0 && h > 0) {
    const scale = (w * h) / total; // area per unit of weight
    const side = Math.min(w, h);
    const worst = (row, sum) => {
      const rowArea = sum * scale;
      let m = 0;
      for (const it of row) {
        const a = it.weight * scale;
        m = Math.max(m, (side * side * a) / (rowArea * rowArea), (rowArea * rowArea) / (side * side * a));
      }
      return m;
    };
    let row = [rest[0]], sum = rest[0].weight;
    while (row.length < rest.length) {
      const next = rest[row.length];
      if (worst([...row, next], sum + next.weight) > worst(row, sum)) break;
      row.push(next);
      sum += next.weight;
    }
    const thick = (sum * scale) / side; // the row's depth across the short side
    let off = 0;
    for (const it of row) {
      const len = (it.weight * scale) / thick;
      out.push(w >= h ? { item: it, x, y: y + off, w: thick, h: len } : { item: it, x: x + off, y, w: len, h: thick });
      off += len;
    }
    if (w >= h) { x += thick; w -= thick; } else { y += thick; h -= thick; }
    rest = rest.slice(row.length);
    total -= sum;
  }
  return out;
}

// archGraph picks what the architecture graph shows. By default that's the
// change itself: the ends of the dependencies it adds or removes, and the
// existing dependencies between them for context. With allImports, every
// touched unit and everything it depends on (dense for Rails, whose layers
// reference each other in cycles).
function archGraph(arch, allImports) {
  const status = new Map((arch.packages || []).map((p) => [p.path, p.status]));
  const nodes = new Map(); // path → {path, status}
  const add = (p) => { if (!nodes.has(p)) nodes.set(p, { path: p, status: status.get(p) || "" }); };
  if (allImports) for (const p of arch.packages || []) add(p.path);
  const edges = [];
  for (const e of arch.edges || []) { add(e.from); add(e.to); edges.push(e); }
  for (const e of arch.existing || []) {
    if (!allImports && !(nodes.has(e.from) && nodes.has(e.to))) continue;
    add(e.from); add(e.to);
    edges.push(e);
  }
  return { nodes: [...nodes.values()], edges };
}

// layoutArchGraph places packages in rows by import depth (a package sits
// below everything that imports it) and orders each row by the average x of
// its importers, so edges mostly run straight down. Cycles (an edge removed
// one way and added the other) are cut where they're found. Returns
// {rows: [[node]], depth: Map path → row}.
function layoutArchGraph(nodes, edges) {
  const parents = new Map(nodes.map((n) => [n.path, []]));
  for (const e of edges) if (parents.has(e.to) && parents.has(e.from) && e.from !== e.to) parents.get(e.to).push(e.from);
  const depth = new Map();
  const visiting = new Set();
  const depthOf = (p) => {
    if (depth.has(p)) return depth.get(p);
    if (visiting.has(p)) return 0;
    visiting.add(p);
    let d = 0;
    for (const q of parents.get(p)) d = Math.max(d, depthOf(q) + 1);
    visiting.delete(p);
    depth.set(p, d);
    return d;
  };
  const sorted = [...nodes].sort((a, b) => (a.path < b.path ? -1 : 1));
  for (const n of sorted) depthOf(n.path);
  const rows = [];
  for (const n of sorted) (rows[depth.get(n.path)] ||= []).push(n);
  const pos = new Map();
  rows.forEach((row, r) => {
    if (r > 0) {
      const bary = (n) => {
        const xs = parents.get(n.path).filter((q) => pos.has(q)).map((q) => pos.get(q));
        return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : Infinity;
      };
      row.sort((a, b) => bary(a) - bary(b) || (a.path < b.path ? -1 : 1));
    }
    row.forEach((n, i) => pos.set(n.path, (i + 0.5) / row.length));
  });
  return { rows: rows.filter(Boolean), depth };
}

// createOverview builds the tab in panel. The chat view points it at a
// session (setWindow); the reviewer view at a branch (setTarget, without a
// transcript), and learns what the branch resolved to through onTarget.
function createOverview(panel, { onOpenDiff, describeUser = () => ({ kind: "user" }), revealTurn, strip, onShowOverview, onDraftPrompt, onMention, onTarget } = {}) {
  let windowID = null; // the target's storage key (a window id, or "branch:…")
  let api = ""; // its endpoint prefix
  let withTranscript = true; // false in the reviewer view: no trace, no strip
  let available = false;
  let visible = false;
  let data = null; // the last /overview response
  let etag = null;
  let arch = null; // the last /architecture response
  let archEtag = null;
  let allImports = storageGet(OVERVIEW_ALL_IMPORTS_KEY) === true;
  let archView = storageGet(OVERVIEW_ARCH_VIEW_KEY) === "functions" ? "functions" : "packages";
  let calls = null; // the last /calls response (fetched only for the Functions view)
  let callsEtag = null;
  let callsError = "";
  let callsInflight = -1;
  const callsView = createCallsView({
    // A removed function's line is in the base version: open its diff
    // without a line.
    onOpen: (path, line, before) => openDiff({ path }, before ? undefined : line),
    onMention,
    // The prompt whose edit last changed a function (chat view only).
    changedIn: (f) => (withTranscript && data?.root && f.status && !f.before ? turnForRange(trace, data.root, f.path, f.line, f.end || f.line) : null),
    onRevealTurn: revealTurn,
  });
  let lastLoad = 0;
  let fetching = false; // a fetch of origin's base is running
  const trace = createIntentTrace(describeUser);
  let traceShown = -1; // trace.version last rendered
  let traceTimer = 0;
  let selected = null; // {path, turn} of the trace cell whose detail is shown
  const agentsRead = new Map(); // subagent id → {at, done}
  const traceBox = el("div", { class: "overview-section" });
  let scope = null; // {sig, mode, result, at} of the last check, from localStorage
  let scopeBusy = false;
  let scopeError = "";
  let providers = null; // ticket id → provider, from /api/tickets
  let inflight = -1; // the gen of the request in flight, if any
  let gen = 0; // bumped on window or mode switch; stale answers are dropped
  let areaFilter = null; // an area clicked in the treemap
  let error = "";
  let mode = storageGet(OVERVIEW_MODE_KEY) === "head" ? "head" : "branch";
  const hidden = new Set(Array.isArray(storageGet(OVERVIEW_HIDDEN_KEY)) ? storageGet(OVERVIEW_HIDDEN_KEY) : []);

  // The tab strip shows the number of rule violations next to "Overview".
  const badge = el("span", { class: "overview-badge", hidden: "" });

  const root = el("div", { class: "overview" });
  const tm = el("div", { class: "overview-treemap", role: "img" });
  panel.replaceChildren(root);

  let lastSize = "";
  new ResizeObserver(() => {
    const size = `${tm.clientWidth}x${tm.clientHeight}`;
    if (size !== lastSize && data && visible) { lastSize = size; drawTreemap(); }
  }).observe(tm);

  function note(text) {
    root.replaceChildren(el("div", { class: "overview__note", text }));
  }

  function setMode(m) {
    if (m === mode) return;
    mode = m;
    storageSet(OVERVIEW_MODE_KEY, m);
    data = null; etag = null; arch = null; archEtag = null; areaFilter = null; gen++;
    resetCalls();
    note("Loading…");
    load();
  }

  function toggleKind(kind) {
    if (hidden.has(kind)) hidden.delete(kind); else hidden.add(kind);
    storageSet(OVERVIEW_HIDDEN_KEY, [...hidden]);
    render();
  }

  function resetCalls() {
    calls = null; callsEtag = null; callsError = "";
    callsView.reset();
  }

  // removedCalled counts removed functions something still calls, from the
  // last /calls answer (only known once the Functions view was opened).
  function removedCalled() {
    return calls?.repo ? (calls.findings || []).filter((f) => f.kind === "removed-called").length : 0;
  }

  function renderBadge() {
    const v = arch?.violations || 0, r = removedCalled();
    const n = v + r;
    badge.hidden = !n;
    badge.textContent = String(n);
    const parts = [];
    if (v) parts.push(v === 1 ? "1 import breaks a layer rule" : `${v} imports break a layer rule`);
    if (r) parts.push(r === 1 ? "1 removed function is still called" : `${r} removed functions are still called`);
    badge.title = parts.join("; ");
  }

  function render() {
    renderBadge();
    if (!data) { note(error || (available ? "Loading…" : "No live session.")); return; }
    if (!data.repo) { note("This session isn't in a git checkout."); return; }
    const sum = summarizeOverview(data);
    const files = data.files || [];
    root.replaceChildren(
      header(),
      ...(files.length ? [chips(sum), noiseBar(), contracts(), withTranscript ? traceBox : scopeSection(), body(sum)] : [el("div", { class: "overview__note", text: data.mode === "branch" ? `No changes against ${data.base}.` : "No uncommitted changes." })]),
    );
    if (files.length) { drawTreemap(); if (withTranscript) renderTrace(); }
    renderStrip();
  }

  // renderStrip fills the line above the composer: shown only when the
  // change needs attention (an import breaking a layer rule, or a change
  // spread over many areas).
  function renderStrip() {
    if (!strip || !withTranscript) return;
    const parts = [];
    const v = arch?.repo ? arch.violations : 0;
    if (v) parts.push(el("span", { class: "overview-strip__bad", text: v === 1 ? "1 new import breaks a layer rule" : `${v} new imports break a layer rule` }));
    const rc = removedCalled();
    if (rc) parts.push(el("span", { class: "overview-strip__bad", text: rc === 1 ? "1 removed function is still called" : `${rc} removed functions are still called` }));
    const areas = data?.repo ? summarizeOverview(data).areas.filter((a) => !a.noise).length : 0;
    if (areas >= STRIP_AREAS) parts.push(el("span", { text: `${areas} areas touched` }));
    const drift = scope && scope.mode === data?.mode ? driftFiles().length : 0;
    if (drift) parts.push(el("span", { text: drift === 1 ? "1 file outside the ask" : `${drift} files outside the ask` }));
    strip.hidden = !parts.length;
    if (!parts.length) return;
    const open = el("button", { class: "link-btn overview-strip__open", type: "button", text: "Open Overview" });
    open.addEventListener("click", () => onShowOverview?.());
    const sig = parts.map((p) => p.textContent).join("|");
    if (strip.dataset.sig === sig) return; // unchanged: keep the button under the pointer
    strip.dataset.sig = sig;
    const items = [el("b", { text: data.mode === "branch" ? "This branch" : "Uncommitted" })];
    for (const p of parts) items.push(document.createTextNode(" · "), p);
    strip.replaceChildren(el("span", { class: "overview-strip__text" }, items), open);
  }

  // renderTrace fills the intent trace section: files × the turns whose
  // edits touched them, and the detail of the selected cell.
  function renderTrace() {
    traceShown = trace.version;
    const head = el("div", { class: "overview-section__head" }, [
      el("h3", { text: "Intent trace" }),
      el("span", { class: "overview__muted", text: "files by the prompt that first edited them" }),
      scopeButton(),
    ]);
    if (!data?.files?.length) { traceBox.replaceChildren(); return; }
    const files = data.files.filter((f) => !hidden.has(f.kind));
    const { columns, groups, traced } = buildTraceRows(files, trace.edits, data.root);
    if (!trace.turns.length && !traced) {
      traceBox.replaceChildren(head, el("div", { class: "overview__note", text: "No prompts in this conversation yet." }));
      return;
    }
    const turnText = (n) => trace.turns[n - 1]?.text || (n === 0 ? "(before the first prompt)" : "");
    const headRow = el("tr", {}, [el("th", { class: "overview-trace__file", text: `${plural(traced, "file")} edited here` })]);
    for (const n of columns) {
      const th = el("th", { class: "overview-trace__turn" });
      const b = el("button", { class: "overview-trace__turnbtn", type: "button", title: `${turnText(n)}\n\nJump to this prompt`, text: n === 0 ? "–" : String(n) });
      b.addEventListener("click", () => jumpTo(n));
      th.appendChild(b);
      headRow.appendChild(th);
    }
    const body = el("tbody", {});
    for (const g of groups) {
      const label = g.turn === null ? "Not edited in this conversation (Bash, another session, or earlier)" : `${g.turn === 0 ? "Before the first prompt" : g.turn + ". " + oneLine(turnText(g.turn), 140)}`;
      body.appendChild(el("tr", { class: "overview-trace__group" + (g.turn === null ? " is-untraced" : "") }, [el("td", { colspan: String(columns.length + 1), title: g.turn ? turnText(g.turn) : "", text: label })]));
      for (const r of g.rows) {
        const name = el("button", { class: "overview-trace__name", type: "button", title: `${r.f.path} — show changes`, text: r.f.path });
        name.addEventListener("click", () => openDiff(r.f));
        const tr = el("tr", { class: selected?.path === r.f.path ? "is-selected" : "" }, [el("td", { class: "overview-trace__file" }, [el("span", { class: "overview-trace__namewrap" }, [name, ...driftTag(r.f.path)])])]);
        for (const n of columns) {
          const td = el("td", { class: "overview-trace__cell" });
          if (r.turns.has(n)) {
            const es = r.edits.filter((e) => e.turn === n);
            const cell = el("button", {
              class: "overview-trace__mark" + (es.every((e) => e.agent) ? " is-agent" : "") + (selected?.path === r.f.path && selected?.turn === n ? " is-selected" : ""),
              type: "button",
              title: `${es.length} edit${es.length === 1 ? "" : "s"} in prompt ${n}${es.some((e) => e.agent) ? " (by a subagent)" : ""}`,
            });
            cell.addEventListener("click", () => { selected = { path: r.f.path, turn: n }; renderTrace(); });
            td.appendChild(cell);
          }
          tr.appendChild(td);
        }
        body.appendChild(tr);
      }
    }
    const table = el("table", { class: "overview-trace" }, [el("thead", {}, [headRow]), body]);
    traceBox.replaceChildren(head, ...scopeCard(), el("div", { class: "overview-trace-scroll" }, [table]), ...(selected ? [traceDetail(groups)] : []), el("div", { class: "overview-legend is-static" }, [
      el("span", {}, [el("i", { class: "overview-trace__mark is-legend" }), document.createTextNode("edited in that prompt")]),
      el("span", {}, [el("i", { class: "overview-trace__mark is-agent is-legend" }), document.createTextNode("by a subagent")]),
    ]));
  }

  function traceDetail(groups) {
    const row = groups.flatMap((g) => g.rows).find((r) => r.f.path === selected.path);
    const es = row ? row.edits.filter((e) => e.turn === selected.turn) : [];
    if (!es.length) { selected = null; return el("div"); }
    const turn = trace.turns[selected.turn - 1];
    const notes = [...new Set(es.map((e) => e.note).filter(Boolean))];
    const jump = el("button", { class: "btn btn--small btn--primary", type: "button", text: "Jump to prompt" });
    jump.addEventListener("click", () => jumpTo(selected.turn));
    jump.disabled = !turn;
    const diff = el("button", { class: "btn btn--small", type: "button", text: "Show changes" });
    diff.addEventListener("click", () => openDiff(row.f));
    const close = el("button", { class: "link-btn", type: "button", text: "Close" });
    close.addEventListener("click", () => { selected = null; renderTrace(); });
    return el("div", { class: "overview-why" }, [
      el("div", { class: "overview-why__head" }, [
        el("span", { class: "mono", text: selected.path }),
        el("span", { class: "overview__muted", text: ` · ${es.length} ${es.map((e) => e.tool).filter((t, i, a) => a.indexOf(t) === i).join("/")} in prompt ${selected.turn}` + (es.some((e) => e.agent) ? " (subagent)" : "") }),
        close,
      ]),
      ...(turn ? [el("div", { class: "overview-why__prompt", text: oneLine(turn.text, 400) })] : []),
      ...notes.slice(-2).map((n) => el("div", { class: "overview-why__note" }, [el("span", { class: "overview__muted", text: "Claude, before the edit: " }), document.createTextNode(oneLine(n, 500))])),
      el("div", { class: "overview-why__actions" }, [jump, diff]),
    ]);
  }

  // --- Scope check ---

  function scopeRequest() {
    return scopeRequestFrom(data.files || [], trace, data.root);
  }

  // scopeSig identifies what a check looked at, to tell when it's stale.
  function scopeSig() {
    return JSON.stringify([data.mode, scopeRequest()]);
  }

  function driftFiles() {
    if (!scope?.result || scope.mode !== data?.mode) return [];
    const listed = new Set((data.files || []).map((f) => f.path));
    return scope.result.files.filter((f) => f.verdict === "drift" && listed.has(f.path));
  }

  function driftTag(path) {
    if (!scope?.result || scope.mode !== data?.mode) return [];
    const v = scope.result.files.find((f) => f.path === path);
    if (!v || v.verdict === "in_scope") return [];
    return [el("span", { class: `overview-tag is-${v.verdict}`, title: v.reason, text: v.verdict === "drift" ? "drift" : "unclear" })];
  }

  function ticketKey() {
    return (data?.branch || "").match(TICKET_RE)?.[0] || "";
  }

  function scopeButton() {
    const key = ticketKey();
    const b = el("button", { class: "btn btn--small", type: "button", text: scopeBusy ? "Checking…" : key ? `Check scope against ${key}` : "Check scope", title: "Ask Claude which files drift from what was asked (no tools; usually under a minute)" });
    b.disabled = scopeBusy || !available;
    b.addEventListener("click", runScope);
    return b;
  }

  async function ticketFor(key) {
    if (!key) return null;
    if (!providers) {
      providers = new Map();
      try {
        const res = await fetch("/api/tickets", { cache: "no-store" });
        const body = await res.json();
        for (const t of body.tickets || []) providers.set(t.ID, t.Provider);
      } catch (_) { /* fall back to the default provider */ }
    }
    return { provider: providers.get(key) || "jira", id: key };
  }

  async function runScope() {
    if (scopeBusy || !data) return;
    const g = gen, id = windowID, sig = scopeSig(), m = data.mode;
    scopeBusy = true; scopeError = "";
    if (withTranscript) renderTrace(); else render();
    try {
      const res = await fetch(`${api}/scope?base=${m}`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ticket: await ticketFor(ticketKey()), turns: scopeRequest() }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(res.status === 409 ? "A check is already running for this session." : body.error || String(res.status));
      const saved = { sig, mode: m, at: Date.now(), result: { summary: body.summary, files: body.files || [] }, ticket: body.ticket || null, ticketError: body.ticketError || "", pr: body.pr || null };
      storageSet(SCOPE_KEY + id, saved);
      if (g === gen) scope = saved;
    } catch (err) {
      if (g === gen) scopeError = `Scope check failed: ${err.message}`;
    } finally {
      if (g === gen) { scopeBusy = false; render(); }
    }
  }

  function scopeAgainst() {
    const parts = [];
    if (scope.pr) parts.push(`PR #${scope.pr.id} “${scope.pr.title}”`);
    if (scope.ticket) parts.push(`${scope.ticket.id} “${scope.ticket.title}”`);
    if (parts.length) return " against " + parts.join(" and ") + (scope.ticketError ? ` (ticket not read: ${scope.ticketError})` : "");
    if (scope.ticketError) return ` without the ticket (${scope.ticketError})`;
    return withTranscript ? " against the prompts" : " from the diffs alone";
  }

  function splitPrompt(drift) {
    return "These files were changed outside what this branch is for:\n" +
      drift.map((f) => `- ${f.path}: ${f.reason}`).join("\n") +
      "\n\nPlease move those changes out of this branch (revert them here and list what to do separately), or tell me why each one is needed.";
  }

  function scopeCard() {
    if (scopeBusy) return [el("div", { class: "overview-scope is-busy", text: "Checking which files fit what was asked… (usually under a minute)" })];
    if (scopeError) return [el("div", { class: "overview-scope is-error", text: scopeError })];
    if (!scope || scope.mode !== data.mode) return [];
    const drift = driftFiles();
    const stale = scope.sig !== scopeSig();
    const when = new Date(scope.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    const parts = [
      el("div", { class: "overview-scope__head" }, [
        el("b", { text: drift.length ? `${plural(drift.length, "file")} outside the ask` : "Everything fits what was asked" }),
        el("span", { class: "overview__muted", text: ` · checked ${when}` + scopeAgainst() }),
      ]),
      ...(scope.result.summary ? [el("div", { text: scope.result.summary })] : []),
      ...(stale ? [el("div", { class: "overview__muted", text: "The change moved on since this check — run it again for an up-to-date answer." })] : []),
    ];
    if (drift.length) {
      parts.push(el("ul", { class: "overview-scope__list" }, drift.map((f) => {
        const b = el("button", { class: "link-btn overview-violation__file", type: "button", text: f.path });
        b.addEventListener("click", () => openDiff({ path: f.path }));
        return el("li", {}, [b, document.createTextNode(` — ${f.reason}`)]);
      })));
      if (onDraftPrompt) {
        const ask = el("button", { class: "btn btn--small btn--primary", type: "button", text: "Ask Claude to split these out" });
        ask.title = "Puts a prompt in the message box; nothing is sent until you send it";
        ask.addEventListener("click", () => onDraftPrompt(splitPrompt(drift)));
        parts.push(el("div", { class: "overview-why__actions" }, [ask]));
      }
    }
    return [el("div", { class: "overview-scope" + (drift.length ? " is-drift" : "") }, parts)];
  }

  // scopeSection holds the scope check in the reviewer view, where there's
  // no trace to put it in.
  function scopeSection() {
    return el("div", { class: "overview-section" }, [
      el("div", { class: "overview-section__head" }, [
        el("h3", { text: "Scope" }),
        el("span", { class: "overview__muted", text: "does every file fit what the branch is for?" }),
        scopeButton(),
      ]),
      ...scopeCard(),
    ]);
  }

  function jumpTo(n) {
    const t = trace.turns[n - 1];
    if (t?.uuid) revealTurn?.(t.uuid);
  }

  // A trace update from the transcript re-renders at most once a second,
  // and only when the trace changed and the tab is showing.
  function scheduleTrace() {
    if (traceTimer || !visible || !data || !withTranscript) return;
    traceTimer = setTimeout(() => {
      traceTimer = 0;
      if (visible && data && trace.version !== traceShown) renderTrace();
    }, 1000);
  }

  // syncAgents reads the transcripts of subagents spawned in this
  // conversation, so their edits land on the turn that spawned them.
  // Finished agents are read once, running ones every AGENT_REFRESH_MS.
  async function syncAgents(g) {
    if (!trace.hasAgents()) return;
    const res = await fetch(`${api}/subagents`, { cache: "no-store" }).catch(() => null);
    if (!res?.ok || g !== gen) return;
    const agents = await res.json().catch(() => []);
    for (const a of agents) {
      const turn = trace.agentTurn(a.tool_use_id);
      const read = agentsRead.get(a.id);
      if (turn === undefined || read?.done || (read && Date.now() - read.at < AGENT_REFRESH_MS)) continue;
      agentsRead.set(a.id, { at: Date.now(), done: !a.running });
      const r = await fetch(`${api}/subagents/${encodeURIComponent(a.id)}/transcript?once=1`, { cache: "no-store" }).catch(() => null);
      if (!r?.ok || g !== gen) return;
      trace.addAgent(a.id, turn, await r.json().catch(() => []));
    }
    scheduleTrace();
  }

  function header() {
    // A branch that isn't checked out has no uncommitted changes to show.
    const seg = el("span", { class: "overview-seg", role: "group", "aria-label": "Compare with", ...(data.head ? { hidden: "" } : {}) });
    for (const [m, label, title] of [["branch", "Branch", "Everything since this branch split off the default branch"], ["head", "Uncommitted", "Only changes not committed yet"]]) {
      const b = el("button", { class: "overview-seg__btn" + (mode === m ? " is-active" : ""), type: "button", title, text: label, "aria-pressed": String(mode === m) });
      b.addEventListener("click", () => setMode(m));
      seg.appendChild(b);
    }
    const what = data.mode === "branch"
      ? [el("b", { text: data.branch || "HEAD" }), document.createTextNode(" vs "), el("span", { class: "mono", text: data.base }), document.createTextNode(" @ "), el("span", { class: "mono", title: data.mergeBase, text: data.mergeBase.slice(0, 7) }), el("span", { class: "overview__muted", text: " (merge base)" })]
      : [el("b", { text: data.branch || "HEAD" }), document.createTextNode(" — uncommitted changes")];
    if (data.head) what.push(el("span", { class: "overview__muted", text: " · up to " }), el("span", { class: "mono", title: data.head, text: data.head.slice(0, 7) }));
    return el("div", { class: "overview-head" }, [
      el("span", { class: "overview-head__what" }, what),
      seg,
      ...(mode === "branch" && data.fallback ? [el("span", { class: "overview__muted", text: data.head ? "Nothing to compare: the branch is already part of its base." : "On the default branch, or no default branch found: showing uncommitted changes." })] : []),
      ...(data.truncated ? [el("span", { class: "overview__muted", text: "File list truncated." })] : []),
      ...staleBase(),
    ]);
  }

  // staleBase warns when origin's copy of the base is a week or more old:
  // the merge base is old too, and work already merged shows as changed.
  function staleBase() {
    if (data.mode !== "branch" || !data.baseFetched) return [];
    const days = Math.floor((Date.now() / 1000 - data.baseFetched) / 86400);
    if (days < 7) return [];
    const btn = el("button", { class: "link-btn", type: "button", text: fetching ? "Fetching…" : "Fetch" });
    btn.disabled = fetching;
    btn.addEventListener("click", fetchBase);
    return [el("span", { class: "overview-stale" }, [document.createTextNode(`${data.base} last fetched ${days} days ago · `), btn])];
  }

  async function fetchBase() {
    if (fetching) return;
    fetching = true;
    render();
    try {
      const res = await fetch(`${api}/fetch-base`, { method: "POST" });
      if (!res.ok) error = `Couldn't fetch: ${(await res.json().catch(() => ({}))).error || res.status}`;
      etag = null; archEtag = null;
    } finally {
      fetching = false;
      await load();
      render();
    }
  }

  function chip(n, label, cls) {
    return el("div", { class: "overview-chip" + (cls ? " " + cls : "") }, [
      el("span", { class: "overview-chip__n", text: n }),
      el("span", { class: "overview-chip__l", text: label }),
    ]);
  }

  function chips(sum) {
    const areas = sum.areas.filter((a) => !a.noise);
    const out = [
      chip(`+${data.added} −${data.removed}`, plural(data.files.length, "file")),
      chip(String(sum.logicLines), sum.logicLines === 1 ? "line of logic to read" : "lines of logic to read"),
      chip(plural(areas.length, "area"), areas.slice(0, 4).map((a) => a.area).join(" · ") + (areas.length > 4 ? " …" : ""), areas.length > 3 ? "is-warn" : ""),
    ];
    if (arch?.repo) {
      const added = arch.edges.filter((e) => e.op === "+").length, removed = arch.edges.length - added;
      if (arch.violations) out.push(chip(String(arch.violations), arch.violations === 1 ? "new import breaks a layer rule" : "new imports break a layer rule", "is-bad"));
      else if (arch.languages?.length) out.push(chip(`+${added} −${removed}`, "dependencies between parts"));
      const cats = SURFACE_KINDS.filter((k) => arch.surface[k.key]?.length);
      const n = cats.reduce((s, k) => s + arch.surface[k.key].length, 0);
      out.push(chip(String(n), n ? (n === 1 ? "contract change: " : "contract changes: ") + cats.map((k) => k.label.toLowerCase()).join(" · ") : "contract changes"));
    }
    const drift = driftFiles();
    if (scope && scope.mode === mode) out.push(chip(String(drift.length), drift.length === 1 ? "file outside the ask" : "files outside the ask", drift.length ? "is-warn" : ""));
    return el("div", { class: "overview-chips" }, out);
  }

  function noiseBar() {
    const kinds = OVERVIEW_KINDS.filter((k) => data.kinds?.[k.kind]?.files);
    const total = kinds.reduce((s, k) => s + Math.max(1, data.kinds[k.kind].lines), 0);
    const bar = el("div", { class: "overview-bar", role: "img", "aria-label": kinds.map((k) => `${k.label} ${data.kinds[k.kind].lines}`).join(", ") + " changed lines" });
    const legend = el("div", { class: "overview-legend" });
    for (const k of kinds) {
      const t = data.kinds[k.kind];
      const seg = el("i", { class: `overview-bar__seg is-${k.kind}` + (hidden.has(k.kind) ? " is-hidden" : ""), title: `${k.label}: ${t.lines} lines in ${plural(t.files, "file")}` });
      seg.style.flexGrow = String(Math.max(1, t.lines) / total);
      bar.appendChild(seg);
      const item = el("button", { class: "overview-legend__item" + (hidden.has(k.kind) ? " is-hidden" : ""), type: "button", title: hidden.has(k.kind) ? `Show ${k.label.toLowerCase()} files in the list` : `Hide ${k.label.toLowerCase()} files from the list`, "aria-pressed": String(!hidden.has(k.kind)) }, [
        el("i", { class: `overview-sw is-${k.kind}` }),
        document.createTextNode(`${k.label} ${t.lines}`),
      ]);
      item.addEventListener("click", () => toggleKind(k.kind));
      legend.appendChild(item);
    }
    return el("div", { class: "overview-section" }, [
      el("div", { class: "overview-section__head" }, [el("h3", { text: "What needs eyes" }), el("span", { class: "overview__muted", text: "Click a kind to hide its files" })]),
      bar, legend,
    ]);
  }

  // contracts is the architecture graph and the contract surface, side by
  // side. Until the first /architecture answer it's a placeholder.
  function contracts() {
    if (!arch) return el("div", { class: "overview__note", text: "Reading imports and contracts…" });
    if (!arch.repo) return el("div");
    // The call graph needs the width: the surface goes under it.
    return el("div", { class: "overview-body" + (archView === "functions" ? " is-stacked" : "") }, [architecture(), surfaceSection()]);
  }

  function rulesNote() {
    const r = arch.rules;
    const presets = r.presets?.length ? `built-in ${r.presets.join(", ")} rules` + (r.autoPresets ? " (detected)" : "") : "";
    if (r.error) return el("div", { class: "overview-rules is-error", text: `${r.path}: ${r.error}` + (presets ? ` — only the ${presets} applied.` : " — rules not applied.") });
    if (!r.found && !presets) return el("div", { class: "overview-rules", text: `No layer rules: add ${r.path} to flag dependencies that cross layers.` });
    if (!r.found) return el("div", { class: "overview-rules", text: `Checked against the ${presets}. Add ${r.path} to add your own layers, or presets = [] to turn these off.` });
    const own = r.layers ? `${r.path} (${plural(r.layers, "layer")})` : r.path;
    return el("div", { class: "overview-rules", text: `Checked against ${own}` + (presets ? ` and the ${presets}.` : ".") });
  }

  // viewSwitch flips the section between packages (imports) and functions
  // (calls).
  function viewSwitch() {
    const seg = el("span", { class: "overview-seg", role: "group", "aria-label": "Show" });
    for (const [v, label, title] of [["packages", "Packages", "Dependencies between parts of the code"], ["functions", "Functions", "Calls between functions"]]) {
      const b = el("button", { class: "overview-seg__btn" + (archView === v ? " is-active" : ""), type: "button", title, text: label, "aria-pressed": String(archView === v) });
      b.addEventListener("click", () => {
        if (archView === v) return;
        archView = v;
        storageSet(OVERVIEW_ARCH_VIEW_KEY, v);
        render();
        if (v === "functions") loadCalls();
      });
      seg.appendChild(b);
    }
    return seg;
  }

  function architecture() {
    if (archView === "functions") return functionsSection();
    const head = el("div", { class: "overview-section__head" }, [el("h3", { text: "Architecture" }), viewSwitch()]);
    if (!arch.languages?.length) {
      return el("div", { class: "overview-section" }, [head, el("div", { class: "overview__note", text: "No supported language in this change (Go, Ruby on Rails, JavaScript/TypeScript, Python, Kotlin/Java, Swift)." })]);
    }
    const toggle = el("label", { class: "overview__muted overview-toggle" }, [
      el("input", { type: "checkbox", id: "overview-all-imports", ...(allImports ? { checked: "" } : {}) }),
      document.createTextNode(" all dependencies"),
    ]);
    toggle.querySelector("input").addEventListener("change", (e) => {
      allImports = e.target.checked;
      storageSet(OVERVIEW_ALL_IMPORTS_KEY, allImports);
      render();
    });
    head.insertBefore(toggle, head.lastChild);
    const changed = arch.edges.length;
    const parts = [head];
    parts.push(changed || allImports ? archSvg() : el("div", { class: "overview__note", text: `No dependencies between parts of the code added or removed (${plural(arch.packages.length, "part")} touched).` }));
    const approx = arch.languages.filter((l) => !l.exact && l.units);
    if (approx.length) parts.push(el("div", { class: "overview-rules", text: `${approx.map((l) => LANG_LABEL[l.name] || l.name).join(", ")}: dependencies are inferred from names, so they're approximate (dotted).` }));
    const listed = arch.edges.filter((e) => e.violation || e.fixed);
    if (listed.length) parts.push(el("div", { class: "overview-violations" }, listed.map(violationRow)));
    parts.push(rulesNote());
    return el("div", { class: "overview-section" }, parts);
  }

  function functionsSection() {
    const head = el("div", { class: "overview-section__head" }, [el("h3", { text: "Calls" }), viewSwitch()]);
    const parts = [head];
    if (callsError && !calls) {
      parts.push(el("div", { class: "overview-rules is-error", text: `Couldn't read the calls: ${callsError}` }));
    } else if (!calls) {
      parts.push(el("div", { class: "overview__note", text: "Reading functions and calls…" }));
    } else if (!calls.repo) {
      return el("div");
    } else if (!calls.languages?.length) {
      parts.push(el("div", { class: "overview__note", text: "No functions in this change in a language the call graph reads (Go so far)." }));
    } else {
      const sum = callsSummary(calls);
      if (sum) parts.push(el("div", { class: "overview__muted", text: sum }));
      parts.push(callsView.el);
      const approx = calls.languages.filter((l) => !l.exact && l.units);
      if (approx.length) parts.push(el("div", { class: "overview-rules", text: `${approx.map((l) => LANG_LABEL[l.name] || l.name).join(", ")}: calls are inferred from names, so they're approximate (dotted).` }));
      if (calls.unparsed?.length) parts.push(el("div", { class: "overview-rules", text: `Not read (doesn't parse right now): ${calls.unparsed.join(", ")}` }));
      if (calls.truncated) parts.push(el("div", { class: "overview-rules", text: "This change is large: only part of it, or of its callers, is shown." }));
      for (const e of calls.errors || []) parts.push(el("div", { class: "overview-rules is-error", text: e }));
    }
    return el("div", { class: "overview-section" }, parts);
  }

  function violationRow(e) {
    const files = e.files.map((f) => {
      const b = el("button", { class: "link-btn overview-violation__file", type: "button", text: `${f.path}:${f.line}`, title: "Show changes" });
      b.addEventListener("click", () => openDiff({ path: f.path }, f.line));
      return b;
    });
    return el("div", { class: "overview-violation" + (e.fixed ? " is-fixed" : "") }, [
      el("span", { class: "overview-violation__edge", text: `${e.from} ${e.op === "-" ? "↛" : "→"} ${e.to}` }),
      el("span", { text: e.violation ? `breaks: ${e.violation}` : `no longer breaks: ${e.fixed}` }),
      el("span", { class: "overview-violation__files" }, files),
    ]);
  }

  // archSvg draws the graph: packages as labelled boxes in layered rows,
  // imports as curves from importer to imported.
  function archSvg() {
    const { nodes, edges } = archGraph(arch, allImports);
    const { rows } = layoutArchGraph(nodes, edges);
    const CHAR = 6.6, PAD = 10, H = 24, ROW = 64, GAP = 14, M = 8;
    const label = (n) => arch.labels?.[n.path] || n.path;
    const width = (n) => Math.max(40, label(n).length * CHAR + 2 * PAD);
    const rowW = rows.map((r) => r.reduce((s, n) => s + width(n), 0) + GAP * (r.length - 1));
    const W = Math.max(...rowW) + 2 * M;
    const box = new Map();
    rows.forEach((r, i) => {
      let x = M + (W - 2 * M - rowW[i]) / 2;
      for (const n of r) {
        box.set(n.path, { x, y: M + i * ROW, w: width(n) });
        x += width(n) + GAP;
      }
    });
    const Ht = M * 2 + (rows.length - 1) * ROW + H;
    const svg = svgEl("svg", { class: "overview-arch", viewBox: `0 0 ${W} ${Ht}`, width: W, height: Ht, role: "img", "aria-label": "Package imports: " + edges.filter((e) => e.op).map((e) => `${e.op === "+" ? "added" : "removed"} ${e.from} to ${e.to}`).join(", ") });
    const defs = svgEl("defs", {});
    const marker = svgEl("marker", { id: "ov-arrow", viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 6, markerHeight: 6, orient: "auto-start-reverse" });
    marker.appendChild(svgEl("path", { d: "M0,0 L10,5 L0,10 z", fill: "context-stroke" })); // the edge's own color
    defs.appendChild(marker);
    svg.appendChild(defs);
    // Changed edges are drawn last, on top of the faint existing ones.
    const ordered = [...edges].sort((a, b) => (a.op ? 1 : 0) - (b.op ? 1 : 0) || (a.violation ? 1 : 0) - (b.violation ? 1 : 0));
    for (const e of ordered) {
      const a = box.get(e.from), b = box.get(e.to);
      if (!a || !b) continue;
      const x1 = a.x + a.w / 2, x2 = b.x + b.w / 2;
      const down = b.y > a.y;
      const y1 = down ? a.y + H : a.y, y2 = down ? b.y : b.y + H;
      const dy = Math.max(24, Math.abs(y2 - y1) / 2) * (down ? 1 : -1);
      const cls = (e.violation ? "is-bad" : e.op === "+" ? "is-new" : e.op === "-" ? "is-removed" : "is-existing") + (e.approx ? " is-approx" : "");
      const path = svgEl("path", { class: `overview-arch__edge ${cls}`, d: `M${x1},${y1} C${x1},${y1 + dy} ${x2},${y2 - dy} ${x2},${y2}`, "marker-end": "url(#ov-arrow)" });
      const title = svgEl("title", {});
      title.textContent = `${e.from} → ${e.to}` + (e.op === "+" ? " (new)" : e.op === "-" ? " (removed)" : "") + (e.approx ? " · approximate" : "") + (e.violation ? ` — breaks: ${e.violation}` : "");
      path.appendChild(title);
      svg.appendChild(path);
    }
    for (const n of nodes) {
      const b = box.get(n.path);
      const g = svgEl("g", { class: `overview-arch__node is-${n.status || "context"}` });
      g.appendChild(svgEl("rect", { x: b.x, y: b.y, width: b.w, height: H }));
      const t = svgEl("text", { x: b.x + b.w / 2, y: b.y + H / 2 + 4, "text-anchor": "middle" });
      t.textContent = label(n);
      const title = svgEl("title", {});
      title.textContent = n.path + (n.status ? ` (${n.status})` : " (not changed)");
      g.append(title, t);
      svg.appendChild(g);
    }
    const legend = el("div", { class: "overview-legend is-static" }, [
      el("span", {}, [el("i", { class: "overview-sw is-edge-new" }), document.createTextNode("new dependency")]),
      el("span", {}, [el("i", { class: "overview-sw is-edge-removed" }), document.createTextNode("removed")]),
      el("span", {}, [el("i", { class: "overview-sw is-edge-bad" }), document.createTextNode("breaks a rule")]),
    ]);
    return el("div", { class: "overview-arch-wrap" }, [el("div", { class: "overview-arch-scroll" }, [svg]), legend]);
  }

  function surfaceSection() {
    const head = el("div", { class: "overview-section__head" }, [el("h3", { text: "Contract surface" })]);
    const cats = SURFACE_KINDS.filter((k) => arch.surface[k.key]?.length);
    if (!cats.length) {
      return el("div", { class: "overview-section" }, [head, el("div", { class: "overview__note", text: "No exported API, routes, flags, config keys, env vars, dependencies, migrations or permissions changed." })]);
    }
    const rows = cats.map((k) => {
      const all = arch.surface[k.key];
      const items = all.slice(0, SURFACE_SHOWN).map((c) => {
        const b = el("button", { class: "overview-contract", type: "button", title: `${c.path}${c.line ? ":" + c.line : ""} — show changes` }, [
          el("span", { class: `overview-contract__op is-${c.op === "+" ? "add" : c.op === "-" ? "del" : "mod"}`, text: c.op === "-" ? "−" : c.op }),
          el("span", { class: "overview-contract__name", text: c.name }),
          ...(c.detail ? [el("span", { class: "overview-contract__detail", text: c.detail })] : []),
        ]);
        b.addEventListener("click", () => openDiff({ path: c.path }, c.line));
        return b;
      });
      if (all.length > SURFACE_SHOWN) items.push(el("div", { class: "overview-group__hidden", text: `${all.length - SURFACE_SHOWN} more` }));
      return el("div", { class: "overview-surface__row" }, [
        el("div", { class: "overview-surface__kind", text: `${k.label} (${all.length})` }),
        el("div", { class: "overview-surface__items" }, items),
      ]);
    });
    return el("div", { class: "overview-section" }, [head, el("div", { class: "overview-surface" }, rows)]);
  }

  function body(sum) {
    return el("div", { class: "overview-body" }, [
      el("div", { class: "overview-section" }, [
        el("div", { class: "overview-section__head" }, [el("h3", { text: "Footprint" }), el("span", { class: "overview__muted", text: "area = lines changed" })]),
        tm,
        el("div", { class: "overview-legend is-static" }, [
          el("span", {}, [el("i", { class: "overview-sw is-added" }), document.createTextNode("mostly added")]),
          el("span", {}, [el("i", { class: "overview-sw is-removed" }), document.createTextNode("mostly removed")]),
          el("span", {}, [el("i", { class: "overview-sw is-noise" }), document.createTextNode("only noise")]),
        ]),
      ]),
      el("div", { class: "overview-section" }, [
        el("div", { class: "overview-section__head" }, [
          el("h3", { text: areaFilter ? areaFilter : "Files by area" }),
          ...(areaFilter ? [clearFilterBtn()] : []),
        ]),
        el("div", { class: "overview-files" }, fileGroups(sum)),
      ]),
    ]);
  }

  function clearFilterBtn() {
    const b = el("button", { class: "link-btn", type: "button", text: "Show all areas" });
    b.addEventListener("click", () => { areaFilter = null; render(); });
    return b;
  }

  function fileGroups(sum) {
    const out = [];
    for (const a of sum.areas) {
      if (areaFilter && a.area !== areaFilter) continue;
      const shown = a.files.filter((f) => !hidden.has(f.kind));
      out.push(el("div", { class: "overview-group" }, [
        el("span", { text: a.area === "." ? "(repo root)" : a.area }),
        el("span", { class: "file-row__counts" }, [el("span", { class: "file-row__plus", text: `+${a.added}` }), el("span", { class: "file-row__minus", text: `−${a.removed}` })]),
      ]));
      for (const f of shown) out.push(fileRow(f, a.area));
      if (shown.length < a.files.length) out.push(el("div", { class: "overview-group__hidden", text: `${plural(a.files.length - shown.length, "file")} hidden` }));
    }
    return out.length ? out : [el("div", { class: "overview__note", text: "No files." })];
  }

  function fileRow(f, area) {
    const rel = area !== "." && f.path.startsWith(area + "/") ? f.path.slice(area.length + 1) : f.path;
    const { dir, name } = splitPath(rel);
    const title = f.oldPath ? `${f.oldPath} → ${f.path}` : f.path;
    const row = el("button", { class: "file-row overview-file" + (OVERVIEW_NOISE.has(f.kind) ? " is-noise" : ""), type: "button", title: `${title} — show changes` }, [
      el("span", { class: "file-row__mark " + (FILE_MARK_CLASS[f.status] || ""), text: f.status }),
      el("span", { class: "file-row__name" }, [
        el("span", { class: "file-row__dir", text: dir }),
        el("span", { class: "file-row__base", text: name }),
        ...(f.kind !== "logic" ? [el("span", { class: `overview-tag is-${f.kind}`, text: OVERVIEW_KIND_LABEL[f.kind].toLowerCase() })] : []),
        ...driftTag(f.path),
      ]),
      el("span", { class: "file-row__counts" }, fileCounts(f)),
    ]);
    row.addEventListener("click", () => openDiff(f));
    return row;
  }

  // drawTreemap tiles the areas by changed lines, then each area's files
  // inside it, so one big area still shows where its lines are. Clicking an
  // area's label filters the file list; clicking a file opens its diff.
  function drawTreemap() {
    const sum = summarizeOverview(data);
    const W = tm.clientWidth, H = tm.clientHeight;
    lastSize = `${W}x${H}`;
    tm.setAttribute("aria-label", "Changed lines by area: " + sum.areas.map((a) => `${a.area} ${a.added + a.removed}`).join(", "));
    tm.replaceChildren(...layoutTreemap(sum.areas, W, H).map(({ item: a, x, y, w, h }) => {
      const box = el("div", { class: "overview-area" + (areaFilter === a.area ? " is-selected" : "") + (a.noise ? " is-noise" : "") });
      Object.assign(box.style, { left: x + "px", top: y + "px", width: w + "px", height: h + "px" });
      const labelH = h >= 44 && w >= 48 ? 18 : 0;
      if (labelH) {
        const label = el("button", { class: "overview-area__label", type: "button", title: `${a.area}: +${a.added} −${a.removed} in ${plural(a.files.length, "file")} — show only this area`, text: a.area === "." ? "(root)" : a.area.replace(/^internal\//, "") });
        label.addEventListener("click", () => { areaFilter = areaFilter === a.area ? null : a.area; render(); });
        box.appendChild(label);
      }
      const files = a.files.map((f) => ({ f, weight: Math.max(1, f.added + f.removed) })).sort((p, q) => q.weight - p.weight);
      for (const { item: { f }, x: fx, y: fy, w: fw, h: fh } of layoutTreemap(files, w - 2, h - 2 - labelH)) {
        const lines = f.added + f.removed;
        const share = lines ? f.added / lines : 1;
        const cls = OVERVIEW_NOISE.has(f.kind) ? "is-noise" : share >= 0.6 ? "is-added" : share <= 0.4 ? "is-removed" : "is-mixed";
        const tile = el("button", {
          class: `overview-tile ${cls}` + (fw < 40 || fh < 16 ? " is-tiny" : ""),
          type: "button",
          title: `${f.path} (${OVERVIEW_KIND_LABEL[f.kind].toLowerCase()}): +${f.added} −${f.removed} — show changes`,
        }, [el("span", { text: splitPath(f.path).name })]);
        Object.assign(tile.style, { left: 1 + fx + "px", top: 1 + labelH + fy + "px", width: fw + "px", height: fh + "px" });
        tile.addEventListener("click", () => openDiff(f));
        box.appendChild(tile);
      }
      return box;
    }));
  }

  // openDiff shows a file's changes, scrolled to line if given.
  function openDiff(f, line) {
    onOpenDiff?.(f.path, data.mode === "branch" ? "bdiff" : "diff", line);
  }

  // get fetches one endpoint with If-None-Match: null when unchanged.
  async function get(what, tag) {
    const res = await fetch(`${api}/${what}?base=${mode}`, {
      headers: tag ? { "If-None-Match": tag } : {}, cache: "no-store",
    });
    if (res.status === 304) return null;
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(body.error || String(res.status));
    return { body, etag: res.headers.get("ETag") };
  }

  async function load() {
    // A request for an older window or mode doesn't block this one; its
    // answer is dropped when it lands.
    if (!windowID || !available || inflight === gen) return;
    const g = gen;
    inflight = g;
    lastLoad = Date.now();
    try {
      const [ov, ar] = await Promise.allSettled([get("overview", etag), get("architecture", archEtag)]);
      if (g !== gen) return;
      let changed = false;
      if (ov.status === "rejected") {
        error = `Couldn't read the change: ${ov.reason.message}`;
        changed = !data;
      } else if (ov.value) {
        error = "";
        data = ov.value.body;
        etag = ov.value.etag;
        if (areaFilter && !(data.files || []).some((f) => f.area === areaFilter)) areaFilter = null;
        changed = true;
        if (data.target) onTarget?.(data.target);
      }
      // A failed analysis just leaves the sections out; the overview stands.
      if (ar.status === "fulfilled" && ar.value) {
        arch = ar.value.body;
        archEtag = ar.value.etag;
        changed = true;
      }
      if (changed) render();
      loadCalls();
      if (visible && withTranscript) await syncAgents(g);
    } finally {
      if (inflight === g) inflight = -1;
    }
  }

  // loadCalls polls /calls on its own, only while the Functions view is
  // shown: it can take seconds, and the rest of the tab shouldn't wait.
  async function loadCalls() {
    if (archView !== "functions" || !visible || !windowID || !available || callsInflight === gen) return;
    const g = gen;
    callsInflight = g;
    try {
      const res = await get("calls", callsEtag);
      if (g !== gen) return;
      callsError = "";
      if (res) {
        calls = res.body;
        callsEtag = res.etag;
        if (calls.repo) callsView.update(calls);
        render();
      }
    } catch (err) {
      if (g !== gen) return;
      callsError = err.message;
      if (!calls) render();
    } finally {
      if (callsInflight === g) callsInflight = -1;
    }
  }

  setInterval(() => {
    if (document.visibilityState !== "visible") return;
    if (visible || Date.now() - lastLoad >= OVERVIEW_BG_POLL_MS) load();
  }, OVERVIEW_POLL_MS);
  document.addEventListener("visibilitychange", () => { if (visible) load(); });

  return {
    badge,
    setWindow(id) {
      this.setTarget(id ? { key: id, api: `/api/sessions/${encodeURIComponent(id)}` } : { key: null });
    },
    // setTarget points the tab at key (stored scope checks) served under
    // api. transcript: false is the reviewer view (no trace, no strip).
    setTarget({ key: id, api: apiBase = "", transcript = true }) {
      if (id === windowID) return;
      windowID = id;
      api = apiBase;
      withTranscript = transcript;
      data = null; etag = null; arch = null; archEtag = null; areaFilter = null; error = ""; gen++;
      resetCalls();
      scope = id ? storageGet(SCOPE_KEY + id) : null;
      scopeBusy = false; scopeError = "";
      if (strip) { strip.hidden = true; delete strip.dataset.sig; }
      render();
      load();
    },
    onTranscriptLine(line) {
      trace.add(line);
      scheduleTrace();
    },
    resetTranscript() {
      trace.reset();
      agentsRead.clear();
      selected = null;
      scheduleTrace();
    },
    setAvailable(ok) {
      if (ok === available) return;
      available = ok;
      if (ok) load(); else if (!data) render();
    },
    setVisible(v) {
      visible = v;
      panel.hidden = !v;
      if (v) {
        if (data) { drawTreemap(); if (withTranscript && trace.version !== traceShown) renderTrace(); }
        load();
      }
    },
  };
}
