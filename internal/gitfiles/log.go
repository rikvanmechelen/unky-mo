package gitfiles

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// The Files panel's Graph tab: a checkout's commit history with parents and
// refs, for the browser to lay out as a graph, plus one commit's changed
// files and their before/after contents for a commit diff tab.

const (
	maxLogCommits = 300  // commits per log; more sets Truncated
	maxUnpushed   = 1000 // unpushed commits looked up for marking
)

// Log scopes: ScopeBranch is HEAD plus its upstream and the default branch
// (where this branch split off and what landed there since); ScopeAll is
// every branch, remote branch and tag.
const (
	ScopeBranch = "branch"
	ScopeAll    = "all"
)

// ErrUnknownCommit is returned for a hash that isn't a commit in the repo.
var ErrUnknownCommit = errors.New("no such commit")

// Ref is one decoration on a commit. Kind is "head" (a detached HEAD),
// "branch", "remote" or "tag"; Head marks the branch HEAD points at.
type Ref struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Head bool   `json:"head,omitempty"`
}

// Commit is one log entry. Time is the committer time in Unix seconds (a
// rebased commit shows when it was rebased, in line with its neighbours).
type Commit struct {
	Hash     string   `json:"hash"`
	Parents  []string `json:"parents"`
	Refs     []Ref    `json:"refs,omitempty"`
	Author   string   `json:"author"`
	Time     int64    `json:"time"`
	Subject  string   `json:"subject"`
	Unpushed bool     `json:"unpushed,omitempty"`
}

// Log is what the Graph tab shows: commits newest first in topological
// order (so a commit always comes before its parents). Base is the default
// branch shown alongside HEAD in ScopeBranch, "" when there is none.
type Log struct {
	Root      string   `json:"root"`
	Scope     string   `json:"scope"`
	Head      string   `json:"head"`
	Branch    string   `json:"branch"`
	Upstream  string   `json:"upstream,omitempty"`
	Base      string   `json:"base,omitempty"`
	Commits   []Commit `json:"commits"`
	Truncated bool     `json:"truncated,omitempty"`
}

// logFormat separates fields with US (0x1f); git log -z ends each commit
// with a NUL.
const logFormat = "%H%x1f%P%x1f%D%x1f%an%x1f%ct%x1f%s"

// ParseLog parses `git log -z --decorate=full --format=<logFormat>` output.
func ParseLog(out []byte) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(string(out), "\x00") {
		rec = strings.TrimPrefix(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) != 6 || f[0] == "" {
			continue
		}
		t, _ := strconv.ParseInt(f[4], 10, 64)
		commits = append(commits, Commit{
			Hash:    f[0],
			Parents: strings.Fields(f[1]),
			Refs:    ParseDecorations(f[2]),
			Author:  f[3],
			Time:    t,
			Subject: f[5],
		})
	}
	return commits
}

// ParseDecorations parses a full-name %D decoration list, e.g.
// "HEAD -> refs/heads/main, refs/remotes/origin/main, tag: refs/tags/v1".
// Remote HEAD symrefs (refs/remotes/origin/HEAD) are left out as noise.
func ParseDecorations(d string) []Ref {
	var refs []Ref
	for _, part := range strings.Split(d, ", ") {
		part = strings.TrimSpace(part)
		head := false
		if rest, ok := strings.CutPrefix(part, "HEAD -> "); ok {
			part, head = rest, true
		}
		part = strings.TrimPrefix(part, "tag: ")
		switch {
		case part == "":
		case part == "HEAD":
			refs = append(refs, Ref{Name: "HEAD", Kind: "head"})
		case strings.HasPrefix(part, "refs/heads/"):
			refs = append(refs, Ref{Name: strings.TrimPrefix(part, "refs/heads/"), Kind: "branch", Head: head})
		case strings.HasPrefix(part, "refs/remotes/"):
			if !strings.HasSuffix(part, "/HEAD") {
				refs = append(refs, Ref{Name: strings.TrimPrefix(part, "refs/remotes/"), Kind: "remote"})
			}
		case strings.HasPrefix(part, "refs/tags/"):
			refs = append(refs, Ref{Name: strings.TrimPrefix(part, "refs/tags/"), Kind: "tag"})
		}
	}
	return refs
}

