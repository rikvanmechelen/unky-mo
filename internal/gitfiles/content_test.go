package gitfiles

import (
	"context"
	"errors"
	"os"
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

func TestResolveAllowsInsidePaths(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sub/a.txt", "x")
	if err := os.Symlink("sub/a.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)

	cases := map[string]string{
		"sub/a.txt":        "sub/a.txt",
		"alias.txt":        "sub/a.txt", // symlink inside the checkout resolves
		"gone.txt":         "gone.txt",  // deleted file
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
