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

  // ── Blocks ──────────────────────────────────────────────────────

  function chips(list) {
    return el("div", { class: "insp-chips" }, list.filter(Boolean).map((c) => el("span", { class: "insp-chip" + (c.tone ? ` is-${c.tone}` : "") + (c.mono ? " is-mono" : ""), text: c.t, ...(c.title ? { title: c.title } : {}) })));
  }

  function alert(tone, text) {
    return el("div", { class: "insp-alert" + (tone ? ` is-${tone}` : ""), text });
  }

  function sigBlock(f) {
    if (!f.sig && !f.oldSig) return null;
    return el("div", { class: "insp-sig" }, [
      ...(f.oldSig ? [el("div", { class: "insp-sig__row is-old" }, [el("span", { class: "insp-sig__k", text: "Before" }), el("span", { text: f.oldSig })])] : []),
      ...(f.sig ? [el("div", { class: "insp-sig__row is-new" }, [el("span", { class: "insp-sig__k", text: "After" }), el("span", { text: f.sig })])] : []),
    ]);
  }

  function heading(text, count) {
    return el("div", { class: "insp-h" }, [el("span", { text }), ...(count != null ? [el("span", { class: "insp-h__n", text: String(count) })] : [])]);
  }

  // codeLines renders excerpt rows, marking the row at line on side.
  function codeLines(rows, line, side, cls = "") {
    const box = el("div", { class: "insp-code__lines" + (cls ? " " + cls : "") });
    for (const r of rows) {
      const n = side === "old" ? r.oldLn : r.ln;
      const focus = line && n === line && (r.sign !== (side === "old" ? "+" : "-"));
      box.append(el("div", { class: "insp-line" + (r.sign === "+" ? " is-add" : r.sign === "-" ? " is-del" : "") + (focus ? " is-focus" : "") }, [
        el("span", { class: "insp-line__n", text: String(r.ln || r.oldLn || "") }),
        el("span", { class: "insp-line__s", text: r.sign === " " ? "" : r.sign === "-" ? "−" : "+" }),
        el("span", { class: "insp-line__t", text: r.text }),
      ]));
    }
    return box;
  }

  // fill puts an excerpt's lines into box once they're in (synchronously
  // when cached), with bones meanwhile.
  function fill(box, path, line, opts, make) {
    const done = (x) => box.replaceChildren(...make(x));
    const cached = overview.peekExcerpt(path, line, opts);
    if (cached) { done(cached); return; }
    box.replaceChildren(bone("80%", 11), bone("60%", 11), bone("70%", 11));
    overview.excerpt(path, line, opts).then(done, () => box.replaceChildren(el("div", { class: "insp-code__note", text: "Not available." })));
  }

  // codeBlock is a titled excerpt of path around line, with Diff and File.
  function codeBlock(title, path, line, { side = "new", end = 0, ctx = 3, first = false } = {}) {
    if (!path) return null;
    const at = el("span", { class: "insp-code__at", text: line && !first ? `${path}:${line}` : path });
    const lines = el("div", { class: "insp-code__body" });
    const block = el("div", { class: "insp-code" }, [
      el("div", { class: "insp-h", text: title }),
      el("div", { class: "insp-code__box" }, [
        el("div", { class: "insp-code__head" }, [at, ...openLinks(path, line, side)]),
        lines,
      ]),
    ]);
    const make = (x) => {
      if (!x || x.binary || x.tooLarge) return [el("div", { class: "insp-code__note", text: x?.binary ? "A binary file." : x?.tooLarge ? "Too large to show." : "Not available." })];
      let rows = x.rows || [];
      let focus = line;
      if (first) {
        // The first change: three rows either side of the first +/- row.
        const i = ovFirstChange(rows);
        if (i < 0) return [el("div", { class: "insp-code__note", text: rows.length ? "No line changes (a rename, or whitespace only)." : "Empty." })];
        focus = side === "old" ? rows[i].oldLn : rows[i].ln || rows[i].oldLn;
        at.textContent = `${path}:${focus}`;
        rows = rows.slice(Math.max(0, i - 3), i + 4);
      }
      if (!rows.length) return [el("div", { class: "insp-code__note", text: "That line isn't in this version." })];
      return [codeLines(rows, focus, side)];
    };
    fill(lines, path, line || 1, first ? { side, end: 400, ctx: 0 } : { side, end, ctx }, make);
    return block;
  }

  function openLinks(path, line, side) {
    const out = [btn("insp-code__link", "Diff", "Open the change in a tab", () => overview.open(cellish(path, line, side), "diff"))];
    if (side === "new") out.push(btn("insp-code__link", "File", "Open the whole file in a tab", () => overview.open(cellish(path, line, side), "file")));
    return out;
  }

  // cellish turns a place into something overview.open understands.
  function cellish(path, line, side) {
    return { path, line, side };
  }

  // why is the prompts that explain a change: [{n, text, notes}].
  function why(title, items) {
    return el("div", { class: "insp-why" }, [
      el("div", { class: "insp-h", text: title }),
      ...items.map((w) => {
        const n = el("button", { class: "insp-why__n" + (w.n ? "" : " is-none"), type: "button", text: w.n ? String(w.n) : "—", title: w.n ? `Prompt ${w.n}` : "" });
        if (w.n) {
          n.addEventListener("click", () => sel.select("prompt:" + w.n));
          n.addEventListener("mouseenter", () => sel.hover("prompt:" + w.n));
          n.addEventListener("mouseleave", () => sel.hover(null));
        } else n.disabled = true;
        return el("div", { class: "insp-why__row" }, [n, el("div", { class: "insp-why__text" }, [
          el("span", { class: "insp-why__prompt", text: oneLine(w.text || "", 220) }),
          ...(w.notes || []).slice(-2).map((q) => el("span", { class: "insp-why__quote", text: `“${oneLine(q, 400)}”` })),
          ...(w.sub ? [el("span", { class: "insp-why__sub", text: w.sub })] : []),
        ])]);
      }),
    ]);
  }

  // rel is a list of related entities: {id, label, sub, mark, tone, site:
  // {path, line, side}} — a site shows its line and Peek / Diff / File.
  function rel(title, items, more = 0) {
    if (!items.length) return null;
    const M = overview.model();
    return el("div", { class: "insp-rel" }, [
      heading(title, items.length + more),
      ...items.map((it, i) => relRow(M, it, i < 8)),
      ...(more ? [el("div", { class: "insp-rel__more", text: `and ${more} more` })] : []),
    ]);
  }

  function relRow(M, it, withCode) {
    const e = it.id ? M.E.get(it.id) : null;
    const mark = it.mark ?? (e?.type === "fn" ? e.mark : e?.type === "file" ? e.status : e?.type === "con" ? (e.op === "-" ? "−" : e.op) : "");
    const name = btn("insp-rel__name" + (e?.type === "prompt" || e?.type === "find" ? " is-sans" : ""), it.label ?? (e ? ovLabel(M, it.id) : ""), "", () => it.id && sel.select(it.id));
    name.removeAttribute("aria-label");
    if (!it.id) name.disabled = true;
    else {
      name.addEventListener("mouseenter", () => sel.hover(it.id));
      name.addEventListener("mouseleave", () => sel.hover(null));
    }
    const main = [name, ...(it.sub ? [el("span", { class: "insp-rel__sub", text: it.sub })] : [])];
    if (it.site) {
      const { path, line, side } = it.site;
      const code = el("div", { class: "insp-rel__code" });
      if (withCode) fill(code, path, line, { side, ctx: 0 }, (x) => (x?.rows?.length ? [codeLines(x.rows, line, side, "is-one")] : []));
      const key = (sel.current() || "") + "|" + path + ":" + line;
      const open = peeks.has(key);
      const peek = btn("insp-rel__peek" + (open ? " is-open" : ""), open ? "Hide" : "Peek", "", () => { if (open) peeks.delete(key); else peeks.add(key); render(); });
      peek.removeAttribute("aria-label");
      main.push(code, el("div", { class: "insp-rel__links" }, [peek, ...openLinks(path, line, side)]));
      if (open) {
        const box = el("div", { class: "insp-rel__peekbox" });
        fill(box, path, line, { side, ctx: 3 }, (x) => (x?.rows?.length ? [el("div", { class: "insp-rel__peekat", text: `${path}:${line}` }), codeLines(x.rows, line, side)] : [el("div", { class: "insp-code__note", text: "Not available." })]));
        main.push(box);
      }
    }
    return el("div", { class: "insp-rel__row" + (it.tone ? ` is-${it.tone}` : "") }, [
      el("span", { class: "insp-rel__mark" + (it.square ? " has-sq" : ""), text: it.square ? "" : mark || "" }),
      el("div", { class: "insp-rel__main" }, main),
    ]);
  }

  // A call as a row seen from one end: the other function, where the
  // call is, and how.
  function callRow(M, cid, side) {
    const c = M.E.get(cid);
    const other = side === "in" ? c.fromId : c.toId;
    const t = c.sites[0];
    const words = ovCallWords(c).filter((w) => w !== "existing");
    return {
      id: other,
      sub: (t ? (side === "in" ? "calls it at " : "called at ") + `${t.path}:${t.line}` : "") + (words.length ? " · " + words.join(", ") : "") + (c.label ? ` · ${c.label}` : ""),
      tone: c.findings.some((f) => M.E.get(f).sev === "red") ? "red" : c.op === "+" ? "green" : c.op === "-" ? "red" : null,
      site: t ? { path: t.path, line: t.line, side: c.op === "-" ? "old" : "new" } : null,
    };
  }

  function actionsBar(list) {
    const items = list.filter(Boolean);
    if (!items.length) return null;
    return el("div", { class: "insp-actions" }, items.map((a, i) => {
      const b = btn("insp-act" + (i === 0 ? " is-primary" : ""), a.label, a.title || "", a.act);
      if (!a.title) b.removeAttribute("aria-label");
      return b;
    }));
  }

  // The usual actions: open the entity's place, and draft a fix.
  function openActs(id, w) {
    if (!w) return [];
    return [
      { label: "Diff", title: "Open the change in a tab (o)", act: () => overview.open(id, "diff") },
      ...(w.side === "new" ? [{ label: "File", title: "Open the whole file in a tab (f)", act: () => overview.open(id, "file") }] : []),
    ];
  }

  function fixAct(M, id) {
    const text = overview.draft ? ovFixPrompt(M, id) : "";
    return text ? { label: "Ask Claude to fix", title: "Puts a prompt in the message box; nothing is sent until you send it", act: () => overview.draft(text) } : null;
  }

  // promptsOf is a file's prompts as "why" items, at one turn or all.
  function promptsOf(M, fileId, n = null) {
    const fe = fileId ? M.E.get(fileId) : null;
    if (!fe) return [];
    return fe.prompts.filter((p) => n === null || p.n === n).map((p) => ({
      n: p.n, text: M.E.get("prompt:" + p.n)?.text || (p.n === 0 ? "(before the first prompt)" : ""), notes: p.notes,
      sub: `${pl(p.edits, "edit")}${p.agent ? " by a subagent" : ""} · ${p.tools.join(", ")}`,
    }));
  }

  // ── Per entity ──────────────────────────────────────────────────

  function entityView(M, id) {
    if (id.startsWith("cell:")) return cellView(M, id).filter(Boolean);
    const e = M.E.get(id);
    const views = { fn: fnView, call: callView, imp: impView, file: fileView, pkg: pkgView, con: conView, find: findView, prompt: promptView };
    return (views[e.type] || (() => []))(M, e).filter(Boolean);
  }

  function head(kicker, title, sans) {
    return [el("div", { class: "insp-kicker", text: kicker }), el("div", { class: "insp-title" + (sans ? " is-sans" : ""), text: title })];
  }

  function fnView(M, f) {
    const finds = f.findings.map((x) => M.E.get(x));
    const untested = finds.some((x) => x.kind === "untested");
    const tested = f.testedBy ? `Reached by ${f.testedBy.name}` + (f.testedBy.via ? ` (via ${f.testedBy.via})` : "") : "";
    const callers = f.callers.map((c) => callRow(M, c, "in"));
    const callees = f.callees.map((c) => callRow(M, c, "out"));
    const w = ovWhere(M, f.id);
    return [
      ...head("Function · " + (f.unit || "(root)"), f.name),
      chips([
        { t: f.status ? CALL_STATUS_TEXT[f.status] : "unchanged", tone: f.status === "removed" ? "red" : f.status === "added" ? "green" : null },
        f.test ? { t: "test" } : null,
        tested ? { t: tested, title: `${f.testedBy.path}:${f.testedBy.line}` } : untested ? { t: "No test reaches it", tone: "warn" } : null,
        f.from ? { t: `was ${f.from.split(/[./#:]/).pop()}`, mono: true, title: f.from } : null,
      ]),
      ...finds.filter((x) => x.sev !== "fold").map((x) => alert(x.sev, ovFindingTitle(M, x) + (x.sites.length ? ` — ${x.sites.slice(0, 3).map((t) => `${t.path}:${t.line}`).join(", ")}${x.sites.length > 3 ? " …" : ""}` : "") + ".")),
      f.unresolved ? alert(null, `${pl(f.unresolved, "call")} in it couldn’t be resolved (function values, or names that don’t exist), so they aren’t drawn.`) : null,
      sigBlock(f),
      f.path ? codeBlock(f.status === "removed" ? "What was removed" : f.status ? "Definition" : "Where it is", f.path, f.line, { side: f.before ? "old" : "new", end: Math.min(f.end || f.line, f.line + 10), ctx: 1 }) : null,
      rel("Implementations", f.impls.map((x) => ({ id: x }))),
      rel("Implements", f.implOf.map((x) => ({ id: x, sub: "calls through this interface reach it" }))),
      rel("Called by", callers, f.moreCallers),
      rel("Calls", callees),
      rel("Contracts", f.contracts.map((x) => ({ id: x, sub: M.E.get(x).catLabel }))),
      f.turn !== null ? why("Why it changed", promptsOf(M, f.fileId, f.turn)) : null,
      actionsBar([
        { label: "Focus", title: "Redraw the map around this function", act: () => sel.setFocus(f.id) },
        ...openActs(f.id, w),
        overview.mention ? { label: "Mention in prompt", act: () => overview.mention(`\`${f.name}\` (${f.path}:${f.line})`) } : null,
        fixAct(M, f.id),
      ]),
    ];
  }

  function callView(M, c) {
    const from = M.E.get(c.fromId), to = M.E.get(c.toId);
    const t = c.sites[0];
    return [
      ...head("Call", `${from?.name || "?"} → ${to?.name || "?"}`),
      chips([...ovCallWords(c).map((x) => ({ t: x, tone: x === "new" ? "green" : x === "removed" ? "red" : null })), c.label ? { t: c.label, mono: true } : null]),
      ...c.findings.map((x) => alert(M.E.get(x).sev, ovFindingTitle(M, M.E.get(x)) + ".")),
      t ? codeBlock(`Where ${from?.name || "the caller"} makes the call`, t.path, t.line, { side: c.op === "-" ? "old" : "new", ctx: 2 }) : null,
      to?.path ? codeBlock(`Where ${to.name} is defined`, to.path, to.line, { side: to.before ? "old" : "new", end: Math.min(to.end || to.line, to.line + 6), ctx: 0 }) : null,
      rel("More places", c.sites.slice(1).map((s) => ({ id: c.fromId, sub: `${s.path}:${s.line}`, site: { path: s.path, line: s.line, side: c.op === "-" ? "old" : "new" } }))),
      rel("Caller and callee", [{ id: c.fromId }, { id: c.toId }]),
      rel("Findings", c.findings.map((x) => ({ id: x }))),
    ];
  }

  const IMP_WORD = { broken: "breaks a layer rule", new: "new import", removed: "removed import", fixed: "no longer breaks a rule", ctx: "existing import" };

  function impView(M, e) {
    const t = e.files[0];
    const side = e.op === "-" ? "old" : "new";
    const calls = [...M.E.values()].filter((x) => x.type === "call" && M.E.get(x.fromId)?.unit === e.from && M.E.get(x.toId)?.unit === e.to);
    return [
      ...head("Import", `${e.from} → ${e.to}`),
      chips([{ t: IMP_WORD[e.kind], tone: e.kind === "broken" ? "red" : e.kind === "new" || e.kind === "fixed" ? "green" : null }, e.approx ? { t: "inferred from names" } : null, e.lang ? { t: LANG_LABEL[e.lang] || e.lang } : null]),
      e.violation ? alert("red", `Breaks: ${e.violation}`) : null,
      e.fixed ? alert(null, `No longer breaks: ${e.fixed}`) : null,
      e.approx ? alert(null, "Inferred from names, not an import statement, so it may be wrong.") : null,
      t ? codeBlock(e.op === "-" ? "Where it was" : "The import", t.path, t.line, { side, ctx: 1 }) : null,
      rel("More places", e.files.slice(1).map((s) => ({ label: `${s.path}:${s.line}`, site: { path: s.path, line: s.line, side } }))),
      rel("Calls across this import", calls.map((x) => ({ id: x.id, sub: ovCallWords(x).join(", ") }))),
      rel("Packages", [{ id: e.fromId }, { id: e.toId }]),
      actionsBar([...openActs(e.id, ovWhere(M, e.id)), fixAct(M, e.id)]),
    ];
  }

  const STATUS_WORD = { A: "Added", M: "Modified", D: "Deleted", R: "Renamed", "?": "Untracked", C: "Copied", T: "Type changed" };

  function fileView(M, f) {
    const prompts = promptsOf(M, f.id);
    const cons = [...M.E.values()].filter((x) => x.type === "con" && x.fileId === f.id);
    const side = f.status === "D" ? "old" : "new";
    return [
      ...head("File · " + (f.unit ?? f.area), f.name),
      chips([
        { t: f.dir || "./", mono: true },
        { t: STATUS_WORD[f.status] || f.status, tone: f.status === "D" ? "red" : f.status === "A" || f.status === "?" ? "green" : null },
        { t: OVERVIEW_KIND_LABEL[f.kind] || f.kind },
        f.added || f.removed ? { t: `+${f.added} −${f.removed}`, mono: true } : null,
        f.oldPath ? { t: `was ${f.oldPath}`, mono: true } : null,
        f.reviewed ? { t: "Reviewed", tone: "green" } : null,
      ]),
      f.drift ? alert("warn", (f.drift.verdict === "drift" ? "Outside the ask: " : "Unclear against the ask: ") + f.drift.reason) : null,
      f.binary ? alert(null, "A binary file.") : codeBlock("First change", f.status === "D" ? f.path : f.path, 1, { side, first: true }),
      rel("Functions in this file", f.fns.map((x) => ({ id: x }))),
      M.traced ? (prompts.length ? why("Edited by", prompts) : why("Edited by", [{ n: 0, text: "Not edited in this conversation", notes: [], sub: "Bash, another session, or earlier work" }])) : null,
      rel("Contracts", cons.map((x) => ({ id: x.id, sub: x.catLabel }))),
      actionsBar([
        ...openActs(f.id, ovWhere(M, f.id)),
        { label: f.reviewed ? "Unmark reviewed" : "Mark reviewed", title: "x", act: () => overview.toggleReviewed(f.path) },
        overview.mention ? { label: "Mention in prompt", act: () => overview.mention(`\`${f.path}\``) } : null,
      ]),
    ];
  }

  function pkgView(M, p) {
    const imps = [...M.E.values()].filter((x) => x.type === "imp" && x.changed);
    const fns = p.fns.map((x) => M.E.get(x)).sort((a, b) => (b.status ? 1 : 0) - (a.status ? 1 : 0));
    const STATUS = { added: "New package", changed: "Changed", removed: "Removed", context: "Unchanged — shown for context" };
    return [
      ...head("Package", p.unit || "(root)"),
      chips([{ t: STATUS[p.status] || p.status, tone: p.status === "added" ? "green" : p.status === "removed" ? "red" : null }, p.layer ? { t: "layer: " + p.layer } : null, p.lang ? { t: LANG_LABEL[p.lang] || p.lang } : null]),
      rel("Imports", imps.filter((x) => x.fromId === p.id).map((x) => ({ id: x.id, label: x.to, sub: IMP_WORD[x.kind] }))),
      rel("Imported by", imps.filter((x) => x.toId === p.id).map((x) => ({ id: x.id, label: x.from, sub: IMP_WORD[x.kind] }))),
      rel("Functions", fns.slice(0, 14).map((f) => ({ id: f.id })), Math.max(0, fns.length - 14)),
      rel("Changed files", p.files.slice(0, 14).map((x) => ({ id: x })), Math.max(0, p.files.length - 14)),
    ];
  }

  function conView(M, c) {
    const f = c.fnId ? M.E.get(c.fnId) : null;
    const side = c.op === "-" ? "old" : "new";
    return [
      ...head("Contract · " + c.catLabel, c.name),
      chips([{ t: { "+": "Added", "~": "Changed", "-": "Removed" }[c.op] || c.op, tone: c.op === "-" ? "red" : c.op === "+" ? "green" : null }]),
      c.detail ? el("div", { class: "insp-detail", text: c.detail }) : null,
      c.path ? codeBlock("Where it’s defined", c.path, c.line || 1, { side, ctx: 2 }) : null,
      f ? rel("Who relies on it", f.callers.map((x) => callRow(M, x, "in")), f.moreCallers) : null,
      rel("Defined in", [f ? { id: f.id } : null, c.fileId ? { id: c.fileId } : null].filter(Boolean)),
      actionsBar(openActs(c.id, ovWhere(M, c.id))),
    ];
  }

  const SEV_KICKER = { red: "Finding — breaks something", warn: "Finding — worth a look", fold: "Finding — a hint" };

  function findView(M, x) {
    const f = x.fnId ? M.E.get(x.fnId) : null;
    const t = x.sites[0];
    const testFile = x.kind === "test-not-updated";
    return [
      ...head(SEV_KICKER[x.sev], ovFindingTitle(M, x), true),
      x.kind === "untested" ? alert(null, "No test calls it within two calls. A hint: table-driven or reflective tests can reach it without the graph seeing.") : null,
      x.kind === "signature-callers" && f ? sigBlock(f) : null,
      t && !testFile ? codeBlock(x.kind === "removed-called" ? "Where it’s still called" : "A caller", t.path, t.line, { ctx: 2 }) : null,
      rel(testFile ? "Its test files" : "More places", (testFile ? x.sites : x.sites.slice(1)).map((s) => ({ label: `${s.path}:${s.line}`, site: { path: s.path, line: s.line, side: "new" } }))),
      f?.path ? codeBlock(f.status === "removed" ? "What was removed" : "The function", f.path, f.line, { side: f.before ? "old" : "new", end: Math.min(f.end || f.line, f.line + 6), ctx: 0 }) : null,
      rel("Involves", [f ? { id: f.id } : null, ...x.calls.map((c) => ({ id: M.E.get(c).fromId, sub: "a caller" }))].filter(Boolean)),
      actionsBar([...openActs(x.id, ovWhere(M, x.id)), fixAct(M, x.id)]),
    ];
  }

  function promptView(M, p) {
    const fns = [...M.E.values()].filter((x) => x.type === "fn" && x.turn === p.n);
    return [
      ...head(p.n === 0 ? "Before the first prompt" : `Prompt ${p.n}`, oneLine(p.text || "(before the first prompt)", 300), true),
      rel("Files it edited", p.files.map((f) => ({ id: "file:" + f.path, sub: `${pl(f.edits, "edit")}${f.agent ? " · subagent" : ""}` + (f.notes.length ? ` — “${oneLine(f.notes[f.notes.length - 1], 160)}”` : "") }))),
      rel("Functions it changed", fns.map((f) => ({ id: f.id }))),
      actionsBar([
        p.n && overview.canRevealPrompt() ? { label: "Jump to prompt in Chat", act: () => overview.revealPrompt(p.n) } : null,
        ...p.files.slice(0, 1).map((f) => ({ label: "Open diff", act: () => overview.open("file:" + f.path, "diff") })),
      ]),
    ];
  }

  function cellView(M, id) {
    const { path, n } = parseCell(id);
    const f = M.E.get("file:" + path);
    const item = f?.prompts.find((p) => p.n === n);
    return [
      ...head("Edits · file × prompt", `${f?.name || path} × ${n === 0 ? "before the first prompt" : "prompt " + n}`),
      item ? chips([{ t: pl(item.edits, "edit") }, { t: item.agent ? "Subagent" : "Main agent" }, { t: item.tools.join(", ") }]) : null,
      item ? why("The reason", promptsOf(M, f.id, n)) : null,
      f && !f.binary ? codeBlock("What the file’s change starts with", path, 1, { side: f.status === "D" ? "old" : "new", first: true }) : null,
      rel("Open", [{ id: "file:" + path }, ...(M.E.has("prompt:" + n) ? [{ id: "prompt:" + n }] : [])]),
      actionsBar(openActs(id, ovWhere(M, id))),
    ];
  }

  const peeks = new Set(); // "selection|path:line" of the sites peeked at

  sel.subscribe((what) => { if (what !== "hover") render(); });
  render();
  return { render };
}
