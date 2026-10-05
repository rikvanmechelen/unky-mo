package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// moduleRepo builds a Go module on main, then a branch "feat" with some
// committed and some uncommitted changes:
//
//	a/a.go     imports b; gains an import of c, a route, env vars, a config tag; API changes
//	a/a_test.go (new) imports d — test imports don't count
//	d/d.go     drops its import of c, but d/d2.go (unchanged) still imports c
//	e/e.go     new package, imports a
//	f/f.go     deleted package that imported b
//	cmd/mo     package main swaps a flag
//	go.mod     gains a requirement
//	db/migrate/001_init.sql added
func moduleRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.22\n\nrequire (\n\tgithub.com/old/dep v1.0.0\n)\n")
	write(t, dir, "a/a.go", `package a

import "example.com/m/b"

func Old(x int) int { return x + b.N }

func Gone() {}

type Config struct {
	Addr string
	port int
}
`)
	write(t, dir, "b/b.go", "package b\n\nconst N = 1\n")
	write(t, dir, "c/c.go", "package c\n\nimport \"os\"\n\nvar Shared = os.Getenv(\"SHARED\")\n")
	write(t, dir, "d/d.go", "package d\n\nimport \"example.com/m/c\"\n\nvar X = c.Shared\n")
	write(t, dir, "d/d2.go", "package d\n\nimport \"example.com/m/c\"\n\nvar Y = c.Shared\n")
	write(t, dir, "f/f.go", "package f\n\nimport \"example.com/m/b\"\n\nvar Z = b.N\n")
	write(t, dir, "cmd/mo/main.go", "package main\n\nfunc Exported() {}\n\nfunc main() { cmd.Flags().String(\"old-flag\", \"\", \"\") }\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "a/a.go", `package a

import (
	"os"

	"example.com/m/b"
	"example.com/m/c"
)

func Old(y string) int { return len(y) + b.N + len(c.Shared) }

func New() {}

var token = os.Getenv("NEW_TOKEN")
var shared = os.Getenv("SHARED")

const pattern = "GET /api/things/{id}"

type Config struct {
	Addr   string
	port   int
	secret string
	Listen string `+"`toml:\"listen\"`"+`
}
`)
	run(t, dir, "rm", "-q", "f/f.go")
	run(t, dir, "commit", "-q", "-am", "feat work")
	// Uncommitted on top.
	write(t, dir, "a/a_test.go", "package a\n\nimport \"example.com/m/d\"\n\nvar _ = d.X\n")
	write(t, dir, "d/d.go", "package d\n\nvar X = 1\n")
	write(t, dir, "e/e.go", "package e\n\nimport \"example.com/m/a\"\n\nvar _ = a.New\n")
	write(t, dir, "cmd/mo/main.go", "package main\n\nfunc Exported(x int) {}\n\nfunc main() { cmd.Flags().BoolVarP(&v, \"new-flag\", \"n\", false, \"\") }\n")
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.22\n\nrequire (\n\tgithub.com/old/dep v1.2.0\n\tgithub.com/new/dep v0.1.0 // indirect\n)\n")
	write(t, dir, "db/migrate/001_init.sql", "create table x (id int);\n")
	write(t, dir, ".unky-mo/architecture.toml", `
[[layer]]
name = "core"
paths = ["a"]
allow = ["b"]

[[layer]]
name = "leaf"
paths = ["f"]
deny = ["b"]
`)
	return dir
}

func analyze(t *testing.T, dir string) *Analysis {
	t.Helper()
	ctx := context.Background()
	o, err := gitfiles.GetOverview(ctx, moexec.DefaultCommander, dir, gitfiles.ModeBranch)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(ctx, moexec.DefaultCommander, o)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func edgeKeys(es []Edge) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Op+e.From+">"+e.To)
	}
	return out
}

func changeKeys(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Op+c.Name)
	}
	return out
}