// gitLine runs git and returns its trimmed stdout, or "" if it failed.
func gitLine(ctx context.Context, cmd moexec.Commander, dir string, args ...string) string {
	out, err := git(ctx, cmd, dir, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// defaultBranch guesses the branch this checkout's work merges into: the
// remote's HEAD (origin/main), else a local main or master.
func defaultBranch(ctx context.Context, cmd moexec.Commander, root string) string {
	if b := gitLine(ctx, cmd, root, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); b != "" {
		return b
	}
	for _, b := range []string{"main", "master"} {
		if gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", "refs/heads/"+b) != "" {
			return b
		}
	}
	return ""
}

// GetLog reads the commit graph of the checkout containing dir.
func GetLog(ctx context.Context, cmd moexec.Commander, dir, scope string) (*Log, error) {
	root, err := Root(ctx, cmd, dir)
	if err != nil {
		return nil, err
	}
	if scope != ScopeAll {
		scope = ScopeBranch
	}
	l := &Log{Root: root, Scope: scope, Commits: []Commit{}}
	l.Head = gitLine(ctx, cmd, root, "rev-parse", "--verify", "-q", "HEAD")
	if l.Head == "" {
		return l, nil // no commits yet
	}
	l.Branch = gitLine(ctx, cmd, root, "branch", "--show-current")
	l.Upstream = gitLine(ctx, cmd, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")

	args := []string{"log", "-z", "--topo-order", "--decorate=full", "--format=" + logFormat, "-n", strconv.Itoa(maxLogCommits + 1)}
	if scope == ScopeAll {
		args = append(args, "--branches", "--remotes", "--tags", "HEAD")
	} else {
		args = append(args, "HEAD")
		if l.Upstream != "" {
			args = append(args, l.Upstream)
		}
		l.Base = defaultBranch(ctx, cmd, root)
		if l.Base != "" && l.Base != l.Branch && l.Base != l.Upstream {
			args = append(args, l.Base)
		}
	}
	out, err := git(ctx, cmd, root, append(args, "--")...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	l.Commits = ParseLog(out)
	if len(l.Commits) > maxLogCommits {
		l.Commits, l.Truncated = l.Commits[:maxLogCommits], true
	}
	if l.Commits == nil {
		l.Commits = []Commit{}
	}

	// Unpushed: on HEAD but not its upstream, or with no upstream, not on
	// any remote branch. A repo without remotes has nothing to push to.
	var not []string
	if l.Upstream != "" {
		not = []string{l.Upstream}
	} else if gitLine(ctx, cmd, root, "for-each-ref", "--count=1", "refs/remotes") != "" {
		not = []string{"--remotes"}
	}
	if not != nil {
		args := append([]string{"rev-list", "-n", strconv.Itoa(maxUnpushed), "HEAD", "--not"}, not...)
		if out, err := git(ctx, cmd, root, append(args, "--")...); err == nil {
			unpushed := make(map[string]bool)
			for _, h := range strings.Fields(string(out)) {
				unpushed[h] = true
			}
			for i := range l.Commits {
				l.Commits[i].Unpushed = unpushed[l.Commits[i].Hash]
			}
		}
	}
	return l, nil
}

// CommitFile is one path a commit changed. OldPath is set for a rename or
// copy. Status is A, M, D, R, C or T (type change).
type CommitFile struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
}

// CommitDetail is one commit with its full message and changed files,
// compared with its first parent (so a merge shows what it brought in).
type CommitDetail struct {
	Hash      string       `json:"hash"`
	Parents   []string     `json:"parents"`
	Author    string       `json:"author"`
	Email     string       `json:"email"`
	Time      int64        `json:"time"`
	Message   string       `json:"message"`
	Files     []CommitFile `json:"files"`
	Truncated bool         `json:"truncated,omitempty"`
}

// ParseNameStatus parses `git diff-tree --name-status -z` output. Renames
// and copies ("R100", "C75") carry the old path, then the new one.
func ParseNameStatus(out []byte) []CommitFile {
	var files []CommitFile
	recs := strings.Split(string(out), "\x00")
	for i := 0; i+1 < len(recs); i++ {
		st := recs[i]
		if st == "" {
			continue
		}
		f := CommitFile{Status: st[:1], Path: recs[i+1]}
		i++
		if (f.Status == "R" || f.Status == "C") && i+1 < len(recs) {
			f.OldPath, f.Path = f.Path, recs[i+1]
			i++
		}
		files = append(files, f)
	}
	return files
}

var hashRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// commitParents checks that hash names a commit (a full object id, so
// it's never read as a ref name or an option) and returns its parents.
func commitParents(ctx context.Context, cmd moexec.Commander, root, hash string) ([]string, error) {
	if !hashRe.MatchString(hash) {
		return nil, ErrUnknownCommit
	}
	if gitLine(ctx, cmd, root, "cat-file", "-t", hash) != "commit" {
		return nil, ErrUnknownCommit
	}
	out, err := git(ctx, cmd, root, "rev-list", "--parents", "-n", "1", hash, "--")
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return nil, ErrUnknownCommit
	}
	return f[1:], nil
}

// diffTreeArgs compares hash with its first parent, or with the empty
// tree for a root commit.
func diffTreeArgs(hash string, parents []string, format string) []string {
	args := []string{"diff-tree", "-r", "-z", "-M", "--no-commit-id", format}
	if len(parents) == 0 {
		return append(args, "--root", hash, "--")
	}
	return append(args, parents[0], hash, "--")
}

// GetCommit reads one commit of the checkout at root: its message and the
// files it changed, with line counts.
func GetCommit(ctx context.Context, cmd moexec.Commander, root, hash string) (*CommitDetail, error) {
	parents, err := commitParents(ctx, cmd, root, hash)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, cmd, root, "show", "-s", "--format=%an%x1f%ae%x1f%at%x1f%B", hash, "--")
	if err != nil {
		return nil, fmt.Errorf("git show: %w", err)
	}
	f := strings.SplitN(string(out), "\x1f", 4)
	if len(f) != 4 {
		return nil, fmt.Errorf("git show %s: unexpected output", hash)
	}
	t, _ := strconv.ParseInt(f[2], 10, 64)
	d := &CommitDetail{Hash: hash, Parents: parents, Author: f[0], Email: f[1], Time: t, Message: strings.TrimRight(f[3], "\n")}

	ns, err := git(ctx, cmd, root, diffTreeArgs(hash, parents, "--name-status")...)
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %w", err)
	}
	counts := map[string]Counts{}
	if num, err := git(ctx, cmd, root, diffTreeArgs(hash, parents, "--numstat")...); err == nil {
		counts = ParseNumstat(num)
	}
	d.Files = []CommitFile{}
	for _, cf := range ParseNameStatus(ns) {
		if len(d.Files) == maxFiles {
			d.Truncated = true
			break
		}
		c := counts[cf.Path]
		cf.Added, cf.Removed, cf.Binary = c.Added, c.Removed, c.Binary
		d.Files = append(d.Files, cf)
	}
	return d, nil
}

