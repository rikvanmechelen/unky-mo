package review

import (
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

func TestPyScanDefs(t *testing.T) {
	src := `import os
from .models import (User,
    Base as B)


@decorate
def top(a, b=1):
    """fake() in a docstring isn't a call."""
    x = helper(a)  # nope() in a comment isn't either
    def inner():
        return other()
    return inner()


class Outer(B, mixins.Thing):
    def method(self):
        return self.go(top, key=self.other)

    @staticmethod
    def build():
        return super().build()

main()
`
	f := (&pyCalls{}).scanFile("x.py", src)
	if !f.OK {
		t.Fatal("not OK")
	}
	got := map[string]string{}
	for _, d := range f.Defs {
		var calls []string
		for _, c := range d.Calls {
			k := c.Name
			if c.Recv != "" {
				k = c.Recv + "." + k
			}
			if c.Ref {
				k += "@ref"
			}
			calls = append(calls, k)
		}
		got[qualify(d.Owner, d.Name)] = strings.Join(calls, " ") + rangeOf(d)
	}
	want := map[string]string{
		"top":          "decorate@ref helper a@ref inner [7-12]", // a@ref resolves to nothing: dropped later
		"top.inner":    "other [10-11]",
		"Outer":        " [15-21]",
		"Outer.method": "self.go top@ref self.other@ref [16-17]",
		"Outer.build":  "staticmethod@ref super.build [20-21]",
		"<module>":     "main [23-23]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defs:\n%v\nwant\n%v", got, want)
	}
	if len(f.Classes) != 1 || !reflect.DeepEqual(f.Classes[0].Bases, []string{"B", "mixins.Thing"}) {
		t.Errorf("classes = %+v", f.Classes)
	}
	var imps []string
	for _, i := range f.Imports {
		imps = append(imps, i.Local+"="+i.Spec+":"+i.Name)
	}
	if want := []string{"os=os:", "User=.models:User", "B=.models:Base"}; !reflect.DeepEqual(imps, want) {
		t.Errorf("imports = %v, want %v", imps, want)
	}
	for _, d := range f.Defs {
		if d.Name == "build" && !d.Static {
			t.Error("staticmethod not marked")
		}
	}
}

func rangeOf(d hDef) string {
	return " [" + strconv.Itoa(d.Line) + "-" + strconv.Itoa(d.End) + "]"
}

// pyCallRepo: a package with a re-export, a class hierarchy, aliases and a
// test, changed on a branch.
func pyCallRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	write(t, dir, "app/__init__.py", "from .models import User\n")
	write(t, dir, "app/models.py", `class Base:
    def save(self):
        return self.validate()

    def validate(self):
        return True


class User(Base):
    def __init__(self, name):
        self.name = name

    def full_name(self):
        return format_name(self.name)

    def gone(self):
        pass


def format_name(n):
    """fake() in a docstring doesn't count."""
    return n.title()  # nope() in a comment either
`)
	write(t, dir, "app/util.py", "def helper():\n    return 1\n\n\ndef fake():\n    pass\n\n\ndef nope():\n    pass\n")
	write(t, dir, "app/service.py", `from app import User
from app.models import format_name as fmt
import app.util as util


def register(name):
    u = User(name)
    u.save()
    return fmt(name)


def legacy(u):
    return u.gone()
`)
	write(t, dir, "tests/test_service.py", "from app.service import register\n\n\ndef test_register():\n    register(\"x\")\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "init")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "app/models.py", `class Base:
    def save(self):
        return self.validate()

    def validate(self):
        return True


class User(Base):
    def __init__(self, name):
        self.name = name

    def full_name(self):
        self.save()
        return format_name(self.name)

    def shout(self):
        return self.full_name().upper()


def format_name(n):
    """fake() in a docstring doesn't count."""
    return n.title()  # nope() in a comment either
`)
	write(t, dir, "app/service.py", `from app import User
from app.models import format_name as fmt
import app.util as util


def register(name, email):
    u = User(name)
    u.save()
    util.helper()
    list(map(fmt, [name]))
    return fmt(name)


def legacy(u):
    return u.gone()
`)
	return dir
}

// gitRepo is an empty repo on main, skipping the test without git.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func TestPyCalls(t *testing.T) {
	cg := callGraphOf(t, pyCallRepo(t), gitfiles.ModeBranch)
	if len(cg.Errors) > 0 || len(cg.Unparsed) > 0 {
		t.Fatalf("errors %v, unparsed %v", cg.Errors, cg.Unparsed)
	}
	want := map[string]string{
		"app/models.py:User.full_name": FuncChanged,
		"app/models.py:User.gone":      FuncRemoved,
		"app/models.py:User.shout":     FuncAdded,
		"app/service.py:register":      FuncSignature,
	}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v\nwant %v", got, want)
	}
	calls := callKeys(cg)
	for _, w := range []string{
		"+app/models.py:User.full_name>app/models.py:Base.save@approx", // self, through the base class
		"+app/models.py:User.shout>app/models.py:User.full_name@approx",
		"+app/service.py:register>app/util.py:helper@approx",         // a module alias
		"+app/service.py:register>app/models.py:format_name@ref",     // an aliased import passed as a value
		"app/service.py:register>app/models.py:User.__init__@approx", // a constructor through a package re-export
		"app/service.py:register>app/models.py:Base.save@approx",     // the only save() in the repo
		"app/service.py:register>app/models.py:format_name@approx",
		"tests/test_service.py:test_register>app/service.py:register@approx",
	} {
		if !contains(calls, w) {
			t.Errorf("missing call %s", w)
		}
	}
	for _, c := range calls {
		if strings.Contains(c, ":fake") || strings.Contains(c, ":nope") {
			t.Errorf("call from a docstring or comment: %s", c)
		}
	}
	findings := findingKeys(cg)
	for _, w := range []string{
		"removed-called:app/models.py:User.gone app/service.py", // u.gone() on an unknown receiver
		"signature-callers:app/service.py:register tests/test_service.py",
		"untested:app/models.py:User.shout",
	} {
		if !contains(findings, w) {
			t.Errorf("missing finding %s in %v", w, findings)
		}
	}
	if cg.Languages[0].Exact {
		t.Error("python call graph marked exact")
	}
}

// An unknown receiver's method resolves to the repo's only method of that
// name, unless the name is a standard collection/string method.
func TestUniqueMethodSkipsCommonNames(t *testing.T) {
	r := &hResolver{l: &pyCalls{}, byName: map[string][]hDefRef{
		"append":  {{"a.py", &hDef{Name: "append", Owner: "Log", Class: "Log"}}},
		"archive": {{"a.py", &hDef{Name: "archive", Owner: "Log", Class: "Log"}}},
	}}
	if id, ok := r.uniqueMethod("archive"); !ok || id != "a.py:Log.archive" {
		t.Errorf("archive = %q, %v", id, ok)
	}
	if id, ok := r.uniqueMethod("append"); ok {
		t.Errorf("append resolved to %q", id)
	}
}
