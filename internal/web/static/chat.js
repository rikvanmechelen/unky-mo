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
  if (/^\[Request interrupted[^\]]*\]$/.test(text.trim())) return { kind: "meta", text: text.trim().slice(1, -1) };
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
  if (block.name === "AskUserQuestion" && Array.isArray(input.questions)) {
    const qs = input.questions;
    return { text: qs.length === 1 ? qs[0].question || "" : `${qs.length} questions`, mono: false };
  }
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
// ({questions:[{question,header,options:[{label,description}],multiSelect}]})
// is rendered readably; anything else (a future interactive tool, or a shape
// that doesn't match) falls back to pretty-printed JSON so something is
// still visible instead of nothing.
//
// onAnswer(questions, answers), when given, turns it into a form: radio
// buttons for a single-select question, checkboxes for a multiSelect one,
// and a "Type something" field for each, as in Claude Code's dialog. It
// resolves to "" on success or an error message.
function renderQuestionBanner(tool, input, onAnswer) {
  const frag = document.createDocumentFragment();
  frag.appendChild(el("div", { class: "question-banner__tool", text: tool }));

  if (!(input && Array.isArray(input.questions))) {
    frag.appendChild(el("pre", { text: JSON.stringify(input, null, 2) }));
    return frag;
  }
  if (!onAnswer) {
    for (const q of input.questions) {
      if (q.header) frag.appendChild(el("div", { class: "question-banner__tool", text: q.header }));
      frag.appendChild(el("div", { class: "question-banner__question", text: q.question || "" }));
      if (Array.isArray(q.options)) {
        frag.appendChild(el("ol", { class: "question-banner__options" },
          q.options.map((o) => el("li", { text: o.label + (o.description ? " — " + o.description : "") }))));
      }
    }
    return frag;
  }

  const form = el("form", { class: "question-form" });
  const groups = input.questions.map((q, qi) => {
    const type = q.multiSelect ? "checkbox" : "radio";
    const name = `q${qi}`;
    const boxes = [];
    const opts = (q.options || []).map((o, oi) => {
      const box = el("input", { type, name, value: String(oi) });
      boxes.push(box);
      return el("label", { class: "question-form__opt" }, [
        box,
        el("span", { class: "question-form__label" }, [
          el("span", { text: o.label }),
          o.description ? el("span", { class: "question-form__desc", text: o.description }) : null,
        ].filter(Boolean)),
      ]);
    });
    const otherBox = el("input", { type, name, value: "other", "aria-label": "Type something" });
    const otherText = el("input", { type: "text", class: "question-form__text", placeholder: "Type something", maxlength: "2000" });
    // Typing checks the box, as in the terminal.
    otherText.addEventListener("input", () => { if (otherText.value.trim()) otherBox.checked = true; update(); });
    otherBox.addEventListener("change", () => { if (otherBox.checked && !otherText.value.trim()) otherText.focus(); });
    const fieldset = el("fieldset", { class: "question-form__q" }, [
      el("legend", {}, [
        q.header ? el("span", { class: "question-banner__tool", text: q.header + (q.multiSelect ? " · pick any" : "") }) : null,
        el("span", { class: "question-banner__question", text: q.question || "" }),
      ].filter(Boolean)),
      ...opts,
      el("label", { class: "question-form__opt" }, [otherBox, otherText]),
    ]);
    form.appendChild(fieldset);
    return {
      question: q.question,
      answer() {
        const options = boxes.filter((b) => b.checked).map((b) => Number(b.value));
        const other = otherBox.checked ? otherText.value.trim() : "";
        return { options, other, ok: (options.length > 0 || other !== "") && !(otherBox.checked && !other) };
      },
    };
  });

  const submit = el("button", { type: "submit", class: "question-form__send", text: groups.length > 1 ? "Send answers" : "Send answer" });
  const error = el("span", { class: "question-form__error", role: "alert" });
  form.appendChild(el("div", { class: "question-form__foot" }, [submit, error]));

  function update() {
    submit.disabled = form.classList.contains("is-sending") || !groups.every((g) => g.answer().ok);
  }
  form.addEventListener("change", update);
  update();

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    update();
    if (submit.disabled) return;
    const answers = groups.map((g) => { const { options, other } = g.answer(); return { options, other }; });
    error.textContent = "";
    form.classList.add("is-sending");
    form.querySelectorAll("input").forEach((i) => { i.disabled = true; });
    submit.disabled = true;
    submit.textContent = "Answering…";
    const err = await onAnswer(groups.map((g) => g.question), answers);
    if (!err) return; // the banner goes once the session moves on
    error.textContent = err;
    form.classList.remove("is-sending");
    form.querySelectorAll("input").forEach((i) => { i.disabled = false; });
    submit.textContent = groups.length > 1 ? "Send answers" : "Send answer";
    update();
  });

  frag.appendChild(form);
  return frag;
}

