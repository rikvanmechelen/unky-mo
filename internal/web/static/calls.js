// calls.js — the Overview's Functions view: the change's call graph from
// /calls. Changed functions sit in the middle, grouped by the part of the
// code they're in; the functions that call them on the left, and what they
// call on the right. Clicking a function shows its detail; "Focus" redraws
// the graph around it. Needs common.js (el) and graph.js (svgEl).

const CALL_MARK = { added: "+", removed: "−", renamed: "↦", signature: "sig", changed: "~" };
const CALL_STATUS_TEXT = {
  added: "added", removed: "removed", renamed: "renamed", signature: "signature changed", changed: "body changed",
};
const CALLS_COLLAPSE_AT = 60; // more changed functions than this start grouped by unit
const CALL_FINDING_TEXT = {
  "removed-called": "removed but still called",
  "signature-callers": "signature changed; callers not updated",
  untested: "no test reaches it",
};

// callIndex maps the graph by function: funcs, and calls in and out.
function callIndex(cg) {
  const funcs = new Map((cg.funcs || []).map((f) => [f.id, f]));
  const out = new Map(), into = new Map();
  for (const c of cg.calls || []) {
    if (!out.has(c.from)) out.set(c.from, []);
    if (!into.has(c.to)) into.set(c.to, []);
    out.get(c.from).push(c);
    into.get(c.to).push(c);
  }
  return { funcs, out, into };
}

// callFocus picks what to draw. Without a focus: every changed function in
// the middle (grouped into unit boxes when there are many and the unit
// isn't expanded), their callers left, their callees right. With a focus:
// that function, its callers and its callees. Implementations behind an
// interface call are listed in the detail, not drawn.
// Returns {left, center: [{unit, funcs, collapsed, collapsible, added,
// removed}], right, edges}: added/removed count the unit's new and dropped
// calls, so a collapsed unit still shows them.
function callFocus(cg, ix, focusID, expanded) {
  const isCenter = (f) => (focusID ? f.id === focusID : !!f.status);
  const centerFns = [...ix.funcs.values()].filter(isCenter);
  const inCenter = new Set(centerFns.map((f) => f.id));
  const left = new Map(), right = new Map();
  const drawn = (c) => c.kind !== "impl";
  for (const id of inCenter) {
    for (const c of ix.into.get(id) || []) {
      if (drawn(c) && !inCenter.has(c.from) && ix.funcs.has(c.from)) left.set(c.from, ix.funcs.get(c.from));
    }
    for (const c of ix.out.get(id) || []) {
      if (drawn(c) && !inCenter.has(c.to) && !left.has(c.to) && ix.funcs.has(c.to)) right.set(c.to, ix.funcs.get(c.to));
    }
  }
  const byUnit = new Map();
  for (const f of centerFns) {
    const u = f.unit || "";
    if (!byUnit.has(u)) byUnit.set(u, []);
    byUnit.get(u).push(f);
  }
  const collapse = !focusID && centerFns.length > CALLS_COLLAPSE_AT;
  const center = [...byUnit.keys()].sort().map((unit) => {
    const ids = new Set(byUnit.get(unit).map((f) => f.id));
    const changed = (cg.calls || []).filter((c) => c.op && drawn(c) && (ids.has(c.from) || ids.has(c.to)));
    return {
      unit, funcs: orderByCalls(byUnit.get(unit), ix),
      collapsed: collapse && !expanded.has(unit), collapsible: collapse,
      added: changed.filter((c) => c.op === "+").length,
      removed: changed.filter((c) => c.op === "-").length,
    };
  });
  const shown = new Set([...left.keys(), ...right.keys()]);
  for (const g of center) if (!g.collapsed) for (const f of g.funcs) shown.add(f.id);
  const edges = (cg.calls || []).filter((c) => drawn(c) && shown.has(c.from) && shown.has(c.to)
    && (inCenter.has(c.from) || inCenter.has(c.to)));
  // With collapsed units, the side columns only keep what connects to an
  // expanded function.
  const linked = new Set(edges.flatMap((c) => [c.from, c.to]));
  const keep = (m) => [...m.values()].filter((f) => !collapse || linked.has(f.id));
  return { left: keep(left), center, right: keep(right), edges };
}

