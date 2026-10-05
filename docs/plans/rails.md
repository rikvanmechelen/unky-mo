# Better Ruby on Rails support

## Goal

Make the Overview (architecture graph, contract surface, Functions view) and `mo calls` understand a Rails app the way a Rails developer reads it:
- routes lead to actions, and actions to templates;
- views drive Stimulus controllers;
- a model's associations and columns are methods;
- jobs, mailers and policies are called by framework conventions, not by name.

Built for the three apps it will run on:

| App | Stack notes |
|---|---|
| moma-org-rails | importmap, Stimulus/Turbo, ViewComponent (`app/components`), Minitest + factory_bot, a 45-line `routes.rb` |
| moma-apps-rails | Pundit, AASM, ActiveModel serializers + jbuilder (a JSON API for the mobile apps), Minitest, an 882-line `routes.rb` |
| moma-chatbot | importmap, Stimulus/Turbo, jbuilder, neighbor (pgvector), Minitest |

Constraints (same as `architecture-languages.md` and `call-graph.md`):
- Nothing to install in the project, and no Ruby needed. Everything is read from the index, so it works for the working tree, a head commit and the base.
- Heuristics are labelled approximate. Unresolved means counted, never guessed.

## Items

**Now:** 1, 2, 3, 5, 6. **Future options:** 4, 7–14.

| # | Item | Size | Status |
|---|---|---|---|
| 1 | Routes from `config/routes.rb` | M | now |
| 2 | Stimulus links (views → JS controller methods) | M | now |
| 3 | ActiveRecord-aware receivers | L | now |
| 4 | Schema-aware database changes | M | future |
| 5 | Rails test conventions | M | now |
| 6 | Implicit calls (jobs, mailers, Pundit, templates, partials, components) | M | now |
| 7 | API response shape (jbuilder, serializers) | M | future |
| 8 | Strong params | S | future |
| 9 | Config YAML, credentials, ENV | S–M | future |
| 10 | i18n keys | S–M | future |
| 11 | More layers in the `rails` preset | S | future |
| 12 | Autoload paths and Zeitwerk config | S | future |
| 13 | Finer units (namespaces) and per-repo `lib` → `app` rules | M | future |
| 14 | Ruby scanner gaps | S each | future |

---

## 1. Routes from `config/routes.rb` (now)

**Parser** (`review/rails_routes.go`, pure, no Ruby):
- **Input:** `config/routes.rb` plus files it `draw(:name)`s (`config/routes/name.rb`), read through the index.
- **Comments:** `rubyCode` blanks comments; the string literals (paths, `to:` targets) are read from the original line at the same offsets.
- **Output:** `[]railsRoute{Verb, Path, Controller, Action, Helper, File, Line}`. `Controller` is a constant path (`Admin::TicketsController`), `Helper` the route name without `_path`/`_url` (`admin_ticket`, `""` if none).
- **Blocks:** a stack of scopes from `do … end`, each scope holding a path prefix, a module prefix, a name prefix, a controller and, for nested resources, the parent param:
  - `namespace :admin` adds path `/admin`, module `Admin::` and name `admin_`.
  - `scope "x"` / `scope path:, module:, as:` add only what they name.
  - `controller :c do` sets the controller.
  - `resources :x do` inside, and `member do` / `collection do`.
  - `concern :name do … end` is recorded and expanded where `concerns: [:name]` is used.
- **`resources :tickets`** gives the seven actions (index, create, new, edit, show, update ×2, destroy) with their verbs and helpers (`tickets`, `new_ticket`, `edit_ticket`, `ticket`):
  - `only:` and `except:` filter them (symbol arrays and `%i[]`);
  - `controller:`, `path:`, `as:`, `module:`, `param:`, `shallow: true` (nested member routes lose the parent prefix);
  - `resource` (singular) gives six actions with no `:id` and no index;
  - nested resources prefix `/tickets/:ticket_id`.
