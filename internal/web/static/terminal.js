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
  const errorEl = el("span", { class: "term-drawer__error" });
  const outputEl = el("pre", { class: "term-drawer__output" });
  const input = el("input", { class: "term-drawer__input", type: "text", autocomplete: "off", spellcheck: "false", "aria-label": "Terminal command" });
  const ctrlC = el("button", { class: "term-drawer__ctrlc", type: "button", text: "Ctrl-C", title: "Interrupt the running command" });
  const form = el("form", { class: "term-drawer__line" }, [el("span", { class: "term-drawer__prompt", text: "$" }), input, ctrlC]);
  const body = el("div", { class: "term-drawer__body" }, [outputEl, form]);
  root.replaceChildren(
    el("div", { class: "term-drawer__bar" }, [tabsEl, newBtn, el("span", { class: "term-drawer__spacer" }), errorEl, toggleBtn]),
    body
  );

  let windowID = null;
  let terminals = [];
  let selected = null; // pane id (no "%")
  let open = true;
  let tabsKey = null; // last rendered tab data — unchanged polls don't rebuild (keeps clicks)
  let lastOutput = null;
  let gen = 0; // bumped on window switch; stale responses are dropped

  const base = () => `/api/sessions/${encodeURIComponent(windowID)}/terminals`;

  function setError(msg) {
    errorEl.textContent = msg || "";
  }

  function render() {
    const key = JSON.stringify([terminals, selected, open]);
    if (key !== tabsKey) {
      tabsKey = key;
      tabsEl.replaceChildren(...terminals.map((t) => {
        const tab = el("button", {
          class: "term-tab" + (t.id === selected ? " is-selected" : ""),
          type: "button",
          title: `${t.cwd}${t.visible ? " — shown in the tmux drawer" : ""}`,
        }, [
          el("span", { class: "term-tab__mark" + (t.visible ? " is-visible" : "") }),
          document.createTextNode(t.name),
        ]);
        tab.addEventListener("click", () => {
          selected = t.id;
          open = true;
          lastOutput = null;
          render();
          refreshOutput();
        });
        return tab;
      }));
    }
    toggleBtn.textContent = open ? "Hide" : "Show";
    body.hidden = !open;
    form.hidden = !selected;
    if (!terminals.length) {
      outputEl.textContent = "No terminals in this window. Open one with New terminal.";
      lastOutput = null;
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
    if (!windowID || document.visibilityState !== "visible") return;
    const g = gen;
    try {
      const list = await call("GET", base());
      if (g !== gen) return;
      terminals = list || [];
      if (!terminals.some((t) => t.id === selected)) {
        selected = (terminals.find((t) => t.visible) || terminals[0] || {}).id || null;
        lastOutput = null;
      }
      setError("");
      render();
    } catch (err) {
      if (g === gen) setError(err.message);
    }
  }

  async function refreshOutput() {
    if (!windowID || !selected || !open || document.visibilityState !== "visible") return;
    const g = gen, pane = selected;
    try {
      const data = await call("GET", `${base()}/${pane}/output`);
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
    if (!selected) return;
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
    if (!selected) return;
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

  toggleBtn.addEventListener("click", () => {
    open = !open;
    render();
    if (open) refreshOutput();
  });

  document.addEventListener("visibilitychange", () => { refreshList(); refreshOutput(); });
  setInterval(refreshList, TERM_LIST_MS);
  setInterval(refreshOutput, TERM_OUTPUT_MS);

  return {
    setWindow(id) {
      if (id === windowID) return;
      gen++;
      windowID = id;
      terminals = [];
      selected = null;
      lastOutput = null;
      outputEl.textContent = "";
      input.value = "";
      setError("");
      root.hidden = !id;
      render();
      refreshList().then(refreshOutput);
    },
  };
}
