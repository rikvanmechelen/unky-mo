# Whole file with +/− markers in the Overview inspector

## Goal

The inspector (left rail in Overview mode) shows only a few rows of a file
(`codeBlock`: ±1–3 context rows, or the "first change" ±3). Add a way to
expand any of those blocks to the **whole file**, with the same +/− row
markers, without opening an editor tab.

## What already exists

- `gitfiles.Annotate` already builds the *whole* file as `+`/`-`/` ` rows
  (one `git diff -U<huge>`; untracked, deleted and unchanged files read
  directly). `Annotated.Cut(line, end, side, ctx)` slices it, capped at
  `MaxExcerptRows` = 400 (`Truncated` flag set when the cap hits).
- `GET /excerpt` (session and reviewer prefixes, all `?base=` modes) already
  does path checks, caching (`excerptCache`) and ETags. The frontend
  (`overview.excerpt`) caches per overview ETag.
- `inspector.js` `codeLines(rows, line, side)` renders rows with the
  `.insp-line.is-add/.is-del/.is-focus` styles. Headers have Diff / File
  links (`openLinks`).

So the work is mostly UI plus one server parameter; no new git work.

## Design

1. **Expand control on each code block.** `codeBlock`'s head gets a third
   link, "Whole file" (toggles to "Collapse"). Expanded state is kept per
   `path` + side in a `Set` like `peeks` (survives `render()`), and is
   dropped on selection change like peeks.
2. **Expanded view = same block, taller.** The body renders *all* rows via
   `codeLines`, scrolled so the focus row (the one `line` points at) is in
   view (`scrollIntoView({block:"center"})` after layout), inside a
   `max-height` (~60vh) scroll box so the rail stays usable. Horizontal
   scroll already works (`.insp-code__body`).
3. **Fold unchanged runs (default) with a toggle.** Long unchanged stretches
   are noise. Default: collapse runs of unchanged rows longer than ~8 into a
   "⋯ N unchanged lines" row (click expands that run), keeping 3 rows of
   context around each change and around the focus row. A "Changes only /
   All lines" toggle in the block head overrides. Pure helper
   `ovFoldRows(rows, {keep, ctx, focusIdx})` in `overview-model.js`
   (Node-tested), returning rows plus fold markers.
4. **Side handling.** New-side files show the new numbering; removed files
   (`status: removed` / `side: "old"`) show all rows as `−`. Rows carry both
   `ln` and `oldLn`, so the gutter shows the new number, falling back to the
   old one for `−` rows (as `codeLines` does today).
5. **Truncation.** Files over 400 rows: the server must be able to return
   more. Add `?all=1` to `/excerpt` (parse in `parseExcerpt`) that skips
   `Cut`'s window and returns every row up to a higher cap
   (`MaxFullFileRows`, e.g. 5000, still bounded by the existing 2 MB read
   cap and `tooLarge`). Over the cap: show the window around the focus row
   with the existing `Truncated` note ("Showing rows a–b of N") — no
   pagination in v1.
6. **Binary / too large / unlisted paths**: unchanged behavior (note shown,
   control hidden for binary).

## Steps (commit per step)

1. **Server**: `?all=1` on `/excerpt` + `Cut`-bypassing `Annotated.Window`
   (full rows, cap, `Truncated`, `Total`). Tests: `gitfiles/excerpt_test.go`
   (all rows, cap, deleted/untracked/unchanged file), `handlers_excerpt_test.go`
   (param parsing, cache key unchanged since the annotation is shared, ETag).
2. **Model**: `ovFoldRows` + tests in `jstests/overview_model.test.js`
   (runs below/above threshold, focus row never folded, ctx edges, no
   changes at all → one fold).
3. **Frontend**: `overview.excerpt` learns `all: true` (cache key gains it);
   `inspector.js` expand/collapse + fold toggle + scroll to focus; CSS for
   `.insp-code.is-full`, `.insp-fold` row, scroll box. Use tokens only.
4. **Reach**: also offer "Whole file" on the file inspector's "First change"
   block and on relRow's Peek box (`peekbox` gets the same link). Skip the
   one-line `is-one` rows.
5. **Docs**: update CLAUDE.md's `/excerpt` paragraph and `testing.md`.
   Verify in the browser (chrome-devtools) in light and dark, narrow rail
   (340px) and the phone bottom sheet, which must not trap scroll.

## Risks / open questions

- **Rail width (340px)**: long lines scroll horizontally; line-number gutter
  is 34px. Fine for v1; the rail is draggable if more room is wanted.
- **Memory**: a 5000-row JSON per expanded file, cached per overview ETag
  (cleared on change). Acceptable; expanded one at a time in practice.
- **Alternative considered**: just open the Diff tab with all unchanged
  ranges expanded. Already possible ("Diff" link) but leaves the inspector
  context; the point here is to stay in the rail.
- **Choice for you**: fold unchanged runs by default (proposed), or show
  every line by default? The toggle covers both; only the default differs.

## Revision: preview in the rail (2026-10-17)

The per-block "Whole file" toggle was replaced by this behaviour: wherever
the Overview offers Diff or File, clicking it previews the whole file in the
inspector (diff: +/− rows with unchanged runs folded; file: the new version).
An icon button (square with an arrow out of its top-left corner) opens the
same place in the main panel. State: `selection.preview` in `overview.js`;
view: `previewView` in `inspector.js`. Not covered: the Files panel's own
Changed / Branch files lists, which still open tabs.
