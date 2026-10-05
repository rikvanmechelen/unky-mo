// Subagents a session spawned with the Agent tool, from
// /api/sessions/{windowID}/subagents (see handlers_subagents.go):
//   - a strip above the composer listing the ones still running, since a
//     session waiting on a background agent otherwise just reads "idle";
//   - a status line under each Agent tool card in the transcript;
//   - a dialog that live-streams one agent's own transcript.
// el comes from common.js; formatDuration and createTranscriptView from
// chat.js (only called after it has loaded).

const SUBAGENTS_POLL_MS = 2000;

// subagentElapsed is how long the agent has been at it: up to now while
// it runs, up to its last activity once it's done.
function subagentElapsed(a) {
  if (!a.started) return "";
  const end = a.running ? Date.now() : Date.parse(a.last_activity || a.started);
  return formatDuration(end - Date.parse(a.started));
}

// subagentStateLabel names an agent's state for the card and dialog.
function subagentStateLabel(a) {
  switch (a.state) {
    case "running": return "Running";
    case "waiting": return "Waiting on its background work";
    case "done": return "Done";
    default: return a.state ? a.state[0].toUpperCase() + a.state.slice(1) : "";
  }
}

// subagentProgress is "2m 13s · 14 tools · WebFetch https://…" (the last
// tool only while it runs).
function subagentProgress(a) {
  const parts = [subagentElapsed(a)];
  if (a.tool_uses) parts.push(`${a.tool_uses} tool${a.tool_uses === 1 ? "" : "s"}`);
  if (a.state === "running" && a.last_tool) parts.push(a.last_detail ? `${a.last_tool} ${a.last_detail}` : a.last_tool);
  return parts.filter(Boolean).join(" · ");
}

