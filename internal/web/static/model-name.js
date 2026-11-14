// model-name.js — which model the session is on, read from its transcript,
// with no DOM. Node's tests require it (internal/web/jstests).
//
// Two kinds of JSONL line name the model, and the later one wins:
//   an assistant line's message.model    "claude-opus-5-5" (the API id)
//   /model's local-command-stdout        "Set model to `Opus 5.5 (default)` …"
// The second covers a switch made since Claude last answered. Error lines
// Claude Code writes itself carry "<synthetic>", which isn't a model.

// modelDisplayName turns an API model id into Claude Code's own wording:
// claude-opus-5-5 → "Opus 5.5", claude-sonnet-4-5-20250929 → "Sonnet 4.5",
// claude-3-5-haiku-20241022 → "Haiku 3.5", a [1m] suffix → "(1M context)".
// An id it can't read comes back as-is.
function modelDisplayName(id) {
  if (typeof id !== "string" || !id) return "";
  let rest = id.trim();
  let suffix = "";
  const ctx = /\[(\d+[km])\]$/i.exec(rest);
  if (ctx) {
    suffix = ` (${ctx[1].toUpperCase()} context)`;
    rest = rest.slice(0, ctx.index);
  }
  rest = rest.replace(/^(?:[a-z]+\.)?anthropic\./, "").replace(/-v\d+(?::\d+)?$/, "").replace(/-\d{8}$/, "");
  // claude-<family>-<major>[-<minor>] (current) or claude-<major>[-<minor>]-<family> (older)
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d{1,2}))?$/.exec(rest) ||
    /^claude-(\d+)(?:-(\d{1,2}))?-([a-z]+)$/.exec(rest);
  if (!m) return id;
  const [family, major, minor] = /^\d/.test(m[1]) ? [m[3], m[1], m[2]] : [m[1], m[2], m[3]];
  const version = minor === undefined ? major : `${major}.${minor}`;
  return `${family[0].toUpperCase()}${family.slice(1)} ${version}${suffix}`;
}

// modelFamily is what the chip's colour goes by: "opus", "fable", or
// "other" for every other model (and ids it can't read).
function modelFamily(m) {
  const family = /^(opus|fable)\b/i.exec((m && m.name) || "");
  return family ? family[1].toLowerCase() : "other";
}

const SET_MODEL = /(?:Set model to|Kept model as)\s+`([^`]+)`/;
const ANSI = /\x1b\[[0-9;]*[A-Za-z]/g;

// modelFromLine returns {id, name} for a line that names the model, else
// null. id is the API id when the line has one ("" for /model's output).
function modelFromLine(msg) {
  if (!msg || typeof msg !== "object" || msg.isSidechain) return null;
  const message = msg.message;
  if (!message) return null;
  if (msg.type === "assistant") {
    const id = message.model;
    if (typeof id !== "string" || !id || id.startsWith("<")) return null;
    return { id, name: modelDisplayName(id) };
  }
  if (msg.type !== "user") return null;
  const parts = typeof message.content === "string" ? [message.content]
    : Array.isArray(message.content) ? message.content.map((c) => (c && c.type === "text" ? c.text : "")) : [];
  for (const text of parts) {
    if (typeof text !== "string" || !text.includes("<local-command-stdout>")) continue;
    const m = SET_MODEL.exec(text.replace(ANSI, ""));
    if (m) return { id: "", name: m[1].replace(/\s*\(default\)\s*$/i, "").trim() };
  }
  return null;
}

// createModelTracker keeps the newest model a transcript names. The
// transcript replays from its start on every (re)connect, so callers
// reset() first.
function createModelTracker() {
  let current = null;
  return {
    reset() { current = null; },
    // Returns true when the model changed.
    onLine(msg) {
      const m = modelFromLine(msg);
      if (!m || (current && current.id === m.id && current.name === m.name)) return false;
      current = m;
      return true;
    },
    current() { return current; },
  };
}

if (typeof module === "object" && module.exports) {
  module.exports = { modelDisplayName, modelFamily, modelFromLine, createModelTracker };
}
