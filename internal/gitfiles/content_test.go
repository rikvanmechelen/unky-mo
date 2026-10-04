package gitfiles

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

func TestResolveRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, root, "ok.txt", "x")
	write(t, outside, "secret.txt", "s")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		"", ".", "..", "../x", "a/../../x", "/etc/passwd", "./ok.txt", "sub//ok.txt", `sub\ok.txt`,
		"link.txt",           // symlink to a file outside
		"linkdir/secret.txt", // through a symlinked dir
		"linkdir/gone.txt",   // missing, but its parent is outside
	} {
		if _, err := Resolve(root, rel); err == nil {
			t.Errorf("Resolve(%q): want error, got nil", rel)
		}
	}
}

// Symlinks are refused even when they point inside the checkout: they can
// reach ignored files (.env) or .git, which the editor must never touch.
func TestResolveRejectsInsideSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sub/a.txt", "x")
	write(t, root, ".env", "SECRET=1")
	write(t, root, ".git/hooks/pre-commit", "#!/bin/sh")
	for link, target := range map[string]string{
		"alias.txt": "sub/a.txt",
		"env-link":  ".env",
		"hook-link": ".git/hooks/pre-commit",
		"subdir":    "sub",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"alias.txt", "env-link", "hook-link", "subdir/a.txt", "subdir/new.txt"} {
		if _, err := Resolve(root, rel); !errors.Is(err, ErrOutsideRoot) {
			t.Errorf("Resolve(%q): want ErrOutsideRoot, got %v", rel, err)
		}
	}
	if _, err := WriteFile(root, "hook-link", "evil", "x"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("WriteFile through a symlink into .git: want ErrOutsideRoot, got %v", err)
	}
}

func TestResolveAllowsInsidePaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sub/a.txt", "x")
	realRoot, _ := filepath.EvalSymlinks(root)

	cases := map[string]string{
		"sub/a.txt":        "sub/a.txt",
		"gone.txt":         "gone.txt", // deleted file
		"deleted/dir/x.go": "deleted/dir/x.go",
	}
	for rel, want := range cases {
		got, err := Resolve(root, rel)
		if err != nil {
			t.Errorf("Resolve(%q): %v", rel, err)
			continue
		}
		if got != filepath.Join(realRoot, want) {
			t.Errorf("Resolve(%q) = %q, want %q", rel, got, filepath.Join(realRoot, want))
		}
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "hello\n")
	write(t, root, "bin.dat", "a\x00b")
	write(t, root, "latin1.txt", "caf\xe9")
	if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, MaxContentBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	c, err := ReadFile(root, "a.txt")
	if err != nil || !c.Exists || c.Text != "hello\n" || c.Size != 6 ||
		c.Hash != "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03" {
		t.Errorf("a.txt: %+v, %v", c, err)
	}
	for _, rel := range []string{"bin.dat", "latin1.txt"} {
		if c, err := ReadFile(root, rel); err != nil || !c.Binary || c.Text != "" || c.Hash == "" {
			t.Errorf("%s: want binary with hash, got %+v, %v", rel, c, err)
		}
	}
	if c, err := ReadFile(root, "big.txt"); err != nil || !c.TooLarge || c.Text != "" {
		t.Errorf("big.txt: want TooLarge, got %+v, %v", c, err)
	}
	if c, err := ReadFile(root, "gone.txt"); err != nil || c.Exists {
		t.Errorf("gone.txt: want !Exists, got %+v, %v", c, err)
	}
	if _, err := ReadFile(root, "dir"); err == nil {
		t.Error("dir: want error for a directory")
	}
	if _, err := ReadFile(root, "../x"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("../x: want ErrOutsideRoot, got %v", err)
	}
}

