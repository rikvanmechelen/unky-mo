// Terminal drawer for the chat view: the browser side of the sidebar's
// terminal drawer. Tabs list the window's terminals (the one shown in the
// tmux drawer, plus those parked in its mo-terms session); the output pane
// mirrors the selected one, colours and cursor included, via tmux
// capture-pane (nothing is attached or resized).
//
// Input has two modes. Live (the default with a keyboard): every keystroke
// goes straight to the shell, so its own Tab completion, suggestions and
// history search work, and each keystroke's answer is the screen right
// after it. Line (the default on touch devices): a text box sends one line
// at a time, plus a Ctrl-C button.
// Uses el() from common.js.

const TERM_LIST_MS = 3000;
const TERM_OUTPUT_MS = 1000;
const TERM_LIVE_KEY = "mo.termLive";
// The output's height, set by dragging the drawer's top edge.
const TERM_HEIGHT_KEY = "mo.termHeight";
const TERM_HEIGHT_DEFAULT = 160;
const TERM_HEIGHT_MIN = 80;
const TERM_TRANSCRIPT_MIN = 120; // the transcript keeps at least this much

// liveKeyName maps a keydown to the tmux key name a live terminal sends
// (the server allows the same set, liveKeyNames), or null to leave the key
// to the browser — printable text arrives through the input event instead,
// so keyboard layouts, dead keys and IMEs all work.
function liveKeyName(e) {
  if (e.isComposing || e.metaKey) return null; // Cmd shortcuts stay the browser's
  const mod = (e.ctrlKey ? "C-" : "") + (e.altKey ? "M-" : "") + (e.shiftKey && !e.ctrlKey && !e.altKey ? "S-" : "");
  const arrows = { ArrowUp: "Up", ArrowDown: "Down", ArrowLeft: "Left", ArrowRight: "Right" };
  if (arrows[e.key]) {
    // tmux takes one modifier on an arrow here; pick the first.
    return (mod ? mod.slice(0, 2) : "") + arrows[e.key];
  }
  if (e.ctrlKey && e.shiftKey) return null; // Ctrl+Shift+C/V etc.: the browser's copy and paste
  const plain = {
    Enter: e.altKey ? "M-Enter" : "Enter", Tab: e.shiftKey ? "BTab" : "Tab",
    Backspace: e.altKey || e.ctrlKey ? "M-BSpace" : "BSpace", Delete: "DC", Insert: "IC", Escape: "Escape",
    Home: "Home", End: "End", PageUp: "PPage", PageDown: "NPage",
  };
  if (plain[e.key]) return plain[e.key];
  if (/^F([1-9]|1[0-2])$/.test(e.key)) return e.key;
  if (e.ctrlKey && e.altKey) return null; // AltGr on Linux/Windows: a character, not a shortcut
  if (e.ctrlKey) {
    if (e.key === " ") return "C-Space";
    // e.code, not e.key: the letter's position, whatever the layout or Shift.
    const m = /^Key([A-Z])$/.exec(e.code);
    return m ? "C-" + m[1].toLowerCase() : null;
  }
  if (e.altKey) {
    // On a Mac, Option+letter types a symbol; e.code still names the key.
    const m = /^Key([A-Z])$/.exec(e.code) || /^Digit([0-9])$/.exec(e.code);
    if (m) return "M-" + m[1].toLowerCase();
    return ".,<>/?_-".includes(e.key) && e.key.length === 1 ? "M-" + e.key : null;
  }
  return null;
}

// ANSI colours 0-15, then the 6x6x6 cube and the gray ramp of 256 colours.
function ansi256(n) {
  if (n < 16) return `var(--ansi-${n})`;
  if (n < 232) {
    const c = n - 16, v = (x) => (x ? 55 + x * 40 : 0);
    return `rgb(${v(Math.floor(c / 36))},${v(Math.floor(c / 6) % 6)},${v(c % 6)})`;
  }
  const g = 8 + (n - 232) * 10;
  return `rgb(${g},${g},${g})`;
}

