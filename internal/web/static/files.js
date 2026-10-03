// Files panel for the chat view: the browser counterpart of the sidebar's
// Files section. "Changed" lists the session checkout's changed files with
// line counts; "All files" is a collapsible tree of every tracked and
// untracked file; the footer shows totals and the upstream sync state.
// Data comes from /api/sessions/{windowID}/files and /tree (gitfiles).
// Uses el() from common.js.

const FILE_MARK_CLASS = { M: "is-mod", R: "is-mod", A: "is-add", "?": "is-add", D: "is-del", U: "is-del" };
const FILES_POLL_MS = 3000;
const TREE_POLL_MS = 10000;

function splitPath(path) {
  const i = path.lastIndexOf("/");
  return i < 0 ? { dir: "", name: path } : { dir: path.slice(0, i + 1), name: path.slice(i + 1) };
}

function plural(n, word) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

// syncText describes where the branch stands against its upstream.
function syncText(sync) {
  if (!sync || !sync.upstream) return "Sync — no upstream";
  const { ahead, behind } = sync;
  if (ahead && behind) return `Sync — ${ahead} to push, ${behind} to pull`;
  if (ahead) return `Sync — ${plural(ahead, "commit")} to push`;
  if (behind) return `Sync — ${plural(behind, "commit")} to pull`;
  return "Sync — up to date";
}

function fileCounts(f) {
  if (f.binary) return [el("span", { class: "file-row__bin", text: "bin" })];
  return [
    el("span", { class: "file-row__plus", text: f.added ? `+${f.added}` : "" }),
    el("span", { class: "file-row__minus", text: f.removed ? `−${f.removed}` : "" }),
  ];
}

// buildTree nests sorted root-relative paths into {dirs: Map, files: []}.
function buildTree(paths) {
  const root = { dirs: new Map(), files: [] };
  for (const p of paths) {
    const parts = p.split("/");
    let node = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const path = parts.slice(0, i + 1).join("/");
      if (!node.dirs.has(parts[i])) node.dirs.set(parts[i], { name: parts[i], path, dirs: new Map(), files: [] });
      node = node.dirs.get(parts[i]);
    }
    node.files.push({ name: parts[parts.length - 1], path: p });
  }
  return root;
}