func TestReadHEADRealGit(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "a.go", "changed\n")
	run(t, dir, "rm", "-rq", "sub")
	write(t, dir, "new.txt", "n\n")
	ctx := context.Background()

	c, err := ReadHEAD(ctx, moexec.DefaultCommander, dir, "a.go")
	if err != nil || !c.Exists || c.Text != "one\ntwo\n" {
		t.Errorf("a.go: want committed text, got %+v, %v", c, err)
	}
	// Deleted along with its directory: still readable from HEAD.
	if c, err := ReadHEAD(ctx, moexec.DefaultCommander, dir, "sub/b.go"); err != nil || c.Text != "b\n" {
		t.Errorf("sub/b.go: got %+v, %v", c, err)
	}
	if c, err := ReadHEAD(ctx, moexec.DefaultCommander, dir, "new.txt"); err != nil || c.Exists {
		t.Errorf("new.txt: want !Exists, got %+v, %v", c, err)
	}
	if _, err := ReadHEAD(ctx, moexec.DefaultCommander, dir, "../x"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("../x: want ErrOutsideRoot, got %v", err)
	}
}

// A git failure is an error, not "the file isn't in HEAD" (which would show
// the whole file as added).
func TestReadHEADGitFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir() // not a repository
	write(t, dir, "a.txt", "x")
	if c, err := ReadHEAD(context.Background(), moexec.DefaultCommander, dir, "a.txt"); err == nil {
		t.Errorf("outside a repo: want an error, got %+v", c)
	}
}

func TestReadHEADNoCommitsYet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(t, dir, "a.txt", "x")
	if c, err := ReadHEAD(context.Background(), moexec.DefaultCommander, dir, "a.txt"); err != nil || c.Exists {
		t.Errorf("no commits: want !Exists, got %+v, %v", c, err)
	}
}

func TestReadHEADTooLargeSkipsBlob(t *testing.T) {
	dir := newRepo(t)
	write(t, dir, "big.txt", strings.Repeat("x", MaxContentBytes+1))
	run(t, dir, "add", "big.txt")
	run(t, dir, "commit", "-qm", "big")
	c, err := ReadHEAD(context.Background(), moexec.DefaultCommander, dir, "big.txt")
	if err != nil || !c.Exists || !c.TooLarge || c.Text != "" || c.Size != MaxContentBytes+1 {
		t.Errorf("big.txt: want TooLarge, got exists=%v tooLarge=%v size=%d err=%v", c.Exists, c.TooLarge, c.Size, err)
	}
}

func TestWriteFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.sh", "echo one\n")
	if err := os.Chmod(filepath.Join(root, "a.sh"), 0o750); err != nil {
		t.Fatal(err)
	}
	orig, _ := ReadFile(root, "a.sh")

	got, err := WriteFile(root, "a.sh", "echo two\n", orig.Hash)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	disk, _ := os.ReadFile(filepath.Join(root, "a.sh"))
	if string(disk) != "echo two\n" || got.Text != "echo two\n" || got.Hash == orig.Hash {
		t.Errorf("after write: disk %q, got %+v", disk, got)
	}
	if info, _ := os.Stat(filepath.Join(root, "a.sh")); info.Mode().Perm() != 0o750 {
		t.Errorf("mode: want 0750, got %v", info.Mode().Perm())
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}

	// The stale base hash now conflicts, and the conflict carries the
	// current version; the file is untouched.
	_, err = WriteFile(root, "a.sh", "echo three\n", orig.Hash)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.Current.Text != "echo two\n" {
		t.Fatalf("stale base: want ConflictError with current text, got %v", err)
	}
	if disk, _ := os.ReadFile(filepath.Join(root, "a.sh")); string(disk) != "echo two\n" {
		t.Errorf("conflicting write changed the file: %q", disk)
	}
}

func TestWriteFileRefusals(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, outside, "secret.txt", "s")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile(root, "link.txt", "x", ""); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("symlink out: want ErrOutsideRoot, got %v", err)
	}
	if disk, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(disk) != "s" {
		t.Errorf("file outside the checkout was changed: %q", disk)
	}
	var conflict *ConflictError
	if _, err := WriteFile(root, "new.txt", "x", ""); !errors.As(err, &conflict) || conflict.Current.Exists {
		t.Errorf("missing file: want ConflictError (not created), got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("WriteFile created a new file")
	}
	write(t, root, "big.txt", "x")
	if _, err := WriteFile(root, "big.txt", strings.Repeat("x", MaxContentBytes+1), ""); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: want ErrTooLarge, got %v", err)
	}
}
