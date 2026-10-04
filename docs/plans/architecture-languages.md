# Architecture checks beyond Go

## Goal

The Overview's architecture section (new dependencies between parts of the code, checked against `.unky-mo/architecture.toml`) and its contract surface work for Go today. Extend both to **Ruby on Rails, Node (JS/TS), Python, Swift (iOS) and Kotlin/Java (Android)**.

Constraint: projects need nothing extra (no Packwerk, no dependency-cruiser, no language toolchain installed). Any tooling lives inside unky-mo.

## Shared design (applies to every language)

### One engine, one plug-in per language

`internal/review` today has Go-specific code (`goarch.go`). It becomes a language-agnostic **edge engine** plus a small analyzer per language:

```go
type language interface {
	Name() string
	Owns(path string) bool               // which files this analyzer reads
	Unit(path string, idx *index) string // the "part of the code" a file belongs to ("" = not analyzed)
	Refs(path, src string, idx *index) []ref // outgoing references: {to unit, line}
}
```

The engine keeps today's rules, unchanged for every language:
- Compare each changed file's references before and after.
- A candidate edge only counts if no *unchanged* file of the same unit already has it, so a reference that just moved between files isn't new.
- Only new and removed edges are reported, so existing violations are never flagged.
- Test files are skipped; generated files count.

Today's `goarch.go` becomes the first analyzer.

### Units are paths

A unit is always named by a repo path (`app/models`, `src/features/cart`, `moma/pipelines`, `app/data`, `Sources/Checkout`), so one graph can hold several languages (a Rails app with Stimulus JS shows `app/models` and `app/javascript/controllers` side by side). The rules file's `allow`/`deny` path prefixes work the same for all of them.

### The index

Some languages can't resolve a reference from the file alone (Ruby constants, Kotlin imports, Swift types), so each analysis builds an **index** from the target's file list (`git ls-files` / `ls-tree`):
- **Paths** for Rails (Zeitwerk naming), Python and Node resolution.
- **Declared symbols** for Kotlin/Java (`package` lines) and Swift (top-level type names), read from file heads.
- Symbols are cached in memory by **blob id** (`git ls-files -s` / `ls-tree` give them), so polls don't re-read unchanged files. The cache is bounded (LRU).

### Parsing: hand-written scanners first, tree-sitter if needed

Imports and declarations are syntactically simple in all five languages. A small tokenizer per language (skipping comments, strings and string interpolation, and for JS, regex literals and template strings) plus pattern matching is enough and stays pure Go.

- **Rejected:** `cgo` tree-sitter. It complicates the build and cross-compiling.
- **Fallback if a scanner proves too inaccurate** (most likely for Swift/Kotlin type references): tree-sitter grammars compiled to WebAssembly and run with `wazero`, a pure-Go runtime. It's exact parsing for all five languages, at the cost of roughly 5–10 MB more binary. It's an option to adopt per language later, not a starting point.

### Accuracy is shown, not hidden

Each analyzer declares how it resolves references:
- **Exact:** Go, and SwiftPM/Gradle module imports.
- **Resolved:** Node and Python imports that resolve to files.
- **Heuristic:** Ruby constants, and Swift/Kotlin same-module type references.

The architecture section labels heuristic edges ("approximate: inferred from names") and lists unresolved references in a tooltip rather than guessing.

### Presets

Rules files can opt into built-in layer rules per stack (`preset = "rails"`, `"android-clean"`, …), next to or instead of hand-written layers. A preset expands to ordinary `[[layer]]` entries, so `deny`/`allow` semantics stay the same.

### Drafting a rules file

`mo architecture init [--preset X]` (CLI) and a "Draft rules" button in the Overview write a starting `.unky-mo/architecture.toml`:
- the matching preset;
- a comment listing the repo's current cross-unit dependencies, so a team can turn what it sees into rules;
- nothing that would flag existing code: only new edges are ever checked.

The file isn't committed for you.

### Contract surface

Each analyzer adds its stack's "edges of the system" to the existing surface categories (routes, flags, config, env, dependencies, migrations), plus two new categories where a stack has them: **Permissions** and **Public API**.

### Base freshness (folded in)

Checkout targets and the chat view compare against whatever `origin/<default>` the clone last fetched. The Overview will show the merge base's age when `origin/<default>` is older than 7 days, with a "Fetch" button (the same refspec fetch the reviewer view does).

