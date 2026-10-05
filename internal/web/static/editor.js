// Editor tabs for the chat view: the middle column becomes tabbed, with the
// chat (transcript + composer + terminal drawer) as a pinned first tab and
// one CodeMirror tab per opened file or diff (the file's changes against
// HEAD, with a per-change "Revert"), or one file's changes in a commit
// (read-only, from the Files panel's Git log tab). Line comments for Claude live in
// review.js. Tabs are remembered per window in
// localStorage. Files come from /api/sessions/{windowID}/file, which only
// serves paths the Files panel lists; the active tab re-polls it with
// If-None-Match so a file Claude edits updates in place.
//
// Editing: a tab remembers its base — the disk version the edit started
// from — and saves with PUT {path, text, baseHash}; the server refuses (409)
// if the file no longer has that hash, so a save never silently overwrites
// Claude's edit. Unsaved edits are kept as drafts in localStorage, so
// switching sessions or reloading the page doesn't lose them.
//
// CodeMirror is the vendored bundle's window.CM (vendor/codemirror.js,
// rebuilt with `make codemirror`); el() comes from common.js, showDialog
// from actions.js, createReview from review.js.

const EDITOR_POLL_MS = 3000;
const EDITOR_TABS_KEY = "mo.editorTabs.";
const EDITOR_DRAFT_KEY = "mo.editorDraft.";
const DRAFT_SAVE_MS = 400;
const QUICK_OPEN_LIMIT = 50;
const DIFF_SPLIT_MIN_WIDTH = 760; // px of editor pane for a side-by-side diff (two ~50-column sides)

// Editor theme on the MoMA tokens. The colors are CSS variables, so it
// follows the page's light/dark theme (data-theme on <html>) live.
const editorTheme = CM.EditorView.theme({
  "&": { height: "100%", fontSize: "12px", backgroundColor: "var(--paper)" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  ".cm-gutters": { backgroundColor: "var(--paper)", color: "var(--ink-4)", borderRight: "1px solid var(--surface-2)" },
  ".cm-activeLine": { backgroundColor: "var(--surface)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--surface)", color: "var(--ink)" },
  "&.cm-focused": { outline: "none" },
  // CodeMirror's own defaults for these assume a light page.
  ".cm-content": { caretColor: "var(--ink)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--ink)" },
  ".cm-collapsedLines": { background: "var(--surface)", color: "var(--ink-3)" },
  ".cm-tooltip": { backgroundColor: "var(--paper)", color: "var(--ink)", border: "1px solid var(--line)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": { backgroundColor: "var(--cm-selection) !important" },
  ".cm-searchMatch": { backgroundColor: "var(--cm-search)" },
  ".cm-searchMatch-selected": { backgroundColor: "var(--yellow)" },
  ".cm-panels": { backgroundColor: "var(--surface)", color: "var(--ink)", fontFamily: "var(--font-sans)" },
  ".cm-panels-top": { borderBottom: "1px solid var(--line)" },
  ".cm-panels-bottom": { borderTop: "1px solid var(--line)" },
  ".cm-textfield": { border: "1px solid var(--ink-4)", borderRadius: "0", fontSize: "13px" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--paper)", border: "1px solid var(--ink-4)", borderRadius: "0" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--surface-2)", border: "0", color: "var(--ink-2)" },
});

const editorHighlight = CM.syntaxHighlighting(CM.HighlightStyle.define([
  { tag: [CM.tags.keyword, CM.tags.modifier, CM.tags.controlKeyword, CM.tags.operatorKeyword], color: "var(--syn-keyword)" },
  { tag: [CM.tags.string, CM.tags.special(CM.tags.string), CM.tags.regexp], color: "var(--syn-string)" },
  { tag: [CM.tags.number, CM.tags.bool, CM.tags.null, CM.tags.atom], color: "var(--syn-number)" },
  { tag: [CM.tags.comment, CM.tags.lineComment, CM.tags.blockComment], color: "var(--ink-4)", fontStyle: "italic" },
  { tag: [CM.tags.typeName, CM.tags.className, CM.tags.namespace], color: "var(--syn-type)" },
  { tag: [CM.tags.function(CM.tags.variableName), CM.tags.function(CM.tags.propertyName)], color: "var(--syn-function)" },
  { tag: [CM.tags.definition(CM.tags.variableName)], color: "var(--ink)", fontWeight: "bold" },
  { tag: [CM.tags.propertyName, CM.tags.attributeName], color: "var(--syn-property)" },
  { tag: [CM.tags.tagName, CM.tags.heading], color: "var(--syn-tag)", fontWeight: "bold" },
  { tag: [CM.tags.meta, CM.tags.processingInstruction], color: "var(--ink-3)" },
  { tag: CM.tags.link, textDecoration: "underline" },
  { tag: CM.tags.emphasis, fontStyle: "italic" },
  { tag: CM.tags.strong, fontWeight: "bold" },
  { tag: CM.tags.invalid, color: "var(--red)" },
]));

// editorExtensions is everything a file tab's editor needs; onSave runs on
// Mod-s, onChange after every document change.
function editorExtensions(path, { onSave, onChange }) {
  const lang = CM.languageFor(path);
  return [
    CM.lineNumbers(), CM.highlightActiveLineGutter(), CM.highlightSpecialChars(), CM.foldGutter(),
    CM.history(), CM.drawSelection(), CM.indentOnInput(), CM.bracketMatching(),
    CM.highlightActiveLine(), CM.highlightSelectionMatches(),
    CM.search({ top: true }),
    CM.keymap.of([
      { key: "Mod-s", preventDefault: true, run: () => { onSave(); return true; } },
      ...CM.searchKeymap, ...CM.historyKeymap, ...CM.foldKeymap, ...CM.defaultKeymap, CM.indentWithTab,
    ]),
    CM.EditorView.updateListener.of((u) => { if (u.docChanged) onChange(); }),
    editorTheme, editorHighlight,
    ...(lang ? [lang] : []),
  ];
}

// CodeMirror keeps lines joined with "\n", so a CRLF file is edited as LF
// and converted back on save. eolOf reports a file's line ending: "\r\n"
// when every break is CRLF, "\n" when there's no CR at all, and null for
// mixed endings or lone CRs — those open read-only, since saving would
// rewrite every line ending.
function eolOf(text) {
  const cr = (text.match(/\r/g) || []).length;
  if (!cr) return "\n";
  const crlf = (text.match(/\r\n/g) || []).length;
  const lf = (text.match(/\n/g) || []).length;
  return cr === crlf && lf === crlf ? "\r\n" : null;
}
const toLF = (text) => text.replace(/\r\n/g, "\n");

// readOnlyExtensions is the HEAD side of a side-by-side diff: highlighting
// and search, no editing.
function readOnlyExtensions(path) {
  const lang = CM.languageFor(path);
  return [
    CM.lineNumbers(), CM.highlightSpecialChars(), CM.drawSelection(), CM.search({ top: true }),
    CM.keymap.of([...CM.searchKeymap, ...CM.defaultKeymap]),
    editorTheme, editorHighlight,
    ...(lang ? [lang] : []),
  ];
}

// minimalChange is the single replacement turning a into b, trimmed to
// what differs, so reloading a file keeps the scroll position and
// selection of everything around the edit.
function minimalChange(a, b) {
  const max = Math.min(a.length, b.length);
  let start = 0;
  while (start < max && a.charCodeAt(start) === b.charCodeAt(start)) start++;
  let end = 0;
  while (end < max - start && a.charCodeAt(a.length - 1 - end) === b.charCodeAt(b.length - 1 - end)) end++;
  return { from: start, to: a.length - end, insert: b.slice(start, b.length - end) };
}

// quickOpenMatches ranks paths for the quick-open box: every space-separated
// term must appear in the path (case-insensitive); a hit in the file name
// ranks first, then shorter paths.
function quickOpenMatches(paths, query) {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return paths.slice(0, QUICK_OPEN_LIMIT);
  const scored = [];
  for (const p of paths) {
    const lower = p.toLowerCase();
    if (!terms.every((t) => lower.includes(t))) continue;
    const base = lower.slice(lower.lastIndexOf("/") + 1);
    scored.push({ p, score: (base.includes(terms[terms.length - 1]) ? 0 : 1000) + p.length });
  }
  scored.sort((x, y) => x.score - y.score || x.p.localeCompare(y.p));
  return scored.slice(0, QUICK_OPEN_LIMIT).map((s) => s.p);
}

// Storage helpers: localStorage can be unavailable or full; every access is
// best effort and reports whether it worked.
function storageGet(key) {
  try { return JSON.parse(localStorage.getItem(key) || "null"); } catch (_) { return null; }
}
function storageSet(key, value) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
    return true;
  } catch (_) { return false; }
}

