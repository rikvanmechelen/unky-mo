// Tests for static/overview-model.js, run by `node --test` (jstest_test.go
// runs them from go test when node is installed).
const test = require("node:test");
const assert = require("node:assert/strict");
const m = require("../static/overview-model.js");

const ROOT = "/repo";

// An "alarm" change: web gains a handler that imports tui (breaking a
// rule), Summarize is removed but still called, NewServer's signature
// changed, a test, a doc, generated mocks, and a file nobody edited here.
function alarmInputs() {
  const overview = {
    root: ROOT, mode: "branch", added: 600, removed: 100,
    kinds: { logic: { files: 4, lines: 120 }, test: { files: 1, lines: 50 } },
    files: [
      { path: "internal/web/handlers_overview.go", status: "A", added: 34, removed: 0, kind: "logic", area: "internal/web" },
      { path: "internal/web/server.go", status: "M", added: 3, removed: 1, kind: "logic", area: "internal/web" },
      { path: "internal/web/summary.go", status: "D", added: 0, removed: 84, kind: "logic", area: "internal/web" },
      { path: "internal/tui/badge.go", status: "M", added: 6, removed: 9, kind: "logic", area: "internal/tui" },
      { path: "internal/web/handlers_overview_test.go", status: "A", added: 50, removed: 0, kind: "test", area: "internal/web" },
      { path: "docs/overview.md", status: "A", added: 16, removed: 0, kind: "docs", area: "docs" },
      { path: "internal/web/mocks/mock.go", status: "A", added: 400, removed: 0, kind: "generated", area: "internal/web" },
      { path: "internal/tui/theme.go", oldPath: "internal/tui/colors.go", status: "R", added: 0, removed: 0, kind: "renamed", area: "internal/tui" },
    ],
  };
  const arch = {
    repo: true,
    packages: [{ path: "internal/web", status: "changed", lang: "go" }, { path: "internal/tui", status: "changed", lang: "go" }],
    edges: [{ from: "internal/web", to: "internal/tui", op: "+", violation: "web must not import internal/tui", lang: "go", files: [{ path: "internal/web/handlers_overview.go", line: 9 }] }],
    existing: [{ from: "cmd/mo", to: "internal/web", lang: "go" }],
    violations: 1,
    rules: { path: ".unky-mo/architecture.toml", found: true, layers: 3, presets: [], order: ["web", "tui"] },
    surface: {
      exports: [
        { op: "~", name: "web.NewServer", detail: "func NewServer(cfg Config, poll time.Duration) *Server", path: "internal/web/server.go", line: 40 },
        { op: "-", name: "web.Summarize", path: "internal/web/summary.go", line: 12 },
      ],
      deps: [{ op: "+", name: "golang.org/x/tools", path: "go.mod", line: 14 }],
    },
    languages: [{ name: "go", exact: true, units: 2 }],
    unitLayers: { "internal/web": "web", "internal/tui": "tui" },
    fileUnits: {
      "internal/web/handlers_overview.go": "internal/web", "internal/web/server.go": "internal/web", "internal/web/summary.go": "internal/web",
      "internal/tui/badge.go": "internal/tui", "internal/web/handlers_overview_test.go": "internal/web", "internal/web/mocks/mock.go": "internal/web/mocks",
      "internal/tui/theme.go": "internal/tui",
    },
  };
  const calls = {
    repo: true,
    funcs: [
      { id: "internal/web.handleOverview", name: "handleOverview", path: "internal/web/handlers_overview.go", line: 23, end: 28, unit: "internal/web", lang: "go", status: "added", testedBy: { id: "internal/web.TestHandleOverview", name: "TestHandleOverview", path: "internal/web/handlers_overview_test.go", line: 12 } },
      { id: "internal/web.NewServer", name: "NewServer", path: "internal/web/server.go", line: 40, end: 44, unit: "internal/web", lang: "go", status: "signature", sig: "func NewServer(cfg Config, poll time.Duration) *Server", oldSig: "func NewServer(cfg Config) *Server" },
      { id: "internal/web.Summarize", name: "Summarize", path: "internal/web/summary.go", line: 12, end: 18, before: true, unit: "internal/web", lang: "go", status: "removed" },
      { id: "internal/tui.RenderBadge", name: "RenderBadge", path: "internal/tui/badge.go", line: 29, end: 32, unit: "internal/tui", lang: "go", status: "renamed", from: "internal/tui.renderBadge" },
      { id: "cmd/mo.runWeb", name: "runWeb", path: "cmd/mo/web.go", line: 28, unit: "cmd/mo", lang: "go" },
      { id: "cmd/mo.report", name: "report", path: "cmd/mo/report.go", line: 41, unit: "cmd/mo", lang: "go" },
      { id: "(internal/tui.Theme).ColorFor", name: "Theme.ColorFor", path: "internal/tui/theme.go", line: 8, unit: "internal/tui", lang: "go" },
      { id: "(internal/tui.dark).ColorFor", name: "dark.ColorFor", path: "internal/tui/theme.go", line: 21, unit: "internal/tui", lang: "go" },
    ],
    calls: [
      { from: "internal/web.handleOverview", to: "internal/tui.RenderBadge", kind: "static", op: "+", sites: [{ path: "internal/web/handlers_overview.go", line: 26 }] },
      { from: "cmd/mo.runWeb", to: "internal/web.NewServer", kind: "static", sites: [{ path: "cmd/mo/web.go", line: 31 }] },
      { from: "internal/tui.RenderBadge", to: "(internal/tui.Theme).ColorFor", kind: "dynamic", sites: [{ path: "internal/tui/badge.go", line: 30 }] },
      { from: "(internal/tui.Theme).ColorFor", to: "(internal/tui.dark).ColorFor", kind: "impl" },
    ],
    findings: [
      { kind: "untested", func: "internal/tui.RenderBadge" },
      { kind: "signature-callers", func: "internal/web.NewServer", sites: [{ path: "cmd/mo/web.go", line: 31 }] },
      { kind: "removed-called", func: "internal/web.Summarize", sites: [{ path: "cmd/mo/report.go", line: 44 }] },
    ],
    languages: [{ name: "go", exact: true, units: 4 }],
    unitLayers: { "internal/web": "web", "internal/tui": "tui", "cmd/mo": "cmd" },
  };
  const trace = {
    turns: [{ n: 1, uuid: "u1", text: "Add an Overview endpoint" }, { n: 2, uuid: "u2", text: "Match the TUI badge" }],
    edits: [
      { turn: 1, path: ROOT + "/internal/web/handlers_overview.go", tool: "Write", id: "t1", note: "A new handler.", agent: null, hunks: [[1, Infinity, 1, 0]] },
      { turn: 1, path: ROOT + "/internal/web/server.go", tool: "Edit", id: "t2", note: "Poll interval.", agent: null, hunks: [[40, 2, 40, 1]] },
      { turn: 2, path: ROOT + "/internal/tui/badge.go", tool: "Edit", id: "t3", note: "Export it.", agent: null, hunks: [[29, 1, 29, 1]] },
      { turn: 2, path: ROOT + "/internal/web/handlers_overview.go", tool: "Edit", id: "t4", note: "", agent: "a1", hunks: [[26, 1, 26, 0]] },
      { turn: 1, path: ROOT + "/internal/web/handlers_overview_test.go", tool: "Write", id: "t5", note: "", agent: null, hunks: [[1, Infinity, 1, 0]] },
      { turn: 2, path: ROOT + "/docs/overview.md", tool: "Write", id: "t6", note: "", agent: "a1", hunks: [[1, Infinity, 1, 0]] },
    ],
  };
  const scope = { mode: "branch", result: { files: [{ path: "internal/tui/badge.go", verdict: "drift", reason: "TUI restyle." }, { path: "docs/overview.md", verdict: "in_scope" }] } };
  return { overview, arch, calls, trace, scope };
}

