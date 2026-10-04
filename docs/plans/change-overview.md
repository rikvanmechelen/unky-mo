# Change overview in the web chat view

## Goal

Make the *shape* of a session's change visible at a glance — how far it spreads, how much of it is noise, which contracts and package boundaries it touches, and why each file changed — so architecture creep in Claude-made changes is caught while the session runs, not in PR review.

Mockup and background analysis: https://claude.ai/artifact/2yuBLfDhoN6BFBgZDk2Wdj

## Decisions

- **Where:** a pinned **Overview** tab next to Chat in the middle column's tab strip (`editor.js`). "Review (N)" is already the review-comments button, so the tab isn't called Review. Later phases add a one-line strip above the composer and a Files panel grouping.
- **Base:** the branch's whole change — `merge-base(HEAD, default branch)` to the working tree, committed and uncommitted together (mode `branch`). A toggle switches to `head` (uncommitted only, what the Files panel shows). The default branch is the one the Graph tab already uses (`defaultBranch`: `origin/HEAD`, else `main`/`master`). Without one (or on the default branch itself), `branch` falls back to `head`.
- **The browser never names a revision.** `?base=branch|head` picks a mode; the server computes the merge base. The base side of a branch diff is `?rev=base` on `/file`, which the server resolves to the cached merge base.
- **Deterministic first.** Phases 1–3 use git, `go/parser` and the transcript; no LLM. The scope-drift check (phase 4) is an on-demand `claude -p` call.
- **Transcript interpretation stays in the browser** (CLAUDE.md: the frontend owns the JSONL shape). The intent trace is built in `overview.js` from the transcript the chat already streams.

## Phases

| # | What | Status |
|---|------|--------|
| 1 | Range diff + noise classification (`gitfiles.GetOverview`), `GET /overview`, `rev=base` diffs, Overview tab with chips, noise bar, treemap, area-grouped file list | done |
| 2 | Contract surface (Go exported decls, routes, config, manifests) + architecture delta (import edges of changed files) + `.unky-mo/architecture.toml` rules | done |
| 3 | Intent trace (files × turns, files outside the conversation, subagents), jump-to-turn, live strip above the composer | done |
| 4 | Ticket from branch name, on-demand scope-drift check via `claude -p`, "ask Claude to split it out" | done |

## Phase 1 — detailed plan

### Backend: `internal/gitfiles/overview.go`

- `GetOverview(ctx, cmd, dir, mode) (*Overview, error)`:
  1. `Root`; `HEAD` (none → every file is untracked, mode `head`).
  2. mode `branch`: `base := defaultBranch`; `mergeBase := git merge-base HEAD <base>`. If either is missing, or `mergeBase == HEAD` and the branch *is* the base, fall back to mode `head` (`Fallback: true` so the UI can say why).
  3. `rev` = mergeBase or `HEAD`. Run against `rev` vs the working tree:
     - `git diff -z --name-status -M <rev> --` → status + old path (reuse `ParseNameStatus`).
     - `git diff -z --numstat -M <rev> --` → counts (reuse `ParseNumstat`).
     - `git diff -z --numstat -M -w <rev> --` → a file with changes that's missing here (or 0/0) is **whitespace-only**.
     - `git ls-files -z --others --exclude-standard` → untracked, counted with `countLines` (same caps as `GetChanges`).
  4. Each file gets `Kind` from `Classify` and `Area` from `AreaOf`; totals per kind; `Areas` = distinct areas.
- `Classify(path, status, head []byte, wsOnly bool, added, removed int) Kind` — pure:
  - `generated`: Go's `^// Code generated .* DO NOT EDIT\.$` header or `@generated` in the first 1 KB of the working copy; lockfiles (`go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Gemfile.lock`, `Cargo.lock`, `poetry.lock`, `composer.lock`); `vendor/`, `node_modules/`, `*.min.js`, `*.pb.go`, `*_gen.go`.
  - `test`: `_test.go`, `_spec.rb`, `.test.`/`.spec.` JS/TS, `test_*.py`, `*_test.py`, or a `test/`, `tests/`, `spec/`, `__tests__/`, `testdata/` dir.
  - `docs`: `.md`, `.mdx`, `.rst`, `.adoc`, `.txt`, or a `docs/`/`doc/` dir.
  - `renamed`: status R with 0/0 lines.
  - `format`: whitespace-only.
  - `logic`: everything else.
  - Order: generated > renamed > format > test > docs > logic (a whitespace-only test change is noise first).
