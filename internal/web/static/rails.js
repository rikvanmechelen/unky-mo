// Hiding the chat view's side rails (the session nav and the Files panel).
// Each rail has a tab centred on the main column's edge, the same grey as
// the rail. The tab keeps its shape either way: open, its arrow points to
// hide the rail; hidden, the same tab sits against the window edge with the
// arrow reversed. The left tab shows a status square per live session (the
// current one ringed), the right one the changed-file count. [ and ] toggle
// them. Hidden rails are remembered in localStorage (mo.rails).
//
// Either rail's width can be dragged from its edge of the main column
// (like the terminal drawer's height): double-click goes back to the
// default, arrow keys step it. The chat and Overview mode keep separate
// widths (the Overview's nav holds the inspector), each in localStorage and
// set as a custom property on the shell.

const RAIL_WIDTHS = {
  nav: {
    chat: { def: 260, min: 200, max: 600, prop: "--chat-nav-w", store: "mo.navW" },
    ov: { def: 340, min: 260, max: 760, prop: "--ov-nav-w", store: "mo.overviewNavW" },
  },
  files: {
    chat: { def: 300, min: 220, max: 760, prop: "--chat-files-w", store: "mo.filesW" },
    ov: { def: 320, min: 240, max: 760, prop: "--ov-files-w", store: "mo.overviewFilesW" },
  },
};
// RAIL_MAIN_MIN: what the main column keeps, however wide the rails are dragged.
const RAIL_MAIN_MIN = 480;