// sgr applies one SGR escape's parameters to the style state.
function sgr(st, params) {
  const p = params === "" ? [0] : params.split(/[;:]/).map((x) => parseInt(x, 10) || 0);
  for (let i = 0; i < p.length; i++) {
    const n = p[i];
    if (n === 0) Object.assign(st, { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, inverse: false, strike: false });
    else if (n === 1) st.bold = true;
    else if (n === 2) st.dim = true;
    else if (n === 3) st.italic = true;
    else if (n === 4) st.underline = true;
    else if (n === 7) st.inverse = true;
    else if (n === 9) st.strike = true;
    else if (n === 22) st.bold = st.dim = false;
    else if (n === 23) st.italic = false;
    else if (n === 24) st.underline = false;
    else if (n === 27) st.inverse = false;
    else if (n === 29) st.strike = false;
    else if (n >= 30 && n <= 37) st.fg = ansi256(n - 30);
    else if (n >= 90 && n <= 97) st.fg = ansi256(n - 90 + 8);
    else if (n >= 40 && n <= 47) st.bg = ansi256(n - 40);
    else if (n >= 100 && n <= 107) st.bg = ansi256(n - 100 + 8);
    else if (n === 39) st.fg = null;
    else if (n === 49) st.bg = null;
    else if (n === 38 || n === 48) {
      let color = null;
      if (p[i + 1] === 5) { color = ansi256(p[i + 2] & 255); i += 2; }
      else if (p[i + 1] === 2) { color = `rgb(${p[i + 2] & 255},${p[i + 3] & 255},${p[i + 4] & 255})`; i += 4; }
      if (n === 38) st.fg = color; else st.bg = color;
    }
  }
}

function styleCSS(st) {
  let fg = st.fg, bg = st.bg;
  if (st.inverse) { fg = st.bg || "var(--term-bg)"; bg = st.fg || "var(--term-text)"; }
  let css = "";
  if (fg) css += `color:${fg};`;
  if (bg) css += `background:${bg};`;
  if (st.bold) css += "font-weight:700;";
  if (st.dim) css += "opacity:0.6;";
  if (st.italic) css += "font-style:italic;";
  if (st.underline || st.strike) css += `text-decoration:${st.underline ? "underline " : ""}${st.strike ? "line-through" : ""};`;
  return css;
}

// cellWidth is how many terminal cells a character takes (tmux's cursor
// column counts cells): 0 for combining marks, 2 for wide East Asian
// characters and emoji, else 1. An approximation of wcwidth.
function cellWidth(ch) {
  const c = ch.codePointAt(0);
  if ((c >= 0x300 && c < 0x370) || (c >= 0x200b && c <= 0x200f) || (c >= 0xfe00 && c <= 0xfe0f)) return 0;
  if ((c >= 0x1100 && c <= 0x115f) || (c >= 0x2e80 && c <= 0xa4cf) || (c >= 0xac00 && c <= 0xd7a3) ||
      (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe4f) || (c >= 0xff00 && c <= 0xff60) ||
      (c >= 0xffe0 && c <= 0xffe6) || (c >= 0x1f300 && c <= 0x1faff) || (c >= 0x20000 && c <= 0x3fffd)) return 2;
  return 1;
}

