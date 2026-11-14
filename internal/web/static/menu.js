// Claude Code's slash-command menus (/model, /config, /permissions, …) in
// the browser. The server parses the menu off Claude's pane (menu.go) and
// this draws it: a title or tab strip, a search box, rows with a cursor,
// extra content, and the legend of keys. Needs menu-model.js.

const MENU_POLL_MS = 1000;

// createMenuWatcher polls GET /menu while active (the footer is gone, so a
// dialog may be up) and hands each change to onChange(menu | null). The
// ETag makes an unchanged screen a 304.
function createMenuWatcher(onChange) {
  let windowID = null;
  let active = false;
  let etag = null;
  let timer = null;
  let gen = 0;
  let last = null;

  function set(menu) {
    last = menu;
    onChange(menu);
  }

  async function poll() {
    const g = gen;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/menu`,
        { headers: etag ? { "If-None-Match": etag } : {} });
      if (g !== gen) return;
      if (res.status === 304) {
        // unchanged
      } else if (res.ok) {
        const data = await res.json();
        if (g !== gen) return;
        etag = res.headers.get("ETag");
        set(data.menu || null);
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
    etag = null;
    if (last) set(null);
  }

  return {
    setWindow(id) {
      const was = active;
      stop();
      windowID = id;
      if (was && id) { gen++; poll(); }
    },
    setActive(on) {
      if (on === active) return;
      active = on;
      if (!windowID) return;
      if (on) poll();
      else stop();
    },
    current() { return last; },
    // refresh re-reads now, e.g. right after keys went to the menu.
    refresh() {
      if (!windowID) return;
      clearTimeout(timer);
      etag = null;
      poll();
    },
    // adopt takes a view the server already returned (a key press answers
    // with the new screen), so the dialog doesn't wait for the next poll.
    adopt(menu, newEtag) {
      etag = newEtag || null;
      set(menu);
    },
  };
}

// createMenuDialog draws a menu view into a <dialog>. The dialog is
// read-only for now: it shows what the terminal shows.
function createMenuDialog({ onClose = () => {} } = {}) {
  const dialog = el("dialog", { class: "dialog menu-dialog" });
  const body = el("div", { class: "menu-dialog__body" });
  dialog.appendChild(body);
  let view = null;
  let showRaw = false;

  dialog.addEventListener("close", () => { onClose(); });

  function render() {
    const scroll = body.querySelector(".menu-rows");
    const keep = scroll ? scroll.scrollTop : 0;
    body.replaceChildren();
    if (!view) return;

    body.appendChild(el("div", { class: "dialog__title", text: view.title || "Menu" }));
    if (view.tabs && view.tabs.length) {
      body.appendChild(el("div", { class: "menu-tabs", role: "tablist" }, view.tabs.map((t) =>
        el("span", { class: "menu-tab" + (t.active ? " is-active" : ""), role: "tab", "aria-selected": String(!!t.active), text: t.label }))));
    }
    if (view.subtitle) body.appendChild(el("div", { class: "menu-sub", text: view.subtitle }));
    if (view.input) {
      const shown = view.input.text || view.input.placeholder || "";
      body.appendChild(el("div", { class: "menu-input" + (view.input.text ? "" : " is-placeholder") }, [
        el("span", { class: "menu-input__icon", "aria-hidden": "true", text: view.input.kind === "search" ? "⌕" : "›" }),
        el("span", { text: shown }),
      ]));
    }

    if (showRaw) {
      body.appendChild(el("pre", { class: "menu-raw", text: view.raw || "" }));
    } else {
      if (view.rows && view.rows.length) {
        const list = el("div", { class: "menu-rows", role: "listbox" });
        if (view.above) list.appendChild(el("div", { class: "menu-more", text: `↑ ${view.above} more` }));
        for (const sec of menuSections(view)) {
          if (sec.group) list.appendChild(el("div", { class: "menu-group", text: sec.group }));
          for (const { row, index } of sec.items) {
            const cls = "menu-row" + (row.cursor ? " is-cursor" : "") + (row.marker ? " has-marker" : "");
            const cells = [];
            if (row.number) cells.push(el("span", { class: "menu-row__num", text: `${row.number}.` }));
            if (row.marker) cells.push(el("span", { class: "menu-row__marker", "aria-hidden": "true", text: row.marker }));
            (row.cols && row.cols.length ? row.cols : [row.text]).forEach((c, i) =>
              cells.push(el("span", { class: "menu-row__col" + (i ? " is-secondary" : ""), text: c })));
            list.appendChild(el("div", { class: cls, role: "option", "aria-selected": String(!!row.cursor), "data-index": String(index) }, cells));
          }
        }
        if (view.below) list.appendChild(el("div", { class: "menu-more", text: `↓ ${view.below} more` }));
        body.appendChild(list);
        list.scrollTop = keep;
        const cur = list.querySelector(".is-cursor");
        if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
      }
      if (view.extra && view.extra.length) body.appendChild(el("pre", { class: "menu-extra", text: view.extra.join("\n") }));
    }

    if (view.legend && view.legend.length) {
      body.appendChild(el("div", { class: "menu-legend" }, view.legend.map((k) =>
        el("span", { class: "menu-legend__item" }, [
          el("kbd", { text: k.typing ? "type" : (k.keys || []).join(" / ") }),
          el("span", { text: " " + k.label }),
        ]))));
    }

    const actions = el("div", { class: "dialog__actions" });
    const rawBtn = el("button", { class: "btn btn--small", type: "button", text: showRaw ? "Structured view" : "Terminal view" });
    rawBtn.addEventListener("click", () => { showRaw = !showRaw; render(); });
    const closeBtn = el("button", { class: "btn", type: "button", text: "Close" });
    closeBtn.addEventListener("click", () => dialog.close());
    actions.append(rawBtn, closeBtn);
    body.appendChild(actions);
  }

  return {
    element: dialog,
    isOpen() { return dialog.open; },
    open(v) {
      view = v;
      if (!dialog.isConnected) document.body.appendChild(dialog);
      render();
      if (!dialog.open) dialog.showModal();
    },
    update(v) {
      view = v;
      if (dialog.open) render();
    },
    close() { if (dialog.open) dialog.close(); },
  };
}
