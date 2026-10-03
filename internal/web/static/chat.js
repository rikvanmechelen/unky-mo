// Chat-style live transcript + prompt input. No build step, no framework —
// same convention as app.js. Each SSE "transcript" event is the *exact*
// original JSONL line (see internal/web/transcript.go) — this file owns all
// interpretation of that schema; the backend stays schema-agnostic.
// STATUS, el, statusSquare and renderUsage come from common.js.

function windowIDFromURL() {
  return new URLSearchParams(location.search).get("window"); // e.g. "@5"
}

function isMetaContent(text) {
  return text.startsWith("<local-command") || text.startsWith("<command");
}

// relativize strips a leading project cwd off an absolute path, e.g.
// "/Users/rik/workspace/unky-mo/internal/web/chat.js" with base
// "/Users/rik/workspace/unky-mo" becomes "internal/web/chat.js". Falls back
// to the original path if it isn't under base.
function relativize(absPath, base) {
  if (!base || !absPath.startsWith(base)) return absPath;
  const rel = absPath.slice(base.length).replace(/^\/+/, "");
  return rel || absPath;
}

// toolSummaryDetail picks a short, human-readable detail to show next to a
// tool's name in its card summary: Bash-style tools carry a "description"
// input field; file-editing tools (Edit/Write/Read/...) carry "file_path"
// instead, shown relative to the turn's project cwd.
function toolSummaryDetail(block, cwd) {
  const input = block.input || {};
  if (typeof input.description === "string") return { text: input.description, mono: false };
  if (typeof input.file_path === "string") return { text: relativize(input.file_path, cwd), mono: true };
  return null;
}

// renderQuestionBanner builds the pending-question banner's content for a
// given tool name + input (already a parsed JS value — /api/state's JSON
// response embeds it verbatim, same as Go's json.RawMessage does on the wire
// — not a string to re-parse). The AskUserQuestion shape
// ({questions:[{question,header,options:[{label,description}]}]}) is
// rendered readably; anything else (a future interactive tool, or a shape
// that doesn't match) falls back to pretty-printed JSON so something is
// still visible instead of nothing.
function renderQuestionBanner(tool, input) {
  const frag = document.createDocumentFragment();
  frag.appendChild(el("div", { class: "question-banner__tool", text: tool }));

  if (input && Array.isArray(input.questions)) {
    for (const q of input.questions) {
      if (q.header) frag.appendChild(el("div", { class: "question-banner__tool", text: q.header }));
      frag.appendChild(el("div", { class: "question-banner__question", text: q.question || "" }));
      if (Array.isArray(q.options)) {
        const list = el("ol", { class: "question-banner__options" }, q.options.map((o, i) =>
          el("li", { text: o.label + (o.description ? " — " + o.description : "") })
        ));
        frag.appendChild(list);
      }
    }
    return frag;
  }

  frag.appendChild(el("pre", { text: JSON.stringify(input, null, 2) }));
  return frag;
}

// renderDiff turns an Edit tool's structuredPatch (array of unified-diff
// hunks, each with a "lines" array of already-prefixed +/-/space strings)
// into colored diff rows — no diffing algorithm needed, the hunks are
// already computed.
function renderDiff(structuredPatch) {
  const wrap = el("div", { class: "tool-card__diff" });
  let ln = null;
  for (const hunk of structuredPatch || []) {
    ln = hunk.newStart;
    for (const line of hunk.lines || []) {
      const sign = line[0];
      const text = line.slice(1);
      const cls = sign === "+" ? "is-add" : sign === "-" ? "is-del" : "";
      const row = el("div", { class: "diff-line" + (cls ? " " + cls : "") }, [
        el("span", { class: "diff-line__ln", text: sign === "-" ? "" : String(ln) }),
        el("span", { class: "diff-line__sign", text: sign === " " ? "" : sign }),
        el("span", { class: "diff-line__text", text }),
      ]);
      wrap.appendChild(row);
      if (sign !== "-") ln++;
    }
  }
  return wrap;
}


