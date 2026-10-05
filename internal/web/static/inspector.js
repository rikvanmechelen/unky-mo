// inspector.js — the Overview's inspector, in the chat view's left rail
// while the Overview tab shows: a nav bar (Back / Forward through the
// selection history, breadcrumbs, Clear) over what's selected. With
// nothing selected it's "Start here": the kinds bar and the checks
// (ovChecks), each a way into the change. It renders from overview.model()
// and overview.selection (overview.js), and opens code through
// overview.open. Needs common.js (el) and overview-model.js.

const ENTITY_KIND = { pkg: "Package", file: "File", fn: "Function", call: "Call", imp: "Import", con: "Contract", find: "Finding", prompt: "Prompt" };

function createInspector(host, overview) {
  const sel = overview.selection;
  const bar = el("div", { class: "insp-bar" });
  const body = el("div", { class: "insp-body" });
  host.replaceChildren(bar, body);

  function btn(cls, text, title, onClick) {
    const b = el("button", { class: cls, type: "button", title, "aria-label": title, text });
    b.addEventListener("click", onClick);
    return b;
  }

  function renderBar(M) {
    const back = btn("insp-bar__nav is-back", "", "Back (Alt+←)", () => sel.back());
    const fwd = btn("insp-bar__nav is-forward", "", "Forward (Alt+→)", () => sel.forward());
    back.disabled = !sel.canBack();
    fwd.disabled = !sel.canForward();
    const crumbs = el("span", { class: "insp-bar__crumbs" });
    sel.crumbs().forEach((c, i, all) => {
      if (i) crumbs.append(el("span", { class: "insp-bar__sep", text: "/" }));
      const b = btn("insp-bar__crumb" + (i === all.length - 1 ? " is-current" : ""), M ? ovLabel(M, c.id) || c.id : c.id, "", () => sel.go(c.at));
      b.removeAttribute("aria-label");
      crumbs.append(b);
    });
    const parts = [back, fwd, crumbs];
    if (sel.current()) parts.push(btn("insp-bar__clear", "Clear", "Clear the selection (Esc)", () => sel.select(null)));
    bar.replaceChildren(...parts);
  }

  function render() {
    const M = overview.model();
    renderBar(M);
    const id = sel.current();
    if (!M) {
      body.replaceChildren(el("div", { class: "insp-note", text: overview.hasData() ? "" : "Nothing to show yet." }));
      return;
    }
    if (!id) { body.replaceChildren(...startHere(M)); return; }
    const exists = id.startsWith("cell:") ? M.E.has("file:" + parseCell(id).path) : M.E.has(id);
    if (!exists) {
      body.replaceChildren(
        el("div", { class: "insp-kicker", text: "Gone" }),
        el("div", { class: "insp-title", text: "No longer in this change" }),
        el("div", { class: "insp-text", text: "The change moved on, or this view shows a different part of it." }),
        el("div", { class: "insp-actions" }, [btn("insp-act", "Clear", "Clear the selection (Esc)", () => sel.select(null))]),
      );
      return;
    }
    body.replaceChildren(...entityView(M, id));
  }

  // startHere is the inspector with nothing selected.
  function startHere(M) {
    const o = M.overview;
    const kinds = OVERVIEW_KINDS.filter((k) => o.kinds?.[k.kind]?.files);
    const total = kinds.reduce((s, k) => s + Math.max(1, o.kinds[k.kind].lines), 0);
    const hidden = overview.hiddenKinds();
    const barEl = el("div", { class: "insp-kinds__bar", role: "img", "aria-label": kinds.map((k) => `${k.label} ${o.kinds[k.kind].lines}`).join(", ") + " changed lines" });
    const legend = el("div", { class: "insp-kinds__legend" });
    for (const k of kinds) {
      const t = o.kinds[k.kind];
      const seg = el("i", { class: `overview-bar__seg is-${k.kind}` + (hidden.has(k.kind) ? " is-hidden" : "") });
      seg.style.flexGrow = String(Math.max(1, t.lines) / total);
      barEl.append(seg);
      const item = el("button", { class: "overview-legend__item" + (hidden.has(k.kind) ? " is-hidden" : ""), type: "button", "aria-pressed": String(!hidden.has(k.kind)), title: hidden.has(k.kind) ? `Show ${k.label.toLowerCase()} files` : `Hide ${k.label.toLowerCase()} files from Footprint and Review` }, [
        el("i", { class: `overview-sw is-${k.kind}` }),
        document.createTextNode(`${k.label} ${t.lines}`),
      ]);
      item.addEventListener("click", () => overview.toggleKind(k.kind));
      legend.append(item);
    }
    const list = el("div", { class: "insp-checks" }, ovChecks(M, { filtered: o.mode === "commits" }).map((c) => {
      const row = el("button", { class: `insp-check is-${c.tone}`, type: "button" }, [
        el("span", { class: "insp-check__sq", "aria-hidden": "true" }),
        el("span", { class: "insp-check__text" }, [
          el("span", { class: "insp-check__label", text: c.label }),
          ...(c.sub ? [el("span", { class: "insp-check__sub", text: c.sub })] : []),
        ]),
      ]);
      if (c.target) {
        row.addEventListener("click", () => sel.select(c.target));
        row.addEventListener("mouseenter", () => sel.hover(c.target));
        row.addEventListener("mouseleave", () => sel.hover(null));
      } else row.disabled = true;
      if (c.tone === "pending") row.prepend(bone("10px", 10));
      return row;
    }));
    return [
      el("div", { class: "insp-kicker", text: "Nothing selected" }),
      el("div", { class: "insp-title is-sans", text: "Start here" }),
      el("div", { class: "insp-kinds" }, [barEl, legend]),
      list,
    ];
  }

  // entityView is a selected entity: what it is, and where to read it.
  function entityView(M, id) {
    if (id.startsWith("cell:")) {
      const { path, n } = parseCell(id);
      return [
        el("div", { class: "insp-kicker", text: "Edits · file × prompt" }),
        el("div", { class: "insp-title", text: `${path.split("/").pop()} × ${n === 0 ? "before the first prompt" : "prompt " + n}` }),
        actions(id),
      ];
    }
    const e = M.E.get(id);
    const where = e.type === "fn" ? e.unit : e.type === "file" ? e.dir || "./" : e.type === "con" ? e.catLabel : "";
    return [
      el("div", { class: "insp-kicker", text: ENTITY_KIND[e.type] + (where ? " · " + where : "") }),
      el("div", { class: "insp-title" + (e.type === "find" || e.type === "prompt" ? " is-sans" : ""), text: e.type === "prompt" ? e.text || "(before the first prompt)" : e.type === "find" ? findingTitle(M, e) : ovLabel(M, id) }),
      actions(id),
    ];
  }

  function findingTitle(M, f) {
    const fn = f.fnId ? M.E.get(f.fnId) : null;
    return `${fn?.name || f.func} ${CALL_FINDING_TEXT[f.kind] || f.kind}`;
  }

  function actions(id) {
    const w = ovWhere(overview.model(), id);
    if (!w) return el("div");
    return el("div", { class: "insp-actions" }, [
      btn("insp-act", "Diff", "Open the change in a tab (o)", () => overview.open(id, "diff")),
      ...(w.side === "new" ? [btn("insp-act", "File", "Open the whole file in a tab (f)", () => overview.open(id, "file"))] : []),
    ]);
  }

  sel.subscribe((what) => { if (what !== "hover") render(); });
  render();
  return { render };
}
