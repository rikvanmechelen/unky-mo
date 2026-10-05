package gitfiles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// The chat view's Overview tab: a branch's whole change (committed and
// uncommitted, against where it split off the default branch) with every
// file classified as logic or noise and placed in an area, so the browser
// can show how far a change spreads and how much of it needs reading.

// Overview modes: ModeBranch compares the merge base with the default
// branch to the working tree; ModeHead compares HEAD to the working tree
// (the Files panel's Changed list); ModeCommits compares two commits.
const (
	ModeBranch = "branch"
	ModeHead   = "head"
	// ModeCommits is a selection of consecutive commits (GetOverviewRange).
	ModeCommits = "commits"
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
// mode (the merge base, or HEAD), empty without commits. Head is set for a
// branch that isn't checked out (GetOverviewAt): the commit compared to,
// instead of a working tree.
type Overview struct {
	Root      string `json:"root"`
	Branch    string `json:"branch"`
	Mode      string `json:"mode"`
	Fallback  bool   `json:"fallback,omitempty"`
	Base      string `json:"base,omitempty"`
	MergeBase string `json:"mergeBase,omitempty"`
	Rev       string `json:"rev,omitempty"`
	Head      string `json:"head,omitempty"`
	// Worktree marks a ModeCommits overview that ends at the working tree
	// (a Git log selection that includes the uncommitted changes).
	Worktree bool `json:"worktree,omitempty"`
	// BaseFetched is when origin's copy of Base was last updated (unix
	// seconds), 0 when unknown or Base isn't origin's: an old fetch means an
	// old merge base, and an overview that shows work already merged.
	BaseFetched int64              `json:"baseFetched,omitempty"`
	Files       []OverviewFile     `json:"files"`
	Added       int                `json:"added"`
	Removed     int                `json:"removed"`
	Kinds       map[Kind]KindTotal `json:"kinds"`
	Areas       []string           `json:"areas"`
	Truncated   bool               `json:"truncated,omitempty"`
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
				o.BaseFetched = BaseFetched(ctx, cmd, root, base)
			}
		}
	}

	o.Rev = rev
	return overviewToWorktree(ctx, cmd, o, root, rev)
}

