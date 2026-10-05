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
// (storageGet/storageSet), files.js (file-row helpers), graph.js (svgEl)
// and overview-model.js (the pure helpers and the entity model).

const OVERVIEW_POLL_MS = 3000;
const OVERVIEW_HIDDEN_KEY = "mo.overview.hidden";
const OVERVIEW_MODE_KEY = "mo.overview.mode";

// Whether the architecture section shows packages or functions (calls.js).
const OVERVIEW_ARCH_VIEW_KEY = "mo.overview.archView";
const OVERVIEW_BG_POLL_MS = 15000; // while the tab is hidden, for the strip
const AGENT_REFRESH_MS = 15000; // a running subagent's transcript is re-read this often
const STRIP_AREAS = 4; // a change this spread out shows in the strip
const SCOPE_KEY = "mo.overview.scope."; // + windowID: the last scope check
const TICKET_RE = /[A-Z][A-Z0-9]+-\d+/;
const SEL_KEY = "mo.overview.sel."; // + target key: the selection history (sessionStorage)
const REVIEWED_KEY = "mo.overview.reviewed."; // + target key: {path: reviewSig}

// Skeletons stand in for the parts of the tab still loading: shimmering
// boxes laid out with the real sections' classes, so the page doesn't jump
// when the answer lands. They fade in after a moment (CSS), so a fast load
// never flashes one.

// bone is one shimmering box; w is a CSS width, h a height in px.
function bone(w, h = 12, cls = "") {
  const b = el("span", { class: "skel" + (cls ? " " + cls : ""), "aria-hidden": "true" });
  b.style.width = w;
  b.style.height = h + "px";
  return b;
}

// skelWrap marks a skeleton for assistive tech and the fade-in.
function skelWrap(cls, label, children) {
  return el("div", { class: "skel-wrap " + cls, role: "status", "aria-busy": "true" }, [el("span", { class: "skel-label", text: label }), ...children]);
}

function skelHead(title, extra = []) {
  return el("div", { class: "overview-section__head" }, [title ? el("h3", { text: title }) : bone("90px", 11), ...extra]);
}

// overviewSkeleton is the whole tab before its first /overview answer: the
// header (mode row, verdict, chips) and the section heads.
function overviewSkeleton() {
  const secHead = (w) => el("div", { class: "ov-sec" }, [el("div", { class: "ov-sec__head is-skel" }, [bone("6px", 6), bone("70px", 14), bone(w, 11)])]);
  return skelWrap("overview-skel", "Loading the overview…", [
    el("div", { class: "ov-head" }, [
      el("div", { class: "overview-head" }, [bone("170px", 26), bone("min(340px, 50%)", 14)]),
      el("div", { class: "ov-verdict" }, [bone("min(620px, 90%)", 24), bone("min(420px, 60%)", 24)]),
      el("div", { class: "ov-chips" }, ["60px", "110px", "70px", "150px"].map((w) => bone(w, 22))),
    ]),
    secHead("220px"), secHead("180px"), secHead("240px"), secHead("120px"),
  ]);
}

// archSkeleton is the import graph's box: rows of package nodes. head is
// the section head to keep (its view switch works while loading).
function archSkeleton(head) {
  const rows = [["90px", "120px"], ["110px", "70px", "130px"], ["100px"]];
  return el("div", { class: "overview-section" }, [
    head || skelHead(null, [bone("130px", 22)]),
    skelWrap("skel-graph", "Reading imports and contracts…", rows.map((r) => el("div", { class: "skel-graph__row" }, r.map((w) => bone(w, 24))))),
  ]);
}

// callsSkeleton is the Functions view's graph: callers, the changed
// functions grouped in a unit, callees.
function callsSkeleton() {
  const col = (ws) => el("div", { class: "skel-calls__col" }, ws.map((w) => bone(w, 22)));
  return skelWrap("skel-calls", "Reading functions and calls…", [
    col(["70%", "85%", "60%"]),
    el("div", { class: "skel-calls__unit" }, [bone("50%", 11), ...["90%", "75%", "82%", "68%"].map((w) => bone(w, 22))]),
    col(["80%", "65%"]),
  ]);
}

