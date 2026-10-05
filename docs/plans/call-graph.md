# Call graph of a change

## Goal

The Overview's architecture graph shows how a change moves dependencies between **units** (packages, Rails layers, …). Add the same view one level down: **functions**. For the change, show:
- which functions it adds, removes, renames or changes (body or signature);
- which calls between functions appear or disappear;
- who calls the changed functions (the blast radius) and what they call;
- findings a reviewer would otherwise miss: a removed function that's still called, a changed signature whose callers the change doesn't touch, a changed function no test calls.

It should work for every language the architecture checks support (Go, Rails, Node, Python, Kotlin/Java, Swift), in the chat view and the reviewer view, under the same constraint as `architecture-languages.md`: **projects need nothing extra installed**.

## Deep dive: koknat/callGraph

### What it is

- One Perl script (~1500 lines), GPL-3.0. Last commit Nov 2024.
- Runtime needs: Perl, the CPAN `GraphViz` module and Graphviz `dot`. JSON/YAML output also needs `JSON::XS` / `YAML::XS`.
- Input: files or directories on disk, **one language per run** (from extension, shebang or `-language`).
- Output: a Graphviz image (png/svg/pdf/dot). `-jsnOut`/`-ymlOut` dump `{"file:func": {calls: {"file:func": n}, called_by: {...}}}`.
- Options: `-start <re>` (root nodes), `-ignore <re>`, `-fullPath`, `-cluster` (group by file), `-writeSubsetCode` (extract the graphed functions into one file), `-verbose` (Perl/Tcl globals).
- Languages: awk, bash, basic, dart, fortran, go, julia, js/ts, kotlin, lua, matlab, pascal, perl, php, python, R, raku, ruby, rust, scala, swift, tcl, verilog. C, C++ and Java are explicitly unsupported.

### How it works (from the source)

1. **Per language, four regexes** (`defineSyntax`): function definition, function end, call, comment.
   - Go def: `(\s*)(func)\s+(?:\(.*?\))?\s*(\w+)`. Go call: `(\w+)\s*\(`.
   - Python def: `(\s*)(def)\s+(\w+)\(`. Ruby call: `(\w+)\s*`, so every word on a line counts as a call.
2. **Line-by-line scan** (`parseFiles`):
   - Joins `\`-continued lines.
   - Cuts each line at the comment marker. A `#` or `//` inside a string cuts the line too, and `/* */` blocks aren't handled.
   - A definition line pushes the function on a stack with its indentation. The function ends at the first "end" line (`}`, `end`, or any non-blank line in Python) **at that same indentation**.
   - Python can't nest: a `def` resets the stack, so methods and nested functions are flattened.
   - Every call-regex match on a line is a "potential call" from the function on top of the stack.
3. **Name-only resolution**: a potential call is kept only if *some* function has that name.
   - If the name is defined in the same file, it resolves there.
   - Else, if exactly one other file defines it, it resolves to that file.
   - Otherwise it's **silently dropped** (the "AMBIGUOUS" message is commented out).
4. Roots are functions nobody calls (or `-start` matches). The graph is drawn from the roots with Perl's GraphViz.

### Measured on our own code

Run on `internal/review` (21 non-test files, 67 ms, with a stub `GraphViz.pm` so it could run):

- **The interface dispatch the package is built around disappears.**
  - `refs`, `unit`, `owns`, `detect`, `exact` and `name` are each defined in 6 files (one per language), so every `lang.refs(...)` call in `edgeDelta` is dropped.
  - The graph shows `edgeDelta` calling `add`, `sortedKeys`, `oldPath` and `check`, but not the six analyzers it actually drives.
  - 10 of 135 names are ambiguous, and those are exactly the ones a review would care about.
- **Same-file-first gives wrong edges.** A call to `x.refs()` in `ruby.go` resolves to `ruby.go:refs` whatever `x` is.
- **Methods are bare names.** `(*side).add` and any other `add` are the same node. Receivers and classes are lost.
- On small, self-contained files (`engine.go` + `rules.go` + `review.go`) the result was right. It works for scripts, as its README says.

### Why we're not using it

