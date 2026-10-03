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

// tagText returns the trimmed inner text of the first <tag>…</tag> in text,
// or null when the tag is absent.
function tagText(text, tag) {
  const m = text.match(new RegExp(`<${tag}>([\\s\\S]*?)</${tag}>`));
  return m ? m[1].trim() : null;
}

function stripAnsi(text) {
  return text.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");
}

// describeUserString classifies a user line's string content: what a human
// typed ({kind: "user"}), a meta row ({kind: "meta"}), or noise to hide
// ({kind: "skip"}). Slash commands, their output and background-task
// notifications all arrive as "user" lines in the JSONL but aren't yours.
// parseTeammateMessages extracts the <teammate-message …>…</teammate-message>
// blocks agent-team teammates send their lead (as plain user lines with no
// other marker). Returns [] when text has none.
function parseTeammateMessages(text) {
  const items = [];
  const re = /<teammate-message\s+([^>]*)>([\s\S]*?)<\/teammate-message>/g;
  let m;
  while ((m = re.exec(text))) {
    const attrs = {};
    for (const a of m[1].matchAll(/([\w-]+)="([^"]*)"/g)) attrs[a[1]] = a[2];
    items.push({ id: attrs.teammate_id || "teammate", color: attrs.color || "", summary: attrs.summary || "", body: m[2].trim() });
  }
  return items;
}

function describeUserString(msg, text) {
  if (msg.isCompactSummary) return { kind: "meta", text: "Conversation compacted" };
  if (text.includes("<teammate-message")) {
    const items = parseTeammateMessages(text);
    if (items.length) return { kind: "teammates", items };
  }
  if (msg.isMeta) return { kind: "skip" }; // command caveat, system nudges
  if (msg.origin?.kind === "task-notification" || text.startsWith("<task-notification>")) {
    return { kind: "meta", text: tagText(text, "summary") || "Background task finished" };
  }
  const name = tagText(text, "command-name");
  if (name !== null) {
    const args = tagText(text, "command-args");
    return { kind: "meta", text: args ? `${name} ${args}` : name };
  }
  const out = tagText(text, "local-command-stdout") ?? tagText(text, "local-command-stderr");
  if (out !== null) {
    const clean = stripAnsi(out).trim();
    return clean ? { kind: "meta", text: clean } : { kind: "skip" };
  }
  if (isMetaContent(text)) return { kind: "meta", text };
  return { kind: "user", text };
}

// Claude Code shows a random past-tense verb when a turn ends but never
// records it, so pick one per turn from the entry's uuid — stable across
// reloads.
const TURN_VERBS = ["Baked", "Brewed", "Churned", "Cogitated", "Cooked", "Crunched", "Simmered", "Worked"];

function turnVerb(uuid) {
  let h = 0;
  for (const ch of uuid || "") h = (h * 31 + ch.charCodeAt(0)) | 0;
  return TURN_VERBS[Math.abs(h) % TURN_VERBS.length];
}

