// Review comments for the chat view's editor tabs: a "+" in the gutter of a
// file or diff tab opens a comment box under that line; comments collect per
// window (in localStorage) and "Send review" pastes them into the session as
// one prompt — like a PR review, but for the working copy Claude is editing.
//
// Comments are block widgets in a StateField, so they move with their line
// as the file changes; sync() writes the new line numbers back. Uses CM
// (vendor/codemirror.js) and el() (common.js); editor.js wires it in.

const REVIEW_KEY = "mo.review.";

// formatReview turns comments into the prompt sent to Claude, grouped by
// file and ordered by line, each quoting the line it's about.
function formatReview(comments) {
  const sorted = [...comments].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line);
  const parts = sorted.map((c) => {
    const quote = c.text.trim() ? `> ${c.text.trim()}\n` : "";
    return `${c.path}:${c.line}\n${quote}${c.body.trim()}`;
  });
  return `Review comments on the current changes (from the web editor). Please address each one:\n\n${parts.join("\n\n")}`;
}

const reviewRefresh = CM.StateEffect.define();

// The comments field: block widgets after their lines. A reviewRefresh
// effect rebuilds it from {comments, draft}; other changes just map it.
const reviewField = CM.StateField.define({
  create: () => CM.Decoration.none,
  update(deco, tr) {
    deco = deco.map(tr.changes);
    for (const e of tr.effects) if (e.is(reviewRefresh)) deco = buildReviewDecos(tr.state, e.value);
    return deco;
  },
  provide: (f) => CM.EditorView.decorations.from(f),
});

function buildReviewDecos(state, { comments, draft, ui }) {
  const ranges = [];
  const at = (line) => state.doc.line(Math.min(Math.max(1, line), state.doc.lines)).to;
  for (const c of comments) {
    const widget = draft && draft.id === c.id ? new ReviewComposer(draft, ui) : new ReviewComment(c, ui);
    ranges.push(CM.Decoration.widget({ widget, block: true, side: 1 }).range(at(c.line)));
  }
  if (draft && !draft.id) ranges.push(CM.Decoration.widget({ widget: new ReviewComposer(draft, ui), block: true, side: 2 }).range(at(draft.line)));
  return CM.Decoration.set(ranges, true);
}

class ReviewComment extends CM.WidgetType {
  constructor(c, ui) { super(); this.c = c; this.ui = ui; }
  eq(o) { return o.c.id === this.c.id && o.c.body === this.c.body; }
  get commentID() { return this.c.id; }
  toDOM() {
    const edit = el("button", { class: "cm-mo-comment__btn", type: "button", text: "Edit" });
    const del = el("button", { class: "cm-mo-comment__btn", type: "button", text: "Delete" });
    edit.addEventListener("click", () => this.ui.edit(this.c));
    del.addEventListener("click", () => this.ui.remove(this.c.id));
    return el("div", { class: "cm-mo-comment" }, [
      el("div", { class: "cm-mo-comment__body", text: this.c.body }),
      el("div", { class: "cm-mo-comment__actions" }, [edit, del]),
    ]);
  }
}

class ReviewComposer extends CM.WidgetType {
  constructor(draft, ui) { super(); this.draft = draft; this.ui = ui; }
  eq(o) { return o.draft === this.draft; }
  get commentID() { return this.draft.id || null; }
  toDOM() {
    const input = el("textarea", { class: "cm-mo-composer__input", rows: "3", placeholder: "Comment for Claude about this line…" });
    input.value = this.draft.body || "";
    const ok = el("button", { class: "cm-mo-comment__btn is-primary", type: "button", text: this.draft.id ? "Update comment" : "Add comment" });
    const cancel = el("button", { class: "cm-mo-comment__btn", type: "button", text: "Cancel" });
    const submit = () => this.ui.commit(this.draft, input.value);
    ok.addEventListener("click", submit);
    cancel.addEventListener("click", () => this.ui.cancel());
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) { e.preventDefault(); submit(); }
      if (e.key === "Escape") { e.preventDefault(); this.ui.cancel(); }
    });
    setTimeout(() => input.focus(), 0);
    return el("div", { class: "cm-mo-comment is-composer" }, [
      input,
      el("div", { class: "cm-mo-comment__actions" }, [
        el("span", { class: "cm-mo-comment__hint", text: "Ctrl+Enter to add" }), cancel, ok,
      ]),
    ]);
  }
  // The textarea handles its own input; the editor shouldn't see it.
  ignoreEvent() { return true; }
}

