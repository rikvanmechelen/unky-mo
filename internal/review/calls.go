package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// Function statuses.
const (
	FuncAdded     = "added"
	FuncRemoved   = "removed"
	FuncRenamed   = "renamed"
	FuncSignature = "signature" // its parameters or results changed (and maybe its body)
	FuncChanged   = "changed"   // its body changed
)

// Call kinds.
const (
	CallStatic  = "static"  // resolved exactly
	CallDynamic = "dynamic" // through an interface: drawn to the interface method
	CallImpl    = "impl"    // an interface method → a known implementation
	CallRef     = "ref"     // a function used as a value (a handler, a callback), not called
	CallApprox  = "approx"  // resolved by name, not by types
)

// Finding kinds.
const (
	FindingRemovedCalled    = "removed-called"    // a removed function something still calls
	FindingSignatureCallers = "signature-callers" // a changed signature with callers the change doesn't touch
	FindingUntested         = "untested"          // a changed function no test reaches within untestedHops calls
)

const (
	maxCallFuncs   = 400 // functions in one answer; context goes first
	maxCallers     = 50  // context callers per function
	untestedHops   = 2
	maxCallSites   = 20 // sites kept per edge or finding
	callHashLength = 16
)

// Func is a function, method or other callable (a view, a callback list)
// that a change touches, or that calls or is called by one. ID is stable
// across file moves ("internal/review.edgeDelta", "(*internal/review.goLang).refs").
// Path and Line are in the new version, or, when Before is set (a removed
// function), in the base version.
type Func struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	End    int    `json:"end,omitempty"`
	Before bool   `json:"before,omitempty"`
	Unit   string `json:"unit,omitempty"`
	Lang   string `json:"lang"`
	// Status is one of the Func* constants, or "" for context.
	Status string `json:"status,omitempty"`
	// From is a renamed function's old ID.
	From string `json:"from,omitempty"`
	Test bool   `json:"test,omitempty"`
	// Unresolved counts calls that look like they go to this repo's code
	// but couldn't be resolved.
	Unresolved int `json:"unresolved,omitempty"`
	// MoreCallers counts callers left out past maxCallers.
	MoreCallers int `json:"moreCallers,omitempty"`
}

// Call is a call (or reference) from one function to another. Op is OpAdded
// or OpRemoved for a call the change adds or drops, "" for context.
type Call struct {
	From  string     `json:"from"`
	To    string     `json:"to"`
	Kind  string     `json:"kind"`
	Op    string     `json:"op,omitempty"`
	Sites []EdgeFile `json:"sites,omitempty"`
}

// CallFinding is something about the change's functions a reviewer should
// look at, with the places that show it.
type CallFinding struct {
	Kind  string     `json:"kind"`
	Func  string     `json:"func"`
	Sites []EdgeFile `json:"sites,omitempty"`
}

