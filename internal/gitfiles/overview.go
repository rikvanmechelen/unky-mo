package gitfiles

import (
	"context"
	"errors"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// The chat view's Overview tab: a branch's whole change (committed and
// uncommitted, against where it split off the default branch) with every
// file classified as logic or noise and placed in an area, so the browser
// can show how far a change spreads and how much of it needs reading.

// Overview modes: ModeBranch compares the merge base with the default
// branch to the working tree; ModeHead compares HEAD to the working tree
// (the Files panel's Changed list).
const (
	ModeBranch = "branch"
	ModeHead   = "head"
)

// Kind is what a changed file is, for review purposes.
type Kind string

const (
	KindLogic     Kind = "logic"
	KindTest      Kind = "test"
	KindGenerated Kind = "generated"
	KindDocs      Kind = "docs"
	KindFormat    Kind = "format"  // whitespace-only changes
	KindRenamed   Kind = "renamed" // moved without content changes
)

// OverviewFile is one changed path, relative to the repo root. OldPath is
// set for a rename. Status is A, M, D, R, T or ? (untracked).
type OverviewFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
	Kind    Kind   `json:"kind"`
	Area    string `json:"area"`
}

// KindTotal sums the files and changed lines of one Kind.
type KindTotal struct {
	Files int `json:"files"`
	Lines int `json:"lines"`
}

// Overview is a checkout's change for the Overview tab. Mode is the mode
// actually used: a ModeBranch request falls back to ModeHead (Fallback
// set) when there's no default branch to compare with, or HEAD is on it.
// Base is the default branch's name and MergeBase the commit compared
// with, both empty in ModeHead. Rev is the commit compared with in either
// mode (the merge base, or HEAD), empty without commits.
type Overview struct {
	Root      string             `json:"root"`
	Branch    string             `json:"branch"`
	Mode      string             `json:"mode"`
	Fallback  bool               `json:"fallback,omitempty"`
	Base      string             `json:"base,omitempty"`
	MergeBase string             `json:"mergeBase,omitempty"`
	Rev       string             `json:"rev,omitempty"`
	Files     []OverviewFile     `json:"files"`
	Added     int                `json:"added"`
	Removed   int                `json:"removed"`
	Kinds     map[Kind]KindTotal `json:"kinds"`
	Areas     []string           `json:"areas"`
	Truncated bool               `json:"truncated,omitempty"`
}