const alarm = () => m.buildModel(alarmInputs());

test("entities and their links", () => {
  const M = alarm();
  const E = M.E;
  assert.equal(E.get("pkg:internal/web").status, "changed");
  assert.equal(E.get("pkg:internal/web").layer, "web");
  assert.equal(E.get("pkg:cmd/mo").status, "context");
  assert.equal(E.get("pkg:cmd/mo").layer, "cmd", "a context unit's layer comes from /calls");
  assert.deepEqual(E.get("pkg:internal/web").files.slice(0, 2), ["file:internal/web/handlers_overview.go", "file:internal/web/server.go"]);
  assert.equal(E.get("file:internal/tui/badge.go").drift.verdict, "drift");
  assert.equal(E.get("file:docs/overview.md").drift, null, "in_scope isn't drift");
  assert.equal(E.get("file:docs/overview.md").unit, null, "docs have no unit");

  const fn = E.get("fn:internal/web.handleOverview");
  assert.equal(fn.mark, "+");
  assert.equal(fn.fileId, "file:internal/web/handlers_overview.go");
  assert.equal(fn.turn, 2, "the last edit to its lines was prompt 2's");
  assert.equal(fn.testedBy.name, "TestHandleOverview");
  assert.equal(E.get("fn:internal/web.Summarize").fileId, "file:internal/web/summary.go");
  assert.deepEqual(E.get("fn:(internal/tui.Theme).ColorFor").impls, ["fn:(internal/tui.dark).ColorFor"]);
  assert.deepEqual(E.get("fn:(internal/tui.dark).ColorFor").implOf, ["fn:(internal/tui.Theme).ColorFor"]);
  assert.ok(!E.has("call:(internal/tui.Theme).ColorFor>(internal/tui.dark).ColorFor|impl|"), "impl edges aren't calls");

  const imp = E.get("imp:internal/web>internal/tui|+");
  assert.equal(imp.kind, "broken");
  assert.equal(E.get("imp:cmd/mo>internal/web|").kind, "ctx");
  assert.deepEqual(M.order.imp, ["imp:internal/web>internal/tui|+"]);

  assert.equal(E.get("con:exports:0").fnId, "fn:internal/web.NewServer");
  assert.equal(E.get("con:exports:1").fnId, "fn:internal/web.Summarize", "a removed export matches the removed function");
  assert.equal(E.get("con:deps:0").fnId, null);

  // Findings sorted red, warn, fold; the signature finding sits on the call at its site.
  assert.deepEqual(M.order.find.map((id) => E.get(id).kind), ["removed-called", "signature-callers", "untested"]);
  const sig = [...E.values()].find((e) => e.type === "find" && e.kind === "signature-callers");
  assert.deepEqual(sig.calls, ["call:cmd/mo.runWeb>internal/web.NewServer|static|"]);

  // Prompts and their files; a subagent's edit; a file no edit touched.
  assert.deepEqual(E.get("prompt:2").files.map((f) => [f.path, f.agent]), [["internal/web/handlers_overview.go", true], ["internal/tui/badge.go", false], ["docs/overview.md", true]]);
  assert.deepEqual(M.orphans, ["file:internal/web/summary.go", "file:internal/web/mocks/mock.go", "file:internal/tui/theme.go"]);
  assert.deepEqual(E.get("file:internal/web/server.go").prompts[0].notes, ["Poll interval."]);
});

