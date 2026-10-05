# Bash diffs in the chat view

Status: steps 1–3 built on small-tasks (2026-10-05).

Claude often changes code through Bash: `sed -i`, `perl -pi`, `cat > f <<EOF`,
a `python3 - <<EOF` script that rewrites files, `gofmt -w`, `go mod tidy`, a
codemod. The chat view only gets a diff for `Edit`/`Write`/`MultiEdit`, because
those tools put a `structuredPatch` in the transcript (`renderDiff` in
`chat.js`). A Bash card shows its input JSON and output, nothing else, and
`sed -i` prints no output at all. So the most invasive edits are the ones you
can see the least of.

Goal: a Bash card whose command changed files previews those changes the way an
Edit card does (green/red rows, line numbers), and all diffs, Edit ones too,
get syntax highlighting.

## What the transcript can and can't tell us

- **It can't tell us what a Bash call changed.** The command text is arbitrary
  shell, and the output is whatever it printed. Parsing commands (`sed`
  expressions, heredocs) covers a few shapes and silently gets the rest wrong.
- **After the fact is too late.** By the time the `tool_result` line reaches the
  web server, the "before" content is gone. Diffing against HEAD would mix in
  every earlier Edit of the session.
- **The only exact source is a snapshot before and after the command.** Claude
  Code's `PreToolUse` hook runs synchronously before the tool starts, and
  `PostToolUse` after it returns; both carry `tool_use_id`. That's the seam.

## Design

### Snapshots (step 2)

A tree snapshot of the working tree, taken without touching the repo:

```
cp <git-dir>/index $TMP/index                       # keeps the stat cache: only changed files get hashed
GIT_INDEX_FILE=$TMP/index \
GIT_OBJECT_DIRECTORY=~/.cache/unky-mo/bash-snapshots/objects \
GIT_ALTERNATE_OBJECT_DIRECTORIES=<common-dir>/objects \
  git add -A && git write-tree
```

- Tracked + untracked-not-ignored files, the same set the Files panel shows.
  Ignored files (`.env`, `node_modules`) are never read.
- New blobs and trees go to unky-mo's own object directory, with the repo's
  objects as an alternate. The repo gets no loose objects, its index isn't
  locked or rewritten, and cleaning up is deleting a cache directory.
- Measured on this repo (394 files): 8 ms. It's a `git status`-sized walk, so
  large repos pay what `git status` costs there (fsmonitor/untracked cache help
  if configured).

New subcommand `mo snapshot pre|post` (reads the hook JSON on stdin):

- Runs only for `tool_name == "Bash"`, from the payload's `cwd`, inside a git
  checkout. Anything else, or any error: exit 0 silently. It must never block or
  fail a Bash call.
- `pre` writes `<cache>/<session_id>/<tool_use_id>.json` = `{root, pre}`;
  `post` adds `post` (and skips the write when `post == pre`, the common case:
  `go test`, `grep`, `git log`).
- Ids are validated (`session_id` UUID, `tool_use_id` `^toolu_[A-Za-z0-9]+$`)
  before they become paths.

Hooks: two new unky-mo entries, `PreToolUse` and `PostToolUse` with matcher
`Bash`, command `mo snapshot pre|post # unky-mo`, timeout ~10 s. They're
separate from `status-hook.sh` so non-Bash tools pay nothing and the status
path stays as fast as it is. `v2HookTypes`/`hooksV2UpToDate` assume one unky-mo
entry per event type; that becomes "the exact set of (event, matcher, command)
entries", so `EnsureHooksV2` keeps rewriting stale settings correctly.
`[web] bash_diffs = false` (or a top-level setting) leaves the hooks out.

Sweep: snapshot records and the object dir older than 24 h are removed on TUI
start and by `mo web` periodically, like `AttachmentStore.Sweep`. The object
dir is a loose-object pile; deleting it whole is fine once no record younger
than the cutoff references it (simplest: one object dir per day, drop old days).

### Serving them (step 3)

- `GET /api/sessions/{windowID}/bash-changes` → `{toolUseId: {files: [{path,
  oldPath, added, removed}]}}` for every record of the state row's session with
  `pre != post` (`git diff --numstat -z --find-renames pre post`, same env).
  Trees are immutable, so each record's numstat is cached forever (bounded LRU).
  ETag = hash, polled like `/files` while the Chat tab is visible, plus a fetch
  whenever a Bash `tool_result` arrives.