// A "diff" tab compares the working copy with HEAD, a "bdiff" tab with the
// branch's merge base (opened from the Overview tab). Both are editable on
// the working-copy side and share all the diff code.
const isDiffKind = (kind) => kind === "diff" || kind === "bdiff";
const READ_ONLY_NOTE = "Read-only: this branch isn't checked out, so this is its last commit.";
const DIFF_REV = { diff: "HEAD", bdiff: "base" };
const DIFF_BASE_LABEL = { diff: "HEAD", bdiff: "the branch base" };

// loadSavedTabs returns a window's remembered tabs as {tabs: [{kind,
// path}], active: key}. Older saves listed plain file paths.
function loadSavedTabs(windowID) {
  const saved = storageGet(EDITOR_TABS_KEY + windowID);
  if (saved && Array.isArray(saved.tabs)) {
    return {
      tabs: saved.tabs.filter((t) => t && typeof t.path === "string" &&
        (t.kind === "file" || isDiffKind(t.kind) || (t.kind === "commit" && /^[0-9a-f]{40}([0-9a-f]{24})?$/.test(t.hash)))),
      active: saved.active,
    };
  }
  if (saved && Array.isArray(saved.paths)) return { tabs: saved.paths.map((path) => ({ kind: "file", path })), active: saved.active && "file:" + saved.active };
  return { tabs: [], active: null };
}

