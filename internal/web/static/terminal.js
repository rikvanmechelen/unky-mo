// Terminal drawer for the chat view: the browser side of the sidebar's
// terminal drawer. Tabs list the window's terminals (the one shown in the
// tmux drawer, plus those parked in its mo-terms session); the output pane
// mirrors the selected one via tmux capture-pane (nothing is attached or
// resized); one line at a time can be typed into it, plus Ctrl-C.
// Uses el() from common.js.

const TERM_LIST_MS = 3000;
const TERM_OUTPUT_MS = 1000;

function createTerminalDrawer(root) {
  const tabsEl = el("div", { class: "term-drawer__tabs" });
  const newBtn = el("button", { class: "term-drawer__new", type: "button", text: "New terminal" });
  const toggleBtn = el("button", { class: "term-drawer__toggle", type: "button", text: "Hide" });
  const closeBtn = el("button", { class: "term-drawer__toggle", type: "button", text: "Close", title: "Close this terminal" });
  const errorEl = el("span", { class: "term-drawer__error" });
  const emptyEl = el("span", { class: "term-drawer__empty", text: "No terminals" });
  const outputEl = el("pre", { class: "term-drawer__output" });
  const input = el("input", { class: "term-drawer__input", type: "text", autocomplete: "off", spellcheck: "false", "aria-label": "Terminal command" });
  const ctrlC = el("button", { class: "term-drawer__ctrlc", type: "button", text: "Ctrl-C", title: "Interrupt the running command" });
  const form = el("form", { class: "term-drawer__line" }, [el("span", { class: "term-drawer__prompt", text: "$" }), input, ctrlC]);
  const body = el("div", { class: "term-drawer__body" }, [outputEl, form]);
  root.replaceChildren(
    el("div", { class: "term-drawer__bar" }, [tabsEl, emptyEl, newBtn, el("span", { class: "term-drawer__spacer" }), errorEl, closeBtn, toggleBtn]),
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
  let gen = 0; // bumped on window switch; stale responses are dropped

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
          render();
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
    form.hidden = !selected || isShell(selected); // shells are read-only
    closeBtn.hidden = !selected || isShell(selected);
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
    try {
      const data = await call("GET", path);
      if (g !== gen || pane !== selected || data.text === lastOutput) return;
      // Follow new output only if already scrolled to the bottom.
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
    if (open) refreshOutput();
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
      outputEl.textContent = "";
      input.value = "";
      setError("");
      root.hidden = true; // until setAvailable(true)
      render();
    },
  };
}