- `AreaOf(path)`: first two segments under a container dir (`internal`, `cmd`, `pkg`, `app`, `lib`, `src`, `packages`, `apps`, `services`), else the first segment; files at the root → `"."`. `internal/web/static/x.js` → `internal/web`. Good enough for "spread"; per-repo override can come with phase 2's rules file.
- Header reads go through `Resolve` + `O_NOFOLLOW` (`openNoFollow`), first 1 KB only; failures just skip the header check.
- `ReadAt(ctx, cmd, root, rev, rel)`: like `ReadHEAD` for a full commit id (`hashRe` + `cat-file -t` = commit, or `ErrUnknownCommit`).

### Web

- `GitFiles` gains `Overview(dir, mode string)` and `ReadAt(root, rev, path string)`; `make mocks`.
- `GET /api/sessions/{windowID}/overview?base=branch|head` (`handlers_overview.go`): checkout from the state row, `overviewCache` (2 s), ETag = body hash, 304, unknown mode → 400, not a repo → `{repo:false}`.
- `/file?rev=base`: resolve the merge base from the cached branch overview (409-free: no merge base → read as HEAD); the base side of a renamed file reads its old path. `listedPath` also accepts paths in the branch overview (a file deleted in a commit is in neither the tree nor the changes list).

### Frontend: `static/overview.js`

- `createOverview({ panel, onOpenDiff(path, rev) })` with `setWindow(id)`, `setAvailable(ok)`, `show()`, `hide()`; polls `/overview` every 3 s with `If-None-Match` only while visible.
- `editor.js`: a second pinned tab `Overview` after Chat; `activate(OVERVIEW)` shows the overview panel. Remembered as active like other tabs. A new tab kind `bdiff` = diff against the merge base (`rev=base` on the left, the same editable working copy on the right); label `± name` with "vs base".
- Content: header (`branch` vs `base` @ short merge base, mode toggle, fallback note), summary chips (lines/files, logic lines, areas), noise bar (click a kind to hide it from the list; remembered in localStorage `mo.overview.hidden`), squarified treemap by area (tile colour = add share, generated tiles neutral; clicking a tile filters the list), file list grouped by area with kind tags; a file click opens a `bdiff` (branch mode) or `diff` (head mode) tab.
- `layoutTreemap` and `summarize` are pure functions so they can be checked in the browser console / headless.

### Tests

- `overview_test.go` (real git in `t.TempDir()`): branch with commits + uncommitted + untracked; whitespace-only; rename; generated header; fallback on the default branch and with no default branch; no commits yet; `ReadAt` refusing refs/abbreviations/tree ids.
- `Classify` / `AreaOf` table tests.
- `handlers_overview_test.go`: mode validation, checkout from the state row, cache (`Times(1)`), 304, not-a-repo; `/file?rev=base` reads the merge base and the old path of a rename; `listedPath` accepts a path that's only in the branch overview.
- Manual: a scratch `mo web` (`XDG_CONFIG_HOME` pointing at a config with `[web] disable_tls = true`, so no login or TLS) on another port, driven in Chrome via the devtools MCP: light + dark + 390 px, kind hiding, area filter, mode toggle, opening a branch diff.

### Phase 1 notes

- Area tiles hold their files as a nested treemap: with only a few areas, area-only tiles said little.
- The `@generated` marker only counts as a comment line of its own; the first run flagged `overview_test.go` because a fixture string contained it.
- Switching mode while a poll was in flight left "Loading…" up until the next poll: the in-flight guard is per generation now.

## Phase 2 — detailed plan

### Package `internal/review`

New package (depends on `gitfiles` and `exec` only). `Analyze(ctx, cmd, o *gitfiles.Overview) (*Analysis, error)` takes the overview the handler already has cached and reads each changed file's two versions: the base side with `gitfiles.ReadAt(root, o.Rev, oldPath|path)` (skipped for `A`/`?`), the working side with `gitfiles.ReadFile` (skipped for `D`). `Overview` gains `Rev`: the commit compared with (the merge base, or HEAD in head mode). Capped at 300 analyzed files (`Truncated`).

