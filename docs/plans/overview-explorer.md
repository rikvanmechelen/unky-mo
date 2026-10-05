# Overview Explorer (design 9a, "Overview Explorer v3")

Source: Claude Design project `3c743cfe-be90-4bc6-bd2e-6ee1eb44a0b5`,
`Overview Explorer v3.dc.html` + `overview-model.js` (mock data: Alarm,
Healthy, Huge and Reviewer states). v3 is v2 (8a) with most of what v2
dropped put back. This plan maps v3 onto the code we have, lists what it
needs that doesn't exist yet, and what today's Overview (or the chat view
around it) does that v3 still leaves out.

## What the design is

The Overview stays a tab in the middle pane. While it's the active tab it
takes over both side rails, and everything selects by entity id.

- **Left rail (340px)**: compact session switcher (28px rows: status
  square, project, branch, "here"/"working"/"needs you"), then a **nav bar**
  (back / forward, breadcrumbs of the last 4 selections, Clear), then the
  **inspector**, which shows whatever is selected. Usage meter at the
  bottom. With nothing selected it shows **Start here**: the kinds bar and a
  Checks list (lines of logic, areas, layer rules, call findings, contract
  changes, files changed outside the conversation, each clickable). In the
  reviewer view the session list becomes a list of the project's open PRs
  ("viewing" on the current one).