// overviewToWorktree fills o with the files changed from commit rev (none
// without commits) to the working tree, untracked files included.
func overviewToWorktree(ctx context.Context, cmd moexec.Commander, o *Overview, root, rev string) (*Overview, error) {
	var files []OverviewFile
	if rev != "" {
		var err error
		files, err = diffFiles(ctx, cmd, root, []string{rev}, func(p, status string) []byte { return readHeader(root, p, status) })
		if err != nil {
			return nil, err
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
	finish(o, files)
	return o, nil
}

// diffFiles lists and classifies the files changed between revs[0] and
// the working tree, or between revs[0] and revs[1]. header reads the start
// of a file's new version, for spotting a "generated" marker.
func diffFiles(ctx context.Context, cmd moexec.Commander, root string, revs []string, header func(path, status string) []byte) ([]OverviewFile, error) {
	diff := func(extra ...string) ([]byte, error) {
		args := append([]string{"diff", "-z", "-M"}, extra...)
		return git(ctx, cmd, root, append(append(args, revs...), "--")...)
	}
	ns, err := diff("--name-status")
	if err != nil {
		return nil, err
	}
	counts := map[string]Counts{}
	if out, err := diff("--numstat"); err == nil {
		counts = ParseNumstat(out)
	}
	// A file with changes that -w doesn't report changed only whitespace.
	var ws map[string]Counts
	if out, err := diff("--numstat", "-w"); err == nil {
		ws = ParseNumstat(out)
	}
	var files []OverviewFile
	for _, cf := range ParseNameStatus(ns) {
		c := counts[cf.Path]
		f := OverviewFile{Path: cf.Path, OldPath: cf.OldPath, Status: cf.Status, Added: c.Added, Removed: c.Removed, Binary: c.Binary}
		wsOnly := false
		if ws != nil && !c.Binary && c.Added+c.Removed > 0 {
			w, ok := ws[cf.Path]
			wsOnly = !ok || w.Added+w.Removed == 0
		}
		f.Kind = Classify(f.Path, f.Status, f.Added+f.Removed, wsOnly, header(f.Path, f.Status))
		files = append(files, f)
	}
	return files, nil
}

// finish sorts the files and fills in areas, totals and kinds.
func finish(o *Overview, files []OverviewFile) {
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
}

// maxBlobHeaders caps the head-commit blobs read for "generated" markers
// in a ref-mode overview (one git call each).
const maxBlobHeaders = 300

// GetOverviewAt reads the change of a branch that isn't checked out: from
// its merge base with base to head, a full commit id (from ResolveBranch or
// ResolvePR). base is a branch name such as a PR's "origin/develop"; empty,
// or a ref that doesn't exist, means the default branch. Nothing is read
// from a working tree. Head is set; with no base, or head on it, the
// overview is empty and Fallback set.
func GetOverviewAt(ctx context.Context, cmd moexec.Commander, root, branch, head, base string) (*Overview, error) {
	if !hashRe.MatchString(head) || gitLine(ctx, cmd, root, "cat-file", "-t", head) != "commit" {
		return nil, ErrUnknownCommit
	}
	o := &Overview{Root: root, Branch: branch, Mode: ModeBranch, Head: head, Files: []OverviewFile{}, Kinds: map[Kind]KindTotal{}, Areas: []string{}}
	if base == "" || strings.HasPrefix(base, "-") || gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", base+"^{commit}") == "" {
		base = defaultBranch(ctx, cmd, root)
	}
	mb := ""
	if base != "" {
		mb = gitLine(ctx, cmd, root, "merge-base", head, base)
	}
	if mb == "" || mb == head {
		o.Fallback = true
		return o, nil
	}
	o.Base, o.MergeBase, o.Rev = base, mb, mb
	return overviewBetween(ctx, cmd, o, root, mb, head)
}

// overviewBetween fills o with the files changed from commit base to commit
// head, reading generated-file headers from head.
func overviewBetween(ctx context.Context, cmd moexec.Commander, o *Overview, root, base, head string) (*Overview, error) {
	read := 0
	files, err := diffFiles(ctx, cmd, root, []string{base, head}, func(p, status string) []byte {
		if status == "D" || read == maxBlobHeaders {
			return nil
		}
		read++
		c, err := readBlob(ctx, cmd, root, head+":"+p, p)
		if err != nil || c.Binary || c.TooLarge {
			return nil
		}
		if len(c.Text) > headerBytes {
			return []byte(c.Text[:headerBytes])
		}
		return []byte(c.Text)
	})
	if err != nil {
		return nil, err
	}
	finish(o, files)
	return o, nil
}

// TreeAt lists every file in commit head (a full commit id), sorted.
func TreeAt(ctx context.Context, cmd moexec.Commander, root, head string) ([]string, error) {
	if !hashRe.MatchString(head) {
		return nil, ErrUnknownCommit
	}
	out, err := git(ctx, cmd, root, "ls-tree", "-r", "-z", "--name-only", "--full-tree", head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// baseSpec is the refspec that updates origin's copy of base (a branch
// name; empty means the default branch, if it's origin's), or none for a
// base that isn't a valid branch name or isn't on origin.
func baseSpec(ctx context.Context, cmd moexec.Commander, root, base string) []string {
	if base == "" {
		base = strings.TrimPrefix(defaultBranch(ctx, cmd, root), "origin/")
		if base == "" || gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+base) == "" {
			return nil
		}
	}
	if strings.HasPrefix(base, "-") {
		return nil
	}
	if _, _, err := cmd.Output(ctx, root, "git", "check-ref-format", "--branch", base); err != nil {
		return nil
	}
	return []string{"+refs/heads/" + base + ":refs/remotes/origin/" + base}
}

// BaseFetched returns when origin's copy of base ("origin/main") was last
// updated, from its reflog, in unix seconds; 0 when base isn't origin's or
// there's no reflog.
func BaseFetched(ctx context.Context, cmd moexec.Commander, root, base string) int64 {
	if !strings.HasPrefix(base, "origin/") {
		return 0
	}
	line := gitLine(ctx, cmd, root, "reflog", "--date=unix", "-n1", "refs/remotes/"+base, "--")
	_, rest, ok := strings.Cut(line, "@{")
	if !ok {
		return 0
	}
	stamp, _, _ := strings.Cut(rest, "}")
	t, _ := strconv.ParseInt(stamp, 10, 64)
	return t
}

// FetchBase updates origin's copy of base ("origin/main") in the repo at
// root, and nothing else.
func FetchBase(ctx context.Context, cmd moexec.Commander, root, base string) error {
	branch, ok := strings.CutPrefix(base, "origin/")
	if !ok {
		return fmt.Errorf("%s isn't a branch of origin", base)
	}
	specs := baseSpec(ctx, cmd, root, branch)
	if specs == nil {
		return ErrBadBranch
	}
	_, stderr, err := cmd.Output(ctx, root, "git", append([]string{"fetch", "--quiet", "--no-tags", "origin"}, specs...)...)
	if err != nil {
		return fmt.Errorf("git fetch: %s", strings.TrimSpace(string(stderr)))
	}
	return nil
}

// ErrBadBranch is returned for a name that isn't a valid branch name.
var ErrBadBranch = errors.New("not a valid branch name")

// ErrNoBranch is returned when a branch doesn't exist (locally, or on
// origin for a remote lookup).
var ErrNoBranch = errors.New("no such branch")

// ResolveBranch returns the commit branch points at in the repo at root:
// the local branch, or with remote, origin's copy, fetched first (only
// that branch, into refs/remotes/origin/<branch>). The name is checked
// with git check-ref-format before git sees it as a ref.
func ResolveBranch(ctx context.Context, cmd moexec.Commander, root, branch string, remote bool) (string, error) {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return "", ErrBadBranch
	}
	if _, _, err := cmd.Output(ctx, root, "git", "check-ref-format", "--branch", branch); err != nil {
		return "", ErrBadBranch
	}
	ref := "refs/heads/" + branch
	if remote {
		ref = "refs/remotes/origin/" + branch
		// The default branch comes along: a stale one would put a stale
		// merge base under the overview.
		specs := append([]string{"+refs/heads/" + branch + ":" + ref}, baseSpec(ctx, cmd, root, "")...)
		if _, stderr, err := cmd.Output(ctx, root, "git", append([]string{"fetch", "--quiet", "--no-tags", "origin"}, specs...)...); err != nil {
			// Offline or gone upstream: fall back to what was fetched before.
			if gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", ref+"^{commit}") == "" {
				return "", fmt.Errorf("%w: git fetch: %s", ErrNoBranch, strings.TrimSpace(string(stderr)))
			}
		}
	}
	head := gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if head == "" {
		return "", ErrNoBranch
	}
	return head, nil
}

