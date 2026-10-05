// The reviewer view (/branch?project=…&branch=… or ?project=…&pr=…): the
// Overview tab and editor tabs for any branch or pull request, without a
// live session. The server resolves the branch: a checkout (editable, as
// in the chat view) or a commit (read-only). Loaded last.

function branchMain() {
  const params = new URLSearchParams(location.search);
  const project = params.get("project") || "";
  const branch = params.get("branch") || "";
  const pr = /^\d+$/.test(params.get("pr") || "") ? params.get("pr") : "";
  const title = document.getElementById("branch-title");
  const meta = document.getElementById("branch-meta");
  const links = document.getElementById("branch-links");
  const note = document.getElementById("branch-note");

  if (!project || (!branch && !pr)) {
    title.textContent = "Nothing to review";
    note.hidden = false;
    note.textContent = "Open this page from a branch, worktree or pull request on the dashboard.";
    return;
  }

  const api = `/api/projects/${encodeURIComponent(project)}/` + (pr ? `pulls/${pr}` : `branches/${encodeURIComponent(branch)}`);
  const key = `branch:${project}:` + (pr ? `#${pr}` : branch);
  title.textContent = pr ? `${project} #${pr}` : project;
  meta.replaceChildren(el("span", { text: branch || "…" }));
  document.title = `Unky Mo — ${pr ? "#" + pr : branch} (${project})`;

  let sessionWindow = null;
  const overview = createOverview(document.getElementById("overview-panel"), {
    onOpenDiff: (path, kind, line) => editor.reveal(path, line, kind),
    onTarget: showTarget,
  });
  const editor = createEditorTabs({
    strip: document.getElementById("editor-tabs"),
    chatPanel: null,
    editorPanel: document.getElementById("editor-panel"),
    overview,
    diffKind: () => overview.diffKind(),
    onShowInOverview: (path) => { editor.showOverview(); overview.showFile(path); },
  });

  // showTarget fills the header from what the server resolved the branch
  // to; it runs after every overview load, so it only rebuilds on change.
  let shown = "";
  function showTarget(t) {
    const sig = JSON.stringify(t);
    if (sig === shown) return;
    shown = sig;
    if (pr) title.textContent = `${project} #${pr}` + (t.prTitle ? ` — ${t.prTitle}` : "");
    meta.replaceChildren(
      el("span", { text: t.branch }),
      el("span", { class: "branch-bar__kind", text: t.kind === "checkout" ? `checked out at ${t.path}` : `not checked out · read-only at ${t.head.slice(0, 7)}` }),
    );
    const items = [];
    if (t.prURL) items.push(el("a", { href: t.prURL, target: "_blank", rel: "noopener", text: "On GitHub" }));
    if (t.sessionWindow) items.push(el("a", { href: `/chat?window=${encodeURIComponent(t.sessionWindow)}`, text: "Open chat" }));
    links.replaceChildren(...items);
    // Review comments go to the branch's live session, if it has one.
    if ((t.sessionWindow || null) !== sessionWindow) {
      sessionWindow = t.sessionWindow || null;
      editor.setTarget({ key, api, sendTo: sessionWindow });
    }
  }

  overview.setTarget({ key, api, transcript: false });
  overview.setAvailable(true);
  // Restores the branch's remembered tabs; Overview is the home tab.
  editor.setTarget({ key, api, sendTo: null });
  editor.setAvailable(true);
}

// The tab icon follows session status here too, like the other pages.
async function pollFavicon() {
  try {
    const res = await fetch("/api/state");
    if (res.ok) setFavicon((await res.json()).projects);
  } catch (_) { /* keep the last icon */ }
}

branchMain();
pollFavicon();
setInterval(pollFavicon, 2000);