function createRails(shell) {
  const main = shell.querySelector(".chat-main");
  const RAILS = {
    nav: { key: "[", name: "sessions", cls: "hide-nav", side: "left" },
    files: { key: "]", name: "files", cls: "hide-files", side: "right" },
  };

  let hidden = {};
  try { hidden = JSON.parse(localStorage.getItem("mo.rails") || "{}") || {}; } catch (e) { hidden = {}; }

  function chevron() { return el("span", { class: "rail-tab__chev", "aria-hidden": "true" }); }

  const navMarks = el("span", { class: "rail-tab__marks" });
  const countEl = el("span", { class: "rail-tab__count" });
  const tabs = {
    nav: el("button", { class: "rail-tab rail-tab--left", type: "button" }, [chevron(), navMarks]),
    files: el("button", { class: "rail-tab rail-tab--right", type: "button" }, [
      chevron(),
      el("span", { class: "rail-tab__lines", "aria-hidden": "true" }, [el("span"), el("span"), el("span")]),
      countEl,
    ]),
  };
  for (const [rail, tab] of Object.entries(tabs)) {
    tab.addEventListener("click", () => toggle(rail));
    main.append(tab);
  }

  function apply() {
    for (const [rail, r] of Object.entries(RAILS)) {
      const isHidden = !!hidden[rail];
      shell.classList.toggle(r.cls, isHidden);
      tabs[rail].classList.toggle("is-hidden", isHidden);
      const label = `${isHidden ? "Show" : "Hide"} ${r.name}`;
      tabs[rail].title = `${label}  ${r.key}`;
      tabs[rail].setAttribute("aria-label", label);
      tabs[rail].setAttribute("aria-expanded", String(!isHidden));
    }
  }

  function toggle(rail) {
    hidden[rail] = !hidden[rail];
    try { localStorage.setItem("mo.rails", JSON.stringify(hidden)); } catch (e) { /* not remembered */ }
    apply();
  }

  document.addEventListener("keydown", (e) => {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
    if (document.querySelector("dialog[open]")) return;
    const rail = Object.keys(RAILS).find((r) => RAILS[r].key === e.key);
    if (!rail) return;
    e.preventDefault();
    toggle(rail);
  });

  apply();

  // Resizing the rails. Each rail has a handle on its edge of the main
  // column and a width per mode: the chat's, and the Overview's (where the
  // nav holds the inspector). A drag sets the width of the mode showing.
  const modeOf = () => (shell.classList.contains("is-overview") ? "ov" : "chat");
  const otherRail = { nav: "files", files: "nav" };
  const handles = {};
  const widths = {};
  for (const [rail, r] of Object.entries(RAILS)) {
    handles[rail] = el("div", {
      class: `rail-resize rail-resize--${rail}`, role: "separator", tabindex: "0",
      "aria-orientation": "vertical", "aria-label": `Resize ${r.name}`,
      title: "Drag to resize, double-click to reset",
    });
    main.append(handles[rail]);
    widths[rail] = {};
    for (const mode of Object.keys(RAIL_WIDTHS[rail])) {
      let w = null;
      try { w = Number(localStorage.getItem(RAIL_WIDTHS[rail][mode].store)) || null; } catch (e) { /* default */ }
      widths[rail][mode] = w || RAIL_WIDTHS[rail][mode].def;
    }
  }

  function railShown(rail) {
    if (hidden[rail]) return false;
    if (rail === "files") return shell.classList.contains("has-files") && !window.matchMedia("(max-width: 1100px)").matches;
    return !window.matchMedia("(max-width: 760px)").matches;
  }
  // maxW leaves the main column RAIL_MAIN_MIN beside the other rail, at the
  // width it has in the same mode.
  function maxW(rail, mode) {
    const spec = RAIL_WIDTHS[rail][mode];
    const other = otherRail[rail];
    const otherW = railShown(other) ? widths[other][mode] : 0;
    return Math.max(spec.min, Math.min(spec.max, window.innerWidth - otherW - RAIL_MAIN_MIN));
  }
  // applyW shows a rail at w (clamped) without storing it, so a small window
  // doesn't shrink the preference for good.
  function applyW(rail, mode, w) {
    const spec = RAIL_WIDTHS[rail][mode];
    const clamped = Math.round(Math.max(spec.min, Math.min(w, maxW(rail, mode))));
    shell.style.setProperty(spec.prop, `${clamped}px`);
    if (mode === modeOf()) {
      handles[rail].setAttribute("aria-valuemin", String(spec.min));
      handles[rail].setAttribute("aria-valuenow", String(clamped));
      handles[rail].setAttribute("aria-valuemax", String(maxW(rail, mode)));
    }
    return clamped;
  }
  function setW(rail, mode, w) {
    widths[rail][mode] = applyW(rail, mode, w);
    try { localStorage.setItem(RAIL_WIDTHS[rail][mode].store, String(widths[rail][mode])); } catch (e) { /* not remembered */ }
  }
  function applyAll() {
    for (const rail of Object.keys(widths)) for (const mode of Object.keys(widths[rail])) applyW(rail, mode, widths[rail][mode]);
  }
  applyAll();
  window.addEventListener("resize", applyAll);
  // The mode and the rails' visibility are classes on the shell, set here
  // and in chat.js; the clamps depend on them.
  let dragging = false;
  new MutationObserver(() => { if (!dragging) applyAll(); }).observe(shell, { attributes: true, attributeFilter: ["class"] });

  for (const [rail, handle] of Object.entries(handles)) {
    // Dragging away from the main column widens the rail.
    const dir = rail === "nav" ? 1 : -1;
    const pane = shell.querySelector(rail === "nav" ? ".chat-nav" : ".files-pane");
    handle.addEventListener("pointerdown", (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      handle.setPointerCapture(e.pointerId);
      const mode = modeOf();
      const startX = e.clientX;
      const startW = pane.offsetWidth;
      dragging = true;
      shell.classList.add("is-resizing-rail");
      document.body.classList.add("is-resizing-rail");
      let w = startW;
      const move = (ev) => { w = applyW(rail, mode, startW + dir * (ev.clientX - startX)); };
      const done = () => {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", done);
        handle.removeEventListener("pointercancel", done);
        setW(rail, mode, w);
        dragging = false;
        shell.classList.remove("is-resizing-rail");
        document.body.classList.remove("is-resizing-rail");
      };
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", done);
      handle.addEventListener("pointercancel", done);
    });
    handle.addEventListener("dblclick", () => { const mode = modeOf(); setW(rail, mode, RAIL_WIDTHS[rail][mode].def); });
    handle.addEventListener("keydown", (e) => {
      const mode = modeOf();
      const step = e.shiftKey ? 80 : 20;
      const cur = widths[rail][mode];
      let w = null;
      if (e.key === "ArrowRight") w = cur + dir * step;
      else if (e.key === "ArrowLeft") w = cur - dir * step;
      else if (e.key === "Home") w = RAIL_WIDTHS[rail][mode].min;
      else if (e.key === "End") w = maxW(rail, mode);
      if (w === null) return;
      e.preventDefault();
      setW(rail, mode, w);
    });
  }

  // Skips rebuilding when nothing changed, like the nav itself.
  let marksKey = null;
  return {
    // setSessions takes the nav's groups (navGroups) and the current window.
    setSessions(groups, windowID) {
      const rows = groups.flatMap((g) => g.items);
      const key = JSON.stringify([rows.map((p) => [p.window_id, p.status]), windowID]);
      if (key === marksKey) return;
      marksKey = key;
      navMarks.replaceChildren(...rows.map((p) => {
        const meta = STATUS[p.status] || STATUS.none;
        return statusSquare(meta.navSq || meta.sq, meta.ring, p.window_id === windowID ? "is-current" : "");
      }));
    },
    setCount(n) { countEl.textContent = n == null ? "" : String(n); },
  };
}
