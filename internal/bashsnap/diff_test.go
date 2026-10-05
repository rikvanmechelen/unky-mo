package bashsnap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiff(t *testing.T) {
	repo := gitRepo(t)
	write(t, repo, "old.txt", strings.Repeat("same line\n", 10))
	write(t, repo, "gone.txt", "bye\n")
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "more")
	s := testStore(t)
	ctx := context.Background()

	if err := s.Pre(ctx, session, "toolu_a", repo, false); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "a.txt", "one\ntwo\n")
	write(t, repo, "fresh.go", "package x\n")
	os.Remove(filepath.Join(repo, "gone.txt"))
	if err := os.Rename(filepath.Join(repo, "old.txt"), filepath.Join(repo, "moved.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "img.bin", "\x00\x01\x02")
	if err := s.Post(ctx, session, "toolu_a"); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Record(session, "toolu_a")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Diff(ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]FileStat{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if f := byPath["a.txt"]; f.Added != 1 || f.Removed != 0 {
		t.Errorf("a.txt = %+v", f)
	}
	if f := byPath["fresh.go"]; f.Added != 1 {
		t.Errorf("fresh.go = %+v", f)
	}
	if f := byPath["gone.txt"]; f.Removed != 1 {
		t.Errorf("gone.txt = %+v", f)
	}
	if f := byPath["moved.txt"]; f.OldPath != "old.txt" {
		t.Errorf("moved.txt = %+v, want a rename", f)
	}
	if f := byPath["img.bin"]; !f.Binary {
		t.Errorf("img.bin = %+v, want binary", f)
	}
	if len(d.Files) != 5 {
		t.Errorf("files = %+v", d.Files)
	}
	for _, want := range []string{"+++ b/a.txt", "+two", "+++ b/fresh.go", "--- a/gone.txt", "rename from old.txt"} {
		if !strings.Contains(d.Patch, want) {
			t.Errorf("patch lacks %q:\n%s", want, d.Patch)
		}
	}
}

func TestParseNumstat(t *testing.T) {
	got, err := parseNumstat([]byte("1\t2\ta b.txt\x00-\t-\tx.png\x000\t0\t\x00old.go\x00new.go\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []FileStat{{Path: "a b.txt", Added: 1, Removed: 2}, {Path: "x.png", Binary: true}, {Path: "new.go", OldPath: "old.go"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"x\ty\tz\x00", "1\t2\x00", "1\t2\t\x00only-old\x00"} {
		if _, err := parseNumstat([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
