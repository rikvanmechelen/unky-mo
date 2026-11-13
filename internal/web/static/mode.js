// The session's permission mode (manual / accept edits / plan / auto) as a
// small chip under the composer, read from Claude Code's footer via
// /api/sessions/{windowID}/mode (see mode.go). Clicking it opens a menu of
// modes; picking one has the server press shift+tab in Claude's pane until
// the footer shows it. The composer is tinted with the mode's color, like
// Claude Code's own prompt box.

const MODE_POLL_MS = 1500;
// Claude Code's ⏸ renders as a color emoji in most browser fonts (even
// with U+FE0E), so the pause modes get a plain double bar instead.
const PAUSE = "‖";

// The menu, in Claude Code's shift+tab order. bypassPermissions is only
// reachable when the session was launched for it, so it's listed only
// while it's the current mode.
const MODES = [
  { key: "manual", glyph: PAUSE, label: "Manual", hint: "Ask before edits and commands" },
  { key: "acceptEdits", glyph: "⏵⏵", label: "Accept edits", hint: "Edit files without asking" },
  { key: "plan", glyph: PAUSE, label: "Plan", hint: "Explore and plan, no changes" },
  { key: "auto", glyph: "⏵⏵", label: "Auto", hint: "Run without asking, with safety checks" },
];
const BYPASS_MODE = { key: "bypassPermissions", glyph: "⏵⏵", label: "Bypass permissions", hint: "Run everything without asking" };

// Shown when the footer isn't up: Claude is showing a dialog, where
// shift+tab is one of its own answers rather than the mode cycle.
const DIALOG_HINT = "Claude is showing a dialog — answer it before changing mode";

// Statuses worth polling the footer for. Whether the mode can be changed
// isn't one of them: that's decided by the footer itself (see `live`
// below), since a dialog replaces the prompt box and the state file's
// status can lag behind the screen.
const MODE_LIVE = new Set(["idle", "active", "question", "permission"]);

function modeInfo(m) {
  if (!m) return null;
  return [...MODES, BYPASS_MODE].find((x) => x.key === m.mode)
    || { key: "", glyph: "⏵⏵", label: m.label, hint: "" };
}

// createModeChip renders into root and tints tintEl via data-mode.
// onError gets a message to show (or "" to clear).
function createModeChip(root, tintEl, onError) {
  const chip = el("button", { class: "mode-chip", type: "button", "aria-haspopup": "menu", "aria-expanded": "false" });
  const menu = el("div", { class: "mode-menu", role: "menu" });
  menu.hidden = true;
  root.append(chip, menu);
  root.hidden = true;

  let windowID = null;
  let status = "none";
  let mode = null; // last modeView seen, kept while the footer is hidden by a dialog
  let live = false; // the last poll found the footer, so shift+tab reaches the cycle
  let busy = false;
  let gen = 0;
  let timer = null;

  function settable() { return live && !busy && !!mode; }

  function render() {
    const info = modeInfo(mode);
    root.hidden = !windowID || !MODE_LIVE.has(status) || !info;
    tintEl.dataset.mode = info && MODE_LIVE.has(status) ? info.key : "";
    if (!info) return;
    chip.dataset.mode = info.key;
    chip.replaceChildren(
      el("span", { class: "mode-chip__glyph", "aria-hidden": "true", text: info.glyph }),
      el("span", { text: busy ? "switching…" : info.label.toLowerCase() + " mode" }),
      el("span", { class: "mode-chip__hint", text: "· shift+tab to cycle" })
    );
    chip.disabled = !settable();
    chip.title = live ? "Change permission mode (shift+tab cycles)" : DIALOG_HINT;
    if (!settable()) closeMenu();
  }

  function openMenu() {
    const list = mode && mode.mode === BYPASS_MODE.key ? [...MODES, BYPASS_MODE] : MODES;
    menu.replaceChildren(...list.map((m) => {
      const current = mode && mode.mode === m.key;
      return el("button", {
        class: "mode-menu__item" + (current ? " is-current" : ""), type: "button", role: "menuitemradio",
        "aria-checked": String(current), "data-mode": m.key,
        onClick: () => { closeMenu(); if (!current) setMode({ mode: m.key }); },
      }, [
        el("span", { class: "mode-menu__glyph", "aria-hidden": "true", text: m.glyph }),
        el("span", { class: "mode-menu__label", text: m.label }),
        el("span", { class: "mode-menu__hint", text: m.hint }),
      ]);
    }));
    menu.hidden = false;
    chip.setAttribute("aria-expanded", "true");
    const cur = menu.querySelector(".is-current") || menu.firstChild;
    if (cur) cur.focus();
  }

  function closeMenu() {
    if (menu.hidden) return;
    menu.hidden = true;
    chip.setAttribute("aria-expanded", "false");
  }

  chip.addEventListener("click", () => { if (menu.hidden) openMenu(); else closeMenu(); });
  document.addEventListener("click", (e) => { if (!root.contains(e.target)) closeMenu(); });
  menu.addEventListener("keydown", (e) => {
    const items = [...menu.querySelectorAll("button")];
    const i = items.indexOf(document.activeElement);
    if (e.key === "Escape") { closeMenu(); chip.focus(); }
    else if (e.key === "ArrowDown") { e.preventDefault(); items[(i + 1) % items.length].focus(); }
    else if (e.key === "ArrowUp") { e.preventDefault(); items[(i - 1 + items.length) % items.length].focus(); }
  });

  async function setMode(body) {
    if (!settable()) return;
    const id = windowID, g = gen;
    busy = true;
    render();
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/mode`, {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const data = await res.json().catch(() => ({}));
      if (g !== gen) return;
      if (!res.ok) onError(data.error || `Couldn't change mode (${res.status})`);
      else { onError(""); if (data.mode) mode = data.mode; }
    } catch (err) {
      if (g === gen) onError(String(err));
    } finally {
      if (g === gen) { busy = false; render(); }
    }
  }

  async function poll() {
    const g = gen;
    if (!busy) {
      try {
        const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/mode`);
        const data = res.ok ? await res.json() : null;
        if (g === gen && !busy && data) {
          // A null mode means a dialog is covering the footer: keep showing
          // the last one, but shift+tab would answer the dialog instead.
          live = !!data.mode;
          if (data.mode) mode = data.mode;
          render();
        }
      } catch (err) {
        // transient — keep the last mode showing, and keep it settable:
        // only Claude's own screen says a dialog has the keyboard.
      }
    }
    if (g === gen) timer = setTimeout(poll, MODE_POLL_MS);
  }

  function stop() {
    gen++;
    clearTimeout(timer);
    timer = null;
    busy = false;
    live = false;
  }

  return {
    setWindow(id) {
      stop();
      windowID = id;
      mode = null;
      status = "none";
      closeMenu();
      render();
    },
    // setStatus is driven by the chat view's state poll; the footer is
    // only polled while the session is live.
    setStatus(s) {
      const wasLive = MODE_LIVE.has(status);
      status = s;
      if (MODE_LIVE.has(s) && !wasLive && windowID) poll();
      else if (!MODE_LIVE.has(s) && wasLive) stop();
      render();
    },
    // cycle steps one mode forward, like shift+tab in Claude Code. The key
    // works anywhere in the chat view, so when it can't act it says why
    // rather than doing nothing.
    cycle() {
      if (!windowID || !mode || busy) return;
      if (!live) { onError(DIALOG_HINT); return; }
      setMode({ next: true });
    },
  };
}
