// question-model.js — reads an answered AskUserQuestion back into
// per-question answers for the chat view's tool card (chat.js), with no
// DOM. Node's tests require it (internal/web/jstests).
//
// The tool result's toolUseResult carries {questions, answers,
// annotations}: answers maps each question's text to one string. For a
// multiSelect question that string joins the picks with ", ", and a pick
// that itself holds ", " or a double quote is written as a JSON string
// (Claude Code 2.1.289). splitAnswerList undoes that.

// splitAnswerList splits a multiSelect answer into its picks, or returns
// null when the string isn't in that form.
function splitAnswerList(s) {
  if (typeof s !== "string" || s === "") return null;
  const out = [];
  let rest = s;
  for (;;) {
    if (rest.startsWith('"')) {
      let end = -1;
      for (let i = 1, esc = false; i < rest.length; i++) {
        if (esc) { esc = false; continue; }
        if (rest[i] === "\\") { esc = true; continue; }
        if (rest[i] === '"') { end = i; break; }
      }
      if (end < 0) return null;
      let v;
      try { v = JSON.parse(rest.slice(0, end + 1)); } catch { return null; }
      out.push(v);
      rest = rest.slice(end + 1);
    } else {
      const at = rest.indexOf(", ");
      const part = at < 0 ? rest : rest.slice(0, at);
      if (part.includes('"')) return null;
      out.push(part);
      rest = at < 0 ? "" : rest.slice(at);
    }
    if (rest === "") return out;
    if (!rest.startsWith(", ")) return null;
    rest = rest.slice(2);
    if (rest === "") return null;
  }
}

// questionAnswers pairs each question of input with its answer from
// result (a toolUseResult). Each entry: {header, question, multiSelect,
// options: [{label, description, picked}], typed: [text…] (answers that
// aren't an option: "Type something"), notes, answered}. Returns null
// when result has no answers map (an older Claude Code, or a declined
// question).
function questionAnswers(input, result) {
  const qs = Array.isArray(input?.questions) ? input.questions : [];
  const answers = result && typeof result === "object" ? result.answers : null;
  if (!answers || typeof answers !== "object" || !qs.length) return null;
  const notes = result.annotations && typeof result.annotations === "object" ? result.annotations : {};
  return qs.map((q) => {
    const raw = answers[q.question];
    const answered = typeof raw === "string" && raw !== "";
    const opts = Array.isArray(q.options) ? q.options : [];
    let parts = answered ? [raw] : [];
    if (answered && q.multiSelect) parts = splitAnswerList(raw) || [raw];
    const labels = new Set(opts.map((o) => o.label));
    const picked = new Set(parts.filter((p) => labels.has(p)));
    return {
      header: q.header || "",
      question: q.question || "",
      multiSelect: !!q.multiSelect,
      options: opts.map((o) => ({ label: o.label, description: o.description || "", picked: picked.has(o.label) })),
      typed: parts.filter((p) => !labels.has(p)),
      notes: typeof notes[q.question]?.notes === "string" ? notes[q.question].notes : "",
      answered,
    };
  });
}

if (typeof module === "object" && module.exports) {
  module.exports = { splitAnswerList, questionAnswers };
}