func TestAnalyzeArchitecture(t *testing.T) {
	a := analyze(t, moduleRepo(t))
	if a.Module != "example.com/m" {
		t.Errorf("module %q", a.Module)
	}
	wantPkgs := []Package{{"a", "changed", "go"}, {"cmd/mo", "changed", "go"}, {"d", "changed", "go"}, {"e", "added", "go"}, {"f", "removed", "go"}}
	if !reflect.DeepEqual(a.Packages, wantPkgs) {
		t.Errorf("packages %+v, want %+v", a.Packages, wantPkgs)
	}
	// d→c stays (d2.go), a_test.go's import of d doesn't count.
	if got, want := edgeKeys(a.Edges), []string{"+a>c", "+e>a", "-f>b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v", got, want)
	}
	if got, want := edgeKeys(a.Existing), []string{"a>b", "d>c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("existing %v, want %v", got, want)
	}
	byKey := map[string]Edge{}
	for _, e := range a.Edges {
		byKey[e.Op+e.From+">"+e.To] = e
	}
	if e := byKey["+a>c"]; e.Violation != "core may only import b" || len(e.Files) != 1 || e.Files[0] != (EdgeFile{Path: "a/a.go", Line: 7}) {
		t.Errorf("a>c: %+v", e)
	}
	if e := byKey["-f>b"]; e.Fixed != "leaf must not import b" || e.Violation != "" {
		t.Errorf("f>b: %+v", e)
	}
	if byKey["+e>a"].Violation != "" || a.Violations != 1 {
		t.Errorf("e>a: %+v, violations %d", byKey["+e>a"], a.Violations)
	}
	if !a.Rules.Found || a.Rules.Layers != 2 || a.Rules.Error != "" {
		t.Errorf("rules %+v", a.Rules)
	}
}

func TestAnalyzeSurface(t *testing.T) {
	s := analyze(t, moduleRepo(t)).Surface
	// Exports: package main has none. Config gained an exported field (its
	// new unexported one alone wouldn't count), d.X's value changed and f
	// was deleted.
	if got, want := changeKeys(s.Exports), []string{"-a.Gone", "~a.Config", "+a.New", "~a.Old", "~d.X", "-f.Z"}; !reflect.DeepEqual(sortedStrings(got), sortedStrings(want)) {
		t.Errorf("exports %v", got)
	}
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+GET /api/things/{id}"}) {
		t.Errorf("routes %v", got)
	}
	// SHARED moved into a changed file but was already used in c: not new.
	if got := changeKeys(s.Env); !reflect.DeepEqual(got, []string{"+NEW_TOKEN"}) {
		t.Errorf("env %v", got)
	}
	if got := changeKeys(s.Config); !reflect.DeepEqual(got, []string{"+listen"}) {
		t.Errorf("config %v", got)
	}
	if got := changeKeys(s.Flags); !reflect.DeepEqual(got, []string{"+new-flag", "-old-flag"}) {
		t.Errorf("flags %v", got)
	}
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"+github.com/new/dep", "~github.com/old/dep"}) {
		t.Errorf("deps %v", got)
	}
	for _, d := range s.Deps {
		if d.Op == OpChanged && d.Detail != "v1.0.0 → v1.2.0" || d.Op == OpAdded && d.Detail != "v0.1.0 (indirect)" {
			t.Errorf("dep detail %+v", d)
		}
	}
	if got := changeKeys(s.Migrations); !reflect.DeepEqual(got, []string{"+db/migrate/001_init.sql"}) {
		t.Errorf("migrations %v", got)
	}
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestBrokenRulesAreReported(t *testing.T) {
	dir := moduleRepo(t)
	write(t, dir, ".unky-mo/architecture.toml", "[[layer]]\nname = \"x\"\npaths = [\"a\"]\nallwo = [\"b\"]\n")
	a := analyze(t, dir)
	if !a.Rules.Found || !strings.Contains(a.Rules.Error, "allwo") || a.Violations != 0 {
		t.Errorf("rules %+v, violations %d", a.Rules, a.Violations)
	}
}