// formatDuration renders milliseconds as "17s", "11m 3s" or "1h 4m".
function formatDuration(ms) {
  const total = Math.max(0, Math.round(ms / 1000));
  const h = Math.floor(total / 3600), m = Math.floor((total % 3600) / 60), sec = total % 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${sec}s`;
  return `${sec}s`;
}

// describeSystem turns a forwarded system line (see forwardedSystemSubtypes
// in transcript.go) into meta-row text, or null to show nothing.
function describeSystem(msg) {
  switch (msg.subtype) {
    case "turn_duration": {
      const done = msg.timestamp
        ? " · done " + new Date(msg.timestamp).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
        : "";
      return `${turnVerb(msg.uuid)} for ${formatDuration(msg.durationMs || 0)}${done}`;
    }
    case "scheduled_task_fire":
      return msg.content || null;
    case "local_command": {
      const out = tagText(msg.content || "", "local-command-stdout") ?? tagText(msg.content || "", "local-command-stderr");
      const clean = out === null ? "" : stripAnsi(out).trim();
      return clean || null;
    }
    default:
      return null;
  }
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
  // A Bash call without a description: show the command itself.
  if (typeof input.command === "string" && input.command.trim()) {
    const first = input.command.trim().split("\n")[0];
    return { text: first.length > 100 ? first.slice(0, 99) + "…" : first, mono: true };
  }
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
//
// onPick(n), when given, makes each option a button that answers with its
// 1-based number — the same as typing the number into the composer.
function renderQuestionBanner(tool, input, onPick) {
  const frag = document.createDocumentFragment();
  frag.appendChild(el("div", { class: "question-banner__tool", text: tool }));

  if (input && Array.isArray(input.questions)) {
    for (const q of input.questions) {
      if (q.header) frag.appendChild(el("div", { class: "question-banner__tool", text: q.header }));
      frag.appendChild(el("div", { class: "question-banner__question", text: q.question || "" }));
      if (Array.isArray(q.options)) {
        const list = el("ol", { class: "question-banner__options" }, q.options.map((o, i) => {
          const text = o.label + (o.description ? " — " + o.description : "");
          if (!onPick) return el("li", { text });
          const btn = el("button", { class: "question-banner__option", type: "button", text });
          btn.addEventListener("click", () => onPick(i + 1));
          return el("li", {}, [btn]);
        }));
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
  (structuredPatch || []).forEach((hunk, i) => {
    // Mark skipped, unchanged lines between hunks.
    if (i > 0) wrap.appendChild(el("div", { class: "diff-line diff-gap" }, [el("span", { class: "diff-line__ln", text: "⋯" })]));
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
  });
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
  const shell = document.getElementById("chat-shell");
  const filesEl = document.getElementById("files-pane");
  const filesPane = createFilesPane(filesEl);
  const drawer = createTerminalDrawer(document.getElementById("term-drawer"));
  const spinner = createSpinner(document.getElementById("spinner"));

  // Per-session state — reset by resetSession when the nav switches to
  // another window in place.
  let windowID = null;
  // The conversation is a tree (parentUuid links): an edited or resubmitted
  // prompt leaves the old branch in the file. Only the active branch — the
  // chain from the newest line back to the root — is shown, like Claude
  // Code itself. nodes holds every line's parent (forwarded messages and
  // the backend's "link" stubs for lines it doesn't send); messages keeps
  // the renderable ones in arrival order for re-rendering on a branch switch.
  const nodes = new Map(); // uuid -> parent uuid (or null); doubles as the reconnect dedupe
  let messages = [];
  let leaf = null;
  const toolCards = new Map(); // tool_use_id -> {card, body, ...} — persists across messages
  let previewed = null; // the toolCards entry currently auto-opened by previewToolCard

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
    transcript.appendChild(el("div", { class: "msg-assistant" }, [renderMarkdown(text)]));
  }

  // Teammate colours (from the team config) mapped onto the MoMA palette.
  const TEAMMATE_COLORS = {
    blue: "var(--blue)", green: "var(--green)", yellow: "var(--yellow)", red: "var(--red)",
    purple: "#753BBD", orange: "#FF8F1C", pink: "#E93CAC", cyan: "#00AFD7",
  };

  // appendTeammates renders each teammate message as a collapsible row —
  // the teammate, its summary, and the report as markdown on demand. An
  // idle notification is just a meta line.
  function appendTeammates(items) {
    for (const t of items) {
      let payload = null;
      try { payload = JSON.parse(t.body); } catch (_) { /* a markdown report */ }
      if (payload && payload.type === "idle_notification") {
        appendBubble("meta", `${t.id} is idle`);
        continue;
      }
      const body = el("div", { class: "msg-teammate__body msg-assistant" });
      body.hidden = true;
      const action = el("span", { class: "tool-card__action", text: "Show" });
      const sq = el("span", { class: "tool-card__sq" });
      sq.style.background = TEAMMATE_COLORS[t.color] || "var(--gray-767)";
      const toggle = el("button", { class: "tool-card__toggle", type: "button" }, [
        sq,
        el("span", { class: "tool-card__name", text: t.id }),
        ...(t.summary ? [el("span", { class: "tool-card__detail", text: t.summary })] : []),
        action,
      ]);
      toggle.addEventListener("click", () => withPin(() => {
        if (body.hidden && !body.childNodes.length) body.appendChild(renderMarkdown(t.body));
        body.hidden = !body.hidden;
        action.textContent = body.hidden ? "Show" : "Hide";
      }));
      transcript.appendChild(el("div", { class: "tool-card msg-teammate" }, [toggle, body]));
    }
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
    const entry = {
      card, body, action, name: block.name,
      input: el("pre", { text: JSON.stringify(block.input, null, 2) }),
    };
    card.querySelector(".tool-card__toggle").addEventListener("click", () => {
      withPin(() => {
        // A click takes the card out of preview mode for good, so the next
        // preview doesn't collapse a card you chose to look at.
        if (previewed === entry) previewed = null;
        setCardOpen(entry, body.style.display === "none");
      });
    });

    toolCards.set(block.id, entry);
    transcript.appendChild(card);
  }

  function setCardOpen(entry, open, preview = false) {
    entry.body.style.display = open ? "flex" : "none";
    entry.action.textContent = open ? "Hide" : "Show";
    entry.card.classList.toggle("is-preview", open && preview);
  }

  // Like Claude Code's terminal, the most recent edit's diff (or command's
  // output) is shown open, in a height-capped preview, until a newer one
  // replaces it. A card you've toggled yourself is left alone.
  function previewToolCard(entry) {
    if (previewed && previewed !== entry) setCardOpen(previewed, false);
    previewed = entry;
    setCardOpen(entry, true, true);
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
      previewToolCard(entry);
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
    if (entry.name === "Bash" && text.trim()) previewToolCard(entry);
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

    if (msg.type === "system") {
      const text = describeSystem(msg);
      if (text) appendBubble("meta", text);
      return;
    }

    if (msg.type === "user") {
      const content = msg.message?.content;
      if (typeof content === "string") {
        const d = describeUserString(msg, content);
        if (d.kind === "teammates") appendTeammates(d.items);
        else if (d.kind !== "skip") appendBubble(d.kind, d.text);
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
    nodes.clear();
    messages = [];
    leaf = null;
    toolCards.clear();
    previewed = null;
    transcript.replaceChildren();
  }

  function connectTranscript(sessionID) {
    clearTranscript();
    streamSessionID = sessionID;
    const stream = new EventSource(`/api/transcript/${encodeURIComponent(windowID)}`);
    es = stream;
    stream.addEventListener("transcript", (e) => {
      if (es !== stream) return; // a late event from a stream we've since replaced
      onLine(JSON.parse(e.data));
    });
  }

  // parentOf follows a compaction boundary (a new root) back to the
  // conversation it summarises, so compacting doesn't hide earlier history.
  function parentOf(msg) {
    return msg.parentUuid ?? msg.logicalParentUuid ?? null;
  }

  function isToolResultOnly(msg) {
    const c = msg.type === "user" && msg.message?.content;
    return Array.isArray(c) && c.length > 0 && c.every((b) => b.type === "tool_result");
  }

  function onLine(msg) {
    if (!msg.uuid) {
      if (msg.type !== "link") withPin(() => renderMessage(msg));
      return;
    }
    if (nodes.has(msg.uuid)) return; // replayed by an EventSource reconnect
    const parent = parentOf(msg);
    nodes.set(msg.uuid, parent);
    const isLink = msg.type === "link";
    if (!isLink) messages.push(msg);

    if (leaf === null || parent === leaf) {
      // Continues the active branch.
      leaf = msg.uuid;
      if (!isLink) withPin(() => renderMessage(msg));
    } else if (isToolResultOnly(msg)) {
      // A parallel tool call's result hangs off an earlier assistant line;
      // it fills that call's card without moving the branch.
      withPin(() => renderMessage(msg));
    } else {
      // A new branch (edited/resubmitted prompt, rewind): show it instead.
      leaf = msg.uuid;
      withPin(rerenderActiveBranch);
    }
  }

  function rerenderActiveBranch() {
    const chain = new Set();
    for (let u = leaf; u && !chain.has(u); u = nodes.get(u)) chain.add(u);
    transcript.replaceChildren();
    toolCards.clear();
    previewed = null;
    for (const m of messages) {
      if (chain.has(m.uuid) || (isToolResultOnly(m) && chain.has(parentOf(m)))) renderMessage(m);
    }
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

  function setNotice(text) {
    chatNotice.textContent = text || "";
    chatNotice.style.display = text ? "block" : "none";
  }

  function lockSend(label) {
    sendBtn.disabled = true;
    sendBtn.classList.add("is-locked");
    sendBtn.textContent = label;
  }

  function setBadge(key) {
    const meta = STATUS[key] || STATUS.none;
    statusBadge.replaceChildren(statusSquare(meta.sq, meta.ring), document.createTextNode(meta.label));
    statusBadge.style.background = meta.bg;
    statusBadge.style.boxShadow = `inset 0 0 0 1px ${meta.border}`;
  }

  // The question banner is rebuilt only when the question changes, so a
  // click on an option that straddles the 2s poll isn't lost.
  let questionKey = null;

  function showQuestion(key, build) {
    if (key !== questionKey) {
      questionKey = key;
      questionBanner.replaceChildren(...build());
    }
    questionBanner.style.display = "flex";
  }

  function hideBanners() {
    permissionBanner.style.display = "none";
    questionBanner.style.display = "none";
    questionKey = null;
  }

  async function pickOption(n) {
    questionBanner.querySelectorAll("button").forEach((b) => { b.disabled = true; });
    if (!(await sendPrompt(String(n)))) {
      questionBanner.querySelectorAll("button").forEach((b) => { b.disabled = false; });
    }
  }

  function markEnded() {
    ended = true;
    if (es) { es.close(); es = null; }
    spinner.setActive(false);
    hideBanners();
    setBadge("ended");
    setNotice("Session ended. The tmux window is closed.");
    stopBtn.style.display = "none";
    lockSend("Session ended");
    filesPane.setAvailable(false, "Session ended.");
    drawer.setAvailable(false);
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
    hideBanners();
    stopBtn.style.display = "none";
    statusBadge.replaceChildren();
    chatTitle.textContent = "—";
    chatMeta.replaceChildren();
    document.title = "Unky Mo — Chat";
    if (!id) lockSend("No session");
    filesEl.hidden = !id;
    shell.classList.toggle("has-files", !!id);
    filesPane.setWindow(id);
    drawer.setWindow(id);
    spinner.setWindow(id);
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
    // Siblings share a checkout (and so a branch) — keep their "[2]" /
    // custom-title suffix so the rows stay tellable apart.
    const suffix = p.branch ? ((p.name || "").match(/ \[[^\]]+\]$/) || [""])[0] : "";
    row.branch.textContent = p.branch ? p.branch + suffix : (p.window_name || p.window_id);
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
      const own = windowID && (data.projects || []).find((pr) => pr.window_id === windowID);
      renderUsage(usageBox, data.usage, { compact: true, tokens: own && own.session_id ? own.tokens : 0 });
      if (!windowID || ended) return;

      // row: the window's state entry (it may have no session); p: the same
      // entry when a session is live in it.
      const row = (data.projects || []).find((pr) => pr.window_id === windowID);
      const p = row && row.session_id ? row : null;
      if (!p) {
        if (everSeen) { markEnded(); return; }
        if (!starting) setNotice("No session is running in this window.");
        else if (Date.now() - openedAt < STARTUP_GRACE_MS) setNotice("Starting session…");
        else setNotice("The session hasn't come up yet. It may be waiting on a prompt in tmux (for example, trusting a new folder).");
      } else {
        everSeen = true;
        setNotice("");
      }
      // After the block above, so the poll that first sees the session
      // already counts it as started.
      const waitingToStart = starting && !everSeen;
      const status = p ? p.status : "none";
      const external = status === "external";
      // External sessions aren't mo's to stop (the TUI imports them instead).
      stopBtn.style.display = p && !external ? "" : "none";
      if (p && p.session_id !== streamSessionID) connectTranscript(p.session_id);
      setBadge(p ? status : waitingToStart ? "starting" : "none");
      filesPane.setAvailable(!!p, waitingToStart ? "Starting…" : "No live session.");
      drawer.setAvailable(!!row);

      if (row) {
        chatTitle.textContent = row.name || windowID;
        document.title = `Unky Mo — ${row.name || windowID}`;
        chatMeta.replaceChildren(
          el("span", { text: row.window_name || windowID }),
          el("span", { text: row.branch || "" })
        );
      }

      permissionBanner.style.display = status === "permission" ? "block" : "none";
      if (status === "question" && p.pending_question_tool) {
        showQuestion(JSON.stringify([p.session_id, p.pending_question_tool, p.pending_question_input]),
          () => [renderQuestionBanner(p.pending_question_tool, p.pending_question_input, pickOption)]);
      } else if (status === "question") {
        // Detected via `claude agents --json` rather than the PreToolUse
        // hook, so the question's text/options were never captured.
        showQuestion("uncaptured", () => [
          el("div", { class: "question-banner__tool", text: "Waiting for your answer" }),
          el("div", { class: "question-banner__question", text: "Claude is showing a question in the terminal that couldn't be captured here. Reply with an option number or your answer." }),
        ]);
      } else {
        questionBanner.style.display = "none";
        questionKey = null;
      }

      spinner.setActive(status === "active");

      if (queued && status === "idle") {
        const text = queued;
        queued = null;
        if (!(await sendPrompt(text))) promptInput.value = text; // give it back to retry
      }
      if (queued) setNotice(`Starting session… your message will be sent when it's ready: “${queued}”`);

      const canSend = status === "idle" || status === "question" || (waitingToStart && !queued);
      sendBtn.disabled = !canSend;
      sendBtn.classList.toggle("is-locked", !canSend);
      sendBtn.textContent = waitingToStart ? (queued ? "Queued" : "Send when ready")
        : canSend ? "Send"
        : !p ? "No session"
        : status === "permission" ? "Waiting for permission"
        : external ? "External session"
        : "Claude is working…";
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
