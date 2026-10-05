// Hiding the chat view's side rails (the session nav and the Files panel).
// Each rail has a tab centred on the main column's edge, the same grey as
// the rail. The tab keeps its shape either way: open, its arrow points to
// hide the rail; hidden, the same tab sits against the window edge with the
// arrow reversed. The left tab shows a status square per live session (the
// current one ringed), the right one the changed-file count. [ and ] toggle
// them. Hidden rails are remembered in localStorage (mo.rails).
//
// In Overview mode the nav holds the inspector, so its width can be dragged
// from the main column's left edge (like the terminal drawer's height):
// double-click goes back to the default, arrow keys step it. The width is
// kept in localStorage (mo.overviewNavW) and set as --ov-nav-w.

const OV_NAV_DEFAULT = 340;
const OV_NAV_MIN = 260;
const OV_NAV_MAX = 760;
// OV_MAIN_MIN: what the Overview page keeps, however wide the nav is dragged.
const OV_MAIN_MIN = 480;

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

  // Resizing the Overview's nav.
  const resize = el("div", {
    class: "nav-resize", role: "separator", tabindex: "0",
    "aria-orientation": "vertical", "aria-label": "Resize inspector",
    "aria-valuemin": String(OV_NAV_MIN),
    title: "Drag to resize, double-click to reset",
  });
  main.append(resize);
  let navW = OV_NAV_DEFAULT;
  try { navW = Number(localStorage.getItem("mo.overviewNavW")) || OV_NAV_DEFAULT; } catch (e) { /* default */ }

  function maxNavW() {
    const files = shell.querySelector(".files-pane");
    const filesW = files ? files.offsetWidth : 0;
    return Math.max(OV_NAV_MIN, Math.min(OV_NAV_MAX, window.innerWidth - filesW - OV_MAIN_MIN));
  }
  // applyNavW shows the nav at w (clamped) without storing it, so a small
  // window doesn't shrink the preference for good.
  function applyNavW(w) {
    const clamped = Math.round(Math.max(OV_NAV_MIN, Math.min(w, maxNavW())));
    shell.style.setProperty("--ov-nav-w", `${clamped}px`);
    resize.setAttribute("aria-valuenow", String(clamped));
    resize.setAttribute("aria-valuemax", String(maxNavW()));
    return clamped;
  }
  function setNavW(w) {
    navW = applyNavW(w);
    try { localStorage.setItem("mo.overviewNavW", String(navW)); } catch (e) { /* not remembered */ }
  }
  applyNavW(navW);
  window.addEventListener("resize", () => applyNavW(navW));

  resize.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    resize.setPointerCapture(e.pointerId);
    const startX = e.clientX;
    const startW = shell.querySelector(".chat-nav").offsetWidth;
    shell.classList.add("is-resizing-nav");
    document.body.classList.add("is-resizing-nav");
    let w = startW;
    const move = (ev) => { w = applyNavW(startW + ev.clientX - startX); };
    const done = () => {
      resize.removeEventListener("pointermove", move);
      resize.removeEventListener("pointerup", done);
      resize.removeEventListener("pointercancel", done);
      shell.classList.remove("is-resizing-nav");
      document.body.classList.remove("is-resizing-nav");
      setNavW(w);
    };
    resize.addEventListener("pointermove", move);
    resize.addEventListener("pointerup", done);
    resize.addEventListener("pointercancel", done);
  });
  resize.addEventListener("dblclick", () => setNavW(OV_NAV_DEFAULT));
  resize.addEventListener("keydown", (e) => {
    const step = e.shiftKey ? 80 : 20;
    let w = null;
    if (e.key === "ArrowRight") w = navW + step;
    else if (e.key === "ArrowLeft") w = navW - step;
    else if (e.key === "Home") w = OV_NAV_MIN;
    else if (e.key === "End") w = maxNavW();
    if (w === null) return;
    e.preventDefault();
    setNavW(w);
  });

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
