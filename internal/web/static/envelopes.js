// Claude Code wraps much of what it puts in a session's transcript and
// queue in XML-ish envelopes: slash commands and their output, background
// task notifications, messages from other sessions and subagents, pastes in
// a prompt. This file reads them, so the chat view can show what they say
// instead of the markup. Only the tags listed here are ever parsed, and a
// human's prompt only for pastes: what people type is full of tags that are
// content (HTML, log levels, templates) and stays text.

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

// The envelopes parseEnvelopes knows, by tag name.
const ENVELOPE_KINDS = {
  "cross-session-message": "peer",
  "agent-message": "agent",
  "task-notification": "task",
  "teammate-message": "teammate",
  "pasted_content": "pasted",
  "system-reminder": "hidden",
  "local-command-caveat": "hidden",
};

// What Claude Code writes before a peer's envelope on the line it becomes.
const PEER_LEAD_IN = /Another Claude session sent a message:\s*$/;

function unescapeAttr(v) {
  return v.replace(/&quot;/g, '"').replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&");
}

function parseAttrs(s) {
  const attrs = {};
  for (const a of (s || "").matchAll(/([\w-]+)="([^"]*)"/g)) attrs[a[1]] = unescapeAttr(a[2]);
  return attrs;
}

// A subagent's hand-back is framed by a fixed notice, then the report with
// every line indented by two spaces.
function agentReport(body) {
  const i = body.indexOf("The report follows:");
  if (!body.startsWith("[Subagent hand-back]") || i < 0) return body.trim();
  return body.slice(i + "The report follows:".length).replace(/^\n/, "")
    .split("\n").map((l) => l.replace(/^ {2}/, "")).join("\n").trim();
}

function envelopeSegment(kind, tag, attrs, inner) {
  switch (kind) {
    case "peer":
      return { kind, name: attrs["from-name"] || "another session", from: attrs.from || "", mode: attrs["from-mode"] || "", body: inner.trim() };
    case "agent":
      return { kind, from: attrs.from || "", body: agentReport(inner.replace(/^\n/, "")) };
    case "task":
      return {
        kind, summary: tagText(inner, "summary") || "", status: tagText(inner, "status") || "",
        taskId: tagText(inner, "task-id") || "", outputFile: tagText(inner, "output-file") || "",
      };
    case "teammate":
      return { kind, id: attrs.teammate_id || "teammate", color: attrs.color || "", summary: attrs.summary || "", body: inner.trim() };
    case "pasted":
      return { kind, id: attrs.id || "", body: inner.replace(/^\n/, "").replace(/\n$/, "") };
    default:
      return { kind: "hidden", tag };
  }
}

