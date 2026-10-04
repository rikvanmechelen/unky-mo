// Editor tabs for the chat view: the middle column becomes tabbed, with the
// chat (transcript + composer + terminal drawer) as a pinned first tab and
// one CodeMirror tab per opened file. Tabs are remembered per window in
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
// from actions.js.

const EDITOR_POLL_MS = 3000;
const EDITOR_TABS_KEY = "mo.editorTabs.";
const EDITOR_DRAFT_KEY = "mo.editorDraft.";
const DRAFT_SAVE_MS = 400;
const QUICK_OPEN_LIMIT = 50;

// Light theme on the MoMA tokens, to sit inside the white chat column.
const editorTheme = CM.EditorView.theme({
  "&": { height: "100%", fontSize: "12px", backgroundColor: "var(--white)" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  ".cm-gutters": { backgroundColor: "var(--white)", color: "var(--gray-767)", borderRight: "1px solid var(--gray-eee)" },
  ".cm-activeLine": { backgroundColor: "var(--gray-f6)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--gray-f6)", color: "var(--black)" },
  "&.cm-focused": { outline: "none" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection": { backgroundColor: "#cfe0f5 !important" },
  ".cm-searchMatch": { backgroundColor: "#ffe78a" },
  ".cm-searchMatch-selected": { backgroundColor: "var(--yellow)" },
  ".cm-panels": { backgroundColor: "var(--gray-f6)", color: "var(--black)", fontFamily: "var(--font-sans)" },
  ".cm-panels-top": { borderBottom: "1px solid var(--gray-ddd)" },
  ".cm-panels-bottom": { borderTop: "1px solid var(--gray-ddd)" },
  ".cm-textfield": { border: "1px solid var(--gray-767)", borderRadius: "0", fontSize: "13px" },
  ".cm-button": { backgroundImage: "none", backgroundColor: "var(--white)", border: "1px solid var(--gray-767)", borderRadius: "0" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--gray-eee)", border: "0", color: "var(--gray-444)" },
});

const editorHighlight = CM.syntaxHighlighting(CM.HighlightStyle.define([
  { tag: [CM.tags.keyword, CM.tags.modifier, CM.tags.controlKeyword, CM.tags.operatorKeyword], color: "#7a1fa2" },
  { tag: [CM.tags.string, CM.tags.special(CM.tags.string), CM.tags.regexp], color: "#1b7a32" },
  { tag: [CM.tags.number, CM.tags.bool, CM.tags.null, CM.tags.atom], color: "#a8440f" },
  { tag: [CM.tags.comment, CM.tags.lineComment, CM.tags.blockComment], color: "var(--gray-767)", fontStyle: "italic" },
  { tag: [CM.tags.typeName, CM.tags.className, CM.tags.namespace], color: "#0057b8" },
  { tag: [CM.tags.function(CM.tags.variableName), CM.tags.function(CM.tags.propertyName)], color: "#004a8f" },
  { tag: [CM.tags.definition(CM.tags.variableName)], color: "#000000", fontWeight: "bold" },
  { tag: [CM.tags.propertyName, CM.tags.attributeName], color: "#7d5b00" },
  { tag: [CM.tags.tagName, CM.tags.heading], color: "#b0002a", fontWeight: "bold" },
  { tag: [CM.tags.meta, CM.tags.processingInstruction], color: "var(--gray-666)" },
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

function loadSavedTabs(windowID) {
  const saved = storageGet(EDITOR_TABS_KEY + windowID);
  return saved && Array.isArray(saved.paths) ? saved : { paths: [], active: null };
}

function clockTime() {
  return new Date().toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

function createEditorTabs({ strip, chatPanel, editorPanel }) {
  let windowID = null;
  let available = false;
  // Each tab: {path, wrap, host, banner, status, saveBtn, view, base, etag,
  // conflict, dirty, loading, saving, draftTimer, draftFailed}. base is the
  // disk version the edit started from ({text, hash}); conflict is a newer
  // disk version that arrived while the tab had unsaved edits.
  let tabs = [];
  let active = null; // a tab, or null for the chat tab
  let gen = 0; // bumped on window switch; stale responses are dropped

  const stripTabs = el("div", { class: "editor-tabs__list", role: "tablist" });
  const openBtn = el("button", { class: "editor-tabs__open", type: "button", title: "Open a file (Ctrl+P)", text: "Open file…" });
  strip.replaceChildren(stripTabs, openBtn);

  const fileURL = () => `/api/sessions/${encodeURIComponent(windowID)}/file`;
  const draftKey = (path) => EDITOR_DRAFT_KEY + windowID + "\u0000" + path;

  function save() {
    if (windowID) storageSet(EDITOR_TABS_KEY + windowID, { paths: tabs.map((t) => t.path), active: active ? active.path : null });
  }

  function renderStrip() {
    const items = [{ tab: null, label: "Chat", title: "Conversation" }, ...tabs.map((t) => ({ tab: t, label: t.path.split("/").pop(), title: t.path }))];
    stripTabs.replaceChildren(...items.map(({ tab, label, title }) => {
      const isActive = tab === active;
      const btn = el("button", { class: "editor-tab" + (isActive ? " is-active" : "") + (tab ? "" : " is-chat") + (tab?.dirty ? " is-dirty" : ""), type: "button", role: "tab", "aria-selected": String(isActive), title: tab?.dirty ? `${title} (unsaved)` : title }, [
        el("span", { class: "editor-tab__label", text: label }),
      ]);
      btn.addEventListener("click", () => activate(tab));
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
    active = tab;
    chatPanel.hidden = !!tab;
    editorPanel.hidden = !tab;
    for (const t of tabs) t.wrap.hidden = t !== tab;
    renderStrip();
    save();
    if (tab) load(tab);
  }

  function newTab(path) {
    const status = el("span", { class: "editor-pane__status" });
    const saveBtn = el("button", { class: "editor-pane__btn is-primary", type: "button", text: "Save", disabled: "" });
    const findBtn = el("button", { class: "editor-pane__btn", type: "button", text: "Find" });
    const lineBtn = el("button", { class: "editor-pane__btn", type: "button", text: "Go to line" });
    const banner = el("div", { class: "editor-pane__banner", hidden: "" });
    const host = el("div", { class: "editor-pane__host" });
    const wrap = el("div", { class: "editor-pane" }, [
      el("div", { class: "editor-pane__bar" }, [
        // rtl so a long path loses its start, not the file name; bdi keeps
        // the text itself (e.g. a leading ".") in order.
        el("span", { class: "editor-pane__path" }, [el("bdi", { text: path })]),
        status,
        el("span", { class: "editor-pane__spacer" }),
        findBtn, lineBtn, saveBtn,
      ]),
      banner,
      host,
    ]);
    wrap.hidden = true;
    const tab = { path, wrap, host, banner, status, saveBtn, view: null, base: null, etag: null, conflict: null, dirty: false, loading: false, saving: false, draftTimer: 0, draftFailed: false };
    findBtn.addEventListener("click", () => tab.view && CM.openSearchPanel(tab.view));
    lineBtn.addEventListener("click", () => tab.view && CM.gotoLine(tab.view));
    saveBtn.addEventListener("click", () => saveTab(tab));
    editorPanel.appendChild(wrap);
    return tab;
  }

  function open(path) {
    if (!windowID || !path) return;
    let tab = tabs.find((t) => t.path === path);
    if (!tab) {
      tab = newTab(path);
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
    storageSet(draftKey(tab.path), null);
    const i = tabs.indexOf(tab);
    tabs.splice(i, 1);
    tab.view?.destroy();
    tab.wrap.remove();
    if (active === tab) activate(tabs[i] || tabs[i - 1] || null);
    else { renderStrip(); save(); }
  }

  function showNote(tab, text) {
    tab.view?.destroy();
    tab.view = null;
    tab.base = null;
    tab.etag = null;
    setConflict(tab, null);
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
    const dirty = !!(tab.view && tab.base && docText(tab) !== tab.base.text);
    if (dirty !== tab.dirty) {
      tab.dirty = dirty;
      renderStrip();
    }
    tab.saveBtn.disabled = !tab.view || tab.saving || (!dirty && !tab.conflict);
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
    tab.draftFailed = !storageSet(draftKey(tab.path), draft) && !!draft;
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
    const reload = el("button", { class: "editor-pane__btn", type: "button", text: current.exists ? "Reload from disk" : "Close tab" });
    const overwrite = el("button", { class: "editor-pane__btn", type: "button", text: "Keep mine and save" });
    reload.addEventListener("click", () => {
      if (!current.exists) { tab.dirty = false; closeTab(tab); return; }
      applyDisk(tab, current, true);
    });
    overwrite.addEventListener("click", () => saveTab(tab, current.hash));
    tab.banner.replaceChildren(
      el("span", { class: "editor-pane__banner-text", text: current.exists
        ? "This file changed on disk while you were editing it (Claude may have edited it)."
        : "This file was deleted on disk while you were editing it." }),
      reload,
      ...(current.exists && !current.binary && !current.tooLarge ? [overwrite] : []),
    );
    tab.banner.hidden = false;
  }

  // applyDisk makes a disk version the tab's base and content, discarding
  // any unsaved edits when force is set.
  function applyDisk(tab, data, force) {
    tab.base = { text: data.text, hash: data.hash };
    tab.etag = `"${data.hash}"`;
    setConflict(tab, null);
    const old = docText(tab);
    if (old !== data.text) tab.view.dispatch({ changes: minimalChange(old, data.text) });
    updateState(tab, force ? "Reloaded " + clockTime() : old !== data.text ? "Updated " + clockTime() : undefined);
  }

  // ── Loading and saving ────────────────────────────────────────

  // load fetches the tab's file, sending its ETag so an unchanged file
  // costs a body-less 304.
  async function load(tab) {
    if (!available || tab.loading || tab.saving) {
      if (!available && !tab.view) showNote(tab, "No live session.");
      return;
    }
    const g = gen;
    tab.loading = true;
    try {
      const headers = tab.etag ? { "If-None-Match": tab.etag } : {};
      const res = await fetch(`${fileURL()}?path=${encodeURIComponent(tab.path)}`, { headers, cache: "no-store" });
      if (g !== gen || !tabs.includes(tab) || tab.saving) return;
      if (res.status === 304) return;
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        if (tab.dirty) return; // keep the edits; the save will report the problem
        showNote(tab, res.status === 404 ? "This file isn't part of the checkout (or is ignored by git)." : `Couldn't open the file: ${data.error || res.status}`);
        return;
      }
      if (!data.exists || data.binary || data.tooLarge) {
        if (tab.dirty) { tab.etag = res.headers.get("ETag"); setConflict(tab, data); return; }
        showNote(tab, !data.exists ? "This file was deleted." : data.binary ? "Binary file — not shown." : `Too large to open here (${Math.round(data.size / 1024)} KB).`);
        return;
      }

      if (!tab.view) {
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
    const draft = storageGet(draftKey(tab.path));
    tab.base = { text: data.text, hash: data.hash };
    tab.etag = `"${data.hash}"`;
    tab.host.replaceChildren();
    tab.view = new CM.EditorView({
      parent: tab.host,
      state: CM.EditorState.create({
        doc: draft && typeof draft.text === "string" ? draft.text : data.text,
        extensions: editorExtensions(tab.path, { onSave: () => saveTab(tab), onChange: () => updateState(tab) }),
      }),
    });
    if (draft && draft.baseHash !== data.hash) {
      // The draft was made against an older version of the file.
      tab.base = { text: null, hash: draft.baseHash };
      setConflict(tab, data);
    }
    updateState(tab, draft ? "Restored unsaved changes" : "");
  }

  // saveTab writes the tab's text. baseHash defaults to the version the
  // edit started from; "Keep mine and save" passes the conflicting disk
  // version's hash to overwrite it deliberately.
  async function saveTab(tab, baseHash) {
    if (!tab.view || tab.saving || !windowID) return;
    if (!tab.dirty && !tab.conflict) return;
    const g = gen;
    const text = docText(tab);
    tab.saving = true;
    updateState(tab, "Saving…");
    try {
      const res = await fetch(fileURL(), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: tab.path, text, baseHash: baseHash || tab.base.hash }),
      });
      const data = await res.json().catch(() => ({}));
      if (g !== gen || !tabs.includes(tab)) return;
      tab.saving = false;
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
      updateState(tab, `Save failed: ${err.message}`);
    }
  }

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
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/tree`);
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

  // Only the visible tab polls; others reload when activated.
  function poll() {
    if (active && available && document.visibilityState === "visible") load(active);
  }
  document.addEventListener("visibilitychange", poll);
  setInterval(poll, EDITOR_POLL_MS);

  return {
    open,
    // setWindow restores the window's remembered tabs; files load lazily
    // when their tab is shown, and unsaved edits come back from drafts.
    setWindow(id) {
      if (id === windowID) return;
      flushDrafts();
      gen++;
      for (const t of tabs) { clearTimeout(t.draftTimer); t.view?.destroy(); t.wrap.remove(); }
      tabs = [];
      active = null;
      windowID = id;
      available = false;
      strip.hidden = !id;
      if (qoDialog.open) qoDialog.close();
      const saved = id ? loadSavedTabs(id) : { paths: [], active: null };
      for (const p of saved.paths) tabs.push(newTab(p));
      activate(tabs.find((t) => t.path === saved.active) || null);
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
