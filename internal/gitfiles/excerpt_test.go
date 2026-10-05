package gitfiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

func TestParseFullDiff(t *testing.T) {
	out := []byte(`diff --git a/x.go b/y.go
similarity index 80%
rename from x.go
rename to y.go
--- a/x.go
+++ b/y.go
@@ -1,4 +1,4 @@
 one
-two
+TWO
+
 three
-four
\ No newline at end of file
`)
	rows, binary := parseFullDiff(out)
	want := []Row{
		{Ln: 1, OldLn: 1, Sign: " ", Text: "one"},
		{OldLn: 2, Sign: "-", Text: "two"},
		{Ln: 2, Sign: "+", Text: "TWO"},
		{Ln: 3, Sign: "+", Text: ""},
		{Ln: 4, OldLn: 3, Sign: " ", Text: "three"},
		{OldLn: 4, Sign: "-", Text: "four"},
	}
	if binary || !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows %+v binary %v", rows, binary)
	}

	if rows, _ := parseFullDiff([]byte("diff --git a/n b/n\nnew file mode 100644\n--- /dev/null\n+++ b/n\n@@ -0,0 +1,2 @@\n+a\n+b\n")); !reflect.DeepEqual(rows, []Row{{Ln: 1, Sign: "+", Text: "a"}, {Ln: 2, Sign: "+", Text: "b"}}) {
		t.Errorf("new file: %+v", rows)
	}
	if rows, binary := parseFullDiff([]byte("diff --git a/b.png b/b.png\nindex 1..2 100644\nBinary files a/b.png and b/b.png differ\n")); !binary || rows != nil {
		t.Errorf("binary: %+v %v", rows, binary)
	}
	if rows, binary := parseFullDiff([]byte("diff --git a/x b/y\nsimilarity index 100%\nrename from x\nrename to y\n")); binary || rows != nil {
		t.Errorf("pure rename: %+v %v", rows, binary)
	}
	if rows, _ := parseFullDiff(nil); rows != nil {
		t.Errorf("empty: %+v", rows)
	}
}

func cutFixture() *Annotated {
	return &Annotated{Path: "a.go", Rows: []Row{
		{Ln: 1, OldLn: 1, Sign: " ", Text: "one"},
		{OldLn: 2, Sign: "-", Text: "two"},
		{Ln: 2, Sign: "+", Text: "TWO"},
		{Ln: 3, Sign: "+", Text: "2.5"},
		{Ln: 4, OldLn: 3, Sign: " ", Text: "three"},
	}}
}

func texts(e *Excerpt) string {
	var s []string
	for _, r := range e.Rows {
		s = append(s, r.Text)
	}
	return strings.Join(s, ",")
}

func TestCut(t *testing.T) {
	a := cutFixture()
	cases := []struct {
		line, end int
		side      string
		ctx       int
		want      string
	}{
		{2, 0, SideNew, 0, "TWO"},
		{2, 0, SideNew, 1, "two,TWO,2.5"},
		{2, 0, SideOld, 1, "one,two,TWO"},
		{3, 0, SideOld, 0, "three"},
		{2, 3, SideNew, 0, "TWO,2.5"},
		{1, 4, SideNew, 9, "one,two,TWO,2.5,three"},
		{9, 0, SideNew, 3, ""},
		{4, 2, SideNew, 0, "three"}, // end before line means just line
	}
	for _, c := range cases {
		e := a.Cut(c.line, c.end, c.side, c.ctx)
		if got := texts(e); got != c.want || e.Side != c.side || e.Path != "a.go" {
			t.Errorf("Cut(%d, %d, %s, %d) = %q (side %s), want %q", c.line, c.end, c.side, c.ctx, got, e.Side, c.want)
		}
	}

	big := &Annotated{Path: "big.go"}
	for i := 1; i <= 1000; i++ {
		big.Rows = append(big.Rows, Row{Ln: i, OldLn: i, Sign: " "})
	}
	e := big.Cut(10, 900, SideNew, 3)
	if !e.Truncated || len(e.Rows) != MaxExcerptRows || e.Rows[0].Ln != 7 {
		t.Errorf("truncated: %v, %d rows from %d", e.Truncated, len(e.Rows), e.Rows[0].Ln)
	}
	if e := (&Annotated{Path: "b", Binary: true}).Cut(1, 0, SideNew, 3); !e.Binary || e.Rows == nil || len(e.Rows) != 0 {
		t.Errorf("binary cut: %+v", e)
	}
}