// parseEnvelopes splits text into {kind: "text", text} and envelope
// segments, in order. only (a list of kinds) limits which envelopes count.
// An envelope ends at the first close tag of its name; a paste only at the
// close tag carrying its own id, since it can hold anything, envelopes
// included. An open tag that never closes stays text.
function parseEnvelopes(text, { only = null } = {}) {
  const out = [];
  const pushText = (t) => {
    if (!t) return;
    const last = out[out.length - 1];
    if (last && last.kind === "text") last.text += t;
    else out.push({ kind: "text", text: t });
  };
  const open = /<([a-z][\w-]*)((?:\s+[\w-]+="[^"]*")*)\s*>/g;
  let pos = 0;
  let m;
  while ((m = open.exec(text))) {
    const tag = m[1];
    const kind = ENVELOPE_KINDS[tag];
    if (!kind || (only && !only.includes(kind))) continue;
    const attrs = parseAttrs(m[2]);
    const start = m.index + m[0].length;
    let end = -1, closeLen = 0;
    if (kind === "pasted") {
      const closes = attrs.id ? [`</${tag} id="${attrs.id}">`, `</${tag}>`] : [`</${tag}>`];
      for (const c of closes) {
        const i = text.indexOf(c, start);
        if (i >= 0) { end = i; closeLen = c.length; break; }
      }
    } else {
      const close = new RegExp(`</${tag}(?:\\s[^>]*)?>`, "g");
      close.lastIndex = start;
      const c = close.exec(text);
      if (c) { end = c.index; closeLen = c[0].length; }
    }
    if (end < 0) continue;
    let before = text.slice(pos, m.index);
    if (kind === "peer") before = before.replace(PEER_LEAD_IN, "");
    pushText(before);
    out.push(envelopeSegment(kind, tag, attrs, text.slice(start, end)));
    pos = end + closeLen;
    open.lastIndex = pos;
  }
  pushText(text.slice(pos));
  return out;
}

// parseTeammateMessages extracts the <teammate-message …>…</teammate-message>
// blocks agent-team teammates send their lead (as plain user lines with no
// other marker). Returns [] when text has none.
function parseTeammateMessages(text) {
  return parseEnvelopes(text, { only: ["teammate"] }).filter((s) => s.kind === "teammate")
    .map(({ id, color, summary, body }) => ({ id, color, summary, body }));
}

const envOneLine = (s) => s.replace(/\s+/g, " ").trim();

function lineCount(s) {
  return s ? s.split("\n").length : 0;
}

// envelopeSummary is text as one plain line of at most max characters, its
// envelopes named rather than shown: for the Queued strip and tooltips.
function envelopeSummary(text, max = 160) {
  const parts = parseEnvelopes(text).map((s) => {
    switch (s.kind) {
      case "text": return envOneLine(s.text);
      case "peer": return `${s.name}: ${envOneLine(s.body)}`;
      case "agent": return `Subagent report: ${envOneLine(s.body)}`;
      case "task": return s.summary || "Background task";
      case "teammate": return `${s.id}: ${s.summary || envOneLine(s.body)}`;
      case "pasted": return `[Pasted ${lineCount(s.body)} line${lineCount(s.body) === 1 ? "" : "s"}]`;
      default: return "";
    }
  }).filter(Boolean);
  const line = envOneLine(parts.join(" "));
  return line.length > max ? line.slice(0, max - 1).trimEnd() + "…" : line;
}

// soleEnvelope returns the one envelope text consists of (surrounding
// whitespace aside), or null. Unknown tags count too, as {kind: "envelope"}.
function soleEnvelope(text) {
  const segs = parseEnvelopes(text).filter((s) => s.kind !== "text" || s.text.trim());
  if (segs.length === 1 && segs[0].kind !== "text") return segs[0];
  const m = text.match(/^\s*<([a-z][\w-]*)((?:\s+[\w-]+="[^"]*")*)\s*>([\s\S]*)<\/\1(?:\s[^>]*)?>\s*$/);
  if (m && !ENVELOPE_KINDS[m[1]]) return { kind: "envelope", tag: m[1], attrs: parseAttrs(m[2]), body: m[3].trim() };
  return null;
}

// describeUserString classifies a user line's string content: what a human
// typed ({kind: "user", text, segments}), a meta row ({kind: "meta"}), a
// message from another session ({kind: "peer"}) or subagent ({kind:
// "agent"}), an unknown envelope ({kind: "envelope"}), teammate messages
// ({kind: "teammates"}), or noise to hide ({kind: "skip"}). Slash commands,
// their output and background-task notifications all arrive as "user"
// lines in the JSONL but aren't yours.
function describeUserString(msg, text) {
  if (msg.isCompactSummary) return { kind: "meta", text: "Conversation compacted" };
  if (text.includes("<teammate-message")) {
    const items = parseTeammateMessages(text);
    if (items.length) return { kind: "teammates", items };
  }
  const origin = msg.origin?.kind;
  if (origin === "peer") {
    const seg = parseEnvelopes(text, { only: ["peer"] }).find((s) => s.kind === "peer");
    return {
      kind: "peer",
      name: msg.origin.name || seg?.name || "another session",
      mode: msg.origin.fromMode || seg?.mode || "",
      body: typeof msg.origin.body === "string" ? msg.origin.body : seg ? seg.body : text,
    };
  }
  if (msg.isMeta) return { kind: "skip" }; // command caveat, system nudges
  if (origin === "task-notification" || text.startsWith("<task-notification>")) {
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
  // What isn't a human's own prompt may be one envelope from elsewhere.
  if (origin !== "human") {
    const env = soleEnvelope(text);
    if (env && env.kind === "peer") return { kind: "peer", name: env.name, mode: env.mode, body: env.body };
    if (env && env.kind === "agent") return { kind: "agent", from: env.from, body: env.body };
    if (env && env.kind === "task") return { kind: "meta", text: env.summary || "Background task finished" };
    // An unknown tag, or one hidden from view, only on a line that says
    // where it came from: an older prompt without an origin may be markup
    // a person typed.
    if (env && env.kind === "hidden" && msg.origin) return { kind: "skip" };
    if (env && env.kind === "envelope" && msg.origin) return env;
  }
  return { kind: "user", text, segments: parseEnvelopes(text, { only: ["pasted"] }) };
}

// describeQueuedCommand describes a queued_command attachment: a message
// Claude read in the middle of a turn, recorded only this way. The same
// kinds as describeUserString, with queued: true; null if it has no prompt.
function describeQueuedCommand(att) {
  if (!att || typeof att.prompt !== "string" || !att.prompt.trim()) return null;
  const origin = att.origin || (att.commandMode === "task-notification" ? { kind: "task-notification" } : undefined);
  return { ...describeUserString({ origin }, att.prompt), queued: true };
}

if (typeof module === "object" && module.exports) {
  module.exports = {
    isMetaContent, tagText, stripAnsi, parseEnvelopes, parseTeammateMessages,
    envelopeSummary, describeUserString, describeQueuedCommand,
  };
}