## Ruby on Rails

Grounded on moma-org-rails, moma-apps-rails and moma-chatbot (standard layout, no Packwerk).

**Units.** Rails' own folders: `app/models`, `app/controllers`, `app/services`, `app/jobs`, `app/views`, `app/helpers`, `app/components`, `app/presenters`, `app/serializers`, `app/policies`, `app/forms`, `app/mailers`, `app/channels`, `app/validators`, `app/observers`, plus `lib` (and `lib/<dir>`). That's what `AreaOf` already returns. Namespaced subfolders (`app/models/audio/`) stay in their layer.

**Index (paths only, no file reads).** Zeitwerk naming maps every autoloaded file to a constant:
- `app/models/museum_location.rb` → `MuseumLocation`
- `app/services/audio/tour_builder.rb` → `Audio::TourBuilder`
- `app/*/concerns/x.rb` → `X`

References are matched by **underscoring** them (`HTTPClient` → `http_client`), which sidesteps the app's custom acronym inflections.

**References** (in a Ruby scanner that skips comments, strings, heredocs and `%w[]` literals):
- **Constants** (`Foo`, `Foo::Bar`, `::Foo`), resolved like Ruby's lexical lookup. Try the file's own namespace first (from its path), then shorter prefixes, then the top level.
- **Association macros:** `belongs_to/has_many/has_one/has_and_belongs_to_many :name` → the classified model, and `class_name: "X"`. Models reference each other this way more than by constant.
- **ERB/HAML/Slim views:** constants inside code tags.
- **Unresolved constants** (gems, Ruby core) are external and ignored.

**Preset `rails`:**
- `app/models` may not reference controllers, views, helpers, components, presenters or serializers.
- `app/services` and `app/jobs` may not reference controllers, views, helpers or components.
- `lib` may not reference `app/*`.

