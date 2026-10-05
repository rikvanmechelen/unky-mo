# Overview of selected commits

## Goal

In the chat view's Git log tab, select one or more commits with the usual multi-select rules, and have the Overview tab show **the change those commits make**: files, kinds, areas, treemap, architecture, contract surface and the function call graph. It's for reviewing part of a branch ("what did these three commits do?") instead of all of it (Branch) or only what isn't committed (Uncommitted).

**v1 covers consecutive commits only**: selections the Overview can show as a plain diff between two real commits. A selection with gaps (A and C without B) is refused with a clear reason. Building a synthetic commit for gaps (`git merge-tree --write-tree` per commit, then `git commit-tree`) is a possible follow-up, listed at the end.

## Why it's mostly plumbing

Everything after the file list already works from a pair of revisions. `review.Analyze` and `Calls` build their `repo` from `Overview.Rev` (before) and `Overview.Head` (after), and with `Head` set they never read the working tree (`review` tests already check this for the reviewer view's ref targets). `gitfiles.GetOverviewAt` does the same for a branch nobody has checked out, comparing its merge base with its head. A selection is a third way to choose that pair: `Rev` = the commit just before the selection, `Head` = its newest commit.

## What counts as consecutive

Let S be the selected commits. S is accepted when:

1. **One newest commit:** exactly one commit in S (`head`) has every other commit of S as an ancestor.
2. **One boundary:** the parents of S's commits that aren't themselves in S are exactly one commit (`base`). All parents count, not just the first one, so a merge whose other side isn't selected has two boundaries and is refused ("the merge at abc1234 brings in commits you didn't select").
3. **Nothing missing:** `git rev-list base..head` is exactly S. This catches a gap (a commit between two selected ones that wasn't selected), and shift-selected rows from another branch in the "all" scope.

Then the change is `git diff base head`, which is exactly what S's commits did.

A selection that contains a root commit (no parents, no boundary) is refused in v1: comparing with the empty tree means `Rev` would be the empty tree, and `Overview.Rev == ""` currently means "no commits". It's rare enough to leave.

This applies to any commit in the repository, not just the branch's: Overview data is read-only, and the reviewer view already reads arbitrary refs. Each hash must still be a full hex id that `cat-file -t` says is a commit (the `/commits/{hash}` rule), and S is capped at 200 commits (the Git log tab shows at most its newest ones anyway).

## Server

### gitfiles

- `ResolveSelection(ctx, cmd, root, hashes []string) (base, head string, err error)`: applies the three rules above.
  - Validation: full hex ids, all commits, at most 200 (all checked before git runs for anything else).
  - Steps: one `git rev-list --parents --no-walk <S…>` for parents; `merge-base --is-ancestor` (or one `rev-list head` membership pass) for rule 1; `rev-list base..head` for rule 3.
  - Errors are typed so the handler can return them as a 422 with a message the browser shows: `ErrSelectionGap` (names the first missing commit), `ErrSelectionMerge` (names the merge and its unselected parent), `ErrSelectionRoot`, `ErrSelectionHeads` (two newest commits, e.g. two branches' tips). `ErrUnknownCommit` stays a 404.
- `GetOverviewRange(ctx, cmd, root, base, head string) (*Overview, error)`: `GetOverviewAt`'s body without the merge-base lookup. Factor the shared part (diffFiles with blob headers from `head`, `finish`) out of `GetOverviewAt` so both call it. Sets `Mode: ModeCommits` (new constant `"commits"`), `Rev = MergeBase = base`, `Head = head`, `Base = ""` (so no "Fetch" offer).
- `ReadAt` already serves either side of a file tab (`base` and `head` are full commit ids).

### web

- `GitFiles` gains `ResolveSelection` and `OverviewRange` (regenerate mocks).
- `/overview`, `/architecture` and `/calls` under `/api/sessions/{windowID}` accept `?base=commits&commits=<h1>,<h2>,…` (comma-separated full hashes, in any order). One helper used by all three (next to the `?base=` parsing that's currently repeated in each handler; fold that repetition into it while there):
  - parses and validates the list (400 on malformed);
  - resolves `(base, head)` through a cache keyed by the sorted hashes plus the checkout root (commits never change, so the answer never goes stale while the objects exist; a bounded `ttlCache` with a long TTL is enough);
  - gets the overview from `overviewCache` under `root + "\x00range\x00" + base + "\x00" + head`.

  The architecture and calls caches use the same key. `changeFingerprint` already skips the disk for an overview with `Head` set, so a cached call graph is reused for as long as the cache holds it.
- **File tabs:** `/file` gets `rev=sel-base` and `rev=sel-head` together with the same `commits=` list. Both read the server-resolved commit (`ReadAt`), never a revision named by the browser. Like `rev=base`, only paths the selection's overview lists are served (under the old path for a rename on the base side). The response is `readOnly: true`.
- Scope check (`POST /scope`) refuses `base=commits` (400); the browser hides it in this mode anyway.
- The reviewer view has no Git log tab, so its routes don't get `base=commits` in v1. The helper takes a root and caches by root, so adding them later is just wiring.

## Browser

### Git log tab (`graph.js`)

- **Selection model:** `selected` (a Set of hashes) and `anchor` (the last row clicked without Shift).
  - Plain click: expands/collapses as today, sets `anchor`, and **leaves the selection alone**. Expanding is how you read a commit, and a selection that disappeared on every read would be annoying. (Alternative: a plain click replaces the selection with that one commit, as in a file manager. Decide while building; the bar below makes either clear.)
  - Ctrl/Cmd-click: toggles that commit, sets `anchor`. Doesn't expand.
  - Shift-click: replaces the selection with every commit row between `anchor` and this row, in display order (Ctrl+Shift adds that range to the selection instead). Doesn't expand. The "Uncommitted changes" row is skipped.
  - Esc (focus in the tab) clears.
  - `mousedown` with Shift calls `preventDefault()` so the browser doesn't select text across rows.
- **Look:** selected rows get `.is-selected` (a `--surface-2` background and an `--ink` left edge, tokens only) and `aria-selected`. The rows sit in a `role="listbox"` with `aria-multiselectable="true"`.
- **Keyboard:** Space toggles the focused row, Shift+↑/↓ extends from the anchor.
- **Selection bar** at the top of the tab, shown while something is selected: `3 commits · abc1234..def5678` + **Show in Overview** + **Clear**.
  - The browser runs a quick check with the parents the log already has (one boundary, and every selected commit except the newest has a selected child). When the check fails, the button is disabled and the reason is shown, e.g. "Not consecutive: 9f3e2a1 is missing" or "Two branches selected". The server's answer remains the authority.
  - A single selected commit is fine: its Overview shows what that commit did.
- The selection survives log polls. Hashes that disappear from the log (e.g. after a rebase) are dropped. It's per window and in memory, so a reload or window switch clears it.
- New callback: `onSelectionChange(hashes)` (through `files.js`), so the Overview can follow the selection.

### Overview tab (`overview.js`)

- **New mode `commits`**, never stored in `OVERVIEW_MODE_KEY` (a selection is temporary). `setSelection(hashes)` from chat.js:
  - With hashes, while the Overview is in `commits` mode, it reloads at once. Otherwise it only updates the switch's third button.
  - "Show in Overview" calls `setSelection` + `setMode("commits")` and opens the Overview tab (`editor.showOverview()`).
  - With no hashes while in `commits` mode, it goes back to the stored mode.
- **Switch:** Branch | Uncommitted | **Selected (3)**. The third button only exists while there is a selection; its title lists the short hashes and subjects.
- Requests use `?base=commits&commits=…` wherever they use `?base=${mode}` today (`load`, `loadArch`, `loadCalls`). A 422 shows the server's message as the tab's note, with a "Back to Branch" link.
- **Hidden in `commits` mode:**
  - the intent trace and turn links (`turnForRange`): the conversation maps to the session's whole change, not to a slice of commits;
  - the scope check;
  - the base-fetch offer.
- `#overview-strip` keeps following the branch: it watches the session, not the selection, so its polls keep `base=branch` whatever the tab shows.
- **Header line:** "3 commits · abc1234..def5678 (subject of the newest)". No "against <base>".

### Editor tabs (`editor.js`)

- **New tab kind `range`**: a read-only diff with `rev=sel-base` on the left and `rev=sel-head` on the right.
  - Same `MergeView` / `unifiedMergeView` as a commit tab: no save, no review comments, no polling (both sides are commits).
  - The key includes `head`, so tabs for two selections don't collide. Label `± name`, kind note "selected commits".
  - The tab stores the hash list it was opened with. Restoring it after a reload works without the selection, because the list is in the tab.
- `onOpenDiff` from the Overview opens `range` in `commits` mode, and `editor.reveal(path, line, "range")` handles calls.js's "Open at line".

## Tests

- `gitfiles/selection_test.go` (real git, `t.TempDir()`), on a history with a merge:
  - a single commit, a linear run;
  - a run that includes a merge with its other side selected (accepted) and without it (`ErrSelectionMerge`);
  - a gap (`ErrSelectionGap` names the missing commit), two tips (`ErrSelectionHeads`), a root commit;
  - abbreviations, refs, options and tree ids refused before any other git call; the cap.
  - `GetOverviewRange` matches `git diff base head` (rename, delete, generated header) and reads nothing from a dirty working tree.
- `handlers_overview_test.go`: `base=commits` reaches `ResolveSelection` with the parsed list and `OverviewRange` with its answer. Both caches are shared across `/overview`, `/architecture` and `/calls` (`.Times(1)`), hash order doesn't change the key, malformed lists are 400 with no `GitFiles` call, selection errors are 422 and unknown commits 404.
- `handlers_editor_test.go`: `rev=sel-base`/`sel-head` read the resolved commits (the old path for a rename on the base side), refuse paths outside the selection's overview, and answer read-only.
- `handlers_calls_test.go`: a selection overview's fingerprint doesn't read the disk.
- No JS test harness exists. Keep the browser's consecutive check in a pure function (`selectionProblem(commits, selected)` in `graph.js`) so it could get one, and check it by hand on this repo's history (merges from `small-tasks` etc.).

## Build order

1. `gitfiles.ResolveSelection` + `GetOverviewRange` (with the `GetOverviewAt` refactor), and their tests.
2. Web: the `?base=` helper, `base=commits` on the three endpoints, the `/file` revs, mocks and handler tests.
3. `graph.js` selection model, look and bar.
4. `overview.js` commits mode, and `editor.js` `range` tabs.
5. CLAUDE.md (Git log tab, Overview tab, testing notes), `make install`, try it on this repo.

## Later

- **Selections with gaps:** apply each selected commit in order onto `base` with `git merge-tree --write-tree --merge-base=<c>^ <acc> <c>` (no checkout or index), then `git commit-tree` the final tree. That leaves an unreferenced commit for `gc` to clean up. A conflict means the selection depends on a commit it leaves out, and gets the same kind of 422.
- The "Uncommitted changes" row as part of a selection (the newest selected commit up to the working tree).
- `base=commits` on the reviewer view once it has a Git log tab.
- `mo calls --commits a..b` for the terminal.

## Step 1 in detail: `gitfiles`

New file `internal/gitfiles/selection.go`, tests in `selection_test.go`.

### API

```go
const ModeCommits = "commits" // next to ModeBranch/ModeHead in overview.go
const MaxSelection = 200

type Selection struct{ Root, Base, Head string }

var ErrBadSelection = errors.New(…)   // empty, over MaxSelection, or not full hex ids
type SelectionError struct {          // a valid set of commits that isn't consecutive
	Reason string // "gap", "merge", "root", "heads"
	Commit string // the commit the message is about (full id)
	Other  string // merge: the parent that isn't selected; heads: the other tip
}
func (e *SelectionError) Error() string // "abc1234 isn't selected", …

func ResolveSelection(ctx, cmd, dir string, hashes []string) (*Selection, error)
func GetOverviewRange(ctx, cmd, root, base, head string) (*Overview, error)
```

`ResolveSelection` takes the checkout dir (like `GetOverview`) and returns its root, so the handler doesn't need a separate `Root` call. Duplicate hashes are dropped before counting.

### ResolveSelection, step by step

1. **Validate** with no git calls: at least one id, at most `MaxSelection` after dedup, each matching `hashRe` (40 or 64 hex). Otherwise `ErrBadSelection`.
2. **Types:** one `git cat-file --batch-check='%(objectname) %(objecttype)'` with the ids on stdin (`OutputStdin`). Anything that isn't `commit` (missing, tree, blob, an annotated tag's own id) gives `ErrUnknownCommit`. A tag id would otherwise be peeled into a commit by `rev-list`.
3. **Parents:** `git rev-list --no-walk=unsorted --parents <ids…> --`. A commit without parents gives `SelectionError{root}`.
4. **Head:** tips = selected commits that aren't a parent of another selected commit. With more than one tip, `git merge-base --independent <tips…>`: two or more independent tips gives `SelectionError{heads}` (two branches). Otherwise the one left is `head`; the other tips are older commits cut off by a gap, which step 6 reports.
5. **Boundary:** B = parents of selected commits that aren't selected.
6. **Reachable set:** R = `git rev-list head --not <B…> --`. R is always a subset of the selection (a commit in R that isn't selected would have to be a parent of a selected one, so in B). A selected commit missing from R is cut off by a boundary commit that is its descendant, which means there's a gap: the missing commits are `git rev-list --topo-order --ancestry-path <cut>..head` minus the selection. Report the oldest of them (the last in topological order) as `SelectionError{gap, Commit: missing}`. This also covers a side branch that forks off a selected commit and is merged back by a selected merge: its commits sit between the two, so leaving them out is a gap (found while testing; the first draft of this plan called it a merge error).
7. **One base:** with R equal to the selection, a linear run has exactly one boundary. More than one can only come from a merge whose other side forked off before the selection and isn't selected: report the selected merge and its unselected parent as `SelectionError{merge}`. (If the merge's first parent is the unselected one, report that.)
8. Return `{Root, Base: B[0], Head: head}`.

On success that's 4 or 5 git processes, regardless of how many commits are selected.

### GetOverviewRange

- Both ids must match `hashRe` and be commits (`cat-file -t`), else `ErrUnknownCommit`; the handler always passes ids from `ResolveSelection`, but the function doesn't trust that.
- Refactor: move `GetOverviewAt`'s diff + blob-header + `finish` part into `overviewBetween(ctx, cmd, o, root, base, head)`. `GetOverviewAt` calls it after its merge-base lookup; `GetOverviewRange` calls it directly.
- Fields: `Mode: ModeCommits`, `Rev = MergeBase = base`, `Head = head`, `Branch` = the checkout's current branch (header text only), `Base` empty (so there's no base-fetch offer), `Fallback` false.

### Tests (real git)

A fixture history on `main` (`newRepo` plus):

```
r ─ c1 ─ c2 ─ c3 ─────── m ─ c5      (main)
          └─ s1 ─ s2 ───┘            (side, merged by m)
```

- Accepted, checking `Base`/`Head`:
  - {c2} → c1..c2; {c2,c3} → c1..c3;
  - {c1,c2,c3} given in shuffled order with a duplicate → r..c3;
  - {c3,s1,s2,m}: a merge with both its sides selected; c3 and s1 share the parent c2 → c2..m.
- Refused:
  - {c1,c3} and {c1,c3,c5} → gap naming c2;
  - {c1,c2,c3,m} → gap naming s1 (s1 and s2 fork off c2 and come back in m);
  - {c3,m} → merge naming m and s2; {m,c5} → merge naming m and its first parent c3;
  - {c3,s2} → heads;
  - {r,c1} → root;
  - {}, 201 ids, a short id, `HEAD` → `ErrBadSelection`;
  - a tree id, a blob id → `ErrUnknownCommit`.
- `GetOverviewRange(c1, c3)`: files and line counts match `git diff --numstat c1 c3`, a rename in the range shows `OldPath`, a generated header is read from `head`, and a dirty working tree doesn't leak in (edit a file after the commits). Bad ids → `ErrUnknownCommit`.
- `TestGetOverviewAt` keeps passing unchanged (guards the refactor).

## Step 2 in detail: web endpoints

### Interface

`GitFiles` (`deps.go`) gains, with `realGitFiles` wrappers and `make mocks`:

```go
// ResolveSelection checks that hashes are consecutive commits of the checkout containing dir (gitfiles.ResolveSelection).
ResolveSelection(dir string, hashes []string) (*gitfiles.Selection, error)
// OverviewRange reads the change between two commits (gitfiles.GetOverviewRange).
OverviewRange(root, base, head string) (*gitfiles.Overview, error)
```

### Which change a request asks for (`handlers_overview.go`)

```go
// changeQuery is the change an Overview request asks for.
type changeQuery struct {
	mode    string   // gitfiles.ModeBranch, ModeHead or ModeCommits
	commits []string // ModeCommits only: sorted, deduplicated full ids
}
func parseChange(r *http.Request) (changeQuery, error)
func (q changeQuery) key(dir string) string // dir + "\x00" + mode [+ "\x00" + joined commits]
```

- `?base=` defaults to branch, as today. `?commits=` is a comma-separated list, required with `base=commits` and refused with any other mode. Syntax only (non-empty, `MaxSelection`, hex shape); the deep checks stay in `gitfiles`. Everything here is a 400.
- `s.sessionChange(w, r) (dir string, q changeQuery, ok bool)` does the parse plus `sessionPath` and writes the 400/404, so `handleOverview`, `handleArchitecture` and `handleCalls` each lose their copy of the `?base=` checks.
- `s.change(dir string, q changeQuery) (*gitfiles.Overview, error)`:
  - branch/head: `s.overview(dir, mode)`, as now;
  - commits: `s.selection(dir, q.commits)` then `overviewCache` under `root + "\x00range\x00" + base + "\x00" + head` → `Git.OverviewRange`.
- `s.selection(dir, commits)`: a new `selectionCache` (`ttlCache`, 10 minutes) keyed by `q.key(dir)`. A commit's parents never change, so the answer only goes stale if the checkout is deleted, and a long TTL is safe. Errors are cached too (a refused selection stays refused).
- The three handlers use `q.key(dir)` as the `archCache`/`callCache` key, which needs no resolution, so a cached graph is served without any git call.

### Errors

`changeStatus(err) int` maps:
- `ErrBadSelection` → 400;
- `*SelectionError` → 422, with its message as the `error` body the browser shows;
- `ErrUnknownCommit` → 404 (e.g. a commit gone after a rebase + gc);
- anything else → 502.

`writeOverview`, `serveArchitecture` and `serveCalls` use it in place of their fixed 502. `ErrNotRepo` keeps its `{repo:false}` answer.

`POST /scope` keeps its own check, which already rejects any mode but branch/head with a 400, so `base=commits` is refused there.

### Files: `rev=sel-base` / `rev=sel-head`

In `handleSessionFile`, these two revs require `commits=` (400 otherwise) and skip `listedPath`. Instead:
- the path must be in the selection's overview `Files` (404 otherwise, with no read);
- `sel-head` reads `ReadAt(root, head, path)`;
- `sel-base` reads `ReadAt(root, base, oldPath or path)` with `Path` set back to the new name, like `readBase`;
- the answer is `readOnly: true`.

Errors go through `changeStatus` as well.

### Bounding the caches

`ttlCache` never forgets a key, which was fine for a handful of checkouts and modes. Selections make keys unbounded. On every store, if the map holds more than 128 entries, entries older than the TTL are dropped. Live entries are at most what was fetched within one TTL, and selection entries are small, so this keeps memory bounded without an LRU. Test it with the injected `now`.

### Tests

New file `handlers_selection_test.go`:
- `/overview?base=commits&commits=b,a,a` reaches `ResolveSelection` with `[a b]` and `OverviewRange` with its answer.
- `/architecture` and `/calls` for the same commits in another order reuse one `ResolveSelection` and one `OverviewRange` (`.Times(1)`), and `Analyze`/`Calls` are called once.
- 400 with no `GitFiles` call for: `commits` missing, `commits` with `base=branch`, a short id, 201 ids.
- `SelectionError` → 422 with its message; `ErrUnknownCommit` → 404.
- `/file?rev=sel-head` and `rev=sel-base` read the head and base (base under a rename's old path), are read-only, and a path outside the selection's files is a 404 with no `ReadAt`.
- `ttlCache` pruning: past 128 keys, expired entries go and fresh ones stay.

## Step 3 in detail: selecting in the Git log tab

All in `static/graph.js` + `style.css`; `files.js` only passes the new callbacks and methods through. The Overview side (what "Show in Overview" does) is step 4. Until then, chat.js passes no handler, so the button is hidden.

### State (inside `createGraphView`)

- `selected`: a Set of hashes; `anchor`: the hash of the last row clicked without Shift (or `null`).
- Cleared on `setWindow`. Kept across log polls and scope switches, but hashes no longer in `log.commits` are dropped on each new log (a rebase, or switching to "This branch" when the commit is only on another branch).
- Part of `renderIfChanged`'s key, so a change redraws.

### Pointer rules (the row's click handler gets the event)

- **Plain click:** as today (expand/collapse; the pseudo row shows the changed files) and sets `anchor`. The selection is left alone.
- **Ctrl/Cmd-click** (`ctrlKey || metaKey`): toggles the row in `selected`, sets `anchor`, doesn't expand.
- **Shift-click:** replaces the selection with the commit rows from `anchor` to this row in display order (Ctrl/Cmd+Shift adds them instead). With no anchor, it selects just this row and makes it the anchor. Doesn't expand.
- The "Uncommitted changes" row can't be selected: modified clicks on it are ignored, and ranges skip it.
- `mousedown` with Shift or Ctrl/Cmd calls `preventDefault()`, so the browser doesn't select text across rows (and Firefox's Ctrl-click table selection stays out of it).

### Keyboard (on a focused row)

- ↑/↓ move focus to the previous/next row, so the list can be walked without Tab. Shift+↑/↓ also moves and selects from `anchor` to the newly focused row (setting the anchor first if there is none).
- Ctrl/Cmd+Space toggles the focused row; plain Space/Enter keep expanding (they're the button's default).
- Esc, with focus anywhere in the tab, clears the selection.

### Accessibility

The rows stay buttons (they expand), so `listbox`/`aria-selected` don't apply. A selected row gets `aria-pressed="true"` (a toggle state screen readers announce) next to `aria-expanded`, and the selection bar is an `aria-live="polite"` region that announces "3 commits selected".

### Selection bar

It sits between the scope switch and the rows, only while something is selected:

```
3 commits · 1a2b3c4..9f8e7d6            Show in Overview   Clear
Not consecutive: 5d6e7f8 isn't selected
```

- The label is `1 commit · 1a2b3c4`, or `N commits · <oldest>..<newest>` in display order.
- **Show in Overview** only appears when the view got an `onShowSelection` handler (step 4 provides it). It's disabled, with the reason on its own line below, while `selectionProblem` reports one.
- **Clear** empties the selection.

### `selectionProblem(commits, selected)`

A pure, top-level function (testable later), mirroring the server's rules over the parents the log already has. The server stays the authority: commits beyond the log's limit are unknown here, and the browser only rejects what it can prove.

1. A selected commit without parents → "the first commit can't be compared with anything before it".
2. Tips (selected commits no other selected commit has as a parent): if several, walk parents from each tip through the loaded log. If no tip reaches all the others → "on different branches".
3. Boundary = parents of selected commits that aren't selected. A boundary commit from which a selected commit is reachable sits between selected commits → "not consecutive: <short> isn't selected".
4. More than one boundary → a merge whose other side isn't selected → "the merge <short> brings in <short>, which isn't selected".

### Callbacks and methods

- `onSelectionChange(selection)` after every change, including drops on poll; `onShowSelection(selection)` from the button. `selection` is `[{hash, subject}]` newest first (display order), so the Overview can name what it shows without its own log.
- `clearSelection()` for the Overview's ×.
- `createFilesPane` passes `onSelectionChange`/`onShowSelection` into the view and exposes `clearSelection()`, plus `showGraph()` (switch the panel to the Git log tab) for step 4.

### Look (`style.css`, tokens only)

- `.graph-row.is-selected`: background `--surface-2` and an inset 3px `--ink` left edge, so it differs from hover/open (`--surface`).
- `.graph-selbar`: `--surface` background, 12px text, the reason in `--ink-3`. Buttons styled like `.graph-detail__action`.

## Step 4 in detail: Overview mode and range tabs

### `overview.js`

- **State:** `selection` (`[{hash, subject}]`, newest first, from the Git log) and `mode` (which can now be `"commits"`). `"commits"` is never written to `OVERVIEW_MODE_KEY`; leaving the mode goes back to the stored one.
- **API:**
  - `setSelection(sel)`: called on every Git log change. In `commits` mode, a different set reloads (like `setMode`), and an empty one goes back to the stored mode. Otherwise only the header redraws (its third button appears or disappears).
  - `showSelection(sel)`: `setSelection` + `setMode("commits")`.
  - New option `onClearSelection` for the ×, which goes through the Files pane, so the Git log and the Overview can't disagree.
  - `setTarget` (a window switch) drops the selection and leaves `commits` mode.
- **Requests:** `query()` gives `base=commits&commits=<sorted ids>` or `base=<mode>`, used by `get` for /overview, /architecture and /calls.
- **No polling in `commits` mode:** commits never change. The interval skips `load()` once `data` is in, and `loadCalls` skips once `calls` is in. That matters because the server recomputes an expired overview/analysis on every poll even when it answers 304.
- **Header:**
  - The switch is no longer hidden just because `data.head` is set (that check meant "reviewer view, ref target"; it becomes `data.head && data.mode !== "commits"`).
  - While there's a selection, a third button, `Selected (N)`, sits next to Branch | Uncommitted. Its title lists `short subject` per commit, and a small × after it clears the selection.
  - The description reads `N commits · <oldest>..<newest> · <newest subject>`, without "up to".
- **Hidden in `commits` mode:**
  - the trace and the scope section (`render` leaves both out);
  - `changedIn` (`turnForRange`) returns null;
  - the strip above the composer is hidden. **Change from the plan:** the plan wanted the strip to keep following the branch through its own polls. That's a second poll stream for a temporary mode, so the strip just hides while a selection is shown and comes back with the branch's data when you leave the mode.
- **Errors:** a 422 ends up in `error` like any failed load. In `commits` mode the note gets a "Back to Branch" button.
- **No files:** "These commits don't change any files."
- **Files:** `openDiff` in `commits` mode calls `onOpenDiff(path, "range", line, ids)`, where `ids` is the sorted ids joined with ",".

### `editor.js`: `range` tabs

- A `range` tab is a commit tab whose `hash` is the comma-joined sorted ids. That reuses the `hash` field the tab list already saves and restores. The key is `range:<ids>:<path>`.
- `loadSavedTabs` accepts `range` when `hash` is a comma-separated list of full ids.
- **Loading:** `loadCommit` handles both kinds. A range tab GETs `/file?path=&rev=sel-base&base=commits&commits=<ids>` and the same with `rev=sel-head`. A missing side is an added/deleted file ("Added in these commits." / "Deleted in these commits."). A 404 → "This file isn't changed in the selected commits."; other failures show the server's message.
- **Bar:** the same Previous/Next/Open file buttons as a commit tab, the kind note `selected commits` (title: how many), no Save. Strip label `± name`, title `Changes in <path> across the selected commits`.
- **Review comments:** none, as for commit tabs (`updateState`/`viewReady` skip both kinds).
- **Lines:** `reveal(path, line, kind, hash)` passes `hash` through, so "Open at line" works for range tabs.
  - `buildCommitView` leaves unchanged ranges uncollapsed when `tab.expanded`, as diff tabs already do.
  - `loadCommit`/`rebuildView` call `viewReady` after building, so a pending line is applied. This also fixes commit tabs ignoring a line.

### `chat.js`

- `createFilesPane(…, { onSelectionChange: overview.setSelection, onShowSelection: (sel) => { overview.showSelection(sel); editor.showOverview(); } })`.
- `createOverview(…, { onClearSelection: () => filesPane.clearSelection(), onOpenDiff: (path, kind, line, hash) => editor.reveal(path, line, kind, hash) })`.

### Checks (by hand, on this repo; there's no JS test harness)

- Select 3 consecutive commits → Show in Overview: files, chips, treemap, architecture and Functions are all for those commits. No trace, no scope check, and the strip is hidden.
- Ctrl-click one more commit: the tab reloads for 4 commits. Clear: back to Branch.
- Select a gap from the browser console past the client check (or via a URL): the note shows the server's 422 text with "Back to Branch".
- Open a file: a read-only range diff with the right sides; a renamed file's left side comes from its old path. Reload the page: the tab comes back.
- Functions view → a function's "Open": the range tab opens at the line.
- Switch window: the selection and `commits` mode are gone.