// navGroups groups the state file's live sessions by project for the left
// nav, keeping state-file order; external (stray) sessions come last.
function navGroups(projects) {
  const groups = [];
  const byKey = new Map();
  const rows = (projects || []).filter((p) => p.window_id && p.session_id);
  for (const p of [...rows.filter((p) => p.section !== "external"), ...rows.filter((p) => p.section === "external")]) {
    // Concurrent siblings are named "proj [2]" — group them under "proj".
    const key = p.section === "external" ? "External" : (p.parent || p.name.replace(/ \[\d+\]$/, ""));
    let g = byKey.get(key);
    if (!g) {
      g = { project: key, items: [] };
      byKey.set(key, g);
      groups.push(g);
    }
    g.items.push(p);
  }
  return groups;
}

function main() {
  const transcript = document.getElementById("transcript");
  const transcriptScroll = document.getElementById("transcript-scroll");
  const chatTitle = document.getElementById("chat-title");
  const chatMeta = document.getElementById("chat-meta");
  const statusBadge = document.getElementById("status-badge");
  const composer = document.getElementById("composer");
  const promptInput = document.getElementById("prompt-input");
  const sendBtn = document.getElementById("send-btn");
  const sendError = document.getElementById("send-error");
  const permissionBanner = document.getElementById("permission-banner");
  const questionBanner = document.getElementById("question-banner");
  const chatNotice = document.getElementById("chat-notice");
  const stopBtn = document.getElementById("stop-btn");
  const sessionNav = document.getElementById("session-nav");
  const usageBox = document.getElementById("usage");

  // Per-session state — reset by resetSession when the nav switches to
  // another window in place.
  let windowID = null;
  const seen = new Set(); // dedup by uuid — EventSource reconnects replay the full backlog
  const toolCards = new Map(); // tool_use_id -> {card, body} — persists across messages
  let busyRow = null;

  // The SSE stream is bound server-side to whichever Claude session ID the
  // window had when it connected. /clear (or /new) starts a fresh session —
  // new ID, new JSONL file — in the same window, so the status poll below
  // watches session_id and, on a change, wipes the log and reconnects.
  let es = null;
  let streamSessionID = null;

  // A message typed while a just-launched session is still starting is
  // queued, then sent the first time the session reads idle (pollStatus).
  let queued = null;

  // Lifecycle around launches from the dashboard (?new=1): the window exists
  // before the main TUI has attributed a Claude session to it, so for a few
  // seconds there's no state row yet. Once one has been seen, its
  // disappearance means the session ended (stopped here, /exit, or killed).
  const STARTUP_GRACE_MS = 20000;
  let starting = false;
  let openedAt = 0;
  let everSeen = false;
  let ended = false;

  // Auto-scroll only follows new content if the viewport was already pinned
  // to the bottom — if you've scrolled up to read something, new messages
  // (or a tool card expanding) must never yank you back down.
  function isPinned() {
    return transcriptScroll.scrollHeight - transcriptScroll.scrollTop - transcriptScroll.clientHeight < 40;
  }
  function scrollToBottom() {
    requestAnimationFrame(() => { transcriptScroll.scrollTop = transcriptScroll.scrollHeight; });
  }
  function withPin(fn) {
    const pinned = isPinned();
    fn();
    if (pinned) scrollToBottom();
  }

  function appendBubble(kind, text) {
    if (kind === "meta") {
      transcript.appendChild(el("div", { class: "msg-system" }, [
        el("span", { class: "msg-system__text", text }),
        el("span", { class: "msg-system__rule" }),
      ]));
      return;
    }
    if (kind === "user") {
      transcript.appendChild(el("div", { class: "msg-user" }, [
        el("span", { class: "msg-user__label", text: "You" }),
        el("span", { class: "msg-user__text", text }),
      ]));
      return;
    }
    transcript.appendChild(el("div", { class: "msg-assistant", text }));
  }

  function appendToolUse(block, cwd) {
    const detail = toolSummaryDetail(block, cwd);
    const body = el("div", { class: "tool-card__body" });
    body.style.display = "none";
    const action = el("span", { class: "tool-card__action", text: "Show" });

    const card = el("div", { class: "tool-card" }, [
      el("button", { class: "tool-card__toggle", type: "button" }, [
        el("span", { class: "tool-card__sq" }),
        el("span", { class: "tool-card__name", text: block.name }),
        ...(detail ? [el("span", { class: "tool-card__detail" + (detail.mono ? " is-mono" : ""), text: detail.text })] : []),
        action,
      ]),
      body,
    ]);
    card.querySelector(".tool-card__toggle").addEventListener("click", () => {
      withPin(() => {
        const open = body.style.display !== "none";
        body.style.display = open ? "none" : "flex";
        action.textContent = open ? "Show" : "Hide";
      });
    });

    toolCards.set(block.id, {
      card, body,
      input: el("pre", { text: JSON.stringify(block.input, null, 2) }),
    });
    transcript.appendChild(card);
  }

  function fillToolResult(block, toolUseResult) {
    const entry = toolCards.get(block.tool_use_id);
    if (!entry) return; // result for a tool_use we never saw (e.g. truncated backlog) — drop silently
    const { card, body } = entry;

    if (block.is_error || (toolUseResult && typeof toolUseResult === "object" && toolUseResult.toolDenialKind)) {
      card.classList.add("is-error");
    }

    // A Write that creates a file carries an empty structuredPatch — show its
    // input/output instead of an empty diff box.
    if (toolUseResult && typeof toolUseResult === "object" && toolUseResult.structuredPatch?.length) {
      body.appendChild(renderDiff(toolUseResult.structuredPatch));
      return;
    }

    const text = typeof toolUseResult === "string"
      ? toolUseResult
      : typeof block.content === "string"
        ? block.content
        : JSON.stringify(block.content);

    body.appendChild(el("div", { class: "tool-card__field" }, [
      el("span", { class: "tool-card__field-label", text: "Input" }),
      entry.input,
    ]));
    body.appendChild(el("div", { class: "tool-card__field is-output" }, [
      el("span", { class: "tool-card__field-label", text: "Output" }),
      el("pre", { text }),
    ]));
  }

  function renderMessage(msg) {
    if (msg.type === "assistant") {
      for (const block of msg.message?.content || []) {
        if (block.type === "text") appendBubble("assistant", block.text);
        else if (block.type === "tool_use") appendToolUse(block, msg.cwd);
        // thinking blocks: skipped — empty in practice, nothing to show
      }
      return;
    }

    if (msg.type === "user") {
      const content = msg.message?.content;
      if (typeof content === "string") {
        appendBubble(isMetaContent(content) ? "meta" : "user", content);
        return;
      }
      for (const block of content || []) {
        if (block.type === "tool_result") {
          fillToolResult(block, msg.toolUseResult);
        }
      }
    }
  }

  function clearTranscript() {
    if (es) { es.close(); es = null; }
    streamSessionID = null;
    seen.clear();
    toolCards.clear();
    busyRow = null;
    transcript.replaceChildren();
  }

  function connectTranscript(sessionID) {
    clearTranscript();
    streamSessionID = sessionID;
    const stream = new EventSource(`/api/transcript/${encodeURIComponent(windowID)}`);
    es = stream;
    stream.addEventListener("transcript", (e) => {
      if (es !== stream) return; // a late event from a stream we've since replaced
      const msg = JSON.parse(e.data);
      if (seen.has(msg.uuid)) return;
      seen.add(msg.uuid);
      withPin(() => renderMessage(msg));
    });
  }

  async function sendPrompt(text) {
    sendError.textContent = "";
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/prompt`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        sendError.textContent = data.error || `request failed (${res.status})`;
        return false;
      }
      return true;
    } catch (err) {
      sendError.textContent = String(err);
      return false;
    }
  }

  composer.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!windowID) return;
    const text = promptInput.value.trim();
    if (!text) return;
    if (starting && !everSeen) {
      queued = text;
      promptInput.value = "";
      sendError.textContent = "";
      pollStatus();
      return;
    }
    if (await sendPrompt(text)) promptInput.value = "";
  });

  function setBusyRow(show) {
    if (show && !busyRow) {
      busyRow = el("div", { class: "chat-busy" }, [
        el("span", { class: "chat-busy__sq" }),
        document.createTextNode("Working"),
      ]);
      withPin(() => transcript.appendChild(busyRow));
    } else if (!show && busyRow) {
      const row = busyRow;
      busyRow = null;
      row.remove();
    }
  }

  function setNotice(text) {
    chatNotice.textContent = text || "";
    chatNotice.style.display = text ? "block" : "none";
  }

  function lockSend(label) {
    sendBtn.disabled = true;
    sendBtn.classList.add("is-locked");
    sendBtn.textContent = label;
  }

  function markEnded() {
    ended = true;
    if (es) { es.close(); es = null; }
    setBusyRow(false);
    setNotice("Session ended. The tmux window is closed.");
    stopBtn.style.display = "none";
    lockSend("Session ended");
  }

  // resetSession points the view at another window (initial load, a nav
  // click, or back/forward) — everything per-session starts over.
  function resetSession(id, isStarting) {
    windowID = id;
    clearTranscript();
    queued = null;
    starting = isStarting;
    openedAt = Date.now();
    everSeen = false;
    ended = false;
    promptInput.value = "";
    sendError.textContent = "";
    setNotice(id ? "" : "Pick a session on the left.");
    permissionBanner.style.display = "none";
    questionBanner.style.display = "none";
    stopBtn.style.display = "none";
    statusBadge.replaceChildren();
    chatTitle.textContent = "—";
    chatMeta.replaceChildren();
    document.title = "Unky Mo — Chat";
    if (!id) lockSend("No session");
    pollStatus();
  }

  stopBtn.addEventListener("click", async () => {
    const id = windowID;
    if (await stopSession(id, chatMeta.firstChild ? chatMeta.firstChild.textContent : id) && id === windowID) markEnded();
  });

  function switchTo(id, push) {
    if (id === windowID) return;
    if (push) history.pushState({}, "", `/chat?window=${encodeURIComponent(id)}`);
    resetSession(id, false);
  }

  window.addEventListener("popstate", () => {
    const id = windowIDFromURL();
    if (id !== windowID) resetSession(id, new URLSearchParams(location.search).has("new"));
  });

  // The nav's links are rebuilt only when the set of sessions changes; on a
  // plain status/label change the existing rows are updated in place, so a
  // click that straddles the 2s poll isn't swallowed by a replaced node.
  let navShape = null;
  const navRows = new Map(); // window_id -> {link, sq, branch, status}

  function fillNavRow(row, p) {
    const meta = STATUS[p.status] || STATUS.none;
    const current = p.window_id === windowID;
    row.link.classList.toggle("is-current", current);
    row.link.title = `${p.name} · ${p.window_name || p.window_id}`;
    row.sq.replaceWith(row.sq = statusSquare(meta.navSq || meta.sq, meta.ring, "", current ? "#fff" : "#000"));
    row.branch.textContent = p.branch || p.window_name || p.window_id;
    row.status.textContent = meta.short;
  }

  function newNavRow(p) {
    const row = {
      link: el("a", { class: "nav-session plain", href: `/chat?window=${encodeURIComponent(p.window_id)}` }),
      sq: el("span"),
      branch: el("span", { class: "nav-session__branch" }),
      status: el("span", { class: "nav-session__status" }),
    };
    row.link.append(row.sq, row.branch, row.status);
    row.link.addEventListener("click", (e) => {
      // Modified clicks keep their browser meaning (new tab/window).
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      switchTo(p.window_id, true);
    });
    return row;
  }

  function renderSessionNav(projects) {
    const groups = navGroups(projects);
    const shape = JSON.stringify(groups.map((g) => [g.project, g.items.map((p) => p.window_id)]));
    if (shape !== navShape) {
      navShape = shape;
      navRows.clear();
      if (!groups.length) {
        sessionNav.replaceChildren(el("div", { class: "chat-nav__empty", text: "No live sessions." }));
        return;
      }
      sessionNav.replaceChildren(...groups.map((g) => el("div", { class: "nav-group" }, [
        el("div", { class: "nav-group__project", text: g.project }),
        ...g.items.map((p) => {
          const row = newNavRow(p);
          navRows.set(p.window_id, row);
          return row.link;
        }),
      ])));
    }
    for (const g of groups) for (const p of g.items) fillNavRow(navRows.get(p.window_id), p);
  }

  async function pollStatus() {
    try {
      const res = await fetch("/api/state");
      const data = await res.json();
      renderSessionNav(data.projects);
      renderUsage(usageBox, data.usage, { compact: true });
      if (!windowID || ended) return;

      const p = (data.projects || []).find((pr) => pr.window_id === windowID && pr.session_id);
      if (!p) {
        if (everSeen) { markEnded(); return; }
        if (!starting) setNotice("No session is running in this window.");
        else if (Date.now() - openedAt < STARTUP_GRACE_MS) setNotice("Starting session…");
        else setNotice("The session hasn't come up yet. It may be waiting on a prompt in tmux (for example, trusting a new folder).");
      } else {
        everSeen = true;
        setNotice("");
      }
      stopBtn.style.display = p ? "" : "none";
      const status = p ? p.status : "none";
      if (p && p.session_id !== streamSessionID) connectTranscript(p.session_id);
      const meta = STATUS[status] || STATUS.none;

      statusBadge.replaceChildren(statusSquare(meta.sq, meta.ring), document.createTextNode(meta.label));
      statusBadge.style.background = meta.bg;
      statusBadge.style.boxShadow = `inset 0 0 0 1px ${meta.border}`;

      if (p) {
        chatTitle.textContent = p.name || windowID;
        document.title = `Unky Mo — ${p.name || windowID}`;
        chatMeta.replaceChildren(
          el("span", { text: p.window_name || windowID }),
          el("span", { text: p.branch || "" })
        );
      }

      permissionBanner.style.display = status === "permission" ? "block" : "none";
      if (status === "question" && p && p.pending_question_tool) {
        questionBanner.replaceChildren(renderQuestionBanner(p.pending_question_tool, p.pending_question_input));
        questionBanner.style.display = "flex";
      } else if (status === "question") {
        // Detected via `claude agents --json` rather than the PreToolUse
        // hook, so the question's text/options were never captured.
        questionBanner.replaceChildren(
          el("div", { class: "question-banner__tool", text: "Waiting for your answer" }),
          el("div", { class: "question-banner__question", text: "Claude is showing a question in the terminal that couldn't be captured here. Reply with an option number or your answer." })
        );
        questionBanner.style.display = "flex";
      } else {
        questionBanner.style.display = "none";
      }

      setBusyRow(status === "active");

      if (queued && status === "idle") {
        const text = queued;
        queued = null;
        if (!(await sendPrompt(text))) promptInput.value = text; // give it back to retry
      }
      if (queued) setNotice(`Starting session… your message will be sent when it's ready: “${queued}”`);

      const waitingToStart = starting && !everSeen;
      const canSend = status === "idle" || status === "question" || (waitingToStart && !queued);
      sendBtn.disabled = !canSend;
      sendBtn.classList.toggle("is-locked", !canSend);
      sendBtn.textContent = waitingToStart ? (queued ? "Queued" : "Send when ready")
        : canSend ? "Send" : !p ? "No session" : "Claude is working…";
      promptInput.placeholder = status === "question"
        ? "Type a number or your answer…"
        : p ? `Message ${p.name}` : "Message this session";
    } catch (err) {
      // transient — leave the last known status showing
    }
  }

  resetSession(windowIDFromURL(), new URLSearchParams(location.search).has("new"));
  setInterval(pollStatus, 2000);
}

main();
