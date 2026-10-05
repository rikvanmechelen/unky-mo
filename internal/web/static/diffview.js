// diffview.js — the chat view's diff and code helpers, without any DOM:
// syntax highlighting of text and of diff hunks (through CodeMirror's
// parsers, when the bundle is loaded), unified-diff parsing for a Bash
// command's output, and splitting a command into its shell and heredoc
// parts. chat.js draws with them; Node's tests require it
// (internal/web/jstests).

// Past this many lines a block is shown without highlighting.
const HIGHLIGHT_MAX_LINES = 3000;

let synHighlighterCache = null;

// synHighlighter maps CodeMirror's highlight tags to the syn-* classes in
// style.css, which use the theme's --syn-* tokens.
function synHighlighter() {
  if (synHighlighterCache) return synHighlighterCache;
  const t = CM.tags;
  synHighlighterCache = CM.tagHighlighter([
    { tag: [t.keyword, t.modifier, t.controlKeyword, t.operatorKeyword], class: "syn-k" },
    { tag: [t.string, t.special(t.string), t.regexp], class: "syn-s" },
    { tag: [t.number, t.bool, t.null, t.atom], class: "syn-n" },
    { tag: [t.comment, t.lineComment, t.blockComment], class: "syn-c" },
    { tag: [t.typeName, t.className, t.namespace], class: "syn-t" },
    { tag: [t.function(t.variableName), t.function(t.propertyName)], class: "syn-f" },
    { tag: [t.definition(t.variableName)], class: "syn-d" },
    { tag: [t.propertyName, t.attributeName], class: "syn-p" },
    { tag: [t.tagName, t.heading], class: "syn-g" },
    { tag: [t.meta, t.processingInstruction], class: "syn-m" },
    { tag: t.invalid, class: "syn-x" },
  ]);
  return synHighlighterCache;
}

const parserCache = new Map();

// parserFor returns the Lezer parser for a file name (CodeMirror's
// languageFor), or null for plain text or without the bundle.
function parserFor(path) {
  if (typeof CM === "undefined" || !CM.languageFor || !path) return null;
  const base = (path.split("/").pop() || "").toLowerCase();
  const dot = base.lastIndexOf(".");
  const key = dot > 0 ? base.slice(dot) : base;
  if (parserCache.has(key)) return parserCache.get(key);
  let parser = null;
  try {
    const lang = CM.languageFor(base);
    parser = lang ? (lang.language || lang).parser || null : null;
  } catch {
    parser = null;
  }
  parserCache.set(key, parser);
  return parser;
}

// highlightText splits text into lines of segments, [[{text, cls}]], with
// cls a syn-* class or "". Returns null when the language isn't known or
// the text is too long, so the caller shows it plain.
function highlightText(text, path) {
  const parser = parserFor(path);
  if (!parser) return null;
  const lines = text.split("\n");
  if (lines.length > HIGHLIGHT_MAX_LINES) return null;
  const marks = [];
  try {
    CM.highlightTree(parser.parse(text), synHighlighter(), (from, to, cls) => marks.push([from, to, cls]));
  } catch {
    return null;
  }
  const out = [];
  let pos = 0;
  let mi = 0;
  for (const line of lines) {
    const start = pos;
    const end = pos + line.length;
    const segs = [];
    let at = start;
    while (mi < marks.length && marks[mi][1] <= start) mi++;
    for (let j = mi; j < marks.length && marks[j][0] < end; j++) {
      const from = Math.max(marks[j][0], start);
      const to = Math.min(marks[j][1], end);
      if (from > at) segs.push({ text: text.slice(at, from), cls: "" });
      if (to > from) segs.push({ text: text.slice(Math.max(from, at), to), cls: marks[j][2] });
      at = Math.max(at, to);
    }
    if (at < end) segs.push({ text: text.slice(at, end), cls: "" });
    out.push(segs);
    pos = end + 1;
  }
  return out;
}

// highlightHunk highlights a hunk's prefixed lines ("+x", "-y", " z"): the
// new side (context and additions) and the old side (context and
// removals) are parsed separately, so a removed line is read in the code
// it came from. Returns one segment list per line (without the sign), or
// null when there's nothing to highlight with.
function highlightHunk(lines, path) {
  const sides = { new: [], old: [] };
  lines.forEach((line, i) => {
    const sign = line[0];
    if (sign === " " || sign === "+") sides.new.push(i);
    if (sign === " " || sign === "-") sides.old.push(i);
  });
  const hiNew = highlightText(sides.new.map((i) => lines[i].slice(1)).join("\n"), path);
  if (!hiNew) return null;
  const hiOld = highlightText(sides.old.map((i) => lines[i].slice(1)).join("\n"), path);
  const out = lines.map((line) => [{ text: line.slice(1), cls: "" }]);
  sides.new.forEach((i, k) => { out[i] = hiNew[k]; });
  if (hiOld) sides.old.forEach((i, k) => { if (lines[i][0] === "-") out[i] = hiOld[k]; });
  return out;
}

const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