// excerptRepo is newRepo plus, on branch feat: a.go edited, old.go renamed
// to new.go and edited, gone.go deleted, bin.dat changed; then an
// untracked fresh.go and a symlink link.go in the working tree.
func excerptRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	write(t, dir, "keep.go", "k1\nk2\n")
	write(t, dir, "gone.go", "g1\ng2\n")
	write(t, dir, "old.go", "o1\no2\no3\no4\no5\n")
	write(t, dir, "bin.dat", "a\x00b")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-q", "-m", "base")
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "a.go", "one\nTWO\ntwo and a half\n")
	run(t, dir, "mv", "old.go", "new.go")
	write(t, dir, "new.go", "o1\no2\nO3\no4\no5\n")
	run(t, dir, "rm", "-q", "gone.go")
	write(t, dir, "bin.dat", "a\x00c")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "feat")
	write(t, dir, "fresh.go", "f1\nf2\n")
	if err := os.Symlink("keep.go", filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAnnotateWorkingTree(t *testing.T) {
	dir := excerptRepo(t)
	ctx, cmd := context.Background(), moexec.DefaultCommander
	o, err := GetOverview(ctx, cmd, dir, ModeBranch)
	if err != nil || o.Mode != ModeBranch {
		t.Fatalf("overview: %+v %v", o, err)
	}
	ann := func(path string) *Annotated {
		t.Helper()
		a, err := Annotate(ctx, cmd, o, path)
		if err != nil {
			t.Fatalf("Annotate(%s): %v", path, err)
		}
		return a
	}

	want := []Row{
		{Ln: 1, OldLn: 1, Sign: " ", Text: "one"},
		{OldLn: 2, Sign: "-", Text: "two"},
		{Ln: 2, Sign: "+", Text: "TWO"},
		{Ln: 3, Sign: "+", Text: "two and a half"},
	}
	if a := ann("a.go"); !reflect.DeepEqual(a.Rows, want) {
		t.Errorf("a.go: %+v", a.Rows)
	}

	// A rename, asked for by either name, has both versions' lines.
	for _, p := range []string{"new.go", "old.go"} {
		a := ann(p)
		e := a.Cut(3, 0, SideNew, 0)
		o3 := a.Cut(3, 0, SideOld, 0)
		if a.Path != p || texts(e) != "O3" || texts(o3) != "o3" || len(a.Rows) != 6 {
			t.Errorf("%s: %+v", p, a.Rows)
		}
	}

	if a := ann("gone.go"); !reflect.DeepEqual(a.Rows, []Row{{OldLn: 1, Sign: "-", Text: "g1"}, {OldLn: 2, Sign: "-", Text: "g2"}}) {
		t.Errorf("deleted: %+v", a.Rows)
	}
	if a := ann("fresh.go"); !reflect.DeepEqual(a.Rows, []Row{{Ln: 1, Sign: "+", Text: "f1"}, {Ln: 2, Sign: "+", Text: "f2"}}) {
		t.Errorf("untracked: %+v", a.Rows)
	}
	if a := ann("keep.go"); !reflect.DeepEqual(a.Rows, []Row{{Ln: 1, OldLn: 1, Sign: " ", Text: "k1"}, {Ln: 2, OldLn: 2, Sign: " ", Text: "k2"}}) {
		t.Errorf("unchanged: %+v", a.Rows)
	}
	if a := ann("bin.dat"); !a.Binary || a.Rows != nil {
		t.Errorf("binary: %+v", a)
	}

	for _, p := range []string{"link.go", "../x", "/etc/passwd"} {
		if _, err := Annotate(ctx, cmd, o, p); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// With a head commit, nothing comes from the working tree.
func TestAnnotateAtCommit(t *testing.T) {
	dir := excerptRepo(t)
	ctx, cmd := context.Background(), moexec.DefaultCommander
	write(t, dir, "a.go", "dirty\n")
	write(t, dir, "keep.go", "dirty\n")
	o, err := GetOverviewRange(ctx, cmd, dir, gitOut(t, dir, "rev-parse", "main"), gitOut(t, dir, "rev-parse", "feat"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := Annotate(ctx, cmd, o, "a.go")
	if err != nil || texts(a.Cut(1, 3, SideNew, 0)) != "one,two,TWO,two and a half" {
		t.Errorf("a.go: %+v %v", a, err)
	}
	a, err = Annotate(ctx, cmd, o, "keep.go")
	if err != nil || texts(a.Cut(1, 2, SideNew, 0)) != "k1,k2" {
		t.Errorf("keep.go: %+v %v", a, err)
	}
	o.Head = "HEAD"
	if _, err := Annotate(ctx, cmd, o, "a.go"); !errors.Is(err, ErrUnknownCommit) {
		t.Errorf("a ref as head: %v", err)
	}
}

func TestStamp(t *testing.T) {
	dir := t.TempDir()
	if got := Stamp(dir, "x.go"); got != "missing" {
		t.Errorf("missing: %q", got)
	}
	write(t, dir, "x.go", "a\n")
	before := Stamp(dir, "x.go")
	write(t, dir, "x.go", "ab\n")
	if after := Stamp(dir, "x.go"); after == before || !strings.HasPrefix(after, "3:") {
		t.Errorf("stamp %q then %q", before, after)
	}
	if got := Stamp(dir, "../x"); got != "unresolved" {
		t.Errorf("outside: %q", got)
	}
}
