// Editor tabs for the chat view: the middle column becomes tabbed, with the
// chat (transcript + composer + terminal drawer) as a pinned first tab and
// one CodeMirror tab per opened file. Tabs are remembered per window in
// localStorage. Files come from /api/sessions/{windowID}/file, which only
// serves paths the Files panel lists; the active tab re-polls it with
// If-None-Match so a file Claude edits updates in place.
// CodeMirror is the vendored bundle's window.CM (vendor/codemirror.js,
// rebuilt with `make codemirror`); el() comes from common.js.

const EDITOR_POLL_MS = 3000;
const EDITOR_TABS_KEY = "mo.editorTabs.";
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

function editorExtensions(path) {
  const lang = CM.languageFor(path);
  return [
    CM.lineNumbers(), CM.highlightActiveLineGutter(), CM.highlightSpecialChars(), CM.foldGutter(),
    CM.drawSelection(), CM.highlightActiveLine(), CM.highlightSelectionMatches(), CM.bracketMatching(),
    CM.search({ top: true }),
    CM.keymap.of([...CM.searchKeymap, ...CM.foldKeymap, ...CM.defaultKeymap]),
    CM.EditorState.readOnly.of(true),
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

function loadSavedTabs(windowID) {
  try {
    const saved = JSON.parse(localStorage.getItem(EDITOR_TABS_KEY + windowID) || "null");
    if (saved && Array.isArray(saved.paths)) return saved;
  } catch (_) { /* storage unavailable or corrupt: start empty */ }
  return { paths: [], active: null };
}

function createEditorTabs({ strip, chatPanel, editorPanel }) {
  let windowID = null;
  let available = false;
  let tabs = []; // {path, wrap, host, status, view, etag, loading}
  let active = null; // a tab, or null for the chat tab
  let gen = 0; // bumped on window switch; stale responses are dropped

  const stripTabs = el("div", { class: "editor-tabs__list", role: "tablist" });
  const openBtn = el("button", { class: "editor-tabs__open", type: "button", title: "Open a file (Ctrl+P)", text: "Open file…" });
  strip.replaceChildren(stripTabs, openBtn);

  function save() {
    if (!windowID) return;
    try {
      localStorage.setItem(EDITOR_TABS_KEY + windowID, JSON.stringify({ paths: tabs.map((t) => t.path), active: active ? active.path : null }));
    } catch (_) { /* best effort */ }
  }

  function renderStrip() {
    const items = [{ tab: null, label: "Chat", title: "Conversation" }, ...tabs.map((t) => ({ tab: t, label: t.path.split("/").pop(), title: t.path }))];
    stripTabs.replaceChildren(...items.map(({ tab, label, title }) => {
      const isActive = tab === active;
      const btn = el("button", { class: "editor-tab" + (isActive ? " is-active" : "") + (tab ? "" : " is-chat"), type: "button", role: "tab", "aria-selected": String(isActive), title }, [
        el("span", { class: "editor-tab__label", text: label }),
      ]);
      btn.addEventListener("click", () => activate(tab));
      if (!tab) return btn;
      const close = el("span", { class: "editor-tab__close", role: "button", title: `Close ${tab.path}`, "aria-label": `Close ${tab.path}`, text: "×" });
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
    const findBtn = el("button", { class: "editor-pane__btn", type: "button", text: "Find" });
    const lineBtn = el("button", { class: "editor-pane__btn", type: "button", text: "Go to line" });
    const host = el("div", { class: "editor-pane__host" });
    const wrap = el("div", { class: "editor-pane" }, [
      el("div", { class: "editor-pane__bar" }, [
        // rtl so a long path loses its start, not the file name; bdi keeps
        // the text itself (e.g. a leading ".") in order.
        el("span", { class: "editor-pane__path" }, [el("bdi", { text: path })]),
        status,
        el("span", { class: "editor-pane__spacer" }),
        findBtn, lineBtn,
      ]),
      host,
    ]);
    wrap.hidden = true;
    const tab = { path, wrap, host, status, view: null, etag: null, loading: false };
    findBtn.addEventListener("click", () => tab.view && CM.openSearchPanel(tab.view));
    lineBtn.addEventListener("click", () => tab.view && CM.gotoLine(tab.view));
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

  function closeTab(tab) {
    const i = tabs.indexOf(tab);
    if (i < 0) return;
    tabs.splice(i, 1);
    tab.view?.destroy();
    tab.wrap.remove();
    if (active === tab) activate(tabs[i] || tabs[i - 1] || null);
    else { renderStrip(); save(); }
  }

  function showNote(tab, text) {
    tab.view?.destroy();
    tab.view = null;
    tab.etag = null;
    tab.host.replaceChildren(el("div", { class: "editor-pane__note", text }));
  }

  // load fetches the tab's file, sending its ETag so an unchanged file
  // costs a body-less 304.
  async function load(tab) {
    if (!available || tab.loading) {
      if (!available && !tab.view) showNote(tab, "No live session.");
      return;
    }
    const g = gen;
    tab.loading = true;
    try {
      const headers = tab.etag ? { "If-None-Match": tab.etag } : {};
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/file?path=${encodeURIComponent(tab.path)}`, { headers, cache: "no-store" });
      if (g !== gen || !tabs.includes(tab)) return;
      if (res.status === 304) return;
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        showNote(tab, res.status === 404 ? "This file isn't part of the checkout (or is ignored by git)." : `Couldn't open the file: ${data.error || res.status}`);
        return;
      }
      if (!data.exists) { showNote(tab, "This file was deleted."); return; }
      if (data.binary) { showNote(tab, "Binary file — not shown."); return; }
      if (data.tooLarge) { showNote(tab, `Too large to open here (${Math.round(data.size / 1024)} KB).`); return; }

      tab.etag = res.headers.get("ETag");
      if (!tab.view) {
        tab.host.replaceChildren();
        tab.view = new CM.EditorView({ parent: tab.host, state: CM.EditorState.create({ doc: data.text, extensions: editorExtensions(tab.path) }) });
        tab.status.textContent = "Read-only";
      } else {
        const old = tab.view.state.doc.toString();
        if (old !== data.text) {
          tab.view.dispatch({ changes: minimalChange(old, data.text) });
          tab.status.textContent = "Updated " + new Date().toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" });
        }
      }
    } catch (err) {
      if (g === gen && !tab.view) showNote(tab, `Couldn't open the file: ${err.message}`);
    } finally {
      tab.loading = false;
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
    if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "p") {
      e.preventDefault(); // instead of the browser's print dialog
      showQuickOpen();
    }
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
    // when their tab is shown.
    setWindow(id) {
      if (id === windowID) return;
      gen++;
      for (const t of tabs) { t.view?.destroy(); t.wrap.remove(); }
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
