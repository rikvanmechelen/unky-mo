package gitfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// MaxContentBytes caps a file sent to the browser's editor. Larger files
// come back with TooLarge set and no content.
const MaxContentBytes = 2 << 20

// ErrOutsideRoot is returned for a path that isn't a plain relative path
// inside the checkout, or that resolves (through a symlink) outside it.
var ErrOutsideRoot = errors.New("path is outside the checkout")

// Content is one version of a file, for the web editor. Text is only set
// for a file that exists, is valid UTF-8 without NUL bytes (Binary
// otherwise) and fits MaxContentBytes (TooLarge otherwise). Hash is the
// sha256 of the raw bytes, set whenever the file exists.
type Content struct {
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"tooLarge,omitempty"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash,omitempty"`
	Text     string `json:"text"`
}

// Resolve joins a browser-supplied path onto root, refusing anything that
// isn't a clean relative path (absolute, "..", "./", backslashes) and any
// path whose symlinks resolve outside root. The returned path is the
// resolved one. A missing file resolves under its deepest existing
// ancestor, as long as that ancestor stays inside root.
func Resolve(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, `\`) || filepath.Clean(rel) != rel ||
		rel == ".." || strings.HasPrefix(rel, "../") {
		return "", ErrOutsideRoot
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(realRoot, rel)
	resolved, err := filepath.EvalSymlinks(full)
	if errors.Is(err, os.ErrNotExist) {
		// Deleted in the working tree (maybe with its directories): the
		// deepest ancestor that still exists must resolve inside root.
		dir, rest := filepath.Dir(full), filepath.Base(full)
		for {
			resolvedDir, derr := filepath.EvalSymlinks(dir)
			if derr == nil {
				if resolvedDir != realRoot && !within(realRoot, resolvedDir) {
					return "", ErrOutsideRoot
				}
				return filepath.Join(resolvedDir, rest), nil
			}
			if !errors.Is(derr, os.ErrNotExist) || dir == realRoot {
				return "", derr
			}
			dir, rest = filepath.Dir(dir), filepath.Join(filepath.Base(dir), rest)
		}
	}
	if err != nil {
		return "", err
	}
	if !within(realRoot, resolved) {
		return "", ErrOutsideRoot
	}
	return resolved, nil
}

func within(root, path string) bool {
	r, err := filepath.Rel(root, path)
	return err == nil && r != "." && r != ".." && !strings.HasPrefix(r, "../")
}

// ReadFile reads the working-tree version of rel in the checkout at root.
// A missing file isn't an error: it comes back with Exists false.
func ReadFile(root, rel string) (*Content, error) {
	path, err := Resolve(root, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Content{Path: rel}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	// Read one byte past the cap so an oversized file is detected even if
	// it grew since Stat.
	data, err := io.ReadAll(io.LimitReader(f, MaxContentBytes+1))
	if err != nil {
		return nil, err
	}
	return newContent(rel, data, info.Size()), nil
}

// ReadHEAD reads rel as committed at HEAD in the checkout at root. A path
// that isn't in HEAD (new or untracked file, or a repo without commits)
// comes back with Exists false. rel must already have passed Resolve: git
// would otherwise read any committed path, symlinked or not.
func ReadHEAD(ctx context.Context, cmd moexec.Commander, root, rel string) (*Content, error) {
	if _, err := Resolve(root, rel); err != nil {
		return nil, err
	}
	// cat-file -s first so a huge blob is never read into memory.
	spec := "HEAD:" + rel
	out, _, err := cmd.Output(ctx, root, "git", "cat-file", "-s", spec)
	if err != nil {
		return &Content{Path: rel}, nil
	}
	var size int64
	if _, err := fmt.Sscan(string(out), &size); err != nil {
		return nil, fmt.Errorf("git cat-file -s %s: %q", spec, out)
	}
	if size > MaxContentBytes {
		return &Content{Path: rel, Exists: true, TooLarge: true, Size: size}, nil
	}
	data, err := git(ctx, cmd, root, "cat-file", "blob", spec)
	if err != nil {
		return nil, err
	}
	return newContent(rel, data, size), nil
}

func newContent(rel string, data []byte, size int64) *Content {
	c := &Content{Path: rel, Exists: true, Size: size}
	if len(data) > MaxContentBytes {
		c.TooLarge = true
		return c
	}
	sum := sha256.Sum256(data)
	c.Hash = hex.EncodeToString(sum[:])
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		c.Binary = true
		return c
	}
	c.Text = string(data)
	return c
}

// ConflictError is returned by WriteFile when the file on disk no longer
// has the hash the edit was based on (Claude, or another tab, changed it).
// Current is what's on disk now.
type ConflictError struct{ Current *Content }

func (e *ConflictError) Error() string { return "file changed on disk since it was loaded" }

// ErrTooLarge is returned by WriteFile for text over MaxContentBytes.
var ErrTooLarge = errors.New("file is too large to save")

// WriteFile replaces the working-tree file rel with text, but only if it
// still has baseHash (the hash of the version the edit started from) —
// otherwise it returns a *ConflictError carrying the current version. Only
// existing regular files are written (creating and deleting files is out
// of scope). The write is atomic: a temp file in the same directory, with
// the original's permissions, renamed over it.
func WriteFile(root, rel, text, baseHash string) (*Content, error) {
	if len(text) > MaxContentBytes {
		return nil, ErrTooLarge
	}
	path, err := Resolve(root, rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &ConflictError{Current: &Content{Path: rel}}
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", rel)
	}
	if err := checkBase(path, rel, baseHash); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".mo-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// Check again right before the swap: Claude may have written the file
	// while the temp file was being written.
	if err := checkBase(path, rel, baseHash); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return newContent(rel, []byte(text), int64(len(text))), nil
}

// checkBase returns a *ConflictError unless the file at path hashes to
// baseHash.
func checkBase(path, rel, baseHash string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &ConflictError{Current: &Content{Path: rel}}
	}
	if err != nil {
		return err
	}
	cur := newContent(rel, data, int64(len(data)))
	if cur.Hash != baseHash {
		return &ConflictError{Current: cur}
	}
	return nil
}