function createSubagents(strip, { bashChanges, onOpenDiff } = {}) {
  let windowID = null;
  let gen = 0; // bumped on every switch so a late poll is dropped
  let timer = null;
  let polling = false;
  let agents = [];
  const byToolUse = new Map(); // tool_use_id -> agent
  const cards = new Map(); // tool_use_id -> {line, state, progress, sq}
  let stripShape = null;
  const stripRows = new Map(); // agent id -> {progress}

  // ── Dialog with one agent's live transcript ─────────────────────────
  const dlgTitle = el("span", { class: "agent-dialog__title" });
  const dlgMeta = el("span", { class: "agent-dialog__meta" });
  const dlgClose = el("button", { class: "diff-dialog__close", type: "button", text: "Close" });
  const dlgInner = el("div", { class: "chat-transcript__inner" });
  const dlgBody = el("div", { class: "diff-dialog__body agent-dialog__body" }, [dlgInner]);
  const dialog = el("dialog", { class: "diff-dialog agent-dialog", "aria-label": "Agent transcript" }, [
    el("div", { class: "diff-dialog__head" }, [
      el("div", { class: "agent-dialog__heading" }, [dlgTitle, dlgMeta]),
      el("span", { class: "diff-dialog__spacer" }),
      dlgClose,
    ]),
    dlgBody,
  ]);
  document.body.appendChild(dialog);
  const view = createTranscriptView(dlgInner, dlgBody, { userLabel: "Task", bashChanges, onOpenDiff });
  let dlgStream = null;
  let dlgAgent = null; // id of the agent shown

  dlgClose.addEventListener("click", () => dialog.close());
  dialog.addEventListener("click", (e) => { if (e.target === dialog) dialog.close(); }); // backdrop
  dialog.addEventListener("close", () => {
    if (dlgStream) { dlgStream.close(); dlgStream = null; }
    dlgAgent = null;
    view.clear();
  });

  function fillDialogHead() {
    const a = agents.find((x) => x.id === dlgAgent);
    if (!a) return;
    dlgTitle.textContent = a.description || a.type || "Agent";
    dlgMeta.textContent = [a.type, subagentStateLabel(a).toLowerCase(), subagentProgress(a)].filter(Boolean).join(" · ");
  }

  function open(id) {
    if (!windowID) return;
    if (dlgStream) dlgStream.close();
    view.clear();
    dlgAgent = id;
    fillDialogHead();
    const stream = new EventSource(`/api/sessions/${encodeURIComponent(windowID)}/subagents/${encodeURIComponent(id)}/transcript`);
    dlgStream = stream;
    stream.addEventListener("transcript", (e) => {
      if (dlgStream !== stream) return;
      view.onLine(JSON.parse(e.data));
    });
    if (!dialog.open) dialog.showModal();
    view.scrollToBottom();
  }

  // ── Agent tool cards in the session transcript ──────────────────────
  function fillCard(c, a) {
    c.line.hidden = !a;
    if (!a) return;
    c.line.classList.toggle("is-running", a.running);
    c.line.classList.toggle("is-stopped", !a.running && a.state !== "done");
    c.state.textContent = subagentStateLabel(a);
    c.progress.textContent = subagentProgress(a);
  }

  // decorateCard adds a status line under an Agent tool card; it's filled
  // in once the listing names the agent that call spawned.
  function decorateCard(entry, block) {
    if (!isAgentTool(block.name)) return;
    const c = {
      sq: el("span", { class: "agent-line__sq" }),
      state: el("span", { class: "agent-line__state" }),
      progress: el("span", { class: "agent-line__progress" }),
      open: el("button", { class: "agent-line__open", type: "button", text: "Transcript" }),
    };
    c.line = el("div", { class: "agent-line" }, [c.sq, c.state, c.progress, c.open]);
    c.open.addEventListener("click", () => {
      const a = byToolUse.get(block.id);
      if (a) open(a.id);
    });
    entry.card.insertBefore(c.line, entry.body);
    cards.set(block.id, c);
    fillCard(c, byToolUse.get(block.id));
  }

  // ── Strip of running agents above the composer ──────────────────────
  function renderStrip() {
    const running = agents.filter((a) => a.running);
    const shape = running.map((a) => a.id).join(",");
    if (shape !== stripShape) {
      stripShape = shape;
      stripRows.clear();
      strip.replaceChildren(...running.map((a) => {
        const row = {
          state: el("span", { class: "agent-strip__state" }),
          progress: el("span", { class: "agent-strip__progress" }),
        };
        const btn = el("button", { class: "agent-strip__row", type: "button", title: "Show this agent's transcript" }, [
          el("span", { class: "agent-strip__sq", "aria-hidden": "true" }),
          el("span", { class: "agent-strip__desc", text: a.description || a.type || "Agent" }),
          ...(a.type ? [el("span", { class: "agent-strip__type", text: a.type })] : []),
          row.state,
          row.progress,
        ]);
        btn.addEventListener("click", () => open(a.id));
        stripRows.set(a.id, row);
        return btn;
      }));
    }
    for (const a of running) {
      const row = stripRows.get(a.id);
      row.state.textContent = a.state === "waiting" ? "waiting on its background work" : "";
      row.progress.textContent = subagentProgress(a);
    }
    strip.hidden = running.length === 0;
  }

  function render() {
    renderStrip();
    for (const [toolUseID, c] of cards) {
      if (!c.line.isConnected) { cards.delete(toolUseID); continue; } // a re-rendered branch
      fillCard(c, byToolUse.get(toolUseID));
    }
    if (dlgAgent) fillDialogHead();
  }

  function setAgents(list) {
    agents = list;
    byToolUse.clear();
    for (const a of agents) if (a.tool_use_id) byToolUse.set(a.tool_use_id, a);
    render();
  }

  async function poll() {
    const g = gen;
    const id = windowID;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/subagents`);
      if (g !== gen) return;
      setAgents(res.ok ? await res.json() : []);
    } catch (_) {
      // transient — keep the last listing
    }
    if (g === gen) timer = setTimeout(poll, SUBAGENTS_POLL_MS);
  }

  function start() {
    if (polling || !windowID) return;
    polling = true;
    gen++;
    poll();
  }

  function stop() {
    polling = false;
    gen++;
    clearTimeout(timer);
  }

  // Elapsed times tick between polls.
  setInterval(() => { if (agents.some((a) => a.running)) render(); }, 1000);

  return {
    decorateCard,
    // setWindow points it at another window; null stops it.
    setWindow(id) {
      stop();
      windowID = id;
      cards.clear();
      if (dialog.open) dialog.close();
      setAgents([]);
      start();
    },
    // setAvailable pauses polling while the window has no live session.
    setAvailable(ok) {
      if (ok) start();
      else if (polling) { stop(); setAgents([]); }
    },
  };
}