// renderScreen turns a capture (text with tmux's SGR escapes, plus the
// cursor) into styled spans. Built as DOM nodes, never innerHTML. Blank
// lines below both the text and the cursor are left out.
function renderScreen(screen, showCursor) {
  const lines = (screen.text || "").split("\n");
  const plain = (l) => l.replace(/\x1b\[[0-9;:?]*[A-Za-z]/g, "").trim();
  let last = lines.length - 1;
  while (last > 0 && last > screen.cursorLine && plain(lines[last]) === "") last--;
  const frag = document.createDocumentFragment();
  const st = { fg: null, bg: null };
  // SGR (kept), any other CSI, OSC (hyperlinks, titles) and lone escapes (dropped).
  const esc = /\x1b(?:\[([0-9;:?]*)([A-Za-z])|\][^\x07\x1b]*(?:\x07|\x1b\\)?|.?)/g;
  for (let li = 0; li <= last; li++) {
    const line = lines[li];
    const runs = []; // [text, css]
    let pos = 0, m;
    esc.lastIndex = 0;
    while ((m = esc.exec(line))) {
      if (m.index > pos) runs.push([line.slice(pos, m.index), styleCSS(st)]);
      if (m[2] === "m" && !m[1].includes("?")) sgr(st, m[1]);
      pos = esc.lastIndex;
    }
    if (pos < line.length) runs.push([line.slice(pos), styleCSS(st)]);

    const cursorHere = showCursor && screen.cursorVisible && li === screen.cursorLine;
    let col = 0, placed = false;
    for (const [text, css] of runs) {
      let chunk = "";
      const flush = () => {
        if (!chunk) return;
        const span = document.createElement("span");
        if (css) span.style.cssText = css;
        span.textContent = chunk;
        frag.appendChild(span);
        chunk = "";
      };
      for (const ch of text) {
        if (cursorHere && !placed && col >= screen.cursorCol) {
          flush();
          frag.appendChild(el("span", { class: "term-cursor", text: ch }));
          placed = true;
          col += cellWidth(ch);
          continue;
        }
        chunk += ch;
        col += cellWidth(ch);
      }
      flush();
    }
    if (cursorHere && !placed) {
      if (screen.cursorCol > col) frag.appendChild(document.createTextNode(" ".repeat(screen.cursorCol - col)));
      frag.appendChild(el("span", { class: "term-cursor", text: " " }));
    }
    if (li < last) frag.appendChild(document.createTextNode("\n"));
  }
  return frag;
}

function createTerminalDrawer(root) {
  const tabsEl = el("div", { class: "term-drawer__tabs" });
  const newBtn = el("button", { class: "term-drawer__new", type: "button", text: "New terminal" });
  const toggleBtn = el("button", { class: "term-drawer__toggle", type: "button", text: "Hide" });
  const closeBtn = el("button", { class: "term-drawer__toggle", type: "button", text: "Close", title: "Close this terminal" });
  const errorEl = el("span", { class: "term-drawer__error" });
  const emptyEl = el("span", { class: "term-drawer__empty", text: "No terminals" });
  const outputEl = el("pre", { class: "term-drawer__output" });
  // The live terminal's keyboard: an offscreen textarea, so typed text
  // (any layout, IME or phone keyboard) arrives through its input event.
  const sink = el("textarea", {
    class: "term-drawer__sink", autocomplete: "off", autocapitalize: "off", spellcheck: "false",
    "aria-label": "Terminal (keys go straight to the shell)",
  });
  const hintEl = el("div", {
    class: "term-drawer__hint",
    text: "Keys go straight to the shell: Tab completes, ↑ and Ctrl-R search history. Select text to copy.",
    title: "The browser keeps Ctrl-W, Ctrl-T and Ctrl-N; use Alt-Backspace to delete a word.",
  });
  // A phone keyboard has no Tab, Esc, Ctrl or arrows: on touch screens a
  // row of them sits under the live terminal. Ctrl is sticky, applying to
  // the next letter typed (or arrow tapped).
  const ctrlKey = el("button", { class: "term-key", type: "button", text: "Ctrl", "aria-pressed": "false", title: "Ctrl for the next key" });
  const keysRow = el("div", { class: "term-drawer__keys" }, [
    ...[["Esc", "Escape"], ["Tab", "Tab"]].map(([label, key]) => el("button", { class: "term-key", type: "button", text: label, "data-key": key })),
    ctrlKey,
    ...[["↑", "Up"], ["↓", "Down"], ["←", "Left"], ["→", "Right"]].map(([label, key]) => el("button", { class: "term-key", type: "button", text: label, "data-key": key, "aria-label": key })),
    el("button", { class: "term-key", type: "button", text: "^C", "data-key": "C-c", "aria-label": "Ctrl-C" }),
  ]);
  const liveBtn = el("button", { class: "term-drawer__toggle", type: "button", title: "Send each key to the shell, or type a whole line first" });
  const input = el("input", { class: "term-drawer__input", type: "text", autocomplete: "off", spellcheck: "false", "aria-label": "Terminal command" });
  const ctrlC = el("button", { class: "term-drawer__ctrlc", type: "button", text: "Ctrl-C", title: "Interrupt the running command" });
  const form = el("form", { class: "term-drawer__line" }, [el("span", { class: "term-drawer__prompt", text: "$" }), input, ctrlC]);
  const body = el("div", { class: "term-drawer__body" }, [outputEl, sink, hintEl, keysRow, form]);
  const handle = el("div", {
    class: "term-drawer__handle", role: "separator", tabindex: "0",
    "aria-orientation": "horizontal", "aria-label": "Resize terminal",
    title: "Drag to resize, double-click to maximize",
  });
  root.replaceChildren(
    handle,
    el("div", { class: "term-drawer__bar" }, [tabsEl, emptyEl, newBtn, el("span", { class: "term-drawer__spacer" }), errorEl, liveBtn, closeBtn, toggleBtn]),
    body
  );

  let windowID = null;
  let available = false; // the window has a state row — only then poll
  let terminals = [];
  let shells = []; // Claude's own Bash-tool shells: read-only tabs
  let selected = null; // tab key: a pane id (no "%"), or "s<pid>" for a shell
  let open = false; // collapsed until a tab, Show or New terminal opens it; the open tab or Hide collapses it
  let tabsKey = null; // last rendered tab data — unchanged polls don't rebuild (keeps clicks)
  let lastOutput = null;
  let lastScreen = null; // the selected terminal's last capture, redrawn when focus moves
  let gen = 0; // bumped on window switch; stale responses are dropped
  let outSeq = 0; // bumped by each live keystroke batch; older polls are dropped
  let live = (() => {
    try {
      const v = localStorage.getItem(TERM_LIVE_KEY);
      if (v !== null) return v === "1";
    } catch {}
    return true;
  })();
  let ctrlArmed = false; // the keys row's sticky Ctrl
  let pending = []; // live keystrokes not sent yet
  let sending = false;

  const base = () => `/api/sessions/${encodeURIComponent(windowID)}/terminals`;

  function setError(msg) {
    errorEl.textContent = msg || "";
  }

  const isShell = (key) => typeof key === "string" && key.startsWith("s");

  // tabs merges the window's terminals and Claude's shells into one strip.
  function tabs() {
    return [
      ...terminals.map((t) => ({
        key: t.id, label: t.name, mark: t.visible ? " is-visible" : "",
        title: `${t.cwd}${t.visible ? " — shown in the tmux drawer" : ""}`,
      })),
      ...shells.map((sh) => ({
        key: "s" + sh.id, label: `claude: ${sh.command}`, mark: " is-shell", output: sh.output,
        title: `Claude's Bash shell (read-only), started ${sh.started}`,
      })),
    ];
  }

  function render() {
    const all = tabs();
    const key = JSON.stringify([all, selected, open]);
    if (key !== tabsKey) {
      tabsKey = key;
      tabsEl.replaceChildren(...all.map((t) => {
        const tab = el("button", {
          class: "term-tab" + (t.key === selected ? " is-selected" : ""),
          type: "button",
          title: t.title,
        }, [
          el("span", { class: "term-tab__mark" + t.mark }),
          el("span", { class: "term-tab__label", text: t.label }),
        ]);
        tab.addEventListener("click", () => {
          // Tapping the open tab again collapses the drawer, like Hide.
          if (open && selected === t.key) {
            open = false;
            render();
            return;
          }
          selected = t.key;
          open = true;
          lastOutput = null;
          lastScreen = null;
          pending = [];
          render();
          focusInput();
          refreshOutput();
        });
        return tab;
      }));
    }
    // With no tabs the drawer is just its bar: nothing to show or hide.
    const any = all.length > 0;
    emptyEl.hidden = any;
    toggleBtn.hidden = !any;
    toggleBtn.textContent = open ? "Hide" : "Show";
    body.hidden = !open || !any;
    handle.hidden = body.hidden;
    if (!body.hidden) applyHeight(height);
    const writable = !!selected && !isShell(selected); // shells are read-only
    form.hidden = !writable || live;
    hintEl.hidden = !writable || !live;
    keysRow.hidden = !writable || !live;
    sink.disabled = !writable || !live;
    outputEl.classList.toggle("is-live", writable && live);
    closeBtn.hidden = !writable;
    liveBtn.hidden = !writable;
    liveBtn.textContent = live ? "Line input" : "Live keys";
  }

  // focusInput puts the caret in the command line (or the live terminal)
  // once the drawer opens on a writable terminal, so typing works without
  // another click. Not on touch devices, where focusing would pop the
  // on-screen keyboard over the output.
  function focusInput() {
    if (!selected || isShell(selected) || body.hidden || window.matchMedia("(pointer: coarse)").matches) return;
    (live ? sink : input).focus({ preventScroll: true });
  }

  // Resizing: the drawer's top edge drags the output taller or shorter.
  // Only the browser's view changes; the tmux pane is never resized, the
  // capture's scrollback just fills the extra room.
  let height = (() => {
    try {
      const v = parseInt(localStorage.getItem(TERM_HEIGHT_KEY), 10);
      if (v >= TERM_HEIGHT_MIN) return v;
    } catch {}
    return TERM_HEIGHT_DEFAULT;
  })();

  // maxHeight is the tallest output that still leaves the transcript
  // TERM_TRANSCRIPT_MIN: the transcript is the flexible part, so whatever
  // it has above that minimum can go to the drawer.
  function maxHeight() {
    const transcript = root.parentElement && root.parentElement.querySelector(".chat-transcript");
    if (!transcript || body.hidden) return Infinity;
    return Math.max(TERM_HEIGHT_MIN, outputEl.offsetHeight + transcript.clientHeight - TERM_TRANSCRIPT_MIN);
  }

  // applyHeight shows the output at h (clamped), staying at the bottom if
  // it was there. The stored preference is left alone, so a small window
  // doesn't shrink it for good.
  function applyHeight(h) {
    const pinned = outputEl.scrollHeight - outputEl.scrollTop - outputEl.clientHeight < 24;
    const clamped = Math.round(Math.max(TERM_HEIGHT_MIN, Math.min(h, maxHeight())));
    root.style.setProperty("--term-h", `${clamped}px`);
    if (pinned) outputEl.scrollTop = outputEl.scrollHeight;
    handle.setAttribute("aria-valuenow", String(clamped));
    return clamped;
  }

  function setHeight(h) {
    height = applyHeight(h);
    try { localStorage.setItem(TERM_HEIGHT_KEY, String(height)); } catch {}
  }

  handle.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    handle.setPointerCapture(e.pointerId);
    const startY = e.clientY;
    const startH = outputEl.offsetHeight;
    document.body.classList.add("is-resizing-term");
    const move = (ev) => applyHeight(startH + startY - ev.clientY);
    const done = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", done);
      handle.removeEventListener("pointercancel", done);
      document.body.classList.remove("is-resizing-term");
      setHeight(outputEl.offsetHeight);
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", done);
    handle.addEventListener("pointercancel", done);
  });

  // Double-click toggles between the default and the tallest height.
  handle.addEventListener("dblclick", () => {
    const max = maxHeight();
    setHeight(outputEl.offsetHeight >= max - 1 ? TERM_HEIGHT_DEFAULT : max);
  });

  handle.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? 80 : 20;
    const cur = outputEl.offsetHeight;
    let h = null;
    if (e.key === "ArrowUp") h = cur + step;
    else if (e.key === "ArrowDown") h = cur - step;
    else if (e.key === "Home") h = maxHeight();
    else if (e.key === "End") h = TERM_HEIGHT_MIN;
    if (h === null) return;
    e.preventDefault();
    setHeight(h);
  });

  // A smaller window takes room back from the drawer; a larger one gives
  // the stored height back.
  window.addEventListener("resize", () => { if (!body.hidden) applyHeight(height); });

  function showScreen(screen) {
    lastScreen = screen;
    const showCursor = live && !isShell(selected);
    const key = JSON.stringify([screen, showCursor, document.activeElement === sink]);
    if (key === lastOutput) return;
    // Follow new output only if already scrolled to the bottom.
    const pinned = outputEl.scrollHeight - outputEl.scrollTop - outputEl.clientHeight < 24;
    lastOutput = key;
    outputEl.replaceChildren(renderScreen(screen, showCursor));
    if (pinned) outputEl.scrollTop = outputEl.scrollHeight;
  }

  // queueKeys adds live keystrokes and sends them; while a batch is in
  // flight, new ones collect and go together, in order.
  function queueKeys(items) {
    for (const it of items) {
      const prev = pending[pending.length - 1];
      if (it.text && prev && prev.text) prev.text += it.text;
      else pending.push(it);
    }
    sendKeys();
  }

  async function sendKeys() {
    if (sending || !pending.length || !selected || isShell(selected)) return;
    const g = gen, pane = selected;
    const batch = pending;
    pending = [];
    sending = true;
    const seq = ++outSeq;
    try {
      const keys = batch.filter((k) => !k.paste);
      const paste = batch.filter((k) => k.paste).map((k) => k.paste).join("");
      if (keys.length) {
        const data = await call("POST", `${base()}/${pane}/keys`, { keys });
        if (g === gen && pane === selected && seq === outSeq) showScreen(data);
      }
      if (paste) {
        const data = await call("POST", `${base()}/${pane}/keys`, { paste });
        if (g === gen && pane === selected && seq === outSeq) showScreen(data);
      }
      setError("");
      outputEl.scrollTop = outputEl.scrollHeight;
      // A command may still be printing: look again soon rather than in a second.
      if (batch.some((k) => k.key === "Enter")) setTimeout(refreshOutput, 250);
    } catch (err) {
      if (g === gen) setError(err.message);
    } finally {
      sending = false;
      if (g === gen) sendKeys(); else pending = [];
    }
  }

  async function call(method, path, payload) {
    const res = await fetch(path, {
      method,
      headers: payload ? { "Content-Type": "application/json" } : undefined,
      body: payload ? JSON.stringify(payload) : undefined,
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `request failed (${res.status})`);
    return data;
  }

  async function refreshList() {
    if (!windowID || !available || document.visibilityState !== "visible") return;
    const g = gen;
    try {
      const [list, shellList] = await Promise.all([
        call("GET", base()),
        // No live session (yet) means no shells, not an error.
        call("GET", `/api/sessions/${encodeURIComponent(windowID)}/shells`).catch(() => []),
      ]);
      if (g !== gen) return;
      terminals = list || [];
      shells = shellList || [];
      const all = tabs();
      if (!all.some((t) => t.key === selected)) {
        selected = (terminals.find((t) => t.visible) || terminals[0] || {}).id || (all[0] || {}).key || null;
        lastOutput = null;
      }
      setError("");
      render();
    } catch (err) {
      if (g === gen) setError(err.message);
    }
  }

  async function refreshOutput() {
    if (!windowID || !available || !selected || !open || document.visibilityState !== "visible") return;
    const g = gen, pane = selected;
    if (isShell(pane) && !(shells.find((sh) => "s" + sh.id === pane) || {}).output) {
      outputEl.textContent = "This shell has no output file to show.";
      lastOutput = null;
      return;
    }
    const path = isShell(pane)
      ? `/api/sessions/${encodeURIComponent(windowID)}/shells/${pane.slice(1)}/output`
      : `${base()}/${pane}/output`;
    const seq = outSeq;
    try {
      const data = await call("GET", path);
      // A keystroke answered meanwhile carries a newer screen.
      if (g !== gen || pane !== selected || seq !== outSeq || sending) return;
      if (!isShell(pane)) { showScreen(data); return; }
      if (data.text === lastOutput) return;
      const pinned = outputEl.scrollHeight - outputEl.scrollTop - outputEl.clientHeight < 24;
      lastOutput = data.text;
      outputEl.textContent = data.text;
      if (pinned) outputEl.scrollTop = outputEl.scrollHeight;
    } catch (err) {
      if (g === gen) refreshList(); // the pane probably went away
    }
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!selected || isShell(selected)) return;
    try {
      await call("POST", `${base()}/${selected}/input`, { text: input.value });
      input.value = "";
      setError("");
      outputEl.scrollTop = outputEl.scrollHeight;
      setTimeout(refreshOutput, 150);
    } catch (err) {
      setError(err.message);
    }
  });

  sink.addEventListener("keydown", (e) => {
    // Ctrl-C with text selected copies, as in a terminal emulator.
    if (e.ctrlKey && !e.shiftKey && !e.altKey && e.code === "KeyC" && String(window.getSelection())) return;
    // Ctrl-V pastes through the paste event below.
    if (e.ctrlKey && !e.shiftKey && !e.altKey && e.code === "KeyV") return;
    const key = liveKeyName(e);
    if (!key) return;
    e.preventDefault();
    e.stopPropagation(); // e.g. Ctrl-P is the shell's, not quick open's
    queueKeys([{ key }]);
  });
  sink.addEventListener("input", (e) => {
    if (e.isComposing) return;
    const text = sink.value.replace(/[\r\n]/g, "");
    sink.value = "";
    if (text) queueKeys(withCtrl(text));
  });
  sink.addEventListener("compositionend", () => {
    const text = sink.value.replace(/[\r\n]/g, "");
    sink.value = "";
    if (text) queueKeys(withCtrl(text));
  });

  function setCtrl(on) {
    ctrlArmed = on;
    ctrlKey.classList.toggle("is-on", on);
    ctrlKey.setAttribute("aria-pressed", String(on));
  }
  // withCtrl turns typed text into keystrokes, applying an armed Ctrl to
  // its first letter (or space); anything else just disarms it.
  function withCtrl(text) {
    if (!ctrlArmed) return [{ text }];
    setCtrl(false);
    const c = text[0].toLowerCase();
    const key = c === " " ? "C-Space" : /[a-z]/.test(c) ? "C-" + c : null;
    if (!key) return [{ text }];
    return text.length > 1 ? [{ key }, { text: text.slice(1) }] : [{ key }];
  }
  // The keys row must not take focus from the sink, or the phone keyboard
  // would close: pointerdown is cancelled and the key sent on click.
  keysRow.addEventListener("pointerdown", (e) => { if (e.target.closest(".term-key")) e.preventDefault(); });
  keysRow.addEventListener("click", (e) => {
    const b = e.target.closest(".term-key");
    if (!b || sink.disabled) return;
    if (b === ctrlKey) {
      setCtrl(!ctrlArmed);
    } else {
      let key = b.dataset.key;
      if (ctrlArmed && /^(Up|Down|Left|Right)$/.test(key)) key = "C-" + key;
      setCtrl(false);
      queueKeys([{ key }]);
    }
    if (document.activeElement !== sink) sink.focus({ preventScroll: true });
  });
  sink.addEventListener("paste", (e) => {
    e.preventDefault();
    const text = (e.clipboardData && e.clipboardData.getData("text/plain")) || "";
    if (text) queueKeys([{ paste: text.replace(/\r\n?/g, "\n") }]);
  });
  // The cursor is solid while the terminal has the keyboard, hollow otherwise.
  const redraw = () => { if (lastScreen && !isShell(selected)) showScreen(lastScreen); };
  sink.addEventListener("focus", () => { outputEl.classList.add("is-focused"); redraw(); });
  sink.addEventListener("blur", () => { outputEl.classList.remove("is-focused"); redraw(); });
  // A click in the output gives the live terminal the keyboard, unless it
  // selected text (to copy).
  outputEl.addEventListener("mouseup", () => {
    if (live && !sink.disabled && !String(window.getSelection())) sink.focus({ preventScroll: true });
  });

  liveBtn.addEventListener("click", () => {
    live = !live;
    try { localStorage.setItem(TERM_LIVE_KEY, live ? "1" : "0"); } catch {}
    pending = [];
    lastOutput = null;
    render();
    if (lastScreen) showScreen(lastScreen);
    focusInput();
  });

  ctrlC.addEventListener("click", async () => {
    if (!selected || isShell(selected)) return;
    try {
      await call("POST", `${base()}/${selected}/interrupt`);
      setTimeout(refreshOutput, 150);
    } catch (err) {
      setError(err.message);
    }
  });

  newBtn.addEventListener("click", async () => {
    if (!windowID) return;
    newBtn.disabled = true;
    try {
      const data = await call("POST", base());
      selected = data.id;
      open = true;
      lastOutput = null;
      await refreshList();
      focusInput();
      setTimeout(refreshOutput, 300); // give the shell a moment to print its prompt
    } catch (err) {
      setError(err.message);
    } finally {
      newBtn.disabled = false;
    }
  });

  // Close kills the selected terminal (the sidebar's `x`), after the same
  // kind of confirmation the TUI asks for destructive actions.
  closeBtn.addEventListener("click", async () => {
    const pane = selected;
    const t = terminals.find((x) => x.id === pane);
    if (!t) return;
    const ok = await showDialog({
      title: `Close ${t.name}?`,
      text: "The shell and anything running in it are killed.",
      actions: [{ label: "Cancel" }, { label: "Close terminal", value: true, danger: true }],
    });
    if (!ok) return;
    try {
      await call("DELETE", `${base()}/${pane}`);
      setError("");
      if (selected === pane) { selected = null; lastOutput = null; }
      await refreshList();
      refreshOutput();
    } catch (err) {
      setError(err.message);
    }
  });

  toggleBtn.addEventListener("click", () => {
    open = !open;
    render();
    if (open) { focusInput(); refreshOutput(); }
  });

  document.addEventListener("visibilitychange", () => { refreshList(); refreshOutput(); });
  setInterval(refreshList, TERM_LIST_MS);
  setInterval(refreshOutput, TERM_OUTPUT_MS);

  return {
    // setAvailable is driven by the chat view's state poll: the drawer is
    // shown, and polls tmux, only while the window has a state row.
    setAvailable(ok) {
      if (ok === available) return;
      available = ok;
      root.hidden = !ok || !windowID;
      if (ok) refreshList().then(refreshOutput);
    },
    setWindow(id) {
      if (id === windowID) return;
      gen++;
      available = false;
      windowID = id;
      terminals = [];
      shells = [];
      selected = null;
      open = false;
      lastOutput = null;
      lastScreen = null;
      pending = [];
      outputEl.textContent = "";
      input.value = "";
      setError("");
      root.hidden = true; // until setAvailable(true)
      render();
    },
  };
}