// A file that doesn't parse (mid-edit) leaves its package's edges alone
// rather than reporting every import as removed.
func TestUnparsableFileSkipsPackageEdges(t *testing.T) {
	dir := moduleRepo(t)
	write(t, dir, "a/a.go", "package a\n\nimport (\n\t\"example.com/m/b\"\n")
	for _, e := range analyze(t, dir).Edges {
		if e.From == "a" {
			t.Errorf("edge from a package mid-edit: %+v", e)
		}
	}
}

func TestRuleCheck(t *testing.T) {
	rs := &ruleSet{layers: []layer{
		{Name: "web", Paths: []string{"internal/web"}, Deny: []string{"internal/status"}},
		{Name: "gitfiles", Paths: []string{"internal/gitfiles"}, Allow: []string{"internal/exec"}},
		{Name: "internal", Paths: []string{"internal"}, Deny: []string{"cmd"}},
	}}
	cases := []struct{ from, to, want string }{
		{"internal/web", "internal/status", "web must not import internal/status"},
		{"internal/web/sub", "internal/status/x", "web must not import internal/status/x"},
		{"internal/web", "internal/ops", ""},
		{"internal/gitfiles", "internal/exec", ""},
		{"internal/gitfiles", "internal/config", "gitfiles may only import internal/exec"},
		{"internal/gitfiles", "internal/gitfiles/sub", ""},            // own layer
		{"internal/ops", "cmd/mo", "internal must not import cmd/mo"}, // longest prefix: the catch-all layer
		{"internal/ops", "internal/tmux", ""},                         // same (catch-all) layer
		{"cmd/mo", "internal/web", ""},                                // no layer
		{"internal/webby", "internal/status", ""},                     // a prefix is a path, not a string prefix
	}
	for _, c := range cases {
		if got := rs.check(c.from, c.to, "go"); got != c.want {
			t.Errorf("check(%s, %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestExportedDecls(t *testing.T) {
	_, d, ok := exportedDecls(`package p

type T struct{ A int; b string; *Embedded }
type I interface{ M() }
type G[K comparable] map[K]int
type unexported struct{}

func (t *T) Method(a, b int) (int, error) { return 0, nil }
func (u unexported) Method() {}
func (t T) private() {}
func F[X any](x X) {}

const C, d = 1, 2
var V string
`)
	if !ok {
		t.Fatal("didn't parse")
	}
	want := map[string]string{
		"T":        "type struct{A int; *Embedded}",
		"I":        "type interface{ M() }",
		"G":        "type[comparable] map[K]int",
		"T.Method": "func(int, int) (int, error)",
		"F":        "func[any](X)",
		"C":        "const = 1",
		"V":        "var string",
	}
	got := map[string]string{}
	for n, decl := range d {
		got[n] = decl.sig
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	if d["T.Method"].detail != "func T.Method(int, int) (int, error)" {
		t.Errorf("detail %q", d["T.Method"].detail)
	}
}

func TestFindNames(t *testing.T) {
	js := "app.get('/users', h)\nrouter.post(\"/login\", h)\nconst k = process.env.API_KEY + process.env['OTHER']\n"
	if got := keys(findNames(js, routePatterns, "src/app.ts")); !reflect.DeepEqual(got, []string{"GET /users", "POST /login"}) {
		t.Errorf("express routes %v", got)
	}
	if got := keys(findNames(js, envPatterns, "src/app.ts")); !reflect.DeepEqual(got, []string{"API_KEY", "OTHER"}) {
		t.Errorf("js env %v", got)
	}
	rb := "Rails.application.routes.draw do\n  resources :users, only: [:index] # list\n  get 'health', to: 'health#show'\nend\n"
	if got := keys(findNames(rb, routePatterns, "config/routes.rb")); !reflect.DeepEqual(got, []string{"get 'health', to: 'health#show'", "resources :users, only: [:index]"}) {
		t.Errorf("rails routes %v", got)
	}
	if got := keys(findNames("x = ENV.fetch('DB_URL')\ny = ENV[\"REDIS\"]\n", envPatterns, "config/x.rb")); !reflect.DeepEqual(got, []string{"DB_URL", "REDIS"}) {
		t.Errorf("ruby env %v", got)
	}
	if got := keys(findNames("os.environ['A']\nos.getenv(\"B\")\n", envPatterns, "app/x.py")); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("python env %v", got)
	}
	// Examples in Go comments don't count.
	goSrc := "package x\n// Flags().String(\"in-comment\", …) or \"GET /doc\"\n/* \"POST /block\" */\nvar r = \"GET /real\"\n"
	if got := keys(findNames(goSrc, routePatterns, "x.go")); !reflect.DeepEqual(got, []string{"GET /real"}) {
		t.Errorf("go routes %v", got)
	}
	if got := findNames(goSrc, routePatterns, "x.go")["GET /real"].line; got != 4 {
		t.Errorf("line %d, want 4", got)
	}
	if got := findNames(goSrc, flagPatterns, "x.go"); len(got) != 0 {
		t.Errorf("flag in a comment: %v", got)
	}
	// Patterns only apply to their languages.
	if got := findNames(js, envPatterns, "README.md"); len(got) != 0 {
		t.Errorf("md: %v", got)
	}
}

func keys(m map[string]found) []string { return sortedStrings(sortedKeys(m)) }

func TestDepParsers(t *testing.T) {
	pj := packageJSONDeps(`{"dependencies": {"react": "^18.0.0"}, "devDependencies": {"vite": "5.0.0"}}`)
	if pj["react"].version != "^18.0.0" || pj["vite"].version != "5.0.0 (dev)" || pj["react"].line != 1 {
		t.Errorf("package.json %+v", pj)
	}
	gems := gemfileGems("source 'https://rubygems.org'\ngem 'rails', '~> 7.1'\n  gem \"pg\"\n")
	if gems["rails"] != (depEntry{"~> 7.1", 2}) || gems["pg"] != (depEntry{"", 3}) {
		t.Errorf("gems %+v", gems)
	}
	mod := goModRequires("module x\n\nrequire github.com/a/b v1.0.0\nrequire (\n\tgithub.com/c/d v2.0.0 // indirect\n)\n")
	if mod["github.com/a/b"] != (depEntry{"v1.0.0", 3}) || mod["github.com/c/d"] != (depEntry{"v2.0.0 (indirect)", 5}) {
		t.Errorf("go.mod %+v", mod)
	}
}

// For a branch that isn't checked out, the analysis reads the branch's
// head commit: the working tree (here, main with a broken rules file) never
// leaks in.
func TestAnalyzeHeadCommit(t *testing.T) {
	dir := moduleRepo(t)
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "rest of feat")
	run(t, dir, "checkout", "-q", "main")
	write(t, dir, ".unky-mo/architecture.toml", "not = [valid")
	ctx := context.Background()
	head := gitOut(t, dir, "rev-parse", "feat")
	o, err := gitfiles.GetOverviewAt(ctx, moexec.DefaultCommander, dir, "feat", head, "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(ctx, moexec.DefaultCommander, o)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := edgeKeys(a.Edges), []string{"+a>c", "+e>a", "-f>b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v, want %v", got, want)
	}
	if a.Rules.Error != "" || a.Rules.Layers != 2 || a.Violations != 1 {
		t.Errorf("rules %+v, violations %d", a.Rules, a.Violations)
	}
	if got := changeKeys(a.Surface.Env); !reflect.DeepEqual(got, []string{"+NEW_TOKEN"}) {
		t.Errorf("env %v", got)
	}
}

func TestRuleGlobs(t *testing.T) {
	rs := &ruleSet{layers: []layer{
		{Name: "model", Paths: []string{"*/model"}, Deny: []string{"*/ui", "*/data"}},
		{Name: "app model", Paths: []string{"app/model"}, Deny: []string{"app/ui"}},
	}}
	cases := []struct{ from, to, want string }{
		{"core/model", "core/ui", "model must not import core/ui"},
		{"core/model/sub", "feature/data/remote", "model must not import feature/data/remote"},
		{"core/model", "core/util", ""},
		{"app/model", "app/data", ""}, // the literal layer is more specific than "*/model"
		{"app/model", "app/ui", "app model must not import app/ui"},
		{"model", "ui", ""}, // "*/model" needs two segments
	}
	for _, c := range cases {
		if got := rs.check(c.from, c.to, "go"); got != c.want {
			t.Errorf("check(%s, %s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

// Presets apply on their own when their stack is detected, unless the rules
// file says which (or none); the repo's own layers win over a preset's.
func TestPresets(t *testing.T) {
	presets["test-stack"] = preset{
		detect: func(idx *index) bool { return idx.has("go.mod") },
		layers: []layer{{Name: "core", Paths: []string{"a"}, Deny: []string{"c"}}},
	}
	defer delete(presets, "test-stack")
	dir := moduleRepo(t)

	// No rules file: detected.
	write(t, dir, ".unky-mo/architecture.toml", "")
	a := analyze(t, dir)
	if !a.Rules.AutoPresets || strings.Join(a.Rules.Presets, ",") != "test-stack" || a.Violations != 1 {
		t.Errorf("auto: %+v, violations %d", a.Rules, a.Violations)
	}
	for _, e := range a.Edges {
		if e.From == "a" && e.To == "c" && e.Violation != "test-stack: core must not import c" {
			t.Errorf("a>c: %+v", e)
		}
	}

	// presets = [] turns them off.
	write(t, dir, ".unky-mo/architecture.toml", "presets = []\n")
	if a := analyze(t, dir); len(a.Rules.Presets) != 0 || a.Violations != 0 {
		t.Errorf("off: %+v, violations %d", a.Rules, a.Violations)
	}

	// The repo's own layer for the same path wins over the preset's.
	write(t, dir, ".unky-mo/architecture.toml", "presets = [\"test-stack\"]\n[[layer]]\nname = \"mine\"\npaths = [\"a\"]\nallow = [\"b\", \"c\"]\n")
	if a := analyze(t, dir); a.Rules.AutoPresets || a.Violations != 0 || a.Rules.Layers != 1 {
		t.Errorf("own layer: %+v, violations %d", a.Rules, a.Violations)
	}

	// An unknown preset is reported.
	write(t, dir, ".unky-mo/architecture.toml", "presets = [\"nope\"]\n")
	if a := analyze(t, dir); !strings.Contains(a.Rules.Error, `unknown preset "nope"`) {
		t.Errorf("unknown: %+v", a.Rules)
	}
}

// An unchanged file's references are cached by blob id: a second analysis
// doesn't parse it again.
func TestSymbolCache(t *testing.T) {
	dir := moduleRepo(t)
	analyze(t, dir)
	n := 0
	idx := newIndex(&repo{ctx: context.Background(), cmd: moexec.DefaultCommander, root: dir})
	v := idx.symbols("refs:go\x00example.com/m", "d/d2.go", func(string) any { n++; return nil })
	if n != 0 || v == nil {
		t.Errorf("d/d2.go's refs weren't cached (parsed %d times, got %v)", n, v)
	}
}

func TestDraftRules(t *testing.T) {
	presets["test-stack"] = preset{detect: func(idx *index) bool { return idx.has("go.mod") }}
	defer delete(presets, "test-stack")
	dir := moduleRepo(t)
	text, err := DraftRules(context.Background(), moexec.DefaultCommander, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# presets = [\"test-stack\"]", "detected for this repo: test-stack", "#   a -> b, c", "#   e -> a"} {
		if !strings.Contains(text, want) {
			t.Errorf("draft lacks %q:\n%s", want, text)
		}
	}
	// a_test.go's import of d is a test import: not listed.
	if strings.Contains(text, "a -> b, c, d") {
		t.Errorf("test import listed:\n%s", text)
	}
	// The draft parses as a rules file.
	var doc struct {
		Presets *[]string `toml:"presets"`
		Layer   []layer   `toml:"layer"`
	}
	if _, msg := decodeRules(text, &doc); msg != "" {
		t.Errorf("draft doesn't parse: %s", msg)
	}
	if text, err := DraftRules(context.Background(), moexec.DefaultCommander, dir, []string{}); err != nil || !strings.Contains(text, "\npresets = []\n") {
		t.Errorf("chosen none: %v\n%s", err, text)
	}
	if _, err := DraftRules(context.Background(), moexec.DefaultCommander, dir, []string{"nope"}); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestDoubleStarGlob(t *testing.T) {
	for _, c := range []struct {
		p, pat string
		want   bool
	}{
		{"app/src/main/kotlin/org/moma/data", "**/data", true},
		{"app/src/main/kotlin/org/moma/data/remote", "**/data", true},
		{"app/src/main/kotlin/org/moma/database", "**/data", false},
		{"data", "**/data", true},
		{"core/data/src/main/kotlin/x/ui", "core/**/ui", true},
		{"feature/ui", "core/**/ui", false},
	} {
		if got := under(c.p, c.pat); got != c.want {
			t.Errorf("under(%s, %s) = %v", c.p, c.pat, got)
		}
	}
}

// An analysis that runs out of time says so instead of returning what it
// managed to read.
func TestAnalyzeTimeout(t *testing.T) {
	dir := moduleRepo(t)
	o, err := gitfiles.GetOverview(context.Background(), moexec.DefaultCommander, dir, gitfiles.ModeBranch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a, err := Analyze(ctx, moexec.DefaultCommander, o); err == nil {
		t.Errorf("got %+v, want a timeout error", a)
	}
}

// countingCommander counts the git processes an index spawns.
type countingCommander struct {
	moexec.Commander
	n int
}

func (c *countingCommander) Output(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, error) {
	c.n++
	return c.Commander.Output(ctx, dir, name, args...)
}

func (c *countingCommander) OutputStdin(ctx context.Context, dir string, in []byte, name string, args ...string) ([]byte, []byte, error) {
	c.n++
	return c.Commander.OutputStdin(ctx, dir, in, name, args...)
}

// A commit's index reads its files with one batched git process, and then
// serves them without any more.
func TestIndexAtPrefetch(t *testing.T) {
	dir := moduleRepo(t)
	base := gitOut(t, dir, "rev-parse", "main")
	cmd := &countingCommander{Commander: moexec.DefaultCommander}
	r := &repo{ctx: context.Background(), cmd: cmd, root: dir, rev: base}
	idx := newIndexAt(r, base)
	if !idx.has("f/f.go") || idx.has("e/e.go") {
		t.Fatalf("base index paths = %v", idx.paths)
	}
	cmd.n = 0
	idx.prefetch(idx.paths)
	if cmd.n != 1 {
		t.Errorf("prefetch ran %d git processes, want 1", cmd.n)
	}
	got := idx.read("a/a.go")
	if got == nil || !strings.Contains(*got, "func Gone()") || cmd.n != 1 {
		t.Errorf("read after prefetch: %v (%d processes)", got != nil, cmd.n)
	}
	// Without a prefetch, a base read still works, one file at a time.
	idx2 := newIndexAt(r, base)
	if got := idx2.read("b/b.go"); got == nil || !strings.Contains(*got, "const N = 1") {
		t.Errorf("unprefetched base read = %v", got)
	}
}

// A file modified since it was staged isn't cached under its staged blob
// id: that would serve the old content's scan.
func TestIndexDropsModifiedBlobs(t *testing.T) {
	dir := moduleRepo(t)
	idx := newIndex(&repo{ctx: context.Background(), cmd: moexec.DefaultCommander, root: dir})
	if idx.blobs["a/a.go"] == "" || idx.blobs["d/d2.go"] == "" {
		t.Fatalf("committed files lack blob ids: %v", idx.blobs)
	}
	write(t, dir, "d/d2.go", "package d\n\nvar Y = 2\n")
	idx = newIndex(&repo{ctx: context.Background(), cmd: moexec.DefaultCommander, root: dir})
	if idx.blobs["d/d2.go"] != "" {
		t.Error("a modified file kept its staged blob id")
	}
	if !idx.has("d/d2.go") {
		t.Error("a modified file left the index")
	}
}