// diffPath strips git's a/ and b/ prefixes and a trailing tab-separated
// timestamp (diff -u); /dev/null is null.
function diffPath(raw) {
  let p = raw.split("\t")[0].trim();
  if (p.startsWith('"') && p.endsWith('"')) p = p.slice(1, -1);
  if (p === "/dev/null") return null;
  return p.replace(/^[ab]\//, "");
}

// parseUnifiedDiff reads the unified diffs in a command's output (git diff,
// git show, diff -u) as [{path, oldPath, hunks: [{oldStart, newStart,
// lines}]}], in the same hunk shape as an Edit's structuredPatch. Text
// around the diffs (a commit message) is skipped. Returns null when the
// output holds no hunk.
function parseUnifiedDiff(text) {
  if (typeof text !== "string" || !text.includes("@@")) return null;
  const files = [];
  let file = null;
  let pendingOld;
  let hunk = null;
  let oldLeft = 0;
  let newLeft = 0;
  for (const line of text.split("\n")) {
    if (hunk && (oldLeft > 0 || newLeft > 0)) {
      const sign = line[0];
      if (sign === " " || line === "") {
        hunk.lines.push(" " + line.slice(1)); oldLeft--; newLeft--; continue;
      }
      if (sign === "-") { hunk.lines.push(line); oldLeft--; continue; }
      if (sign === "+") { hunk.lines.push(line); newLeft--; continue; }
      if (sign === "\\") continue;
      hunk = null; // a malformed hunk ends early
    }
    if (hunk && line.startsWith("\\")) continue; // "\ No newline at end of file"
    hunk = null;
    if (line.startsWith("diff --git ")) {
      file = null;
      pendingOld = undefined;
      continue;
    }
    if (line.startsWith("--- ")) { pendingOld = diffPath(line.slice(4)); continue; }
    if (line.startsWith("+++ ") && pendingOld !== undefined) {
      const path = diffPath(line.slice(4));
      file = { path: path || pendingOld, oldPath: path && pendingOld && pendingOld !== path ? pendingOld : null, hunks: [] };
      if (path === null) file.deleted = true;
      if (pendingOld === null) file.added = true;
      files.push(file);
      pendingOld = undefined;
      continue;
    }
    const m = file && HUNK_HEADER.exec(line);
    if (m) {
      hunk = { oldStart: +m[1], newStart: +m[3], lines: [] };
      oldLeft = m[2] === undefined ? 1 : +m[2];
      newLeft = m[4] === undefined ? 1 : +m[4];
      file.hunks.push(hunk);
    }
  }
  const withHunks = files.filter((f) => f.hunks.length);
  return withHunks.length ? withHunks : null;
}

// Interpreters whose heredoc is a script in their language.
const SCRIPT_LANGS = [
  [/\bpython[\d.]*\b/, "script.py"],
  [/\b(node|deno|bun)\b/, "script.js"],
  [/\bruby\b/, "script.rb"],
  [/\b(bash|sh|zsh)\b/, "script.sh"],
  [/\b(psql|sqlite3|mysql)\b/, "script.sql"],
];

const HEREDOC = /<<(-?)\s*(?:'([^'\n]+)'|"([^"\n]+)"|\\?([A-Za-z_][\w.-]*))/;

// heredocTarget names a heredoc body's language by the command on its
// first line: the file it's written to (cat > f, tee f), or the
// interpreter it's fed to (python3 -, node). The path only picks the
// highlighting; it's never read.
function heredocTarget(head) {
  const unquote = (p) => p.replace(/^['"]|['"]$/g, "");
  const tee = /\btee\s+(?:-a\s+)?([^\s<>|;&()-][^\s<>|;&()]*)/.exec(head);
  if (tee) return { path: unquote(tee[1]), write: true };
  for (const m of head.matchAll(/(?:^|[^>&\d])>>?\s*([^\s<>|;&()]+)/g)) {
    if (!m[1].startsWith("&") && m[1] !== "/dev/null") return { path: unquote(m[1]), write: true };
  }
  for (const [re, path] of SCRIPT_LANGS) if (re.test(head)) return { path, write: false };
  return { path: null, write: false };
}

// splitCommand cuts a Bash command into parts for display: shell text,
// and heredoc bodies with the path their language comes from ({text,
// path, body: true, write}). Joining every part's text with "\n" gives
// the command back.
function splitCommand(command) {
  const lines = String(command || "").split("\n");
  const parts = [];
  let shell = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const m = HEREDOC.exec(line);
    shell.push(line);
    if (!m) continue;
    const delim = m[2] || m[3] || m[4];
    const strip = m[1] === "-";
    let end = i + 1;
    while (end < lines.length && (strip ? lines[end].replace(/^\t+/, "") : lines[end]) !== delim) end++;
    if (end >= lines.length) continue; // unterminated: leave it as shell
    parts.push({ text: shell.join("\n"), path: "command.sh", body: false });
    shell = [];
    const target = heredocTarget(line.slice(0, m.index) + line.slice(m.index + m[0].length));
    parts.push({ text: lines.slice(i + 1, end).join("\n"), path: target.path, body: true, write: target.write });
    shell.push(lines[end]);
    i = end;
  }
  parts.push({ text: shell.join("\n"), path: "command.sh", body: false });
  return parts;
}

if (typeof module === "object" && module.exports) {
  module.exports = { highlightText, highlightHunk, parseUnifiedDiff, splitCommand, heredocTarget, HIGHLIGHT_MAX_LINES };
}