function createFilesPane(pane) {
  const tabs = pane.querySelectorAll(".files-tab");
  const countEl = pane.querySelector("#files-count");
  const list = pane.querySelector("#files-list");
  const foot = pane.querySelector("#files-foot");

  let windowID = null;
  let available = false; // the window has a live session row — only then poll
  let mode = "changed";
  let changes = null; // last /files body
  let treePaths = null; // last /tree paths
  let treeFetchedAt = 0;
  let treeVersion = 0; // bumped when the tree's file list changes
  let note = ""; // message shown instead of the list
  const expanded = new Map(); // windowID -> Set of expanded dir paths
  let gen = 0; // bumped on window switch; stale responses are dropped
  // What the list was last rendered from. Polls that return the same data
  // skip the re-render, so a click on a folder isn't lost to a node swap.
  let renderedKey = null;

  function expandedSet() {
    if (!expanded.has(windowID)) expanded.set(windowID, null); // null = not yet seeded
    return expanded.get(windowID);
  }

  // On the first tree render for a window, open the folders that hold
  // changes so they're visible without clicking.
  function seedExpanded() {
    if (expandedSet()) return expanded.get(windowID);
    const set = new Set();
    for (const f of changes?.files || []) {
      const parts = f.path.split("/");
      for (let i = 1; i < parts.length; i++) set.add(parts.slice(0, i).join("/"));
    }
    expanded.set(windowID, set);
    return set;
  }

  function renderIfChanged() {
    const key = JSON.stringify([note, changes, treeVersion]);
    if (key === renderedKey) return;
    render();
  }

  function render() {
    renderedKey = JSON.stringify([note, changes, treeVersion]);
    tabs.forEach((t) => t.classList.toggle("is-active", t.dataset.mode === mode));
    countEl.textContent = changes?.repo ? String(changes.files?.length || 0) : "";

    if (note) {
      list.replaceChildren(el("div", { class: "files-pane__note", text: note }));
      foot.replaceChildren();
      return;
    }
    if (!changes) {
      list.replaceChildren();
      foot.replaceChildren();
      return;
    }
    list.replaceChildren(...(mode === "changed" ? changedRows() : treeRows()));

    const files = changes.files || [];
    foot.replaceChildren(
      ...(files.length ? [el("span", {}, [
        el("span", { class: "files-pane__total", text: `+${changes.added} −${changes.removed}` }),
        document.createTextNode(` across ${plural(files.length, "file")}${changes.truncated ? " (list truncated)" : ""}`),
      ])] : []),
      el("span", { text: syncText(changes.sync) })
    );
  }

  function changedRows() {
    const files = changes.files || [];
    if (!files.length) return [el("div", { class: "files-pane__note", text: "No changes." })];
    return files.map((f) => {
      const { dir, name } = splitPath(f.path);
      return el("div", { class: "file-row", title: f.path }, [
        el("span", { class: "file-row__mark " + (FILE_MARK_CLASS[f.status] || ""), text: f.status }),
        el("span", { class: "file-row__name" }, [
          el("span", { class: "file-row__dir", text: dir }),
          el("span", { class: "file-row__base", text: name }),
        ]),
        el("span", { class: "file-row__counts" }, fileCounts(f)),
      ]);
    });
  }

  function treeRows() {
    if (!treePaths) return [el("div", { class: "files-pane__note", text: "Loading…" })];
    const byPath = new Map((changes.files || []).map((f) => [f.path, f]));
    const open = seedExpanded();
    const rows = [];
    const indent = (depth) => `${20 + depth * 14}px`;

    function walk(node, depth) {
      for (const d of [...node.dirs.values()].sort((a, b) => a.name.localeCompare(b.name))) {
        const isOpen = open.has(d.path);
        const row = el("button", { class: "file-row is-dir", type: "button", title: d.path, "aria-expanded": String(isOpen) }, [
          el("span", { class: "file-row__mark", text: isOpen ? "▾" : "▸" }),
          el("span", { class: "file-row__name", text: d.name + "/" }),
          el("span", { class: "file-row__counts" }),
        ]);
        row.style.paddingLeft = indent(depth);
        row.addEventListener("click", () => {
          if (open.has(d.path)) open.delete(d.path); else open.add(d.path);
          render();
        });
        rows.push(row);
        if (isOpen) walk(d, depth + 1);
      }
      for (const f of node.files) {
        const c = byPath.get(f.path);
        const row = el("div", { class: "file-row", title: f.path }, [
          el("span", { class: "file-row__mark " + (c ? FILE_MARK_CLASS[c.status] || "" : ""), text: c ? c.status : "" }),
          el("span", { class: "file-row__name", text: f.name }),
          el("span", { class: "file-row__counts" }, c ? fileCounts(c) : []),
        ]);
        row.style.paddingLeft = indent(depth);
        rows.push(row);
      }
    }
    walk(buildTree(treePaths), 0);
    return rows.length ? rows : [el("div", { class: "files-pane__note", text: "No files." })];
  }

  async function fetchJSON(path) {
    const res = await fetch(path);
    if (res.status === 404) return { gone: true };
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error(data.error || `request failed (${res.status})`);
    }
    return res.json();
  }

  async function refresh(force) {
    if (!windowID || !available || document.visibilityState !== "visible") return;
    const g = gen;
    const base = `/api/sessions/${encodeURIComponent(windowID)}`;
    try {
      const data = await fetchJSON(`${base}/files`);
      if (g !== gen) return;
      if (data.gone) { note = "No live session."; changes = null; renderIfChanged(); return; }
      if (!data.repo) { note = "Not a git repository."; changes = null; renderIfChanged(); return; }
      note = "";
      changes = data;

      if (mode === "all" && (force || !treePaths || Date.now() - treeFetchedAt >= TREE_POLL_MS)) {
        const tree = await fetchJSON(`${base}/tree`);
        if (g !== gen) return;
        // Only a changed file list counts as new tree data.
        const paths = tree.paths || [];
        if (JSON.stringify(paths) !== JSON.stringify(treePaths)) treeVersion++;
        treePaths = paths;
        treeFetchedAt = Date.now();
      }
      renderIfChanged();
    } catch (err) {
      if (g !== gen) return;
      note = `Couldn't read git status: ${err.message}`;
      renderIfChanged();
    }
  }

  tabs.forEach((t) => t.addEventListener("click", () => {
    if (mode === t.dataset.mode) return;
    mode = t.dataset.mode;
    render();
    refresh(true);
  }));

  document.addEventListener("visibilitychange", () => refresh(false));
  setInterval(() => refresh(false), FILES_POLL_MS);

  return {
    // setAvailable is driven by the chat view's state poll: the panel only
    // polls git while the window has a live session, and otherwise shows
    // note (e.g. "Starting…", "Session ended.").
    setAvailable(ok, unavailableNote) {
      if (ok === available && (ok || note === unavailableNote)) return;
      available = ok;
      if (!ok) {
        changes = null;
        note = unavailableNote || "No live session.";
        renderIfChanged();
        return;
      }
      note = "Loading…";
      render();
      refresh(true);
    },
    setWindow(id) {
      if (id === windowID) return;
      gen++;
      available = false;
      windowID = id;
      changes = null;
      treePaths = null;
      treeFetchedAt = 0;
      note = id ? "Loading…" : "";
      render();
    },
  };
}