| Need | callGraph |
|---|---|
| No extra installs | Needs Perl + CPAN GraphViz (+ JSON::XS for JSON) |
| Read the base, a PR ref or the working tree from git blobs | Reads files on disk only; we'd have to write temp trees |
| A *delta* (added/removed calls, changed functions) | Draws the call graph of one version, whole files |
| Mixed-language repos (Rails + Stimulus, Kotlin + Java) | One language per run; no Java |
| Methods, classes, receivers, imports | Bare names; imports ignored |
| Honest about what it can't resolve | Ambiguous calls silently dropped |
| Comments and strings never produce references (our scanners' rule) | Line-cut comments only; strings are scanned as code |
| Our own interactive SVG, linked to diff tabs | Graphviz image |
| License | GPL-3.0: shelling out is fine; porting its code would make that part GPL |

Even with Perl present, its output can't be diffed reliably: dropped ambiguous calls appear and disappear as names gain or lose a second definition. Porting its regexes would carry over its accuracy problems, and the code would be GPL.

Its idea is worth keeping: find definitions, find call sites, and resolve names against definitions. We already own better building blocks for all three:
- **Comments and strings:** `jsCode`, `pyCode`, `rubyCode`, `jvmCode` and `swiftCode` blank out comments, strings, heredocs, template and regex literals, and keep byte offsets and lines 1:1.
- **Imports:** Node's `resolveSpec` and Python's `targets` already return the imported *file*, not just a unit.
- **Exact Go parsing:** `go/parser` + `go/types` are in the standard library.
- **Reading any version from git, the blob-id `symCache`, `twoPhase` scan/resolve, a time budget, `Truncated`:** all already in `internal/review`.

### Other options considered

- **Language servers** (gopls, pyright, ruby-lsp, sourcekit-lsp) "call hierarchy". Rejected as the default: they need each toolchain installed, only see the working tree (not a base or a PR ref), and take seconds to start.
- **`golang.org/x/tools/go/callgraph` (CHA/VTA)**. Rejected as the default: it loads through `go/packages`, which runs `go list` and needs the toolchain, the module cache and a checkout. It's a possible later upgrade (phase 6).
- **tree-sitter grammars on `wazero`**. Exact *syntax* for every language, at roughly 5–10 MB more binary. It doesn't solve *resolution*, which is where the heuristics are weakest. Kept as the phase 6 fallback for a language whose function boundaries the scanner gets wrong.

## Measurements behind the design

`go/types` type-checking from source with a custom importer (scratch benchmark, non-test files, build tags honoured):

| Module | Packages | Parse | Parse + type-check |
|---|---|---|---|
| unky-mo | 26 | 30 ms | 86 ms |
| golang.org/x/tools | 218 | 170 ms | 390 ms |
| aws-sdk-go (huge generated code) | 890 | 2.2 s | 6.3 s |

- **Faking the standard library and third-party packages costs almost nothing.** Packages outside the module become empty packages: 2,432 in-module calls resolved with fakes vs 2,434 when stdlib is type-checked from GOROOT source. GOROOT source makes it 17× slower (1.6 s) and needs the Go toolchain. So: fake everything outside the module, no toolchain needed.
- **Build tags must be honoured.** Ignoring `//go:build` (generators tagged `ignore`, per-OS files) creates import cycles, and without a guard the importer recursed until the stack overflowed. A stack overflow is fatal in Go and can't be recovered, so it would take down `mo web`. The loader needs both: `go/build` file matching and an in-progress guard that breaks cycles.
- **Interface dispatch is common.** In unky-mo, 308 of ~2,740 in-module calls go through an interface, so `dynamic` edges are a first-class case, not an edge case.
- **Reading files at a revision is the hidden cost.** `gitfiles.ReadAt` runs 3 git processes per file (`cat-file -t`, `-s`, `blob`). A whole-tree caller scan of a PR (a ref target, nothing on disk) would spawn thousands. That needs a batch reader (`git cat-file --batch`), which would also speed up today's architecture analysis on ref targets.
- **Claude Code's transcript carries edit line ranges.** `Edit`/`Write` results have `toolUseResult.structuredPatch` (`oldStart`, `oldLines`, `newStart`, `newLines`), so functions can be mapped to the prompt that changed them (phase 5).

## Shared design

### What a "function" is

A `Func` is a definition with a stable **ID** that survives a file move:

| Language | ID |
|---|---|
| Go | `types.Func.FullName()` with the module prefix cut: `internal/review.edgeDelta`, `(*internal/review.goLang).refs` |
| Python | `pkg/mod.py:Class.method` / `pkg/mod.py:func` (module path, `.py` kept so it reads as a path) |
| Ruby | `Const::Path#method` / `Const::Path.method` (Zeitwerk name from the path) |
| JS/TS | `src/x/file#name` / `#Class.method` (`#default` for default exports) |
| Kotlin/Java | `package.Class.method` / `package.fn` (overloads share a node) |
| Swift | `Module.Type.method` / `Module.fn` |

Besides the ID, a func has:
- `Name`: the display name (`goLang.refs`, `User#full_name`);
- `Path`, `Line`, `End` in its version, plus `Unit`, from the architecture analyzer, so both graphs line up;
- `Lang` and `Test`;
- two hashes:
  - **body hash:** tokens of the body only (Go: `go/scanner`; others: the blanked code with whitespace collapsed), so a comment or formatting edit isn't a change;
  - **signature hash:** the types-only signature (Go, like `typeSig`), or the parameter list text (others).

Status:
- `added` / `removed`: the ID exists on one side only;
- `renamed`: an added + removed pair with the same body hash and the same unit (shown once, `from → to`);
- `signature`: the signature hash differs;
- `changed`: the body hash differs;
- otherwise untouched: shown only as context.

A changed file whose old path differs (a rename) maps its base functions through `OldPath`. A function that moves between files keeps its ID, so it isn't a change, the same rule `edgeDelta` applies to moved imports.

### Calls

A call is `{From, To, Kind, Op, Sites []EdgeFile}`.
- **`Kind`:**
  - `static`: resolved exactly;
  - `dynamic`: an interface or virtual call, drawn to the interface method;
  - `impl`: interface method → a known implementation (dotted, collapsed in the UI);
  - `ref`: a function used as a value, not called (Go method values like `s.handleOverview` passed to the mux, `before_action :load_user`, `onClick={save}`). These are how handlers get wired, so they count;
  - `approx`: resolved by a heuristic.
- **`Op`:** `+` / `-` for a changed edge, `""` for context.
- **Unresolved calls** are counted per function (`Func.Unresolved`), never drawn and never guessed.

### The delta (`callDelta`)

1. Build the **before** set: the functions and outgoing calls of the changed files at the base revision, resolved against the base tree.
2. Build the **after** set: the same for the changed files in the new version, plus a whole-tree **caller index** (callee ID → call sites) of the new version.
3. Function status by ID and hashes, as above.
4. **Edges of changed functions:** for each caller that exists on both sides, edges only after are `+` and only before are `-`. A caller that's added or removed brings all its edges as `+` or `-`. Calls in unchanged files can't change, so they're never diffed.
5. **Context, 1 hop, new version:**
   - callers of every changed, signature or removed function, from the caller index (capped at 50 per function, then `+N`);
   - callees of every changed or added function that the change doesn't touch.
6. **Findings:**
   - **`removed-called`:** a removed function the new tree still calls. The new side's unresolved call sites are kept with their spelled name (`pkg.Fn`, `Fn`, `obj.method`), and a removed function's name is matched against those in files that could see it (its package and importers). Red, counted on the badge and in the strip. For Ruby and Python this is a runtime error waiting to happen; for Go it's a compile error, still worth flagging mid-session.
   - **`signature-callers`:** a signature change whose callers sit in files the change doesn't touch. Yellow, with the call sites.
   - **`untested`:** a changed or added non-test function that no test function reaches within 2 hops. A grey hint, never red: it can't see table-driven or reflective tests.

### Where it runs

- `review.Calls(ctx, cmd, o *gitfiles.Overview) (*CallGraph, error)` lives next to `Analyze` and shares `repo`, `index` and `symCache`.
- Per language, an implementation of:

```go
// callLang builds one version's functions for a language.
type callLang interface {
	name() string
	exact() bool
	// funcs returns the functions (with resolved calls) of the given files
	// in the version idx describes. full adds the whole tree's calls for the
	// caller index; tests includes test files.
	funcs(idx *index, files []string, full bool) (*callSet, error)
}
```

  Go implements it with `go/types` over whole packages; the heuristic languages with a cached per-file `scan` plus a per-analysis `resolve`, the same `twoPhase` split the architecture analyzers use.
- Budget: its own `callTimeout` (20 s) in `deps.go`, and response caps (400 functions, 50 callers per function). Past either, the result is `Truncated`. A timed-out analysis is an error, never a partial graph, as with `Analyze`.
- Every per-language analyzer runs under `recover()`: a panic in one language drops that language with an error in `Languages`, it doesn't fail the request.

### API

A separate endpoint, not a field on `/architecture`: callers mean a whole-tree scan, and the architecture section shouldn't wait for it.
- `GET /api/sessions/{windowID}/calls?base=branch|head`
- `GET /api/projects/{name}/branches/{branch}/calls`, `/pulls/{n}/calls`

```go
type CallGraph struct {
	Funcs     []Func        `json:"funcs"`      // changed + context
	Calls     []Call        `json:"calls"`
	Findings  []CallFinding `json:"findings"`
	Languages []LangInfo    `json:"languages"`
	Truncated bool          `json:"truncated,omitempty"`
}
```

## Phases

1. **Batch reader, engine and Go (exact).** Backend and endpoint, no UI.
2. **UI.** A Packages / Functions switch on the architecture graph, focus layout, detail panel, open at line, mention in prompt, badge and strip.
3. **Python + JS/TS.** Heuristic, import-aware.
4. **Ruby/Rails, Kotlin/Java, Swift.**
5. **`mo calls` CLI, intent trace link, tuning on real repos.**
6. **Upgrades, only if measurements call for them.**

---

## Phase 1 — detailed plan

### 1a. Batch blob reads

- `internal/exec`: add to `Commander`:
  ```go
  // OutputStdin is Output with stdin fed from in.
  OutputStdin(ctx context.Context, dir string, in []byte, name string, args ...string) (stdout, stderr []byte, err error)
  ```
  Implement it in `realCommander`, then `make mocks`. Existing mocks gain the method, so no test changes.
- `internal/gitfiles/blobs.go`:
  ```go
  // ReadBlobs reads blobs by object id in one `git cat-file --batch`.
  // Missing ids are absent from the map. Blobs over maxFileSize are skipped.
  func ReadBlobs(ctx context.Context, cmd moexec.Commander, root string, oids []string) (map[string][]byte, error)
  ```
  - Parses `<oid> blob <size>\n<bytes>\n` records, and `<oid> missing`.
  - Every id must match `^[0-9a-f]{40,64}$`, or it's refused before git runs. Ids only ever come from `ls-tree`/`ls-files -s`.
  - Chunks of 500 ids per process.
- **`index`:**
  - **Revision-side indexes:** `newIndex` takes an explicit revision, so the base tree can be indexed too.
    - `newIndex(r)` (after side) keeps today's behaviour.
    - `newIndexAt(r, rev)` is used for the base side (`r.rev`).
  - **`prefetch(paths []string)`:** batch-reads the blobs of the listed paths into a per-analysis `map[path]string`.
    - `read` checks that map first.
    - For the working tree, tracked files whose blob id matches HEAD's could come from git too. Simpler, and enough: only rev-side indexes prefetch, and the working tree keeps reading from disk.
  - **Size limit:** `ls-tree -r -l` adds the blob size, so files over 2 MB (`gitfiles` cap) are skipped without being read.
- **Side win:** `edgeDelta`'s unchanged-file reads on ref targets go through `prefetch` too. Measure the architecture analysis of a PR in moma-org-rails before and after.

### 1b. Engine: `internal/review/calls.go`

Exported types (JSON):

```go
type Func struct {
	ID, Name, Path, Unit, Lang string
	Line, End  int
	Status     string // added|removed|renamed|signature|changed|"" (context)
	From       string // renamed: the old ID
	Test       bool
	Unresolved int
}
type Call struct {
	From, To string
	Kind     string // static|dynamic|impl|ref|approx
	Op       string // + - ""
	Sites    []EdgeFile
}
type CallFinding struct {
	Kind  string // removed-called|signature-callers|untested
	Func  string // ID
	Sites []EdgeFile
}
```

Internal types:

```go
type fn struct {
	Func
	body, sig  string     // hashes
	calls      []callSite // resolved: to ID, kind, line
	unresolved []string   // spelled names of calls that didn't resolve
}
type callSet struct {
	funcs   map[string]*fn       // ID → fn
	callers map[string][]callRef // callee ID → caller ID + site (full sets only)
	byPath  map[string][]*fn     // file → its functions
}
```

- **`Calls(ctx, cmd, o)`:**
  1. Changed files come from `o.Files`, with the same `analyzed` filter and `maxAnalyzed` cap as `Analyze`.
  2. Build `newIndex(r)` and `newIndexAt(r, r.rev)`.
  3. Run `detect` on the architecture `language`s to learn which call languages apply. That reuses Go's module path, Node's tsconfig and Ruby's Zeitwerk index.
  4. For each call language:
     - `before := funcs(baseIdx, changedOld, false)`;
     - `after := funcs(idx, changedNew, true)`;
     - then `callDelta(before, after, changed)`.
  5. Merge the languages, apply the caps and sort, as `sortChanges` does.
- **`callDelta`** follows "The delta" above. It's a pure function over two `callSet`s, so most rules are tested without git.
- **`untested`:** walk the caller index backwards up to 2 hops from each changed function, looking for a `Test` caller. That needs test files in the full set (see Go below).

### 1c. Go: `internal/review/gocalls.go`

**Loader:**
- The loader is per side and per analysis:
  ```go
  type goLoader struct{ idx *index; module string; pkgs map[string]*goPkg; busy map[string]bool; fset *token.FileSet }
  ```
- **Which files:** it lists a directory's `.go` files from `idx.under(dir)` (direct children only). Files are kept with `build.Context.MatchFile`, using a copy of `build.Default` whose `OpenFile`/`ReadDir` hooks read from the index, so the exact `go build` rules apply for the host GOOS/GOARCH. `CgoEnabled = false` plus `FakeImportC`.
- **Importer:**
  - **In-module paths:** type-check the directory from source, memoized.
  - **A path already being checked** (a cycle): return an empty, complete package instead of recursing.
  - **Everything else:** an empty `types.NewPackage(path, guessName(path))`, marked complete. `guessName` drops a `/vN` suffix and a `go-` prefix. A wrong guess only makes external selectors invalid, which we ignore anyway.
- **Type-check settings:** `types.Config{Importer: l, Error: func(error){}, FakeImportC: true}` and `parser.SkipObjectResolution | parser.ParseComments`. Errors are expected, collected and ignored.
- **Which packages:**
  - the changed packages on that side;
  - for the full (after) set, the **direct reverse importers** of the changed packages. Only they can call into them; their own imports load lazily through the importer.
  - Reverse importers come from a module-wide import map built from the `refs:go` cache entries `edgeDelta` already fills (per blob id), so no extra parsing.
  - Cap: 300 packages, then `Truncated` (aws-sdk-go's 890 packages take 6 s; typical changes load far fewer).
- **Tests:** after the normal pass, the full side also type-checks each changed package together with its `_test.go` files, and its external `_test` package. Their functions are `Test: true`. That's enough for `untested`; tests of reverse importers aren't loaded.

**Walk (per `FuncDecl`):**
- The ID is `obj.FullName()` with the module prefix trimmed. Methods on generic types use `Origin()`.
- A package-level `var` initializer belongs to a pseudo-function `pkg.init`. All of a package's `init` funcs merge into `pkg.init`.
- Calls and refs are found by `ast.Inspect` over the body, including func literals, which belong to the enclosing declaration:
  - **`CallExpr`**, after unwrapping parens and an `IndexExpr` (generic instantiation):
    - `*ast.Ident` → `Info.Uses` must be a `*types.Func` (in-module) → `static`;
    - `*ast.SelectorExpr` with a `Selection` whose receiver is an interface → `dynamic` to the interface method's ID (`(pkg.Iface).Method`);
    - otherwise `Uses[sel.Sel]` → `static`;
    - anything else (a func value, a conversion, a builtin, an external function) → unresolved (only counted when the spelled name could be in-module, i.e. not `len` or `fmt.X`).
  - **Any other in-module `*types.Func` use** (`Uses` that isn't the `Fun` of a call) → `ref`.
- **Hashes:**
  - body: `go/scanner` over `decl.Body`'s byte range, joining token text (comments dropped);
  - signature: the receiver type + `fieldTypes`/`results` from `goarch.go`, from the AST, so it stays stable when external types are fake.
- **`impl` edges:** for each interface method that is the target of a changed or context `dynamic` edge, the named types of the loaded packages for which `types.Implements(T)` or `types.Implements(*T)` holds give `impl` edges to `T.Method`. Computed only for those interfaces.

**Caching:** ASTs aren't cached across analyses (memory), since the benchmark says a re-check is cheap. If big repos say otherwise, phase 6 adds per-package fact caching keyed by the blob ids of the package and its in-module deps.

### 1d. Web

- `deps.go`:
  - `CallGrapher interface { Calls(o *gitfiles.Overview) (*review.CallGraph, error) }`, appended to the mockgen list;
  - `Deps.Calls`;
  - `realCallGrapher` with `callTimeout = 20 * time.Second`.
- `handlers_calls.go`:
  - `handleCalls` (session) and `handleBranchCalls` (reviewer), sharing `serveCalls(w, r, key, get)`, a copy of `serveArchitecture`'s shape: marshalled bytes cached, then `writeHashed` for the ETag/304.
- **Cache: `callCache`, not a short TTL.**
  - A computation can take seconds, and a 3 s TTL under a 3 s poll would recompute forever.
  - The key is the target key plus a **fingerprint**:
    - checkout targets: the overview's paths with each file's size and mtime (`os.Lstat` under the root);
    - ref targets: the `refKey` (immutable).
  - Fingerprint hits are served with no TTL. Up to 8 entries, LRU.
  - Concurrent misses for one key wait for a single computation (a small `inflight` map, since `ttlCache` doesn't do this).
- `handleFetchBase` clears `callCache` too.
- Routes: `GET /api/sessions/{windowID}/calls` and `GET {branch,pull prefix}/calls` in `server.go`.

### 1e. Tests

- **`gitfiles/blobs_test.go`** (real git): several blobs in one call, a missing id, a bad id refused with no git call (mock commander), a size skip.
- **`review/calls_test.go`** (pure, `callSet`s built in Go):
  - a move between files is unchanged;
  - a comment- or format-only edit is unchanged;
  - signature vs body changes are told apart;
  - rename detection (same body, same unit);
  - an added caller brings `+` edges, a removed one `-`;
  - a call moved between two changed functions is one `-` and one `+`;
  - context callers are capped with a count;
  - `removed-called` fires for an unchanged caller and not when the change also fixes it;
  - `signature-callers` only lists untouched files;
  - `untested` sees a test 2 hops away and not 3.
- **`review/gocalls_test.go`** (real-git fixture module on a branch, like `moduleRepo`):
  - static calls across packages;
  - a method value passed as an argument → `ref`;
  - interface dispatch → `dynamic` + `impl`;
  - a generic function;
  - a closure's calls belong to the enclosing function;
  - calls to `strings`/an external module don't fail the check;
  - a `//go:build ignore` file that would cause an import cycle is skipped;
  - a forced cycle doesn't recurse;
  - base vs head versions differ as expected;
  - a reverse importer outside the changed set shows as a caller;
  - `_test.go` callers mark `untested` off;
  - analysis at a head commit never reads the working tree (mock-free: delete the checkout file and expect the same result);
  - the package cap gives `Truncated`.
- **`handlers_calls_test.go`:**
  - the checkout comes from the state row (or the reviewer target);
  - one `Overview` and one `Calls` call across two polls with the same fingerprint, and a recompute after an mtime change;
  - 304 on a matching ETag;
  - unknown `base` → 400;
  - not a repo → `{repo:false}`;
  - ref targets keyed by `refKey`.
- **Benchmark:** `BenchmarkGoCalls` over unky-mo's own tree (skipped in `-short`).

### Done when

- `curl -k https://localhost:7890/api/sessions/<id>/calls` on a branch that changes `internal/review` lists the changed functions.
- `edgeDelta` shows its six analyzers as `dynamic` callees with `impl` edges, and `Analyze` as its caller.
- The x/tools and unky-mo timings stay within 2× of the benchmark above.

---

## Phase 2 — detailed plan (UI)

### `static/calls.js` (new, loaded after `graph.js`, before `overview.js`, on both pages)

Pure helpers (no DOM):
- `callIndex(cg)` → maps by ID for funcs, callers and callees.
- **`callFocus(cg, focusID)` → `{callers, center, callees, edges}`:**
  - `focusID == null`: `center` = every changed, added, removed, renamed or signature function, grouped by `Unit`; `callers` and `callees` = their context.
  - With a focus: `center = [focus]`, and its callers and callees on either side.
- **`layoutCallBands(view)`** → node positions:
  - three columns;
  - the center band ordered by the call chains between changed functions, reusing `layoutArchGraph`'s barycenter ordering;
  - the side columns ordered by the barycenter of their center neighbours, to keep edges short.
- **Collapse:** past 60 center functions, `center` collapses to unit boxes with counts. A click expands one unit.

DOM: `createCallsView({onOpen, onMention, onFocus})` returns `{el, update(cg), setFocus(id)}`.

**SVG:**
- Its own marker ids: `ov-call-arrow` and `ov-call-arrow-open` (`dynamic`). The architecture graph's `#ov-arrow` is a fixed global.
- Unit groups as rounded boxes behind the center band.
- Nodes are `rect` + label (`Name`, middle-truncated). The `title` tooltip shows the full ID, `path:line`, status and the unresolved count.
- Status badges `+ − ~ sig ↦` (renamed).
- **Edges:**

  | Edge | Style |
  |---|---|
  | `+` | green |
  | `-` | grey dashed |
  | context | `--ink-5` |
  | `approx` | dotted (`.is-approx` already exists) |
  | `ref` | thin and dashed |
  | `dynamic` | open arrowhead |
  | `impl` | not drawn; an "N impls" chip on the interface node expands them |

  Center↔center edges arc to the right of the band.

**Detail panel** (under the graph, like the Graph tab's commit detail):
- name, status and `path:line`;
- **Open**: diff/`bdiff` tab for a changed file, a file tab otherwise, at the line;
- **Focus** (re-center);
- **Mention in prompt** (`` `Name` (path:line) ``);
- callers and callees as clickable lists with sites, and the function's findings.

A plain click on a node selects it and fills the panel; **Focus** re-centers. That avoids a double-click, which doesn't work on touch. A breadcrumb above the graph (`All changes › edgeDelta › Analyze`) steps back.

**Findings list** below the panel: `removed-called` (red), `signature-callers` (yellow), `untested` (grey). Each opens its site at the line.

### `static/overview.js`

- **View switch:**
  - "Packages | Functions" in the architecture section's header, remembered in `localStorage` (`mo.overview.archView`);
  - the section title becomes "Architecture" / "Calls";
  - the "all dependencies" toggle shows only for Packages.
- **State:** `calls`, `callsEtag`, `callsError`, `callFocus`, reset in `setTarget` and on mode switch (`gen`).
- **Fetching:**
  - `load()` adds `get("calls", callsEtag)` to its `Promise.allSettled` **only while the Functions view is selected and the tab is visible**;
  - background polls don't fetch calls;
  - a failed fetch shows the error inside the section, not the tab.
  - While the first answer is pending: "Reading functions…" (it can take seconds on a big repo).
- **Badge:** `renderBadge` adds `removed-called` findings from the last `calls` answer to the violation count, and the title names both.
- **Strip:** `renderStrip` adds "N removed functions still called" when known.
- **Lines:**
  - `openDiff(f, line)` passes a line through `onOpenDiff(path, kind, line)`;
  - the existing violation-file and contract-item links start passing their lines too (today they drop them).
- `onMention` is a new optional option, absent in the reviewer view, so the button is hidden there, like `onDraftPrompt`.

### `static/editor.js`

- `open(path, kind, hash, {line})`: sets `tab.pendingLine`, and calls `viewReady(tab)` if the view already exists.
- Generalize `reveal(path, line)` → `reveal(path, line, kind)` and export it.
- **Unified diffs (phones):** `collapseUnchanged` may fold the target line. After scrolling, if the line is inside a collapsed range, dispatch the merge package's uncollapse effect for it. If the bundle doesn't export one, add it to `tools/codemirror/entry.js` and `make codemirror`; check first whether the bundle already exposes it.

### Hosts

- `chat.js`: `onOpenDiff: (path, kind, line) => editor.open(path, kind, undefined, {line})`.
- `chat.js`: factor the Graph tab's mention code into `insertMention(text)` and pass it as `onMention` to both `createFilesPane` and `createOverview`.
- `branch.js`: the same `onOpenDiff`, and no `onMention`.
- `branch.html` / `chat.html`: add `<script src="/static/calls.js">`.

### CSS (`style.css`, tokens only)

- `.calls-*`: `__band`, `__unit`, `__node.is-added|is-removed|is-changed|is-signature|is-renamed|is-context|is-test`, `__edge.is-new|is-removed|is-context|is-ref|is-dynamic`, `__chip`, `__detail`, `__crumbs`, `__findings`.
- Node colors follow `.overview-arch__node`, so the two views look like one tool.
- Both themes are checked in the browser.

### Tests and checks

- There are no JS tests in the repo, so keep the logic in the pure helpers (`callFocus`, `layoutCallBands`) and check them by hand through the browser console.
- **Manual (chrome-devtools MCP), on this branch's own change:**
  - Functions view on the chat view and the reviewer view of a PR;
  - focus and breadcrumb;
  - Open lands on the line (split and unified diff, the latter at phone width via `emulate`);
  - Mention inserts at the cursor;
  - badge and strip with a deliberately removed-but-called function;
  - dark mode;
  - a 100+ function change collapses to units.

---

## Phase 3 — detailed plan (Python + JS/TS)

Both are heuristic (`exact() == false`, every edge `approx`), built as `twoPhase`:
- `scan(path, src)`: definitions, call sites and import bindings, cached per blob id under the `calls:py` / `calls:js` keys;
- `resolve`: turns call sites into IDs against a per-analysis **definition index**, file → `[]def`, plus the name lookups below.

### Python (`pycalls.go`)

- **Definitions:**
  - Walk `pyCode(src)` (strings and comments blanked) by logical lines, using `depth()` and the backslash/bracket joining from `pyImports`.
  - An indent stack of `class`/`def` gives `Class.method` and nested `outer.inner`. Nested defs are their own functions, called from their parent.
  - A def ends at the line before the next non-blank logical line indented at or below its own `def`. Decorator lines belong to the function.
- **Bases:** `class A(B, mixins.C):` records base names, resolved like calls, for method lookup.
- **Imports:** extend `pyImport` with aliases (`Names` keeps the original names; a new `As []string` gives the bound name; `import a.b as c` binds `c`). Existing architecture behaviour is unchanged.
- **Call sites:**
  - `qualifier.name(` and `name(` on blanked code, skipping keywords and builtins (`print`, `len`, `isinstance`, …);
  - `@decorator` → a `ref` to the decorator function;
  - a function name passed bare (`map(fn, xs)`, `callback=fn`) → `ref` when it resolves.
- **Resolution, first unique match:**
  1. `self.m` / `cls.m` → the enclosing class, then its bases (in-tree, left to right, depth first).
  2. `super().m` → the bases only.
  3. A bare name bound by `from m import name [as x]` → `name` in the file `targets()` resolves `m` to. If that's a package `__init__.py` that re-exports it, follow one level.
  4. `alias.name` where `alias` is an imported module → that module file's top-level `name`.
  5. `Cls.m` / `Cls(` where `Cls` resolves by rule 3 or is local → that class's method, or `__init__`.
  6. A bare name defined in the same file → it.
  7. `obj.m` with an unknown receiver → only if `m` is defined as a method exactly once in the repo, and isn't a dunder.
  8. Otherwise unresolved.
- **Tests:** files under `tests/`, `test_*.py`, `*_test.py` (the existing `isTest`).

### JS/TS (`jscalls.go`)

- **Definitions**, on `jsCode` (template, regex and comments blanked; plain strings kept, skipped via `inString`):
  - `function name(`, `async function`, `function*`, `export [default] function`;
  - `const|let|var name = [async] (function | (params) => | ident =>)`;
  - `class X [extends Y]`, and methods inside the class body: `name(…) {`, with `static`/`async`/`get`/`set`/`#private`/TS modifiers, skipping `if|for|while|switch|catch`;
  - object-literal methods in `export default { … }` (Vue options API);
  - TS overload signatures and `declare` (no body) are skipped.
  - **Ranges:** brace depth from the opening `{`; an expression-bodied arrow ends at the statement's end at the same depth.
- **Import bindings:** new `jsImportBindings`, parsed like `jsImports` but keeping names:
  - `import X, { a as b } from "s"`, `import * as ns`;
  - `const { a } = require("s")`, `const x = require("s")`;
  - `export { a } from "s"` / `export * from "s"`: re-exports followed up to 2 hops through `resolveSpec`.
  - Default export of a file → `#default` (or the named function it exports).
- **Call sites:**
  - `name(`, `this.name(`, `ns.name(`, `obj.name(`, `new Name(`;
  - JSX `<Name` / `<ns.Name` (capitalized) in `.jsx`/`.tsx`/`.vue`/`.svelte`/`.astro` (via `componentScript`), as calls;
  - a function passed bare (`onClick={save}`, `arr.map(fmt)`, `addEventListener("x", h)`) → `ref`.
- **Resolution:**
  1. `this.m` → the enclosing class, then `extends` chain.
  2. An imported binding → the definition in the file `resolveSpec` returns.
  3. `ns.name` → that module's export.
  4. A same-file top-level definition.
  5. `obj.m` with an unknown receiver → unique-in-repo method name only, as in Python.
  6. Otherwise unresolved.
  
  Stimulus controllers' `data-action="ctrl#method"` in ERB could link views to controller methods; noted for phase 5, not built here.
- **IDs:** path without extension + `#name`; `index` files keep the directory path (`src/cart/index#total`).

### Tests

- Scanner unit tests (`pycalls_scan_test.go`, `jscalls_scan_test.go`):
  - definitions and ranges: nested defs, decorators, class methods, arrows, expression-bodied arrows, overloads, getters, private `#m`;
  - nothing from comments, strings, template literals, regex literals or docstrings.
- Real-git fixtures extending `pyRepo` / `nodeRepo`:
  - each resolution rule;
  - an ambiguous `obj.save()` stays unresolved;
  - aliases;
  - re-exports through `index.ts`;
  - JSX components;
  - a tsconfig `paths` import;
  - `removed-called` for a deleted function still imported elsewhere.

---

## Phase 4 — detailed plan (Ruby/Rails, Kotlin/Java, Swift)

### Ruby (`rbcalls.go`)

- **Index:** `rubyLang.consts` stores the defining **file** next to the unit: `map[string]rubyConst{unit, file}`, built at the same place in `detect`. Architecture behaviour is unchanged: lookups still read `unit`.
- **Definitions:**
  - On `rubyCode`.
  - **Nesting:** `class`/`module` (including `Foo::Bar` compact form), `def m`, `def self.m`, `class << self` (its defs are class methods). Endless `def m = expr` is a one-line function.
  - **Ends:** keyword counting over block openers at statement start (`class module def if unless while until case begin for` and trailing `do`), so modifier-`if` doesn't count. It's cross-checked with indentation (Rails code is rubocop-formatted). When the two disagree the file is marked unparsed: its functions are listed without ranges or body hashes, and shown as "changed" only if the file changed.
- **Call sites:**
  - `Const.m`, `Const::Path.m`, `Const.new` (→ `initialize`), `self.m`, `m(`;
  - bare `m` / `m arg` when `m` is a method of the enclosing class chain and not a local assigned earlier in the method (scan for `m =` / block params `|m|`);
  - `super` → the same method up the chain;
  - `send(:m)` / `public_send(:m)` / `method(:m)` → `approx`;
  - **Rails callbacks:** `before_|after_|around_action`, `skip_*`, `before_|after_validation`, `validate`, `before_|after_save|create|update|destroy|commit`, `after_initialize`, plus their `if:`/`unless:` symbols → `ref` from the class (pseudo-function `Class.<callbacks>`) to the method;
  - **ERB/HAML views:** a pseudo-function per view file (`view:app/views/users/show.html.erb`); bare calls resolve against `app/helpers` (all helpers are mixed into views) and the controller's `helper_method`s.
- **Resolution:**
  1. receiver known → that class, then `include`d modules (concerns) in order, then the superclass chain (in-tree);
  2. a bare name → the same chain from the enclosing class;
  3. `Const.m` → the Zeitwerk index's file;
  4. otherwise unresolved. No repo-wide unique-name guess for Ruby: too many same-named methods (`call`, `perform`, `show`).
- **Routes:** controller actions are already listed by `controllerActions`. Phase 5 can draw `GET /users/:id` → `UsersController#show` as a caller.

### Kotlin/Java (`jvmcalls.go`)

- **New declaration index** (there's none today besides `jvmPackage`), cached per blob id:
  - `class|interface|object|enum class|data class|sealed class|annotation class` names with brace ranges;
  - Kotlin `fun [Recv.]name(` (top-level, member, extension);
  - Java method declarations at class-body depth: an identifier followed by `(`, preceded by a type or modifier, followed by `{` or `throws` (constructors use the class name).
- **Call sites:** `name(`, `recv.name(`, `Type.name(`, `Type(` / `new Type(` (constructor), `this.`/`super.`, Kotlin trailing lambdas `name {`, method references `::name` / `Type::name` → `ref`.
- **Resolution:**
  1. the enclosing class and its supertypes (from the declaration line, in-tree);
  2. extension functions for a known receiver type;
  3. imported names (`import pkg.Type`, `import pkg.fn`, aliases kept);
  4. the same package (Kotlin and Java see their package without imports);
  5. otherwise unresolved.
- `@Composable` functions get their calls like any other, which gives Compose UI trees for free.

### Swift (`swiftcalls.go`)

- **Declarations:**
  - extend `swiftDecls` to return lines and ranges, and to see `extension Type` (its members belong to `Type`), `func`, `init`, `subscript`, `static/class func`;
  - protocols with their requirements, for `dynamic` + `impl` (types declaring conformance in-tree, including via extensions).
- **Call sites:** `name(`, `self.name(`, `Type.name(`, `Type(` (→ `init`), `super.`; trailing closures `name {`; the implicit member `.name(` is unresolved.
- **Resolution:**
  1. the enclosing type plus its extensions anywhere in the module;
  2. the same module (a SwiftPM target or the existing unit, since files in a module see each other without imports);
  3. imported in-repo modules;
  4. otherwise unresolved.

### Tests

- Scanner tests per language: ranges, nesting, Ruby modifier-`if`, `class << self`, endless defs, Kotlin extension funs, Java constructors, Swift extensions; nothing from strings, heredocs or comments.
- Fixtures extending `railsRepo` / `ktRepo` / `swiftRepo`:
  - Rails: concern lookup, callback `ref`s, a helper called from a view, `removed-called` for a deleted model method still called from a controller, an ambiguous `call` left unresolved;
  - Kotlin: an extension fun and a same-package call;
  - Swift: an extension method and protocol dispatch.

---

## Phase 5 — detailed plan (CLI, intent link, tuning)

### `mo calls`

- `cmd/mo/calls.go`: `mo calls [--base branch|head] [--json|--dot] [--fail-on removed-called]` in the current checkout. It runs `gitfiles.GetOverview` then `review.Calls`.
- **Default output:** text. Changed functions by unit with their status, `+`/`-` calls, a callers count, then findings.
- **`--dot`:** Graphviz with clusters per unit and the same colors as the web view, for anyone who wants callGraph-style images.
- **`--fail-on`:** exits 1, for a pre-push hook or a Claude Code `Stop` hook.
- **CLAUDE.md:** a line so Claude sessions know they can run it ("what calls what I just changed?").

### Intent trace link

- `createIntentTrace` keeps each `Edit`/`Write` result's `toolUseResult.structuredPatch` hunks (new-side line ranges) with the turn.
- For each function in the calls view, the **latest** turn whose hunk overlaps `[Line, End]` in the current version tags it.
- Later edits shift lines, so hunks of later edits to the same file move earlier ranges by their line delta. That's approximate, and labelled so.
- The detail panel shows "Changed in: <prompt>" (calls `view.reveal(uuid)`). Functions changed outside the conversation say so.

### Routes as callers

- Surface routes that name a handler become pseudo-callers of it:
  - Go mux `HandleFunc(pattern, s.handleX)`: already a `ref` from the registering function, now labelled with the pattern;
  - Rails routes → `Controller#action`;
  - Express `router.get(path, handler)`.

### Tuning on real repos

- Run on recent PRs of unky-mo, moma-org-rails, moma-apps-rails, moma-chatbot, moma-app-android and moma-app-ios.
- Per language, hand-check 30 sampled edges and 20 unresolved calls.
- Record precision, the unresolved rate and timings in "Phase 5 notes" below.
- Thresholds that trigger phase 6 for a language:
  - precision under 90%;
  - range errors on more than 5% of functions.
- Tune the `untested` hop count and the context caps from what reviewers find useful.

---

## Phase 6 — upgrades (conditional)

- **tree-sitter on `wazero`** for a language that fails the phase 5 thresholds: replaces only its definition and range extraction (`scan`). Resolution stays the same. The binary grows by roughly 5–10 MB per grammar set, so it's measured before adopting.
- **`x/tools/go/callgraph/vta`** for Go checkouts when `go` is on `PATH`: more precise `dynamic` targets than CHA. Working tree only; base and refs keep the stdlib loader.
- **Per-package Go fact caching** (calls + hashes keyed by the blob ids of the package and its in-module dependency closure), if large Go repos make the per-analysis re-check too slow.

## Decisions taken

- **Our own engine**, not callGraph (reasons above).
- **Go first and exact**, with everything outside the module faked: no toolchain, about 0.1% of in-module calls lost.
- **A Packages / Functions switch** on the existing architecture graph, not a new section.
- **"Untested" is a grey hint**, never red.
- **`/calls` is fetched only while the Functions view is visible.** Its cache is keyed by a fingerprint of the changed files, not a short TTL.
- **Heuristic languages never guess between candidates:** an ambiguous call is counted as unresolved.
