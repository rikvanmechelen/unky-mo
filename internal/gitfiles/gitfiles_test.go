package gitfiles

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

func z(recs ...string) []byte { return []byte(strings.Join(recs, "\x00") + "\x00") }

func TestParseStatusV2(t *testing.T) {
	out := z(
		"# branch.oid 39b60ea3a15dc0b1ccf422aa0bf9e443bcf9b9b7",
		"# branch.head feat x",
		"# branch.upstream origin/feat x",
		"# branch.ab +3 -1",
		"1 .M N... 100644 100644 100644 aaa bbb internal/web/server.go",
		"1 A. N... 000000 100644 100644 000 ccc internal/web/files.go",
		"1 D. N... 100644 000000 000000 ddd 000 old.go",
		"1 MM N... 100644 100644 100644 eee fff path with spaces.go",
		"2 R. N... 100644 100644 100644 ggg ggg R100 new name.go", "old name.go",
		"u UU N... 100644 100644 100644 100644 h1 h2 h3 conflict.go",
		"? notes/todo.md",
		"! build/out.bin",
	)
	files, sync := ParseStatusV2(out)

	wantSync := Sync{Branch: "feat x", Upstream: "origin/feat x", Ahead: 3, Behind: 1}
	if sync != wantSync {
		t.Errorf("sync: want %+v, got %+v", wantSync, sync)
	}
	want := []File{
		{Path: "internal/web/server.go", Status: "M"},
		{Path: "internal/web/files.go", Status: "A"},
		{Path: "old.go", Status: "D"},
		{Path: "path with spaces.go", Status: "M"},
		{Path: "new name.go", Status: "R"},
		{Path: "conflict.go", Status: "U"},
		{Path: "notes/todo.md", Status: "?"},
	}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("files:\nwant %+v\ngot  %+v", want, files)
	}
}

func TestParseStatusV2NoUpstreamOrDetached(t *testing.T) {
	_, sync := ParseStatusV2(z("# branch.oid abc", "# branch.head main"))
	if sync != (Sync{Branch: "main"}) {
		t.Errorf("no upstream: got %+v", sync)
	}
	_, sync = ParseStatusV2(z("# branch.oid abc", "# branch.head (detached)"))
	if sync.Branch != "(detached)" || sync.Upstream != "" {
		t.Errorf("detached: got %+v", sync)
	}
}

func TestParseNumstat(t *testing.T) {
	out := z(
		"24\t6\tinternal/web/server.go",
		"-\t-\tlogo.png",
		"3\t1\t", "old name.go", "new name.go",
		"0\t12\tgone.go",
	)
	got := ParseNumstat(out)
	want := map[string]Counts{
		"internal/web/server.go": {Added: 24, Removed: 6},
		"logo.png":               {Binary: true},
		"new name.go":            {Added: 3, Removed: 1},
		"gone.go":                {Removed: 12},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("want %+v\ngot  %+v", want, got)
	}
}

// newRepo creates a repo with one commit holding a.go (2 lines) and
// sub/b.go (1 line). Skips when git isn't installed.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	run(t, dir, "config", "user.email", "test@example.com")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "commit.gpgsign", "false")
	write(t, dir, "a.go", "one\ntwo\n")
	write(t, dir, "sub/b.go", "b\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-m", "root")
	return dir
}

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

func TestGetChangesRealGit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "one\nTWO\nthree\n") // unstaged: +2 -1
	write(t, dir, "sub/b.go", "b\nc\n")
	run(t, dir, "add", "sub/b.go")               // staged: +1
	write(t, dir, "new file.txt", "x\ny\nz")     // untracked, no trailing newline: 3 lines
	write(t, dir, "bin.dat", "a\x00b")           // untracked binary
	write(t, dir, ".gitignore", "ignored.log\n") // untracked: 1 line
	write(t, dir, "ignored.log", "not listed\n")

	// Run from a subfolder: paths must still be root-relative.
	ch, err := GetChanges(context.Background(), moexec.DefaultCommander, filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatalf("GetChanges: %v", err)
	}
	got := map[string]File{}
	for _, f := range ch.Files {
		got[f.Path] = f
	}
	want := map[string]File{
		"a.go":         {Path: "a.go", Status: "M", Added: 2, Removed: 1},
		"sub/b.go":     {Path: "sub/b.go", Status: "M", Added: 1},
		"new file.txt": {Path: "new file.txt", Status: "?", Added: 3},
		"bin.dat":      {Path: "bin.dat", Status: "?", Binary: true},
		".gitignore":   {Path: ".gitignore", Status: "?", Added: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files:\nwant %+v\ngot  %+v", want, got)
	}
	if ch.Added != 7 || ch.Removed != 1 {
		t.Errorf("totals: want +7 -1, got +%d -%d", ch.Added, ch.Removed)
	}
	if ch.Sync != (Sync{Branch: "main"}) {
		t.Errorf("sync: want main with no upstream, got %+v", ch.Sync)
	}

	if b := CurrentBranch(context.Background(), moexec.DefaultCommander, dir); b != "main" {
		t.Errorf("CurrentBranch: want main, got %q", b)
	}
}

func TestTreeRealGit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "z.txt", "new\n")
	write(t, dir, ".gitignore", "*.log\n")
	write(t, dir, "debug.log", "ignored\n")

	_, paths, err := Tree(context.Background(), moexec.DefaultCommander, filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	want := []string{".gitignore", "a.go", "sub/b.go", "z.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("want %v, got %v", want, paths)
	}
}

func TestNotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := GetChanges(context.Background(), moexec.DefaultCommander, t.TempDir()); err != ErrNotRepo {
		t.Errorf("want ErrNotRepo, got %v", err)
	}
}

func TestDiffRealGit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "one\nTWO\n")
	run(t, dir, "rm", "-q", "sub/b.go")
	write(t, dir, "new.txt", "x\ny\n")
	write(t, dir, "bin.dat", "a\x00b")
	ctx := context.Background()

	cases := []struct {
		path      string
		untracked bool
		want      []string
	}{
		{"a.go", false, []string{"--- a/a.go", "+++ b/a.go", "@@ -1,2 +1,2 @@", "-two", "+TWO"}},
		{"sub/b.go", false, []string{"deleted file mode", "+++ /dev/null", "-b"}},
		{"new.txt", true, []string{"+++ b/new.txt", "@@ -0,0 +1,2 @@", "+x", "+y"}},
		{"bin.dat", true, []string{"Binary files"}},
	}
	for _, c := range cases {
		diff, truncated, err := Diff(ctx, moexec.DefaultCommander, dir, c.path, c.untracked)
		if err != nil || truncated {
			t.Fatalf("%s: err=%v truncated=%v", c.path, err, truncated)
		}
		for _, w := range c.want {
			if !strings.Contains(diff, w) {
				t.Errorf("%s: diff lacks %q:\n%s", c.path, w, diff)
			}
		}
	}
}
