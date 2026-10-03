// Chat-style live transcript + prompt input. No build step, no framework —
// same convention as app.js. Each SSE "transcript" event is the *exact*
// original JSONL line (see internal/web/transcript.go) — this file owns all
// interpretation of that schema; the backend stays schema-agnostic.

const STATUS = {
  active:     { label: "Working",          sq: "#00B140", ring: 0, bg: "#fff",    border: "#DDDDDD" },
  idle:       { label: "Idle",             sq: "#fff",    ring: 2, bg: "#fff",    border: "#DDDDDD" },
  permission: { label: "Needs permission", sq: "#000",    ring: 0, bg: "#FFCD00", border: "#FFCD00" },
  question:   { label: "Needs input",      sq: "#000",    ring: 0, bg: "#FFCD00", border: "#FFCD00" },
  external:   { label: "External session", sq: "#767676", ring: 0, bg: "#fff",    border: "#DDDDDD" },
  none:       { label: "No session",       sq: "#ddd",    ring: 0, bg: "#fff",    border: "#DDDDDD" },
};

function windowIDFromURL() {
  return new URLSearchParams(location.search).get("window"); // e.g. "@5"
}

function el(tag, attrs, children) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") e.className = v;
    else if (k === "text") e.textContent = v;
    else e.setAttribute(k, v);
  }
  for (const child of children || []) e.appendChild(child);
  return e;
}

function statusSquare(sq, ring) {
  const span = el("span", { class: "status-sq" });
  span.style.background = sq;
  span.style.boxShadow = ring ? `inset 0 0 0 ${ring}px #000` : "none";
  return span;
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

function main() {
  const windowID = windowIDFromURL();
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

  if (!windowID) {
    transcript.textContent = "no ?window=@N given";
    return;
  }

  const seen = new Set(); // dedup by uuid — EventSource reconnects replay the full backlog
  const toolCards = new Map(); // tool_use_id -> {card, body} — persists across messages
  let busyRow = null;

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
    const cls = kind === "user" ? "msg-user" : "msg-assistant";
    const bubbleCls = kind === "user" ? "msg-user__bubble" : "msg-assistant__bubble";
    transcript.appendChild(el("div", { class: cls }, [el("div", { class: bubbleCls, text })]));
  }

  function appendToolUse(block, cwd) {
    const detail = toolSummaryDetail(block, cwd);
    const body = el("div", { class: "tool-card__body" });
    body.style.display = "none";
    const action = el("span", { class: "tool-card__action", text: "Show" });

    const card = el("div", { class: "tool-card" }, [
      el("button", { class: "tool-card__toggle" }, [
        el("span", { class: "tool-card__name", text: block.name }),
        el("span", { class: "tool-card__detail" + (detail && detail.mono ? " is-mono" : ""), text: detail ? detail.text : "" }),
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

    if (toolUseResult && typeof toolUseResult === "object" && toolUseResult.structuredPatch) {
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

  // The SSE stream is bound server-side to whichever Claude session ID the
  // window had when it connected. /clear (or /new) starts a fresh session —
  // new ID, new JSONL file — in the same window, so the status poll below
  // watches session_id and, on a change, wipes the log and reconnects.
  let es = null;
  let streamSessionID = null;

  function connectTranscript(sessionID) {
    if (es) es.close();
    streamSessionID = sessionID;
    seen.clear();
    toolCards.clear();
    busyRow = null;
    transcript.replaceChildren();
    es = new EventSource(`/api/transcript/${encodeURIComponent(windowID)}`);
    es.addEventListener("transcript", (e) => {
      const msg = JSON.parse(e.data);
      if (seen.has(msg.uuid)) return;
      seen.add(msg.uuid);
      withPin(() => renderMessage(msg));
    });
  }

  composer.addEventListener("submit", async (e) => {
    e.preventDefault();
    const text = promptInput.value.trim();
    if (!text) return;
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
        return;
      }
      promptInput.value = "";
    } catch (err) {
      sendError.textContent = String(err);
    }
  });

  function setBusyRow(show) {
    if (show && !busyRow) {
      busyRow = el("div", { class: "chat-busy" }, [
        el("span", { class: "chat-busy__sq" }),
        document.createTextNode("Claude is working in the terminal"),
      ]);
      withPin(() => transcript.appendChild(busyRow));
    } else if (!show && busyRow) {
      const row = busyRow;
      busyRow = null;
      row.remove();
    }
  }

  let titled = false;

  async function pollStatus() {
    try {
      const res = await fetch("/api/state");
      const data = await res.json();
      const p = (data.projects || []).find((pr) => pr.window_id === windowID);
      const status = p ? p.status : "none";
      if (p && p.session_id && p.session_id !== streamSessionID) connectTranscript(p.session_id);
      const meta = STATUS[status] || STATUS.none;

      statusBadge.replaceChildren(statusSquare(meta.sq, meta.ring), document.createTextNode(meta.label));
      statusBadge.style.background = meta.bg;
      statusBadge.style.boxShadow = `inset 0 0 0 1px ${meta.border}`;

      if (p && !titled) {
        titled = true;
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

      const canSend = status === "idle" || status === "question";
      sendBtn.disabled = !canSend;
      sendBtn.classList.toggle("is-locked", !canSend);
      sendBtn.textContent = canSend ? "Send" : "Claude is working…";
      promptInput.placeholder = status === "question"
        ? "Type a number or your answer…"
        : "Message this session";
    } catch (err) {
      // transient — leave the last known status showing
    }
  }

  pollStatus();
  setInterval(pollStatus, 2000);
}

main();
