// reviewqueue.js — the Files panel's Review and Branch files tabs, shown
// while the Overview tab is open. Review is what to read, in order: what
// needs eyes (findings, broken imports, files outside the ask), the
// contracts, the logic files biggest first (with reviewed ticks), then the
// tests, docs and noise, folded (ovReviewQueue). Branch files is every
// file the change touches. Rows select their entity (double-click opens
// the diff); j/k or ↓/↑ step through them, x ticks a file reviewed, o
// opens the diff and f the file. Needs common.js (el), files.js
// (splitPath, FILE_MARK_CLASS) and overview-model.js.

function createReviewQueue(overview) {
  const sel = overview.selection;
  const open = new Set(); // folded groups the user opened
  let order = []; // the ids of the rows last shown, top to bottom
  let onChange = null; // the panel's re-render, while a tab of ours shows

  const FOLDED = new Set(["tests", "docs", "noise"]);

  function row(M, id, { tick = false, active, related }) {
    const e = M.E.get(id);
    const cls = ["rq-row"];
    if (id === active) cls.push("is-selected");
    else if (related && related.has(id)) cls.push("is-related");
    else if (related) cls.push("is-dim");
    let lead, title, sub = "", counts = [], tag = null;
    if (e.type === "file") {
      lead = tick
        ? btnTick(e)
        : el("span", { class: "rq-row__mark file-row__mark " + (FILE_MARK_CLASS[e.status] || ""), text: e.status });
      title = e.name;
      sub = e.dir || "./";
      counts = fileCounts(e);
      if (e.drift) tag = el("span", { class: `overview-tag is-${e.drift.verdict}`, title: e.drift.reason, text: e.drift.verdict });
      if (e.status === "D") cls.push("is-deleted");
      if (e.reviewed && tick) cls.push("is-reviewed");
    } else if (e.type === "find") {
      lead = el("span", { class: `rq-row__sq is-${e.sev}` });
      title = ovFindingTitle(M, e);
      const t = e.sites[0];
      sub = t ? `${t.path}:${t.line}` + (e.sites.length > 1 ? ` +${e.sites.length - 1}` : "") : "";
      cls.push("is-sans");
    } else if (e.type === "imp") {
      lead = el("span", { class: "rq-row__sq is-red" });
      title = `${e.from} → ${e.to}`;
      sub = "Breaks a layer rule";
    } else if (e.type === "con") {
      lead = el("span", { class: "rq-row__mark is-" + (e.op === "+" ? "add" : e.op === "-" ? "del" : "mod"), text: e.op === "-" ? "−" : e.op });
      title = e.name;
      sub = e.catLabel;
      if (e.op === "-") cls.push("is-deleted");
    }
    if (e.type === "file" && e.drift && tick) sub = e.drift.reason;
    const name = el("button", { class: "rq-row__name", type: "button", title: "Click to inspect · double-click to open the diff" }, [
      el("span", { class: "rq-row__title", text: title }),
      ...(sub ? [el("span", { class: "rq-row__sub", text: sub })] : []),
    ]);
    name.addEventListener("click", () => sel.select(id));
    name.addEventListener("dblclick", () => overview.open(id, "diff"));
    const r = el("div", { class: cls.join(" "), "data-id": id }, [lead, name, el("span", { class: "rq-row__end" }, [...(counts.length ? [el("span", { class: "file-row__counts" }, counts)] : []), ...(tag ? [tag] : [])])]);
    r.addEventListener("mouseenter", () => sel.hover(id));
    r.addEventListener("mouseleave", () => sel.hover(null));
    return r;
  }

  function btnTick(e) {
    const b = el("button", { class: "rq-tick" + (e.reviewed ? " is-on" : ""), type: "button", title: e.reviewed ? "Reviewed — click to untick (x)" : "Mark reviewed (x)", "aria-pressed": String(e.reviewed), "aria-label": `${e.reviewed ? "Unmark" : "Mark"} ${e.path} reviewed` });
    b.addEventListener("click", () => overview.toggleReviewed(e.path));
    return b;
  }

  function filterChips() {
    const chips = [];
    const area = overview.area();
    if (area) chips.push(chip(`Area: ${area}`, () => overview.setArea(null)));
    const hidden = overview.hiddenKinds();
    if (hidden.size) chips.push(chip(`${pl(hidden.size, "kind")} hidden`, () => overview.clearHidden()));
    return chips.length ? [el("div", { class: "rq-filters" }, chips)] : [];
  }

  function chip(text, clear) {
    const b = el("button", { class: "rq-filter", type: "button", title: "Clear this filter" }, [document.createTextNode(text), el("span", { class: "rq-filter__x", text: "×" })]);
    b.addEventListener("click", clear);
    return b;
  }

  function relatedSet(M) {
    const id = sel.hovered() || sel.current();
    return id ? ovRelated(M, id) : null;
  }

  // review renders the Review tab into list.
  function review(list) {
    const M = overview.model();
    order = [];
    if (!M) { list.replaceChildren(el("div", { class: "files-pane__note", text: overview.hasData() ? "Loading…" : "No change to review." })); return; }
    const active = sel.current();
    const related = relatedSet(M);
    const groups = ovReviewQueue(M, { area: overview.area(), hidden: overview.hiddenKinds() });
    const parts = [...filterChips()];
    for (const g of groups) {
      const folded = FOLDED.has(g.key);
      const isOpen = !folded || open.has(g.key) || (active && g.ids.includes(active));
      const headCls = "rq-group__head" + (folded ? " is-toggle" : "");
      const h = el(folded ? "button" : "div", { class: headCls, ...(folded ? { type: "button", "aria-expanded": String(isOpen) } : {}) }, [
        ...(folded ? [el("span", { class: "rq-group__chev" + (isOpen ? " is-open" : ""), "aria-hidden": "true" })] : []),
        el("span", { class: "rq-group__label", text: g.label }),
        el("span", { class: "rq-group__n", text: String(g.ids.length) }),
      ]);
      if (folded) h.addEventListener("click", () => { if (open.has(g.key)) open.delete(g.key); else open.add(g.key); onChange?.(); });
      parts.push(h);
      if (!isOpen) continue;
      if (!g.ids.length) parts.push(el("div", { class: "rq-empty", text: g.key === "logic" ? "No logic files" + (overview.area() || overview.hiddenKinds().size ? " with these filters." : ".") : "None." }));
      for (const id of g.ids) {
        parts.push(row(M, id, { tick: g.key === "logic", active, related }));
        order.push(id);
      }
    }
    list.replaceChildren(...parts);
    list.querySelector(".rq-row.is-selected")?.scrollIntoView({ block: "nearest" });
  }

  // branchFiles renders every file of the change, by path.
  function branchFiles(list) {
    const M = overview.model();
    order = [];
    if (!M) { list.replaceChildren(el("div", { class: "files-pane__note", text: "Loading…" })); return; }
    const active = sel.current();
    const related = relatedSet(M);
    const files = [...M.E.values()].filter((e) => e.type === "file").sort((a, b) => (a.path < b.path ? -1 : 1));
    order = files.map((f) => f.id);
    list.replaceChildren(...(files.length ? files.map((f) => row(M, f.id, { active, related })) : [el("div", { class: "files-pane__note", text: "No files." })]));
    list.querySelector(".rq-row.is-selected")?.scrollIntoView({ block: "nearest" });
  }

  function count() {
    const M = overview.model();
    if (!M) return "";
    const needs = ovReviewQueue(M).find((g) => g.key === "needs");
    return needs ? String(needs.ids.length) : "";
  }

  // Hover changes re-render at most once a frame.
  let frame = 0;
  sel.subscribe((what) => {
    if (!onChange) return;
    if (what !== "hover") { onChange(); return; }
    if (frame) return;
    frame = requestAnimationFrame(() => { frame = 0; onChange?.(); });
  });

  // The keys, while the Overview shows and one of our tabs is in front.
  document.addEventListener("keydown", (e) => {
    if (!onChange || !overview.visible() || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
    if (document.querySelector("dialog[open]")) return;
    const cur = sel.current();
    const i = order.indexOf(cur);
    if (e.key === "j" || e.key === "ArrowDown") {
      if (order.length) sel.select(order[i < 0 ? 0 : Math.min(order.length - 1, i + 1)]);
    } else if (e.key === "k" || e.key === "ArrowUp") {
      if (order.length) sel.select(order[i < 0 ? 0 : Math.max(0, i - 1)]);
    } else if (e.key === "x" && cur?.startsWith("file:")) {
      overview.toggleReviewed(cur.slice(5));
    } else if ((e.key === "o" || e.key === "f") && cur) {
      overview.open(cur, e.key === "o" ? "diff" : "file");
    } else return;
    e.preventDefault();
  });

  return {
    review, branchFiles, count,
    // attach is the panel's re-render while a tab of ours shows (null when not).
    attach(fn) { onChange = fn; },
  };
}
