// Package review analyzes a branch's change for the chat view's Overview
// tab, beyond line counts: which package imports appear or disappear (and
// whether they break the repo's layer rules), and which of the code's
// contracts change — exported Go API, HTTP routes, CLI flags, config keys,
// env vars, dependencies and migrations. Everything is deterministic: git
// plus go/parser plus regular expressions, no language servers.
package review

import (
	"context"
	"path"
	"sort"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// maxAnalyzed caps the changed files read and parsed per analysis.
const maxAnalyzed = 300

// Ops of a Change or Edge.
const (
	OpAdded   = "+"
	OpRemoved = "-"
	OpChanged = "~"
)

// Change is one entry of the contract surface: Name (e.g. "ops.Launch",
// "GET /api/state", "OP_TOKEN") was added, removed or changed in Path (the
// changed file's current path), at Line of the version it's in.
type Change struct {
	Op     string `json:"op"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
}

// Surface is everything a change does to the code's edges.
type Surface struct {
	Exports    []Change `json:"exports"`
	Routes     []Change `json:"routes"`
	Flags      []Change `json:"flags"`
	Config     []Change `json:"config"`
	Env        []Change `json:"env"`
	Deps       []Change `json:"deps"`
	Migrations []Change `json:"migrations"`
}

// EdgeFile is a file (current path) that adds or drops an import, at the
// import's line in that version.
type EdgeFile struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Edge is an import between two packages of the module, named by their
// directories relative to the repo root ("." for the root package). Op is
// OpAdded or OpRemoved for a changed edge and "" for an existing one.
// Violation says which layer rule an added edge breaks; Fixed which rule a
// removed edge used to break.
type Edge struct {
	From      string     `json:"from"`
	To        string     `json:"to"`
	Op        string     `json:"op,omitempty"`
	Files     []EdgeFile `json:"files,omitempty"`
	Violation string     `json:"violation,omitempty"`
	Fixed     string     `json:"fixed,omitempty"`
}

// Package is a Go package the change touches: "added" (all its files are
// new), "removed" (all deleted) or "changed".
type Package struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Rules describes the repo's layer rules file: whether it exists, and why
// it couldn't be used if it's broken.
type Rules struct {
	Path   string `json:"path"`
	Found  bool   `json:"found"`
	Error  string `json:"error,omitempty"`
	Layers int    `json:"layers"`
}

// Analysis is the architecture delta and contract surface of a change.
type Analysis struct {
	Module     string    `json:"module,omitempty"`
	Packages   []Package `json:"packages"`
	Edges      []Edge    `json:"edges"`
	Existing   []Edge    `json:"existing"`
	Violations int       `json:"violations"`
	Rules      Rules     `json:"rules"`
	Surface    Surface   `json:"surface"`
	Truncated  bool      `json:"truncated,omitempty"`
}

// file is one changed file with its two versions' text (nil where that
// version doesn't exist or isn't text).
type file struct {
	gitfiles.OverviewFile
	before, after *string
}

// oldPath is where the file was in the base version.
func (f *file) oldPath() string {
	if f.OldPath != "" {
		return f.OldPath
	}
	return f.Path
}

// analyzed reports whether a changed file's contents matter to the
// analysis: code and the dependency manifests, not docs or binaries.
func analyzed(p string) bool {
	switch path.Base(p) {
	case "go.mod", "package.json", "Gemfile":
		return true
	}
	switch path.Ext(p) {
	case ".go", ".rb", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".py":
		return true
	}
	return false
}

// repo reads the checkout and its base revision for one analysis. With
// head set (a branch that isn't checked out), the "after" side is that
// commit instead of the working tree.
type repo struct {
	ctx  context.Context
	cmd  moexec.Commander
	root string
	rev  string
	head string
}

func (r *repo) readBefore(p string) *string {
	if r.rev == "" {
		return nil
	}
	c, err := gitfiles.ReadAt(r.ctx, r.cmd, r.root, r.rev, p)
	return text(c, err)
}

func (r *repo) readAfter(p string) *string {
	if r.head != "" {
		c, err := gitfiles.ReadAt(r.ctx, r.cmd, r.root, r.head, p)
		return text(c, err)
	}
	c, err := gitfiles.ReadFile(r.root, p)
	return text(c, err)
}

func text(c *gitfiles.Content, err error) *string {
	if err != nil || c == nil || !c.Exists || c.Binary || c.TooLarge {
		return nil
	}
	return &c.Text
}

// lsFiles lists the checkout's tracked and untracked (not ignored) files,
// or the head commit's files.
func (r *repo) lsFiles() []string {
	if r.head != "" {
		paths, _ := gitfiles.TreeAt(r.ctx, r.cmd, r.root, r.head)
		return paths
	}
	out, _, err := r.cmd.Output(r.ctx, r.root, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// inBase reports whether literal occurs anywhere in the base version's
// tracked files; inWorktree whether it does in the working tree's tracked
// and untracked files. They tell a name that's new to the repo from one
// that only moved into a changed file.
func (r *repo) inBase(literal string) bool {
	if r.rev == "" {
		return false
	}
	_, _, err := r.cmd.Output(r.ctx, r.root, "git", "grep", "-q", "-F", "-e", literal, r.rev, "--")
	return err == nil
}

func (r *repo) inWorktree(literal string) bool {
	if r.head != "" {
		_, _, err := r.cmd.Output(r.ctx, r.root, "git", "grep", "-q", "-F", "-e", literal, r.head, "--")
		return err == nil
	}
	_, _, err := r.cmd.Output(r.ctx, r.root, "git", "grep", "-q", "-F", "--untracked", "-e", literal, "--")
	return err == nil
}

// Analyze reads the changed files of o (an overview from
// gitfiles.GetOverview) and works out their architecture delta and
// contract surface.
func Analyze(ctx context.Context, cmd moexec.Commander, o *gitfiles.Overview) (*Analysis, error) {
	r := &repo{ctx: ctx, cmd: cmd, root: o.Root, rev: o.Rev, head: o.Head}
	a := &Analysis{Packages: []Package{}, Edges: []Edge{}, Existing: []Edge{}}

	var files []*file
	for _, of := range o.Files {
		if !analyzed(of.Path) && !(of.OldPath != "" && analyzed(of.OldPath)) {
			continue
		}
		if len(files) == maxAnalyzed {
			a.Truncated = true
			break
		}
		f := &file{OverviewFile: of}
		if of.Status != "A" && of.Status != "?" {
			f.before = r.readBefore(f.oldPath())
		}
		if of.Status != "D" {
			f.after = r.readAfter(of.Path)
		}
		files = append(files, f)
	}

	a.Module = modulePath(r)
	rules := loadRules(r)
	a.Rules = rules.info
	if a.Module != "" {
		goArchitecture(r, a, files, rules)
	}
	a.Surface = surface(r, a.Module != "", o.Files, files)
	for _, e := range a.Edges {
		if e.Violation != "" {
			a.Violations++
		}
	}
	return a, nil
}

// modulePath is the module named by the root go.mod, or "".
func modulePath(r *repo) string {
	src := r.readAfter("go.mod")
	if src == nil {
		return ""
	}
	for _, line := range strings.Split(*src, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

func sortChanges(cs []Change) []Change {
	if cs == nil {
		return []Change{}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Name != cs[j].Name {
			return cs[i].Name < cs[j].Name
		}
		return cs[i].Op < cs[j].Op
	})
	return cs
}
