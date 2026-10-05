// permission-model.js — what the chat view's permission banner (chat.js)
// shows for a pending tool call, with no DOM. Node's tests require it
// (internal/web/jstests).
//
// The input is the PermissionRequest hook's tool_input (or the transcript's
// tool_use input, or a plan file read by the server), as /permission
// returns it.

// editRowsContext is how many shared lines are kept on each side of a
// change; editRowsMax caps a diff's rows.
const editRowsContext = 3;
const editRowsMax = 400;
const jsonMax = 4000;

function linesOf(s) {
  if (typeof s !== "string" || s === "") return [];
  return s.split("\n");
}

// capRows cuts rows to editRowsMax, saying how many were left out.
function capRows(rows) {
  if (rows.length <= editRowsMax) return rows;
  return [...rows.slice(0, editRowsMax), { sign: "more", n: rows.length - editRowsMax }];
}

// editRows diffs an Edit's old and new strings the simple way: the lines
// both share at the start and end are context (editRowsContext of them,
// the rest folded into a gap), the middle is the old lines removed then
// the new ones added. Rows are {sign: " " | "-" | "+" | "gap", text}.
function editRows(oldText, newText) {
  const a = linesOf(oldText);
  const b = linesOf(newText);
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;

  const rows = [];
  if (pre > editRowsContext) rows.push({ sign: "gap" });
  for (const text of a.slice(Math.max(0, pre - editRowsContext), pre)) rows.push({ sign: " ", text });
  for (const text of a.slice(pre, a.length - suf)) rows.push({ sign: "-", text });
  for (const text of b.slice(pre, b.length - suf)) rows.push({ sign: "+", text });
  for (const text of b.slice(b.length - suf, b.length - suf + editRowsContext)) rows.push({ sign: " ", text });
  if (suf > editRowsContext) rows.push({ sign: "gap" });
  return capRows(rows);
}

function str(v) {
  return typeof v === "string" ? v : "";
}

// permissionContent describes what a permission prompt is about, or null
// when there's no tool call to show.
function permissionContent(tool, input) {
  if (!tool) return null;
  const inp = input && typeof input === "object" ? input : {};
  switch (tool) {
    case "Bash":
      return { kind: "command", command: str(inp.command), description: str(inp.description) };
    case "Edit":
      return { kind: "diff", path: str(inp.file_path), rows: editRows(str(inp.old_string), str(inp.new_string)) };
    case "MultiEdit": {
      const rows = [];
      for (const e of Array.isArray(inp.edits) ? inp.edits : []) {
        if (rows.length) rows.push({ sign: "gap" });
        rows.push(...editRows(str(e && e.old_string), str(e && e.new_string)));
      }
      return { kind: "diff", path: str(inp.file_path), rows: capRows(rows) };
    }
    case "Write":
      return { kind: "diff", path: str(inp.file_path), create: true, rows: capRows(linesOf(str(inp.content)).map((text) => ({ sign: "+", text }))) };
    case "NotebookEdit":
      return { kind: "diff", path: str(inp.notebook_path), rows: capRows(linesOf(str(inp.new_source)).map((text) => ({ sign: "+", text }))) };
    case "Read":
      return { kind: "path", path: str(inp.file_path) };
    case "WebFetch":
      return { kind: "url", url: str(inp.url), prompt: str(inp.prompt) };
    case "WebSearch":
      return { kind: "url", url: str(inp.query), prompt: "" };
    case "ExitPlanMode":
      return { kind: "plan", plan: str(inp.plan), path: str(inp.planFilePath) };
  }
  let text = JSON.stringify(input === undefined ? null : input, null, 2);
  if (text.length > jsonMax) text = text.slice(0, jsonMax) + "\n…";
  return { kind: "json", tool, text };
}

// choiceTone sorts a dialog row by what picking it does: "deny" rejects
// the call, "lasting" also changes something beyond this call (a rule,
// the session's mode), "allow" allows just this call.
function choiceTone(label) {
  const l = str(label);
  if (/^No\b/.test(l)) return "deny";
  if (/don['’]t ask again|always allow|allow reading from|during this session|switch to|use auto mode|remember this/i.test(l)) return "lasting";
  return "allow";
}

if (typeof module === "object" && module.exports) {
  module.exports = { editRows, permissionContent, choiceTone };
}