// orderByCalls orders a unit's functions so callers come before what they
// call (layoutArchGraph's layering, flattened), then by name.
function orderByCalls(fns, ix) {
  const ids = new Set(fns.map((f) => f.id));
  const nodes = fns.map((f) => ({ path: f.id, f }));
  const edges = [];
  for (const f of fns) for (const c of ix.out.get(f.id) || []) if (ids.has(c.to) && c.kind !== "impl") edges.push({ from: f.id, to: c.to });
  const { rows } = layoutArchGraph(nodes, edges);
  // Tests last: they call everything, so they'd otherwise sort first.
  const flat = rows.flat().map((n) => n.f);
  return [...flat.filter((f) => !f.test), ...flat.filter((f) => f.test)];
}

// layoutCallBands sorts the side columns by where their neighbours sit in
// the middle, so edges stay short.
function layoutCallBands(view) {
  const pos = new Map();
  let i = 0;
  for (const g of view.center) for (const f of g.funcs) pos.set(f.id, i++);
  const bary = (id, other) => {
    const xs = view.edges.filter((c) => (other === "to" ? c.from === id : c.to === id)).map((c) => pos.get(other === "to" ? c.to : c.from)).filter((x) => x !== undefined);
    return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : Infinity;
  };
  const sortBy = (list, other) => list.map((f) => ({ f, b: bary(f.id, other) })).sort((a, b) => a.b - b.b || (a.f.name < b.f.name ? -1 : 1)).map((x) => x.f);
  return { ...view, left: sortBy(view.left, "to"), right: sortBy(view.right, "from") };
}

// shortLabel cuts a long name in the middle.
function shortLabel(s, max) {
  return s.length <= max ? s : s.slice(0, Math.ceil(max / 2) - 1) + "…" + s.slice(-(Math.floor(max / 2)));
}

// callsSummary is the one-line count under the section head.
function callsSummary(cg) {
  const n = {};
  for (const f of cg.funcs || []) if (f.status) n[f.status] = (n[f.status] || 0) + 1;
  const parts = ["added", "changed", "signature", "renamed", "removed"].filter((s) => n[s]).map((s) => `${n[s]} ${CALL_STATUS_TEXT[s]}`);
  const plus = (cg.calls || []).filter((c) => c.op === "+").length, minus = (cg.calls || []).filter((c) => c.op === "-").length;
  if (plus || minus) parts.push(`calls +${plus} −${minus}`);
  return parts.join(" · ");
}

