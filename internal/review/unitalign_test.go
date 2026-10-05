package review

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// The Overview map puts /calls functions in /architecture's package boxes
// by unit name, so the two must agree: a changed function's unit is one of
// the packages the analysis lists for its language, and every function
// (context ones too, interface implementations included) has a path and,
// outside the repo root, a unit.
func TestCallUnitsMatchArchitecture(t *testing.T) {
	for _, c := range []struct {
		name string
		repo func(*testing.T) string
	}{
		{"go", callRepo}, {"python", pyCallRepo}, {"node", jsCallRepo}, {"ruby", rbCallRepo},
		{"kotlin", jvmCallRepo}, {"swift", swiftCallRepo}, {"rails", railsChange},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			o, err := gitfiles.GetOverview(ctx, moexec.DefaultCommander, c.repo(t), gitfiles.ModeBranch)
			if err != nil {
				t.Fatal(err)
			}
			a, err := Analyze(ctx, moexec.DefaultCommander, o)
			if err != nil {
				t.Fatal(err)
			}
			cg, err := Calls(ctx, moexec.DefaultCommander, o)
			if err != nil {
				t.Fatal(err)
			}
			pkgs := map[string]bool{}
			for _, p := range a.Packages {
				pkgs[p.Lang+"|"+p.Path] = true
			}
			changed := 0
			for _, f := range cg.Funcs {
				if f.Path == "" || (f.Unit == "" && path.Dir(f.Path) != ".") {
					t.Errorf("%s: path %q, unit %q", f.ID, f.Path, f.Unit)
				}
				if f.Status == "" || f.Lang != c.name && c.name != "rails" {
					continue
				}
				changed++
				if !pkgs[f.Lang+"|"+f.Unit] {
					t.Errorf("changed %s is in unit %q, which /architecture doesn't list (%v)", f.ID, f.Unit, a.Packages)
				}
			}
			if changed == 0 {
				t.Error("no changed functions")
			}
		})
	}
}

// railsChange is railsStimulusRepo with a Ruby and a JavaScript method
// renamed in the working tree.
func railsChange(t *testing.T) string {
	dir := railsStimulusRepo(t)
	write(t, dir, "app/javascript/controllers/search_controller.js", strings.Replace(searchControllerJS, "filter()", "search()", 1))
	write(t, dir, "app/controllers/tickets_controller.rb", strings.Replace(readFile(t, dir, "app/controllers/tickets_controller.rb"), "def index", "def listing", 1))
	return dir
}

// /calls names the layers of its functions' units, context units included
// (the interface implementations in store are in no /architecture list).
func TestCallUnitLayers(t *testing.T) {
	dir := callRepo(t)
	write(t, dir, ".unky-mo/architecture.toml", "[[layer]]\nname = \"storage\"\npaths = [\"store\"]\n\n[[layer]]\nname = \"service\"\npaths = [\"svc\"]\n")
	cg := callGraphOf(t, dir, gitfiles.ModeBranch)
	if cg.UnitLayers["store"] != "storage" || cg.UnitLayers["svc"] != "service" {
		t.Errorf("unit layers %v", cg.UnitLayers)
	}
	if _, ok := cg.UnitLayers["web"]; ok {
		t.Errorf("web is in no layer: %v", cg.UnitLayers)
	}
	impl := false
	for _, f := range cg.Funcs {
		if f.ID == "(*store.Mem).Get" {
			impl = f.Unit == "store" && f.Path == "store/store.go" && f.Line > 0
		}
	}
	if !impl {
		t.Errorf("(*store.Mem).Get has no position or unit: %+v", cg.Funcs)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