class ReviewPlus extends CM.GutterMarker {
  toDOM() { return el("span", { class: "cm-mo-plus", title: "Comment on this line", text: "+" }); }
}
const reviewPlus = new ReviewPlus();

// createReview manages one window's comments. onChange(paths) is called
// with the paths whose comments (or draft) changed, so editor.js can
// refresh those tabs.
function createReview({ onChange, onSent }) {
  let windowID = null;
  let comments = []; // {id, path, line, text, body}
  let draft = null; // {path, line, id?, body?} — the open comment box

  function persist() {
    if (windowID) storageSet(REVIEW_KEY + windowID, comments.length ? comments : null);
  }

  function changed(...paths) {
    persist();
    onChange(new Set(paths.filter(Boolean)));
  }

  const ui = {
    commit(d, body) {
      if (!body.trim()) { ui.cancel(); return; }
      if (d.id) {
        const c = comments.find((x) => x.id === d.id);
        if (c) c.body = body;
      } else {
        comments.push({ id: `c${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`, path: d.path, line: d.line, text: d.text || "", body });
      }
      draft = null;
      changed(d.path);
    },
    cancel() {
      const p = draft?.path;
      draft = null;
      changed(p);
    },
    edit(c) {
      const old = draft?.path;
      draft = { id: c.id, path: c.path, line: c.line, body: c.body };
      changed(old, c.path);
    },
    remove(id) {
      const c = comments.find((x) => x.id === id);
      comments = comments.filter((x) => x.id !== id);
      changed(c?.path);
    },
  };

  // extension is the review part of a tab's editor: the comment widgets and
  // the "+" gutter.
  function extension(path) {
    return [
      reviewField,
      CM.gutter({
        class: "cm-mo-review-gutter",
        lineMarker: () => reviewPlus,
        initialSpacer: () => reviewPlus,
        domEventHandlers: {
          mousedown(view, block, event) {
            if (!event.target.closest(".cm-mo-plus")) return false;
            const line = view.state.doc.lineAt(block.from);
            const old = draft?.path;
            draft = { path, line: line.number, text: line.text };
            changed(old, path);
            return true;
          },
        },
      }),
    ];
  }

  return {
    extension,
    // apply pushes path's comments and draft into a view.
    apply(view, path) {
      view.dispatch({ effects: reviewRefresh.of({ comments: comments.filter((c) => c.path === path), draft: draft && draft.path === path ? draft : null, ui }) });
    },
    // sync records where path's comments ended up after an edit in view.
    sync(view, path) {
      const deco = view.state.field(reviewField, false);
      if (!deco) return;
      let moved = false;
      deco.between(0, view.state.doc.length, (from, _to, value) => {
        const id = value.spec.widget?.commentID;
        const c = id && comments.find((x) => x.id === id);
        if (!c) return;
        const line = view.state.doc.lineAt(from);
        if (c.line !== line.number) { c.line = line.number; c.text = line.text; moved = true; }
      });
      if (moved) persist();
    },
    count: () => comments.length,
    list: () => [...comments].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line),
    remove: ui.remove,
    clear() {
      const paths = comments.map((c) => c.path);
      comments = [];
      draft = null;
      changed(...paths);
    },
    // send pastes the review into the session; resolves to an error
    // message, or null when it was delivered (and the comments cleared).
    async send() {
      if (!comments.length) return "No comments to send.";
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/prompt`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: formatReview(comments) }),
      });
      if (!res.ok) {
        const data = await res.json().catch(() => ({}));
        return res.status === 409 ? "Claude is busy — send the review once the session is idle." : data.error || `request failed (${res.status})`;
      }
      this.clear();
      onSent();
      return null;
    },
    preview: () => formatReview(comments),
    setWindow(id) {
      windowID = id;
      draft = null;
      const saved = id ? storageGet(REVIEW_KEY + id) : null;
      comments = Array.isArray(saved) ? saved.filter((c) => c && typeof c.path === "string" && typeof c.body === "string") : [];
    },
  };
}
