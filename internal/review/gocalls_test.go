package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// callRepo builds a Go module on main and changes it on "feat"
// (uncommitted):
//
//	svc/svc.go  Helper gains a parameter, Lookup's body changes (a new
//	            callee, and a closure calling Helper), Gone is removed,
//	            Apply and double are added (double passed as a value)
//	svc/gen.go  //go:build ignore, imports web: a cycle if tags were ignored
//	store       implements svc.Store twice (unchanged)
//	web         unchanged importer of svc: calls Helper and the removed Gone
//	svc_test.go TestLookup reaches Lookup and what it calls
func callRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.22\n")
	write(t, dir, "svc/svc.go", `package svc

import "strings"

// Store reads values.
type Store interface{ Get(k string) string }

type Service struct{ s Store }

func New(s Store) *Service { return &Service{s: s} }

func (v *Service) Lookup(k string) string { return strings.ToUpper(v.s.Get(k)) }

func Helper() int { return 1 }

func Gone() {}

func Map[T any](xs []T, f func(T) T) []T {
	for i := range xs {
		xs[i] = f(xs[i])
	}
	return xs
}
`)
	write(t, dir, "svc/gen.go", "//go:build ignore\n\npackage svc\n\nimport _ \"example.com/m/web\"\n")
	write(t, dir, "svc/svc_test.go", `package svc

import "testing"

func TestLookup(t *testing.T) { New(nil).Lookup("k") }
`)
	write(t, dir, "store/store.go", `package store

type Mem struct{ m map[string]string }

func (m *Mem) Get(k string) string { return m.m[k] }

type Disk struct{}

func (Disk) Get(k string) string { return "" }
`)
	write(t, dir, "web/web.go", `package web

import (
	"net/http"

	"example.com/m/svc"
)

type S struct{}

func (s *S) handle(w http.ResponseWriter, r *http.Request) { svc.Helper() }

func Routes(mux *http.ServeMux, s *S) { mux.HandleFunc("/", s.handle) }

func Use() { svc.Gone(); _ = svc.Helper() }
`)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "svc/svc.go", `package svc

import "strings"

// Store reads values. (A comment edit doesn't change anything.)
type Store interface{ Get(k string) string }

type Service struct{ s Store }

func New(s Store) *Service { return &Service{s: s} }

func (v *Service) Lookup(k string) string {
	func() { Helper(1) }()
	return strings.ToUpper(v.s.Get(normalize(k)))
}

func normalize(k string) string { return strings.TrimSpace(k) }

func Helper(n int) int { return n }

func Map[T any](xs []T, f func(T) T) []T {
	for i := range xs {
		xs[i] = f(xs[i])
	}
	return xs
}

func Apply() []int { return Map([]int{1}, double) }

func double(x int) int { return 2 * x }
`)
	return dir
}

func callGraphOf(t *testing.T, dir, mode string) *CallGraph {
	t.Helper()
	ctx := context.Background()
	o, err := gitfiles.GetOverview(ctx, moexec.DefaultCommander, dir, mode)
	if err != nil {
		t.Fatal(err)
	}
	cg, err := Calls(ctx, moexec.DefaultCommander, o)
	if err != nil {
		t.Fatal(err)
	}
	return cg
}