**Architecture delta (Go).**
- Module path from the root `go.mod`; only imports inside the module count (others are dependency changes, see the surface).
- A package is a directory. `_test.go` files are left out: test-only imports aren't architecture.
- Per package, candidate added edges = imports in the changed files' working versions minus their base versions; removed = the reverse. A candidate only counts if no *unchanged* non-test `.go` file in that package imports it too (read from the working tree with `go/parser` `ImportsOnly`). Since only edges that appear or disappear are reported, existing violations are never flagged, so no baseline list is needed.
- Packages are `added` (every non-test file new), `removed`, or `changed`. `Existing` lists the touched packages' other in-module imports, for context in the graph.
- Each edge carries the files (and the import's line) that add or drop it.

**Rules** — `.unky-mo/architecture.toml` at the repo root (read through `gitfiles.ReadFile`, so the usual path rules apply):

```toml
[[layer]]
name = "gitfiles"
paths = ["internal/gitfiles"]       # path prefixes of the packages in this layer
allow = ["internal/exec"]           # if set: the only in-module imports allowed (besides the layer itself)
# deny = ["internal/web"]           # or: imports that aren't allowed
```

A package's layer is the longest matching `paths` prefix. An added edge that breaks its layer's rule gets `Violation` (the rule in words). A removed edge that broke it is marked `Fixed`. A missing file means no rules; a broken one is reported (`Rules.Error`) rather than failing the analysis.

**Contract surface** — each entry is `{op: + - ~, name, detail, path}`:
- **Exported Go API:** top-level exported funcs, methods on exported types (`Type.Method`), types, consts and vars of the changed non-test, non-generated files. They're compared per package by name and printed signature (`go/printer`), so a move between changed files of one package isn't a change. A type's detail is its first line (`type X struct`, interface method set changes show as `~`).
- **HTTP routes:** string literals that look like Go 1.22 mux patterns (`"GET /api/…"`), Express `app|router.get('/x'`, Rails `get|post|… 'x'` / `resources :x` lines in `config/routes.rb`.
- **CLI flags:** cobra/pflag `Flags().X("name"` / `XVar(&v, "name"`.
- **Config keys:** `toml:"…"`/`yaml:"…"` struct tags.
- **Env vars:** `os.Getenv("X")`, `os.LookupEnv`, `ENV["X"]`/`ENV.fetch("X")`, `process.env.X`.
- **Dependencies:** `go.mod` `require` lines, `package.json` `dependencies`/`devDependencies`, `Gemfile` `gem` lines — added, removed, or changed version.
- **Migrations / schema:** added or changed files under `db/migrate/`, `migrations/`, or named `schema.rb`/`structure.sql`/`schema.sql`.

Text-based categories compare the *set* of matches before and after, so a moved line isn't a change.

### Web

- Scoped interface `ChangeAnalyzer { Analyze(o *gitfiles.Overview) (*review.Analysis, error) }` in `deps.go` (`Deps.Review`), mocked.
- `GET /api/sessions/{windowID}/architecture?base=branch|head`: overview from `s.overview` (shared cache), then `archCache` (3 s), ETag + 304. Unknown mode → 400, not a repo → `{repo:false}`.

### Frontend

- The Overview tab also polls `/architecture` while visible.
- Chips gain "N rule violations" (red) or "+N −M imports".
- The tab badge shows the violation count.
- **Architecture** section: an SVG graph of touched packages and the endpoints of changed edges (toggle: also their other imports). Nodes are layered by import depth (importers on top), with edges colored new / removed (dashed) / violation (red) / existing (faint). Below it, a list of violations with the rule, each opening the importing file's branch diff.
- **Contract surface** section: one row per non-empty category, entries `+`/`−`/`~`, a click opens the file's diff.
- `layoutArchGraph(nodes, edges)` is pure (depth by longest path, order within a row by barycenter of parents).

### Tests

- `review` on real git repos in `t.TempDir()`:
  - a module with packages `a`, `b`, `c` on a branch;
  - added edge, edge already imported by an unchanged file (not new), removed edge, new package, test-only import ignored;
  - rules allow/deny/longest prefix/same layer/fixed;
  - exported decl add/remove/signature change/move between files;
  - surface regexes on fixtures (routes, flags, env, tags, go.mod/package.json/Gemfile diffs, migrations).
- `handlers_architecture_test.go`: shares the overview cache, ETag/304, mode validation.
- Manual: this branch adds `internal/review` (new package, `web → review` edge). Add `.unky-mo/architecture.toml` for unky-mo itself. Check a violation by temporarily denying `internal/web → internal/review`.

### Phase 2 notes

- Go comments are blanked out (`go/scanner`) before the surface patterns run: the first run on this branch listed the route and flag *examples* in `surface.go`'s own comments.
- `internal/web/mocks` sits under the `web` layer's path prefix, so a denied import shows up twice (once for `web`, once for its mocks), which is correct.
- unky-mo's own `.unky-mo/architecture.toml` encodes the layering CLAUDE.md describes (git readers and `review` only import downward, `ops` never imports a frontend or `status`, web and TUI never import each other). A rule against existing edges (e.g. TUI → tmux) can't be expressed usefully, because only new edges are checked.
- Tested by temporarily denying `internal/web → internal/review`: the red edge, the badge, the chip and the violation list all appeared.

## Phase 3 — detailed plan

### Intent trace (browser only)

- `chat.js` passes every transcript line it receives to `overview.onTranscriptLine(line)`, and `overview.resetTranscript()` when the stream is replaced (`/clear`, window switch). It also injects `describeUser` (its own `describeUserString`, so "what's a prompt" stays defined in one place) and `revealTurn(uuid)`.
- `createIntentTrace()` (pure, in `overview.js`) folds lines into turns and edits:
  - **Turn:** a user line `describeUser` calls a real prompt (`kind: "user"`), numbered from 1.
  - **Edit:** an assistant `tool_use` of `Edit`, `MultiEdit`, `Write` or `NotebookEdit` with a `file_path`/`notebook_path`. It belongs to the current turn, and its note is the assistant's last text before it in that turn ("Claude's explanation"). It's dropped again if its `tool_result` comes back `is_error`.
  - **Agent tool_uses** remember their turn, so a subagent's edits land on the turn that spawned it.
  - Lines are folded in file order regardless of conversation branch: an edit made on an abandoned branch still changed the file.
- **Subagents:**
  - The overview reads `/subagents` while the tab is visible.
  - For each agent spawned by a known tool_use, it fetches `/subagents/{id}/transcript?once=1`, a new one-shot JSON array of the same raw lines the SSE stream sends (`transcriptCursor.readNew` once, so the backend stays schema-agnostic).
  - Finished agents are fetched once; running ones at most every 15 s.
- **Paths** are made relative to the overview's `root`; edits outside the checkout are ignored.

### Trace UI

- A section between the contracts and the footprint.
- A table of files × turns:
  - Columns are the turns that edited a listed file (the last 20; older ones scroll horizontally).
  - Rows are the overview's files, grouped by the turn that first edited them (the group header is the prompt). Hidden kinds are respected.
  - A last group, "Not edited in this conversation", holds files changed by Bash (e.g. `make mocks`), by another session, or before this one.
- Clicking a cell shows a detail card with the file, the turn's prompt and Claude's note before the edit. Its actions are "Jump to turn" (switches to Chat and scrolls to the prompt, with a brief highlight) and "Show changes".
- Clicking a turn header jumps to that turn.

### Live strip

- `#overview-strip` above the subagent strip in the chat footer. It shows when the change needs attention: any new import that breaks a layer rule, or a spread of 4+ areas. For example: `This branch: 1 new import breaks a layer rule · 5 areas · Open Overview`.
- Fed by the same polls. While the Overview tab is hidden, they run every 15 s instead of 3 s.

### Dropped from the mockup

- The Files panel's "Group by area/turn" toggle. The Overview's own file list and the trace already group by area and by turn, so a second copy would duplicate `AreaOf` in JS.
- The TUI sidebar count. It would need the sidebar process to run the analysis; revisit if the strip proves useful.

### Tests

- `handlers_subagents_test.go`: `?once=1` returns the agent's lines as a JSON array, still only for an agent id the listing returned.
- `createIntentTrace` / `buildTraceRows` checked headless in node against synthetic lines:
  - a prompt, an edit, a failed edit, an edit before any prompt, a meta line that isn't a prompt;
  - a subagent edit landing on its spawning turn;
  - a path outside the root.
- Manual, in Chrome against this session: the trace, jump to turn, the detail card, the strip (by temporarily denying an import again).

### Phase 3 notes

- On this very session, most files show under "Not edited in this conversation": I edited them through Bash/python scripts, not the Edit tool. That's the case the group exists for, and a reason the drift check (phase 4) also looks at that group.
- Subagent attribution was checked live: a subagent spawned to write a probe file showed it under the spawning prompt, in subagent color.

## Phase 4 — detailed plan

### Ticket

- The browser takes the ticket key from the branch name (`[A-Z][A-Z0-9]+-\d+`, e.g. `rik/OP-212-web-cleanup` → `OP-212`).
- It finds the provider in `/api/tickets` (my tickets), falling back to `jira`.
- The server fetches the ticket itself (`TicketSource.Detail`, the id re-checked against the same pattern) and puts the title and description in the prompt. If the fetch fails, the check runs without a ticket and says so.

### Scope check

`POST /api/sessions/{windowID}/scope?base=` with `{ticket: {provider, id}?, turns: [{n, prompt, files}]}`, built by the browser from the intent trace. Files no edit touched go in as turn 0, "changed outside this conversation".

**The server bounds what reaches the model:**
- at most 80 turns;
- prompts cut to 2,000 characters;
- files must be in that mode's overview (anything else is dropped);
- at most 300 files.

One check runs per window at a time; another request gets a 409.

**`review.CheckScope` runs:**

```
claude -p --tools "" --strict-mcp-config --setting-sources project \
       --no-session-persistence --output-format json --model sonnet \
       --json-schema <schema> "<prompt>"
```

It runs in the OS temp dir, so no project settings, hooks or CLAUDE.md are picked up, and it has no tools and no MCP servers. `--setting-sources project` skips user settings, so unky-mo's own status hooks don't fire and no phantom session appears. `--no-session-persistence` keeps it out of the session history. The prompt is one argv element (no shell) and starts with fixed text, so it can't be read as a flag. Timeout: 3 minutes.

**Answer:** the envelope's `structured_output`:

```
{summary, files: [{path, verdict: "in_scope" | "drift" | "unclear", reason}]}
```

Paths the model invents (not in the request) are dropped.

- Scoped interface `ScopeChecker` in `deps.go`, mocked; `CheckScope` itself is tested with a mock `Commander` (argv, cwd, envelope parsing, error envelope).

### UI

- A "Check scope (against OP-212)" button in the trace header.
- While it runs: "Checking… (usually under a minute)".
- The result is kept per window in localStorage with the request's signature. If the change moved on, a note says "The change moved on since this check — run it again."
- Drift files get a `drift` tag (reason on hover) in the trace and the file list. A result card shows the summary, each drift file with its reason, and "Ask Claude to split these out", which puts a ready-made prompt in the composer (not sent).
- Chips gain "N files outside the ask" (yellow). The strip adds "N files outside the ask".

### Tests

- `review/scope_test.go`:
  - prompt building (ticket, turns, the outside-conversation group);
  - envelope parsing (`structured_output`, `is_error`, missing output);
  - invented paths dropped;
  - argv/cwd checked through a mock `Commander`.
- `handlers_scope_test.go`:
  - files outside the overview are dropped before the checker sees them;
  - a bad ticket id isn't fetched;
  - a ticket fetch failure still checks;
  - a second concurrent check gets a 409;
  - only POST is accepted.
- Manual: one real check on this branch from Chrome.

### Phase 4 notes

The first real runs on this branch drove three fixes:

1. **Only prompts with edits were sent.** The model said it lacked the earlier prompts. Every prompt now goes in as context, even ones without edits; past 80, the most recent are kept.
2. **Files changed outside the conversation came back "unclear".** Telling the model to judge them by path made it call `chat.js`/`editor.js` drift, though both changed for this feature. Those files now get a short diff excerpt each (1.5 KB each, 40 files / 40 KB total, the file's start when untracked), computed server-side from the overview's `Rev`.
3. **With excerpts, the check was accurate.** It answered in ~16 s: on task, with the two small incidental refactors (shared ETag helper, wider `notInHEAD` match) named as supporting.

Also checked: "Ask Claude to split these out" (with an injected drift result) only fills the composer; nothing is sent.

## Later

- A session ↔ ticket link that isn't the branch name (e.g. from the TUI's ticket view).
- Import graphs for JS/TS (relative imports) and Rails (Packwerk, if the MoMA repos use it).
- The TUI sidebar showing the strip's count.
- Concern grouping beyond turns (an LLM pass), if the trace's turn grouping proves too coarse.
