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
