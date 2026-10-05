// overview-model.js — the Overview's data, without any DOM: the pure
// helpers the tab draws with (the intent trace, treemap and graph
// layouts), and the entity model the explorer selects from. buildModel
// turns the /overview, /architecture and /calls answers, the intent trace,
// the last scope check and the reviewed ticks into entities with ids
// (pkg:, file:, fn:, call:, imp:, con:, find:, prompt:; cell:<path>|<n>
// for one prompt's edits to one file is derived, not stored). The queries
// below (related, where, verdict, checks, reviewQueue, …) read a model.
// Loaded before calls.js and overview.js; Node's tests require it
// (internal/web/jstests).

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
const LANG_LABEL = { go: "Go", ruby: "Ruby", node: "JavaScript/TypeScript", python: "Python", kotlin: "Kotlin/Java", swift: "Swift" };
const TRACE_COLUMNS = 20; // turns shown as columns; older ones scroll
const EDIT_TOOLS = new Set(["Edit", "MultiEdit", "Write", "NotebookEdit"]);

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
  let turns, edits, commands, agentTurns, seen, lastNote, version;
  function reset() {
    turns = []; // [{n, uuid, text}]
    edits = []; // [{turn, path (absolute), tool, id, note, agent, hunks}]
    commands = []; // [{turn, command, id, agent}]: Bash calls, for files no edit touched
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
            const n = edits.length + commands.length;
            edits = edits.filter((e) => e.id !== b.tool_use_id);
            commands = commands.filter((c) => c.id !== b.tool_use_id);
            if (edits.length + commands.length !== n) version++;
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
      if (b.name === "Bash" && typeof b.input?.command === "string") {
        commands.push({ turn: agent ? agentTurn : turnNow(), command: b.input.command, id: b.id, agent: agent || null });
        version++;
        continue;
      }
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
    get commands() { return commands; },
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

const CALL_MARK = { added: "+", removed: "−", renamed: "↦", signature: "sig", changed: "~" };
const CALL_STATUS_TEXT = {
  added: "added", removed: "removed", renamed: "renamed", signature: "signature changed", changed: "body changed",
};
const CALLS_COLLAPSE_AT = 60; // more changed functions than this start grouped by unit
const CALL_FINDING_TEXT = {
  "removed-called": "removed but still called",
  "stimulus-unbound": "binds a Stimulus method or target that doesn't exist",
  "route-without-action": "routes to an action its controller doesn't define",
  "signature-callers": "signature changed; callers not updated",
  untested: "no test reaches it",
  "test-not-updated": "its test file wasn't updated",
};


// ── Entity model ───────────────────────────────────────────────

// How much a call finding matters: red breaks something, warn is worth a
// look, fold is a hint shown folded.
const FINDING_SEVERITY = {
  "removed-called": "red", "stimulus-unbound": "red", "route-without-action": "red",
  "signature-callers": "warn", "test-not-updated": "warn", untested: "fold",
};
const SEVERITY_RANK = { red: 0, warn: 1, fold: 2 };

function pl(n, one, many) {
  return `${n} ${n === 1 ? one : many || one + "s"}`;
}

// reviewSig identifies the version of a file a reviewed tick was given to:
// a file that changes after it was ticked loses its tick.
function reviewSig(f) {
  return `${f.status}:${f.added}:${f.removed}`;
}

// cellID names one prompt's edits to one file (the intent matrix's cells).
function cellID(path, n) {
  return `cell:${path}|${n}`;
}

function parseCell(id) {
  const i = id.lastIndexOf("|");
  return { path: id.slice(5, i), n: Number(id.slice(i + 1)) };
}

// buildModel builds the entity model. Only overview is required: arch,
// calls, trace and scope may be null while they load (or don't apply).
// reviewed maps a path to the reviewSig its tick was given at.
function buildModel({ overview, arch = null, calls = null, trace = null, scope = null, reviewed = {} }) {
  const E = new Map();
  const put = (e) => { E.set(e.id, e); return e; };
  const root = overview?.root || "";
  const files = overview?.files || [];
  const fileUnits = arch?.fileUnits || {};
  const layers = { ...(calls?.unitLayers || {}), ...(arch?.unitLayers || {}) };
  const labels = arch?.labels || {};
  const changedPkg = new Map((arch?.packages || []).map((p) => [p.path, p]));
  const order = { find: [], imp: [], con: [], files: [], pkgs: [] };
  const orphans = [];

  const pkg = (unit, lang) => {
    const id = "pkg:" + unit;
    let e = E.get(id);
    if (!e) {
      const p = changedPkg.get(unit);
      e = put({ type: "pkg", id, unit, label: labels[unit] || unit || "(root)", status: p ? p.status : "context", lang: p?.lang || lang || "", layer: layers[unit] || "", fns: [], files: [] });
      order.pkgs.push(id);
    }
    return e;
  };
  for (const p of arch?.packages || []) pkg(p.path, p.lang);

  // Files, by their path and (for a rename) their old one.
  const fileByPath = new Map(), fileByOld = new Map();
  const drift = new Map();
  if (scope?.result && scope.mode === overview?.mode) {
    for (const f of scope.result.files || []) if (f.verdict && f.verdict !== "in_scope") drift.set(f.path, { verdict: f.verdict, reason: f.reason || "" });
  }
  for (const f of files) {
    const i = f.path.lastIndexOf("/");
    const unit = Object.hasOwn(fileUnits, f.path) ? fileUnits[f.path] : null;
    const e = put({
      type: "file", id: "file:" + f.path, path: f.path, oldPath: f.oldPath || "", status: f.status, added: f.added, removed: f.removed,
      binary: !!f.binary, kind: f.kind, area: f.area, name: f.path.slice(i + 1), dir: f.path.slice(0, i + 1),
      unit, fns: [], prompts: [], drift: drift.get(f.path) || null, outside: false, origin: null,
      reviewed: !!reviewed && reviewed[f.path] === reviewSig(f),
    });
    fileByPath.set(f.path, e.id);
    if (f.oldPath) fileByOld.set(f.oldPath, e.id);
    if (unit !== null) pkg(unit).files.push(e.id);
    order.files.push(e.id);
  }

  // Functions.
  const fns = [];
  for (const f of calls?.funcs || []) {
    const unit = f.unit || "";
    const e = put({
      type: "fn", id: "fn:" + f.id, fid: f.id, name: f.name || f.id, path: f.path || "", line: f.line || 0, end: f.end || f.line || 0,
      before: !!f.before, unit, lang: f.lang || "", status: f.status || "", mark: CALL_MARK[f.status] || "", from: f.from || "",
      test: !!f.test, unresolved: f.unresolved || 0, moreCallers: f.moreCallers || 0, testedBy: f.testedBy || null,
      sig: f.sig || "", oldSig: f.oldSig || "",
      callers: [], callees: [], impls: [], implOf: [], findings: [], contracts: [], turn: null, fileId: null,
    });
    e.fileId = (e.before && fileByOld.get(e.path)) || fileByPath.get(e.path) || null;
    pkg(unit, e.lang).fns.push(e.id);
    if (e.fileId) E.get(e.fileId).fns.push(e.id);
    if (trace && root && e.status && !e.before && e.path) e.turn = turnForRange(trace, root, e.path, e.line, e.end)?.n ?? null;
    fns.push(e);
  }

  // Calls; an impl edge links an interface method and an implementation.
  for (const c of calls?.calls || []) {
    const from = "fn:" + c.from, to = "fn:" + c.to;
    if (c.kind === "impl") {
      E.get(from)?.impls.push(to);
      E.get(to)?.implOf.push(from);
      continue;
    }
    const id = `call:${c.from}>${c.to}|${c.kind}|${c.op || ""}`;
    put({ type: "call", id, fromId: from, toId: to, kind: c.kind, op: c.op || "", sites: c.sites || [], label: c.label || "", findings: [] });
    E.get(from)?.callees.push(id);
    E.get(to)?.callers.push(id);
  }

  // Findings: on their function (a renamed function's old name is found
  // through the new one), and on the calls at their sites.
  const finds = [];
  (calls?.findings || []).forEach((x, i) => {
    let fnId = E.has("fn:" + x.func) ? "fn:" + x.func : null, renamed = false;
    if (!fnId) {
      const r = fns.find((g) => g.from === x.func);
      if (r) { fnId = r.id; renamed = true; }
    }
    const e = put({ type: "find", id: "find:" + i, kind: x.kind, sev: FINDING_SEVERITY[x.kind] || "warn", fnId, func: x.func, renamed, sites: x.sites || [], calls: [] });
    if (fnId) {
      const f = E.get(fnId);
      f.findings.push(e.id);
      for (const cid of f.callers) {
        const c = E.get(cid);
        if (c.sites.some((s) => e.sites.some((t) => t.path === s.path && t.line === s.line))) {
          e.calls.push(cid);
          c.findings.push(e.id);
        }
      }
    }
    finds.push(e);
  });
  order.find = finds.slice().sort((a, b) => SEVERITY_RANK[a.sev] - SEVERITY_RANK[b.sev]).map((f) => f.id);

  // Imports: the change's, and the existing ones around them.
  const impKind = (ed) => (ed.violation ? "broken" : ed.fixed ? "fixed" : ed.op === "+" ? "new" : ed.op === "-" ? "removed" : "ctx");
  for (const [list, changed] of [[arch?.edges || [], true], [arch?.existing || [], false]]) {
    for (const ed of list) {
      pkg(ed.from, ed.lang);
      pkg(ed.to, ed.lang);
      const id = `imp:${ed.from}>${ed.to}|${ed.op || ""}`;
      if (E.has(id)) continue;
      put({
        type: "imp", id, fromId: "pkg:" + ed.from, toId: "pkg:" + ed.to, from: ed.from, to: ed.to, op: ed.op || "",
        kind: changed ? impKind(ed) : "ctx", violation: ed.violation || "", fixed: ed.fixed || "", approx: !!ed.approx,
        files: ed.files || [], lang: ed.lang || "", changed,
      });
      if (changed) order.imp.push(id);
    }
  }

  // Contracts, each on the function that defines it when one does.
  const fnAt = (path, line, old) => {
    let best = null;
    for (const f of fns) {
      if (f.path !== path || f.before !== old || !line || line < f.line || line > f.end) continue;
      if (!best || f.end - f.line < best.end - best.line) best = f;
    }
    return best?.id || null;
  };
  for (const k of SURFACE_KINDS) {
    (arch?.surface?.[k.key] || []).forEach((c, i) => {
      const id = `con:${k.key}:${i}`;
      const fnId = fnAt(c.path, c.line, c.op === "-");
      put({ type: "con", id, cat: k.key, catLabel: k.label, op: c.op, name: c.name, detail: c.detail || "", path: c.path || "", line: c.line || 0, fnId, fileId: fileByPath.get(c.path) || null });
      if (fnId) E.get(fnId).contracts.push(id);
      order.con.push(id);
    });
  }

  // Prompts and the files their edits touched.
  if (trace) {
    for (const t of trace.turns) put({ type: "prompt", id: "prompt:" + t.n, n: t.n, uuid: t.uuid, text: t.text, files: [] });
    const { groups } = buildTraceRows(files, trace.edits, root);
    for (const g of groups) {
      for (const r of g.rows) {
        const fe = E.get("file:" + r.f.path);
        if (!r.turns.size) {
          fe.outside = true;
          fe.origin = ovGuessOrigin(fe, trace.commands);
          orphans.push(fe.id);
          continue;
        }
        for (const n of [...r.turns].sort((a, b) => a - b)) {
          const es = r.edits.filter((x) => x.turn === n);
          const item = {
            n, path: r.f.path, edits: es.length, agent: es.every((x) => x.agent),
            tools: [...new Set(es.map((x) => x.tool))], notes: [...new Set(es.map((x) => x.note).filter(Boolean))],
          };
          fe.prompts.push(item);
          if (n === 0 && !E.has("prompt:0")) put({ type: "prompt", id: "prompt:0", n: 0, uuid: "", text: "", files: [] });
          E.get("prompt:" + n)?.files.push(item);
        }
      }
    }
  }

  return {
    E, order, orphans, overview, arch, calls, traced: !!trace,
    scoped: drift.size > 0 || !!(scope?.result && scope.mode === overview?.mode),
    loaded: { arch: !!arch, calls: !!calls },
  };
}

// ovFileOf is the file entity of path (a function's own, or a site's), if
// the change lists it; old matches a rename's old path too.
function ovFileOf(M, path, old) {
  if (!path) return null;
  if (M.E.has("file:" + path)) return "file:" + path;
  if (old) for (const e of M.E.values()) if (e.type === "file" && e.oldPath === path) return e.id;
  return null;
}

// ovRelated is the set of entities connected to id: what the map, the
// Review list and the inspector highlight while id is hovered or
// selected. It always holds id itself (if it exists).
function ovRelated(M, id) {
  const E = M.E, s = new Set();
  const add = (x) => { if (x && E.has(x)) s.add(x); };
  const addFn = (f) => {
    const e = E.get(f);
    if (!e) return;
    s.add(f);
    add("pkg:" + e.unit);
    add(e.fileId);
  };
  const addSites = (sites, old) => { for (const t of sites || []) add(ovFileOf(M, t.path, old)); };
  if (!id) return s;
  if (id.startsWith("cell:")) {
    const { path, n } = parseCell(id);
    add("file:" + path);
    add("prompt:" + n);
    const fe = E.get("file:" + path);
    if (fe && fe.unit !== null) add("pkg:" + fe.unit);
    s.add(id);
    return s;
  }
  const e = E.get(id);
  if (!e) return s;
  s.add(id);
  switch (e.type) {
    case "fn":
      addFn(id);
      for (const c of e.callers) { add(c); addFn(E.get(c).fromId); }
      for (const c of e.callees) { add(c); addFn(E.get(c).toId); }
      for (const f of [...e.impls, ...e.implOf]) addFn(f);
      for (const x of [...e.findings, ...e.contracts]) add(x);
      if (e.turn !== null) add("prompt:" + e.turn);
      break;
    case "pkg":
      for (const x of [...e.fns, ...e.files]) add(x);
      for (const x of E.values()) {
        if (x.type === "imp" && x.changed && (x.fromId === id || x.toId === id)) { add(x.id); add(x.fromId); add(x.toId); }
      }
      break;
    case "file":
      if (e.unit !== null) add("pkg:" + e.unit);
      for (const f of e.fns) {
        add(f);
        for (const c of [...E.get(f).callers, ...E.get(f).callees]) add(c);
      }
      for (const p of e.prompts) add("prompt:" + p.n);
      for (const x of E.values()) {
        if (x.type === "con" && x.fileId === id) add(x.id);
        else if (x.type === "find" && x.fnId && E.get(x.fnId).fileId === id) add(x.id);
        else if (x.type === "imp" && x.changed && x.files.some((t) => t.path === e.path)) { add(x.id); add(x.fromId); add(x.toId); }
        else if (x.type === "call" && x.sites.some((t) => t.path === e.path)) { add(x.id); addFn(x.fromId); addFn(x.toId); }
      }
      break;
    case "call":
      addFn(e.fromId);
      addFn(e.toId);
      addSites(e.sites, e.op === "-");
      for (const x of e.findings) add(x);
      break;
    case "imp": {
      add(e.fromId);
      add(e.toId);
      addSites(e.files, e.op === "-");
      for (const x of E.values()) {
        if (x.type !== "call") continue;
        const a = E.get(x.fromId), b = E.get(x.toId);
        if (a && b && a.unit === e.from && b.unit === e.to) { add(x.id); addFn(x.fromId); addFn(x.toId); }
      }
      break;
    }
    case "con":
      addFn(e.fnId);
      add(e.fileId);
      if (e.fileId) { const fe = E.get(e.fileId); if (fe.unit !== null) add("pkg:" + fe.unit); }
      break;
    case "find":
      addFn(e.fnId);
      for (const c of e.calls) { add(c); addFn(E.get(c).fromId); }
      addSites(e.sites, false);
      break;
    case "prompt":
      for (const f of e.files) {
        const fe = E.get("file:" + f.path);
        add(fe?.id);
        if (fe && fe.unit !== null) add("pkg:" + fe.unit);
      }
      for (const x of E.values()) if (x.type === "fn" && x.turn === e.n) addFn(x.id);
      break;
  }
  return s;
}

// ovWhere is the place to open for id: {path, line, side} (side "old" when
// the line is in the base version), or null.
function ovWhere(M, id) {
  if (!id) return null;
  if (id.startsWith("cell:")) return { path: parseCell(id).path, line: 0, side: "new" };
  const e = M.E.get(id);
  if (!e) return null;
  const at = (t, old) => (t ? { path: t.path, line: t.line || 0, side: old ? "old" : "new" } : null);
  switch (e.type) {
    case "fn": return e.path ? { path: e.path, line: e.line, side: e.before ? "old" : "new" } : null;
    case "file": return { path: e.path, line: 0, side: e.status === "D" ? "old" : "new" };
    case "call": return at(e.sites[0], e.op === "-");
    case "imp": return at(e.files[0], e.op === "-");
    case "con": return (e.fnId && ovWhere(M, e.fnId)) || (e.path ? { path: e.path, line: e.line, side: e.op === "-" ? "old" : "new" } : null);
    case "find": return at(e.sites[0], false) || ovWhere(M, e.fnId);
    case "pkg": return ovWhere(M, e.files[0]) || ovWhere(M, e.fns[0]);
    case "prompt": return e.files[0] ? { path: e.files[0].path, line: 0, side: "new" } : null;
  }
  return null;
}

// ovLabel is a short name for id, for breadcrumbs.
function ovLabel(M, id) {
  if (!id) return "";
  if (id.startsWith("cell:")) {
    const { path, n } = parseCell(id);
    return `${path.split("/").pop()} × ${n === 0 ? "—" : n}`;
  }
  const e = M.E.get(id);
  if (!e) return "";
  switch (e.type) {
    case "fn": return e.name;
    case "pkg": return e.unit.split("/").pop() || "(root)";
    case "file": return e.name;
    case "con": return e.name;
    case "call": return `${M.E.get(e.fromId)?.name || "?"} → ${M.E.get(e.toId)?.name || "?"}`;
    case "imp": return `${e.from.split("/").pop()} → ${e.to.split("/").pop()}`;
    case "find": return "finding";
    case "prompt": return e.n === 0 ? "before the first prompt" : `prompt ${e.n}`;
  }
  return "";
}

// ovSection is the Overview section an entity lives in.
function ovSection(M, id) {
  if (!id) return null;
  if (id.startsWith("cell:") || id.startsWith("prompt:")) return "trace";
  if (id.startsWith("file:")) return "foot";
  return "map";
}

// ovAreas lists the change's areas that aren't only noise, biggest first.
function ovAreas(M) {
  return summarizeOverview(M.overview || {}).areas.filter((a) => !a.noise).map((a) => a.area);
}

const ovByType = (M, type) => [...M.E.values()].filter((e) => e.type === type);
const ovChangedFns = (M) => ovByType(M, "fn").filter((f) => f.status);
const ovLogicFiles = (M) => ovByType(M, "file").filter((f) => f.kind === "logic").sort((a, b) => b.added + b.removed - (a.added + a.removed));

// ovVerdict is the sentence at the top of the tab, as segments {t, tone,
// target}: tone red/warn/ink or null, target an entity to select. ticket
// names what the scope check compared with ("OP-212").
function ovVerdict(M, { ticket = "", filtered = false } = {}) {
  const segs = [];
  const seg = (t, tone = null, target = null) => segs.push({ t, tone, target });
  const areas = ovAreas(M);
  if (areas.length <= 1) seg((filtered ? "Focused on " : "A focused change in ") + (areas[0] ? (areas[0] === "." ? "the repo root" : areas[0]) : "nothing yet"));
  else if (areas.length <= 3) seg((filtered ? "Across " : "A change across ") + pl(areas.length, "area"));
  else { seg("Spread across "); seg(pl(areas.length, "area"), "warn"); }
  const issues = [];
  const broken = M.order.imp.filter((id) => M.E.get(id).kind === "broken");
  const finds = M.order.find.map((id) => M.E.get(id));
  const red = finds.filter((f) => f.sev === "red");
  const sigs = finds.filter((f) => f.kind === "signature-callers");
  const stale = finds.filter((f) => f.kind === "test-not-updated");
  if (broken.length) issues.push([pl(broken.length, "broken layer rule"), "red", broken[0]]);
  if (red.length) issues.push([pl(red.length, "call that won’t work", "calls that won’t work"), "red", red[0].id]);
  if (sigs.length) issues.push([pl(sigs.length, "signature change") + " with callers left behind", "warn", sigs[0].id]);
  if (stale.length) issues.push([pl(stale.length, "file") + " whose tests didn’t change", "warn", stale[0].id]);
  if (M.order.con.length) issues.push([pl(M.order.con.length, "contract change"), "ink", M.order.con[0]]);
  const drift = ovByType(M, "file").filter((f) => f.drift?.verdict === "drift");
  if (drift.length) issues.push([pl(drift.length, "file") + " outside " + (ticket || "the ask"), "warn", drift[0].id]);
  if (issues.length) {
    seg(", with ");
    issues.forEach(([t, tone, target], i) => {
      seg(t, tone, target);
      if (i < issues.length - 2) seg(", ");
      else if (i === issues.length - 2) seg(" and ");
    });
    seg(".");
  } else if (!M.loaded.arch || !M.loaded.calls) {
    seg(". Checking imports and calls…");
  } else {
    seg(". Nothing needs your attention.");
  }
  return segs;
}

// ovVerdictSub is the line under the verdict.
function ovVerdictSub(M) {
  const o = M.overview || {};
  const logic = o.kinds?.logic?.lines || 0, total = (o.added || 0) + (o.removed || 0);
  return `${logic} of ${total} changed lines are logic.`;
}

// ovRulesText describes the layer rules the analysis checked against, and
// whether that's an error.
function ovRulesText(rules) {
  if (!rules) return { text: "", error: false };
  const presets = rules.presets?.length ? `built-in ${rules.presets.join(", ")} rules` + (rules.autoPresets ? " (detected)" : "") : "";
  if (rules.error) return { text: `${rules.path}: ${rules.error}` + (presets ? ` — only the ${presets} applied.` : " — rules not applied."), error: true };
  if (!rules.found && !presets) return { text: `No layer rules: add ${rules.path} to flag dependencies that cross layers.`, error: false };
  if (!rules.found) return { text: `Checked against the ${presets}. Add ${rules.path} to add your own layers, or presets = [] to turn these off.`, error: false };
  const own = rules.layers ? `${rules.path} (${pl(rules.layers, "layer")})` : rules.path;
  return { text: `Checked against ${own}` + (presets ? ` and the ${presets}.` : "."), error: false };
}

// ovChecks are the "Start here" list: {tone (red/warn/ink/calm/pending),
// label, sub, target}.
function ovChecks(M, { filtered = false } = {}) {
  const out = [];
  const logic = ovLogicFiles(M);
  const lines = logic.reduce((s, f) => s + f.added + f.removed, 0);
  out.push({ tone: logic.length > 12 ? "warn" : "calm", label: pl(lines, "line") + " of logic to read", sub: `${pl(logic.length, "logic file")} in Review, biggest first`, target: logic[0]?.id || null });
  const areas = ovAreas(M);
  out.push({ tone: areas.length > 3 ? "warn" : "calm", label: pl(areas.length, "area"), sub: areas.slice(0, 4).join(" · ") + (areas.length > 4 ? " …" : ""), target: null });
  if (!M.loaded.arch) out.push({ tone: "pending", label: "Reading imports…", sub: "", target: null });
  else {
    const broken = M.order.imp.filter((id) => M.E.get(id).kind === "broken");
    out.push(broken.length
      ? { tone: "red", label: pl(broken.length, "new import breaks", "new imports break") + " a layer rule", sub: ovRulesText(M.arch.rules).text, target: broken[0] }
      : { tone: "calm", label: "No layer rules broken", sub: ovRulesText(M.arch.rules).text, target: null });
  }
  if (!M.loaded.calls) out.push({ tone: "pending", label: "Reading calls…", sub: "", target: null });
  else {
    const finds = M.order.find.map((id) => M.E.get(id)).filter((f) => f.sev !== "fold");
    const red = finds.filter((f) => f.sev === "red");
    const names = finds.map((f) => (f.fnId ? M.E.get(f.fnId).name : f.func)).slice(0, 4).join(", ");
    out.push(red.length ? { tone: "red", label: pl(red.length, "call that won’t work", "calls that won’t work"), sub: names, target: red[0].id }
      : finds.length ? { tone: "warn", label: pl(finds.length, "call-graph warning"), sub: names, target: finds[0].id }
      : { tone: "calm", label: "No call-graph findings", sub: "Static analysis only", target: null });
  }
  if (M.loaded.arch) {
    const cons = M.order.con.map((id) => M.E.get(id));
    out.push(cons.length ? { tone: "ink", label: pl(cons.length, "contract change"), sub: [...new Set(cons.map((c) => c.catLabel))].join(" · "), target: cons[0].id }
      : { tone: "calm", label: "No contract changes", sub: "", target: null });
  }
  if (M.traced && !filtered) {
    out.push(M.orphans.length
      ? { tone: "ink", label: pl(M.orphans.length, "file") + " changed outside the conversation", sub: M.orphans.map((id) => M.E.get(id).name).slice(0, 4).join(", "), target: M.orphans[0] }
      : { tone: "calm", label: "Every file traces to a prompt", sub: "", target: null });
  }
  return out;
}

// ovCaveats are what the analysis couldn't see or only guessed: {tone
// (red/warn/note), text, target}.
function ovCaveats(M) {
  const out = [];
  if (M.arch?.rules) {
    const r = ovRulesText(M.arch.rules);
    out.push({ tone: r.error ? "red" : "note", text: "Layer rules: " + r.text, target: null });
  }
  const approx = new Set();
  for (const l of [...(M.arch?.languages || []), ...(M.calls?.languages || [])]) if (!l.exact && l.units) approx.add(LANG_LABEL[l.name] || l.name);
  if (approx.size) out.push({ tone: "note", text: `${[...approx].join(", ")}: imports and calls are inferred from names, so they’re approximate (dotted).`, target: null });
  for (const p of M.calls?.unparsed || []) out.push({ tone: "warn", text: `${p} doesn’t parse right now, so its functions are left out.`, target: ovFileOf(M, p, true) });
  const unresolved = ovChangedFns(M).reduce((s, f) => s + f.unresolved, 0);
  if (unresolved) out.push({ tone: "note", text: `${pl(unresolved, "call")} in the changed functions couldn’t be resolved, so they aren’t drawn.`, target: null });
  if (ovChangedFns(M).length > 60) out.push({ tone: "warn", text: "More than 60 functions changed, so packages start folded. Open one to see its functions.", target: null });
  if (M.overview?.truncated) out.push({ tone: "warn", text: "The file list is truncated.", target: null });
  if (M.arch?.truncated || M.calls?.truncated) out.push({ tone: "warn", text: "This change is large: only part of it, or of its callers, was analysed.", target: null });
  for (const e of M.calls?.errors || []) out.push({ tone: "red", text: e, target: null });
  return out;
}

// ovChips are the header's counts: {t, target}.
function ovChips(M) {
  const o = M.overview || {};
  const out = [
    { t: pl((o.files || []).length, "file"), target: null },
    { t: `+${o.added || 0} −${o.removed || 0} lines`, target: null },
    { t: pl(ovAreas(M).length, "area"), target: null },
  ];
  if (M.loaded.arch) {
    const imps = M.order.imp.map((id) => M.E.get(id));
    const plus = imps.filter((e) => e.op === "+").length, minus = imps.filter((e) => e.op === "-").length;
    if (plus || minus) out.push({ t: `+${plus} −${minus} imports between parts`, target: M.order.imp[0] });
    const deps = M.order.con.map((id) => M.E.get(id)).filter((c) => c.cat === "deps");
    if (deps.length) {
      const n = (op) => deps.filter((d) => d.op === op).length;
      out.push({ t: `+${n("+")} −${n("-")} dependencies` + (n("~") ? ` · ${n("~")} bumped` : ""), target: deps[0].id });
    }
    if (M.order.con.length) out.push({ t: pl(M.order.con.length, "contract change"), target: M.order.con[0] });
  }
  return out;
}

// ovReviewQueue is the Review list's groups: {key, label, collapsible,
// ids}. area and hidden (a Set of kinds) filter the file rows.
function ovReviewQueue(M, { area = null, hidden = new Set() } = {}) {
  const E = M.E;
  const keep = (f) => (!area || f.area === area) && !hidden.has(f.kind);
  const files = ovByType(M, "file").filter(keep);
  const finds = M.order.find.map((id) => E.get(id));
  const groups = [];
  const needs = [
    ...finds.filter((f) => f.sev === "red").map((f) => f.id),
    ...M.order.imp.filter((id) => E.get(id).kind === "broken"),
    ...finds.filter((f) => f.sev === "warn").map((f) => f.id),
    ...files.filter((f) => f.drift?.verdict === "drift").map((f) => f.id),
  ];
  if (needs.length) groups.push({ key: "needs", label: "Needs eyes", collapsible: false, ids: needs });
  if (M.order.con.length) groups.push({ key: "con", label: "Contracts", collapsible: false, ids: M.order.con });
  const logic = files.filter((f) => f.kind === "logic").sort((a, b) => b.added + b.removed - (a.added + a.removed));
  groups.push({ key: "logic", label: "Logic to read", collapsible: false, ids: logic.map((f) => f.id) });
  const tests = [...files.filter((f) => f.kind === "test").map((f) => f.id), ...finds.filter((f) => f.sev === "fold").map((f) => f.id)];
  if (tests.length) groups.push({ key: "tests", label: "Tests", collapsible: true, ids: tests });
  const docs = files.filter((f) => f.kind === "docs").map((f) => f.id);
  if (docs.length) groups.push({ key: "docs", label: "Docs", collapsible: true, ids: docs });
  const noise = files.filter((f) => OVERVIEW_NOISE.has(f.kind)).map((f) => f.id);
  if (noise.length) groups.push({ key: "noise", label: "Generated and noise", collapsible: true, ids: noise });
  return groups;
}

// ovProgress counts the logic files ticked as reviewed.
function ovProgress(M) {
  const logic = ovLogicFiles(M);
  return { done: logic.filter((f) => f.reviewed).length, total: logic.length };
}

// ovSectionSummaries is each section's one-line summary and flag (red,
// yellow or null), shown in its head whether it's open or not. scope is
// "idle", "running" or "done" (with drift counted from the model); stale
// marks a scope check the change moved on from; hidden is the number of
// kinds hidden, area the area filter and focus the focused function's id.
function ovSectionSummaries(M, { scope = "idle", stale = false, hidden = 0, area = null, focus = null } = {}) {
  const units = ovByType(M, "pkg").filter((p) => p.status !== "context").length;
  const fns = ovChangedFns(M);
  const unresolved = fns.reduce((s, f) => s + f.unresolved, 0);
  const red = M.order.imp.some((id) => M.E.get(id).kind === "broken") || M.order.find.some((id) => M.E.get(id).sev === "red");
  const mapParts = [M.loaded.arch ? pl(units, "package") : "reading packages…", M.loaded.calls ? pl(fns.length, "changed function") : "reading calls…"];
  if (unresolved) mapParts.push(`${unresolved} unresolved`);
  if (focus && M.E.get(focus)) mapParts.push(`focus on ${M.E.get(focus).name}`);
  const logic = M.overview?.kinds?.logic?.lines || 0;
  const footParts = [pl(ovAreas(M).length, "area"), `${logic} ${logic === 1 ? "line" : "lines"} of logic`];
  if (hidden) footParts.push(`${pl(hidden, "kind")} hidden`);
  if (area) footParts.push(area);
  const prompts = ovByType(M, "prompt").filter((p) => p.n > 0).length;
  const drift = ovByType(M, "file").filter((f) => f.drift?.verdict === "drift").length;
  const scopeText = scope === "running" ? "Checking…" : scope !== "done" ? "Not checked yet"
    : (drift ? `${pl(drift, "file")} outside the ask` : "Fits the ask") + (stale ? " · out of date" : "");
  return {
    map: { summary: mapParts.join(" · "), flag: red ? "red" : null },
    foot: { summary: footParts.join(" · "), flag: null },
    trace: { summary: `${pl(prompts, "prompt")} · ${pl(M.orphans.length, "file")} outside the conversation`, flag: null },
    scope: { summary: scopeText, flag: scope === "done" && drift ? "yellow" : null },
  };
}

// ovCallWords describes a call in words: whether the change adds or drops
// it, and how it calls (["new", "via interface"]).
function ovCallWords(c) {
  const out = [c.op === "+" ? "new" : c.op === "-" ? "removed" : "existing"];
  if (c.kind === "ref") out.push("passed as a value");
  else if (c.kind === "dynamic") out.push("via interface");
  else if (c.kind === "approx") out.push("inferred from names");
  return out;
}

// ovFindingTitle is a finding as a sentence ("Summarize removed but still
// called"); a renamed function's old name says so.
function ovFindingTitle(M, f) {
  const fn = f.fnId ? M.E.get(f.fnId) : null;
  if (f.renamed && fn) return `${fn.name} was renamed from ${f.func.split(/[./#:]/).pop()}, but the old name is still called`;
  return `${fn?.name || f.func} ${CALL_FINDING_TEXT[f.kind] || f.kind}`;
}

// ovFixPrompt is what "Ask Claude to fix" puts in the message box for a
// finding or an import that breaks a rule (never sent by itself), or "".
function ovFixPrompt(M, id) {
  const e = M.E.get(id);
  if (!e) return "";
  const at = (t) => `${t.path}:${t.line}`;
  if (e.type === "imp" && e.kind === "broken") {
    const where = e.files.map(at).join(", ");
    return `${e.from} now imports ${e.to}${where ? ` (${where})` : ""}, which breaks a layer rule: ${e.violation}. Please change it so the rule holds.`;
  }
  if (e.type === "fn") {
    const f = e.findings.map((x) => M.E.get(x)).find((x) => x.sev !== "fold");
    return f ? ovFixPrompt(M, f.id) : "";
  }
  if (e.type !== "find") return "";
  const fn = e.fnId ? M.E.get(e.fnId) : null;
  const def = fn?.path ? ` (${fn.path}:${fn.line})` : "";
  const sites = e.sites.length ? ` It's used at ${e.sites.slice(0, 8).map(at).join(", ")}${e.sites.length > 8 ? " and more" : ""}.` : "";
  const sig = e.kind === "signature-callers" && fn?.sig ? ` The signature is now \`${fn.sig}\`${fn.oldSig ? `, was \`${fn.oldSig}\`` : ""}.` : "";
  return `${ovFindingTitle(M, e)}${def}.${sig}${sites} Please fix it.`;
}

// ovFirstChange is the index of the first added or removed row of an
// excerpt's rows, or -1.
function ovFirstChange(rows) {
  return (rows || []).findIndex((r) => r.sign === "+" || r.sign === "-");
}

// ── The map ────────────────────────────────────────────────────

const MAP_BOX_MIN = 240, MAP_BOX_MAX = 300, MAP_GAP_X = 24, MAP_GAP_Y = 44;
const MAP_HEAD = 34, MAP_ROW = 22, MAP_CAP = 12, MAP_FOLD_AT = 60;

// ovMapUnits are the packages the map draws: the changed ones, those
// holding a function of the call graph (changed, or a caller or callee of
// one), and the ends of the change's imports (and of the existing ones
// when existing is set).
function ovMapUnits(M, { existing = false } = {}) {
  const units = new Set();
  for (const p of ovByType(M, "pkg")) if (p.status !== "context") units.add(p.unit);
  for (const f of ovByType(M, "fn")) units.add(f.unit);
  for (const e of ovByType(M, "imp")) {
    if (e.changed || existing) { units.add(e.from); units.add(e.to); }
  }
  return [...units];
}

// ovMapFns are a package's functions as its box lists them: the changed
// ones, callers before what they call, then the others by name.
function ovMapFns(M, unit) {
  const p = M.E.get("pkg:" + unit);
  if (!p) return [];
  const fns = p.fns.map((id) => M.E.get(id));
  const changed = fns.filter((f) => f.status);
  const ids = new Set(changed.map((f) => f.id));
  const edges = [];
  for (const f of changed) {
    for (const cid of f.callees) {
      const c = M.E.get(cid);
      if (ids.has(c.toId)) edges.push({ from: f.id, to: c.toId });
    }
  }
  const { rows } = layoutArchGraph(changed.map((f) => ({ path: f.id })), edges);
  const flat = rows.flat().map((n) => M.E.get(n.path));
  const ordered = [...flat.filter((f) => !f.test), ...flat.filter((f) => f.test)];
  const rest = fns.filter((f) => !f.status).sort((a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return [...ordered, ...rest].map((f) => f.id);
}

// ovLayoutMap places the map's package boxes. Layers are rows (a unit in
// no layer is a group of its own), importers above what they import; the
// boxes of a row wrap to as many lines as the width needs. fold holds the
// user's choices per unit (false folded, true open, "all" every function);
// set is what the selection relates to. Returns {W, H, boxes: Map(unit →
// {x, y, w, h, cx, collapsed, shown, rest, fns}), rowY(fnId)}.
function ovLayoutMap(M, { width = 900, set = null, fold = {}, foldUnrelated = true, existing = false } = {}) {
  const units = ovMapUnits(M, { existing });
  const inMap = new Set(units);
  const groupOf = (u) => {
    const l = M.E.get("pkg:" + u)?.layer;
    return l ? "layer:" + l : "unit:" + u;
  };
  const groups = new Map();
  for (const u of units.slice().sort()) {
    const g = groupOf(u);
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(u);
  }
  const gEdges = [];
  const link = (a, b) => { if (inMap.has(a) && inMap.has(b) && groupOf(a) !== groupOf(b)) gEdges.push({ from: groupOf(a), to: groupOf(b) }); };
  for (const e of ovByType(M, "imp")) if (e.op !== "-" && (e.changed || existing)) link(e.from, e.to);
  for (const c of ovByType(M, "call")) {
    if (c.op === "-") continue;
    const a = M.E.get(c.fromId), b = M.E.get(c.toId);
    if (a && b) link(a.unit, b.unit);
  }
  const { rows } = layoutArchGraph([...groups.keys()].map((g) => ({ path: g })), gEdges);
  const order = M.arch?.rules?.order || [];
  const rank = (g) => { const i = g.startsWith("layer:") ? order.indexOf(g.slice(6)) : -1; return i < 0 ? order.length : i; };

  const perLine = Math.max(1, Math.floor((width + MAP_GAP_X) / (MAP_BOX_MIN + MAP_GAP_X)));
  const totalChanged = ovChangedFns(M).length;
  const boxes = new Map();
  let y = 6;
  for (const row of rows) {
    const rowUnits = row.slice().sort((a, b) => rank(a.path) - rank(b.path)).flatMap((n) => groups.get(n.path));
    for (let k = 0; k < rowUnits.length; k += perLine) {
      const line = rowUnits.slice(k, k + perLine);
      const w = Math.min(MAP_BOX_MAX, Math.floor((width - MAP_GAP_X * (line.length - 1)) / line.length));
      const lineW = line.length * w + (line.length - 1) * MAP_GAP_X;
      let x = Math.max(0, Math.round((width - lineW) / 2));
      let tallest = 0;
      for (const u of line) {
        const fns = ovMapFns(M, u);
        const o = fold[u];
        const related = !set || set.has("pkg:" + u);
        const auto = totalChanged > MAP_FOLD_AT ? (set ? !related : true) : (foldUnrelated && set ? !related : false);
        const collapsed = o === false ? true : o === true || o === "all" ? false : auto;
        const shown = collapsed ? [] : o === "all" ? fns : fns.filter((f, i) => i < MAP_CAP || (set && set.has(f)));
        const rest = fns.length - shown.length;
        const h = MAP_HEAD + (collapsed ? (fns.length ? MAP_ROW : 0) : shown.length * MAP_ROW + (rest ? MAP_ROW : 0)) + 6;
        boxes.set(u, { x, y, w, h, cx: x + w / 2, collapsed, shown, rest, fns });
        tallest = Math.max(tallest, h);
        x += w + MAP_GAP_X;
      }
      y += tallest + MAP_GAP_Y;
    }
  }
  const rowY = (fnId) => {
    const f = M.E.get(fnId);
    const b = f && boxes.get(f.unit);
    if (!b) return null;
    const i = b.shown.indexOf(fnId);
    return i < 0 ? b.y + 15 : b.y + MAP_HEAD + i * MAP_ROW + MAP_ROW / 2;
  };
  return { W: width, H: Math.max(0, y - MAP_GAP_Y + 10), boxes, rowY };
}

// ovLens is the one-hop view around a function for the map's Focus: its
// callers on the left; on the right its callees, or for an interface
// method with implementations, those.
function ovLens(M, fnId) {
  const f = M.E.get(fnId);
  if (!f || f.type !== "fn") return null;
  const callers = f.callers.map((cid) => ({ id: M.E.get(cid).fromId, call: cid })).filter((x) => M.E.has(x.id));
  const impls = f.impls.filter((id) => M.E.has(id));
  const right = impls.length
    ? impls.map((id) => ({ id, call: null }))
    : f.callees.map((cid) => ({ id: M.E.get(cid).toId, call: cid })).filter((x) => M.E.has(x.id));
  return {
    callers, right, implementations: impls.length > 0,
    rightTitle: impls.length ? `Implementations ${impls.length}` : `Calls ${right.length}`,
    callersTitle: `Called by ${callers.length + f.moreCallers}`,
    moreCallers: f.moreCallers, unresolved: f.unresolved,
  };
}

// ovGuessOrigin guesses which Bash command changed a file no edit touched,
// from the commands' text (latest first): one that names the file, an rm
// of a deleted one, or one that regenerates its kind of file. It returns
// {n, command (the line that matched, cut short), how} or null. It's a guess, so
// it's shown as "probably".
function ovGuessOrigin(file, commands) {
  if (!commands?.length) return null;
  const name = file.path.split("/").pop();
  const esc = (x) => x.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const named = new RegExp(`(^|[\\s'"=/(])${esc(name)}($|[\\s'"),;:])`, "m");
  const gens = [];
  if (file.kind === "generated" && /mock/i.test(file.path)) gens.push(/\bmockgen\b|\bmake mocks\b|\bgo generate\b/);
  if (name === "go.mod" || name === "go.sum") gens.push(/\bgo (get|mod)\b/);
  if (/^(package-lock\.json|yarn\.lock|pnpm-lock\.yaml)$/.test(name)) gens.push(/\b(npm|yarn|pnpm)\b/);
  if (name === "Gemfile.lock") gens.push(/\bbundle\b/);
  if (file.kind === "format") gens.push(/\b(gofmt|goimports|prettier|black)\b|\brubocop -a\b/);
  // The line of the command that matched (a heredoc's first line says
  // little), or its first line.
  const out = (c, how, test) => {
    const lines = c.command.split("\n");
    return { n: c.turn, how, command: oneLine(lines.find(test) || lines[0], 120) };
  };
  for (let i = commands.length - 1; i >= 0; i--) {
    const c = commands[i];
    if (c.command.includes(file.path) || named.test(c.command)) {
      return out(c, file.status === "D" && /\b(git rm|rm)\b/.test(c.command) ? "git rm (Bash)" : "Bash", (l) => l.includes(file.path) || named.test(l));
    }
  }
  for (let i = commands.length - 1; i >= 0; i--) {
    const c = commands[i];
    if (gens.some((re) => re.test(c.command))) return out(c, "regenerated (Bash)", (l) => gens.some((re) => re.test(l)));
  }
  return null;
}

// ── Selection history ──────────────────────────────────────────
// {stack, at}: the selections made, and where Back/Forward stand. null is
// "nothing selected", a step of its own so Back can return to it.

const SEL_HISTORY_MAX = 30;

function selInitial() {
  return { stack: [null], at: 0 };
}

// selPush selects id: the forward part of the history is dropped, and
// selecting what's already selected changes nothing (the same object).
function selPush(h, id) {
  const cur = h.stack[h.at] ?? null;
  if (cur === (id ?? null)) return h;
  const stack = h.stack.slice(0, h.at + 1).concat([id ?? null]).slice(-SEL_HISTORY_MAX);
  return { stack, at: stack.length - 1 };
}

function selBack(h) {
  return h.at > 0 ? { stack: h.stack, at: h.at - 1 } : h;
}

function selForward(h) {
  return h.at < h.stack.length - 1 ? { stack: h.stack, at: h.at + 1 } : h;
}

function selCurrent(h) {
  return h.stack[h.at] ?? null;
}

// selCrumbs are the last 4 selections up to the current one, oldest
// first, each with its index in the stack.
function selCrumbs(h) {
  const out = [];
  for (let i = h.at; i >= 0 && out.length < 4; i--) {
    const id = h.stack[i];
    if (id && !out.some((c) => c.id === id)) out.unshift({ id, at: i });
  }
  return out;
}

// selValid checks a stored history (sessionStorage) before it's used.
function selValid(h) {
  return !!h && Array.isArray(h.stack) && h.stack.length > 0 && h.stack.length <= SEL_HISTORY_MAX
    && h.stack.every((x) => x === null || typeof x === "string") && Number.isInteger(h.at) && h.at >= 0 && h.at < h.stack.length;
}

if (typeof module === "object" && module.exports) {
  module.exports = {
    OVERVIEW_KINDS, OVERVIEW_NOISE, SURFACE_KINDS, CALL_MARK, FINDING_SEVERITY,
    createIntentTrace, turnForRange, buildTraceRows, scopeRequestFrom, oneLine, summarizeOverview, layoutTreemap, layoutArchGraph,
    buildModel, ovRelated, ovWhere, ovLabel, ovSection, ovVerdict, ovVerdictSub, ovRulesText, ovChecks, ovCaveats, ovChips, ovReviewQueue, ovProgress,
    reviewSig, cellID, parseCell, ovFileOf, pl,
    ovSectionSummaries, ovGuessOrigin, ovMapUnits, ovMapFns, ovLayoutMap, ovLens, ovCallWords, ovFindingTitle, ovFixPrompt, ovFirstChange, selInitial, selPush, selBack, selForward, selCurrent, selCrumbs, selValid,
  };
}