// renderPermissionBanner builds the permission banner from /permission's
// answer: {tool, input, dialog: {title, question, rows: [{n, label, text}],
// sig} | null}. The content comes from permission-model.js; the choices
// are the dialog's own rows, read from Claude's pane by the server, so
// their wording is exactly the terminal's. onAnswer({sig, row, text,
// approve}) resolves to "" on success or an error message. opts.onOpenFile
// opens a diff's file in an editor tab.
function renderPermissionBanner(data, onAnswer, opts = {}) {
  const frag = document.createDocumentFragment();
  const dialog = data && data.dialog;
  // Without a tool call that matches the dialog, what the dialog itself
  // shows of the call (its lines on screen) stands in.
  const content = permissionContent(data && data.tool, data && data.input)
    || (dialog && dialog.preview ? { kind: "screen", text: dialog.preview } : null);

  frag.appendChild(el("div", { class: "question-banner__tool", text: (dialog && dialog.title) || (data && data.tool) || "Permission" }));
  frag.appendChild(el("div", { class: "question-banner__question", text: dialog ? dialog.question : "Claude is waiting for permission." }));

  if (content) {
    const box = el("div", { class: "permission-content" });
    switch (content.kind) {
      case "command":
        if (content.description) box.appendChild(el("div", { class: "permission-content__desc", text: content.description }));
        box.appendChild(el("pre", { class: "permission-content__mono", text: content.command }));
        break;
      case "diff": {
        const head = el("div", { class: "permission-content__path" }, [el("span", { text: (content.create ? "New file " : "") + content.path })]);
        if (content.path && opts.onOpenFile && !content.create) {
          head.appendChild(el("button", { type: "button", class: "permission-content__open", text: "Open", onClick: () => opts.onOpenFile(content.path) }));
        }
        box.appendChild(head);
        const diff = el("div", { class: "tool-card__diff" });
        for (const r of content.rows) {
          if (r.sign === "gap" || r.sign === "more") {
            diff.appendChild(el("div", { class: "diff-line diff-gap" }, [
              el("span", { class: "diff-line__ln", text: "⋯" }), el("span"),
              el("span", { class: "diff-line__text", text: r.sign === "more" ? `${r.n} more lines` : "" }),
            ]));
            continue;
          }
          const cls = r.sign === "+" ? " is-add" : r.sign === "-" ? " is-del" : "";
          diff.appendChild(el("div", { class: "diff-line" + cls }, [
            el("span", { class: "diff-line__ln" }),
            el("span", { class: "diff-line__sign", text: r.sign === " " ? "" : r.sign }),
            el("span", { class: "diff-line__text", text: r.text }),
          ]));
        }
        box.appendChild(diff);
        break;
      }
      case "path":
        box.appendChild(el("pre", { class: "permission-content__mono", text: content.path }));
        break;
      case "url":
        box.appendChild(el("pre", { class: "permission-content__mono", text: content.url }));
        if (content.prompt) box.appendChild(el("div", { class: "permission-content__desc", text: content.prompt }));
        break;
      case "plan":
        box.appendChild(el("div", { class: "permission-content__plan" }, [renderMarkdown(content.plan)]));
        break;
      case "screen":
        box.appendChild(el("pre", { class: "permission-content__mono", text: content.text }));
        box.appendChild(el("div", { class: "permission-content__desc", text: "As shown in the terminal." }));
        break;
      default:
        box.appendChild(el("pre", { class: "permission-content__mono", text: content.text }));
    }
    const scroll = el("div", { class: "permission-content__scroll" }, [box]);
    frag.appendChild(scroll);
    const expand = el("button", { type: "button", class: "permission-content__expand", text: "Expand", hidden: "" });
    expand.addEventListener("click", () => {
      const open = scroll.classList.toggle("is-expanded");
      expand.textContent = open ? "Collapse" : "Expand";
    });
    frag.appendChild(expand);
    // Shown only when the box overflows, once it's laid out.
    requestAnimationFrame(() => { if (scroll.scrollHeight > scroll.clientHeight + 4) expand.hidden = false; });
  }

  if (!dialog || !onAnswer) {
    frag.appendChild(el("div", { class: "permission-hint", text: "Answer it in the terminal." }));
    return frag;
  }

  const form = el("div", { class: "permission-choices", tabindex: "-1" });
  const error = el("div", { class: "question-form__error", role: "alert" });
  const controls = [];
  let sending = false;

  function setSending(on) {
    sending = on;
    form.classList.toggle("is-sending", on);
    for (const c of controls) c.disabled = on;
    // The feedback buttons stay off while their field is empty.
    if (!on) form.querySelectorAll("textarea").forEach((a) => a.dispatchEvent(new Event("input")));
  }
  async function send(answer) {
    if (sending) return;
    error.textContent = "";
    setSending(true);
    const err = await onAnswer({ sig: dialog.sig, ...answer });
    if (!err) return; // the banner goes once the session moves on
    error.textContent = err;
    setSending(false);
  }

  const quick = new Map(); // digit → button, for rows that need no text
  for (const row of dialog.rows) {
    const tone = choiceTone(row.label);
    const text = el("span", { class: "permission-choice__label-text", text: row.label });
    if (tone === "lasting") text.appendChild(el("span", { class: "permission-choice__tag", text: "changes settings" }));
    const label = [el("span", { class: "permission-choice__n", text: row.n + "." }), text];
    const item = el("div", { class: `permission-choice is-${tone}` });

    if (row.text === "feedback") {
      const area = el("textarea", { class: "question-form__text permission-choice__text", rows: "2", maxlength: "2000", placeholder: row.label });
      const fb = el("button", { type: "button", class: "permission-choice__btn", text: "Send feedback" });
      const approve = el("button", { type: "button", class: "permission-choice__btn", text: "Approve with this feedback", title: "shift+tab in the terminal" });
      const refresh = () => { fb.disabled = approve.disabled = sending || !area.value.trim(); };
      area.addEventListener("input", refresh);
      area.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (!fb.disabled) fb.click(); }
      });
      fb.addEventListener("click", () => send({ row: row.n, text: area.value.trim() }));
      approve.addEventListener("click", () => send({ row: row.n, text: area.value.trim(), approve: true }));
      controls.push(area, fb, approve);
      item.append(el("div", { class: "permission-choice__label" }, label), area, el("div", { class: "permission-choice__actions" }, [fb, approve]));
      refresh();
    } else {
      const btn = el("button", { type: "button", class: "permission-choice__btn is-main" }, label);
      controls.push(btn);
      item.appendChild(btn);
      if (row.text === "amend") {
        const field = el("input", { type: "text", class: "question-form__text permission-choice__text", maxlength: "2000",
          placeholder: row.label === "No" ? "…and tell Claude what to do differently" : "…and tell Claude what to do next" });
        field.addEventListener("keydown", (e) => {
          if (e.key === "Enter" && !e.isComposing) { e.preventDefault(); btn.click(); }
        });
        btn.addEventListener("click", () => send({ row: row.n, text: field.value.trim() }));
        controls.push(field);
        item.appendChild(field);
      } else {
        btn.addEventListener("click", () => send({ row: row.n }));
      }
      quick.set(String(row.n), btn);
    }
    form.appendChild(item);
  }
  form.appendChild(error);

  // A digit picks its row, as in the terminal — not while typing.
  form.addEventListener("keydown", (e) => {
    if (e.target.matches("input, textarea") || e.ctrlKey || e.metaKey || e.altKey) return;
    const btn = quick.get(e.key);
    if (btn && !btn.disabled) { e.preventDefault(); btn.click(); }
  });
  frag.appendChild(form);
  return frag;
}

