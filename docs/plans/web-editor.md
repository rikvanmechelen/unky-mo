# Code editor tabs in the web chat view

## Goal

Browse, quickly edit, and review Claude's changes from `mo web`, on desktop and on a phone. Syntax highlighting and search only: no language servers.

## Decisions

- **CodeMirror 6 + `@codemirror/merge`**, bundled once with esbuild into the committed `internal/web/static/vendor/codemirror.js` (`make codemirror`, sources in `tools/codemirror/`).
  - Monaco was rejected. Its FAQ says mobile browsers aren't supported, and its only bundler-free build (AMD) is deprecated.
  - code-server, openvscode-server and `code serve-web` were rejected:
    - They use 1 GB+ RAM per instance.
    - They're poor on phones, and their webviews need HTTPS or localhost.
    - Inside an iframe, our page can't drive them: every file opened is a full workbench page load.
    - Line comments would need a separate VS Code extension.
  - vscode.dev + tunnels needs a Microsoft or GitHub account, relays through Azure, and bypasses our basic auth.
  - Loading CodeMirror from a CDN or importmap was rejected: duplicate `@codemirror/state` instances break it, and it would need internet access.
- **Tabs:** the middle column is tabbed. Chat is pinned first; then one tab per file. The composer stays on the Chat tab. Tabs are remembered per window in localStorage.
- **Reach:** only files the Files panel lists, i.e. tracked, untracked-not-ignored, or changed. Ignored files (`.env`) are out. No creating or deleting files for now.
- **Conflicts with Claude:** tabs without local edits reload live; a tab with unsaved edits gets a conflict banner. Saves are guarded by a content hash (409 on mismatch).

## Phases

| # | What | PR | Status |
|---|------|----|--------|
| 1 | Backend read: `gitfiles.Resolve`/`ReadFile`/`ReadHEAD`, `GET /api/sessions/{id}/file?path=[&rev=HEAD]` with ETag | 1 | done |
| 2 | Vendored CodeMirror bundle, `make codemirror`, embedded `vendor/`, ETag + gzip static serving | 1 | done |
| 3 | Tab strip (Chat + file tabs), per-window persistence | 1 | done |
| 4 | Read-only file tabs: open from tree / diff dialog / tool cards / quick open (Ctrl+P), find, go to line, live reload | 1 | done |
| 5 | Editing: `PUT /file` `{path, text, baseHash}` → 409 on mismatch, size cap (`MaxBytesReader`), atomic write keeping the mode; dirty marker, Ctrl+S, conflict banner (reload / view diff / overwrite), unsaved-changes guard on tab close, session switch and `beforeunload` | 2 | |
| 6 | Diff tabs replacing the diff dialog: `MergeView` (wide) / `unifiedMergeView` (narrow), HEAD vs working tree, editable right side, collapse unchanged | 3 | |
| 7 | Per-hunk revert via `rejectChunk`, saved through phase 5's hash-checked save | 3 | |
| 8 | Line comments → Claude: gutter "+", comment widgets, drafts per session, "Send review" batches `path:line` + snippet + comment into one prompt (idle/question only) | 4 | |

Later, maybe: project-wide search (ripgrep endpoint).

## Notes for the next phases

- Writes resolve through `gitfiles.Resolve` too, and must stay restricted to listed paths. Re-check the hash right before the rename, not only at request start.
- In `unifiedMergeView`, deleted lines are widgets. A comment on a deleted line has to map through `getChunks()` (`fromA`/`toA`).
- The vendored bundle already exports `MergeView`, `unifiedMergeView`, `getChunks`, `acceptChunk`/`rejectChunk`, `getOriginalDoc`, `Decoration`, `WidgetType`, `gutter` and `GutterMarker`. Adding exports means editing `tools/codemirror/entry.js` and running `make codemirror`.
- Still to check: touch editing on a real phone (headless Chrome at 390px looked fine).
