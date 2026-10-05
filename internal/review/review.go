// Package review analyzes a branch's change for the chat view's Overview
// tab, beyond line counts: which package imports appear or disappear (and
// whether they break the repo's layer rules), and which of the code's
// contracts change — exported Go API, HTTP routes, CLI flags, config keys,
// env vars, dependencies and migrations. Everything is deterministic: git
// plus go/parser plus regular expressions, no language servers.
package review

import (
	"context"
	"fmt"
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
	// Permissions: Android manifest permissions and exported components,
	// iOS privacy usage keys and entitlements.
	Permissions []Change `json:"permissions"`
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
	Lang      string     `json:"lang,omitempty"`
	Approx    bool       `json:"approx,omitempty"` // inferred from names, not an import
}

// Package is a unit the change touches (a Go package, a Rails layer, …):
// "added" (all its files are new), "removed" (all deleted) or "changed".
// Lang is the language that analyzed it.
type Package struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Lang   string `json:"lang"`
}

// LangInfo is a language found in the repo: whether its references are
// exact (imports) or inferred from names, and how many units it touched.
type LangInfo struct {
	Name  string `json:"name"`
	Exact bool   `json:"exact"`
	Units int    `json:"units"`
}

// Rules describes the rules that applied: the repo's rules file (whether it
// exists, why it couldn't be used if it's broken) and the presets.
type Rules struct {
	Path   string `json:"path"`
	Found  bool   `json:"found"`
	Error  string `json:"error,omitempty"`
	Layers int    `json:"layers"` // the file's own layers
	// Presets are the built-in rule sets that applied: chosen by the file's
	// presets = [...], or (AutoPresets) by detecting the stack.
	Presets     []string `json:"presets"`
	AutoPresets bool     `json:"autoPresets,omitempty"`
	// Order is every layer's name in the rule set's order: the file's own
	// layers, then the presets' ("rails: models"). It isn't top to bottom
	// (a file may list its layers either way); the Overview's map orders
	// its rows by import depth and uses this only to break ties.
	Order []string `json:"order"`
}

// Analysis is the architecture delta and contract surface of a change.
type Analysis struct {
	Module   string    `json:"module,omitempty"`
	Packages []Package `json:"packages"`
	// Labels are short display names for units with long paths.
	Labels map[string]string `json:"labels,omitempty"`
	// UnitLayers names the layer of every unit the analysis mentions (its
	// packages and both ends of its edges) that's in one, judged with the
	// unit's own language.
	UnitLayers map[string]string `json:"unitLayers"`
	Languages  []LangInfo        `json:"languages"`
	Edges      []Edge            `json:"edges"`
	Existing   []Edge            `json:"existing"`
	Violations int               `json:"violations"`
	Rules      Rules             `json:"rules"`
	Surface    Surface           `json:"surface"`
	Truncated  bool              `json:"truncated,omitempty"`
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

// manifests are the dependency and config files the surface reads.
var manifests = map[string]bool{
	"go.mod": true, "package.json": true, "Gemfile": true, "Gemfile.lock": true, "pyproject.toml": true,
	"AndroidManifest.xml": true, "build.gradle": true, "build.gradle.kts": true, "libs.versions.toml": true,
	"Info.plist": true, "project.yml": true, "Package.swift": true, "Package.resolved": true, "Podfile": true, "project.pbxproj": true,
}

// analyzed reports whether a changed file's contents matter to the
// analysis: code some language reads, and the manifests; not docs or
// binaries.
func analyzed(p string) bool {
	if b := path.Base(p); manifests[b] || strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt") ||
		strings.HasSuffix(b, ".entitlements") || strings.HasSuffix(b, ".xcconfig") || strings.HasSuffix(b, ".plist") {
		return true
	}
	for _, l := range languages() {
		if l.owns(p) {
			return true
		}
	}
	switch path.Ext(p) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".py":
		return true // surface patterns (routes, env) until those languages have analyzers
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
	a := &Analysis{Packages: []Package{}, Languages: []LangInfo{}, Edges: []Edge{}, Existing: []Edge{}}

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

	idx := newIndex(r)
	var langs []language
	for _, l := range languages() {
		if l.detect(idx) {
			langs = append(langs, l)
		}
	}
	rules := loadRules(r, idx)
	a.Rules = rules.info
	node, kotlin, swift := false, false, false
	for _, l := range langs {
		switch l.(type) {
		case *goLang:
			a.Module = l.(*goLang).module
		case *nodeLang:
			node = true
		case *ktLang:
			kotlin = true
		case *swiftLang:
			swift = true
		}
		n, ne := len(a.Packages), len(a.Edges)+len(a.Existing)
		edgeDelta(idx, l, a, files, rules)
		if lb, ok := l.(labeler); ok && len(a.Packages)+len(a.Edges)+len(a.Existing) > n+ne {
			if a.Labels == nil {
				a.Labels = map[string]string{}
			}
			for _, p := range a.Packages[n:] {
				a.Labels[p.Path] = lb.label(p.Path)
			}
			for _, e := range append(append([]Edge{}, a.Edges...), a.Existing...) {
				if e.Lang == l.name() {
					a.Labels[e.From], a.Labels[e.To] = lb.label(e.From), lb.label(e.To)
				}
			}
		}
		a.Languages = append(a.Languages, LangInfo{Name: l.name(), Exact: l.exact(), Units: len(a.Packages) - n})
	}
	a.Surface = surface(r, a.Module != "", isRailsApp(idx), node, o.Files, files)
	if kotlin {
		androidSurface(&a.Surface, files)
	}
	if swift {
		iosSurface(&a.Surface, files)
	}
	if kotlin || swift {
		a.Surface.Routes, a.Surface.Deps = sortChanges(a.Surface.Routes), sortChanges(a.Surface.Deps)
		a.Surface.Migrations = sortChanges(a.Surface.Migrations)
	}
	a.Surface.Permissions = sortChanges(a.Surface.Permissions)
	for _, e := range a.Edges {
		if e.Violation != "" {
			a.Violations++
		}
	}
	a.UnitLayers = unitLayers(a, rules)
	// Reads that ran out of time failed quietly along the way: a partial
	// analysis would look complete, so say it isn't.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("analysis didn't finish in time: %w", err)
	}
	return a, nil
}

// unitLayers maps each unit a mentions to its layer's name, leaving out
// units in no layer. A unit two languages share takes its first one's.
func unitLayers(a *Analysis, rules *ruleSet) map[string]string {
	m := map[string]string{}
	seen := map[string]bool{}
	add := func(unit, lang string) {
		if seen[unit] {
			return
		}
		seen[unit] = true
		if l := rules.layerOf(unit, lang); l != nil {
			m[unit] = l.Name
		}
	}
	for _, p := range a.Packages {
		add(p.Path, p.Lang)
	}
	for _, e := range append(append([]Edge{}, a.Edges...), a.Existing...) {
		add(e.From, e.Lang)
		add(e.To, e.Lang)
	}
	return m
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