function clockTime() {
  return new Date().toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

// createEditorTabs builds the tab strip. Without a chatPanel (the reviewer
// view) there's no Chat tab and the Overview is the home tab.
function createEditorTabs({ strip, chatPanel, editorPanel, overview }) {
  let windowID = null; // the target's storage key (a window id, or "branch:…")
  let api = ""; // the target's API prefix, e.g. /api/sessions/@3
  let available = false;
  // Each tab: {kind ("file"|"diff"|"bdiff"|"commit"), path, key, wrap, host, banner, info,
  // status, saveBtn, view, merge, layout, base, etag, conflict, dirty,
  // loading, saving, draftTimer, draftFailed} plus, for diff tabs,
  // {original, origEtag, deleted}, and for commit tabs {hash, before,
  // after, loaded}: both sides read-only, never saved. view is always the editable working
  // copy (a MergeView's b side in a split diff). base is the disk version
  // the edit started from ({text, hash}); conflict is a newer disk version
  // that arrived while the tab had unsaved edits.
  let tabs = [];
  let active = null; // a tab, or null for the chat (or overview) tab
  let overviewShown = false; // the pinned Overview tab is showing
  let gen = 0; // bumped on window switch; stale responses are dropped

  const stripTabs = el("div", { class: "editor-tabs__list", role: "tablist" });
  const reviewBtn = el("button", { class: "editor-tabs__review", type: "button", title: "Your comments for Claude", hidden: "" });
  const openBtn = el("button", { class: "editor-tabs__open", type: "button", title: "Open a file (Ctrl+P)", text: "Open file…" });
  strip.replaceChildren(stripTabs, reviewBtn, openBtn);

  const review = createReview({
    onChange(paths) {
      for (const t of tabs) if (t.view && !t.deleted && t.kind !== "commit" && paths.has(t.path)) review.apply(t.view, t.path);
      renderReviewBtn();
    },
    onSent: () => activate(null), // show the chat, where Claude answers
  });

  function renderReviewBtn() {
    const n = review.count();
    reviewBtn.hidden = !n;
    reviewBtn.textContent = `Review (${n})`;
  }

  const fileURL = () => `${api}/file`;
  const draftKey = (tab) => EDITOR_DRAFT_KEY + windowID + "\u0000" + tab.key;

  function save() {
    if (windowID) storageSet(EDITOR_TABS_KEY + windowID, { tabs: tabs.map((t) => ({ kind: t.kind, path: t.path, ...(t.hash ? { hash: t.hash } : {}) })), active: overviewShown ? "overview" : active ? active.key : null });
  }

  function renderStrip() {
    const items = [
      ...(chatPanel ? [{ tab: null, label: "Chat", title: "Conversation" }] : []),
      ...(overview ? [{ tab: null, pinned: "overview", label: "Overview", title: "The shape of this branch's change" }] : []),
      ...tabs.map((t) => ({
        tab: t,
        label: (isDiffKind(t.kind) ? "± " : t.kind === "commit" ? t.hash.slice(0, 7) + " " : "") + t.path.split("/").pop(),
        title: isDiffKind(t.kind) ? `Changes in ${t.path} against ${DIFF_BASE_LABEL[t.kind]}` : t.kind === "commit" ? `${t.path} in commit ${t.hash.slice(0, 7)}` : t.path,
      })),
    ];
    stripTabs.replaceChildren(...items.map(({ tab, pinned, label, title }) => {
      const isActive = pinned ? overviewShown : tab === active && !overviewShown;
      const btn = el("button", { class: "editor-tab" + (isActive ? " is-active" : "") + (tab ? "" : " is-chat") + (tab?.dirty ? " is-dirty" : ""), type: "button", role: "tab", "aria-selected": String(isActive), title: tab?.dirty ? `${title} (unsaved)` : title }, [
        el("span", { class: "editor-tab__label", text: label }),
        ...(pinned ? [overview.badge] : []),
      ]);
      btn.addEventListener("click", () => pinned ? showOverview() : activate(tab));
      if (!tab) return btn;
      const close = el("span", { class: "editor-tab__close", role: "button", title: `Close ${tab.path}`, "aria-label": `Close ${tab.path}` }, [
        el("span", { class: "editor-tab__x", text: "×" }),
        el("span", { class: "editor-tab__dot", text: "●" }),
      ]);
      close.addEventListener("click", (e) => { e.stopPropagation(); closeTab(tab); });
      btn.addEventListener("auxclick", (e) => { if (e.button === 1) closeTab(tab); }); // middle-click closes
      btn.appendChild(close);
      return btn;
    }));
    stripTabs.querySelector(".is-active")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }

  function activate(tab) {
    if (!tab && !chatPanel) { showOverview(); return; }
    active = tab;
    if (overviewShown) { overviewShown = false; overview.setVisible(false); }
    if (chatPanel) chatPanel.hidden = !!tab;
    editorPanel.hidden = !tab;
    for (const t of tabs) t.wrap.hidden = t !== tab;
    renderStrip();
    save();
    if (tab) {
      if (tab.kind !== "file" && tab.view && tab.layout !== diffLayout(tab)) rebuildView(tab);
      load(tab);
    }
  }

  function showOverview() {
    active = null;
    overviewShown = true;
    if (chatPanel) chatPanel.hidden = true;
    editorPanel.hidden = true;
    for (const t of tabs) t.wrap.hidden = true;
    overview.setVisible(true);
    renderStrip();
    save();
  }

  function barButton(text, onClick, cls) {
    const b = el("button", { class: "editor-pane__btn" + (cls ? " " + cls : ""), type: "button", text });
    b.addEventListener("click", onClick);
    return b;
  }

  const tabKey = (kind, path, hash) => kind === "commit" ? `commit:${hash}:${path}` : kind + ":" + path;

  function newTab(kind, path, hash) {
    const tab = {
      kind, path, hash, key: tabKey(kind, path, hash), before: null, after: null, loaded: false,
      view: null, merge: null, layout: null, base: null, etag: null, conflict: null,
      dirty: false, loading: false, saving: false, draftTimer: 0, draftFailed: false,
      original: null, origEtag: null, deleted: false, pendingLine: 0,
      eol: "\n", mixedEol: false, saveSeq: 0,
    };
    tab.status = el("span", { class: "editor-pane__status" });
    tab.saveBtn = barButton("Save", () => saveTab(tab), "is-primary");
    tab.saveBtn.disabled = true;
    const withView = (fn) => () => tab.view && fn(tab.view);
    const buttons = kind !== "file" ? [
      barButton("Previous change", withView((v) => { CM.goToPreviousChunk(v); v.focus(); })),
      barButton("Next change", withView((v) => { CM.goToNextChunk(v); v.focus(); })),
      barButton("Open file", () => open(path, "file")),
    ] : [
      barButton("Find", withView((v) => CM.openSearchPanel(v))),
      barButton("Go to line", withView((v) => CM.gotoLine(v))),
      barButton("Changes", () => open(path, "diff")),
    ];
    tab.banner = el("div", { class: "editor-pane__banner", hidden: "" });
    tab.info = el("div", { class: "editor-pane__info", hidden: "" });
    tab.host = el("div", { class: "editor-pane__host" + (kind !== "file" ? " is-diff" : "") });
    tab.wrap = el("div", { class: "editor-pane" }, [
      el("div", { class: "editor-pane__bar" }, [
        // rtl so a long path loses its start, not the file name; bdi keeps
        // the text itself (e.g. a leading ".") in order.
        el("span", { class: "editor-pane__path" }, [el("bdi", { text: path })]),
        ...(isDiffKind(kind) ? [el("span", { class: "editor-pane__kind", text: kind === "bdiff" ? "vs branch base" : "vs HEAD" })] : []),
        ...(kind === "commit" ? [el("span", { class: "editor-pane__kind", title: hash, text: `commit ${hash.slice(0, 7)}` })] : []),
        tab.status,
        el("span", { class: "editor-pane__actions" }, [...buttons, ...(kind === "commit" ? [] : [tab.saveBtn])]),
      ]),
      tab.banner,
      tab.info,
      tab.host,
    ]);
    tab.wrap.hidden = true;
    editorPanel.appendChild(tab.wrap);
    return tab;
  }

  // open shows (creating if needed) the tab for path: kind "file" is the
  // file itself, "diff" its changes against HEAD, "bdiff" against the
  // branch's merge base, "commit" its changes in commit hash.
  function open(path, kind = "file", hash) {
    if (!windowID || !path || (kind === "commit" && !hash)) return;
    const key = tabKey(kind, path, hash);
    let tab = tabs.find((t) => t.key === key);
    if (!tab) {
      tab = newTab(kind, path, hash);
      // New tabs open right after the active one, like most editors.
      const at = active ? tabs.indexOf(active) + 1 : tabs.length;
      tabs.splice(at, 0, tab);
    }
    activate(tab);
  }

  async function closeTab(tab) {
    if (tab.dirty) {
      const ok = await showDialog({
        title: "Discard unsaved changes?",
        text: `${tab.path} has edits that haven't been saved.`,
        actions: [{ label: "Cancel" }, { label: "Discard changes", value: true, danger: true }],
      });
      if (!ok || !tabs.includes(tab)) return;
    }
    clearTimeout(tab.draftTimer);
    storageSet(draftKey(tab), null);
    const i = tabs.indexOf(tab);
    tabs.splice(i, 1);
    destroyView(tab);
    tab.wrap.remove();
    if (active === tab) activate(tabs[i] || tabs[i - 1] || null);
    else { renderStrip(); save(); }
  }

  function destroyView(tab) {
    if (tab.merge) tab.merge.destroy();
    else tab.view?.destroy();
    tab.merge = null;
    tab.view = null;
  }

  function setInfo(tab, text) {
    tab.info.hidden = !text;
    tab.info.textContent = text || "";
  }

  function showNote(tab, text) {
    destroyView(tab);
    tab.base = null;
    tab.etag = null;
    tab.origEtag = null;
    tab.original = null;
    setConflict(tab, null);
    setInfo(tab, "");
    tab.host.replaceChildren(el("div", { class: "editor-pane__note", text }));
    updateState(tab);
  }

  // ── Dirty state, drafts, status line ──────────────────────────

  function docText(tab) {
    return tab.view ? tab.view.state.doc.toString() : "";
  }

  // updateState recomputes the tab's dirty flag after a change and keeps
  // the strip, Save button and draft in step.
  function updateState(tab, statusText) {
    const dirty = !!(tab.view && tab.base && !tab.deleted && docText(tab) !== tab.base.text);
    if (dirty !== tab.dirty) {
      tab.dirty = dirty;
      renderStrip();
    }
    tab.saveBtn.disabled = !tab.view || tab.deleted || tab.readOnly || tab.saving || (!dirty && !tab.conflict);
    if (tab.view && !tab.deleted && tab.kind !== "commit") review.sync(tab.view, tab.path);
    if (isDiffKind(tab.kind) && tab.view && !tab.deleted && !tab.mixedEol) {
      const c = CM.getChunks(tab.view.state);
      setInfo(tab, c && !c.chunks.length ? `No changes against ${DIFF_BASE_LABEL[tab.kind]}.` : tab.readOnly ? READ_ONLY_NOTE : "");
    }
    if (statusText !== undefined) tab.status.textContent = statusText;
    else if (tab.view && !tab.saving) tab.status.textContent = dirty ? "Modified" : tab.status.textContent === "Modified" ? "" : tab.status.textContent;
    scheduleDraft(tab);
  }

  function scheduleDraft(tab) {
    clearTimeout(tab.draftTimer);
    tab.draftTimer = setTimeout(() => writeDraft(tab), DRAFT_SAVE_MS);
  }

  // writeDraft stores (or clears) the tab's unsaved edits, keyed by the
  // base hash they apply to.
  function writeDraft(tab) {
    clearTimeout(tab.draftTimer);
    tab.draftTimer = 0;
    // Without an editor (not loaded yet, or showing a note) there's nothing
    // to compare against: leave any stored draft for when it loads.
    if (!windowID || !tabs.includes(tab) || !tab.view) return;
    const draft = tab.dirty && tab.base ? { text: docText(tab), baseHash: tab.base.hash } : null;
    tab.draftFailed = !storageSet(draftKey(tab), draft) && !!draft;
  }

  function flushDrafts() {
    for (const t of tabs) if (t.draftTimer) writeDraft(t);
  }

  // ── Conflicts ─────────────────────────────────────────────────

  // setConflict shows (or clears) the banner for a disk version that
  // arrived while the tab had unsaved edits. current is a /file body.
  function setConflict(tab, current) {
    tab.conflict = current;
    if (!current) {
      tab.banner.hidden = true;
      tab.banner.replaceChildren();
      return;
    }
    // A binary or too-large version has no text to reload into the editor.
    const usable = current.exists && !current.binary && !current.tooLarge;
    const reload = barButton(usable ? "Reload from disk" : "Close tab", () => {
      if (!usable) { tab.dirty = false; closeTab(tab); return; }
      applyDisk(tab, current, true);
    });
    const overwrite = barButton("Keep mine and save", () => saveTab(tab, current.hash));
    tab.banner.replaceChildren(
      el("span", { class: "editor-pane__banner-text", text: current.exists
        ? usable ? "This file changed on disk while you were editing it (Claude may have edited it)." : "This file was replaced on disk by a binary or very large file."
        : "This file was deleted on disk while you were editing it." }),
      reload,
      ...(usable ? [overwrite] : []),
    );
    tab.banner.hidden = false;
  }

  // applyDisk makes a disk version the tab's base and content, discarding
  // any unsaved edits when force is set.
  function applyDisk(tab, data, force) {
    const text = toLF(data.text);
    tab.eol = eolOf(data.text) || tab.eol;
    tab.base = { text, hash: data.hash };
    tab.etag = `"${data.hash}"`;
    setConflict(tab, null);
    const old = docText(tab);
    if (old !== text) tab.view.dispatch({ changes: minimalChange(old, text) });
    updateState(tab, force ? "Reloaded " + clockTime() : old !== text ? "Updated " + clockTime() : undefined);
  }

  // ── Diff views ────────────────────────────────────────────────

  // diffLayout picks side by side when the pane is wide enough for two
  // columns of code, inline (unified) otherwise — e.g. on a phone.
  function diffLayout(tab) {
    return (tab.host.clientWidth || editorPanel.clientWidth) >= DIFF_SPLIT_MIN_WIDTH ? "split" : "unified";
  }

  // revertButton renders the per-change revert control: it puts HEAD's
  // version of that change back into the working copy (then Save writes it).
  function revertButton(label) {
    return el("button", { class: "cm-mo-revert", type: "button", title: "Revert this change to HEAD (then save)", text: label });
  }

  // buildDiff creates the tab's merge view: the working copy (doc) against
  // HEAD (tab.original).
  function buildDiff(tab, doc, exts) {
    const original = tab.original || "";
    // A tab opened at a line shows everything: the line could be in a
    // folded stretch of unchanged code.
    const collapse = tab.expanded ? undefined : { margin: 3, minSize: 6 };
    tab.layout = diffLayout(tab);
    if (tab.layout === "split") {
      tab.merge = new CM.MergeView({
        parent: tab.host,
        a: { doc: original, extensions: [...readOnlyExtensions(tab.path), CM.EditorState.readOnly.of(true)] },
        b: { doc, extensions: exts },
        ...(tab.deleted || tab.readOnly ? {} : { revertControls: "a-to-b", renderRevertControl: () => revertButton("⟵") }),
        collapseUnchanged: collapse,
      });
      tab.view = tab.merge.b;
    } else {
      tab.view = new CM.EditorView({
        parent: tab.host,
        state: CM.EditorState.create({
          doc,
          extensions: [...exts, CM.unifiedMergeView({
            original,
            // Only "Revert": accepting would mean changing HEAD.
            mergeControls: tab.deleted || tab.readOnly ? false : (type, action) => {
              if (type !== "reject") return el("span", { hidden: "" });
              const b = revertButton("Revert");
              b.addEventListener("mousedown", action);
              return b;
            },
            collapseUnchanged: collapse,
          })],
        }),
      });
    }
  }

  // setOriginal swaps in a new HEAD version under an open diff, e.g. after
  // Claude commits.
  function setOriginal(tab, text) {
    const old = tab.original || "";
    tab.original = text;
    if (!tab.view || old === text) return;
    const change = minimalChange(old, text);
    if (tab.merge) tab.merge.a.dispatch({ changes: change });
    else tab.view.dispatch({ effects: CM.originalDocChangeEffect(tab.view.state, CM.ChangeSet.of(change, old.length)) });
  }

  // rebuildView recreates a diff tab's view (layout change), keeping its
  // text, unsaved edits included.
  function rebuildView(tab) {
    if (tab.kind === "commit") { buildCommitView(tab); return; }
    const doc = docText(tab);
    destroyView(tab);
    tab.host.replaceChildren();
    buildDiff(tab, doc, tabExtensions(tab));
    viewReady(tab);
  }

  function tabExtensions(tab) {
    const exts = editorExtensions(tab.path, { onSave: () => saveTab(tab), onChange: () => updateState(tab) });
    if (tab.deleted) return [...exts, CM.EditorState.readOnly.of(true)];
    return [...exts, ...review.extension(tab.path), ...(tab.mixedEol || tab.readOnly ? [CM.EditorState.readOnly.of(true)] : [])];
  }

  // viewReady runs once a tab has a (new) editor: show its comments and
  // honour a pending reveal.
  function viewReady(tab) {
    if (!tab.view) return;
    if (!tab.deleted && tab.kind !== "commit") review.apply(tab.view, tab.path);
    if (tab.pendingLine) {
      const line = tab.view.state.doc.line(Math.min(Math.max(1, tab.pendingLine), tab.view.state.doc.lines));
      tab.pendingLine = 0;
      tab.view.dispatch({ selection: { anchor: line.from }, effects: CM.EditorView.scrollIntoView(line.from, { y: "center" }) });
    }
  }

  // buildCommitView (re)creates a commit tab's view: the file in the
  // commit's first parent against the commit, both read-only.
  function buildCommitView(tab) {
    destroyView(tab);
    tab.host.replaceChildren();
    const exts = [...readOnlyExtensions(tab.path), CM.EditorState.readOnly.of(true)];
    const collapse = { margin: 3, minSize: 6 };
    tab.layout = diffLayout(tab);
    if (tab.layout === "split") {
      tab.merge = new CM.MergeView({ parent: tab.host, a: { doc: tab.before, extensions: exts }, b: { doc: tab.after, extensions: exts }, collapseUnchanged: collapse });
      tab.view = tab.merge.b;
    } else {
      tab.view = new CM.EditorView({
        parent: tab.host,
        state: CM.EditorState.create({ doc: tab.after, extensions: [...exts, CM.unifiedMergeView({ original: tab.before, mergeControls: false, collapseUnchanged: collapse })] }),
      });
    }
  }

  // loadCommit reads a commit tab's two versions once: a commit never
  // changes, so there's nothing to poll.
  async function loadCommit(tab) {
    if (tab.loaded || tab.loading) return;
    const g = gen;
    tab.loading = true;
    try {
      const res = await fetch(`${api}/commits/${tab.hash}/file?path=${encodeURIComponent(tab.path)}`);
      const data = await res.json().catch(() => ({}));
      if (g !== gen || !tabs.includes(tab)) return;
      if (!res.ok) {
        tab.loaded = res.status === 404; // a transient failure retries on the next poll
        showNote(tab, res.status === 404 ? "This commit (or this file in it) isn't in the session's repository." : `Couldn't read the commit: ${data.error || res.status}`);
        return;
      }
      tab.loaded = true;
      const sides = [data.before, data.after].filter((v) => v.exists);
      if (sides.some((v) => v.binary)) { showNote(tab, "Binary file — no text diff."); return; }
      if (sides.some((v) => v.tooLarge)) { showNote(tab, "Too large to diff here."); return; }
      tab.before = data.before.exists ? toLF(data.before.text) : "";
      tab.after = data.after.exists ? toLF(data.after.text) : "";
      buildCommitView(tab);
      setInfo(tab, data.status === "A" ? "Added in this commit." : data.status === "D" ? "Deleted in this commit." : data.oldPath ? `Renamed from ${data.oldPath}.` : "");
    } catch (err) {
      if (g === gen && !tab.view) showNote(tab, `Couldn't read the commit: ${err.message}`);
    } finally {
      tab.loading = false;
    }
  }

  // ── Loading and saving ────────────────────────────────────────

  // fetchVersion GETs one version of the tab's file, sending the ETag we
  // last saw so an unchanged file costs a body-less 304 (returned as null).
  async function fetchVersion(tab, rev, etag) {
    const res = await fetch(`${fileURL()}?path=${encodeURIComponent(tab.path)}${rev ? "&rev=" + rev : ""}`, {
      headers: etag ? { "If-None-Match": etag } : {}, cache: "no-store",
    });
    if (res.status === 304) return null;
    const data = await res.json().catch(() => ({}));
    return { ok: res.ok, status: res.status, etag: res.headers.get("ETag"), data };
  }

  // loadOriginal refreshes a diff tab's HEAD (or branch base) version.
  // Returns false (after showing a note) when it can't be diffed.
  async function loadOriginal(tab) {
    const r = await fetchVersion(tab, DIFF_REV[tab.kind], tab.origEtag);
    if (!r) return true;
    let problem = null;
    if (!r.ok) problem = r.status === 404 ? "This file isn't part of the checkout (or is ignored by git)." : `Couldn't read ${DIFF_BASE_LABEL[tab.kind]}: ${r.data.error || r.status}`;
    else if (r.data.binary || r.data.tooLarge) problem = r.data.binary ? "Binary file — no text diff." : "Too large to diff here.";
    if (problem) {
      // An open editor keeps its (possibly unsaved) text; only say why the
      // diff can't update.
      if (tab.view) { setInfo(tab, problem); return true; }
      showNote(tab, problem);
      return false;
    }
    tab.origEtag = r.etag;
    setOriginal(tab, r.data.exists ? toLF(r.data.text) : ""); // a new file diffs against nothing
    return true;
  }

  // load fetches the tab's file (and, for a diff tab, HEAD's version).
  async function load(tab) {
    if (tab.kind === "commit" && available) return loadCommit(tab);
    if (!available || tab.loading || tab.saving) {
      if (!available && !tab.view) showNote(tab, "No live session.");
      return;
    }
    const g = gen;
    const seq = tab.saveSeq; // a save since this poll started makes its answer stale
    tab.loading = true;
    try {
      if (isDiffKind(tab.kind) && !(await loadOriginal(tab))) return;
      if (g !== gen || !tabs.includes(tab)) return;
      const r = await fetchVersion(tab, "", tab.etag);
      if (g !== gen || !tabs.includes(tab) || tab.saving || tab.saveSeq !== seq || !r) return;
      const { data } = r;
      // A branch that isn't checked out: its files come from a commit.
      if (!!data.readOnly !== !!tab.readOnly) {
        tab.readOnly = !!data.readOnly;
        if (tab.view) { destroyView(tab); tab.host.replaceChildren(); }
      }
      if (!r.ok) {
        if (tab.dirty) return; // keep the edits; the save will report the problem
        showNote(tab, r.status === 404 ? "This file isn't part of the checkout (or is ignored by git)." : `Couldn't open the file: ${data.error || r.status}`);
        return;
      }
      if (isDiffKind(tab.kind) && !data.exists && !tab.dirty) {
        // Deleted in the working tree: show what HEAD (or the base) had, read-only.
        if (!tab.deleted || !tab.view) {
          tab.deleted = true;
          destroyView(tab);
          tab.host.replaceChildren();
          tab.base = { text: "", hash: null };
          tab.etag = r.etag;
          buildDiff(tab, "", tabExtensions(tab));
          viewReady(tab);
          setInfo(tab, `Deleted in the working tree — showing what ${DIFF_BASE_LABEL[tab.kind]} had.`);
          updateState(tab, "");
        }
        return;
      }
      if (!data.exists || data.binary || data.tooLarge) {
        if (tab.dirty) { tab.etag = r.etag; setConflict(tab, data); return; }
        showNote(tab, !data.exists ? "This file was deleted." : data.binary ? "Binary file — not shown." : `Too large to open here (${Math.round(data.size / 1024)} KB).`);
        return;
      }

      if (!tab.view || tab.deleted) {
        if (tab.deleted) { tab.deleted = false; destroyView(tab); setInfo(tab, ""); }
        createView(tab, data);
      } else if (!tab.dirty) {
        applyDisk(tab, data, false);
      } else if (data.hash !== tab.base.hash) {
        // Changed underneath unsaved edits: keep them, flag the conflict.
        tab.etag = `"${data.hash}"`;
        setConflict(tab, data);
        updateState(tab);
      }
    } catch (err) {
      if (g === gen && !tab.view) showNote(tab, `Couldn't open the file: ${err.message}`);
    } finally {
      tab.loading = false;
    }
  }

  // createView builds the tab's editor from a disk version, restoring a
  // saved draft on top of it when there is one.
  function createView(tab, data) {
    const draft = storageGet(draftKey(tab));
    const text = toLF(data.text);
    const eol = eolOf(data.text);
    tab.eol = eol || "\n";
    tab.mixedEol = !eol;
    setInfo(tab, tab.mixedEol ? "Mixed line endings — read-only here, so saving can't rewrite them."
      : tab.readOnly ? READ_ONLY_NOTE : "");
    const doc = draft && typeof draft.text === "string" && !tab.mixedEol ? draft.text : text;
    tab.base = { text, hash: data.hash };
    tab.etag = `"${data.hash}"`;
    tab.host.replaceChildren();
    if (isDiffKind(tab.kind)) {
      buildDiff(tab, doc, tabExtensions(tab));
    } else {
      tab.view = new CM.EditorView({ parent: tab.host, state: CM.EditorState.create({ doc, extensions: tabExtensions(tab) }) });
    }
    if (draft && draft.baseHash !== data.hash) {
      // The draft was made against an older version of the file.
      tab.base = { text: null, hash: draft.baseHash };
      setConflict(tab, data);
    }
    updateState(tab, draft ? "Restored unsaved changes" : "");
    viewReady(tab);
  }

  // saveTab writes the tab's text. baseHash defaults to the version the
  // edit started from; "Keep mine and save" passes the conflicting disk
  // version's hash to overwrite it deliberately.
  async function saveTab(tab, baseHash) {
    if (!tab.view || tab.saving || tab.deleted || tab.kind === "commit" || !windowID) return;
    if (!tab.dirty && !tab.conflict) return;
    if (tab.mixedEol || tab.readOnly) return;
    const g = gen;
    const key = draftKey(tab); // the window may change while the save is out
    const text = docText(tab);
    tab.saving = true;
    tab.saveSeq++;
    updateState(tab, "Saving…");
    try {
      const res = await fetch(fileURL(), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: tab.path, text: tab.eol === "\r\n" ? text.replace(/\n/g, "\r\n") : text, baseHash: baseHash || tab.base.hash }),
      });
      const data = await res.json().catch(() => ({}));
      if (g !== gen || !tabs.includes(tab)) {
        if (res.ok) storageSet(key, null); // saved: its draft is done with
        return;
      }
      tab.saving = false;
      tab.saveSeq++;
      if (res.status === 409) {
        tab.etag = data.current?.exists ? `"${data.current.hash}"` : null;
        setConflict(tab, data.current || { exists: false });
        updateState(tab, "Not saved");
        return;
      }
      if (!res.ok) {
        updateState(tab, `Save failed: ${data.error || res.status}`);
        return;
      }
      tab.base = { text, hash: data.hash };
      tab.etag = `"${data.hash}"`;
      setConflict(tab, null);
      updateState(tab, "Saved " + clockTime());
      writeDraft(tab);
    } catch (err) {
      tab.saving = false;
      tab.saveSeq++;
      updateState(tab, `Save failed: ${err.message}`);
    }
  }

  // reveal opens path's tab of kind ("file", "diff" or "bdiff") scrolled to
  // line (from the review list, or the Overview's functions and sites).
  function reveal(path, line, kind = "file") {
    if (!line || kind === "commit") { open(path, kind); return; }
    const key = tabKey(kind, path);
    const existing = tabs.find((t) => t.key === key);
    if (existing) existing.pendingLine = line;
    open(path, kind);
    const tab = tabs.find((t) => t.key === key);
    if (!tab) return;
    if (!existing) tab.pendingLine = line;
    if (kind !== "file" && !tab.expanded) {
      tab.expanded = true;
      if (tab.view) { rebuildView(tab); return; } // viewReady applies the line
    }
    if (tab.view) viewReady(tab);
  }

  // ── Review list ───────────────────────────────────────────────
  const rvList = el("div", { class: "review-dialog__list" });
  const rvError = el("div", { class: "review-dialog__error" });
  const rvSend = el("button", { class: "btn btn--primary", type: "button", text: "Send to Claude" });
  // Copy is for pasting the review elsewhere, e.g. a GitHub PR review.
  const rvCopy = el("button", { class: "btn", type: "button", text: "Copy" });
  const rvDiscard = el("button", { class: "btn btn--danger", type: "button", text: "Discard all" });
  const rvClose = el("button", { class: "btn", type: "button", text: "Close" });
  const rvDialog = el("dialog", { class: "dialog review-dialog", "aria-label": "Review" }, [
    el("div", { class: "dialog__title", text: "Review for Claude" }),
    el("div", { class: "dialog__text", text: "These comments are sent as one message. Claude sees each file and line, the line's text, and your comment." }),
    rvList, rvError,
    el("div", { class: "dialog__actions" }, [rvDiscard, el("span", { class: "review-dialog__spacer" }), rvClose, rvCopy, rvSend]),
  ]);
  document.body.appendChild(rvDialog);

  function renderReviewList() {
    const items = review.list();
    if (!items.length) { rvDialog.close(); return; }
    rvList.replaceChildren(...items.map((c) => {
      const go = el("button", { class: "review-dialog__loc", type: "button", text: `${c.path}:${c.line}` });
      go.addEventListener("click", () => { rvDialog.close(); reveal(c.path, c.line); });
      const del = el("button", { class: "review-dialog__del", type: "button", title: "Delete comment", text: "Delete" });
      del.addEventListener("click", () => { review.remove(c.id); renderReviewList(); });
      return el("div", { class: "review-dialog__item" }, [
        el("div", { class: "review-dialog__head" }, [go, del]),
        ...(c.text.trim() ? [el("pre", { class: "review-dialog__quote", text: c.text.trim() })] : []),
        el("div", { class: "review-dialog__body", text: c.body }),
      ]);
    }));
  }

  reviewBtn.addEventListener("click", () => {
    rvError.textContent = "";
    rvSend.hidden = !review.canSend();
    rvSend.disabled = !available;
    renderReviewList();
    rvDialog.showModal();
  });
  rvClose.addEventListener("click", () => rvDialog.close());
  rvCopy.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(review.preview());
      rvError.textContent = "Copied.";
    } catch (e) {
      rvError.textContent = `Couldn't copy: ${e.message}`;
    }
  });
  rvDiscard.addEventListener("click", async () => {
    const ok = await showDialog({ title: "Discard all comments?", text: "This removes every comment in this review.", actions: [{ label: "Cancel" }, { label: "Discard all", value: true, danger: true }] });
    if (ok) { review.clear(); rvDialog.close(); }
  });
  rvSend.addEventListener("click", async () => {
    rvSend.disabled = true;
    rvError.textContent = "";
    try {
      const err = await review.send();
      if (err) rvError.textContent = err;
      else rvDialog.close();
    } catch (e) {
      rvError.textContent = e.message;
    } finally {
      rvSend.disabled = !available;
    }
  });

  // ── Quick open ────────────────────────────────────────────────
  const qoInput = el("input", { class: "quick-open__input", type: "text", placeholder: "Search files by path", autocomplete: "off", spellcheck: "false" });
  const qoList = el("div", { class: "quick-open__list", role: "listbox" });
  const qoDialog = el("dialog", { class: "quick-open", "aria-label": "Open file" }, [qoInput, qoList]);
  document.body.appendChild(qoDialog);
  let qoPaths = [];
  let qoMatches = [];
  let qoSel = 0;

  function renderQuickOpen() {
    qoMatches = quickOpenMatches(qoPaths, qoInput.value);
    qoSel = Math.min(qoSel, Math.max(0, qoMatches.length - 1));
    qoList.replaceChildren(...(qoMatches.length ? qoMatches.map((p, i) => {
      const slash = p.lastIndexOf("/");
      const row = el("button", { class: "quick-open__row" + (i === qoSel ? " is-selected" : ""), type: "button", role: "option", tabindex: "-1" }, [
        el("span", { class: "quick-open__name", text: p.slice(slash + 1) }),
        el("span", { class: "quick-open__dir", text: slash >= 0 ? p.slice(0, slash) : "" }),
      ]);
      row.addEventListener("click", () => pickQuickOpen(p));
      return row;
    }) : [el("div", { class: "quick-open__empty", text: qoPaths.length ? "No matching files." : "Loading…" })]));
    qoList.querySelector(".is-selected")?.scrollIntoView({ block: "nearest" });
  }

  function pickQuickOpen(path) {
    qoDialog.close();
    open(path);
  }

  async function showQuickOpen() {
    if (!windowID || !available) return;
    qoInput.value = "";
    qoSel = 0;
    qoPaths = [];
    renderQuickOpen();
    qoDialog.showModal();
    qoInput.focus();
    try {
      const res = await fetch(`${api}/tree`);
      const data = await res.json();
      qoPaths = data.paths || [];
    } catch (_) {
      qoPaths = [];
    }
    if (qoDialog.open) renderQuickOpen();
  }

  qoInput.addEventListener("input", () => { qoSel = 0; renderQuickOpen(); });
  qoInput.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (qoMatches.length) qoSel = (qoSel + (e.key === "ArrowDown" ? 1 : qoMatches.length - 1)) % qoMatches.length;
      renderQuickOpen();
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (qoMatches[qoSel]) pickQuickOpen(qoMatches[qoSel]);
    }
  });
  qoDialog.addEventListener("click", (e) => { if (e.target === qoDialog) qoDialog.close(); }); // backdrop
  openBtn.addEventListener("click", showQuickOpen);
  document.addEventListener("keydown", (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey) return;
    const key = e.key.toLowerCase();
    if (key === "p") {
      e.preventDefault(); // instead of the browser's print dialog
      showQuickOpen();
    } else if (key === "s" && active) {
      // Focus outside the editor (e.g. on the tab strip): still save the
      // visible file rather than the browser's "save page".
      e.preventDefault();
      saveTab(active);
    }
  });

  // Drafts are written on a short debounce; flush them when the page goes
  // away, and warn only if a draft couldn't be stored.
  window.addEventListener("pagehide", flushDrafts);
  window.addEventListener("beforeunload", (e) => {
    flushDrafts();
    if (tabs.some((t) => t.dirty && t.draftFailed)) e.preventDefault();
  });

  // A visible diff switches between side by side and inline as the window
  // crosses the width threshold.
  let resizeTimer = 0;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if (active && active.kind !== "file" && active.view && active.layout !== diffLayout(active)) rebuildView(active);
    }, 150);
  });

  // Only the visible tab polls; others reload when activated.
  function poll() {
    if (active && available && document.visibilityState === "visible") load(active);
  }
  document.addEventListener("visibilitychange", poll);
  setInterval(poll, EDITOR_POLL_MS);

  return {
    open,
    reveal,
    showChat: () => activate(null),
    showOverview,
    // setWindow restores the window's remembered tabs; files load lazily
    // when their tab is shown, and unsaved edits come back from drafts.
    // setWindow shows a chat window's tabs (files from that session).
    setWindow(id) {
      this.setTarget(id ? { key: id, api: `/api/sessions/${encodeURIComponent(id)}` } : { key: null });
    },
    // setTarget switches to another target: key names its stored tabs,
    // drafts and comments, api is its endpoint prefix, and sendTo is the
    // session window review comments are pasted into (default: key).
    setTarget({ key: id, api: apiBase = "", sendTo = id }) {
      if (id === windowID) { review.setSendTo(sendTo); return; }
      flushDrafts();
      gen++;
      for (const t of tabs) { clearTimeout(t.draftTimer); destroyView(t); t.wrap.remove(); }
      tabs = [];
      active = null;
      windowID = id;
      api = apiBase;
      available = false;
      strip.hidden = !id;
      if (qoDialog.open) qoDialog.close();
      if (rvDialog.open) rvDialog.close();
      review.setWindow(id, sendTo);
      renderReviewBtn();
      const saved = id ? loadSavedTabs(id) : { tabs: [], active: null };
      for (const t of saved.tabs) tabs.push(newTab(t.kind, t.path, t.hash));
      activate(tabs.find((t) => t.key === saved.active) || null);
      if (overview && saved.active === "overview") showOverview();
    },
    // setAvailable is driven by the chat view's state poll: files are only
    // read while the window has a live session.
    setAvailable(ok) {
      if (ok === available) return;
      available = ok;
      openBtn.disabled = !ok;
      if (ok && active) load(active);
      if (!ok && active && !active.view) showNote(active, "No live session.");
    },
  };
}