// CallGraph is the function-level view of a change: the functions it
// touches, the calls it adds and drops, one hop of context around them, and
// findings.
type CallGraph struct {
	Funcs     []Func        `json:"funcs"`
	Calls     []Call        `json:"calls"`
	Findings  []CallFinding `json:"findings"`
	Languages []LangInfo    `json:"languages"`
	// Unparsed lists changed files that don't parse (mid-edit): their
	// functions are left out rather than shown as removed.
	Unparsed  []string `json:"unparsed,omitempty"`
	Errors    []string `json:"errors,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// callSite is one resolved call in a function's body.
type callSite struct {
	to, kind string
	line     int
}

// unresolvedSite is a call that didn't resolve. want lists the IDs it would
// have if it pointed at a function that's gone, so a removed function
// that's still called can be found.
type unresolvedSite struct {
	line int
	want []string
}

// edgeKey identifies a call edge in callDelta.
type edgeKey struct{ from, to, kind string }

// callerRef is one entry of the caller index: who calls, where.
type callerRef struct {
	from string
	site callSite
	path string
}

// fn is one function of one version, with what it calls.
type fn struct {
	Func
	body, sig  string // hashes; sig "" for languages without signatures
	calls      []callSite
	unresolved []unresolvedSite
	// synthetic nodes (an interface method standing for its
	// implementations) are never changed themselves.
	synthetic bool
}

// callSet is one version's functions for one language: the changed files'
// (and for the new version, everything the caller index needs).
type callSet struct {
	funcs     map[string]*fn
	unparsed  map[string]bool // paths that didn't parse
	truncated bool
}

func newCallSet() *callSet {
	return &callSet{funcs: map[string]*fn{}, unparsed: map[string]bool{}}
}

// add puts f in the set. A second definition with the same ID (a
// redeclaration mid-edit, or two languages' guesses colliding) keeps the
// first and merges the calls.
func (s *callSet) add(f *fn) {
	if prev, ok := s.funcs[f.ID]; ok {
		prev.calls = append(prev.calls, f.calls...)
		prev.unresolved = append(prev.unresolved, f.unresolved...)
		prev.body += f.body
		return
	}
	s.funcs[f.ID] = f
}

// callLang builds one version's functions for a language.
type callLang interface {
	name() string
	exact() bool
	// funcs returns the functions of the given files (that version's paths)
	// in the version idx lists, with resolved calls. full adds what the
	// caller index needs: every function that could call the changed ones,
	// and the tests that could reach them.
	funcs(idx *index, files []string, full bool) (*callSet, error)
}

// callLangOf is the call analyzer for an architecture language, nil if
// there's none yet.
func callLangOf(l language) callLang {
	switch l := l.(type) {
	case *goLang:
		return &goCalls{module: l.module}
	}
	return nil
}

// hashOf is a short content hash.
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:callHashLength]
}

// Calls works out the function-level view of a change: o is the overview
// from gitfiles.GetOverview (or GetOverviewAt).
func Calls(ctx context.Context, cmd moexec.Commander, o *gitfiles.Overview) (*CallGraph, error) {
	r := &repo{ctx: ctx, cmd: cmd, root: o.Root, rev: o.Rev, head: o.Head}
	cg := &CallGraph{Funcs: []Func{}, Calls: []Call{}, Findings: []CallFinding{}, Languages: []LangInfo{}}

	var files []gitfiles.OverviewFile
	for _, of := range o.Files {
		if !analyzed(of.Path) && !(of.OldPath != "" && analyzed(of.OldPath)) {
			continue
		}
		if len(files) == maxAnalyzed {
			cg.Truncated = true
			break
		}
		files = append(files, of)
	}

	idx := newIndex(r)
	base := newIndexAt(r, r.rev)
	for _, l := range languages() {
		if !l.detect(idx) {
			continue
		}
		cl := callLangOf(l)
		if cl == nil {
			continue
		}
		var oldPaths, newPaths []string
		renames := map[string]string{} // old path → new path
		for _, f := range files {
			old := f.Path
			if f.OldPath != "" {
				old = f.OldPath
			}
			if f.Status != "A" && f.Status != "?" && l.owns(old) {
				oldPaths = append(oldPaths, old)
				renames[old] = f.Path
			}
			if f.Status != "D" && l.owns(f.Path) {
				newPaths = append(newPaths, f.Path)
			}
		}
		if len(oldPaths)+len(newPaths) == 0 {
			continue
		}
		before, after, err := callSides(cl, base, idx, oldPaths, newPaths)
		if err != nil {
			cg.Errors = append(cg.Errors, fmt.Sprintf("%s: %v", cl.name(), err))
			continue
		}
		n := len(cg.Funcs)
		callDelta(cg, cl, before, after, oldPaths, newPaths, renames)
		changed := 0
		for _, f := range cg.Funcs[n:] {
			if f.Status != "" {
				changed++
			}
		}
		cg.Languages = append(cg.Languages, LangInfo{Name: cl.name(), Exact: cl.exact(), Units: changed})
	}
	capCallGraph(cg)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("call graph didn't finish in time: %w", err)
	}
	return cg, nil
}

// callSides builds both versions, turning a panic in a language's analyzer
// into an error for that language only.
func callSides(cl callLang, base, idx *index, oldPaths, newPaths []string) (before, after *callSet, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("analyzer failed: %v", p)
		}
	}()
	if before, err = cl.funcs(base, oldPaths, false); err != nil {
		return nil, nil, err
	}
	if after, err = cl.funcs(idx, newPaths, true); err != nil {
		return nil, nil, err
	}
	return before, after, nil
}

// callDelta adds one language's changed functions, changed calls, context
// and findings to cg.
func callDelta(cg *CallGraph, cl callLang, before, after *callSet, oldPaths, newPaths []string, renames map[string]string) {
	if before.truncated || after.truncated {
		cg.Truncated = true
	}
	changedOld := map[string]bool{}
	for _, p := range oldPaths {
		changedOld[p] = true
	}
	changedNew := map[string]bool{}
	for _, p := range newPaths {
		changedNew[p] = true
	}
	// A file that doesn't parse on either side hides its functions on both:
	// otherwise they'd all look removed (or added).
	skipOld, skipNew := map[string]bool{}, map[string]bool{}
	for p := range before.unparsed {
		if changedOld[p] {
			skipOld[p], skipNew[renames[p]] = true, true
			cg.Unparsed = append(cg.Unparsed, renames[p])
		}
	}
	for p := range after.unparsed {
		if changedNew[p] {
			skipNew[p] = true
			for old, nw := range renames {
				if nw == p {
					skipOld[old] = true
				}
			}
			cg.Unparsed = append(cg.Unparsed, p)
		}
	}
	cg.Unparsed = sortedUnique(cg.Unparsed)

	// The changed files' functions on each side.
	oldFns, newFns := map[string]*fn{}, map[string]*fn{}
	for id, f := range before.funcs {
		if changedOld[f.Path] && !skipOld[f.Path] && !f.synthetic {
			oldFns[id] = f
		}
	}
	for id, f := range after.funcs {
		if changedNew[f.Path] && !skipNew[f.Path] && !f.synthetic {
			newFns[id] = f
		}
	}

	status := map[string]string{} // ID → status (new IDs, plus removed old ones)
	var added, removed []string
	for _, id := range sortedKeys(newFns) {
		f := newFns[id]
		old, ok := before.funcs[id]
		switch {
		case !ok:
			added = append(added, id)
		case old.sig != f.sig:
			status[id] = FuncSignature
		case old.body != f.body:
			status[id] = FuncChanged
		}
	}
	for _, id := range sortedKeys(oldFns) {
		if _, ok := after.funcs[id]; !ok {
			removed = append(removed, id)
		}
	}
	// Renames: an added and a removed function with the same body in the
	// same unit.
	renamedFrom := map[string]string{} // new ID → old ID
	oldToNew := map[string]string{}
	for _, a := range added {
		nf := newFns[a]
		for i, r := range removed {
			of := oldFns[r]
			if r != "" && of.body != "" && of.body == nf.body && of.Unit == nf.Unit && of.Lang == nf.Lang {
				renamedFrom[a], oldToNew[r] = r, a
				removed[i] = ""
				break
			}
		}
	}
	for _, a := range added {
		if _, ok := renamedFrom[a]; ok {
			status[a] = FuncRenamed
		} else {
			status[a] = FuncAdded
		}
	}
	var gone []string
	for _, r := range removed {
		if r != "" {
			status[r] = FuncRemoved
			gone = append(gone, r)
		}
	}

	// Edges of the changed files' functions: before vs after, with a
	// renamed function (as caller or callee) under its new ID on both sides.
	oldEdges, newEdges := map[edgeKey][]EdgeFile{}, map[edgeKey][]EdgeFile{}
	rename := func(id string) string {
		if n, ok := oldToNew[id]; ok {
			return n
		}
		return id
	}
	for _, f := range oldFns {
		for _, c := range f.calls {
			if c.kind == CallImpl {
				continue // which implementations are known depends on what was loaded
			}
			k := edgeKey{rename(f.ID), rename(c.to), c.kind}
			oldEdges[k] = appendSite(oldEdges[k], EdgeFile{Path: f.Path, Line: c.line})
		}
	}
	for _, f := range newFns {
		for _, c := range f.calls {
			if c.kind == CallImpl {
				continue
			}
			k := edgeKey{f.ID, c.to, c.kind}
			newEdges[k] = appendSite(newEdges[k], EdgeFile{Path: f.Path, Line: c.line})
		}
	}

	g := &graphBuilder{cg: cg, lang: cl.name(), funcs: map[string]*Func{}, edges: map[string]bool{}}
	addFn := func(f *fn, st string, before bool) {
		out := f.Func
		out.Status, out.Before = st, before
		out.Unresolved = len(f.unresolved)
		if st == FuncRenamed {
			out.From = renamedFrom[f.ID]
		}
		g.addFunc(out)
	}
	for id, st := range status {
		if st == FuncRemoved {
			addFn(oldFns[id], st, true)
		} else {
			addFn(newFns[id], st, false)
		}
	}
	center := func(id string) bool { return status[id] != "" }

	approx := !cl.exact()
	kindOf := func(k string) string {
		if approx && (k == CallStatic || k == CallDynamic) {
			return CallApprox
		}
		return k
	}
	for _, ek := range sortedEdgeKeys(newEdges) {
		op := OpAdded
		if _, had := oldEdges[ek]; had {
			// Unchanged: context only when it leaves a changed function.
			if !center(ek.from) {
				continue
			}
			op = ""
		}
		g.ensure(after, before, ek.from)
		g.ensure(after, before, ek.to)
		g.addCall(Call{From: ek.from, To: ek.to, Kind: kindOf(ek.kind), Op: op, Sites: newEdges[ek]})
	}
	for _, ek := range sortedEdgeKeys(oldEdges) {
		if _, has := newEdges[ek]; has {
			continue
		}
		g.ensure(after, before, ek.from)
		g.ensure(after, before, ek.to)
		g.addCall(Call{From: ek.from, To: ek.to, Kind: kindOf(ek.kind), Op: OpRemoved, Sites: oldEdges[ek]})
	}

	// The implementations behind each interface method a drawn edge calls
	// dynamically, for context.
	for _, c := range append([]Call{}, g.calls...) {
		if c.Kind != CallDynamic {
			continue
		}
		if f := after.funcs[c.To]; f != nil {
			for _, ic := range f.calls {
				if ic.kind == CallImpl {
					g.ensure(after, before, ic.to)
					g.addCall(Call{From: c.To, To: ic.to, Kind: CallImpl})
				}
			}
		}
	}

	// Callers of the changed functions in the new version, from anywhere
	// the language looked (context, and the findings' sources).
	callers := map[string][]callerRef{}
	for _, f := range after.funcs {
		for _, c := range f.calls {
			callers[c.to] = append(callers[c.to], callerRef{f.ID, c, f.Path})
		}
	}
	for _, id := range sortedKeys(status) {
		st := status[id]
		if st == FuncRemoved || st == FuncAdded {
			continue // a removed function has no callers left to resolve; an added one's are in the edges
		}
		cs := callers[id]
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].path != cs[j].path {
				return cs[i].path < cs[j].path
			}
			return cs[i].site.line < cs[j].site.line
		})
		seen := map[string]bool{}
		var sigSites []EdgeFile
		for _, c := range cs {
			if c.from == id {
				continue
			}
			if st == FuncSignature && !changedNew[c.path] {
				sigSites = appendSite(sigSites, EdgeFile{Path: c.path, Line: c.site.line})
			}
			if newEdges[edgeKey{c.from, id, c.site.kind}] != nil && changedNew[c.path] {
				continue // already drawn as a changed file's edge
			}
			if !seen[c.from] {
				if len(seen) == maxCallers {
					g.funcs[id].MoreCallers++
					continue
				}
				seen[c.from] = true
			}
			g.ensure(after, before, c.from)
			g.addCall(Call{From: c.from, To: id, Kind: kindOf(c.site.kind), Sites: []EdgeFile{{Path: c.path, Line: c.site.line}}})
		}
		if len(sigSites) > 0 {
			cg.Findings = append(cg.Findings, CallFinding{Kind: FindingSignatureCallers, Func: id, Sites: sigSites})
		}
	}

	// A removed function that something still calls: unresolved calls in
	// the new version that would have gone to it.
	if len(gone) > 0 {
		want := map[string][]EdgeFile{}
		for _, f := range after.funcs {
			for _, u := range f.unresolved {
				for _, w := range u.want {
					want[w] = appendSite(want[w], EdgeFile{Path: f.Path, Line: u.line})
				}
			}
		}
		for _, id := range gone {
			if sites := want[id]; len(sites) > 0 {
				cg.Findings = append(cg.Findings, CallFinding{Kind: FindingRemovedCalled, Func: id, Sites: sites})
			}
		}
	}

	// A changed function no test reaches within untestedHops calls. Only
	// for languages that loaded any tests: otherwise every function would
	// be flagged.
	hasTests := false
	for _, f := range after.funcs {
		if f.Test {
			hasTests = true
			break
		}
	}
	if hasTests {
		for _, id := range sortedKeys(status) {
			st := status[id]
			if st == FuncRemoved || newFns[id] == nil || newFns[id].Test {
				continue
			}
			if !reachedByTest(id, after, callers, untestedHops) {
				cg.Findings = append(cg.Findings, CallFinding{Kind: FindingUntested, Func: id})
			}
		}
	}
	g.flush()
}

// reachedByTest reports whether a test function calls id within hops calls.
func reachedByTest(id string, s *callSet, callers map[string][]callerRef, hops int) bool {
	frontier := []string{id}
	seen := map[string]bool{id: true}
	for h := 0; h < hops && len(frontier) > 0; h++ {
		var next []string
		for _, to := range frontier {
			for _, c := range callers[to] {
				from := c.from
				if seen[from] {
					continue
				}
				seen[from] = true
				if f := s.funcs[from]; f != nil && f.Test {
					return true
				}
				next = append(next, from)
			}
		}
		frontier = next
	}
	return false
}

// graphBuilder collects one language's functions and calls, deduplicated.
type graphBuilder struct {
	cg    *CallGraph
	lang  string
	funcs map[string]*Func
	order []string
	edges map[string]bool
	calls []Call
}

func (g *graphBuilder) addFunc(f Func) {
	if _, ok := g.funcs[f.ID]; ok {
		return
	}
	g.funcs[f.ID] = &f
	g.order = append(g.order, f.ID)
}

// ensure adds a context node for id, from the new version if it's there,
// else the base.
func (g *graphBuilder) ensure(after, before *callSet, id string) {
	if _, ok := g.funcs[id]; ok {
		return
	}
	if f := after.funcs[id]; f != nil {
		out := f.Func
		out.Unresolved = len(f.unresolved)
		g.addFunc(out)
		return
	}
	if f := before.funcs[id]; f != nil {
		out := f.Func
		out.Before = true
		g.addFunc(out)
		return
	}
	// A callee the language resolved without loading its body (an
	// interface method of an unloaded package): a bare node.
	g.addFunc(Func{ID: id, Name: shortName(id), Lang: g.lang})
}

func (g *graphBuilder) addCall(c Call) {
	k := c.From + "\x00" + c.To + "\x00" + c.Kind + "\x00" + c.Op
	if g.edges[k] {
		for i := range g.calls {
			if g.calls[i].From == c.From && g.calls[i].To == c.To && g.calls[i].Kind == c.Kind && g.calls[i].Op == c.Op {
				for _, s := range c.Sites {
					g.calls[i].Sites = appendSite(g.calls[i].Sites, s)
				}
			}
		}
		return
	}
	g.edges[k] = true
	g.calls = append(g.calls, c)
}

func (g *graphBuilder) flush() {
	for _, id := range g.order {
		g.cg.Funcs = append(g.cg.Funcs, *g.funcs[id])
	}
	g.cg.Calls = append(g.cg.Calls, g.calls...)
}

// shortName is the last element of an ID, for display when nothing better
// is known.
func shortName(id string) string {
	id = strings.TrimPrefix(id, "(")
	if i := strings.LastIndexAny(id, "/:#"); i >= 0 {
		id = id[i+1:]
	}
	return strings.NewReplacer("*", "", ")", "").Replace(id)
}

func appendSite(sites []EdgeFile, s EdgeFile) []EdgeFile {
	if len(sites) >= maxCallSites {
		return sites
	}
	for _, x := range sites {
		if x == s {
			return sites
		}
	}
	return append(sites, s)
}

func sortedEdgeKeys(m map[edgeKey][]EdgeFile) []edgeKey {
	keys := make([]edgeKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		return a.kind < b.kind
	})
	return keys
}

func sortedUnique(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, x := range s {
		if x != "" && (i == 0 || x != s[i-1]) {
			out = append(out, x)
		}
	}
	return out
}

// capCallGraph keeps at most maxCallFuncs functions: every changed one
// first, then context, and drops calls whose ends were cut.
func capCallGraph(cg *CallGraph) {
	sort.SliceStable(cg.Findings, func(i, j int) bool {
		return findingRank(cg.Findings[i].Kind) < findingRank(cg.Findings[j].Kind)
	})
	if len(cg.Funcs) <= maxCallFuncs {
		return
	}
	cg.Truncated = true
	sort.SliceStable(cg.Funcs, func(i, j int) bool {
		return (cg.Funcs[i].Status != "") && (cg.Funcs[j].Status == "")
	})
	cg.Funcs = cg.Funcs[:maxCallFuncs]
	kept := map[string]bool{}
	for _, f := range cg.Funcs {
		kept[f.ID] = true
	}
	calls := cg.Calls[:0]
	for _, c := range cg.Calls {
		if kept[c.From] && kept[c.To] {
			calls = append(calls, c)
		}
	}
	cg.Calls = calls
}

func findingRank(kind string) int {
	switch kind {
	case FindingRemovedCalled:
		return 0
	case FindingSignatureCallers:
		return 1
	}
	return 2
}