- `GET /api/sessions/{windowID}/bash-changes/{toolUseId}` → the hunks, in the
  same shape as `structuredPatch` per file (`[{path, hunks:[{oldStart,
  newStart, lines}]}]`), from `git diff -U3 pre post`, parsed server-side (reuse
  `gitfiles.parseFullDiff`'s hunk parsing). Binary files are listed, not
  diffed; a size cap per response, like the file endpoint's 2 MB.
- The session and checkout come from the state row, the record path from
  session id + a validated tool_use_id; the tree ids from our own file, checked
  as hex. Nothing from the request reaches git as a revision or path.
- Same for subagents: their Bash calls carry their own `tool_use_id` and the
  parent's `session_id`, so the subagent dialog can use the same endpoint.
- Reviewer view: not applicable (no transcript).

### In the chat view (step 3, `chat.js`)

- A Bash card with changes gets `+12 −3 · 2 files` in its summary row and is
  previewed like an Edit (`previewToolCard`), showing one `renderDiff` block per
  file under a path header with an "Open" button (opens a `diff` editor tab).
  Input and Output stay below, folded.
- Hunks load when the card is first shown (preview or click), so a 300-call
  backlog costs one listing request, not 300.
- Cards without a record (snapshots off, older sessions, not a git repo) look
  exactly as today.

### Syntax highlighting (step 1, independent of the rest)

- Export `highlightTree` and `classHighlighter` (from `@lezer/highlight`) in
  `tools/codemirror/entry.js` and rebuild the bundle (`make codemirror`); the
  parsers are already in it via `languageFor`.
- `highlightLines(path, lines)`: parse the joined text with
  `languageFor(path).language.parser` (a `StreamLanguage` has one too), walk
  `highlightTree`, and return per-line spans. Done twice per hunk, once for the
  new side (context + `+`) and once for the old side (context + `-`), so a
  removed line isn't highlighted as if it sat between added ones.
- Classes map to the existing `--syn-*` tokens in `style.css`, so both themes
  work, and the add/del row backgrounds stay as they are.
- Lazy and capped: highlight when the card body is first opened, skip files
  over ~2000 diff lines, and fall back to plain text when there's no language.
- Hunks are fragments, so a hunk starting inside a block comment or template
  string can be mis-coloured. GitHub has the same limit; acceptable.
- This improves Edit/Write/MultiEdit cards right away, and Bash diffs pick it
  up for free.

### Cheap extras without snapshots (step 1b, optional)

- A Bash output that *is* a unified diff (`git diff`, `git show`, `diff -u`,
  detected by `diff --git` / `@@ -a,b +c,d @@` headers) renders through the same
  diff renderer instead of a grey `<pre>`.
- The command itself is highlighted as shell, and a heredoc written to a file
  (`cat > path <<'EOF'`, `tee path <<EOF`) highlights its body by `path`'s
  language. Display only; it's never used as the "diff".

## Impact

- **Bash latency:** two extra git calls per Bash command, ~10 ms each here,
  `git status`-sized in big repos. Non-Bash tools are unaffected. If a repo is
  slow the hook's timeout caps it, and the diff is just missing.
- **Settings:** two more unky-mo hook entries in `~/.claude/settings.json`,
  written by the next TUI start. Running sessions may only pick them up after
  restarting Claude.
- **Disk:** blobs of changed/untracked files per snapshot under
  `~/.cache/unky-mo`, swept after 24 h. Content-addressed, so repeated
  snapshots of the same files cost nothing. The repo itself is untouched.
- **Attribution is by time, not by process.** The diff is "what changed in the
  checkout while this Bash call ran":
  - Parallel tool calls (Claude often runs several Bash calls, or a Bash and an
    Edit, at once) each see the others' changes. Overlapping records can be
    flagged ("ran alongside N other calls") by comparing pre/post timestamps.
  - Another session in the same checkout, or you editing, shows up too.
  - A `run_in_background` Bash returns at once: `post` is taken at launch, so
    its later changes are missed. Its record can be marked "background" and
    left without a diff rather than showing a misleading one.
  - Files outside the checkout (`/tmp`, another repo) aren't covered.
- **Exposure:** the diff shows contents of tracked and untracked-not-ignored
  files, which the Files panel and editor already serve. Ignored files are
  never snapshotted.
- **Overview bonus:** the intent trace currently guesses which prompt a
  Bash-made change came from (`ovGuessOrigin`, "Probably Bash in prompt N").
  With records it becomes exact: `tool_use_id` → files and hunks, so those
  files get a real origin, and `turnForRange` can attribute lines too. Worth a
  follow-up step once the records exist.
- **No new trust in the browser:** it only names a window and a tool_use_id;
  every path and revision comes from the server.

## Steps

1. Syntax highlighting for the existing diff cards (bundle exports,
   `highlightLines`, CSS classes → `--syn-*`). Shippable alone. Tests: a Node
   test for `highlightLines` line splitting and old/new side separation.
   1b (optional). Unified-diff output and heredoc bodies in Bash cards.
2. `mo snapshot pre|post` + hook entries + sweep. Tests: real-git snapshot
   (tracked, untracked, ignored left out, repo object dir unchanged, real index
   unchanged), invalid ids and non-Bash payloads are no-ops, `hooksV2UpToDate`
   with the matcher entries.
3. `/bash-changes` endpoints + chat card rendering. Tests: handler tests
   against a `BashChanges` interface mock (state row → session, invalid
   tool_use_id → 404 with no git call, ETag 304), real-git numstat/hunk
   parsing.
4. (Follow-up) Feed records into the Overview's intent trace.