test("a model from the overview alone", () => {
  const { overview } = alarmInputs();
  const M = m.buildModel({ overview });
  assert.equal(M.E.get("file:internal/web/server.go").unit, null);
  assert.equal(M.loaded.arch, false);
  assert.ok(m.ovVerdict(M).map((s) => s.t).join("").endsWith("Checking imports and calls…"));
  assert.deepEqual(m.ovChecks(M).map((c) => c.tone), ["calm", "calm", "pending", "pending"]);
  assert.equal(m.ovReviewQueue(M)[0].key, "logic");
});

test("related", () => {
  const M = alarm();
  const has = (id, ...want) => {
    const s = m.ovRelated(M, id);
    for (const w of want) assert.ok(s.has(w), `${id} → ${w}`);
    return s;
  };
  has("fn:internal/web.NewServer", "fn:cmd/mo.runWeb", "call:cmd/mo.runWeb>internal/web.NewServer|static|", "pkg:cmd/mo", "file:internal/web/server.go", "con:exports:0", "prompt:1");
  has("fn:(internal/tui.Theme).ColorFor", "fn:(internal/tui.dark).ColorFor");
  has("pkg:internal/web", "imp:internal/web>internal/tui|+", "pkg:internal/tui", "fn:internal/web.handleOverview", "file:internal/web/server.go");
  assert.ok(!m.ovRelated(M, "pkg:internal/web").has("imp:cmd/mo>internal/web|"), "existing imports aren't related");
  has("file:internal/web/handlers_overview.go", "pkg:internal/web", "fn:internal/web.handleOverview", "prompt:1", "prompt:2", "imp:internal/web>internal/tui|+", "call:internal/web.handleOverview>internal/tui.RenderBadge|static|+");
  has("imp:internal/web>internal/tui|+", "call:internal/web.handleOverview>internal/tui.RenderBadge|static|+", "fn:internal/tui.RenderBadge", "file:internal/web/handlers_overview.go");
  const find = M.order.find[0];
  has(find, "fn:internal/web.Summarize", "file:internal/web/summary.go");
  has("prompt:2", "file:internal/tui/badge.go", "pkg:internal/tui", "fn:internal/web.handleOverview");
  has("con:exports:0", "fn:internal/web.NewServer", "file:internal/web/server.go", "pkg:internal/web");
  has(m.cellID("internal/tui/badge.go", 2), "file:internal/tui/badge.go", "prompt:2", "pkg:internal/tui");
  assert.equal(m.ovRelated(M, "nope").size, 0);
});