// createOverview builds the tab in panel. The chat view points it at a
// session (setWindow); the reviewer view at a branch (setTarget, without a
// transcript), and learns what the branch resolved to through onTarget.
function createOverview(panel, { onOpenDiff, onOpenFile, onVisible, describeUser = () => ({ kind: "user" }), revealTurn, strip, onShowOverview, onDraftPrompt, onMention, onTarget, onClearSelection } = {}) {
  let windowID = null; // the target's storage key (a window id, or "branch:…")
  let api = ""; // its endpoint prefix
  let withTranscript = true; // false in the reviewer view: no trace, no strip
  let available = false;
  let visible = false;
  let data = null; // the last /overview response
  let etag = null;
  let arch = null; // the last /architecture response
  let archEtag = null;
  let archView = storageGet(OVERVIEW_ARCH_VIEW_KEY) === "functions" ? "functions" : "packages";
  let calls = null; // the last /calls response (fetched while the tab is visible)
  let callsEtag = null;
  let callsError = "";
  let callsInflight = -1;
  const callsView = createCallsView({
    // A removed function's line is in the base version: open its diff
    // without a line.
    onOpen: (path, line, before) => openDiff({ path }, before ? undefined : line),
    onMention,
    // The prompt whose edit last changed a function (chat view only).
    changedIn: (f) => (withTranscript && mode !== "commits" && data?.root && f.status && !f.before ? turnForRange(trace, data.root, f.path, f.line, f.end || f.line) : null),
    onRevealTurn: revealTurn,
  });
  let lastLoad = 0;
  let fetching = false; // a fetch of origin's base is running
  const trace = createIntentTrace(describeUser);
  let traceShown = -1; // trace.version last rendered
  let traceTimer = 0;
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
  const storedMode = () => (storageGet(OVERVIEW_MODE_KEY) === "head" ? "head" : "branch");
  // mode is "branch", "head" or "commits" (commits selected in the Git
  // log, commitSel: [{hash, subject}] newest first). "commits" isn't
  // remembered: leaving it goes back to the stored mode.
  let mode = storedMode();
  let commitSel = [];
  // The selected commits' ids (sorted, comma-joined), and whether the
  // uncommitted changes (graph.js's WORKTREE row) are selected too.
  const selIDs = () => commitSel.map((c) => c.hash).filter((h) => h !== WORKTREE).sort().join(",");
  const selWorktree = () => commitSel.some((c) => c.hash === WORKTREE);
  const selKey = () => selIDs() + (selWorktree() ? "+wt" : "");
  const hidden = new Set(Array.isArray(storageGet(OVERVIEW_HIDDEN_KEY)) ? storageGet(OVERVIEW_HIDDEN_KEY) : []);

  // --- The explorer's model and selection ---
  // The model is rebuilt only when one of its inputs changed.
  let reviewed = {}; // path → reviewSig, for the target
  let reviewedVersion = 0;
  let model = null;
  let modelKey = "";
  function getModel() {
    if (!data?.repo) return null;
    const traced = withTranscript && mode !== "commits";
    const key = [gen, etag, archEtag, callsEtag, traced ? trace.version : -1, scope?.at || 0, reviewedVersion].join("|");
    if (key !== modelKey) {
      modelKey = key;
      model = buildModel({ overview: data, arch: arch?.repo ? arch : null, calls: calls?.repo ? calls : null, trace: traced ? trace : null, scope, reviewed });
    }
    return model;
  }

  // The selection: a history (Back/Forward), what the pointer is over, and
  // the function the map is focused on. Subscribers hear every change.
  let hist = selInitial();
  let hovered = null;
  let focusFn = null;
  const selSubs = new Set();
  function selChanged(what) {
    if (what !== "hover" && windowID) {
      try { sessionStorage.setItem(SEL_KEY + windowID, JSON.stringify(hist)); } catch (_) { /* not remembered */ }
    }
    for (const f of selSubs) f(what);
  }
  function loadSelection() {
    let h = null;
    try { h = JSON.parse(sessionStorage.getItem(SEL_KEY + windowID) || "null"); } catch (_) { h = null; }
    hist = selValid(h) ? h : selInitial();
    hovered = null;
    focusFn = null;
  }
  // Selecting something else than a function, a call or a finding leaves
  // Focus; another function moves it.
  function followFocus(id) {
    if (!focusFn) return;
    if (id?.startsWith("fn:")) focusFn = id;
    else if (!id || !(id.startsWith("call:") || id.startsWith("find:"))) focusFn = null;
  }
  const selection = {
    select(id) {
      const next = selPush(hist, id || null);
      if (next === hist) return;
      hist = next;
      followFocus(id);
      selChanged("select");
    },
    back() {
      const next = selBack(hist);
      if (next === hist) return;
      hist = next;
      followFocus(selCurrent(hist));
      selChanged("select");
    },
    forward() {
      const next = selForward(hist);
      if (next === hist) return;
      hist = next;
      followFocus(selCurrent(hist));
      selChanged("select");
    },
    // go jumps to a breadcrumb's place in the history.
    go(at) {
      if (at === hist.at || at < 0 || at >= hist.stack.length) return;
      hist = { stack: hist.stack, at };
      followFocus(selCurrent(hist));
      selChanged("select");
    },
    hover(id) {
      if ((id || null) === hovered) return;
      hovered = id || null;
      selChanged("hover");
    },
    setFocus(id) {
      if ((id || null) === focusFn) return;
      focusFn = id || null;
      if (id) { hist = selPush(hist, id); }
      selChanged("focus");
    },
    current: () => selCurrent(hist),
    hovered: () => hovered,
    focus: () => focusFn,
    crumbs: () => selCrumbs(hist),
    canBack: () => hist.at > 0,
    canForward: () => hist.at < hist.stack.length - 1,
    subscribe(f) { selSubs.add(f); return () => selSubs.delete(f); },
  };

  const map = createMap({ model: getModel, selection, rerender: () => render() });

  // Alt+←/→ go back and forward, Esc leaves Focus and then clears the
  // selection, while the tab is showing and the keyboard isn't in a field.
  document.addEventListener("keydown", (e) => {
    if (!visible || e.defaultPrevented || e.metaKey || e.ctrlKey) return;
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
    if (document.querySelector("dialog[open]")) return;
    if (e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
      e.preventDefault();
      if (e.key === "ArrowLeft") selection.back(); else selection.forward();
    } else if (e.key === "Escape" && !e.altKey && !e.shiftKey) {
      if (focusFn) selection.setFocus(null);
      else if (selCurrent(hist)) selection.select(null);
      else return;
      e.preventDefault();
    }
  });

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
    const back = el("button", { class: "link-btn", type: "button", text: "Back to Branch" });
    back.addEventListener("click", () => setMode("branch"));
    root.replaceChildren(el("div", { class: "overview__note" }, [document.createTextNode(text), ...(mode === "commits" ? [document.createTextNode(" "), back] : [])]));
  }

  function setMode(m) {
    if (m === mode) return;
    mode = m;
    if (m !== "commits") storageSet(OVERVIEW_MODE_KEY, m);
    reload();
  }

  // reload drops what's shown and reads the current mode afresh.
  function reload() {
    data = null; etag = null; arch = null; archEtag = null; areaFilter = null; error = ""; gen++;
    resetCalls();
    render();
    load();
  }

  // setSelection follows the Git log's selection: in commits mode a new set
  // is reloaded and an empty one leaves the mode; otherwise only the
  // header's Selected button changes.
  function setSelection(sel) {
    const before = selKey();
    commitSel = sel || [];
    if (mode === "commits") {
      if (!commitSel.length) { mode = storedMode(); reload(); return; }
      if (selKey() !== before) { reload(); return; }
    }
    if (data) render();
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

  // redFindings counts the call findings that are bugs waiting to happen
  // (a removed function still called, a view binding a Stimulus method that
  // doesn't exist, a route to a missing action), from the last /calls
  // answer (read while the tab is visible).
  const RED_FINDINGS = ["removed-called", "stimulus-unbound", "route-without-action"];
  function redFindings() {
    return calls?.repo ? (calls.findings || []).filter((f) => RED_FINDINGS.includes(f.kind)).length : 0;
  }
  const redText = (n) => n === 1 ? "1 removed or missing function is still called" : `${n} removed or missing functions are still called`;

  function renderBadge() {
    const v = arch?.violations || 0, r = redFindings();
    const n = v + r;
    badge.hidden = !n;
    badge.textContent = String(n);
    const parts = [];
    if (v) parts.push(v === 1 ? "1 import breaks a layer rule" : `${v} imports break a layer rule`);
    if (r) parts.push(redText(r));
    badge.title = parts.join("; ");
  }

  function render() {
    renderBadge();
    if (!data) {
      if (error || !available) note(error || "No live session.");
      // Rebuilt only when not already shown, so the fade-in doesn't restart.
      else if (!root.firstChild?.classList.contains("overview-skel")) root.replaceChildren(overviewSkeleton());
      return;
    }
    if (!data.repo) { note("This session isn't in a git checkout."); return; }
    const sum = summarizeOverview(data);
    const files = data.files || [];
    if (!files.length) {
      root.replaceChildren(pageHead(null), el("div", { class: "overview__note ov-empty", text: data.mode === "commits" ? "These commits don't change any files." : data.mode === "branch" ? `No changes against ${data.base}.` : "No uncommitted changes." }));
      renderStrip();
      return;
    }
    const M = getModel();
    const sums = ovSectionSummaries(M, { scope: scopeBusy ? "running" : scope?.result && scope.mode === data.mode ? "done" : "idle", stale: !!scope?.result && scope.mode === data.mode && scope.sig !== scopeSig(), hidden: hidden.size, area: areaFilter, focus: focusFn });
    const traced = withTranscript && mode !== "commits";
    const secs = [
      section("map", "Map", sums.map, () => mapBody()),
      section("foot", "Footprint", sums.foot, () => el("div", { class: "ov-sec__stack" }, [noiseBar(), body(sum)])),
      ...(traced ? [section("trace", "Intent", sums.trace, () => traceBox)] : []),
      ...(mode !== "commits" ? [section("scope", "Scope", sums.scope, () => scopeBody())] : []),
    ];
    root.replaceChildren(pageHead(M), ...secs);
    if (isOpen("foot")) drawTreemap();
    if (traced && isOpen("trace")) renderTrace();
    renderStrip();
    selChanged("model");
  }

  // --- The page: a header, then sections that open where the selection is ---

  let manual = {}; // section key → open, set by its head; cleared by a new selection
  let caveatsOpen = false;

  function isOpen(key) {
    if (key in manual) return manual[key];
    const cur = selCurrent(hist);
    return cur ? key === ovSection(getModel(), cur) : true;
  }

  function section(key, title, { summary, flag }, body) {
    const open = isOpen(key);
    const head = el("button", { class: "ov-sec__head", type: "button", "aria-expanded": String(open) }, [
      el("span", { class: "ov-sec__chev" + (open ? " is-open" : ""), "aria-hidden": "true" }),
      el("span", { class: "ov-sec__title", text: title }),
      el("span", { class: "ov-sec__sum", text: summary }),
      ...(flag ? [el("span", { class: `ov-sec__flag is-${flag}`, title: flag === "red" ? "Something here breaks" : "Worth a look" })] : []),
    ]);
    head.addEventListener("click", () => { manual[key] = !open; render(); });
    return el("section", { class: "ov-sec" + (open ? " is-open" : ""), "data-sec": key }, [head, ...(open ? [el("div", { class: "ov-sec__body" }, [body()])] : [])]);
  }

  // revealSection scrolls the selection's section to just under the header.
  function revealSection() {
    const cur = selCurrent(hist);
    if (!cur || !visible) return;
    const sec = root.querySelector(`[data-sec="${ovSection(getModel(), cur)}"]`);
    const head = root.querySelector(".ov-head");
    if (!sec) return;
    const top = sec.getBoundingClientRect().top - panel.getBoundingClientRect().top;
    panel.scrollTop = Math.max(0, panel.scrollTop + top - (head?.offsetHeight || 0));
  }

  // The selection decides which sections are open, so a new one re-renders.
  selection.subscribe((what) => {
    if (what !== "select" && what !== "focus") return;
    manual = {};
    if (!data?.repo) return;
    render();
    requestAnimationFrame(revealSection);
  });

  // selMark is the class a part of a section gets for the selection: the
  // selected entity, one it relates to, or (with a selection) the rest.
  function selMark(id) {
    const cur = selCurrent(hist);
    if (!cur) return "";
    if (id === cur) return " is-selected";
    const M = getModel();
    if (!M) return "";
    if (selMark.for !== cur || selMark.model !== M) { selMark.for = cur; selMark.model = M; selMark.set = ovRelated(M, cur); }
    return selMark.set.has(id) ? " is-related" : " is-dim";
  }

  // selectable makes a node select id on click and open its diff on a
  // double click.
  function selectable(node, id, openID = id) {
    node.addEventListener("click", () => selection.select(id));
    node.addEventListener("dblclick", () => openEntity(openID, "diff"));
    node.addEventListener("mouseenter", () => selection.hover(id));
    node.addEventListener("mouseleave", () => selection.hover(null));
    return node;
  }

  // pageHead is the header: what's compared, notes, the verdict and the
  // counts. Once something is selected it shrinks to a sticky strip.
  function pageHead(M) {
    const compact = !!selCurrent(hist);
    const parts = [header(), ...headNotes()];
    if (M) {
      const cur = selCurrent(hist);
      const segs = ovVerdict(M, { ticket: scope?.ticket?.id || ticketKey(), filtered: data.mode !== "branch" });
      const verdict = el("div", { class: "ov-verdict" }, segs.map((sg) => {
        const span = el("span", { class: "ov-verdict__seg" + (sg.tone ? ` is-${sg.tone}` : "") + (sg.target ? " is-link" : "") + (sg.target && sg.target === cur ? " is-on" : ""), text: sg.t });
        if (sg.target) {
          span.setAttribute("role", "button");
          span.tabIndex = 0;
          span.addEventListener("click", () => selection.select(sg.target));
          span.addEventListener("keydown", (e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); selection.select(sg.target); } });
          span.addEventListener("mouseenter", () => selection.hover(sg.target));
          span.addEventListener("mouseleave", () => selection.hover(null));
        }
        return span;
      }));
      if (!compact) verdict.append(" ", el("span", { class: "ov-verdict__sub", text: ovVerdictSub(M) }));
      parts.push(verdict);
      if (!compact) {
        const chipEls = ovChips(M).map((c) => {
          const b = el("button", { class: "ov-chip", type: "button", text: c.t });
          if (c.target) b.addEventListener("click", () => selection.select(c.target));
          else b.disabled = true;
          return b;
        });
        if (!M.loaded.arch) chipEls.push(bone("140px", 22), bone("110px", 22));
        parts.push(el("div", { class: "ov-chips" }, chipEls));
      }
      parts.push(caveatList(M));
    }
    return el("div", { class: "ov-head" + (compact ? " is-compact" : "") }, parts);
  }

  function caveatList(M) {
    const items = ovCaveats(M);
    const pending = !M.loaded.arch || !M.loaded.calls;
    const toggle = el("button", { class: "ov-caveats__toggle" + (caveatsOpen ? " is-open" : ""), type: "button", "aria-expanded": String(caveatsOpen), text: `Caveats ${items.length}` + (pending ? "…" : "") });
    toggle.addEventListener("click", () => { caveatsOpen = !caveatsOpen; render(); });
    const list = caveatsOpen ? [el("div", { class: "ov-caveats__list" }, [
      ...items.map((c) => {
        const row = el("button", { class: `ov-caveat is-${c.tone}`, type: "button" }, [el("span", { class: "ov-caveat__sq", "aria-hidden": "true" }), el("span", { text: c.text })]);
        if (c.target) row.addEventListener("click", () => selection.select(c.target));
        else row.disabled = true;
        return row;
      }),
      ...(pending ? [bone("60%", 12)] : []),
    ])] : [];
    return el("div", { class: "ov-caveats" }, [toggle, ...list]);
  }

  // progressBar is "Reviewed X of Y" over the logic files, once there are any.
  function progressBar() {
    const M = getModel();
    if (!M) return [];
    const { done, total } = ovProgress(M);
    if (!total) return [];
    const fill = el("span", { class: "ov-progress__fill" });
    fill.style.width = `${Math.round((done / total) * 100)}%`;
    return [el("span", { class: "ov-progress", title: "Logic files ticked as reviewed (x in the Review list)" }, [
      el("span", {}, [document.createTextNode("Reviewed "), el("b", { text: String(done) }), document.createTextNode(` of ${total}`)]),
      el("span", { class: "ov-progress__bar" }, [fill]),
    ])];
  }

  // toggleReviewed ticks a file as reviewed in its current version, or
  // unticks it.
  function toggleReviewed(path) {
    const f = data?.files?.find((x) => x.path === path);
    if (!f || !windowID) return;
    const sig = reviewSig(f);
    if (reviewed[path] === sig) delete reviewed[path];
    else reviewed[path] = sig;
    // Ticks for files no longer in the change are dropped as they go.
    for (const p of Object.keys(reviewed)) if (!data.files.some((x) => x.path === p)) delete reviewed[p];
    storageSet(REVIEWED_KEY + windowID, reviewed);
    reviewedVersion++;
    render();
  }

  // headNotes are the yellow bars under the mode row: what makes this
  // comparison less than it looks.
  function headNotes() {
    const out = [];
    const bar = (text, action) => el("div", { class: "ov-note" }, [el("span", { class: "ov-note__text", text }), ...(action ? [action] : [])]);
    if (mode === "branch" && data.fallback) out.push(bar(data.head ? "Nothing to compare: the branch is already part of its base." : "On the default branch, or no default branch found: showing uncommitted changes."));
    if (data.truncated) out.push(bar("The file list is truncated."));
    const stale = staleBase();
    if (stale) out.push(stale);
    if (error) out.push(bar(error));
    return out;
  }

  // renderStrip fills the line above the composer: shown only when the
  // change needs attention (an import breaking a layer rule, or a change
  // spread over many areas).
  function renderStrip() {
    if (!strip || !withTranscript) return;
    // The strip is about the session's change, not a selection of commits.
    if (mode === "commits") { strip.hidden = true; delete strip.dataset.sig; return; }
    const parts = [];
    const v = arch?.repo ? arch.violations : 0;
    if (v) parts.push(el("span", { class: "overview-strip__bad", text: v === 1 ? "1 new import breaks a layer rule" : `${v} new imports break a layer rule` }));
    const rc = redFindings();
    if (rc) parts.push(el("span", { class: "overview-strip__bad", text: redText(rc) }));
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
      el("span", { class: "overview__muted", text: "Files by the prompt that first edited them. A square is a prompt's edits to a file; a column number is the prompt." }),
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
      const b = el("button", { class: "overview-trace__turnbtn" + selMark("prompt:" + n), type: "button", title: turnText(n), text: n === 0 ? "–" : String(n) });
      b.addEventListener("click", () => selection.select("prompt:" + n));
      th.appendChild(b);
      headRow.appendChild(th);
    }
    const body = el("tbody", {});
    for (const g of groups) {
      const label = g.turn === null ? "Not edited in this conversation (Bash, another session, or earlier)" : `${g.turn === 0 ? "Before the first prompt" : g.turn + ". " + oneLine(turnText(g.turn), 140)}`;
      body.appendChild(el("tr", { class: "overview-trace__group" + (g.turn === null ? " is-untraced" : "") }, [el("td", { colspan: String(columns.length + 1), title: g.turn ? turnText(g.turn) : "", text: label })]));
      for (const r of g.rows) {
        const fid = "file:" + r.f.path;
        const name = selectable(el("button", { class: "overview-trace__name", type: "button", title: `${r.f.path} — click to inspect, double-click for the changes`, text: r.f.path }), fid);
        const origin = g.turn === null ? getModel()?.E.get(fid)?.origin : null;
        const out = g.turn === null ? [el("i", { class: "overview-trace__out", title: origin ? `Not edited in this conversation; probably ${origin.how} in prompt ${origin.n}: ${origin.command}` : "Not edited in this conversation" })] : [];
        const tr = el("tr", { class: selMark(fid).trim() }, [el("td", { class: "overview-trace__file" }, [el("span", { class: "overview-trace__namewrap" }, [...out, name, ...driftTag(r.f.path)])])]);
        for (const n of columns) {
          const td = el("td", { class: "overview-trace__cell" });
          if (r.turns.has(n)) {
            const es = r.edits.filter((e) => e.turn === n);
            const cid = cellID(r.f.path, n);
            const cell = el("button", {
              class: "overview-trace__mark" + (es.every((e) => e.agent) ? " is-agent" : "") + (selCurrent(hist) === cid ? " is-selected" : ""),
              type: "button",
              title: `${es.length} edit${es.length === 1 ? "" : "s"} in prompt ${n}${es.some((e) => e.agent) ? " (by a subagent)" : ""} — click for the reason`,
            });
            selectable(cell, cid, "file:" + r.f.path);
            td.appendChild(cell);
          }
          tr.appendChild(td);
        }
        body.appendChild(tr);
      }
    }
    const table = el("table", { class: "overview-trace" }, [el("thead", {}, [headRow]), body]);
    traceBox.replaceChildren(head, el("div", { class: "overview-trace-scroll" }, [table]), el("div", { class: "overview-legend is-static" }, [
      el("span", {}, [el("i", { class: "overview-trace__mark is-legend" }), document.createTextNode("edited in that prompt (click for the reason)")]),
      el("span", {}, [el("i", { class: "overview-trace__mark is-agent is-legend" }), document.createTextNode("by a subagent")]),
      el("span", {}, [el("i", { class: "overview-trace__out" }), document.createTextNode("not edited in this conversation")]),
    ]));
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
    const b = el("button", { class: "btn btn--small btn--primary", type: "button", text: scopeBusy ? "Checking…" : key ? `Check scope against ${key}` : "Check scope", title: "Ask Claude which files drift from what was asked (no tools; usually under a minute)" });
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
    render();
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
      ...(stale ? [el("div", { class: "ov-note" }, [el("span", { class: "ov-note__text", text: "Out of date: the change moved on since this check." }), (() => { const b = el("button", { class: "ov-note__act", type: "button", text: "Run again" }); b.addEventListener("click", runScope); return b; })()])] : []),
    ];
    if (drift.length) {
      parts.push(el("div", { class: "overview-scope__list" }, drift.map((f) => selectable(el("button", { class: "overview-scope__row" + selMark("file:" + f.path), type: "button" }, [
        el("span", { class: "overview-tag is-drift", text: "drift" }),
        el("span", { class: "overview-scope__file" }, [el("span", { class: "mono", text: f.path }), el("span", { class: "overview__muted", text: f.reason })]),
      ]), "file:" + f.path))));
      if (onDraftPrompt) {
        const ask = el("button", { class: "btn btn--small btn--primary", type: "button", text: "Ask Claude to split these out" });
        ask.title = "Puts a prompt in the message box; nothing is sent until you send it";
        ask.addEventListener("click", () => onDraftPrompt(splitPrompt(drift)));
        parts.push(el("div", { class: "overview-scope__actions" }, [ask, el("span", { class: "overview__muted", text: "Drafts a prompt in the message box. Nothing is sent." })]));
      }
    }
    return [el("div", { class: "overview-scope" + (drift.length ? " is-drift" : "") }, parts)];
  }

  // scopeBody is the Scope section: the check's button and its answer.
  function scopeBody() {
    return el("div", { class: "overview-section" }, [
      el("div", { class: "overview-scope__run" }, [
        scopeButton(),
        el("span", { class: "overview__muted", text: "Does every file fit what " + (withTranscript ? "was asked" : "the branch is for") + "? The one part that asks a model. It only runs when you click." }),
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
    const seg = el("span", { class: "overview-seg", role: "group", "aria-label": "Compare with", ...(data.head && data.mode !== "commits" ? { hidden: "" } : {}) });
    const modes = [["branch", "Branch", "Everything since this branch split off the default branch"], ["head", "Uncommitted", "Only changes not committed yet"]];
    if (commitSel.length) modes.push(["commits", `Selected (${commitSel.length})`, "The commits selected in the Git log:\n" + commitSel.map((c) => `${c.hash.slice(0, 7)} ${c.subject}`).join("\n")]);
    for (const [m, label, title] of modes) {
      const b = el("button", { class: "overview-seg__btn" + (mode === m ? " is-active" : ""), type: "button", title, text: label, "aria-pressed": String(mode === m) });
      b.addEventListener("click", () => setMode(m));
      seg.appendChild(b);
    }
    if (commitSel.length) {
      const x = el("button", { class: "overview-seg__clear", type: "button", title: "Clear the Git log selection", "aria-label": "Clear the Git log selection", text: "×" });
      x.addEventListener("click", () => onClearSelection?.());
      seg.appendChild(x);
    }
    let what;
    if (data.mode === "commits") {
      const cs = commitSel.filter((c) => c.hash !== WORKTREE);
      const n = cs.length;
      const range = n > 1 ? `${cs[n - 1].hash.slice(0, 7)}..${cs[0].hash.slice(0, 7)}` : n ? cs[0].hash.slice(0, 7) : "";
      const count = n === 1 ? "1 commit" : `${n} commits`;
      what = [el("b", { text: !data.worktree ? count : n ? `${count} + uncommitted` : "Uncommitted changes" }),
        ...(range ? [document.createTextNode(" · "), el("span", { class: "mono", text: range })] : []),
        ...(cs[0] ? [el("span", { class: "overview__muted", text: ` · ${cs[0].subject}` })] : [])];
    } else if (data.mode === "branch") {
      what = [el("b", { text: data.branch || "HEAD" }), document.createTextNode(" vs "), el("span", { class: "mono", text: data.base }), document.createTextNode(" @ "), el("span", { class: "mono", title: data.mergeBase, text: data.mergeBase.slice(0, 7) }), el("span", { class: "overview__muted", text: " (merge base)" })];
    } else {
      what = [el("b", { text: data.branch || "HEAD" }), document.createTextNode(" — uncommitted changes")];
    }
    if (data.head && data.mode !== "commits") what.push(el("span", { class: "overview__muted", text: " · up to " }), el("span", { class: "mono", title: data.head, text: data.head.slice(0, 7) }));
    return el("div", { class: "overview-head" }, [
      seg,
      el("span", { class: "overview-head__what" }, what),
      ...progressBar(),
    ]);
  }

  // staleBase warns when origin's copy of the base is a week or more old:
  // the merge base is old too, and work already merged shows as changed.
  function staleBase() {
    if (data.mode !== "branch" || !data.baseFetched) return null;
    const days = Math.floor((Date.now() / 1000 - data.baseFetched) / 86400);
    if (days < 7) return null;
    const btn = el("button", { class: "ov-note__act", type: "button", text: fetching ? "Fetching…" : "Fetch" });
    btn.disabled = fetching;
    btn.addEventListener("click", fetchBase);
    return el("div", { class: "ov-note" }, [el("span", { class: "ov-note__text", text: `${data.base} was last fetched ${days} days ago, so work already merged may show as changed.` }), btn]);
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

  // mapBody is the Map section: the package map, or the Functions view.
  function mapBody() {
    if (!arch) return archSkeleton(skelHead(null, [viewSwitch()]));
    if (!arch.repo) return el("div");
    // Focus (from the inspector) shows the lens in either view.
    if (archView === "functions" && !focusFn) return functionsSection();
    return map.element(viewSwitch());
  }

  function functionsSection() {
    const head = el("div", { class: "overview-section__head" }, [el("h3", { text: "Calls" }), viewSwitch()]);
    const parts = [head];
    if (callsError && !calls) {
      parts.push(el("div", { class: "overview-rules is-error", text: `Couldn't read the calls: ${callsError}` }));
    } else if (!calls) {
      parts.push(callsSkeleton());
    } else if (!calls.repo) {
      return el("div");
    } else if (!calls.languages?.length) {
      parts.push(el("div", { class: "overview__note", text: "No functions in this change in a language the call graph reads (Go, Ruby, JavaScript/TypeScript, Python, Kotlin/Java, Swift)." }));
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
          el("h3", { text: "Files by area" }),
          ...(areaFilter ? [el("span", { class: "overview__muted" }, [document.createTextNode("Showing "), el("b", { text: areaFilter }), document.createTextNode(" here and in the Review list · "), clearFilterBtn()])] : []),
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
    const row = el("button", { class: "file-row overview-file" + (OVERVIEW_NOISE.has(f.kind) ? " is-noise" : "") + selMark("file:" + f.path), type: "button", title: `${title} — click to inspect, double-click for the changes` }, [
      el("span", { class: "file-row__mark " + (FILE_MARK_CLASS[f.status] || ""), text: f.status }),
      el("span", { class: "file-row__name" }, [
        el("span", { class: "file-row__dir", text: dir }),
        el("span", { class: "file-row__base", text: name }),
        ...(f.kind !== "logic" ? [el("span", { class: `overview-tag is-${f.kind}`, text: OVERVIEW_KIND_LABEL[f.kind].toLowerCase() })] : []),
        ...driftTag(f.path),
      ]),
      el("span", { class: "file-row__counts" }, fileCounts(f)),
    ]);
    return selectable(row, "file:" + f.path);
  }

  // drawTreemap tiles the areas by changed lines, then each area's files
  // inside it, so one big area still shows where its lines are. Clicking an
  // area's label filters the file list (and the Review list); clicking a
  // file selects it.
  function drawTreemap() {
    // Kinds hidden from the list are hidden from the treemap too.
    const sum = summarizeOverview({ ...data, files: (data.files || []).filter((f) => !hidden.has(f.kind)) });
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
          class: `overview-tile ${cls}` + (fw < 40 || fh < 16 ? " is-tiny" : "") + selMark("file:" + f.path),
          type: "button",
          title: `${f.path} (${OVERVIEW_KIND_LABEL[f.kind].toLowerCase()}): +${f.added} −${f.removed} — click to inspect, double-click for the changes`,
        }, [el("span", { text: splitPath(f.path).name })]);
        Object.assign(tile.style, { left: 1 + fx + "px", top: 1 + labelH + fy + "px", width: fw + "px", height: fh + "px" });
        selectable(tile, "file:" + f.path);
        box.appendChild(tile);
      }
      return box;
    }));
  }

  // openEntity opens where id is (an entity id, or a place {path, line,
  // side}): its diff ("diff") or the file ("file"), at its line when that
  // line is in the new version.
  function openEntity(id, how = "diff") {
    const w = typeof id === "object" ? id : ovWhere(getModel(), id);
    if (!w?.path || !data) return;
    const line = w.side === "new" && w.line ? w.line : undefined;
    if (how === "file" && w.side === "new") onOpenFile?.(w.path, line);
    else openDiff({ path: w.path }, line);
  }

  // openDiff shows a file's changes, scrolled to line if given.
  function openDiff(f, line) {
    if (data.mode === "commits") onOpenDiff?.(f.path, data.worktree ? "sdiff" : "range", line, selIDs());
    else onOpenDiff?.(f.path, data.mode === "branch" ? "bdiff" : "diff", line);
  }

  // modeQuery is the change the tab shows, as query parameters.
  function modeQuery() {
    const ids = selIDs();
    return mode !== "commits" ? `base=${mode}` : `base=commits${ids ? "&commits=" + ids : ""}${selWorktree() ? "&worktree=1" : ""}`;
  }

  // Excerpts (a few lines of a file as the change shows them) are cached
  // per overview version: a new /overview body starts afresh.
  const excerpts = new Map(); // key → {promise, value}
  let excerptsFor = "";
  function excerptKey(path, line, { side = "new", end = 0, ctx = 3 } = {}) {
    return [path, line, side, end, ctx].join("|");
  }
  function excerptCache() {
    const v = `${gen}|${etag}`;
    if (v !== excerptsFor) { excerpts.clear(); excerptsFor = v; }
    return excerpts;
  }
  function excerpt(path, line, opts = {}) {
    const cache = excerptCache();
    const key = excerptKey(path, line, opts);
    if (cache.has(key)) return cache.get(key).promise;
    const { side = "new", end = 0, ctx = 3 } = opts;
    const q = new URLSearchParams({ path, line: String(Math.max(1, line || 1)), side, ctx: String(ctx) });
    if (end) q.set("end", String(end));
    const entry = { value: undefined };
    entry.promise = fetch(`${api}/excerpt?${modeQuery()}&${q}`, { cache: "no-store" })
      .then(async (res) => {
        const body = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(body.error || String(res.status));
        entry.value = body;
        return body;
      })
      .catch((err) => { entry.value = null; cache.delete(key); throw err; });
    cache.set(key, entry);
    return entry.promise;
  }
  // peekExcerpt is a cached excerpt, or undefined while it loads.
  function peekExcerpt(path, line, opts = {}) {
    return excerptCache().get(excerptKey(path, line, opts))?.value;
  }

  // get fetches one endpoint with If-None-Match: null when unchanged.
  async function get(what, tag) {
    const q = modeQuery();
    const res = await fetch(`${api}/${what}?${q}`, {
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

  // loadCalls polls /calls on its own while the tab is visible: it can
  // take seconds, and the rest of the tab shouldn't wait.
  async function loadCalls() {
    if (!visible || !windowID || !available || callsInflight === gen) return;
    if (mode === "commits" && !selWorktree() && calls) return; // commits never change
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
    if (mode === "commits" && !selWorktree() && data) return; // commits never change
    if (visible || Date.now() - lastLoad >= OVERVIEW_BG_POLL_MS) load();
  }, OVERVIEW_POLL_MS);
  document.addEventListener("visibilitychange", () => { if (visible) load(); });

  return {
    badge,
    // model is the explorer's entity model of what's shown (null before
    // the first overview), and selection its selection store.
    model: getModel,
    selection,
    open: openEntity,
    excerpt,
    peekExcerpt,
    // What the inspector can do beyond showing: each null where the page
    // can't (the reviewer view has no chat, and Selected mode no trace).
    revealPrompt: (n) => jumpTo(n),
    canRevealPrompt: () => !!revealTurn && withTranscript,
    mention: onMention ? (text) => onMention(text) : null,
    draft: onDraftPrompt ? (text) => onDraftPrompt(text) : null,
    mode: () => data?.mode || mode,
    toggleKind,
    hiddenKinds: () => hidden,
    clearHidden() { hidden.clear(); storageSet(OVERVIEW_HIDDEN_KEY, []); render(); },
    // area is the Footprint's area filter, which the Review list follows.
    area: () => areaFilter,
    setArea(a) { areaFilter = a || null; render(); },
    toggleReviewed,
    visible: () => visible,
    // available reports whether the tab has a change to show.
    hasData: () => !!data?.repo,
    setSelection,
    // showSelection switches to the commits selected in the Git log.
    showSelection(sel) {
      setSelection(sel);
      if (commitSel.length) setMode("commits");
    },
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
      commitSel = [];
      if (mode === "commits") mode = storedMode();
      data = null; etag = null; arch = null; archEtag = null; areaFilter = null; error = ""; gen++;
      resetCalls();
      scope = id ? storageGet(SCOPE_KEY + id) : null;
      scopeBusy = false; scopeError = "";
      const r = id ? storageGet(REVIEWED_KEY + id) : null;
      reviewed = r && typeof r === "object" && !Array.isArray(r) ? r : {};
      reviewedVersion++;
      if (id) loadSelection(); else { hist = selInitial(); hovered = null; focusFn = null; }
      selChanged("target");
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
      onVisible?.(v);
      if (v) {
        if (data) { drawTreemap(); if (withTranscript && trace.version !== traceShown) renderTrace(); }
        load();
      }
    },
  };
}
