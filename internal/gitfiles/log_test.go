package gitfiles

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

func TestParseDecorations(t *testing.T) {
	got := ParseDecorations("HEAD -> refs/heads/main, refs/remotes/origin/main, refs/remotes/origin/HEAD, tag: refs/tags/v1, refs/heads/feat")
	want := []Ref{
		{Name: "main", Kind: "branch", Head: true},
		{Name: "origin/main", Kind: "remote"},
		{Name: "v1", Kind: "tag"},
		{Name: "feat", Kind: "branch"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := ParseDecorations("HEAD, refs/heads/x"); !reflect.DeepEqual(got, []Ref{{Name: "HEAD", Kind: "head"}, {Name: "x", Kind: "branch"}}) {
		t.Errorf("detached: got %+v", got)
	}
	if got := ParseDecorations(""); got != nil {
		t.Errorf("empty: got %+v", got)
	}
}

func TestParseLog(t *testing.T) {
	out := []byte("aaa\x1fbbb ccc\x1fHEAD -> refs/heads/main\x1fAnn\x1f1700000000\x1fmerge it\x00" +
		"bbb\x1f\x1f\x1fBob\x1f1690000000\x1froot \x1f odd\x00")
	got := ParseLog(out)
	// The second record has a stray field separator: it's skipped rather
	// than misparsed.
	if len(got) != 1 {
		t.Fatalf("got %d commits: %+v", len(got), got)
	}
	if got[0].Hash != "aaa" || !reflect.DeepEqual(got[0].Parents, []string{"bbb", "ccc"}) || got[0].Time != 1700000000 || got[0].Refs[0].Name != "main" {
		t.Errorf("first: %+v", got[0])
	}
	out = []byte("bbb\x1f\x1f\x1fBob\x1f1690000000\x1froot\x00")
	if got := ParseLog(out); len(got) != 1 || len(got[0].Parents) != 0 || got[0].Subject != "root" {
		t.Errorf("root: %+v", got)
	}
}

func TestParseNameStatus(t *testing.T) {
	got := ParseNameStatus(z("M", "a.go", "R087", "old.go", "new.go", "A", "dir/c.go", "D", "gone"))
	want := []CommitFile{
		{Status: "M", Path: "a.go"},
		{Status: "R", Path: "new.go", OldPath: "old.go"},
		{Status: "A", Path: "dir/c.go"},
		{Status: "D", Path: "gone"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func subjects(l *Log) []string {
	var s []string
	for _, c := range l.Commits {
		s = append(s, c.Subject)
	}
	return s
}

func TestGetLogRealGit(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	cmd := moexec.DefaultCommander

	// main: root, m1. feat (from root): f1, f2. other (from root): o1.
	run(t, dir, "checkout", "-q", "-b", "feat")
	write(t, dir, "a.go", "one\ntwo\nf1\n")
	run(t, dir, "commit", "-q", "-am", "f1")
	write(t, dir, "a.go", "one\ntwo\nf1\nf2\n")
	run(t, dir, "commit", "-q", "-am", "f2")
	run(t, dir, "checkout", "-q", "-b", "other", "main")
	write(t, dir, "sub/b.go", "o1\n")
	run(t, dir, "commit", "-q", "-am", "o1")
	run(t, dir, "checkout", "-q", "main")
	write(t, dir, "sub/b.go", "m1\n")
	run(t, dir, "commit", "-q", "-am", "m1")
	run(t, dir, "tag", "v1")
	run(t, dir, "checkout", "-q", "feat")

	l, err := GetLog(ctx, cmd, dir, ScopeBranch)
	if err != nil {
		t.Fatal(err)
	}
	if l.Branch != "feat" || l.Base != "main" || l.Head != revParse(t, dir, "HEAD") {
		t.Errorf("branch %q base %q head %q", l.Branch, l.Base, l.Head)
	}
	got := subjects(l)
	if len(got) != 4 || got[3] != "root" || strings.Contains(strings.Join(got, ","), "o1") {
		t.Errorf("branch scope: %v (want feat + main, not other)", got)
	}
	for _, c := range l.Commits {
		if c.Subject == "f2" && (len(c.Refs) != 1 || c.Refs[0] != (Ref{Name: "feat", Kind: "branch", Head: true})) {
			t.Errorf("f2 refs: %+v", c.Refs)
		}
		if c.Subject == "m1" && len(c.Refs) != 2 {
			t.Errorf("m1 refs: %+v", c.Refs)
		}
		if c.Unpushed {
			t.Errorf("%s marked unpushed in a repo without remotes", c.Subject)
		}
	}

	l, err = GetLog(ctx, cmd, dir, ScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if got := subjects(l); len(got) != 5 {
		t.Errorf("all scope: %v", got)
	}
}

func TestGetLogUnpushed(t *testing.T) {
	dir := newRepo(t)
	remote := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	run(t, dir, "remote", "add", "origin", remote)
	run(t, dir, "push", "-q", "-u", "origin", "main")
	write(t, dir, "a.go", "local\n")
	run(t, dir, "commit", "-q", "-am", "local")

	l, err := GetLog(context.Background(), moexec.DefaultCommander, dir, ScopeBranch)
	if err != nil {
		t.Fatal(err)
	}
	if l.Upstream != "origin/main" {
		t.Errorf("upstream %q", l.Upstream)
	}
	if len(l.Commits) != 2 || !l.Commits[0].Unpushed || l.Commits[1].Unpushed {
		t.Errorf("commits: %+v", l.Commits)
	}
	// A branch without an upstream counts what no remote branch has.
	run(t, dir, "checkout", "-q", "-b", "topic")
	write(t, dir, "a.go", "topic\n")
	run(t, dir, "commit", "-q", "-am", "topic")
	l, err = GetLog(context.Background(), moexec.DefaultCommander, dir, ScopeBranch)
	if err != nil {
		t.Fatal(err)
	}
	var unpushed []string
	for _, c := range l.Commits {
		if c.Unpushed {
			unpushed = append(unpushed, c.Subject)
		}
	}
	if !reflect.DeepEqual(unpushed, []string{"topic", "local"}) {
		t.Errorf("unpushed without upstream: %v", unpushed)
	}
}

func TestGetLogNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q")
	l, err := GetLog(context.Background(), moexec.DefaultCommander, dir, ScopeBranch)
	if err != nil || l.Head != "" || len(l.Commits) != 0 {
		t.Errorf("got %+v, %v", l, err)
	}
}

func TestGetCommitRealGit(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	cmd := moexec.DefaultCommander
	root := revParse(t, dir, "HEAD")

	run(t, dir, "mv", "sub/b.go", "sub/renamed.go")
	write(t, dir, "a.go", "one\nTWO\nthree\n")
	write(t, dir, "new.txt", "n\n")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "change things\n\nwith a body")
	head := revParse(t, dir, "HEAD")

	d, err := GetCommit(ctx, cmd, dir, head)
	if err != nil {
		t.Fatal(err)
	}
	if d.Message != "change things\n\nwith a body" || d.Author != "Test" || !reflect.DeepEqual(d.Parents, []string{root}) {
		t.Errorf("detail: %+v", d)
	}
	want := []CommitFile{
		{Path: "a.go", Status: "M", Added: 2, Removed: 1},
		{Path: "new.txt", Status: "A", Added: 1},
		{Path: "sub/renamed.go", OldPath: "sub/b.go", Status: "R"},
	}
	if !reflect.DeepEqual(d.Files, want) {
		t.Errorf("files: %+v, want %+v", d.Files, want)
	}

	// The root commit diffs against nothing.
	d, err = GetCommit(ctx, cmd, dir, root)
	if err != nil || len(d.Files) != 2 || d.Files[0].Status != "A" || len(d.Parents) != 0 {
		t.Errorf("root: %+v, %v", d, err)
	}

	f, err := GetCommitFile(ctx, cmd, dir, head, "a.go")
	if err != nil || f.Before.Text != "one\ntwo\n" || f.After.Text != "one\nTWO\nthree\n" {
		t.Errorf("a.go: %+v, %v", f, err)
	}
	f, err = GetCommitFile(ctx, cmd, dir, head, "sub/renamed.go")
	if err != nil || f.OldPath != "sub/b.go" || f.Before.Text != "b\n" || f.After.Text != "b\n" {
		t.Errorf("rename: %+v, %v", f, err)
	}
	f, err = GetCommitFile(ctx, cmd, dir, head, "new.txt")
	if err != nil || f.Before.Exists || f.After.Text != "n\n" {
		t.Errorf("added: %+v, %v", f, err)
	}
	if _, err := GetCommitFile(ctx, cmd, dir, head, "sub/b.go"); !errors.Is(err, ErrNotInCommit) {
		t.Errorf("old name of a rename: %v, want ErrNotInCommit", err)
	}
	if _, err := GetCommitFile(ctx, cmd, dir, root, "a.go"); err != nil {
		t.Errorf("root commit file: %v", err)
	}
}

func TestGetCommitRejectsNonHashes(t *testing.T) {
	dir := newRepo(t)
	ctx := context.Background()
	cmd := moexec.DefaultCommander
	tree := revParse(t, dir, "HEAD^{tree}")
	for _, h := range []string{"HEAD", "main", "--all", revParse(t, dir, "HEAD")[:12], strings.Repeat("0", 40), tree} {
		if _, err := GetCommit(ctx, cmd, dir, h); !errors.Is(err, ErrUnknownCommit) {
			t.Errorf("GetCommit(%q) = %v, want ErrUnknownCommit", h, err)
		}
		if _, err := GetCommitFile(ctx, cmd, dir, h, "a.go"); !errors.Is(err, ErrUnknownCommit) {
			t.Errorf("GetCommitFile(%q) = %v, want ErrUnknownCommit", h, err)
		}
	}
}
