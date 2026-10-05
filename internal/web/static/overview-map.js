// overview-map.js — the Overview's Map: the change's packages as boxes in
// layer rows (ovLayoutMap), each listing its changed functions and the
// ones around them, with the imports between boxes and the calls between
// function rows drawn over them. Everything selects (overview.selection);
// what the hover or selection relates to stays lit and the rest dims.
// Boxes are HTML placed over one SVG of edges. Needs common.js (el),
// graph.js (svgEl) and overview-model.js.

const MAP_PREFS_KEY = "mo.overview.map";
const MAP_LEGEND = [
  ["is-new", "New"], ["is-broken", "Breaks a rule"], ["is-removed", "Removed"], ["is-approx", "Inferred from names"],
  ["is-ref", "Passed as a value"], ["is-dynamic", "Via interface"], ["is-ctx", "Existing"],
];
const IMP_TITLE = { broken: "breaks a layer rule", new: "new import", removed: "removed import", fixed: "no longer breaks a rule", ctx: "existing import" };

function createMap(overview) {
  const sel = overview.selection;
  const stored = storageGet(MAP_PREFS_KEY);
  const prefs = { existing: false, allCalls: false, foldUnrelated: true, isolate: false, ...(stored && typeof stored === "object" ? stored : {}) };
  const fold = {}; // unit → false | true | "all", per target
  let foldFor = null;

  const canvas = el("div", { class: "map__canvas" });
  const svg = svgEl("svg", { class: "map__edges", "aria-hidden": "true" });
  const boxLayer = el("div", { class: "map__boxes" });
  canvas.append(svg, boxLayer);
  const scroll = el("div", { class: "map__scroll" }, [canvas]);
  canvas.addEventListener("click", (e) => { if (e.target === canvas || e.target === boxLayer) sel.select(null); });

  let width = 0;
  new ResizeObserver(() => {
    const w = scroll.clientWidth;
    if (w && Math.abs(w - width) > 4) { width = w; draw(); }
  }).observe(scroll);

  function savePrefs() { storageSet(MAP_PREFS_KEY, prefs); }

  function toolbar(viewSwitch) {
    const toggle = (key, label, title) => {
      const b = el("button", { class: "map__toggle" + (prefs[key] ? " is-on" : ""), type: "button", "aria-pressed": String(prefs[key]), title }, [el("span", { class: "map__box", "aria-hidden": "true" }), document.createTextNode(label)]);
      b.addEventListener("click", () => { prefs[key] = !prefs[key]; savePrefs(); overview.rerender(); });
      return b;
    };
    return el("div", { class: "map__bar" }, [
      viewSwitch,
      el("span", { class: "map__spacer" }),
      toggle("existing", "Existing imports", "Also draw the imports the change keeps"),
      toggle("allCalls", "All calls", "Draw every call between the functions shown, not only the change's"),
      toggle("foldUnrelated", "Fold unrelated", "Fold the packages the selection doesn't touch"),
      toggle("isolate", "Isolate selection", "Fade everything the selection doesn't touch almost away"),
    ]);
  }

  function legend(M) {
    const unresolved = ovByType(M, "fn").filter((f) => f.status).reduce((s, f) => s + f.unresolved, 0);
    return el("div", { class: "map__legend" }, [
      ...MAP_LEGEND.map(([cls, text]) => el("span", { class: "map__key" }, [svgSwatch(cls), document.createTextNode(text)])),
      ...(unresolved ? [el("span", { class: "map__unres", text: `${pl(unresolved, "call")} couldn’t be resolved` })] : []),
    ]);
  }

  function svgSwatch(cls) {
    const s = svgEl("svg", { class: "map__sw", width: 22, height: 8, viewBox: "0 0 22 8" });
    s.appendChild(svgEl("path", { class: `map__edge ${cls}`, d: "M1,4 L21,4" }));
    return s;
  }

  // el is the section body: the toolbar, the legend and the map.
  function element(viewSwitch) {
    const M = overview.model();
    if (M && M.overview.root + "|" + (M.overview.head || "") !== foldFor) {
      for (const k of Object.keys(fold)) delete fold[k];
      foldFor = M.overview.root + "|" + (M.overview.head || "");
    }
    const parts = [toolbar(viewSwitch)];
    if (M) parts.push(legend(M));
    parts.push(scroll);
    queueMicrotask(() => { width = scroll.clientWidth || width; draw(); });
    return el("div", { class: "map" }, parts);
  }

  function relatedSet(M) {
    const id = sel.hovered() || sel.current();
    return id ? ovRelated(M, id) : null;
  }

  function draw() {
    const M = overview.model();
    if (!M || !scroll.isConnected) return;
    const w = Math.max(480, width || scroll.clientWidth || 900);
    const set = relatedSet(M);
    const current = sel.current();
    const L = ovLayoutMap(M, { width: w - 20, set, fold, foldUnrelated: prefs.foldUnrelated, existing: prefs.existing });
    canvas.style.width = L.W + 20 + "px";
    canvas.style.height = L.H + 20 + "px";
    canvas.classList.toggle("is-isolate", prefs.isolate);
    canvas.classList.toggle("has-set", !!set);
    const findFlag = new Map();
    for (const id of M.order.find) {
      const f = M.E.get(id);
      if (f.fnId && f.sev !== "fold" && !findFlag.has(f.fnId)) findFlag.set(f.fnId, f.sev);
    }
    if (!M.loaded.calls && !L.boxes.size) {
      boxLayer.replaceChildren(el("div", { class: "map__empty" }, [bone("60%", 14), bone("40%", 14)]));
      svg.replaceChildren();
      return;
    }
    if (!L.boxes.size) {
      boxLayer.replaceChildren(el("div", { class: "overview__note map__empty", text: M.arch?.languages?.length === 0 ? "No supported language in this change (Go, Ruby on Rails, JavaScript/TypeScript, Python, Kotlin/Java, Swift)." : "No packages or functions to draw." }));
      svg.replaceChildren();
      return;
    }
    boxLayer.replaceChildren(...[...L.boxes].map(([unit, b]) => boxEl(M, unit, b, set, current, findFlag)));
    drawEdges(M, L, set, current);
  }

  function boxEl(M, unit, b, set, current, findFlag) {
    const p = M.E.get("pkg:" + unit);
    const on = current === p.id;
    const cls = ["map-box", `is-${p.status}`];
    if (on) cls.push("is-selected");
    if (set && !set.has(p.id)) cls.push("is-dim");
    const chev = el("button", { class: "map-box__fold" + (b.collapsed ? "" : " is-open"), type: "button", title: b.collapsed ? "Open this package" : "Fold this package", "aria-expanded": String(!b.collapsed) });
    chev.addEventListener("click", (e) => { e.stopPropagation(); fold[unit] = b.collapsed ? true : false; draw(); });
    const name = el("button", { class: "map-box__name", type: "button", title: unit || "(root)" }, [
      el("span", { class: "map-box__label", text: p.label }),
      ...(p.status === "added" ? [el("span", { class: "map-box__tag is-new", text: "new" })] : p.status === "context" ? [el("span", { class: "map-box__tag", text: "context" })] : []),
    ]);
    hook(name, p.id);
    const rows = b.shown.map((fid) => {
      const f = M.E.get(fid);
      const r = el("button", { class: "map-fn" + (f.status ? ` is-${f.status}` : " is-ctx") + (current === fid ? " is-selected" : set && set.has(fid) ? " is-related" : set ? " is-dim" : ""), type: "button", title: `${f.name} — ${f.status ? CALL_STATUS_TEXT[f.status] : "unchanged"}` }, [
        el("span", { class: "map-fn__mark", text: f.mark }),
        el("span", { class: "map-fn__name", text: f.name }),
        ...(findFlag.has(fid) ? [el("span", { class: `map-fn__flag is-${findFlag.get(fid)}` })] : []),
      ]);
      hook(r, fid);
      return r;
    });
    const changed = b.fns.filter((id) => M.E.get(id).status).length;
    if (b.collapsed && b.fns.length) rows.push(restRow(`${pl(b.fns.length, "function")} · ${changed} changed — open`, () => { fold[unit] = true; draw(); }));
    else if (b.rest) rows.push(restRow(`+${b.rest} more — show all`, () => { fold[unit] = "all"; draw(); }));
    if (!M.loaded.calls && !b.fns.length && p.status !== "context") rows.push(el("div", { class: "map-fn is-bone" }, [bone("70%", 10)]), el("div", { class: "map-fn is-bone" }, [bone("50%", 10)]));
    const box = el("div", { class: cls.join(" ") }, [
      el("div", { class: "map-box__head" }, [chev, name]),
      ...(p.layer ? [el("div", { class: "map-box__layer", text: p.layer })] : []),
      ...rows,
    ]);
    Object.assign(box.style, { left: b.x + 10 + "px", top: b.y + 10 + "px", width: b.w + "px", minHeight: b.h + "px" });
    return box;
  }

  function restRow(text, onClick) {
    const r = el("button", { class: "map-fn is-rest", type: "button", text });
    r.addEventListener("click", (e) => { e.stopPropagation(); onClick(); });
    return r;
  }

  function hook(node, id) {
    node.addEventListener("click", (e) => { e.stopPropagation(); sel.select(id); });
    node.addEventListener("mouseenter", () => sel.hover(id));
    node.addEventListener("mouseleave", () => sel.hover(null));
  }

  function drawEdges(M, L, set, current) {
    const OFF = 10; // the canvas's margin
    const parts = [defs()];
    const edge = (id, d, cls, title) => {
      const g = svgEl("g", { class: "map__e" + (set && !set.has(id) ? " is-dim" : "") + (current === id ? " is-selected" : "") });
      const t = svgEl("title", {});
      t.textContent = title;
      g.append(t, svgEl("path", { class: "map__hit", d }));
      if (current === id) g.appendChild(svgEl("path", { class: "map__halo", d }));
      const marker = cls.includes("is-dynamic") ? "map-arrow-open" : cls.includes("is-broken") || cls.includes("is-removed") ? "map-arrow-red" : cls.includes("is-new") || cls.includes("is-fixed") ? "map-arrow-green" : "map-arrow";
      g.appendChild(svgEl("path", { class: `map__edge ${cls}`, d, "marker-end": `url(#${marker})` }));
      g.addEventListener("click", (e) => { e.stopPropagation(); sel.select(id); });
      g.addEventListener("mouseenter", () => sel.hover(id));
      g.addEventListener("mouseleave", () => sel.hover(null));
      parts.push(g);
    };
    // Imports, between boxes.
    for (const e of ovByType(M, "imp")) {
      if (!e.changed && !prefs.existing) continue;
      const a = L.boxes.get(e.from), b = L.boxes.get(e.to);
      if (!a || !b || a === b) continue;
      edge(e.id, impPath(a, b, OFF), `is-imp is-${e.kind === "fixed" ? "fixed" : e.kind}` + (e.approx ? " is-approx" : ""), `${e.from} → ${e.to} — ${IMP_TITLE[e.kind]}` + (e.violation ? `: ${e.violation}` : ""));
    }
    // Calls, between function rows.
    for (const c of ovByType(M, "call")) {
      const red = c.findings.some((x) => M.E.get(x).sev === "red");
      // Unless asked for all of them, only the calls the selection relates
      // to, or, with nothing selected, those that break something.
      const show = set ? set.has(c.id) || prefs.allCalls : prefs.allCalls || red;
      if (!show) continue;
      const fa = M.E.get(c.fromId), fb = M.E.get(c.toId);
      if (!fa || !fb) continue;
      const a = L.boxes.get(fa.unit), b = L.boxes.get(fb.unit);
      const ya = L.rowY(c.fromId), yb = L.rowY(c.toId);
      if (!a || !b || ya === null || yb === null) continue;
      const cls = `is-call is-${c.op === "+" ? "new" : c.op === "-" ? "removed" : "ctx"} is-${c.kind}` + (red ? " is-broken" : "");
      edge(c.id, callPath(a, b, ya, yb, OFF), cls, `${fa.name} → ${fb.name} — ${ovCallWords(c).join(", ")}` + (c.label ? ` · ${c.label}` : ""));
    }
    svg.setAttribute("width", L.W + 20);
    svg.setAttribute("height", L.H + 20);
    svg.replaceChildren(...parts);
  }

  function defs() {
    const d = svgEl("defs", {});
    for (const [id, cls, open] of [["map-arrow", "is-ctx", false], ["map-arrow-green", "is-new", false], ["map-arrow-red", "is-removed", false], ["map-arrow-open", "is-ctx", true]]) {
      const m = svgEl("marker", { id, viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 6, markerHeight: 6, orient: "auto-start-reverse" });
      m.appendChild(svgEl("path", { class: `map__arrow ${cls}` + (open ? " is-open" : ""), d: open ? "M0,0 L10,5 L0,10" : "M0,0 L10,5 L0,10 z" }));
      d.appendChild(m);
    }
    return d;
  }

  function impPath(a, b, o) {
    const ax = a.cx + o, bx = b.cx + o, ay = a.y + o, by = b.y + o;
    if (by > ay + 10) {
      const y1 = ay + a.h, y2 = by - 3, my = (y1 + y2) / 2;
      return `M${ax},${y1} C${ax},${my} ${bx},${my} ${bx},${y2}`;
    }
    if (Math.abs(by - ay) <= 10) {
      const x1 = bx > ax ? a.x + o + a.w : a.x + o, x2 = bx > ax ? b.x + o - 3 : b.x + o + b.w + 3, mx = (x1 + x2) / 2;
      return `M${x1},${ay + 15} C${mx},${ay + 15} ${mx},${by + 15} ${x2},${by + 15}`;
    }
    const x1 = a.x + o, x2 = b.x + o - 3;
    return `M${x1},${ay + 15} C${x1 - 70},${ay} ${x2 - 70},${by + b.h} ${x2},${by + b.h - 10}`;
  }

  function callPath(a, b, ya, yb, o) {
    ya += o; yb += o;
    const ax = a.x + o, bx = b.x + o, acx = a.cx + o, bcx = b.cx + o;
    if (a === b) { const x = ax + a.w; return `M${x},${ya} C${x + 34},${ya} ${x + 34},${yb} ${x + 3},${yb}`; }
    if (bcx > acx + 20) { const x1 = ax + a.w, x2 = bx - 3; return `M${x1},${ya} C${x1 + 50},${ya} ${x2 - 50},${yb} ${x2},${yb}`; }
    if (bcx < acx - 20) { const x1 = ax, x2 = bx + b.w + 3; return `M${x1},${ya} C${x1 - 50},${ya} ${x2 + 50},${yb} ${x2},${yb}`; }
    const x1 = ax + a.w, x2 = bx + b.w + 3;
    return `M${x1},${ya} C${x1 + 70},${ya} ${x2 + 70},${yb} ${x2},${yb}`;
  }

  // A hover redraws at most once a frame. A selection or a new model
  // re-renders the whole page, which asks for the map again (element).
  let frame = 0;
  sel.subscribe((what) => {
    if (what === "target") { for (const k of Object.keys(fold)) delete fold[k]; return; }
    if (what !== "hover" || !scroll.isConnected || frame) return;
    frame = requestAnimationFrame(() => { frame = 0; draw(); });
  });

  return { element, draw };
}
