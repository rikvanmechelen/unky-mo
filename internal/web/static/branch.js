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
  let queue = null;
  const overview = createOverview(document.getElementById("overview-panel"), {
    onOpenDiff: (path, kind, line) => editor.reveal(path, line, kind),
    onOpenFile: (path, line) => editor.reveal(path, line, "file"),
    reviewList: (box) => queue?.review(box),
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

  // The rails: the inspector on the left, the Review list on the right.
  const shell = document.getElementById("chat-shell");
  const rails = createRails(shell);
  // Selecting something shows it in the inspector, which the Files overlay
  // (a phone's Review list) would cover. The nav overlay holds the
  // inspector, so it stays.
  overview.selection.subscribe((what) => { if (what === "select" && overview.selection.current()) rails.closeOverlay("files"); });
  createInspector(document.getElementById("inspector"), overview);
  queue = createReviewQueue(overview);
  reviewPane(document.getElementById("files-pane"), queue);
  reviewNav(document.getElementById("review-nav"), { project, branch, pr });

  overview.setTarget({ key, api, transcript: false });
  overview.setAvailable(true);
  // Restores the branch's remembered tabs; Overview is the home tab.
  editor.setTarget({ key, api, sendTo: null });
  editor.setAvailable(true);
}

// reviewPane runs the Files panel's two tabs: Review (what to read, in
// order) and Files (every file the change touches).
function reviewPane(pane, queue) {
  const tabs = pane.querySelectorAll(".files-tab");
  const list = pane.querySelector("#files-list");
  const foot = pane.querySelector("#files-foot");
  const count = pane.querySelector("#review-count");
  let mode = "review";
  function render() {
    tabs.forEach((t) => t.classList.toggle("is-active", t.dataset.mode === mode));
    count.textContent = queue.count();
    if (mode === "review") queue.review(list); else queue.branchFiles(list);
    foot.replaceChildren(el("span", { class: "files-pane__keys", text: "j / k step · x reviewed · o diff · f file · Alt+← → back, forward · Esc" }));
  }
  tabs.forEach((t) => t.addEventListener("click", () => { mode = t.dataset.mode; render(); }));
  queue.attach(render);
  render();
}

// reviewNav lists the project's open pull requests (and the branch being
// viewed), so the reviewer can move between them.
async function reviewNav(nav, { project, branch, pr }) {
  const row = (href, label, sub, current) => el("a", { class: "nav-session plain" + (current ? " is-current" : ""), href }, [
    el("span", { class: "status-sq" }),
    el("span", { class: "nav-session__name" }, [el("span", { class: "nav-session__project", text: label }), el("span", { class: "nav-session__branch", text: sub })]),
    el("span", { class: "nav-session__status", text: current ? "viewing" : "" }),
  ]);
  const rows = [];
  if (!pr && branch) rows.push(row(location.href, "branch", branch, true));
  nav.replaceChildren(el("div", { class: "nav-group" }, [el("div", { class: "nav-group__project", text: project }), ...rows]));
  try {
    const res = await fetch(`/api/projects/${encodeURIComponent(project)}/prs`);
    const body = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(body.error || String(res.status));
    for (const p of Array.isArray(body) ? body : []) {
      rows.push(row(`/branch?project=${encodeURIComponent(project)}&pr=${p.number}`, `#${p.number}`, p.title, String(p.number) === pr));
    }
    if (!rows.length) rows.push(el("div", { class: "chat-nav__empty", text: "No open pull requests." }));
  } catch (err) {
    rows.push(el("div", { class: "chat-nav__empty", text: `Couldn’t list pull requests: ${err.message}` }));
  }
  nav.replaceChildren(el("div", { class: "nav-group" }, [el("div", { class: "nav-group__project", text: project }), ...rows]));
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