test("where and labels", () => {
  const M = alarm();
  assert.deepEqual(m.ovWhere(M, "fn:internal/web.Summarize"), { path: "internal/web/summary.go", line: 12, side: "old" });
  assert.deepEqual(m.ovWhere(M, "imp:internal/web>internal/tui|+"), { path: "internal/web/handlers_overview.go", line: 9, side: "new" });
  assert.deepEqual(m.ovWhere(M, M.order.find[0]), { path: "cmd/mo/report.go", line: 44, side: "new" });
  assert.deepEqual(m.ovWhere(M, "con:exports:0"), { path: "internal/web/server.go", line: 40, side: "new" });
  assert.deepEqual(m.ovWhere(M, "file:internal/web/summary.go"), { path: "internal/web/summary.go", line: 0, side: "old" });
  assert.equal(m.ovLabel(M, "imp:internal/web>internal/tui|+"), "web → tui");
  assert.equal(m.ovLabel(M, m.cellID("docs/overview.md", 2)), "overview.md × 2");
  assert.equal(m.ovSection(M, "file:x"), "foot");
  assert.equal(m.ovSection(M, "prompt:1"), "trace");
  assert.equal(m.ovSection(M, "fn:x"), "map");
});

test("verdict, checks, caveats and chips", () => {
  const M = alarm();
  const v = m.ovVerdict(M, { ticket: "OP-212" });
  assert.equal(v.map((s) => s.t).join(""), "A change across 3 areas, with 1 broken layer rule, 1 call that won’t work, 1 signature change with callers left behind, 3 contract changes and 1 file outside OP-212.");
  assert.equal(v.find((s) => s.t === "1 broken layer rule").target, "imp:internal/web>internal/tui|+");
  assert.equal(m.ovVerdictSub(M), "120 of 700 changed lines are logic.");
  const c = m.ovChecks(M);
  assert.deepEqual(c.map((x) => x.tone), ["calm", "calm", "red", "red", "ink", "ink"]);
  assert.equal(c[5].label, "3 files changed outside the conversation");
  assert.equal(m.ovChecks(M, { filtered: true }).length, 5);
  assert.ok(m.ovCaveats(M)[0].text.startsWith("Layer rules: Checked against .unky-mo/architecture.toml (3 layers)"));
  assert.deepEqual(m.ovChips(M).map((x) => x.t), ["8 files", "+600 −100 lines", "3 areas", "+1 −0 imports between parts", "+1 −0 dependencies", "3 contract changes"]);

  const healthy = m.buildModel({ ...alarmInputs(), calls: { repo: true, funcs: [], calls: [], findings: [] }, arch: { ...alarmInputs().arch, edges: [], violations: 0, surface: {} }, scope: null });
  assert.ok(m.ovVerdict(healthy).map((s) => s.t).join("").endsWith(". Nothing needs your attention."));
});

