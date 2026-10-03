// Package gitfiles reads a checkout's changed files, file list and upstream
// sync state from git — the data behind the web chat view's Files panel
// (the browser counterpart of the sidebar's Files section).
//
// Parsers are pure functions over git's -z output so they can be tested on
// fixture bytes; the fetchers shell out through a moexec.Commander.
package gitfiles

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// ErrNotRepo is returned when dir isn't inside a git work tree.
var ErrNotRepo = errors.New("not a git repository")

const (
	maxFiles           = 1000    // changed files returned; the rest set Truncated
	maxUntrackedCounts = 200     // untracked files whose lines are counted
	maxCountBytes      = 1 << 20 // untracked files larger than this aren't counted
)

// File is one changed path, relative to the repo root.
type File struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // M, A, D, R, U (conflict) or ? (untracked)
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
}

// Sync is the checkout's branch and its position relative to its upstream.
// Upstream is "" when the branch tracks nothing.
type Sync struct {
	Branch   string `json:"branch"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
}

// Changes is everything the Files panel's Changed tab and footer show.
type Changes struct {
	Root      string `json:"root"`
	Files     []File `json:"files"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Sync      Sync   `json:"sync"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ParseStatusV2 parses `git status --porcelain=v2 --branch -z` output into
// the changed files (in git's order) and the branch/upstream headers.
// Ignored entries ("!") are skipped.
func ParseStatusV2(out []byte) ([]File, Sync) {
	var files []File
	var sync Sync
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			parseBranchHeader(rec, &sync)
		case '1':
			// 1 XY sub mH mI mW hH hI path
			if f := strings.SplitN(rec, " ", 9); len(f) == 9 {
				files = append(files, File{Path: f[8], Status: label(f[1])})
			}
		case '2':
			// 2 XY sub mH mI mW hH hI Xscore path, then origPath as its own record
			if f := strings.SplitN(rec, " ", 10); len(f) == 10 {
				files = append(files, File{Path: f[9], Status: label(f[1])})
			}
			i++ // skip origPath
		case 'u':
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 {
				files = append(files, File{Path: f[10], Status: "U"})
			}
		case '?':
			files = append(files, File{Path: strings.TrimPrefix(rec, "? "), Status: "?"})
		}
	}
	return files, sync
}

func parseBranchHeader(rec string, sync *Sync) {
	switch {
	case strings.HasPrefix(rec, "# branch.head "):
		sync.Branch = strings.TrimPrefix(rec, "# branch.head ")
	case strings.HasPrefix(rec, "# branch.upstream "):
		sync.Upstream = strings.TrimPrefix(rec, "# branch.upstream ")
	case strings.HasPrefix(rec, "# branch.ab "):
		// "# branch.ab +3 -1"
		f := strings.Fields(strings.TrimPrefix(rec, "# branch.ab "))
		if len(f) == 2 {
			sync.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
			sync.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
		}
	}
}

// label picks one character from a v2 "XY" field: the worktree column
// first, then the index column (same priority as the sidebar). "." means
// unchanged in that column.
func label(xy string) string {
	if len(xy) != 2 {
		return "M"
	}
	for _, c := range []byte{xy[1], xy[0]} {
		switch c {
		case '.':
			continue
		case 'C':
			return "A" // a copy is a new file
		case 'T':
			return "M" // type change
		default:
			return string(c)
		}
	}
	return "M"
}

// Counts are a path's line totals from `git diff --numstat`.
type Counts struct {
	Added, Removed int
	Binary         bool
}

// ParseNumstat parses `git diff --numstat -z` output, keyed by the (new)
// path. Renames come as "A\tR\t" followed by the old and new paths as
// separate records; binary files report "-\t-".
func ParseNumstat(out []byte) map[string]Counts {
	res := make(map[string]Counts)
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		f := strings.SplitN(recs[i], "\t", 3)
		if len(f) != 3 {
			continue
		}
		path := f[2]
		if path == "" && i+2 < len(recs) { // rename: old, new follow
			path = recs[i+2]
			i += 2
		}
		var c Counts
		if f[0] == "-" {
			c.Binary = true
		} else {
			c.Added, _ = strconv.Atoi(f[0])
			c.Removed, _ = strconv.Atoi(f[1])
		}
		res[path] = c
	}
	return res
}

func git(ctx context.Context, cmd moexec.Commander, dir string, args ...string) ([]byte, error) {
	out, _, err := cmd.Output(ctx, dir, "git", args...)
	return out, err
}

// Root returns the top level of the work tree containing dir.
func Root(ctx context.Context, cmd moexec.Commander, dir string) (string, error) {
	out, err := git(ctx, cmd, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotRepo
	}
	return strings.TrimSpace(string(out)), nil
}

// GetChanges returns the changed files with line counts, totals and the
// upstream sync state for the checkout containing dir.
func GetChanges(ctx context.Context, cmd moexec.Commander, dir string) (*Changes, error) {
	root, err := Root(ctx, cmd, dir)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, cmd, root, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	files, sync := ParseStatusV2(out)
	if sync.Branch == "(detached)" {
		sync.Branch = ""
	}

	// HEAD-relative counts cover staged and unstaged edits together. A repo
	// with no commits yet has no HEAD to diff against.
	var counts map[string]Counts
	if !bytes.Contains(out, []byte("# branch.oid (initial)")) {
		if ns, err := git(ctx, cmd, root, "diff", "HEAD", "--numstat", "-z"); err == nil {
			counts = ParseNumstat(ns)
		}
	}

	ch := &Changes{Root: root, Sync: sync}
	counted := 0
	for _, f := range files {
		if c, ok := counts[f.Path]; ok {
			f.Added, f.Removed, f.Binary = c.Added, c.Removed, c.Binary
		} else if f.Status == "?" && counted < maxUntrackedCounts {
			counted++
			f.Added, f.Binary = countLines(filepath.Join(root, f.Path))
		}
		ch.Added += f.Added
		ch.Removed += f.Removed
		if len(ch.Files) < maxFiles {
			ch.Files = append(ch.Files, f)
		} else {
			ch.Truncated = true
		}
	}
	return ch, nil
}

// countLines counts newline-terminated lines in a new file, the way git
// would report it as added. Large files are skipped; a NUL byte marks the
// file binary.
func countLines(path string) (lines int, binary bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > maxCountBytes {
		return 0, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return 0, true
	}
	n := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n, false
}

// Tree lists every tracked and untracked (not ignored) file in the checkout
// containing dir, relative to its root, sorted.
func Tree(ctx context.Context, cmd moexec.Commander, dir string) (root string, paths []string, err error) {
	root, err = Root(ctx, cmd, dir)
	if err != nil {
		return "", nil, err
	}
	out, err := git(ctx, cmd, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", nil, err
	}
	seen := make(map[string]bool)
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return root, paths, nil
}

// CurrentBranch returns the branch checked out in dir, or the short commit
// hash when HEAD is detached, or "" outside a repo.
func CurrentBranch(ctx context.Context, cmd moexec.Commander, dir string) string {
	if out, err := git(ctx, cmd, dir, "branch", "--show-current"); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			return b
		}
	}
	if out, err := git(ctx, cmd, dir, "rev-parse", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}