- **`get`/`post`/`put`/`patch`/`delete`/`match … via:`:**
  - `"path" => "c#a"`, `to: "c#a"`, `controller:/action:`;
  - a bare `:action` inside `member`/`collection`/`controller`;
  - `as:` names the helper, otherwise Rails' default from the path.
- **`root "c#a"` / `root to:`** gives `GET /` with helper `root`.
- **Skipped, but counted:** `mount` (an engine: a route item with no action), `redirect(...)`, constraint lambdas and anything else the parser doesn't know. They go in `RouteSet.Skipped` and show as "N routes not read" rather than failing.
- **Cached:** by the routes files' blob ids.

**Contract surface:** the routes category replaces today's line regex for `routes.rb` with the resolved set:
- A route's identity is `VERB path`. Its detail is `→ Controller#action` and the helper.
- **Added/removed:** by identity. **Changed:** the same identity with a different target.
- Controller actions stay (`actionChanges`): they're the other half and catch unrouted actions.

**Call graph** (through a new optional framework hook, so it isn't a special case inside `callDelta`):
- **The hook:** `hExtra` is an interface an `hLang` may implement: `extraDefs(idx *index) map[string][]hDef` adds definitions to files after their (cached) scan. `newHResolver` merges them into copies of the cached `hFile`s, never the cached values.
- Ruby uses it for the routes files:
  - **Each route** is a definition `<route>` (an entry point, so never `untested`). Its ID is `route:VERB /path`, its display `GET /tickets/:id`, and its body hash is the target, so retargeting a route is a change. It calls `Controller#action`.
  - **Each route helper name** is a definition with ID `route:<helper>`, displayed as `ticket_path`. Also an entry point.
- **Resolution:** a bare call to `x_path`/`x_url` (in controllers, views, helpers, mailers, components) resolves to `route:x`.
- **Findings**, through the existing rules:
  - a removed route helper still called anywhere → `removed-called` on `route:ticket`, sites included;
  - a removed action still routed → `removed-called` on `Controller#action` from the route;
  - a route added to an action that doesn't exist shows as an unresolved call on the route node. **New finding kind `route-without-action`** (yellow) for that case.

**Tests:**
- Parser table tests covering each form above, nesting, shallow, concerns, `draw`, comments and strings that look like routes.
- An 882-line-style fixture assembled from those forms.
- A real-git fixture: a route added and removed, a removed action still routed, a removed helper still used in a view and a controller, a route retargeted.
- The surface: routes diffed by identity, a moved line not a change.

## 2. Stimulus links (now)

**Controllers** (built per version, cached by blob id):
- Every `*_controller.js`/`*-controller.js` (and `.ts`) under `app/javascript/` and `app/components/`.
- Identifier by Stimulus' convention: the path below `controllers/` (or the component dir), `_`→`-`, `/`→`--`, minus `_controller`. So `admin/user_list_controller.js` is `admin--user-list`.
- Its methods and `static targets = [...]` come from the cached JS scan (`hcalls:node.js` symbols). The method IDs are the JS analyzer's (`path#default.add`, or the named class).

**View side:** the Ruby view scan (ERB/HAML/Slim, and ViewComponent templates and component `.rb` files) also reads, from the original source:
- `data-controller="a b"`;
- `data-action="click->cart#add keyup->search#filter:prevent"` (default events: `identifier#method`);
- `data-<identifier>-target="name"`;
- the Ruby helper forms `data: { controller: "…", action: "…", "cart-target": "…" }` and `data_controller:`/`data_action:`.

These become calls on the view's definition with receiver `stimulus:<identifier>`:
- an action is a call to the method;
- `data-controller` is a ref to the controller class;
- a target is a call to the pseudo-name `target:<name>`.

**Cross-language resolution:**
- `rbCalls.resolve` maps `stimulus:<identifier>` + method to the JS ID when the controller file exists. Otherwise it's unresolved, with want = the JS ID it would have.
- For a target, it checks the controller's static targets: missing → unresolved with want `target:<identifier>.<name>`.
- `Calls` gains a **cross-language post-pass**, needed because each language's delta only sees its own functions:
  - it fills bare context nodes (callee in another language) from every language's new-version set;
  - it matches every removed function and every removed Stimulus target against every language's wants, so a removed JS method still bound in a view is `removed-called`.
- **New finding kind `stimulus-unbound`** (red): a `data-action` or target naming a method or target that doesn't exist in the controller. Raised even when the view didn't change, since that's when renames break.

**Tests:**
- Identifier naming (nested, dashes, components).
- The view scanner: attribute and helper forms, several actions in one attribute, modifiers, HAML.
- A real-git fixture with importmap and Stimulus:
  - a method renamed in JS while the ERB still binds the old name;
  - a removed target;
  - an added binding;
  - the cross-language edge shown with both ends' real paths.

## 3. ActiveRecord-aware receivers (now)

Today `x.something` resolves only when `x` was assigned `Const.new`. Rails code mostly calls methods on records and relations.

**Types** a receiver can have: `Const` (an instance), `Const[]` (a relation or collection of `Const`), `Const.self` (the class). Added to the scanner's locals map, and to a new per-class instance-variable map.

**Where types come from:**
- **Finders on a model class:**
  - instances: `find`, `find_by`, `find_by!`, `find_or_create_by(!)`, `find_or_initialize_by`, `create(!)`, `new`, `first(!)`, `last(!)`, `take(!)`, `sole`;
  - relations: `where`, `all`, `order`, `includes`, `joins`, `limit`, `not`, `or`, `preload`, `eager_load`, `distinct`, `select`, `rewhere`, and any `scope :name` of that class;
  - `Const.where(...).first` and the like give an instance again.
- **Associations** (from the model scan, by convention):
  - `belongs_to`/`has_one :x` → `X`;
  - `has_many`/`has_and_belongs_to_many :xs` → `X[]` (singularized, as in `ruby.go`);
  - `class_name:` wins;
  - `through:` follows the source association;
  - polymorphic → unknown.
  - Association readers are methods of the model too, so `ticket.event` resolves as a call (to a synthetic association definition, an entry point) and types the chain.
- **Instance variables:**
  - assignments in a class (`@ticket = Ticket.find(params[:id])`, typically in a `before_action` method) type `@ticket` for every method of that class and its subclasses;
  - a controller's types also apply to its views (the view → controller mapping `viewController` already does).
- **Blocks:** `collection.each/map/select/find/find_each/each_with_index/filter_map/flat_map/sort_by/group_by { |x| }` (and `do |x|`) type `x` from the collection.
- **`delegate :a, :b, to: :assoc`** (and `prefix: true`) defines `a`/`b` on the class, calling `Assoc#a`.
- **`enum status: {…}` / `enum :status, {…}`:** generated `status`, `status=`, `active?`, `active!`, the `active` scope, and `Model.statuses`.
- **AASM** (`moma-apps-rails`): `aasm do … event :ship do … end` generates `ship`, `ship!`, `may_ship?`; `after:`/`before:`/`guard:` symbols are refs; states generate `shipped?`.
- **Columns from `db/schema.rb`:**
  - `create_table "artworks"` columns become attribute definitions on the model (by table name convention or `self.table_name`): reader, writer and `?`;
  - `artwork.title` resolves to an attribute node (an entry point, displayed `Artwork.title (column)`) instead of being unresolved;
  - cached by `schema.rb`'s blob id.

**Chains:** the scanner keeps the receiver chain as written (`@ticket.event.venue`). The resolver types it left to right through instance variables, locals, associations, finders and columns, stopping at the first unknown step. That step and the rest are unresolved, never guessed.

**Tests:**
- Each typing source on its own.
- A chain through two associations.
- An instance variable set in `before_action` and used in the action and in its view.
- Block params.
- `delegate`, `enum`, AASM, schema columns.
- Unknown steps stay unresolved.
- A real-git fixture where a model method rename is caught through `@ticket.event.old_name` in a view.

## 4. Schema-aware database changes (future)

- **Surface:** diff `db/schema.rb` (or `structure.sql`) by table, column (name, type, null, default), index and foreign key, instead of "migration added".
- **Findings:**
  - a migration added without a `schema.rb` change (or the reverse);
  - a removed or renamed column still read in code, using item 3's column typing;
  - a new `belongs_to` or `_id` column with no index;
  - `remove_column` with no `ignored_columns` step (a deploy-safety hint).

## 5. Rails test conventions (now)

- **Minitest:**
  - `test "does x" do … end` blocks (and `describe`/`it`, `setup`/`teardown`) become test definitions, named `test "does x"`, so test reachability sees them. Today a test file's whole body is one `<main>`.
  - `ActiveSupport::TestCase` / `ActionDispatch::IntegrationTest` classes are recognised; `assert_*` / `refute_*` aren't resolved.
- **Request tests reach actions:**
  - `get ticket_path(t)` / `post "/tickets"` in an integration or controller test is a call to the route (item 1), and so reaches the action.
  - `get :show` in a functional controller test resolves to that controller's action, by the test's class name.
- **Convention coverage** (engine, `calls.go`): a language may implement `testsFor(path) []string` (Ruby: `app/models/x.rb` → `test/models/x_test.rb`, `spec/models/x_spec.rb`, and for controllers also `test/controllers/…`, `test/integration/…`). `untested` then also accepts "the conventional test file references this function's name or class", which catches tests that go through framework calls the graph can't see.
- **New finding `test-not-updated`** (grey hint): a changed app file whose conventional test file exists and didn't change in this change. Aggregated per file.
- factory_bot `factory :ticket` blocks are references to the model class.

**Tests:**
- The scanner on `test "…" do`, `describe`/`it`, setup.
- A fixture: a model change with and without its test file changing; an integration test reaching an action through a route helper; a functional test's `get :show`.
- Convention mapping for models, controllers, jobs, mailers, services, helpers, components.

## 6. Implicit calls (now)

- **Jobs:** `X.perform_later(…)`, `X.perform_now`, `X.set(…).perform_later` → `X#perform`.
- **Mailers:**
  - `XMailer.with(…).welcome.deliver_later` and `XMailer.welcome(…).deliver_now` → `XMailer#welcome` (a class call runs an instance method);
  - the mailer action → its template (`app/views/x_mailer/welcome.*`).
- **Pundit:**
  - `authorize @ticket` / `authorize Ticket` (in a controller action) → `TicketPolicy#<action>?`; `authorize x, :refund?` → `#refund?`;
  - `policy_scope(Ticket)` → `TicketPolicy::Scope#resolve`;
  - `policy(@x).show?` → `XPolicy#show?`.
  - The record's type comes from item 3.
- **Controller → template:**
  - an action calls its implicit template `app/views/<controller path>/<action>.*` when it exists and the action has no `render`/`redirect_to`/`head`;
  - `render :edit` / `render "x/y"` / `render template:` call that template;
  - `respond_to` blocks count each format's template.
- **Partials:**
  - `render "shared/card"`, `render partial: "x"`, `render @tickets` / `render ticket` (→ `tickets/_ticket`), `render collection:` → the partial view;
  - relative names use the current view's directory.
- **ViewComponent:**
  - `render(FooComponent.new(…))` → `FooComponent#initialize`, plus the component's template as a call from the component;
  - `render FooComponent.with_collection(…)`.
- **Turbo:**
  - `broadcasts_to` / `broadcast_*_to` reference the partial they render;
  - `turbo_stream.replace "x", partial:` → the partial. Lower value; done if cheap.
- **Callbacks with blocks/lambdas** (`after_commit -> { notify }`): the calls belong to the class, as today, but the lambda's calls resolve on the instance side.

Template definitions are the existing `view:<path>` nodes, so controllers, views, partials and components form one graph.

**Tests:** a fixture covering each rule, with `removed-called` for a removed partial still rendered and a removed policy method still authorized.

## 7. API response shape (future)

- **What changes count:** fields added, removed or renamed in jbuilder views (`json.(x, :a, :b)`, `json.a`, `json.extract!`, `json.partial!`, `json.array!`) and in ActiveModel serializers (`attributes`, `attribute`, `has_many`, `belongs_to`, `has_one`), per endpoint.
- **Surface:** a new "API" category. A removed field is red, since the mobile apps depend on it.
- **Endpoint linking:** a template or serializer is tied to its route via items 1 and 6, so a field change names the endpoint.

## 8. Strong params (future)

- **What:** `params.require(:x).permit(...)` (and `params.permit`, `expect`): keys added or removed, with nested hashes and arrays.
- **Surface:** a "Params" category.

## 9. Config YAML, credentials, ENV (future)

- Keys added or removed in `config/*.yml` (per environment).
- `Rails.application.credentials.dig(:a, :b)` / `.a.b` reads.
- `ENV`/`ENV.fetch` in `config/` and initializers (the env surface already reads Ruby `ENV`).
- **Finding:** a key read in code but missing from the YAML.

## 10. i18n keys (future)

- **Keys from code:** `t("x.y")`, `t(".lazy")` (resolved against the view or controller path), `I18n.t`.
- **Findings:**
  - keys used but missing from `config/locales/*.yml` for the default locale;
  - keys removed from the YAML but still used.

## 11. More layers in the `rails` preset (future)

- **New layer rules:**
  - `app/policies` may use only models;
  - `app/serializers` no services/controllers;
  - `app/forms`, `app/observers`, `app/validators` no controllers/views;
  - `app/components` no direct queries;
  - `app/mailers` and `app/channels` no controllers.
- **New check (approximate):** a query in a view or component (`.where(`, `.find(`, `.order(` on a model constant in ERB or a component).

## 12. Autoload paths and Zeitwerk config (future)

- **What to read:** `config.autoload_paths`, `eager_load_paths`, `Rails.autoloaders.main.collapse(...)` and `ignore(...)` in `config/application.rb`.
- **Why:** the constant index then matches the app's real loader instead of the defaults.

## 13. Finer units and per-repo `lib` → `app` rules (future)

- **Units:** an option (rules file `[rails] units = "namespace"`) to unit by `app/<layer>/<namespace>` so domain boundaries show inside a layer. A light Packwerk.
- **Rules:** per-repo `lib` → `app` rules in the rules file.

## 14. Ruby scanner gaps (future)

- `Struct.new do` / `Class.new do` bodies as their own class.
- Lambdas inside callback options.
- `super` into a framework class not counted as unresolved.
- `<main>` of specs/rake tasks hidden from the graph by default.
- `included do` blocks in concerns resolving on the including class.
- `method_missing` / `respond_to_missing?` classes marked dynamic.

---

## How the "now" items are built

Work is split so parallel changes touch different code:
- **Agent A (worktree): items 1 and 2.**
  - `rails_routes.go` (+ tests), the routes surface, the `hExtra` hook in `hcalls.go`;
  - in `rbcalls.go`: route and helper resolution, and Stimulus extraction in the view scan;
  - `stimulus.go`, and the cross-language post-pass and new finding kinds in `calls.go`.
- **Agent B (worktree): items 3 and 6, plus item 5's Ruby scanner part** (Minitest `test "…" do` / `describe`/`it` definitions, request tests calling routes once item 1 lands — wired after the merge). All in `rbcalls.go` scan and resolve, plus `rails_schema.go` for columns.
- **Lead: item 5's engine part:** `testsFor` convention coverage in `calls.go`, the `test-not-updated` finding, UI text for the new finding kinds, docs, merging, and tuning.

Every item is checked on the last 20 commits of all three apps (read-only, as for the call graph): parse rate, resolved share before and after, findings reviewed by hand.

## Notes

(filled in as items land)