// createCallsView renders a call graph. onOpen(path, line, before) opens a
// file at a line (before: the line is in the base version); onMention, if
// given, puts text into the composer; changedIn(f), if given, names the
// conversation turn ({n, uuid, text}) that changed f, which onRevealTurn
// scrolls to.
function createCallsView({ onOpen, onMention, changedIn, onRevealTurn } = {}) {
  const root = el("div", { class: "calls" });
  let cg = null, ix = null;
  let focus = []; // focus stack: function IDs, innermost last
  let selected = null;
  const expanded = new Set();
  let untestedOpen = false;

  function reset() {
    cg = null; ix = null; focus = []; selected = null; expanded.clear();
    root.replaceChildren();
  }

  function update(next) {
    cg = next;
    ix = callIndex(cg);
    focus = focus.filter((id) => ix.funcs.has(id));
    if (selected && !ix.funcs.has(selected)) selected = null;
    render();
  }

  function setFocus(id) {
    if (!id) focus = [];
    else if (focus[focus.length - 1] !== id) focus.push(id);
    selected = id || selected;
    render();
  }

  function select(id) {
    selected = selected === id ? null : id;
    render();
  }

  function open(f, line) {
    onOpen?.(f.path, line ?? f.line, !!f.before);
  }

  function render() {
    if (!cg) { root.replaceChildren(); return; }
    const parts = [];
    if (focus.length) parts.push(crumbs());
    const view = layoutCallBands(callFocus(cg, ix, focus[focus.length - 1], expanded));
    if (!view.center.length) {
      parts.push(el("div", { class: "overview__note", text: "No functions added, removed or changed." }));
    } else {
      parts.push(el("div", { class: "overview-arch-scroll calls__scroll" }, [svg(view)]), legend());
    }
    if (selected && ix.funcs.has(selected)) parts.push(detail(ix.funcs.get(selected)));
    const fl = findingsList();
    if (fl) parts.push(fl);
    root.replaceChildren(...parts);
  }

  function crumbs() {
    const items = [link("All changes", () => { focus = []; render(); })];
    focus.forEach((id, i) => {
      items.push(el("span", { class: "calls__sep", text: "›" }));
      const name = ix.funcs.get(id)?.name || id;
      items.push(i === focus.length - 1 ? el("b", { text: name }) : link(name, () => { focus = focus.slice(0, i + 1); render(); }));
    });
    return el("div", { class: "calls__crumbs" }, items);
  }

  function link(text, onClick, title) {
    const b = el("button", { class: "link-btn", type: "button", text, ...(title ? { title } : {}) });
    b.addEventListener("click", onClick);
    return b;
  }

  function svg(view) {
    const CHAR = 6.6, PAD = 8, H = 22, VGAP = 6, COLGAP = 96, M = 10, UNIT_H = 20, MAXCH = 38;
    const label = (f) => (CALL_MARK[f.status] ? CALL_MARK[f.status] + " " : "") + shortLabel(f.name || f.id, MAXCH);
    const width = (list, extra = 0) => Math.max(80, ...list.map((f) => label(f).length * CHAR + 2 * PAD + extra));
    const centerFns = view.center.flatMap((g) => (g.collapsed ? [] : g.funcs));
    const unitLabel = (g) => (g.collapsible ? (g.collapsed ? "▸ " : "▾ ") : "") + (g.unit || "(root)")
      + (g.collapsed ? ` · ${g.funcs.length} function${g.funcs.length === 1 ? "" : "s"}` : "");
    const callCounts = (g) => [g.added ? ` +${g.added}` : "", g.removed ? ` −${g.removed}` : ""];
    const wl = view.left.length ? width(view.left) : 0;
    const wc = Math.max(width(centerFns, 16), ...view.center.map((g) => (unitLabel(g) + callCounts(g).join("")).length * CHAR + 2 * PAD));
    const wr = view.right.length ? width(view.right) : 0;
    const xl = M, xc = M + (wl ? wl + COLGAP : 0), xr = xc + wc + COLGAP;
    const box = new Map();
    const place = (list, x, w) => list.forEach((f, i) => box.set(f.id, { x, y: M + UNIT_H + i * (H + VGAP), w, f }));
    place(view.left, xl, wl);
    place(view.right, xr, wr);
    let y = M;
    const groups = [];
    for (const g of view.center) {
      const top = y;
      y += UNIT_H;
      if (!g.collapsed) {
        for (const f of g.funcs) { box.set(f.id, { x: xc + 8, y, w: wc - 16, f }); y += H + VGAP; }
      }
      groups.push({ g, top, bottom: y + (g.collapsed ? 4 : 0) });
      y += 10;
    }
    const colH = (n) => M + UNIT_H + n * (H + VGAP);
    const W = (wr ? xr + wr : xc + wc + 40) + M;
    const Ht = Math.max(y, colH(view.left.length), colH(view.right.length)) + M;
    const s = svgEl("svg", { class: "overview-arch calls__svg", viewBox: `0 0 ${W} ${Ht}`, width: W, height: Ht, role: "img", "aria-label": "Call graph of the change: " + callsSummary(cg) });
    const defs = svgEl("defs", {});
    for (const [id, open] of [["ov-call-arrow", false], ["ov-call-arrow-open", true]]) {
      const m = svgEl("marker", { id, viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 6, markerHeight: 6, orient: "auto-start-reverse" });
      m.appendChild(svgEl("path", open ? { d: "M0,0 L10,5 L0,10", fill: "none", stroke: "context-stroke", "stroke-width": 1.5 } : { d: "M0,0 L10,5 L0,10 z", fill: "context-stroke" }));
      defs.appendChild(m);
    }
    s.appendChild(defs);
    // Column titles.
    const title = (x, text) => { const t = svgEl("text", { x, y: M + 11, class: "calls__col" }); t.textContent = text; s.appendChild(t); };
    if (wl) title(xl, "called by");
    if (wr) title(xr, "calls");
    // Unit boxes behind the middle column.
    for (const { g, top, bottom } of groups) {
      const r = svgEl("g", { class: "calls__unit" + (g.collapsed ? " is-collapsed" : "") });
      r.appendChild(svgEl("rect", { x: xc, y: top, width: wc, height: bottom - top, rx: 3 }));
      // The header: a click toggles a unit that can be collapsed.
      const head = svgEl("g", { class: "calls__unit-head" + (g.collapsible ? " is-toggle" : "") });
      head.appendChild(svgEl("rect", { x: xc, y: top, width: wc, height: UNIT_H, class: "calls__unit-hit" }));
      const t = svgEl("text", { x: xc + 8, y: top + 14 });
      t.textContent = unitLabel(g);
      const [plus, minus] = callCounts(g);
      if (plus) { const sp = svgEl("tspan", { class: "calls__plus" }); sp.textContent = plus; t.appendChild(sp); }
      if (minus) { const sp = svgEl("tspan", { class: "calls__minus" }); sp.textContent = minus; t.appendChild(sp); }
      head.appendChild(t);
      const tt = svgEl("title", {});
      tt.textContent = (g.collapsible ? (g.collapsed ? "Show this part's functions" : "Hide this part's functions") : g.unit)
        + (g.added || g.removed ? ` · ${g.added} new call${g.added === 1 ? "" : "s"}, ${g.removed} removed` : "");
      head.appendChild(tt);
      if (g.collapsible) {
        head.setAttribute("tabindex", 0);
        head.setAttribute("role", "button");
        head.setAttribute("aria-expanded", String(!g.collapsed));
        const toggle = () => {
          if (g.collapsed) expanded.add(g.unit); else expanded.delete(g.unit);
          render();
        };
        head.addEventListener("click", toggle);
        head.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); toggle(); } });
      }
      r.appendChild(head);
      s.appendChild(r);
    }
    // Edges, changed ones on top.
    const ordered = [...view.edges].sort((a, b) => (a.op ? 1 : 0) - (b.op ? 1 : 0));
    for (const c of ordered) {
      const a = box.get(c.from), b = box.get(c.to);
      if (!a || !b) continue;
      let d;
      if (a.x === b.x) {
        // Within the middle column: an arc to the right.
        const x = a.x + a.w, y1 = a.y + H / 2, y2 = b.y + H / 2, bend = 30 + Math.min(60, Math.abs(y2 - y1) / 4);
        d = `M${x},${y1} C${x + bend},${y1} ${x + bend},${y2} ${x},${y2}`;
      } else if (a.x < b.x) {
        const x1 = a.x + a.w, x2 = b.x, y1 = a.y + H / 2, y2 = b.y + H / 2, k = (x2 - x1) / 2;
        d = `M${x1},${y1} C${x1 + k},${y1} ${x2 - k},${y2} ${x2},${y2}`;
      } else {
        const x1 = a.x, x2 = b.x + b.w, y1 = a.y + H / 2, y2 = b.y + H / 2, k = (x1 - x2) / 2;
        d = `M${x1},${y1} C${x1 - k},${y1} ${x2 + k},${y2} ${x2},${y2}`;
      }
      const cls = (c.op === "+" ? "is-new" : c.op === "-" ? "is-removed" : "is-existing") + ` is-${c.kind}`;
      const p = svgEl("path", { class: `calls__edge ${cls}`, d, "marker-end": `url(#${c.kind === "dynamic" ? "ov-call-arrow-open" : "ov-call-arrow"})` });
      const t = svgEl("title", {});
      t.textContent = `${a.f.name} → ${b.f.name}` + (c.op === "+" ? " (new)" : c.op === "-" ? " (removed)" : "")
        + (c.kind === "ref" ? " · used as a value" : c.kind === "dynamic" ? " · through an interface" : c.kind === "approx" ? " · inferred from names" : "")
        + (c.label ? ` · ${c.label}` : "")
        + ((c.sites || []).length ? "\n" + c.sites.slice(0, 5).map((x) => `${x.path}:${x.line}`).join("\n") : "");
      p.appendChild(t);
      s.appendChild(p);
    }
    // Nodes.
    for (const { x, y: ny, w, f } of box.values()) {
      const impls = (ix.out.get(f.id) || []).filter((c) => c.kind === "impl").length;
      const g = svgEl("g", {
        class: `calls__node is-${f.status || "context"}` + (f.test ? " is-test" : "") + (f.id === selected ? " is-selected" : ""),
        tabindex: 0, role: "button",
      });
      g.appendChild(svgEl("rect", { x, y: ny, width: w, height: H, rx: 2 }));
      const t = svgEl("text", { x: x + PAD, y: ny + H / 2 + 4 });
      t.textContent = label(f) + (impls ? `  ⋯ ${impls} impl${impls === 1 ? "" : "s"}` : "");
      const tt = svgEl("title", {});
      tt.textContent = `${f.id}\n${f.path}:${f.line}` + (f.status ? ` · ${CALL_STATUS_TEXT[f.status]}` : "")
        + (f.unresolved ? ` · ${f.unresolved} call${f.unresolved === 1 ? "" : "s"} not resolved` : "");
      g.append(tt, t);
      g.addEventListener("click", () => select(f.id));
      g.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); select(f.id); } });
      s.appendChild(g);
    }
    return s;
  }

  function legend() {
    const sw = (cls, text) => el("span", {}, [el("i", { class: `overview-sw ${cls}` }), document.createTextNode(text)]);
    return el("div", { class: "overview-legend is-static" }, [
      sw("is-edge-new", "new call"), sw("is-edge-removed", "removed"), sw("calls-sw-ref", "used as a value"),
      sw("calls-sw-dynamic", "through an interface"),
      el("span", { class: "overview__muted", text: "+ added · ~ body · sig signature · ↦ renamed · − removed" }),
    ]);
  }

  // siteButtons lists call sites as links that open the file at the line.
  function siteButtons(sites, before) {
    return (sites || []).map((s) => link(`${s.path}:${s.line}`, () => onOpen?.(s.path, s.line, before), "Open at this line"));
  }

  function fnRow(f, sites, before) {
    const name = link(f.name || f.id, () => select(f.id), f.id);
    name.classList.add("calls__fn", `is-${f.status || "context"}`);
    return el("div", { class: "calls__row" }, [name, el("span", { class: "calls__sites" }, siteButtons(sites, before))]);
  }

  function detail(f) {
    const callers = (ix.into.get(f.id) || []).filter((c) => c.kind !== "impl");
    const callees = (ix.out.get(f.id) || []).filter((c) => c.kind !== "impl");
    const impls = (ix.out.get(f.id) || []).filter((c) => c.kind === "impl");
    const implements_ = (ix.into.get(f.id) || []).filter((c) => c.kind === "impl");
    const actions = [link(`${f.path}:${f.line}`, () => open(f), f.before ? "Open (this line is in the base version)" : "Open at this line")];
    if (focus[focus.length - 1] !== f.id) actions.push(link("Focus", () => setFocus(f.id), "Redraw the graph around this function"));
    if (onMention) actions.push(link("Mention in prompt", () => onMention(`\`${f.name}\` (${f.path}:${f.line})`)));
    const parts = [
      el("div", { class: "calls__detail-head" }, [
        el("b", { class: "calls__detail-name", text: f.name || f.id }),
        ...(f.status ? [el("span", { class: `calls__chip is-${f.status}`, text: CALL_STATUS_TEXT[f.status] })] : []),
        ...(f.test ? [el("span", { class: "calls__chip", text: "test" })] : []),
        el("span", { class: "calls__actions" }, actions),
      ]),
      el("div", { class: "overview__muted calls__id", text: f.id + (f.from ? ` (was ${f.from})` : "") }),
    ];
    const turn = changedIn?.(f);
    if (turn) {
      const text = turn.text.replace(/\s+/g, " ").trim();
      parts.push(el("div", { class: "calls__turn" }, [
        document.createTextNode(`Changed in turn ${turn.n}: `),
        link(text.length > 90 ? text.slice(0, 89) + "…" : text || "(image)", () => onRevealTurn?.(turn.uuid), "Show this prompt in the chat"),
      ]));
    }
    const section = (title, rows) => rows.length ? el("div", { class: "calls__list" }, [el("div", { class: "calls__list-title", text: title }), ...rows]) : null;
    const opMark = (c) => (c.op === "+" ? " (new)" : c.op === "-" ? " (removed)" : "");
    const rows = (list, end) => list.map((c) => {
      const g = ix.funcs.get(c[end]) || { id: c[end], name: c[end] };
      const row = fnRow(g, c.sites, c.op === "-");
      if (c.op || c.kind !== "static") row.appendChild(el("span", { class: "overview__muted", text: opMark(c) + (c.kind === "ref" ? " as a value" : c.kind === "dynamic" ? " via interface" : c.kind === "approx" ? " (inferred)" : "") }));
      if (c.label) row.appendChild(el("code", { class: "calls__label", text: c.label }));
      return row;
    });
    const more = f.moreCallers ? [el("div", { class: "overview__muted", text: `and ${f.moreCallers} more callers` })] : [];
    for (const s of [
      section(`Called by (${callers.length + (f.moreCallers || 0)})`, [...rows(callers, "from"), ...more]),
      section(`Calls (${callees.length})`, rows(callees, "to")),
      section(`Implementations (${impls.length})`, rows(impls, "to")),
      // Calls through an interface reach this method via these.
      section("Implements (called through)", rows(implements_, "from")),
    ]) if (s) parts.push(s);
    if (f.unresolved) parts.push(el("div", { class: "overview__muted", text: `${f.unresolved} call${f.unresolved === 1 ? "" : "s"} in it couldn't be resolved (function values, or names that don't exist).` }));
    for (const x of (cg.findings || []).filter((x) => x.func === f.id)) parts.push(findingRow(x));
    return el("div", { class: "calls__detail" }, parts);
  }

  function findingRow(x) {
    const f = ix.funcs.get(x.func) || { id: x.func, name: x.func };
    return el("div", { class: `calls__finding is-${x.kind}` }, [
      el("span", {}, [link(f.name || f.id, () => select(f.id), f.id), document.createTextNode(` — ${CALL_FINDING_TEXT[x.kind] || x.kind}`)]),
      ...((x.sites || []).length ? [el("span", { class: "calls__sites" }, siteButtons(x.sites, false))] : []),
    ]);
  }

  function findingsList() {
    const list = cg.findings || [];
    if (!list.length) return null;
    const serious = list.filter((x) => x.kind !== "untested");
    const hints = list.filter((x) => x.kind === "untested");
    const parts = serious.map(findingRow);
    if (hints.length) {
      // A hint, folded: it can't see table-driven or reflective tests.
      const names = el("div", { class: "calls__untested" });
      hints.forEach((x, i) => {
        if (i) names.append(", ");
        names.append(link(ix.funcs.get(x.func)?.name || x.func, () => select(x.func), x.func));
      });
      const box = el("details", { class: "calls__finding is-untested" }, [
        el("summary", { text: `${hints.length} changed function${hints.length === 1 ? "" : "s"} no test reaches within 2 calls` }),
        names,
      ]);
      if (untestedOpen) box.open = true;
      box.addEventListener("toggle", () => { untestedOpen = box.open; });
      parts.push(box);
    }
    return el("div", { class: "calls__findings" }, parts);
  }

  return { el: root, update, reset, setFocus };
}
