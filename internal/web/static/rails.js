// Hiding the chat view's side rails (the session nav and the Files panel).
// Each rail has a tab centred on the main column's edge, the same grey as
// the rail. The tab keeps its shape either way: open, its arrow points to
// hide the rail; hidden, the same tab sits against the window edge with the
// arrow reversed. The left tab shows a status square per live session (the
// current one ringed), the right one the changed-file count. [ and ] toggle
// them. Hidden rails are remembered in localStorage (mo.rails).

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
