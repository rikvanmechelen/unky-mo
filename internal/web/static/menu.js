// Claude Code's slash-command menus (/model, /config, /permissions, …) in
// the browser. The server parses the menu off Claude's pane (menu.go) and
// takes keys back (handlers_menu_keys.go); this draws the menu and turns
// clicks and key presses into those keys. Needs menu-model.js.

const MENU_POLL_MS = 1000;

// createMenuWatcher keeps the current menu: it polls GET /menu while
// active (the footer is gone, so a dialog may be up) and hands each change
// to onChange(menu | null). send() types keys into the menu; the answer is
// the screen afterwards, adopted at once. The ETag makes an unchanged
// screen a 304. onNote gets a message for a refused key press (or "").
// deps replaces fetch and the timers in tests.
function createMenuWatcher(onChange, onNote = () => {}, deps = {}) {
  const fetch = deps.fetch || window.fetch.bind(window);
  const setTimeout = deps.setTimeout || window.setTimeout.bind(window);
  const clearTimeout = deps.clearTimeout || window.clearTimeout.bind(window);
  let windowID = null;
  let active = false;
  let etag = null;
  let timer = null;
  let gen = 0;
  let last = null;
  let sending = 0;
  let chain = Promise.resolve();

  function set(menu) {
    last = menu;
    etag = menu ? `"${menu.sig}"` : null;
    onChange(menu);
  }

  async function poll() {
    const g = gen;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/menu`,
        { headers: etag ? { "If-None-Match": etag } : {} });
      if (res.ok) {
        const data = await res.json();
        // An answer read while a key press is out would put the old screen
        // back: drop it (the press answers with the new one), but keep
        // polling — returning here once killed the loop for good.
        if (g === gen && !sending && (!data.menu || !last || data.menu.sig !== last.sig)) set(data.menu || null);
      }
    } catch (err) {
      // transient — keep what we have
    }
    if (g === gen && active) timer = setTimeout(poll, MENU_POLL_MS);
  }

  function stop() {
    gen++;
    clearTimeout(timer);
    timer = null;
    if (last) set(null);
  }

  async function post(p) {
    const menu = last;
    if (!menu || !windowID) return;
    const id = windowID, g = gen;
    sending++;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/menu/keys`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        // A row click is relative to the screen it was made on.
        body: JSON.stringify({ sig: p.sig || menu.sig, keys: p.keys, goto: p.goto, setText: p.setText }),
      });
      const data = await res.json().catch(() => ({}));
      if (g !== gen) return;
      if (res.ok) {
        onNote("");
        set(data.menu || null);
      } else {
        onNote(data.error || `Couldn't send (${res.status})`);
        if (res.status === 409 && "menu" in data) set(data.menu || null);
      }
    } catch (err) {
      if (g === gen) onNote(String(err));
    } finally {
      sending--;
    }
  }

  return {
    setWindow(id) {
      const was = active;
      stop();
      windowID = id;
      chain = Promise.resolve();
      if (was && id) poll();
    },
    setActive(on) {
      if (on === active) return;
      active = on;
      if (!windowID) return;
      if (on) poll();
      else stop();
    },
    current() { return last; },
    // send types keys (and/or moves the cursor); calls queue so a held
    // arrow key is a sequence, not a race.
    send(payload) {
      chain = chain.then(() => post(payload));
      return chain;
    },
  };
}