- **Middle**: a scrolling page.
  - **Header**: PR line in the reviewer view (`#231 title · Not checked out
    (read-only) · Open on GitHub`). A **Branch / Uncommitted N / Selected
    (N) ×** switch. What's compared (`left vs right @ sha (merge base) · N
    files · +a −d`). "Reviewed X of Y" progress bar. Notes (stale base +
    Fetch, "no merge base, compares against HEAD~5", "file list
    truncated"). The **verdict sentence** with linked phrases, plus "N of M
    changed lines are logic". **Chips**: files, lines, areas, dependencies
    "+a −r · b bumped", contract changes. A **Caveats N** disclosure: rules
    file (or its parse error), approximate languages, unparsed files,
    "packages start folded", unresolved calls. Once something is selected
    the header **shrinks to a sticky strip**: smaller verdict, no sub-line,
    no chips.
  - **Four collapsible sections**, each with a one-line summary and a
    red/yellow flag when collapsed:
    - **Map**: packages in layer rows. Each box lists its changed (and
      context) functions with marks (+ − ~ sig ↦ ctx). Import edges run
      between boxes and call edges between function rows. Each box folds
      from its arrow ("+12 functions"). Toggles: Existing imports, All
      calls, **Fold unrelated** (on by default), Isolate selection. Legend:
      new, breaks a rule, dropped, approximate (dotted), passed as a value
      (dashed), interface, existing. Over 60 functions, packages start
      folded and open when the selection touches them. **Focus** on a
      function swaps the map for a one-hop lens (callers | function |
      callees or implementations, route labels, unresolved notes) with an
      "All packages › Focus: X · Exit" breadcrumb.
    - **Footprint**: kinds bar (click a kind to hide it from the list and
      the treemap), a treemap of areas → files (area = lines changed,
      coloured mostly added / removed / mixed / noise), and Files by area.
      Clicking an area **filters the Review list**, which shows an "Area: x
      ×" chip.
    - **Intent**: the files × prompts matrix (square = edit count, purple =
      subagent, yellow = outside the conversation). Selecting a cell
      (`cell:<path>|<n>`) shows that edit's reason in the inspector.
      Hidden in Uncommitted / Selected and in the reviewer view.
    - **Scope**: Check scope (idle / running / done), when it ran and what
      it was checked against, ticket fetch errors, "Out of date … Run
      again", the drift list, "Ask Claude to split these out".
  - **Adjusting to the selection**: selecting something opens the section it
    belongs to (fn/call/imp/pkg/con/find → Map, file → Footprint,
    prompt/cell → Intent), collapses the others to their one-line summary
    and scrolls there. A manual open or close holds until the next
    selection.
- **Right rail (320px)**: the Files panel's tabs become **Review N /
  Changed N / All files / Git log · N** (reviewer view: Review / Files).
  We build them as **Review / Changed / Branch files / Git log**: in
  Overview mode the rail shows only the change, so "All files" becomes the
  branch's changed files (flat, by path) instead of the repo tree. The
  Git log tab stays ours (see "What v3 still leaves out", 1).
  - **Review** groups: Needs eyes (red findings, rule-breaking imports,
    warnings, drift files with their reason), Contracts, Logic to read
    (biggest first, with tick boxes), then collapsed Tests (plus `untested`
    findings), Docs, Generated and noise.
  - **Changed** is uncommitted files only, as today.
  - Filter chips (area, hidden kinds) sit above the list.
  - Footer: the keys, plus the sync line ("1 commit to push").
- **Reading code at three depths**: an inspector row shows the call line.
  **Peek** unfolds a few lines around it, and **Diff** / **File** open the
  whole file as an editor tab next to Overview at that line. Editor tabs
  carry a diff/file label, and inside the tab there's a **Diff | File**
  switch plus **Show in Overview**.
- **Keys** (Overview active, focus not in a field): j/k or ↑/↓ walk the
  Review list, x ticks reviewed, o opens the diff, f the file, `[` `]` go
  back / forward. Esc leaves an editor tab, then exits Focus, then clears.
  **Decided:** `[` / `]` keep toggling the side rails; back / forward are
  **Alt+← / Alt+→** plus the nav bar's buttons (titles say so).
- **Phone**: no rails. The inspector is a bottom sheet (62% high) that
  opens while something is selected.

Entities: `pkg:`, `file:`, `fn:`, `call:`, `imp:`, `con:`, `find:`,
`prompt:`, `cell:`. `related(id)` drives hover and selection highlighting
in the map, the Review list and the inspector.

## Where the data comes from

| Design element | Today | Gap |
|---|---|---|
| files, kinds, areas, +/−, mode switch, stale base, fallback, truncated | `/overview` | — |
| packages, import edges, existing imports, surface/contracts | `/architecture` | layer of each package (B3) |
| functions, marks, calls, findings, implementations, labels, unresolved | `/calls` | test that reaches a function, signature text (B2); `/calls` is only loaded for the Functions view today (F1) |
| caller rows with the call's code line, Peek, inspector code blocks | — | **excerpt endpoint** (B1) |
| contract → function | `surface` has `path` + `line` | match inside a func's `line..end`, client side |
| "Why it changed", matrix cells, prompt → files | `createIntentTrace`, `turnForRange`, `buildTraceRows` | — |
| "git rm (Bash)", "another session" origins | "not edited in this conversation" | optional heuristic (F10) |
| scope verdicts, ran-at, against, ticket error, staleness | scope check (localStorage) | — |
| caveats | rules/presets note, lang `exact`, unparsed, truncated, analyzer errors | — (collected client side) |
| dependency chip | surface `Dependencies` items | count +/−/~ client side |
| reviewed ticks + progress | — | new, localStorage (F6) |
| reviewer PR list in left rail | dashboard's PR list (`PRClient`, cached) | small endpoint or reuse `/api/projects/{name}` (F11) |
| PR header line | reviewer target already has PR title | — |
| sync line ("1 commit to push") | Files panel ahead/behind | — |

### Backend steps (unchanged from the v2 plan)

- **B1. Excerpts.** `GET …/excerpt?path=&line=&ctx=3&side=new|old` (same
  `?base=` modes and selection params as `/file`, same `listedPath` +
  `gitfiles.Resolve` rules). Context functions in unchanged files must be
  readable too, so "listed" widens to "in the tree at that side". It
  answers rows `[{ln, oldLn, sign, text}]`: the file's diff against the
  overview's base, cut to `line ± ctx`, so a changed region shows its +/−
  rows and an unchanged one plain lines. One `git diff -U<n>` per file,
  cached with the overview's key. A removed function asks `side=old`.
  Tests: `handlers_excerpt_test.go` (refusals before any git call, a
  rename's old path) + a `gitfiles` real-git test.

  **B1 detail.** One shape covers every target: an `Overview` compares
  `Rev` (empty without commits) with `Head` (a commit) or, when `Head` is
  empty, the working tree. So:
  - `gitfiles.Annotate(ctx, cmd, o, path) (*Annotated, error)` returns the
    whole file as rows `{ln, oldLn, sign, text}` (`ln` 0 on a `-` row,
    `oldLn` 0 on a `+` row). For a file in `o.Files` (by `Path`, or by
    `OldPath` for the old side of a rename):
    - untracked (`?`) or no `Rev`: the new side, all `+`;
    - deleted (`D`): `ReadAt(Rev, path)`, all `-`;
    - otherwise one `git diff -M --no-color --no-ext-diff --no-textconv
      -U<huge> Rev [Head] -- [oldPath] path`, parsed by a pure
      `parseFullDiff` (handles `\ No newline`, rename headers, binary).

    A file the change doesn't list (a context caller) is read from the new
    side (`ReadAt(Head)` or `ReadFile`, so `Resolve` + `O_NOFOLLOW`) as
    plain rows with `ln == oldLn`. Binary and over-`MaxContentBytes` come
    back flagged, without rows.
  - `(*Annotated).Cut(line, end, side, ctx)`: anchor on the row whose `ln`
    (side `new`) or `oldLn` (side `old`) is `line`, through `end` (default
    `line`), widened by `ctx` rows (0–20, default 3), at most 400 rows
    (`truncated`). A line that isn't there gives no rows.
  - `GitFiles.Annotate(o, path)` (mock regenerated). The handlers cache the
    `*Annotated` in `excerptCache` (10 min, the 128-key prune), keyed by
    `Root`, `Rev`, `Head` and the path, plus, for a working-tree side, the
    file's size and mtime, so an edit is never served stale.
  - Routes: `GET /api/sessions/{windowID}/excerpt` (same `base` /
    `commits` / `worktree` params, read through `s.change`) and `GET
    …/branches/{branch}/excerpt` / `…/pulls/{pr}/excerpt` (through
    `targetOverview`). Params `path`, `line` (≥1), optional `end` (≥ line,
    ≤ line+400), `side` (`new` default, `old`), `ctx`. Bad params → 400
    before any git call.
  - The path must be listed: in the overview's files (`Path`/`OldPath`), or
    else in the new side's tree (`listedPath` for a checkout; `refTree`
    for a ref target). Otherwise 404 and no `Annotate` call.
    `ErrOutsideRoot` → 403.
  - Answer: `{path, side, rows, binary?, tooLarge?, truncated?}` through
    `writeHashed` (ETag + 304).
  - Tests: `gitfiles/excerpt_test.go` (parser fixtures, `Cut` edges, real
    git with modified / renamed+modified / deleted / untracked / unchanged
    files, a commit head vs the working tree, a symlinked unchanged path
    refused) and `web/handlers_excerpt_test.go` (refusals before git, a
    listed path through each route, the old path of a rename, a ref
    target's tree, the cache key changing on a write, 304).
- **B2. Calls additions.** `Func.TestedBy`: the first test function
  `reachedByTest` found, so the inspector can say "Reached by
  TestHandleOverview". `Func.Sig` / `Func.OldSig` for `signature` (Go: the
  types-only signature the exported API diff already computes; heuristic
  languages: the definition's first line).

  **B2 detail.**
  - `Func.TestedBy *TestRef` (`{id, name, path, line}`): `reachedByTest`
    returns the test it found instead of a bool. `callDelta` runs it for
    the same changed functions as today (not removed, tests, generated,
    entry points or `conventional` ones) whenever the language loaded
    tests. Reached → `TestedBy` on the function's node; not reached → the
    `untested` finding, as today. It's the first test the 0-1 BFS
    discovers, so the nearest one, ties broken by caller ID (callers are
    now sorted, so it's stable between polls). A function in a test file
    counts as a test, helpers too, so `rootTest` walks up from a helper
    through test-file callers to a test nothing calls, and keeps the
    helper as `TestRef.Via` ("TestGoCalls via callGraphOf").
  - `Func.Sig` / `Func.OldSig`, only for status `signature`: the
    definition's header in each version, read as text the same way for
    every language. A post-pass in `Calls` (it has both indexes, and the
    old `fn` from `before` under the same ID) runs `defHeader(text, line)`.
    That skips lines that are only an annotation (`@x`, `@x(…)`), joins
    lines until `(`/`[` balance (at most 8 lines), collapses whitespace and
    cuts at the body: the first `{` outside brackets (not `{}`), or for
    Python the first `:`. Capped at 300 characters. No scanner or
    symbol-cache change. Go's `Line` is the `func` keyword, the others'
    their definition line.
  - Tests: `calls_test.go` covers `defHeader` (multi-line params, an
    annotation, a nested paren, the cap) and `TestedBy` on hand-built sets
    (direct, two hops, through an interface, none → `untested` and no
    `TestedBy`). `gocalls_test.go` checks a real signature change's
    `Sig`/`OldSig` and a real `TestedBy`; `pycalls_test.go` checks one
    heuristic `Sig`/`OldSig`.
- **B3. Layers.** `Package.Layer` (from `ruleSet.layerOf`) and
  `Rules.Order` (layer names top to bottom), so the map can lay rows out by
  the repo's layers. Packages with no layer fall back to import depth
  (`layoutArchGraph`).

  **B3 detail.** A rules file's order isn't a row order (unky-mo's own
  lists its layers bottom-up, and presets have none), so the server names
  layers and the browser orders rows:
  - `Analysis.UnitLayers map[unit]layer name`, for every unit the analysis
    mentions: its packages and both ends of its edges and existing edges,
    each judged with its own language (`layerOf(unit, lang)`, so a preset's
    layer only names its language's units). Units in no layer are left out.
    This replaces the planned `Package.Layer`: context units at the end of
    an edge need a layer too.
  - `Rules.Order`: every layer's name in the rule set's order (the repo's
    own, then the presets', as `"rails: models"`), without duplicates.
  - The map (F7) groups units by layer, orders the rows by import depth
    (a layer whose units import another's sits above it), breaks ties by
    `Rules.Order`, and puts unlayered units, and units only `/calls`
    names, in rows by their own depth.
  - Tests: `review_test.go` checks `UnitLayers` (a changed package, a
    context unit at the end of an existing edge, an unlayered one left out,
    a preset layer only for its language) and `Rules.Order` (file order,
    then presets).
- **B4. Unit names.** Check that `/calls` `Func.Unit` equals
  `/architecture` `Package.Path` for every language (Go's `pkgDir` vs
  `goarch` paths especially), and fix it at the source if not: the map nests
  functions in packages by that key.

  **B4 result.** Checked on every language's fixture: the call analyzers
  name units with their architecture language's own `unit` (Node, Python
  and Ruby from the path alone; Kotlin and Swift through the detected
  instance), so changed functions always land in a listed package. Two
  gaps, both fixed:
  - Go interface implementations were bare nodes (no path, no unit):
    `implsOf` now registers them in `l.callees`, so `addCallees` gives
    them a context node with their position and `pkgDir` unit.
  - Context functions are often in units `/architecture` never mentions
    (an unchanged package, a test folder, a Rails controller), so B3's
    `UnitLayers` couldn't place them. `CallGraph.UnitLayers` now names the
    layer of every function's unit (`loadRules` again in `Calls`; the map
    merges both maps).
  - `unitalign_test.go` keeps it so: for each language's fixture, every
    changed function's unit is one of `/architecture`'s packages and every
    function has a path and (outside the root) a unit. `TestCallUnitLayers`
    checks the layers of context units.

### Frontend steps

Each step is one commit, and the old Overview keeps working until F9 swaps
it over. A step gets its detailed plan in this doc before coding
([[plan-each-step-first]]).

- **F1. Entity model** (`static/overview-model.js`, pure, no DOM). It
  builds `{E, order, layers}` from `/overview` + `/architecture` + `/calls`
  + the trace + the scope result + reviewed ticks, in the shape of the
  mock's `build()`. It also provides `related(id)`, `where(id)` →
  `[path, line, side]`, `verdict(M)` → segments, `checks(M)`, `caveats(M)`,
  `chips(M)`, `reviewQueue(M, {area, hiddenKinds})` and `sectionOf(id)`.
  Load `/calls` whenever the tab is visible: the map, verdict, Review list
  and badge all need it. Until it lands, the map shows packages from
  `/architecture` with function-row skeletons. Unit-tested the way the
  graph layout is.

  **F1 detail.**
  - **Backend addition:** `Analysis.FileUnits` (`path → unit`) for every
    changed file a language owns, test files included (a deleted file by
    its old path's unit). The browser can't guess it from paths: a Kotlin
    file in `app/src/main/kotlin/…` is in unit `app/data`. The first
    language wins. Tested in `review_test.go`.
  - **`static/overview-model.js`** (no DOM; loaded before `calls.js` and
    `overview.js` on both pages). The pure helpers move into it unchanged:
    `createIntentTrace`, `turnForRange`, `buildTraceRows`,
    `scopeRequestFrom`, `oneLine`, `summarizeOverview`, `layoutTreemap`,
    `archGraph`, `layoutArchGraph`, and the constants `OVERVIEW_KINDS`,
    `OVERVIEW_KIND_LABEL`, `OVERVIEW_NOISE`, `SURFACE_KINDS`,
    `LANG_LABEL`. A `module.exports` at the end exports them for Node.
  - **`buildModel({overview, arch, calls, trace, scope, reviewed})`**
    returns `M = {E: Map(id → entity), order, units, orphans, …}`. Any
    input but `overview` may be null (still loading). Entities:
    - `pkg:<unit>`: status (added/changed/removed/context), lang, layer
      (`unitLayers` from either answer), label (`arch.labels`), fns, files.
    - `file:<path>`: the overview file plus unit, fns, prompts (`{n,
      edits, agent, notes}` from the trace), drift (`{verdict, reason}`),
      `outside` (no edit in this conversation), reviewed.
    - `fn:<id>`: the `/calls` func plus a mark, callers/callees (call ids),
      impls/implOf (fn ids, from `impl` edges), findings, contracts, and
      `turn` (`turnForRange`).
    - `call:<from>><to>|<kind>|<op>`: every non-`impl` call, with findings
      whose sites are its sites.
    - `imp:<from>><to>|<op>`: changed edges as new / removed / broken /
      fixed, existing ones as `ctx`, with `approx`.
    - `con:<category>:<i>`: surface items, matched to the function whose
      `path` + `line..end` contains them.
    - `find:<i>`: call findings with a severity. Red is `removed-called`,
      `stimulus-unbound` and `route-without-action`; warn is
      `signature-callers` and `test-not-updated`; fold is `untested`.
    - `prompt:<n>`: turns with their files.

    Cells (`cell:<path>|<n>`) aren't stored; `related`/`where` read them.
  - **Queries:** `related(M, id)` (the design's sets), `where(M, id)` →
    `{path, line, side}`, `label(M, id)`, `sectionOf(id)` (map / foot /
    trace / scope), `verdict(M, ctx)` → `[{t, tone, target}]`, `checks(M)`,
    `caveats(M)`, `chips(M)`, `reviewQueue(M, {area, hidden})` → groups,
    and `reviewSig(file)`.
  - **`overview.js`:** loads `/calls` whenever the tab is visible, not only
    for the Functions view, so the badge and strip count red findings
    sooner. Nothing else visible changes.
  - **Tests:** `internal/web/jstests/overview_model.test.js` (`node
    --test`, outside the embedded `static/`) builds models from small JSON
    fixtures: ids, kinds, `related` for each type, `where`, `verdict` and
    `checks` on an alarm and a healthy change, the review queue's groups
    and filters, and the moved helpers (`layoutArchGraph`, `turnForRange`,
    `buildTraceRows`). `jstest_test.go` runs it from `go test` and skips
    when `node` isn't on PATH.

- **F2. Selection store** in `overview.js`: `select(id)`, history (30),
  back/forward, `hover(id)`, Focus. Listeners re-render the inspector, map,
  Review list and sections. Selection and history are kept per target in
  sessionStorage.

  **F2 detail.**
  - **History in the model file:** a pure value `{stack, at}` with
    `selPush` (no-op for the current id; drops the forward part; at most 30
    entries; `null` is "nothing selected", a real step so Back can return
    to it), `selBack`, `selForward`, `selCurrent` and `selCrumbs` (the last
    4 selections up to the current one, oldest first, without nulls).
    Unit-tested in Node.
  - **`overview.js` holds the store:** `history`, `hover`, `focus` (a
    function id for the map's Focus mode, cleared by any selection that
    isn't a function, call or finding, as in the design), and subscribers
    called on every change. History is per target in sessionStorage
    (`mo.overview.sel.<key>`), restored by `setTarget`.
  - **The model is rebuilt lazily:** `model()` rebuilds when an input
    changed (a new overview / architecture / calls body, the trace's
    version, a new scope result, reviewed ticks), tracked by a version
    counter, not on every read.
  - **API returned by `createOverview`:** `model()`, and `selection =
    {select, back, forward, hover, setFocus, current, hovered, focus,
    crumbs, canBack, canForward, subscribe}`. F3–F7 render from it.
  - **Keys** while the Overview is visible and focus isn't in a text
    field or dialog: Alt+← / Alt+→ go back / forward (preventDefault, so
    the browser doesn't navigate: Alt+← isn't one of Chrome's reserved
    shortcuts. DevTools' synthetic keys showed the handler cancelling it,
    but they never reach the browser's own shortcuts, so a real keypress
    still needs a manual check). Esc exits Focus, then clears the
    selection. `[` / `]` stay with the rails.
  - Nothing new is drawn yet: F3 adds the first consumers.

- **F3. Page shell + header.** Every section keeps a skeleton while its
  data loads (decided): today's `overviewSkeleton` / `archSkeleton` /
  `surfaceSkeleton` / `callsSkeleton` move into their sections, the
  inspector and the Review list get one too, and all fade in after 150 ms. The scrolling page, the sticky compact
  header, the mode switch (today's Branch / Uncommitted / Selected (N) ×,
  including `worktree=1`), the compare line, notes (stale base + Fetch,
  fallback, truncated), verdict, chips, Caveats. The four section frames
  with summaries, flags, open/collapse-on-select and scroll-to. Bodies are
  today's parts moved in, untouched for now: architecture graph + Functions
  view → Map, chips/noise/treemap/files by area → Footprint, trace table →
  Intent, scope check → Scope.

  **F3 detail.**
  - **The panel is one scrolling page.** `.overview` loses its padding:
    the header runs edge to edge with a 2px ink rule under it, then the
    sections, each with a 1px rule.
  - **Header (`pageHead`):**
    - The mode row: today's Branch / Uncommitted / Selected (N) × switch,
      the compare text (today's `header()` content), and a hidden slot for
      F6's progress.
    - Notes as yellow bars: stale base + Fetch, the fallback, a truncated
      list, a load error.
    - The verdict sentence (`ovVerdict`, 26px bold): linked phrases select
      their target on click and hover it on mouseover; red phrases in red
      text, warn ones on yellow.
    - The verdict sub-line, the chips (`ovChips`, clickable when they have
      a target) and the Caveats N disclosure (`ovCaveats`, open state per
      page).
    - Once something is selected the header gets `is-compact`: sticky at
      the top, a 19px verdict, no sub-line, no chips.
    - While `/architecture` or `/calls` is out, the chips and caveats each
      show a bone (skeleton) in their place.
  - **Sections** (`section(key, title, body)`): a full-width head button
    (chevron, title, `ovSectionSummaries` summary, flag square) and a body.
    - **Map:** today's `contracts()` (architecture or functions + the
      contract surface).
    - **Footprint:** the noise bar + treemap + files by area.
    - **Intent:** the trace table; chat view, not Selected mode.
    - **Scope:** the scope button and card, moved out of the trace head;
      both views, not Selected mode.
  - **Open/closed:** `open(k) = manual[k] ?? (selected ? k ===
    ovSection(selected) : true)`. A head click sets `manual[k]`; a new
    selection (or Back/Forward/Focus) clears `manual`, re-renders and
    scrolls the selection's section to just under the sticky header. The
    treemap is drawn only while Footprint is open; the ResizeObserver
    redraws it when it reopens.
  - **`ovSectionSummaries(M, {scope, hidden, area, focus})`** in the model
    file (unit-tested) gives each section's summary and flag:
    - Map: packages · changed functions · unresolved · focus; red when an
      import breaks a rule or a call won't work.
    - Footprint: areas · lines of logic · kinds hidden · area filter.
    - Intent: prompts · files outside the conversation.
    - Scope: not checked / checking / N outside the ask / fits · out of
      date; yellow when files drift.
  - **Skeleton:** before the first `/overview` answer, `overviewSkeleton`
    lays out the new head (mode row, a verdict bone, chips) and four
    section heads.

- **F4. Left rail in Overview mode.** `.chat-shell.is-overview`:
  `renderNav` gets a compact mode, `--nav-w` widens to 340px, and
  `#inspector` (nav bar + body) fills the rest. Switching session keeps the
  Overview tab active there. Start here: kinds bar + Checks. The rail
  tabs and `[` / `]` work as today.

  **F4 detail.**
  - `createOverview` takes `onVisible(v)`; `chat.js` toggles
    `.chat-shell.is-overview` and the inspector's `hidden` with it.
  - **Overview mode CSS:** the nav widens to 340px (`--nav-w`, and its
    children's fixed width) and the Files panel to 320px. The session list
    becomes compact rows: no project headings, each row shows its project
    (a new `.nav-session__project`, hidden outside Overview mode) and
    branch, 28px high. The list is capped at about a third of the rail and
    scrolls, so the inspector gets the rest. `[` / `]` and the rail tabs
    work as before.
  - Switching session from the nav while the Overview shows keeps it
    showing in the new window (`switchTo` calls `editor.showOverview()`).
  - **`static/inspector.js`, `createInspector(host, overview, {onOpen})`:**
    - **Nav bar:** Back / Forward (disabled at the ends, titled with
      Alt+← / Alt+→), breadcrumbs (`selection.crumbs()`, labels from
      `ovLabel`, click → `selection.go`) and Clear (Esc).
    - **Body:** with nothing selected, Start here: the kinds bar (clicking
      a kind hides it from Footprint and Review, shared with Footprint's
      legend via `overview.toggleKind`) and the Checks list (`ovChecks`, a
      tone square, label, sub-line; click selects the target). A selected
      entity gets a minimal view for now: kicker (type and unit), title,
      status chip and Diff / File actions (`overview.open(id, how)`). F5
      fills in the rest.
    - It re-renders on select / focus / target and on a new model, which
      `render()` announces with `selChanged("model")`; not on hover.
    - An id the model no longer has (the change moved on) shows "No longer
      in this change" with Clear.
  - **`overview.open(id, how)`:** `ovWhere` plus today's `openDiff` rules
    (bdiff / diff / range / sdiff by mode). `how === "file"` opens a file
    tab at the line; an old-side line opens the diff without a line,
    since the working copy doesn't have it.
  - Reviewer view: no nav, so no inspector until F12.

- **F5. Inspector per entity** (`static/inspector.js`): function, call,
  import, file, package, contract, finding, prompt, cell, following the
  mock's `inspector()`. Code blocks use B1, and Peek toggles per
  (selection, site). Actions: Diff / File, Focus, Mention in prompt, "Ask
  Claude to fix" (drafts, never sends, like `onDraftPrompt`), Jump to prompt
  in Chat (`revealTurn`), Mark reviewed. It keeps today's extras:
  Implementations / Implements, unresolved count, a reference's `label`,
  "N more callers", subagent edits.

  **F5 detail.**
  - **`overview` gains:**
    - `excerpt(path, line, {side, end, ctx})`: a Promise of B1's answer,
      with the tab's mode params, cached per overview ETag. `peekExcerpt`
      gives the cached value synchronously, so a re-render doesn't flicker.
    - `revealPrompt(n)`, `mention(text)` and `draft(text)`, each null when
      the page can't do it (the reviewer view has no chat to draft in).
  - **Blocks** (`inspector.js`):
    - Chips (red / warn / green / plain / mono).
    - Alert bars (red / warn / note edge).
    - Signature before/after (`sig`, `oldSig`).
    - Code block: a title, a head with `path:line` and Diff / File, and
      rows from `excerpt` (line numbers of the side asked for, +/- tint,
      the focus line marked). Bones until it lands, "Not available" on an
      error or a binary file.
    - "Why" block: prompt number (selects the prompt), its text, Claude's
      note in quotes.
    - Related list: mark or square, name (selects), a sub-line, and for
      call sites the call's own line (`excerpt` ctx 0, first 8 rows) with
      Peek (ctx 3, per selection and site) / Diff / File.
    - Action buttons.
  - **Per type,** following the design's `inspector()` on our data:
    - **Function:** status and test chips (`testedBy` "Reached by X (via
      Y)", or "No test reaches it" with an `untested` finding);
      findings as alerts; unresolved note; signature; definition (`line ..
      min(end, line+10)`, old side when removed); Implementations /
      Implements; Called by (+ "N more"); Calls with their sites, kinds and
      `label`; Contracts; Why it changed (`turn` → prompt + that file's
      notes).
      Actions: Focus, Diff, File, Mention in prompt, Ask Claude to fix
      (with a red/warn finding).
    - **Call:** kind chips + label; findings; where the caller makes it
      (site, old side when removed); where the callee is defined;
      caller/callee; findings.
    - **Import:** kind chip; rule / fixed / approximate alerts; the import
      line; other sites; calls across it (same units); the two packages;
      Ask Claude to fix when broken.
    - **File:** dir, status, kind, +/−, drift alert; first change (the first
      +/- row of `excerpt(path, 1, end 400, ctx 0)`, then ±3 around it);
      functions in it; Edited by (prompts with notes) or "Not edited in
      this conversation"; contracts.
    - **Package:** status, layer, language; imports / imported by (changed
      ones); functions (changed first, 14 + "N more"); changed files.
    - **Contract:** op chip, detail; where it's defined; "Who relies on it":
      the defining function's callers; defined in.
    - **Finding:** severity kicker, sentence title; signature for
      `signature-callers`; the first site's code, more sites as a list;
      the function (or what was removed); Ask Claude to fix.
    - **Prompt:** text; files it edited (edits, subagent, note); functions
      it changed; Jump to prompt in Chat.
    - **Cell:** edit count, subagent, the notes, the file's first change,
      the file and the prompt.
  - **Pure helpers in the model file (Node-tested):** `ovCallWords(call)`
    (new / removed / existing, as a value, via interface, inferred),
    `ovFindingTitle(M, find)`, `ovFixPrompt(M, id)` (what "Ask Claude to
    fix" drafts: the problem, the places, "Please fix it."),
    `ovFirstChange(rows)`.

- **F6. Review tab** in the Files panel (`files.js` gets a `review` mode,
  rows from `reviewQueue`). It exists **only while the Overview tab is
  active** (decided). Opening the Overview switches the panel to Review, and
  leaving brings back the panel's previous tab. In Overview mode the panel's
  tabs are Review / Changed / Branch files / Git log: the repo tree ("All
  files") isn't offered there, since the rail only shows the change. Quick
  open (Ctrl+P) still reads `/tree` and works everywhere. The gutter
  review-comments button in the tab strip is renamed **Comments (N)** in
  the same commit, so "Review" means one thing. Filter chips (area, hidden kinds). Reviewed ticks
  live in `mo.overview.reviewed.<key>` as `{path: sig}`, where sig is the
  file's `+added −removed status` (or a blob id if `/overview` gets one), so
  a file that changes after you ticked it un-ticks itself. Keys
  j/k/x/o/f/Esc and Alt+← / Alt+→. The progress bar goes in the header.

  **F6 detail.**
  - **Files panel tabs:** `chat.html` gains a Review tab (first) and a
    Branch files tab, both hidden outside Overview mode; All files hides in
    it. `files.js` gets `setOverview(queue | null)`. With a queue it
    remembers the current tab, switches to Review and shows Review /
    Changed / Branch files / Git log. With null it puts the remembered tab
    back and hides the two.
  - **`static/reviewqueue.js`, `createReviewQueue(overview)`:**
    - **Review:** `ovReviewQueue(M, {area, hidden})` groups (Needs eyes,
      Contracts, Logic to read, then folded Tests / Docs / Generated and
      noise; a folded group opens while it holds the selection). Rows by
      type:
      - files: a tick box in Logic to read, otherwise the status letter;
        the name, the dir, +/−, and a drift tag;
      - findings: a severity square and the title;
      - imports: a red square and from → to;
      - contracts: the op and the name.
    - **Branch files:** every file of the change, by path.
    - Click selects, double-click opens the diff. The selected row is
      inverted; with something selected or hovered, related rows get a bar
      and the rest dim.
    - Filter chips above the list (Area: x ×, N kinds hidden ×).
    - The tab's count is the Needs eyes count. A Review list that's empty
      while the change has files says why.
    - It re-renders on select, hover (coalesced per frame) and model.
  - **`overview` gains** `area()` / `setArea(a)` (the Footprint filter,
    now shared), `clearHidden()`, and `toggleReviewed(path)`. Ticks are
    stored as `mo.overview.reviewed.<key> = {path: reviewSig}`; a file
    whose +/− or status changes loses its tick.
  - **Progress:** "Reviewed X of Y" and a bar in the header's mode row
    (`ovProgress`). The file inspector gets Mark / Unmark reviewed.
  - **Keys** (Overview visible, not in a field, no dialog): j/k and ↓/↑
    step through the shown Review rows (from the first when nothing is
    selected), x ticks the selected file, o opens its diff, f its file.
    The Files footer lists them while in Overview mode.
  - **Rename:** the tab strip's comments button becomes "Comments (N)" and
    its dialog "Comments for Claude".

- **F7. Map** (`static/overview-map.js`) replaces the Map section's body:
  layer rows of package boxes with function rows, `edgePath` curves, the
  four toggles (remembered like `allImports`), the legend, per-box fold,
  auto-fold past 60 functions, and related-set dimming. Call edge styles
  stay as today: `dynamic` = open arrowhead, `ref` = dashed, `approx` =
  dotted. Box headers keep today's per-unit new/removed call counts.

  **F7 detail.**
  - **Pure layout in the model file (Node-tested):**
    - **`ovMapUnits(M, {existing})`:** the packages to draw. Every changed
      one, every unit that holds a changed function or a function called
      by or calling one, and both ends of the change's imports (and of
      existing imports with `existing`).
    - **`ovMapFns(M, unit)`:** a box's functions: changed ones first,
      ordered so callers come before what they call
      (`layoutArchGraph`), then context ones by name.
    - **`ovLayoutMap(M, {width, set, fold, foldUnrelated, existing})`:**
      - **Rows:** each layer (`pkg.layer`; a unit with none is a group of
        its own) is a node, placed by `layoutArchGraph` over the imports and
        calls between layers, so importers sit above what they import. A
        row holds the units of the layers at that depth, by
        `Rules.Order`, then path. A line holds as many 240–300px boxes as
        fit, then wraps.
      - **Folding** (as the design): a box folds when the user folded it
        (`fold[unit] === false`), or when more than 60 functions changed
        and it isn't related to the selection, or with Fold unrelated on
        and something selected that it isn't related to.
      - **Rows per box:** an open box shows at most 12 functions, plus any
        the selection relates to, unless opened in full (`"all"`).
      - Returns `{W, H, boxes: Map(unit → {x, y, w, h, collapsed, shown,
        rest})}` and `rowY(fnId)` (a hidden function's edges end at its
        box's head).
  - **`static/overview-map.js`, `createMap(overview)`:** boxes are HTML,
    absolutely placed over one SVG of edges (as the design).
    - **A box:** a fold chevron, its name (selects the package), a "new" /
      "context" tag, the layer as a muted second line; dashed when context,
      green when new. Rows show the mark, the name and a red / yellow
      square for a finding. A last row reads "+N more — show all", or for a
      folded box "N functions · M changed — open".
    - **Edges:**
      - imports between boxes: new green, breaks a rule red and thick,
        removed red dashed, fixed green dashed, existing grey (only with
        Existing imports on);
      - calls between function rows: new green, removed red dashed,
        existing grey; `dynamic` gets an open arrowhead, `ref` is dashed,
        `approx` dotted.
      - Calls shown: with nothing selected, those carrying a red finding
        (as the design: drawing every new call buried a 50-function change
        in green curves); with a selection, the related ones; with All
        calls on, every call between visible rows.
      - Each edge has a wide invisible hit path, a tooltip and a
        click-to-select; the selected one gets a halo.
    - **Highlighting:** `ovRelated(hover || selection)`; the rest dims
      (0.3 boxes, 0.35 rows, 0.14 edges; Isolate selection takes them
      almost to 0). A click on the background clears the selection.
    - **Toolbar:** the Packages | Functions switch (kept), then Existing
      imports, All calls, Fold unrelated (on by default) and Isolate
      selection, remembered in localStorage `mo.overview.map`. Then the
      legend and "N calls couldn't be resolved".
    - It redraws on select / hover (one per frame) / model and on a width
      change (ResizeObserver).
  - **The Map section's body** becomes toolbar + map (Packages) or
    toolbar + today's `calls.js` view (Functions). The contract surface
    and the violation rows leave it: contracts are in Review and the
    inspector, violations in Needs eyes and the import inspector, the rules
    note in Caveats.
  - **Skeleton:** `archSkeleton` until `/architecture` lands; then boxes
    with bone rows until `/calls` lands.
  - No supported language: the note from today's Architecture section.

- **F8. Focus lens**: `calls.js`'s one-hop layout, re-skinned as the Map's
  Focus mode (callers | function | callees or implementations), with
  breadcrumb and Exit. **Decided:** the Packages | Functions switch stays
  in the Map section's toolbar: Packages is the new map (F7), Functions is
  today's `calls.js` view (re-skinned, unit collapse past 60, per-unit
  +/− counts), useful for very large changes. Clicking a node there
  selects the `fn:` entity like the map does.

  **F8 detail.**
  - **`ovLens(M, fnId)`** in the model file (Node-tested): `{callers:
    [{id, call}], right: [{id, call | null}], rightTitle, implementations}`.
    For an interface method that has implementations, the right column is
    the implementations ("Implementations N"); otherwise its callees
    ("Calls N"). Callers include the `ref` and `dynamic` ones; the context
    the server cut (`moreCallers`) is reported.
  - **`overview-map.js`** draws the lens instead of the map while
    `selection.focus()` names a function the model has, in both the
    Packages and the Functions view (Focus is reached from the inspector):
    - a bar: "All packages › Focus: name · Exit" (Exit = `setFocus(null)`;
      Esc does the same);
    - three columns (each up to 260px, fitting the width) of 40px rows
      (mark, name, `path:line`), the focused function in the middle and
      inverted;
    - edges styled like the map's, `ref` labels (a handler's route)
      written at the edge's middle, implementations as dotted lines;
    - notes: "Nothing calls it in this change", "and N more callers",
      unresolved calls.
    - A click on a row selects it, which moves the focus to it if it's a
      function (`followFocus`), so you can walk the graph one hop at a
      time; Back returns.
  - The Map section's summary already says "focus on X". The Functions
    view's own Focus (`calls.js`) stays as it is.

- **F9. Footprint, Intent, Scope restyled** to the design. Footprint:
  hide-by-kind applies to the Review list too, and an area click sets the
  Review filter. Intent: matrix cells become selectable entities. Scope:
  ran-at / against / error / stale / drift / split. Then remove the old
  bodies (chips row, noise bar, trace table). `calls.js` stays (F8).

  **F9 detail.** The sections' parts select entities instead of opening
  diffs at once; the inspector then offers Diff / File. A double-click
  still opens the diff.
  - **Footprint:**
    - The kinds bar and its legend (click a kind to hide it) now also hide
      that kind's tiles in the treemap.
    - An area label filters the Review list and Files by area (the shared
      `areaFilter`), with "Showing X in the Review list · Show all areas".
    - A tile or a row selects its file. The selected file's tile and row
      are outlined, related ones marked, the rest dimmed when something is
      selected.
  - **Intent** (the matrix):
    - A cell selects `cell:<path>|<n>`, which the inspector already
      shows: the edits, the notes, the file's first change, Jump to prompt.
      Today's inline cell detail (`traceDetail`) goes.
    - A column number selects `prompt:<n>`; a file name selects the file.
    - The selected cell, row and column are marked.
    - The legend gains the yellow "not edited in this conversation" mark
      for the untraced group's rows.
  - **Scope:**
    - Run button (primary) with the design's line ("The one part that
      asks a model. It only runs when you click.").
    - The answer: the summary, "Checked HH:MM against …", the ticket error,
      "Out of date … Run again", and the drift files as rows that select
      the file (with the reason). Then "Ask Claude to split these out".
  - **Cleanup:** remove what nothing uses any more: `traceDetail` and
    `selected`, and the `.overview-why*` and `.overview-violation*` CSS.

- **F10. (Optional) Origin of untraced files**: scan the trace's Bash
  tool_use commands for the path, `git rm`, `go generate`/`go get`/`make
  mocks`, to say "git rm (Bash)" instead of "not edited in this
  conversation". It's a heuristic, so it's worded as "probably".

  **F10 detail.**
  - `createIntentTrace` also records Bash tool calls as `commands: [{turn,
    command, agent}]`, a subagent's on the turn of its Agent call, and
    drops one whose result `is_error`, like edits.
  - **`ovGuessOrigin(file, commands)`** in the model file (Node-tested),
    latest command first:
    1. **It mentions the file:** its path, or its name as a whole word
       (quoted, after `/`, `=` or a space, before a quote, space or end
       of line) → "Bash".
    2. **A deleted file:** `git rm` / `rm` with its name → "git rm (Bash)".
    3. **A regenerator for its kind:**
       - generated mocks: `mockgen` / `make mocks` / `go generate`;
       - `go.mod` / `go.sum`: `go get` / `go mod`;
       - JS lock files: `npm` / `yarn` / `pnpm`;
       - `Gemfile.lock`: `bundle`;
       - whitespace-only files: `gofmt` / `goimports` / `prettier` /
         `rubocop -a` / `black`.

    The answer is `{n, command (first line, 120 chars), how}`, or null.
  - **`buildModel`** sets `file.origin` for files no edit touched.
    - **The file inspector's "Edited by":** "Probably Bash in prompt N"
      with the command, instead of "Not edited in this conversation", and
      the prompt number selects the prompt.
    - **The trace's untraced rows:** the guess as the yellow mark's
      tooltip.
    - Worded "probably": it's a guess from text.

- **F11. Editor tab Diff | File switch** + "Show in Overview": swaps a tab
  between `bdiff`/`range`/`sdiff` and `file` in place (same path, same
  line), and Show in Overview selects `file:<path>`. In Selected mode,
  File shows the selection's head side read-only.

  **F11 detail.**
  - **Diff | File switch:** every editor tab but a `commit` one gets a
    two-button switch in its bar. It replaces today's "Open file" (diff
    tabs) and "Changes" (file tabs) buttons.
    - **File** opens the plain file tab at the line the cursor is on (a
      diff tab's editable side is the working copy, so its line numbers
      are the file's).
    - **Diff** opens the change the Overview shows, at that line:
      `overview.diffKind()` gives `bdiff` (branch), `diff` (uncommitted)
      or `range` / `sdiff` with the selection's ids (Selected). Without an
      Overview it's `diff`.
    - The switch takes the tab's place: the new tab opens next to it, and
      the old one closes unless it has unsaved edits.
  - **Show in Overview** (when the page has an Overview) shows the
    Overview tab and selects `file:<path>`. A file the change doesn't list
    just shows the Overview.
  - Wiring: `createEditorTabs` takes `diffKind()` and
    `onShowInOverview(path)`; `chat.js` and `branch.js` pass them.

- **F12. Reviewer view** (`branch.html`) gets the three columns. Left: the
  project's open PRs (switching re-targets the page), nav bar and
  inspector. Middle: the PR header line. Right: Review + Files. No Intent
  section; Scope stays.

  **F12 detail.**
  - **`branch.html`** becomes the chat view's grid
    (`.chat-shell.is-overview.has-files`):
    - **Nav:** the wordmark and Dashboard link, a list, and the inspector.
    - **Middle:** today's branch bar, editor tabs and Overview.
    - **Files panel:** two tabs, Review and Files (the branch's changed
      files), and the keys line.
  - **The list** is the project's open pull requests (`/api/projects/
    {name}/prs`, cached server-side) as compact rows (`#n` + title), the
    one being viewed marked "viewing"; for a branch target, the branch
    itself first. A failed fetch (e.g. `gh` not signed in) shows the error
    as a note under the branch.
  - **`branch.js`** wires `createInspector`, `createReviewQueue` (with a
    small tab controller of its own, since `files.js` polls a session) and
    `createRails`, so `[` / `]` and the rail tabs hide the rails here too.
    The Overview is always the home tab, so the page stays in Overview
    mode.
  - No Intent section (no transcript); Scope stays.

- **F13. Phone**: the inspector as a bottom sheet while something is
  selected; map boxes stack in one column. Review becomes a section under
  the page (the design has no right rail on a phone).

  **F13 detail.**
  - **Inspector as a bottom sheet (≤ 760px, where the nav is hidden):**
    in Overview mode the nav becomes a fixed sheet at the bottom, 62% of
    the viewport high, holding only the inspector (no wordmark, sessions or
    usage). It slides up while something is selected (the inspector marks
    itself `has-sel`) and down when the selection clears (the bar's Clear,
    or Esc).
  - **A Review section** (key `review`) in the Overview page, after
    Scope. It renders the same queue (`createOverview` takes
    `reviewList(container)`; `chat.js` and `branch.js` pass the queue's
    `review`). It shows only where the Files panel doesn't: below 1100px,
    or with the right rail hidden (`.hide-files`). It doesn't follow the
    selection's open / close (it's the list you pick from), only its head.
    Its summary: "N need eyes · M logic files".
  - **Smaller type:** the verdict at 20px (17px compact), section padding
    16px, the map at the panel's width (`ovLayoutMap` already puts one box
    per line below ~530px).

- **F14. Docs**: CLAUDE.md (Overview paragraph), testing.md, this plan's
  status.

## What v3 still leaves out (and how to keep it)

v3 brings back most of what v2 dropped: the mode switch, the stale-base
Fetch, notes, caveats, the dependency count, the treemap, hide-by-kind,
files by area, the matrix and its cell detail, the full scope check, Focus,
unit folding past 60, implementations, labels, unresolved counts and
Changed as uncommitted-only. What's still missing:

1. **Git log tab**: the design draws a tick-list of commits. Today's tab is
   a lane graph with expandable commits, read-only `commit` tabs,
   Ctrl/Shift-click ranges, keyboard selection, the "Uncommitted changes"
   row and the consecutive-run check. → **Decided:** keep ours unchanged,
   including the selection → "Show in Overview" → Selected (N) flow.
2. **Selected + uncommitted** (`worktree=1`): the design's Selected is
   commits only. → Keep it in the switch ("Selected (N) + uncommitted").
3. **All files** in the design is a flat list of *changed* files. Today it's
   the whole repo tree. → **Decided:** in Overview mode the rail shows only
   the change ("Branch files"); the tree comes back with the normal panel
   when you leave the Overview. Quick open still covers the tree.
4. **Intent in Uncommitted mode**: v3 hides Intent outside Branch mode. The
   trace still makes sense for uncommitted edits (today it shows). → Show
   it in Uncommitted, hide it only in Selected.
5. **Name clash**: the tab strip already has **Review (N)** for gutter
   review comments ("Send to Claude"). A second "Review" in the Files panel
   would be confusing. → Rename the comments button **Comments (N)**: it
   holds notes you send to Claude, while the Files panel's Review is the
   reading queue the design is built around (F6).
6. **`[` / `]`** toggle the side rails today, and v3 makes them back /
   forward. → **Decided:** they keep toggling the rails; back / forward are
   Alt+← / Alt+→.
7. **Loading states**: the design has none. → **Decided:** keep the
   shimmering skeletons per section (F3), since `/calls` can take seconds.
8. **Contract cap**: today it shows 40 per category + "N more"; the Review
   Contracts group has no cap. → Keep the cap inside the group.
9. **`#overview-strip`** above the composer and the Overview tab badge
   aren't drawn (the design never shows Chat). → Unchanged.
10. **Phone Review list**: v3 hides the right rail on a phone, so there's no
    review queue there at all. → F13 adds it as a section.
11. **Layer rows** assume every package has a layer. Repos with no rules
    file (or only presets for one language) need the import-depth fallback
    (B3).
12. **Analyzer errors / timeouts** (a timed-out analysis is an error today):
    not drawn. → An alert in Caveats and on the Map section's summary.

## New things the design adds

Inspector + selection history, a page that adjusts to the selection
(section focus + sticky header), verdict sentence, Caveats disclosure,
reviewed ticks and progress, Review queue with keys and area filter,
combined package + function map with related-set highlighting, Fold
unrelated and Isolate, Peek excerpts, the call's code line in caller rows,
positive test coverage ("Reached by …"), signature before/after, contract →
function → callers, Diff | File switch inside an editor tab, "Ask Claude to
fix" on findings and broken imports, selectable matrix cells, PR switcher
in the reviewer view, phone bottom-sheet inspector.

## Decisions (2026-10-05)

1. The Packages | Functions switch stays (F8).
2. `[` / `]` keep toggling the side rails; back / forward are Alt+← / →.
3. The tab strip's review-comments button becomes "Comments (N)"; "Review"
   is the Files panel's reading queue.
4. The Review tab exists only while the Overview tab is active.
5. Our Git log tab stays as is, with its commit selection.
6. In Overview mode the right rail shows only the change (Review, Changed,
   Branch files, Git log); no repo tree there.
7. Skeletons stay for every loading part.

## Status

Revised for v3 on 2026-10-05 (the v2 version was never built). B1–B4 (the
backend) F1–F13 built 2026-10-05; next: F14.
