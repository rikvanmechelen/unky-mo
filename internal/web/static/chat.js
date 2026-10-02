// Chat-style live transcript + prompt input. No build step, no framework —
// same convention as app.js. Each SSE "transcript" event is the *exact*
// original JSONL line (see internal/web/transcript.go) — this file owns all
// interpretation of that schema; the backend stays schema-agnostic.

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
  if (typeof input.description === "string") return input.description;
  if (typeof input.file_path === "string") return relativize(input.file_path, cwd);
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
  frag.appendChild(el("div", { class: "q-tool", text: tool }));

  if (input && Array.isArray(input.questions)) {
    for (const q of input.questions) {
      if (q.header) frag.appendChild(el("div", { class: "q-tool", text: q.header }));
      frag.appendChild(el("div", { class: "q-text", text: q.question || "" }));
      if (Array.isArray(q.options)) {
        const list = el("ol", { class: "q-options" }, q.options.map((o, i) =>
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
// into colored <pre> content — no diffing algorithm needed, the hunks are
// already computed.
function renderDiff(structuredPatch) {
  const pre = document.createElement("pre");
  for (const hunk of structuredPatch || []) {
    for (const line of hunk.lines || []) {
      const span = document.createElement("span");
      if (line.startsWith("+")) span.className = "diff-add";
      else if (line.startsWith("-")) span.className = "diff-del";
      span.textContent = line + "\n";
      pre.appendChild(span);
    }
  }
  return pre;
}

function main() {
  const windowID = windowIDFromURL();
  const transcript = document.getElementById("transcript");
  const statusBadge = document.getElementById("status-badge");
  const composer = document.getElementById("composer");
  const promptInput = document.getElementById("prompt-input");
  const sendBtn = document.getElementById("send-btn");
  const sendError = document.getElementById("send-error");
  const questionBanner = document.getElementById("question-banner");

  if (!windowID) {
    transcript.textContent = "no ?window=@N given";
    return;
  }

  const seen = new Set(); // dedup by uuid — EventSource reconnects replay the full backlog
  const toolCards = new Map(); // tool_use_id -> card element, persists across messages

  function appendBubble(cls, text) {
    const bubble = el("div", { class: "bubble" }, []);
    bubble.textContent = text;
    transcript.appendChild(el("div", { class: "msg " + cls }, [bubble]));
    transcript.scrollTop = transcript.scrollHeight;
  }

  function appendToolUse(block, cwd) {
    const detail = toolSummaryDetail(block, cwd);
    const summary = el("summary", { text: detail ? `${block.name} — ${detail}` : block.name });
    const input = el("pre", { text: JSON.stringify(block.input, null, 2) });
    const card = el("details", { class: "tool-card" }, [summary, input]);
    toolCards.set(block.id, card);
    transcript.appendChild(card);
    transcript.scrollTop = transcript.scrollHeight;
  }

  function fillToolResult(block, toolUseResult) {
    const card = toolCards.get(block.tool_use_id);
    if (!card) return; // result for a tool_use we never saw (e.g. truncated backlog) — drop silently

    if (block.is_error || (toolUseResult && typeof toolUseResult === "object" && toolUseResult.toolDenialKind)) {
      card.classList.add("error");
    }

    if (toolUseResult && typeof toolUseResult === "object" && toolUseResult.structuredPatch) {
      card.appendChild(renderDiff(toolUseResult.structuredPatch));
      return;
    }

    const text = typeof toolUseResult === "string"
      ? toolUseResult
      : typeof block.content === "string"
        ? block.content
        : JSON.stringify(block.content);
    card.appendChild(el("pre", { text }));
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

  const es = new EventSource(`/api/transcript/${encodeURIComponent(windowID)}`);
  es.addEventListener("transcript", (e) => {
    const msg = JSON.parse(e.data);
    if (seen.has(msg.uuid)) return;
    seen.add(msg.uuid);
    renderMessage(msg);
  });

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

  async function pollStatus() {
    try {
      const res = await fetch("/api/state");
      const data = await res.json();
      const p = (data.projects || []).find((p) => p.window_id === windowID);
      const status = p ? p.status : "none";
      statusBadge.textContent = status;

      // A pending interactive question (e.g. AskUserQuestion) isn't in the
      // transcript at all — Claude hasn't written it to the JSONL yet (it's
      // blocked waiting on exactly this answer) — so it rides this same
      // /api/state poll that already drives the status badge, not the SSE
      // transcript stream.
      if (status === "question" && p.pending_question_tool) {
        questionBanner.replaceChildren(renderQuestionBanner(p.pending_question_tool, p.pending_question_input));
        questionBanner.style.display = "block";
      } else {
        questionBanner.style.display = "none";
      }

      const canSend = status === "idle" || status === "question";
      sendBtn.disabled = !canSend;
      sendBtn.textContent = canSend ? "Send" : "Claude is working…";
      promptInput.placeholder = status === "question"
        ? "Type a number or your answer…"
        : "Message Claude…";
    } catch (err) {
      // transient — leave the last known status showing
    }
  }

  pollStatus();
  setInterval(pollStatus, 2000);
}

main();