// renderAnswerSummary is an answered AskUserQuestion folded: one row per
// question, its header (or the question) and the answers as chips. Typed
// ("Type something") answers are quoted and marked.
function renderAnswerSummary(qa) {
  const rows = [];
  for (const q of qa) {
    rows.push(el("dt", { class: "qa-summary__q", title: q.question, text: q.header || q.question }));
    const chips = [
      ...q.options.filter((o) => o.picked).map((o) => el("span", { class: "qa-chip", text: o.label })),
      ...q.typed.map((t) => el("span", { class: "qa-chip is-typed", title: "Typed answer", text: `“${t}”` })),
    ];
    if (!q.answered) chips.push(el("span", { class: "qa-none", text: "no answer" }));
    if (q.notes) chips.push(el("span", { class: "qa-notes", text: q.notes }));
    rows.push(el("dd", { class: "qa-summary__a" }, chips));
  }
  return el("dl", { class: "qa-summary" }, rows);
}

// renderAnswerDetail is the opened card: each question with all its
// options, the picked ones marked, plus typed answers and notes.
function renderAnswerDetail(qa) {
  return el("div", { class: "qa-detail" }, qa.map((q) => el("div", { class: "qa-detail__q" }, [
    q.header ? el("span", { class: "qa-detail__header", text: q.header + (q.multiSelect ? " · multiple" : "") }) : null,
    el("span", { class: "qa-detail__question", text: q.question }),
    el("ul", { class: "qa-detail__options" }, [
      ...q.options.map((o) => el("li", { class: o.picked ? "is-picked" : "" }, [
        el("span", { class: "qa-detail__mark", "aria-label": o.picked ? "picked" : "", text: o.picked ? "✓" : "" }),
        el("span", { class: "qa-detail__label" }, [
          el("span", { text: o.label }),
          o.description ? el("span", { class: "qa-detail__desc", text: o.description }) : null,
        ].filter(Boolean)),
      ])),
      ...q.typed.map((t) => el("li", { class: "is-picked is-typed" }, [
        el("span", { class: "qa-detail__mark", text: "✎" }),
        el("span", { class: "qa-detail__label" }, [el("span", { text: t }), el("span", { class: "qa-detail__desc", text: "Typed answer" })]),
      ])),
    ]),
    q.notes ? el("span", { class: "qa-notes", text: q.notes }) : null,
  ].filter(Boolean))));
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

// isAgentTool is true for the tool that spawns a subagent (named "Task"
// in older Claude Code versions).
function isAgentTool(name) {
  return name === "Agent" || name === "Task";
}

// createTranscriptView renders a session's JSONL lines (as streamed by
// /api/transcript or a subagent's transcript stream) into container,
// following the conversation tree's active branch. scrollEl is the
// element that scrolls. opts.userLabel names who wrote user turns
// ("You" by default); opts.onToolCard(entry, block) is called for every
// tool card it creates, so the caller can decorate it. With
// opts.onOpenFile(absPath), file tools (Read/Edit/Write/…) get an "Open"
// button that calls it.
function createTranscriptView(container, scrollEl, opts = {}) {
  const transcript = container;
  const transcriptScroll = scrollEl;
  const userLabel = opts.userLabel || "You";
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

  // appendBubble adds one transcript row. images (user rows only) are data:
  // URLs shown as thumbnails under the text.
  // uuid (a prompt's transcript line) lets reveal() find the bubble.
  function appendBubble(kind, text, images = [], uuid = "") {
    if (kind === "meta") {
      transcript.appendChild(el("div", { class: "msg-system" }, [
        el("span", { class: "msg-system__text", text }),
        el("span", { class: "msg-system__rule" }),
      ]));
      return;
    }
    if (kind === "user") {
      const thumbs = images.map((src) => {
        const btn = el("button", { type: "button", class: "msg-user__image", "aria-label": "View image" }, [el("img", { src, alt: "" })]);
        btn.addEventListener("click", () => openImageViewer(src));
        return btn;
      });
      transcript.appendChild(el("div", { class: "msg-user", ...(uuid ? { "data-uuid": uuid } : {}) }, [
        el("span", { class: "msg-user__label", text: userLabel }),
        ...(text ? [el("span", { class: "msg-user__text", text })] : []),
        ...(thumbs.length ? [el("div", { class: "msg-user__images" }, thumbs)] : []),
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
      sq.style.background = TEAMMATE_COLORS[t.color] || "var(--ink-4)";
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

    const toggle = el("button", { class: "tool-card__toggle", type: "button" }, [
      el("span", { class: "tool-card__sq" }),
      el("span", { class: "tool-card__name", text: block.name }),
      ...(detail ? [el("span", { class: "tool-card__detail" + (detail.mono ? " is-mono" : ""), text: detail.text })] : []),
      action,
    ]);
    const filePath = typeof block.input?.file_path === "string" ? block.input.file_path : null;
    let head = toggle;
    if (filePath && opts.onOpenFile) {
      const open = el("button", { class: "tool-card__open", type: "button", title: `Open ${filePath}`, text: "Open" });
      open.addEventListener("click", () => opts.onOpenFile(filePath));
      head = el("div", { class: "tool-card__head" }, [toggle, open]);
    }
    const card = el("div", { class: "tool-card" }, [head, body]);
    const entry = {
      card, body, action, name: block.name, rawInput: block.input,
      input: el("pre", { text: JSON.stringify(block.input, null, 2) }),
    };
    toggle.addEventListener("click", () => {
      withPin(() => {
        // A click takes the card out of preview mode for good, so the next
        // preview doesn't collapse a card you chose to look at.
        if (previewed === entry) previewed = null;
        setCardOpen(entry, body.style.display === "none");
      });
    });

    toolCards.set(block.id, entry);
    transcript.appendChild(card);
    if (opts.onToolCard) opts.onToolCard(entry, block);
  }

  function setCardOpen(entry, open, preview = false) {
    entry.body.style.display = open ? "flex" : "none";
    entry.action.textContent = open ? "Hide" : "Show";
    entry.card.classList.toggle("is-preview", open && preview);
    entry.card.classList.toggle("is-open", open);
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

    // An answered question shows its answers even while folded; opening
    // it lists every option with the picks marked.
    if (entry.name === "AskUserQuestion" && !block.is_error) {
      const qa = questionAnswers(entry.rawInput, toolUseResult);
      if (qa) {
        card.insertBefore(renderAnswerSummary(qa), body);
        body.appendChild(renderAnswerDetail(qa));
        return;
      }
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
      : toolUseResult?.status === "async_launched"
        ? "Running in the background."
        : typeof block.content === "string"
          ? block.content
          : isAgentTool(entry.name) && Array.isArray(block.content)
            ? block.content.filter((b) => b.type === "text").map((b) => b.text).join("\n\n")
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
        else if (d.kind !== "skip") appendBubble(d.kind, d.text, [], msg.uuid);
        return;
      }
      for (const block of content || []) {
        if (block.type === "tool_result") {
          fillToolResult(block, msg.toolUseResult);
        }
      }
      // A prompt with pasted images is an array of text + image blocks.
      const text = (content || []).filter((b) => b.type === "text").map((b) => b.text).join("\n");
      const images = (content || []).map(imageSrc).filter(Boolean);
      if (!text && !images.length) return;
      const d = describeUserString(msg, text);
      if (d.kind === "user") appendBubble("user", d.text, images, msg.uuid);
      else if (d.kind === "meta") appendBubble("meta", d.text);
    }
  }

  // imageSrc turns an image block into a data: URL, for the image types
  // Claude Code attaches only (an SVG data: URL could carry script).
  function imageSrc(block) {
    const src = block.type === "image" && block.source;
    if (!src || src.type !== "base64" || !ATTACHABLE_TYPES.includes(src.media_type)) return null;
    if (typeof src.data !== "string" || !/^[A-Za-z0-9+/=]+$/.test(src.data)) return null;
    return `data:${src.media_type};base64,${src.data}`;
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

  function clear() {
    nodes.clear();
    messages = [];
    leaf = null;
    toolCards.clear();
    previewed = null;
    transcript.replaceChildren();
  }

  // reveal scrolls to the prompt with transcript line uuid and flashes it.
  // It's false when that prompt isn't on the branch shown.
  function reveal(uuid) {
    const node = [...transcript.querySelectorAll(".msg-user[data-uuid]")].find((n) => n.dataset.uuid === uuid);
    if (!node) return false;
    node.scrollIntoView({ block: "center" });
    node.classList.remove("is-flash");
    void node.offsetWidth; // restart the animation
    node.classList.add("is-flash");
    clearTimeout(node.flashTimer);
    node.flashTimer = setTimeout(() => node.classList.remove("is-flash"), 1600);
    return true;
  }

  return { onLine, clear, withPin, scrollToBottom, toolCards, reveal };
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
  const attachments = createAttachments({
    strip: document.getElementById("attachments"),
    button: document.getElementById("attach-btn"),
    pasteTarget: promptInput,
    dropTarget: composer.closest(".chat-footer"),
    getWindowID: () => windowID,
    onChange: () => {},
    onError: (msg) => { sendError.textContent = msg; },
  });
  const permissionBanner = document.getElementById("permission-banner");
  const questionBanner = document.getElementById("question-banner");
  const chatNotice = document.getElementById("chat-notice");
  const stopBtn = document.getElementById("stop-btn");
  const sessionNav = document.getElementById("session-nav");
  const usageBox = document.getElementById("usage");
  const shell = document.getElementById("chat-shell");
  const filesEl = document.getElementById("files-pane");
  // The Overview tab opens branch diffs as editor tabs and jumps to turns
  // in the transcript; editor and view are defined below by the time a
  // click can happen. It reads prompts the way the transcript does.
  // insertMention adds a reference (a commit, a function) at the cursor in
  // the message box.
  function insertMention(text) {
    const { selectionStart: start, selectionEnd: end, value } = promptInput;
    const before = value.slice(0, start);
    const insert = (before && !/\s$/.test(before) ? " " : "") + text + " ";
    promptInput.value = before + insert + value.slice(end);
    promptInput.setSelectionRange(start + insert.length, start + insert.length);
    promptInput.dispatchEvent(new Event("input"));
    editor.showChat();
    promptInput.focus();
  }
  const inspectorEl = document.getElementById("inspector");
  const overview = createOverview(document.getElementById("overview-panel"), {
    onOpenDiff: (path, kind, line, hash) => editor.reveal(path, line, kind, hash),
    onOpenFile: (path, line) => editor.reveal(path, line, "file"),
    reviewList: (box) => reviewQueue.review(box),
    // While the Overview shows, the left rail holds its inspector.
    onVisible: (v) => {
      shell.classList.toggle("is-overview", v);
      inspectorEl.hidden = !v;
      filesPane.setOverview(v ? reviewQueue : null);
    },
    onMention: insertMention,
    // The × after "Selected (N)": through the Git log, which owns the selection.
    onClearSelection: () => filesPane.clearSelection(),
    describeUser: describeUserString,
    revealTurn: (uuid) => {
      editor.showChat();
      requestAnimationFrame(() => view.reveal(uuid));
    },
    strip: document.getElementById("overview-strip"),
    onShowOverview: () => editor.showOverview(),
    // Puts a prompt in the message box without sending it.
    onDraftPrompt: (text) => {
      editor.showChat();
      setPrompt(text);
      promptInput.focus();
    },
  });
  const editor = createEditorTabs({
    strip: document.getElementById("editor-tabs"),
    chatPanel: document.getElementById("chat-panel"),
    editorPanel: document.getElementById("editor-panel"),
    overview,
    diffKind: () => overview.diffKind(),
    onShowInOverview: (path) => { editor.showOverview(); overview.showFile(path); },
  });
  const rails = createRails(shell);
  createInspector(inspectorEl, overview);
  const reviewQueue = createReviewQueue(overview);
  const filesPane = createFilesPane(filesEl, {
    onCount: rails.setCount,
    onOpen: (path) => editor.open(path, "file"),
    onOpenDiff: (path) => editor.open(path, "diff"),
    onOpenCommitFile: (hash, path) => editor.open(path, "commit", hash),
    onMention: insertMention,
    onSelectionChange: (sel) => overview.setSelection(sel),
    onShowSelection: (sel) => {
      overview.showSelection(sel);
      editor.showOverview();
    },
  });
  const drawer = createTerminalDrawer(document.getElementById("term-drawer"));
  const spinner = createSpinner(document.getElementById("spinner"));
  const modeChip = createModeChip(document.getElementById("mode-chip"), composer,
    (msg) => { sendError.textContent = msg; });
  const subagents = createSubagents(document.getElementById("agent-strip"));
  // Paths in tool cards (and the permission banner) are absolute; editor
  // tabs take them relative to the repo root. Outside the checkout (or
  // before the first files poll) the path is passed as-is and the tab
  // explains it can't be opened.
  const openAbsFile = (absPath) => {
    const root = filesPane.root();
    editor.open(root && absPath.startsWith(root + "/") ? absPath.slice(root.length + 1) : absPath);
  };
  const view = createTranscriptView(transcript, transcriptScroll, {
    onToolCard: subagents.decorateCard,
    onOpenFile: openAbsFile,
  });

  // Per-session state — reset by resetSession when the nav switches to
  // another window in place.
  let windowID = null;

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

  function clearTranscript() {
    if (es) { es.close(); es = null; }
    streamSessionID = null;
    view.clear();
    overview.resetTranscript();
  }

  function connectTranscript(sessionID) {
    clearTranscript();
    streamSessionID = sessionID;
    const stream = new EventSource(`/api/transcript/${encodeURIComponent(windowID)}`);
    es = stream;
    stream.addEventListener("transcript", (e) => {
      if (es !== stream) return; // a late event from a stream we've since replaced
      const line = JSON.parse(e.data);
      view.onLine(line);
      overview.onTranscriptLine(line);
    });
  }

  async function sendPrompt(text, attachmentIDs = []) {
    sendError.textContent = "";
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/prompt`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, attachments: attachmentIDs }),
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

  // The message box grows with its text, up to a cap set in CSS.
  function autosize() {
    promptInput.style.height = "auto";
    promptInput.style.height = `${promptInput.scrollHeight + promptInput.offsetHeight - promptInput.clientHeight}px`;
  }
  promptInput.addEventListener("input", autosize);

  function setPrompt(text) {
    promptInput.value = text;
    autosize();
  }

  // Phone keyboards have no shift+enter, so there Enter adds a line and only
  // the Send button sends.
  const enterAddsLine = window.matchMedia("(pointer: coarse)").matches;
  const commandMenu = createCommandMenu(composer, promptInput,
    () => { if (!sendBtn.disabled) composer.requestSubmit(); }, enterAddsLine);

  promptInput.addEventListener("keydown", (e) => {
    if (commandMenu.handleKey(e)) return;
    // shift+tab cycles the permission mode, as it does in Claude Code.
    if (e.key === "Tab" && e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey) {
      e.preventDefault();
      modeChip.cycle();
      return;
    }
    // Enter sends; shift+enter or alt+enter adds a line, as in Claude Code.
    if (e.key === "Enter" && !e.isComposing && !e.shiftKey && !e.altKey && !enterAddsLine) {
      e.preventDefault();
      if (!sendBtn.disabled) composer.requestSubmit();
    }
  });

  composer.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!windowID) return;
    const text = promptInput.value.trim();
    if (attachments.pending()) {
      sendError.textContent = "Still uploading images…";
      return;
    }
    if (attachments.failed()) {
      sendError.textContent = "Remove the images that failed to upload first.";
      return;
    }
    const ids = attachments.ids();
    if (!text && !ids.length) return;
    if (starting && !everSeen) {
      queued = text;
      setPrompt("");
      sendError.textContent = "";
      pollStatus();
      return;
    }
    if (await sendPrompt(text, ids)) {
      setPrompt("");
      attachments.clear();
    }
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
    statusBadge.replaceChildren(statusSquare(meta.badgeSq || meta.sq, meta.ring), document.createTextNode(meta.label));
    statusBadge.style.background = meta.bg;
    statusBadge.style.color = meta.fg || "";
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
    stopPermission();
    questionBanner.style.display = "none";
    questionKey = null;
  }

  // The permission banner polls /permission once a second while the
  // session waits on a permission prompt: the dialog's choices are read
  // from Claude's pane, and a parallel call's dialog can follow this one.
  // It's rebuilt only when the dialog or its content changes, so text
  // typed into it survives the polls.
  let permissionKey = null;
  let permissionTimer = null;
  let permissionSeq = 0;

  function showPermission(key, data) {
    if (key !== permissionKey) {
      permissionKey = key;
      permissionBanner.replaceChildren(renderPermissionBanner(data, answerPermission, { onOpenFile: openAbsFile }));
    }
    permissionBanner.style.display = "flex";
  }

  async function fetchPermission() {
    const seq = ++permissionSeq;
    const id = windowID;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(id)}/permission`);
      if (seq !== permissionSeq || id !== windowID || !permissionTimer) return;
      if (!res.ok) return; // e.g. 409 once answered: the state poll hides it
      const data = await res.json();
      if (seq !== permissionSeq || !permissionTimer) return;
      showPermission(JSON.stringify([id, data.tool || "", data.input ?? null, data.dialog ? data.dialog.sig : ""]), data);
    } catch (err) {
      // transient — keep the last banner
    }
  }

  // startPermission shows what the state file knows right away, then the
  // dialog once /permission answers.
  function startPermission(p) {
    if (!permissionTimer) {
      permissionTimer = setInterval(fetchPermission, 1000);
      fetchPermission();
    }
    if (permissionKey === null) {
      showPermission("state", { tool: p.pending_tool || "", input: p.pending_input, dialog: null });
    }
  }

  function stopPermission() {
    if (permissionTimer) clearInterval(permissionTimer);
    permissionTimer = null;
    permissionSeq++;
    permissionKey = null;
    permissionBanner.style.display = "none";
  }

  // answerPermission has the server answer the dialog it showed
  // (permission.go). Resolves to "" or an error message for the banner.
  async function answerPermission(answer) {
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/permission`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(answer),
      });
      if (res.ok) {
        setTimeout(fetchPermission, 400);
        return "";
      }
      const data = await res.json().catch(() => ({}));
      if (res.status === 409) permissionKey = null; // rebuild from the next poll
      return data.error || `request failed (${res.status})`;
    } catch (err) {
      return String(err);
    }
  }

  // answerQuestion has the server drive Claude Code's question dialog
  // (answer.go). Resolves to "" or an error message for the form.
  async function answerQuestion(questions, answers) {
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/answer`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ questions, answers }),
      });
      if (res.ok) return "";
      const data = await res.json().catch(() => ({}));
      return data.error || `request failed (${res.status})`;
    } catch (err) {
      return String(err);
    }
  }

  function markEnded() {
    ended = true;
    if (es) { es.close(); es = null; }
    spinner.setActive(false);
    modeChip.setStatus("ended");
    hideBanners();
    setBadge("ended");
    setNotice("Session ended. The tmux window is closed.");
    stopBtn.style.display = "none";
    lockSend("Session ended");
    filesPane.setAvailable(false, "Session ended.");
    drawer.setAvailable(false);
    subagents.setAvailable(false);
    editor.setAvailable(false);
    overview.setAvailable(false);
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
    setPrompt("");
    attachments.clear();
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
    modeChip.setWindow(id);
    subagents.setWindow(id);
    commandMenu.setWindow(id);
    editor.setWindow(id);
    overview.setWindow(id);
    pollStatus();
  }

  stopBtn.addEventListener("click", async () => {
    const id = windowID;
    if (await stopSession(id, chatMeta.firstChild ? chatMeta.firstChild.textContent : id) && id === windowID) markEnded();
  });

  function switchTo(id, push) {
    if (id === windowID) return;
    if (push) history.pushState({}, "", `/chat?window=${encodeURIComponent(id)}`);
    // The Overview stays the open tab in the session switched to.
    const inOverview = shell.classList.contains("is-overview");
    resetSession(id, false);
    if (inOverview && id) editor.showOverview();
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
    row.sq.replaceWith(row.sq = statusSquare(meta.navSq || meta.sq, meta.ring, "", current ? "var(--paper)" : "var(--ink)"));
    // Siblings share a checkout (and so a branch) — keep their "[2]" /
    // custom-title suffix so the rows stay tellable apart.
    const suffix = p.branch ? ((p.name || "").match(/ \[[^\]]+\]$/) || [""])[0] : "";
    row.branch.textContent = p.branch ? p.branch + suffix : (p.window_name || p.window_id);
    row.status.textContent = meta.short;
  }

  function newNavRow(p, project) {
    const row = {
      link: el("a", { class: "nav-session plain", href: `/chat?window=${encodeURIComponent(p.window_id)}` }),
      sq: el("span"),
      branch: el("span", { class: "nav-session__branch" }),
      status: el("span", { class: "nav-session__status" }),
    };
    // The project shows in each row only in the compact list beside the
    // Overview's inspector, which drops the group headings.
    row.link.append(row.sq, el("span", { class: "nav-session__name" }, [el("span", { class: "nav-session__project", text: project }), row.branch]), row.status);
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
    rails.setSessions(groups, windowID);
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
          const row = newNavRow(p, g.project);
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
      setFavicon(data.projects);
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
      subagents.setAvailable(!!p);
      editor.setAvailable(!!p);
      overview.setAvailable(!!p);

      if (row) {
        chatTitle.textContent = row.name || windowID;
        document.title = `Unky Mo — ${row.name || windowID}`;
        chatMeta.replaceChildren(
          el("span", { text: row.window_name || windowID }),
          el("span", { text: row.branch || "" })
        );
      }

      if (status === "permission") startPermission(p);
      else stopPermission();
      if (status === "question" && p.pending_tool) {
        showQuestion(JSON.stringify([p.session_id, p.pending_tool, p.pending_input]),
          () => [renderQuestionBanner(p.pending_tool, p.pending_input,
            p.pending_tool === "AskUserQuestion" ? answerQuestion : null)]);
      } else if (status === "question") {
        // Detected via `claude agents --json` rather than the PreToolUse
        // hook, so the question's text/options were never captured.
        showQuestion("uncaptured", () => [
          el("div", { class: "question-banner__tool", text: "Waiting for your answer" }),
          el("div", { class: "question-banner__question", text: "Claude is showing a question in the terminal that couldn't be read here. Answer it in the terminal, or type a reply (an option number for a simple question)." }),
        ]);
      } else {
        questionBanner.style.display = "none";
        questionKey = null;
      }

      spinner.setActive(status === "active");
      modeChip.setStatus(status);

      if (queued && status === "idle") {
        const text = queued;
        queued = null;
        if (await sendPrompt(text, attachments.ids())) attachments.clear();
        else setPrompt(text); // give it back to retry
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
        ? (p.pending_tool === "AskUserQuestion" ? "Answer in the form above…" : "Type a number or your answer…")
        : status === "permission" ? "Answer the prompt above, or in the terminal"
        : p ? `Message ${p.name}` : "Message this session";
    } catch (err) {
      // transient — leave the last known status showing
    }
  }

  resetSession(windowIDFromURL(), new URLSearchParams(location.search).has("new"));
  setInterval(pollStatus, 2000);
}

main();