// ResolvePR fetches pull request n's head from origin (GitHub's
// refs/pull/<n>/head, which also covers PRs from forks) into
// refs/remotes/origin/pr/<n>, together with its base branch (so the merge
// base is current), and returns the head commit. If the fetch fails
// (offline), an earlier fetch is used.
func ResolvePR(ctx context.Context, cmd moexec.Commander, root string, n int, base string) (string, error) {
	if n < 1 {
		return "", ErrNoBranch
	}
	ref := fmt.Sprintf("refs/remotes/origin/pr/%d", n)
	specs := append([]string{fmt.Sprintf("+refs/pull/%d/head:%s", n, ref)}, baseSpec(ctx, cmd, root, base)...)
	_, stderr, err := cmd.Output(ctx, root, "git", append([]string{"fetch", "--quiet", "--no-tags", "origin"}, specs...)...)
	head := gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if head == "" {
		if err != nil {
			return "", fmt.Errorf("%w: git fetch: %s", ErrNoBranch, strings.TrimSpace(string(stderr)))
		}
		return "", ErrNoBranch
	}
	return head, nil
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

// IsGenerated reports whether a file's text starts like a generated file's
// (a marker comment in its first 4 KB).
func IsGenerated(text string) bool {
	if len(text) > 4096 {
		text = text[:4096]
	}
	return generatedRe.MatchString(text)
}

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
	// Test dirs, including Gradle's src/test and src/androidTest and
	// SwiftPM/Xcode's Tests (and FooTests/FooUITests targets).
	for _, d := range []string{"/test/", "/tests/", "/spec/", "/__tests__/", "/testdata/", "/androidTest/", "/Tests/"} {
		if strings.Contains(dirs, d) {
			return true
		}
	}
	for _, seg := range strings.Split(strings.Trim(dirs, "/"), "/") {
		if strings.HasSuffix(seg, "Tests") && len(seg) > len("Tests") && seg[0] >= 'A' && seg[0] <= 'Z' {
			return true
		}
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_spec.rb") || strings.HasSuffix(base, "_test.rb") ||
		strings.HasSuffix(base, "_test.py") || (strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py")) ||
		base == "conftest.py" ||
		strings.HasSuffix(base, "Tests.swift") || strings.HasSuffix(base, "Test.swift") ||
		strings.HasSuffix(base, "Test.kt") || strings.HasSuffix(base, "Tests.kt") || strings.HasSuffix(base, "Test.java") {
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
