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
	wantPkgs := []Package{{"a", "changed"}, {"cmd/mo", "changed"}, {"d", "changed"}, {"e", "added"}, {"f", "removed"}}
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
		if got := rs.check(c.from, c.to); got != c.want {
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