func TestGoCalls(t *testing.T) {
	cg := callGraphOf(t, callRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || cg.Truncated || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, truncated %v, unparsed %v", cg.Errors, cg.Truncated, cg.Unparsed)
	}
	want := map[string]string{
		"svc.Helper":            FuncSignature,
		"(*svc.Service).Lookup": FuncChanged,
		"svc.Gone":              FuncRemoved,
		"svc.normalize":         FuncAdded,
		"svc.Apply":             FuncAdded,
		"svc.double":            FuncAdded,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+(*svc.Service).Lookup>svc.normalize",
		"+(*svc.Service).Lookup>svc.Helper", // from the closure
		"+svc.Apply>svc.Map",                // a generic instantiation
		"+svc.Apply>svc.double@ref",         // passed as a value
		"(*svc.Service).Lookup>(svc.Store).Get@dynamic",
		"(svc.Store).Get>(*store.Mem).Get@impl",
		"(svc.Store).Get>(store.Disk).Get@impl",
		"web.Use>svc.Helper", // a caller in an unchanged importer
		"(*web.S).handle>svc.Helper",
		"svc.TestLookup>(*svc.Service).Lookup",
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s", w)
		}
	}

	for _, c := range calls {
		if strings.Contains(c, "strings.") || strings.Contains(c, "http.") {
			t.Errorf("call outside the module: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:svc.Gone web/web.go",
		"signature-callers:svc.Helper web/web.go web/web.go", // handle and Use
		"untested:svc.Apply",
		"untested:svc.double",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	for _, f := range findings {
		if f == "untested:svc.normalize" || f == "untested:(*svc.Service).Lookup" {
			t.Errorf("%s: TestLookup reaches it", f)
		}
	}
	for _, f := range cg.Funcs {
		if f.ID == "svc.Gone" && (!f.Before || f.Path != "svc/svc.go" || f.Line != 16) {
			t.Errorf("removed func = %+v", f)
		}
		if f.ID == "svc.TestLookup" && !f.Test {
			t.Errorf("test func not marked: %+v", f)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A handler passed next to a route is a reference labelled with it.
func TestGoCallsRefLabel(t *testing.T) {
	dir := callRepo(t)
	idx := newIndex(&repo{ctx: context.Background(), cmd: moexec.DefaultCommander, root: dir})
	set, err := (&goCalls{module: "example.com/m"}).funcs(idx, []string{"web/web.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	routes := set.funcs["web.Routes"]
	if routes == nil {
		t.Fatal("web.Routes not found")
	}
	var got []string
	for _, c := range routes.calls {
		got = append(got, c.to+"@"+c.kind+":"+c.label)
	}
	if !contains(got, "(*web.S).handle@ref:/") {
		t.Errorf("Routes' calls = %v", got)
	}
}

// A head commit's call graph comes from git, never the working tree.
func TestGoCallsAtHeadCommit(t *testing.T) {
	dir := callRepo(t)
	run(t, dir, "commit", "-q", "-am", "feat")
	run(t, dir, "checkout", "-q", "main")
	// The checkout now has main's files; feat's must come from the commit.
	if err := os.WriteFile(filepath.Join(dir, "svc/svc.go"), []byte("package svc\n\nfunc Other() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	o, err := gitfiles.GetOverviewAt(ctx, moexec.DefaultCommander, dir, "feat", gitOut(t, dir, "rev-parse", "feat"), "")
	if err != nil {
		t.Fatal(err)
	}
	cg, err := Calls(ctx, moexec.DefaultCommander, o)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(cg); got["svc.Apply"] != FuncAdded || got["svc.Other"] != "" || got["svc.Gone"] != FuncRemoved {
		t.Errorf("statuses = %v", got)
	}
}

// An import cycle (invalid Go, but possible mid-edit) doesn't recurse.
func TestGoCallsImportCycle(t *testing.T) {
	dir := callRepo(t)
	write(t, dir, "store/cycle.go", "package store\n\nimport \"example.com/m/web\"\n\nfunc Cycle() { web.Use() }\n")
	done := make(chan *CallGraph)
	go func() { done <- callGraphOf(t, dir, gitfiles.ModeBranch) }()
	select {
	case cg := <-done:
		if statuses(cg)["store.Cycle"] != FuncAdded {
			t.Errorf("statuses = %v", statuses(cg))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("call graph of an import cycle didn't finish")
	}
}

// A changed file that doesn't parse leaves its functions out.
func TestGoCallsUnparsable(t *testing.T) {
	dir := callRepo(t)
	write(t, dir, "svc/svc.go", "package svc\n\nfunc Helper( {\n")
	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if !reflect.DeepEqual(cg.Unparsed, []string{"svc/svc.go"}) {
		t.Errorf("unparsed = %v", cg.Unparsed)
	}
	if got := statuses(cg); len(got) != 0 {
		t.Errorf("statuses = %v, want none", got)
	}
}

func TestGuessPkgName(t *testing.T) {
	for in, want := range map[string]string{
		"charm.land/bubbletea/v2":     "bubbletea",
		"github.com/spf13/cobra":      "cobra",
		"gopkg.in/yaml.v3":            "yaml_v3",
		"github.com/mattn/go-sqlite3": "sqlite3",
		"net/http":                    "http",
	} {
		if got := guessPkgName(in); got != want {
			t.Errorf("guessPkgName(%q) = %q, want %q", in, got, want)
		}
	}
}

func BenchmarkGoCallsSelf(b *testing.B) {
	if testing.Short() {
		b.Skip()
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	o, err := gitfiles.GetOverview(ctx, moexec.DefaultCommander, root, gitfiles.ModeHead)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := Calls(ctx, moexec.DefaultCommander, o); err != nil {
			b.Fatal(err)
		}
	}
}
