package review

import (
	"os/exec"
	"reflect"
	"testing"
)

func TestPyImports(t *testing.T) {
	src := `"""Module doc: import ghost"""
import os, json as j
from typing import TYPE_CHECKING
from .sibling import thing
from ..pkg.sub import (
    a,
    b as bee,
)
import moma.pipelines as pl
from moma \
    import ai
if TYPE_CHECKING:
    from moma.types import Hint
    import moma.hidden
x = "from fake import nothing"
def f():
    from moma.lazy import later  # inside a function still counts
`
	var got []string
	for _, imp := range pyImports(src) {
		s := imp.Module
		for i := 0; i < imp.Level; i++ {
			s = "." + s
		}
		if len(imp.Names) > 0 {
			s += ":" + joinNames(imp.Names)
		}
		got = append(got, s)
	}
	want := []string{"os", "json", "typing:TYPE_CHECKING", ".sibling:thing", "..pkg.sub:a,b", "moma.pipelines", "moma:ai", "moma.lazy:later"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	for _, imp := range pyImports(src) {
		if imp.Module == "..pkg.sub" || imp.Module == "pkg.sub" {
			if imp.Line != 5 {
				t.Errorf("multi-line import on line %d, want 5", imp.Line)
			}
		}
	}
}

func joinNames(ns []string) string {
	out := ""
	for i, n := range ns {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// pyRepo is a src-layout package (package-dir in pyproject) with a
// namespace package; branch "feat" adds imports of each kind.
func pyRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "pyproject.toml", "[project]\nname = \"moma\"\ndependencies = [\"requests>=2\"]\n\n[tool.setuptools]\npackage-dir = {\"\" = \"src\"}\n")
	write(t, dir, "src/moma/__init__.py", "")
	write(t, dir, "src/moma/ai/__init__.py", "")
	write(t, dir, "src/moma/ai/client.py", "import requests\n")
	write(t, dir, "src/moma/pipelines/__init__.py", "")
	write(t, dir, "src/moma/pipelines/sync.py", "import logging\n")
	write(t, dir, "src/moma/util/strings.py", "def slug(s): return s\n") // namespace package: no __init__.py
	write(t, dir, "src/moma/cli/main.py", "import typer\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")

	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "src/moma/pipelines/sync.py", "import logging\nfrom moma.ai import client\nfrom ..util.strings import slug\n")
	write(t, dir, "src/moma/cli/main.py", "import typer\nimport moma.pipelines\n\n@app.command()\ndef run(name: str = typer.Option('x', \"--name\")):\n    pass\n")
	write(t, dir, "pyproject.toml", "[project]\nname = \"moma\"\ndependencies = [\"requests>=2\", \"typer[all]>=0.9\"]\n\n[tool.setuptools]\npackage-dir = {\"\" = \"src\"}\n")
	write(t, dir, "src/moma/api/app.py", "from fastapi import FastAPI\napp = FastAPI()\n\n@app.get(\"/health\")\ndef health(): return 1\n")
	return dir
}

func TestPythonArchitecture(t *testing.T) {
	a := analyze(t, pyRepo(t))
	want := []string{"+src/moma/cli>src/moma/pipelines", "+src/moma/pipelines>src/moma/ai", "+src/moma/pipelines>src/moma/util"}
	if got := edgeKeys(a.Edges); !reflect.DeepEqual(got, want) {
		t.Errorf("edges %v\nwant  %v", got, want)
	}
	for _, e := range a.Edges {
		if e.Lang != "python" || e.Approx || e.Violation != "" {
			t.Errorf("edge %+v", e)
		}
	}
	if len(a.Rules.Presets) != 0 {
		t.Errorf("presets for python: %v", a.Rules.Presets)
	}
	s := a.Surface
	if got := changeKeys(s.Routes); !reflect.DeepEqual(got, []string{"+GET /health"}) {
		t.Errorf("routes %v", got)
	}
	if got := changeKeys(s.Flags); !reflect.DeepEqual(got, []string{"+--name"}) {
		t.Errorf("flags %v", got)
	}
	if got := changeKeys(s.Deps); !reflect.DeepEqual(got, []string{"+typer"}) || s.Deps[0].Detail != ">=0.9" {
		t.Errorf("deps %+v", s.Deps)
	}
}

func TestPyDeps(t *testing.T) {
	r := requirementsDeps("# pinned\nDjango==4.2\nrequests[security]>=2.31 ; python_version>'3'\n-r base.txt\n")
	if r["django"].version != "==4.2" || r["requests"].version != ">=2.31" || len(r) != 2 {
		t.Errorf("requirements %+v", r)
	}
	p := pyprojectDeps("[tool.poetry.dependencies]\npython = \"^3.12\"\nhttpx = \"^0.27\"\n")
	if p["httpx"].version != "^0.27" || len(p) != 1 {
		t.Errorf("poetry %+v", p)
	}
}
