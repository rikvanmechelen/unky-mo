// Slash-command completion for the composer, like Claude Code's own: typing
// "/" at the start of the message opens a list of the session's commands
// (built-ins, skills, plugin and custom commands, from
// /api/sessions/{windowID}/commands — see claude.SlashCommands), filtered as
// you type. ↑/↓ move, Tab completes, Enter completes and sends, Esc closes.
// Commands that open a terminal dialog are marked, since the browser can't
// drive those.

const COMMANDS_STALE_MS = 30000;
const COMMANDS_MAX_SHOWN = 50;

// commandQuery returns the command word being typed — the text after a
// leading "/" up to the caret — or null when the caret isn't in it.
function commandQuery(value, caret) {
  if (!value.startsWith("/")) return null;
  const word = value.slice(1, caret);
  if (/\s/.test(word)) return null;
  const end = value.slice(1).search(/\s/);
  if (end >= 0 && caret > end + 1) return null;
  return word;
}

// rankCommands orders the commands matching q: name prefix, then alias
// prefix, then the part after a plugin's "name:" prefix, then anywhere in
// the name, then (for two letters or more) in the description.
function rankCommands(cmds, q) {
  q = q.toLowerCase();
  const scored = [];
  for (const c of cmds) {
    const name = c.name.toLowerCase();
    let score = -1;
    if (name.startsWith(q)) score = 0;
    else if ((c.aliases || []).some((a) => a.toLowerCase().startsWith(q))) score = 1;
    else if (name.includes(":") && name.slice(name.indexOf(":") + 1).startsWith(q)) score = 2;
    else if (name.includes(q)) score = 3;
    else if (q.length >= 2 && (c.description || "").toLowerCase().includes(q)) score = 4;
    if (score >= 0) scored.push({ c, score });
  }
  scored.sort((a, b) => a.score - b.score || a.c.name.localeCompare(b.c.name));
  return scored.map((s) => s.c);
}

// createCommandMenu attaches the menu to the composer form. onSubmit sends
// the message (after Enter picked a command). With enterAddsLine (phones)
// Enter only completes, like Tab, since there only Send sends.
function createCommandMenu(form, input, onSubmit, enterAddsLine) {
  const menu = el("div", { class: "cmd-menu", id: "cmd-menu", role: "listbox", "aria-label": "Slash commands" });
  menu.hidden = true;
  form.append(menu);
  input.setAttribute("aria-controls", "cmd-menu");
  input.setAttribute("aria-autocomplete", "list");

  let windowID = null;
  let cmds = null;
  let fetchedAt = 0;
  let loading = null;
  let gen = 0;
  let matches = [];
  let active = 0;
  // Esc hides the menu until the command word changes.
  let dismissed = null;

  function setWindow(id) {
    windowID = id;
    cmds = null;
    fetchedAt = 0;
    loading = null;
    gen++;
    dismissed = null;
    close();
  }

  function load() {
    if (!windowID || loading || (cmds && Date.now() - fetchedAt < COMMANDS_STALE_MS)) return;
    const my = gen;
    loading = fetch(`/api/sessions/${encodeURIComponent(windowID)}/commands`)
      .then((res) => (res.ok ? res.json() : null))
      .catch(() => null)
      .then((list) => {
        if (my !== gen) return;
        loading = null;
        if (!list) return;
        cmds = list;
        fetchedAt = Date.now();
        refresh();
      });
  }

  function close() {
    menu.hidden = true;
    matches = [];
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-activedescendant");
  }

  function isOpen() { return !menu.hidden; }

  function refresh() {
    const q = commandQuery(input.value, input.selectionStart);
    if (q === null || document.activeElement !== input) {
      dismissed = null;
      close();
      return;
    }
    if (dismissed !== null && dismissed === q) return;
    dismissed = null;
    load();
    if (!cmds) return; // the first fetch's completion calls refresh again
    const prev = matches[active];
    matches = rankCommands(cmds, q);
    if (!matches.length) { close(); return; }
    const keep = prev ? matches.indexOf(prev) : -1;
    active = keep >= 0 ? keep : 0;
    render();
  }

  function render() {
    const shown = matches.slice(0, COMMANDS_MAX_SHOWN);
    menu.replaceChildren(...shown.map((c, i) => {
      const desc = (c.description || "").split("\n")[0];
      const row = el("div", {
        class: "cmd-menu__item" + (i === active ? " is-active" : ""), role: "option",
        id: `cmd-opt-${i}`, "aria-selected": String(i === active), title: c.description || "",
      }, [
        el("span", { class: "cmd-menu__name", text: "/" + c.name }),
        el("span", { class: "cmd-menu__hint", text: c.argument_hint || "" }),
        el("span", { class: "cmd-menu__desc", text: desc }),
        el("span", { class: "cmd-menu__tags" }, [
          ...(c.dialog ? [el("span", {
            class: "cmd-menu__tag is-dialog", text: "opens in terminal",
            title: c.argument_hint ? "Without arguments this opens a dialog in the terminal, which the browser can't answer"
              : "Opens a dialog in the terminal, which the browser can't answer",
          })] : []),
          el("span", { class: "cmd-menu__tag", text: c.source }),
        ]),
      ]);
      // mousedown, not click: keep the focus (and caret) in the message box.
      row.addEventListener("mousedown", (e) => { e.preventDefault(); complete(c, false); });
      return row;
    }));
    if (matches.length > shown.length) {
      menu.append(el("div", { class: "cmd-menu__more", text: `${matches.length - shown.length} more — keep typing` }));
    }
    menu.hidden = false;
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-activedescendant", `cmd-opt-${active}`);
    const row = menu.children[active];
    if (row) row.scrollIntoView({ block: "nearest" });
  }

  // complete replaces the command word with c's name. Tab or a click leaves
  // a space for arguments; Enter sends right away, as in Claude Code.
  function complete(c, send) {
    const value = input.value;
    const end = value.slice(1).search(/\s/);
    const rest = end >= 0 ? value.slice(end + 1) : "";
    const head = "/" + c.name;
    const tail = send ? rest : (rest ? rest : " ");
    input.value = head + tail;
    const caret = head.length + (send || rest ? 0 : 1);
    input.setSelectionRange(caret, caret);
    input.dispatchEvent(new Event("input"));
    close();
    dismissed = c.name;
    if (send) onSubmit();
  }

  // handleKey runs before the composer's own keydown handling; true means
  // the menu consumed the key.
  function handleKey(e) {
    if (!isOpen() || e.isComposing || e.altKey || e.ctrlKey || e.metaKey) return false;
    const n = Math.min(matches.length, COMMANDS_MAX_SHOWN);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      active = (active + (e.key === "ArrowDown" ? 1 : n - 1)) % n;
      render();
    } else if (e.key === "Tab" && !e.shiftKey) {
      complete(matches[active], false);
    } else if (e.key === "Enter" && !e.shiftKey) {
      complete(matches[active], !enterAddsLine);
    } else if (e.key === "Escape") {
      dismissed = commandQuery(input.value, input.selectionStart);
      close();
    } else {
      return false;
    }
    e.preventDefault();
    e.stopPropagation();
    return true;
  }

  input.addEventListener("input", refresh);
  input.addEventListener("click", refresh);
  input.addEventListener("keyup", (e) => {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight" || e.key === "Home" || e.key === "End") refresh();
  });
  input.addEventListener("blur", close);
  input.addEventListener("focus", refresh);

  return { setWindow, handleKey };
}