// GetOverview reads the change of the checkout containing dir in mode
// (ModeBranch or ModeHead).
func GetOverview(ctx context.Context, cmd moexec.Commander, dir, mode string) (*Overview, error) {
	root, err := Root(ctx, cmd, dir)
	if err != nil {
		return nil, err
	}
	o := &Overview{Root: root, Mode: ModeHead, Files: []OverviewFile{}, Kinds: map[Kind]KindTotal{}, Areas: []string{}}
	o.Branch = CurrentBranch(ctx, cmd, root)
	head := gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", "HEAD")

	rev := head
	if mode == ModeBranch && head != "" {
		o.Fallback = true
		if base := defaultBranch(ctx, cmd, root); base != "" {
			mb := gitLine(ctx, cmd, root, "merge-base", "HEAD", base)
			// On the default branch itself, the branch's change is just
			// what's uncommitted: say so rather than diffing against a
			// merge base that is HEAD.
			if mb != "" && !(mb == head && isBranch(o.Branch, base)) {
				o.Mode, o.Fallback, o.Base, o.MergeBase, rev = ModeBranch, false, base, mb, mb
			}
		}
	}

	o.Rev = rev
	var files []OverviewFile
	if rev != "" {
		ns, err := git(ctx, cmd, root, "diff", "-z", "--name-status", "-M", rev, "--")
		if err != nil {
			return nil, err
		}
		counts := map[string]Counts{}
		if out, err := git(ctx, cmd, root, "diff", "-z", "--numstat", "-M", rev, "--"); err == nil {
			counts = ParseNumstat(out)
		}
		// A file with changes that -w doesn't report changed only whitespace.
		var ws map[string]Counts
		if out, err := git(ctx, cmd, root, "diff", "-z", "--numstat", "-M", "-w", rev, "--"); err == nil {
			ws = ParseNumstat(out)
		}
		for _, cf := range ParseNameStatus(ns) {
			c := counts[cf.Path]
			f := OverviewFile{Path: cf.Path, OldPath: cf.OldPath, Status: cf.Status, Added: c.Added, Removed: c.Removed, Binary: c.Binary}
			wsOnly := false
			if ws != nil && !c.Binary && c.Added+c.Removed > 0 {
				w, ok := ws[cf.Path]
				wsOnly = !ok || w.Added+w.Removed == 0
			}
			f.Kind = Classify(f.Path, f.Status, f.Added+f.Removed, wsOnly, readHeader(root, f.Path, f.Status))
			files = append(files, f)
		}
	}

	out, err := git(ctx, cmd, root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	counted := 0
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		f := OverviewFile{Path: p, Status: "?"}
		if counted < maxUntrackedCounts {
			counted++
			f.Added, f.Binary = countLines(filepath.Join(root, p))
		}
		f.Kind = Classify(p, f.Status, f.Added, false, readHeader(root, p, f.Status))
		files = append(files, f)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	areas := map[string]bool{}
	for _, f := range files {
		f.Area = AreaOf(f.Path)
		o.Added += f.Added
		o.Removed += f.Removed
		k := o.Kinds[f.Kind]
		k.Files++
		k.Lines += f.Added + f.Removed
		o.Kinds[f.Kind] = k
		if !areas[f.Area] {
			areas[f.Area] = true
			o.Areas = append(o.Areas, f.Area)
		}
		if len(o.Files) < maxFiles {
			o.Files = append(o.Files, f)
		} else {
			o.Truncated = true
		}
	}
	sort.Strings(o.Areas)
	return o, nil
}

// isBranch reports whether the local branch name is the default branch
// base ("main", or the "origin/main" it tracks).
func isBranch(branch, base string) bool {
	return branch != "" && (branch == base || strings.HasSuffix(base, "/"+branch))
}

const headerBytes = 1024

// readHeader returns the first bytes of a changed file's working copy, for
// spotting a "generated" marker, or nil for a deleted file or one that
// can't be read safely (outside the checkout, through a symlink).
func readHeader(root, rel, status string) []byte {
	if status == "D" {
		return nil
	}
	p, err := Resolve(root, rel)
	if err != nil {
		return nil
	}
	f, err := openNoFollow(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, headerBytes)
	n, _ := io.ReadFull(f, buf)
	return buf[:n]
}

// generatedRe matches Go's convention for generated files
// (https://go.dev/s/generatedcode) and the "@generated" marker other
// generators (protobuf, Facebook's tools, many JS codegens) write, both
// only as a comment line of their own — not a mention inside code, like a
// test fixture's string literal.
var generatedRe = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$|^\s*(?://|#|/?\*+|<!--)\s*@generated\b`)

var lockfiles = map[string]bool{
	"go.sum": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true, "Gemfile.lock": true,
	"Cargo.lock": true, "poetry.lock": true, "composer.lock": true, "uv.lock": true, "bun.lockb": true,
}

// Classify says what a changed file is. lines is its added+removed count,
// wsOnly that its changes are whitespace only, header the start of its
// working copy (nil if unknown). Noise wins over a file's role: a
// whitespace-only test change is "format", a generated test file
// "generated".
func Classify(p, status string, lines int, wsOnly bool, header []byte) Kind {
	base := path.Base(p)
	dirs := "/" + path.Dir(p) + "/"
	switch {
	case lockfiles[base], strings.HasSuffix(base, ".min.js"), strings.HasSuffix(base, ".min.css"),
		strings.HasSuffix(base, ".pb.go"), strings.HasSuffix(base, "_gen.go"),
		strings.Contains(dirs, "/vendor/"), strings.Contains(dirs, "/node_modules/"),
		header != nil && generatedRe.Match(header):
		return KindGenerated
	case status == "R" && lines == 0:
		return KindRenamed
	case wsOnly:
		return KindFormat
	case isTestPath(base, dirs):
		return KindTest
	case isDocPath(base, dirs):
		return KindDocs
	}
	return KindLogic
}

func isTestPath(base, dirs string) bool {
	for _, d := range []string{"/test/", "/tests/", "/spec/", "/__tests__/", "/testdata/"} {
		if strings.Contains(dirs, d) {
			return true
		}
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "_test.rb") ||
		strings.HasSuffix(base, "_test.py") || (strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) {
		return true
	}
	// foo.test.js, foo.spec.tsx, …
	parts := strings.Split(base, ".")
	if len(parts) >= 3 {
		switch parts[len(parts)-2] {
		case "test", "spec":
			return true
		}
	}
	return false
}

func isDocPath(base, dirs string) bool {
	switch strings.ToLower(path.Ext(base)) {
	case ".md", ".mdx", ".rst", ".adoc", ".txt":
		return true
	}
	return strings.Contains(dirs, "/docs/") || strings.Contains(dirs, "/doc/")
}

// containerDirs hold modules rather than being one: an area under them is
// two segments deep ("internal/web"), elsewhere one ("app").
var containerDirs = map[string]bool{
	"internal": true, "cmd": true, "pkg": true, "app": true, "lib": true, "src": true,
	"packages": true, "apps": true, "services": true,
}

// AreaOf names the part of the repo a path belongs to, for measuring how
// far a change spreads: "internal/web" for internal/web/static/x.js, "docs"
// for docs/plans/y.md, "." for a file at the root.
func AreaOf(p string) string {
	segs := strings.Split(p, "/")
	switch {
	case len(segs) == 1:
		return "."
	case containerDirs[segs[0]] && len(segs) > 2:
		return segs[0] + "/" + segs[1]
	default:
		return segs[0]
	}
}

// ReadAt reads rel as it is in commit rev of the checkout at root. rev
// must be a full commit id (ErrUnknownCommit otherwise), so it's never read
// as a ref name or an option. A path that isn't in that commit comes back
// with Exists false. rel is a committed path (maybe one deleted since), so
// it only has to be a clean relative path.
func ReadAt(ctx context.Context, cmd moexec.Commander, root, rev, rel string) (*Content, error) {
	if _, err := Resolve(root, rel); err != nil {
		return nil, err
	}
	if !hashRe.MatchString(rev) || gitLine(ctx, cmd, root, "cat-file", "-t", rev) != "commit" {
		return nil, ErrUnknownCommit
	}
	c, err := readBlob(ctx, cmd, root, rev+":"+rel, rel)
	var be *blobError
	if errors.As(err, &be) && notInHEAD(be.stderr) {
		return &Content{Path: rel}, nil
	}
	return c, err
}