**Surface:**
- Routes (done).
- Migrations and `db/schema.rb` (done).
- `ENV` (done).
- Gemfile (done), plus `Gemfile.lock` version changes for direct gems.
- `config/credentials` and `config/*.yml` keys added or removed (key names only, never values).
- New `config/initializers`.
- New or removed controller actions, i.e. public methods in `app/controllers` (they're the HTTP surface behind the routes).

**Tests:**
- a fixture app in `testdata/rails/`: namespaced models, a concern, an acronym (`HTTPClient`), an association, a constant inside a view;
- a model gaining a controller reference (violation);
- a service moving a reference between files (not new);
- manual runs on moma-org-rails #341 and moma-apps-rails.

## Node (JS/TS)

Grounded on moma-apps-rails `app/javascript` (Stimulus, relative imports) and vanmechelen-me (Astro + TS).

**Units.** The directory of a file, cut to the module level:
- two segments below a container (`src/`, `app/`, `lib/`, `packages/<pkg>/src`, `app/javascript/`), so `src/features/cart/…` → `src/features/cart`;
- one segment elsewhere.

The depth is configurable in the rules file (`[node] unit_depth`).

**References** (a JS/TS scanner that handles strings, template literals, comments and regex literals):
- `import … from 'x'`, `import 'x'`, `export … from 'x'`, `import('x')` with a literal, `require('x')`;
- `.astro`, `.vue` and `.svelte` script blocks.

**Resolution, all from files in the repo:**
- relative paths with Node/TS extension and `index.*` lookup;
- `tsconfig.json`/`jsconfig.json` `baseUrl` + `paths` (JSONC: comments and trailing commas allowed, `extends` followed inside the repo);
- `package.json` `imports` (`#alias`);
- **workspaces:** `package.json` `workspaces` and `pnpm-workspace.yaml` map package names to their folders, so a monorepo's packages are units;
- **Rails importmap:** `config/importmap.rb` `pin_all_from`/`pin` map bare names to `app/javascript` folders.
- Bare package names that resolve to none of these are external.
- Bundler aliases in vite/webpack configs aren't evaluated; the rules file can list them (`[node] aliases = { "@/" = "src/" }`).

**Preset `frontend`:**
- shared code (`lib`, `utils`, `components`) may not import pages, routes or features;
- features may not import each other except through their index (a "public entry" check: importing `features/cart/internal/x` from outside `features/cart` is flagged).

**Surface:**
- Express routes (done).
- File-based routes: added or removed files under Next.js `pages/` or `app/**/page|route.*`, Astro `src/pages/`, SvelteKit `src/routes/`.
- `package.json` dependencies (done) plus `engines`, `bin` and `scripts`.
- `process.env` (done) and `import.meta.env.X`.
- Public API of workspace packages: exported names of their entry file (`exports`/`main`).

**Tests:** scanner fixtures (comments containing `import`, template strings, regex literals, dynamic imports); resolution fixtures (tsconfig paths with `extends`, workspaces, importmap); a monorepo package importing another's internals.

## Python

Grounded on moma-python-utilities (a `moma` package, absolute imports, typer CLIs).

**Units.** Packages: a directory of `.py` files (with or without `__init__.py`). A unit is named by its path (`moma/pipelines`), shown with its dotted name in the tooltip.

**Source roots** (where top-level packages live) are found from the repo:
- the root, `src/`;
- `[tool.setuptools] package-dir`/`packages` in `pyproject.toml`, and `[tool.poetry] packages`;
- any dir that's a parent of a package imported by its own files.

**References** (a scanner for statements, handling parentheses and backslash continuations and skipping strings, docstrings and comments):
- `import a.b`, `import a.b as c`, `from a.b import c, d`, `from . import x`, `from ..pkg import y`;
- `importlib.import_module("a.b")` with a literal.

Resolution tries `a/b.py`, `a/b/__init__.py`, the namespace dir `a/b/`, and for `from a import c` also `a/c.py`, under each source root. Relative imports resolve against the file's package. Anything else (stdlib, installed packages) is external.

Imports under `if TYPE_CHECKING:` are marked type-only. Rules can ignore them (`[python] type_only = "ignore"`, the default), since they don't create a runtime dependency.

**Presets:**
- `django`: models may not import views, forms or serializers; apps' `migrations` are skipped.
- `layered`: `domain` may not import `adapters`, `api` or `cli`.

**Surface:**
- Flask `@app.route`, FastAPI `@app/router.get|post|…`, Django `path()/re_path()` in `urls.py`.
- CLI options: argparse `add_argument("--x")`, click `@click.option("--x")`, typer `typer.Option(…, "--x")` (moma uses typer).
- `os.environ`/`os.getenv` (done).
- Dependencies: `pyproject.toml` `[project] dependencies` and optional groups, `[tool.poetry.dependencies]`, `requirements*.txt`, `Pipfile`.
- Migrations: Django `migrations/` (done), Alembic `versions/`.

**Tests:** scanner fixtures (multi-line `from … import (…)`, imports inside docstrings, `TYPE_CHECKING`); source-root detection (`src/` layout, `package-dir`); relative imports two levels up; manual run on moma-python-utilities.

## Kotlin/Java (Android)

Grounded on sporza-vrt-watch-app (one Gradle module, packages `data`, `data.local`, `data.remote`, `model`, `ui.*`, `tile`).

**Units.** A Gradle module plus a package within it:
- **Modules:** `include(":app", ":core:data")` in `settings.gradle(.kts)`, with `project(":x").projectDir` overrides.
- **Packages:** relative to the module's root package (the longest common package prefix in `src/main`), cut to one level by default (`data`, `ui`, `model`; `[kotlin] unit_depth`).
- **Naming:** a unit is named by the path of that package's folder (`app/src/main/kotlin/…/data` shows as `app:data`).
- **Single-module apps** (like sporza) get package units only; **multi-module apps** get `module:package`. Cross-module edges are exact: an import resolves to a package that lives in another module.

**Index:** every `.kt`/`.java` file's `package` line (from the file head, cached by blob id) maps packages to units.

**References** (a scanner skipping comments, strings and `${}` templates):
- `import a.b.C` and `import a.b.*`, resolved through the package index; anything else is external;
- same-package references need no import and are never cross-unit.
- Fully-qualified names in code (`a.b.C.method()`) matching an indexed package are caught too.

**Preset `android-clean`:**
- `model`/`domain` may not import `data` or `ui`;
- `ui` may not import `data.local`/`data.remote` (go through repositories);
- `data` may not import `ui`.

**Surface:**
- **Permissions:** `AndroidManifest.xml` `uses-permission` and exported components (`android:exported="true"`, intent filters).
- `minSdk`/`targetSdk`/`compileSdk` changes.
- **Dependencies:** `implementation(…)`/`api(…)` coordinates in `build.gradle(.kts)` and `gradle/libs.versions.toml` (version catalog).
- **Routes:** Retrofit `@GET/@POST/…("path")`.
- **Migrations:** Room `@Database(version = …)` and exported schema JSON.
- `BuildConfig` fields.

**Tests:** a fixture with two modules, a wildcard import, a fully-qualified reference, and a Java file; manifest/catalog fixtures; manual run on sporza-vrt-watch-app.

## Swift (iOS)

There's no local iOS repo, so this plan is based on synthetic fixtures. A real repo to test against would help (open question).

**Units, three tiers, best first:**
1. **SwiftPM targets** (`Package.swift`, including local packages inside an app repo):
   - `.target(name:path:)` and `.executableTarget(name:path:)` with the default `Sources/<name>`; test targets are skipped.
   - `import X` between targets is explicit and exact.
2. **Xcode targets** from `project.pbxproj` (an ASCII plist, parsed in Go):
   - `PBXNativeTarget` → its sources build phase → file paths through the group tree;
   - Xcode 16's `PBXFileSystemSynchronizedRootGroup` (folder → target) is supported too.
   - App ↔ framework/extension imports are exact.
3. **Folders inside one target** (most single-module apps: `Features/Cart`, `Services`, `Models`), cut to one level below the target's root (`[swift] unit_depth`). References between them are type names, so this tier is heuristic.

**Index:** each `.swift` file's top-level declarations (`class/struct/enum/protocol/actor/typealias Name`, plus `extension Name` as a reference, not a definition), read from file contents and cached by blob id. A name declared in two units is ambiguous and isn't resolved.

**References** (a scanner skipping comments, strings, multi-line strings and `\()` interpolation):
- `import X` (`@testable import` skipped, it's for tests);
- in tier 3, capitalized identifiers matched against the index.

**Preset `ios-layered`:**
- `Models` may not reference `Views`/`ViewControllers`/`Features`;
- `Services` may not reference `Views`.

**Surface:**
- **Permissions:** `Info.plist` privacy usage keys (`NS…UsageDescription`) added or removed, URL schemes, background modes, and entitlements (`*.entitlements`) changes.
- **Dependencies:** `Package.swift` `.package(url:…)` and `Package.resolved` versions, CocoaPods `Podfile`.
- **Public API:** `public`/`open` declarations of SwiftPM library targets (like Go's exports).
- **Deployment target:** `IPHONEOS_DEPLOYMENT_TARGET`, `platforms:` changes.

**Tests:** fixtures for SwiftPM (two targets plus a test target), a minimal `project.pbxproj` (classic groups and a synchronized root group), and a single-target folder layout with an ambiguous type name.

## Sequencing

| # | What | Why first |
|---|------|-----------|
| 0 | Engine refactor (`language` interface, index, blob-id cache), Go as the first analyzer, presets, `mo architecture init`, base-freshness note | Everything else plugs into it; Go keeps today's behaviour (its tests must pass unchanged) |
| 1 | Rails | Most of the daily work; three local repos to validate on |
| 2 | Node | moma-apps-rails is Rails + Stimulus, so it completes the Rails picture; Astro next |
| 3 | Python | moma-python-utilities, small and clean |
| 4 | Kotlin/Java | sporza-vrt-watch-app; module/package resolution is exact |
| 5 | Swift | Needs a real repo to validate the pbxproj and heuristic tiers |

Each step:
- gets its own detailed plan section here before it's built, as before;
- ships with fixtures and a manual run on a real repo;
- adds its contract-surface items in the same step.

## Decisions (2026-10-04)

**iOS and Android are validated on MoMA's own apps:** `moma-app-ios` and `moma-app-android`, cloned for testing (blobless) outside the workspace.

**Presets apply without a rules file.** unky-mo detects the stack (Rails, Django, Node, Android, iOS) and applies that stack's preset automatically. Because of that, presets only hold rules that are wrong in any codebase of that kind. A rules file can:
- choose presets (`presets = ["rails"]`);
- turn them off (`presets = []`);
- add layers next to them (a repo's own layers win over a preset's for the same path, by longest match).

**Rule paths may use `*`** for one path segment, so a preset can say `*/model` for any Gradle module or Swift target.

**Kotlin units go one package level deep.**

**Rails will get more work later.** Keep its analyzer easy to extend: references, units and surface items are separate functions.

## Step 0 — engine (detailed plan)

- **`review/lang.go`:**
  - the `language` interface: `name`, `detect(idx)`, `owns(path)`, `unit(path)`, `refs(path, src)`, `exact`;
  - the registry;
  - `index`: the target's file list from `ls-files -s` (or `ls-tree` for a ref), with blob ids;
  - a process-wide symbol cache, keyed by language + blob id, bounded at 20k entries.
- **`review/engine.go`:** `goArchitecture` generalized to `edgeDelta(lang)`. Changed files' before/after refs, unchanged files of the touched units read through the index, the same counting rules. It runs every detected language and merges units and edges (`Edge.Lang`, `Edge.Approx`, `Package.Lang`).
- **`review/golang.go`:** the Go analyzer. Today's `goImports`/`pkgDir` behind the interface. Exported-API diffing stays in the Go surface code. The existing Go tests must pass unchanged.
- **`gitfiles.Classify` test detection** also knows Kotlin/Java/Swift test layouts: `src/test/`, `src/androidTest/`, `Tests/`, `*Tests.swift`, `*Test.kt`, `*Test.java`, `conftest.py`.
- **Rules:**
  - `*` segment globs in `paths`/`allow`/`deny`;
  - `presets = [...]`;
  - built-in presets (`rails`, `django`, `node`, `android`, `ios`) chosen by stack detection when the file doesn't say;
  - `Rules.Presets` reports which applied and whether they were automatic.
  - Each preset is filled in by its language step; step 0 ships the mechanism with an empty table.
- **`Analysis.Languages`:** `[{name, exact, units}]`. The Overview uses it instead of `module` to decide whether to draw the graph, labels approximate edges (dashed, "approximate" in the tooltip), and says which presets applied.
- **Base freshness:**
  - `Overview.BaseFetched` is when `origin/<default>` was last updated (its reflog, else the `FETCH_HEAD` mtime);
  - `POST …/fetch-base` (session and branch prefixes) fetches the default branch;
  - the Overview shows "origin/main last fetched N days ago · Fetch" past 7 days.
- **Tests:**
  - the existing review/web suites unchanged;
  - globs and presets (auto vs file, `presets = []` turns them off, longest match across preset and repo layers);
  - the index and cache (a second analysis reads no unchanged file);
  - `BaseFetched` from the reflog on a real clone;
  - the fetch-base handler.

## Step 1 — Rails (detailed plan)

- **`review/ruby.go`** is the analyzer (`exact: false`). It detects a Rails app (`Gemfile` + `app/`), owns `.rb`, `.rake`, and `.erb`/`.haml`/`.slim`/`.jbuilder` views, and puts each file in a unit with `AreaOf`: `app/models`, `lib/tasks`, `config`, `db`.
- **Constant index.** Every `.rb` file under an autoload root, keyed by its path with the root and `.rb` stripped:
  - roots are each `app/*` directory, each `app/*/concerns`, and `lib` minus `lib/tasks`, `lib/assets` and `lib/generators`;
  - keys are squashed (lowercase, underscores dropped), so `HTTPClient`, `HttpClient` and `OAuth2Token` find `http_client.rb` and `oauth2_token.rb` whatever the app's acronym inflections;
  - a key in two units is ambiguous and never resolved.
- **`review/ruby_scan.go`:** a Ruby code scanner that blanks comments, `=begin/=end` blocks, string and symbol literals (`'…'`, `"…"` with escapes, `%w[]`/`%i[]`/`%q()`/`%Q{}`) and heredocs (`<<~ID`, `<<-ID`, `<<ID`), keeping line numbers. ERB keeps only the code inside `<% %>`; HAML/Slim keep only their code lines (`-`/`=`).
- **References:**
  - **Constants** (`Foo`, `Foo::Bar`, `::Foo`). Resolved like Ruby's lexical lookup, with the nesting taken from the file's own path (`app/services/audio/tour_builder.rb` is `Audio::TourBuilder`). Try `audio/tour_builder/<ref>`, `audio/<ref>`, `<ref>`; for each, also the ref's leading parts, so `Artwork::STATUSES` finds `artwork.rb`. `::Foo` only looks at the top.
  - **Associations:** `belongs_to`/`has_one :name` and `has_many`/`has_and_belongs_to_many :names` (singularized), or their `class_name:`. Polymorphic ones are skipped.
- **Preset `rails`** (automatic for Rails apps):
  - `app/models` must not depend on `app/controllers`, `app/views`, `app/helpers` or `app/components`;
  - `app/services` and `app/jobs` must not depend on `app/controllers`, `app/views`, `app/helpers` or `app/components`.
  - `lib` → `app` is left out: common in real apps, so not wrong in *every* codebase.
- **Surface (`review/rails_surface.go`):**
  - **Controller actions:** the public methods of `app/controllers/**` classes, up to the first `private`/`protected`, named `Audio::ToursController#show`. They go in the routes category.
  - **Initializers:** files added to or removed from `config/initializers/`.
  - **`Gemfile.lock` versions** of the gems the `Gemfile` names directly.
  - Config YAML keys are left for later Rails work.
- **Tests:** a fixture app built in `t.TempDir()`:
  - namespaced service, concern, `HTTPClient` vs `http_client.rb`, association with and without `class_name`;
  - a constant in an ERB view; comments, strings and heredocs that mention a constant;
  - a model gaining a helper reference (preset violation);
  - a reference moved between two services (not new);
  - controller actions added and removed; a Gemfile.lock bump;
  - the scanner on its own.
  - Manual runs on moma-org-rails #341 and a moma-apps-rails branch.

### Step 1 notes

- **Validated on moma-org-rails #341** (no new cross-layer dependencies; existing ones include `app/models → app/presenters` and `→ app/services`) and moma-apps-rails #4710 (a removed `app/services → app/policies`, a new controller action, and route changes). An analysis takes about 2 s.
- **The graph now shows the change by default:** the ends of added and removed dependencies, and existing ones between them. Rails layers reference each other in cycles, and drawing every touched unit with all its dependencies was a tangle. The checkbox, renamed "all dependencies", brings back the full picture.
- **Left for later Rails work:** config YAML keys, `lib` → `app` rules per repo, autoload paths beyond the defaults (read `config.autoload_paths`), constants defined inside another class's file (`Artwork::Status` in `artwork.rb` resolves to `artwork.rb`, which is right, but a separately-filed `Artwork::Status` that doesn't exist yet stays unresolved).

## Step 2 — Node (detailed plan)

- **Units:** one segment below the last container directory in a file's path (`src`, `app`, `apps`, `javascript`): `src/components`, `app/javascript/controllers`, `packages/ui/src/hooks`. Elsewhere, the top directory. Files at the root aren't in a unit. That's one level, like Kotlin; a `[node] unit_depth` can come later if features need splitting.
- **Scanner (`review/js_scan.go`):** blanks comments, template literals and regex literals (a regex is a `/` after an operator, an opening bracket, or `return`/`typeof`/…), keeps plain strings, and records where they are, so an `import` written inside a string isn't read. `.vue`/`.svelte` keep their `<script>` blocks, `.astro` its frontmatter and scripts.
- **References:** static `import … from`, `import '…'`, `export … from`, `import('…')`, `require('…')`.
- **Resolution** (all from repo files; exact):
  - relative paths with extension and `index.*` lookup, including TS's `.js`-for-`.ts`;
  - `tsconfig.json`/`jsconfig.json` `baseUrl` + `paths` (JSONC; `extends` followed within the repo);
  - `package.json` `imports`;
  - workspaces (`package.json` `workspaces` globs, `pnpm-workspace.yaml`), with package name → directory and its `exports`/`main` ignored (the package dir is enough to find the unit);
  - Rails importmap (`pin`, `pin_all_from … under:`).
  - Anything else is external.
- **Presets are scoped to their language.** The `node` preset's `lib`/`components` mustn't judge a Rails app's Ruby `lib`. Preset layers carry the language they apply to; the repo's own layers apply to all.
- **Preset `node`** (automatic when a root `package.json` exists): shared code (`components`, `lib`, `utils`, `hooks`, under `src/` or at the root) must not depend on pages, routes or views (`pages`, `routes`, `views`, and Next's `app`).
- **Surface:**
  - file-based routes (added or removed `pages/**`, `app/**/page.*`/`route.*`, `src/pages/**`, `src/routes/**`);
  - `import.meta.env.X`;
  - `package.json` `engines`, `bin`, `scripts` changes.
- **Tests:**
  - scanner fixtures (an import in a string, a comment and a template; a regex containing quotes; a multi-line import; dynamic import; Vue/Astro);
  - resolution fixtures (tsconfig `paths` with `extends`, `baseUrl`, `.js`→`.ts`, workspaces, importmap);
  - preset scoping (a Rails `lib` isn't judged by the node preset).
  - Manual runs on vanmechelen-me and moma-apps-rails (Stimulus).

### Step 2 notes

- **Validated:**
  - vanmechelen-me (`src/pages → src/layouts → src/styles`).
  - moma-apps-rails, where Rails and Stimulus share one graph: `app/javascript/controllers → utils, channels, analytics`, and on the Ruby side existing `app/models → app/controllers`. The latter is real: models call `ApplicationController.helpers.format_promo_code`, exactly what the rails preset forbids for *new* code.
- **Two-phase references.** Languages whose references resolve against the whole tree (Ruby, Node, Python) cache the *raw* references per blob (`scan`) and resolve them on every analysis (`resolve`), so polls don't re-read unchanged files.
- **Containers include `packages`/`libs`,** so a workspace package without `src/` is still its own unit.

## Step 3 — Python (detailed plan)

- **Units:** a file's directory (its package), like Go. Root-level scripts aren't in a unit.
- **Source roots,** where absolute imports start:
  - `pyproject.toml` `[tool.setuptools] package-dir` (the `""` entry) and `[tool.poetry] packages` (`from`);
  - then `src/` if it exists;
  - then the repo root.
- **Scanner (`review/py_scan.go`):** blanks comments and strings (single, triple-quoted, prefixed `r`/`b`/`f`), joins logical lines (brackets, backslashes), and skips the block under `if TYPE_CHECKING:`, since type-only imports aren't runtime dependencies.
- **References:** `import a.b[ as c], d`, `from a.b import c, d`, `from . import x`, `from ..pkg import y`. Resolved under each root:
  - `a/b.py`, `a/b/__init__.py`, or the namespace package dir `a/b/`;
  - for `from a import c`, also the submodule `a/c.py`;
  - relative imports against the file's own package.
  - Anything else (stdlib, installed packages) is external. Exact.
- **No automatic preset for Python.** Django keeps models and views as *files* of one app, so they're the same unit and a rule can't tell them apart. Layer names like `domain`/`adapters` are a style, not universal. Teams write their own layers.
- **Surface:**
  - **Routes:** Flask `@x.route("…")`, FastAPI `@x.get|post|…("…")`, Django `path()`/`re_path()` in `urls.py`.
  - **CLI options:** argparse `add_argument("--x")`, click `@click.option("--x")`, typer `typer.Option(…, "--x")`.
  - **Dependencies:** `pyproject.toml` `[project] dependencies` (+ optional groups), `[tool.poetry.dependencies]`, `requirements*.txt`.
  - **Migrations:** Alembic `versions/`.
- **Tests:**
  - scanner fixtures (multi-line `from … import (…)`, an import inside a docstring, `TYPE_CHECKING`, backslash continuation);
  - a `src/` layout with `package-dir`, relative imports two levels up, a submodule import, a namespace package, an installed package (external);
  - surface patterns.
  - Manual run on moma-python-utilities.

### Step 3 notes

- **moma-python-utilities has no cross-package imports.** Its 34 `import moma.pipelines` come from inside `moma/pipelines`, so the draft correctly lists none. The fixture covers the cross-package cases.
- **Python comments aren't stripped for the surface patterns yet.** A route written in a `#` comment would count.

## Step 4 — Kotlin/Java (detailed plan)

- **Gradle modules:** `include(":app", ":core:data")` / `include ':app'` in `settings.gradle(.kts)` → `app`, `core/data`. `project(":x").projectDir = file("…")` overrides are honoured. Without settings, the repo root is the only module.
- **Units:** inside a module's source sets (`src/<set>/kotlin|java/`), the module dir plus the package one level below the module's root package. The root package is the longest common package of its main source files. Shown by path: `app/…/vrtsporza/data` labels as `app · data`. Files directly in the root package are the module's root unit.
- **Index:** the `package` line of every `.kt`/`.java` file (cached by blob id) → package → unit. A package declared in two units resolves to neither.
- **Scanner (`review/kt_scan.go`):** blanks comments (nested `/* */` in Kotlin), strings, raw strings and char literals, keeping `import`/`package` lines.
- **References:**
  - `import a.b.C` and `import a.b.C.member` resolve through the package index (longest package prefix);
  - `import a.b.*` resolves to package `a.b`;
  - fully-qualified uses in code (`a.b.C.x()`) are matched against indexed packages, longest prefix, so a qualified name isn't mistaken for a nested member.
  - Same-package references have no import and stay inside the unit. Exact.
- **Preset `android`** (automatic when an `AndroidManifest.xml` exists): `*/model` and `*/domain` must not depend on `*/ui` or `*/data`; `*/data` must not depend on `*/ui`. Paths are unit paths, so the preset matches by the last segment (the package level).
- **Surface:**
  - **Permissions:** `AndroidManifest.xml` `uses-permission` and exported components (`android:exported="true"`).
  - **Dependencies:** `minSdk`/`targetSdk`/`compileSdk`; `implementation|api|kapt|ksp(…)` coordinates in `build.gradle(.kts)`; `gradle/libs.versions.toml` libraries and versions.
  - **Routes:** Retrofit `@GET/@POST/…("path")`.
  - **Migrations:** Room `@Database(… version = N)` changes and `schemas/**.json`.
- **Tests:** a two-module fixture (`:app`, `:core:data`) with a wildcard import, a qualified reference, a Java file and an `android` preset violation; the manifest and version-catalog parsers. Manual runs on moma-app-android and sporza-vrt-watch-app.

### Step 4 notes

- **Kotlin units are `module/package-level`** (`feature/moma/data/di`, `core/ui/topbar`), not the source path. Source paths (`feature/moma/data/src/main/kotlin/com/moma/android/feature/moma/data/di`) were unusable in rules, and the module already names the architecture. Gradle settings aren't parsed: the module is the path before `src/<set>/`.
- **Rule segments are globs** (`ui-*`), and `**` matches any number of segments, since MoMA's UI modules are `ui-mobile`.
- **Validated:**
  - moma-app-android: 80+ units, no android-preset breaks today; `feature/profile/data → feature/moma/data` is a cross-feature dependency a team may want a rule for.
  - sporza-vrt-watch-app: `app/ui → app/data → app/model`.
  - PR #59 (museum map): no new dependencies and an empty surface, which is correct (no manifest or Gradle change). Its +4k lines are mostly SVG/JSON/HTML assets counted as logic; an "assets" kind is a follow-up.

## Step 5 — Swift (built; notes)

- **Units:**
  - SwiftPM targets (any `Package.swift` in the repo), whose `import Target` is explicit;
  - otherwise folders: two levels, three under `Features`/`Modules`/`Packages`/`Sources` (`Features/Tickets/Domain`), so feature layers are units.
  - Xcode targets from `project.pbxproj` (tier 2 in the plan) aren't built: moma-app-ios is one app target (plus test targets, already excluded as tests), generated by XcodeGen.
- **References inside a target are type names** matched to the unit that declares them (declarations cached by blob); a name declared in two units resolves to neither. Approximate.
- **Preset `ios`:** models/domain must not depend on data or views; data, networking and services must not depend on views; the design system must not depend on features.
- **Surface:**
  - privacy usage keys (Info.plist or XcodeGen `project.yml`) and entitlements → Permissions;
  - `Package.swift`/`Package.resolved`/`project.yml` packages/`Podfile` → Dependencies;
  - deployment target (`xcconfig`, `project.pbxproj`, `project.yml`, SwiftPM platforms).
  - Public API of library targets is left for later.
- **Validated on moma-app-ios:**
  - Two existing dependencies break the preset: `Features/Tickets/Domain → Features/Tickets/Data`, which is real (`TicketServices.swift` calls `TicketDateCoding` from Data), and `Features/Events/Models → Features/Tickets/Data`.
  - PR #68 adds two feature folders that depend on `DesignSystem/Tokens` and on each other.
- **Timeouts.** The first analysis of #68 came back partial: the blobless test clone fetched blobs lazily, and reads past the 10 s budget failed quietly. Now an analysis that runs out of time is an error (the page keeps the previous answer and retries), and the budget is 30 s.