test("review queue and progress", () => {
  const inputs = alarmInputs();
  const sig = m.reviewSig(inputs.overview.files[1]);
  const M = m.buildModel({ ...inputs, reviewed: { "internal/web/server.go": sig, "internal/tui/badge.go": "M:1:1" } });
  const q = m.ovReviewQueue(M);
  assert.deepEqual(q.map((g) => g.key), ["needs", "con", "logic", "tests", "docs", "noise"]);
  assert.deepEqual(q[0].ids.map((id) => M.E.get(id).type), ["find", "imp", "find", "file"]);
  assert.deepEqual(q[2].ids, ["file:internal/web/summary.go", "file:internal/web/handlers_overview.go", "file:internal/tui/badge.go", "file:internal/web/server.go"]);
  assert.deepEqual(q[3].ids.map((id) => M.E.get(id).type), ["file", "find"], "untested folds into Tests");
  assert.deepEqual(m.ovProgress(M), { done: 1, total: 4 }, "a tick for an older version doesn't count");
  const tui = m.ovReviewQueue(M, { area: "internal/tui", hidden: new Set(["renamed"]) });
  assert.deepEqual(tui.find((g) => g.key === "logic").ids, ["file:internal/tui/badge.go"]);
  assert.equal(tui.find((g) => g.key === "noise"), undefined);
});

test("moved helpers", () => {
  const { rows } = m.layoutArchGraph([{ path: "a" }, { path: "b" }, { path: "c" }], [{ from: "a", to: "b" }, { from: "b", to: "c" }, { from: "c", to: "a" }]);
  assert.equal(rows.flat().length, 3, "a cycle still places every node");
  const trace = { turns: [{ n: 1 }, { n: 2 }], edits: [{ turn: 1, path: "/r/x.go", hunks: [[10, 5, 10, 0]] }, { turn: 2, path: "/r/x.go", hunks: [[1, 3, 1, 0]] }] };
  assert.equal(m.turnForRange(trace, "/r", "x.go", 13, 14).n, 1, "turn 1's lines moved down by turn 2's insert");
  assert.equal(m.turnForRange(trace, "/r", "x.go", 1, 2).n, 2);
  const { groups } = m.buildTraceRows([{ path: "x.go" }, { path: "y.go" }], trace.edits, "/r");
  assert.deepEqual(groups.map((g) => g.turn), [1, null]);
});

test("selection history", () => {
  let h = m.selInitial();
  assert.equal(m.selCurrent(h), null);
  h = m.selPush(h, "fn:a");
  h = m.selPush(h, "fn:b");
  assert.equal(m.selPush(h, "fn:b"), h, "selecting the current one is a no-op");
  h = m.selPush(h, null);
  assert.equal(m.selCurrent(h), null);
  h = m.selBack(h);
  assert.equal(m.selCurrent(h), "fn:b");
  h = m.selBack(h);
  assert.equal(m.selCurrent(h), "fn:a");
  assert.equal(m.selForward(h).at, 2);
  h = m.selPush(h, "file:x");
  assert.deepEqual(h.stack, [null, "fn:a", "file:x"], "a new selection drops the forward part");
  assert.equal(m.selForward(h), h);
  assert.equal(m.selBack(m.selBack(m.selBack(h))).at, 0);
  for (let i = 0; i < 40; i++) h = m.selPush(h, "fn:" + i);
  assert.equal(h.stack.length, 30);
  assert.equal(h.at, 29);
  assert.deepEqual(m.selCrumbs(h).map((c) => c.id), ["fn:36", "fn:37", "fn:38", "fn:39"]);
  let r = m.selPush(m.selPush(m.selPush(m.selInitial(), "a"), "b"), "a");
  assert.deepEqual(m.selCrumbs(r).map((c) => c.id), ["b", "a"], "a repeat shows once, at its latest");
  assert.ok(m.selValid(h));
  assert.ok(!m.selValid({ stack: [1], at: 0 }));
  assert.ok(!m.selValid({ stack: [null], at: 1 }));
  assert.ok(!m.selValid(null));
});

test("section summaries", () => {
  const M = alarm();
  const s = m.ovSectionSummaries(M, { scope: "done", stale: true, hidden: 2, area: "internal/web", focus: "fn:internal/web.NewServer" });
  assert.deepEqual(s.map, { summary: "2 packages · 4 changed functions · focus on NewServer", flag: "red" });
  assert.deepEqual(s.foot, { summary: "3 areas · 120 lines of logic · 2 kinds hidden · internal/web", flag: null });
  assert.deepEqual(s.trace, { summary: "2 prompts · 3 files outside the conversation", flag: null });
  assert.deepEqual(s.scope, { summary: "1 file outside the ask · out of date", flag: "yellow" });
  const bare = m.ovSectionSummaries(m.buildModel({ overview: alarmInputs().overview }));
  assert.equal(bare.map.summary, "reading packages… · reading calls…");
  assert.equal(bare.scope.summary, "Not checked yet");
});