// createMenuDialog draws a menu view into a <dialog> and sends what the
// user does through send(payload). The dialog holds the keyboard while
// it's open: keys go to Claude's menu, not to the page. With an input in
// the menu (a search box), typing goes to a real <input> whose edits are
// sent as key presses.
function createMenuDialog({ send, onClose = () => {} }) {
  const dialog = el("dialog", { class: "dialog menu-dialog", tabindex: "-1" });
  const body = el("div", { class: "menu-dialog__body" });
  dialog.appendChild(body);
  const inputEl = el("input", { class: "menu-input__field", type: "text", autocomplete: "off", autocapitalize: "off", spellcheck: "false", "aria-label": "Menu input" });
  const note = el("div", { class: "menu-note", role: "status" });
  let view = null;
  let showRaw = false;
  let shownText = ""; // the text last sent to, or read from, the menu's box
  let typing = 0; // text sends in flight, during which the screen's text isn't authoritative
  let typeTimer = null;

  dialog.addEventListener("close", () => onClose());
  // Esc is the menu's, not the dialog's. The keydown handler below sends
  // it; the cancel event that can follow the same key press must not send
  // a second one (two Escapes reach /rewind). A cancel without a keydown
  // (Android's back gesture) sends it here.
  let escAt = 0;
  dialog.addEventListener("cancel", (e) => {
    e.preventDefault();
    if (Date.now() - escAt > 500) send({ keys: [{ key: "esc" }] });
  });
  // Buttons and rows never take focus, so the dialog keeps the keyboard.
  dialog.addEventListener("mousedown", (e) => {
    if (e.target.closest("button, .menu-row, .menu-tab") && e.target !== inputEl) e.preventDefault();
  });

  const NAV_IN_INPUT = new Set(["enter", "esc", "up", "down", "tab", "btab", "pgup", "pgdn"]);
  dialog.addEventListener("keydown", (e) => {
    if (!view) return;
    const key = menuKeyFromEvent(e, view);
    if (!key) return;
    if (e.target === inputEl) {
      if (!NAV_IN_INPUT.has(key)) return; // text, left/right, backspace: the input's own
      flushTyping();
    } else if (view.input && [...key].length === 1) {
      // a printable key with a search box: it's for the box
      inputEl.focus();
      return;
    }
    e.preventDefault();
    if (key === "esc") escAt = Date.now();
    send({ keys: [{ key }] });
  });

  // The box's text goes out whole: the server types the difference from
  // what Claude's box holds right then, so an edit made in the terminal
  // meanwhile isn't counted wrong.
  function flushTyping() {
    clearTimeout(typeTimer);
    typeTimer = null;
    if (inputEl.value === shownText) return;
    shownText = inputEl.value;
    typing++;
    send({ setText: inputEl.value }).finally(() => { typing--; });
  }
  inputEl.addEventListener("input", () => {
    clearTimeout(typeTimer);
    typeTimer = setTimeout(flushTyping, 120);
  });

  function button(text, title, onClick, cls = "") {
    const b = el("button", { class: "btn btn--small menu-btn " + cls, type: "button", title, tabindex: "-1", text });
    b.addEventListener("click", onClick);
    return b;
  }
  const press = (key) => () => send({ keys: [{ key }] });

  // PageUp/PageDown do nothing in Claude's lists (probed), so a cut list
  // scrolls by moving the cursor: clicking "↓ N more" steps it down.
  function moreNode(text, key) {
    const node = el("div", { class: "menu-more", role: "button", title: `Move the cursor ${key}`, text });
    node.addEventListener("click", () => send({ keys: [{ key }] }));
    return node;
  }

  function renderRow(row, index) {
    const cls = "menu-row" + (row.cursor ? " is-cursor" : "") + (row.marker ? " has-marker" : "");
    const cells = [];
    if (row.number) cells.push(el("span", { class: "menu-row__num", text: `${row.number}.` }));
    if (row.marker) cells.push(el("span", { class: "menu-row__marker", "aria-hidden": "true", text: row.marker }));
    (row.cols && row.cols.length ? row.cols : [row.text]).forEach((c, i) =>
      cells.push(el("span", { class: "menu-row__col" + (i ? " is-secondary" : ""), text: c })));
    // Activating is a deliberate press of a labelled button on the cursor
    // row, not a second tap on the row (see menuClickPlan).
    const enter = row.cursor ? menuEnterLabel(view) : null;
    if (enter) {
      const act = button(enter + " ⏎", "Press Enter", (e) => { e.stopPropagation(); send({ keys: [{ key: "enter" }], sig: view.sig }); }, "menu-row__act");
      cells.push(act);
    }
    const node = el("div", { class: cls, role: "option", "aria-selected": String(!!row.cursor), "data-index": String(index) }, cells);
    node.addEventListener("click", () => {
      // Relative to the screen the click was made on.
      const plan = menuClickPlan(view, index);
      if (plan) send({ ...plan, sig: view.sig });
    });
    return node;
  }

  function render() {
    const hadFocus = document.activeElement === inputEl;
    const scroll = body.querySelector(".menu-rows");
    const keep = scroll ? scroll.scrollTop : 0;
    body.replaceChildren();
    if (!view) return;

    body.appendChild(el("div", { class: "dialog__title", text: view.title || "Menu" }));
    if (view.tabs && view.tabs.length) {
      const tabs = el("div", { class: "menu-tabs", role: "tablist" }, view.tabs.map((t) =>
        el("span", { class: "menu-tab" + (t.active ? " is-active" : ""), role: "tab", "aria-selected": String(!!t.active), text: t.label })));
      body.appendChild(el("div", { class: "menu-tabbar" }, [
        tabs,
        button("‹", "Previous tab (←)", press("left")),
        button("›", "Next tab (→)", press("right")),
      ]));
    }
    if (view.subtitle) body.appendChild(el("div", { class: "menu-sub", text: view.subtitle }));
    if (view.input) {
      if (!typing && !typeTimer) shownText = view.input.text || "";
      if (!typing && !typeTimer) inputEl.value = shownText;
      inputEl.placeholder = view.input.placeholder || "";
      body.appendChild(el("label", { class: "menu-input" }, [
        el("span", { class: "menu-input__icon", "aria-hidden": "true", text: view.input.kind === "search" ? "⌕" : "›" }),
        inputEl,
      ]));
    }

    if (showRaw) {
      body.appendChild(el("pre", { class: "menu-raw", text: view.raw || "" }));
    } else {
      if (view.rows && view.rows.length) {
        const list = el("div", { class: "menu-rows", role: "listbox" });
        if (view.above) list.appendChild(moreNode(`↑ ${view.above} more`, "up"));
        for (const sec of menuSections(view)) {
          if (sec.group) list.appendChild(el("div", { class: "menu-group", text: sec.group }));
          for (const { row, index } of sec.items) list.appendChild(renderRow(row, index));
        }
        if (view.below) list.appendChild(moreNode(`↓ ${view.below} more`, "down"));
        body.appendChild(el("div", { class: "menu-listbar" }, [
          el("span", { class: "menu-listbar__count", text: view.below || view.above ? "scrolls — ↑/↓ move through the rest" : `${view.rows.length} items` }),
          button("▲", "Cursor up (↑)", press("up")),
          button("▼", "Cursor down (↓)", press("down")),
        ]));
        body.appendChild(list);
        list.scrollTop = keep;
        const cur = list.querySelector(".is-cursor");
        if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
      }
      if (view.extra && view.extra.length) body.appendChild(el("pre", { class: "menu-extra", text: view.extra.join("\n") }));
    }

    body.appendChild(note);
    const legend = menuLegendButtons(view.legend);
    if (legend.length) {
      body.appendChild(el("div", { class: "menu-legend" }, legend.map((k) => el("span", { class: "menu-legend__item" }, [
        ...(k.typing
          ? [el("kbd", { text: "type" })]
          : k.buttons.map((b) => button(b.text, `${b.key} — ${k.label}`, press(b.key), "menu-btn--key"))),
        el("span", { text: " " + k.label }),
      ]))));
    }

    const actions = el("div", { class: "dialog__actions" });
    actions.append(
      button(showRaw ? "Structured view" : "Terminal view", "Show what the terminal shows", () => { showRaw = !showRaw; render(); }),
      button("Hide", "Hide this; the menu stays open in the terminal", () => dialog.close()),
    );
    body.appendChild(actions);
    if (hadFocus) {
      inputEl.focus();
      inputEl.setSelectionRange(inputEl.value.length, inputEl.value.length);
    }
  }

  return {
    element: dialog,
    isOpen() { return dialog.open; },
    note(msg) { note.textContent = msg || ""; },
    open(v) {
      view = v;
      if (!dialog.isConnected) document.body.appendChild(dialog);
      render();
      if (!dialog.open) dialog.showModal();
      if (!view.input) dialog.focus();
      else inputEl.focus();
    },
    update(v) {
      const same = view && v && v.sig === view.sig;
      view = v;
      if (dialog.open && !same) render();
    },
    close() { if (dialog.open) dialog.close(); },
  };
}

if (typeof module === "object" && module.exports) {
  module.exports = { createMenuWatcher };
}