// CommitFileDiff is one changed file of a commit: its version in the first
// parent (Before, under OldPath for a rename) and in the commit (After).
// A side where the file doesn't exist has Exists false.
type CommitFileDiff struct {
	Hash    string   `json:"hash"`
	Path    string   `json:"path"`
	OldPath string   `json:"oldPath,omitempty"`
	Status  string   `json:"status"`
	Before  *Content `json:"before"`
	After   *Content `json:"after"`
}

// ErrNotInCommit is returned for a path the commit didn't change.
var ErrNotInCommit = errors.New("path not changed in this commit")

// GetCommitFile reads both versions of a path the commit changed. The path
// must be one of the commit's changed files: they're read straight from
// git's object store (never the working tree), so no symlink can redirect
// them, and the listing keeps the browser to what the commit touched.
func GetCommitFile(ctx context.Context, cmd moexec.Commander, root, hash, path string) (*CommitFileDiff, error) {
	parents, err := commitParents(ctx, cmd, root, hash)
	if err != nil {
		return nil, err
	}
	ns, err := git(ctx, cmd, root, diffTreeArgs(hash, parents, "--name-status")...)
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %w", err)
	}
	var file *CommitFile
	for _, cf := range ParseNameStatus(ns) {
		if cf.Path == path {
			file = &cf
			break
		}
	}
	if file == nil {
		return nil, ErrNotInCommit
	}
	d := &CommitFileDiff{Hash: hash, Path: file.Path, OldPath: file.OldPath, Status: file.Status}
	oldPath := file.Path
	if file.OldPath != "" {
		oldPath = file.OldPath
	}
	if file.Status == "A" || len(parents) == 0 {
		d.Before = &Content{Path: oldPath}
	} else if d.Before, err = readBlob(ctx, cmd, root, parents[0]+":"+oldPath, oldPath); err != nil {
		return nil, err
	}
	if file.Status == "D" {
		d.After = &Content{Path: file.Path}
	} else if d.After, err = readBlob(ctx, cmd, root, hash+":"+file.Path, file.Path); err != nil {
		return nil, err
	}
	return d, nil
}

// readBlob reads one blob by spec ("<rev>:<path>"), checking its size
// first so a huge blob is never read into memory.
func readBlob(ctx context.Context, cmd moexec.Commander, root, spec, rel string) (*Content, error) {
	out, stderr, err := cmd.Output(ctx, root, "git", "cat-file", "-s", spec)
	if err != nil {
		return nil, &blobError{spec: spec, err: err, stderr: strings.TrimSpace(string(stderr))}
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

// blobError is a failed `git cat-file -s`, keeping git's stderr so callers
// can tell a missing path from git failing.
type blobError struct {
	spec, stderr string
	err          error
}

func (e *blobError) Error() string {
	return fmt.Sprintf("git cat-file -s %s: %v: %s", e.spec, e.err, e.stderr)
}
