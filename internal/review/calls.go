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
	// FindingTestNotUpdated: a changed source file whose conventional test
	// file exists but wasn't changed with it (a hint, like untested).
	FindingTestNotUpdated = "test-not-updated"
	// A route (added, retargeted, or whose controller changed) to an action
	// its controller doesn't define, with no template to render instead.
	FindingRouteWithoutAction = "route-without-action"
	// A view's data-action or data-*-target naming a method or target its
	// Stimulus controller doesn't have.
	FindingStimulusUnbound = "stimulus-unbound"
)

const (
	maxCallFuncs   = 1000 // functions in one answer; context goes first
	maxCallers     = 50   // context callers per function
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
	// TestedBy is the nearest test that reaches a changed function within
	// untestedHops calls (nil when none does, or when it wasn't checked).
	TestedBy *TestRef `json:"testedBy,omitempty"`
	// Sig and OldSig are a signature change's definition header in the new
	// and the old version, as written ("func NewServer(cfg Config) *Server").
	Sig    string `json:"sig,omitempty"`
	OldSig string `json:"oldSig,omitempty"`
}

// TestRef names a test function and where it is. Via is the test helper
// the test reaches the function through, when that's how (a helper in a
// test file counts as a test for reachedByTest, but isn't one to name).
type TestRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Line int    `json:"line"`
	Via  string `json:"via,omitempty"`
}

// Call is a call (or reference) from one function to another. Op is OpAdded
// or OpRemoved for a call the change adds or drops, "" for context.
type Call struct {
	From  string     `json:"from"`
	To    string     `json:"to"`
	Kind  string     `json:"kind"`
	Op    string     `json:"op,omitempty"`
	Sites []EdgeFile `json:"sites,omitempty"`
	// Label says what a reference is for, when the code says: the route a
	// handler is registered for ("GET /api/state").
	Label string `json:"label,omitempty"`
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
	// UnitLayers names the layer of every unit a function here is in, like
	// Analysis.UnitLayers: context functions are often in units the
	// architecture analysis doesn't mention.
	UnitLayers map[string]string `json:"unitLayers"`
}

// callSite is one resolved call in a function's body.
type callSite struct {
	to, kind string
	line     int
	label    string // what wires it up, e.g. the route a handler serves
}

// unresolvedSite is a call that didn't resolve. want lists the IDs it would
// have if it pointed at a function that's gone, so a removed function
// that's still called can be found.
type unresolvedSite struct {
	line int
	want []string
	// quiet sites are kept for matching want, not counted as unresolved
	// (a method call on an unknown receiver that may well be external).
	quiet bool
	// flag is a finding kind this site is on its own (see hFlagger), raised
	// when its function changed or one of the about files did.
	flag  string
	about []string
}

// countUnresolved counts the sites that aren't quiet.
func countUnresolved(sites []unresolvedSite) int {
	n := 0
	for _, u := range sites {
		if !u.quiet {
			n++
		}
	}
	return n
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
	body, sig string // hashes; sig "" for languages without signatures
	// req hashes only the required parameters (no default value): a new
	// parameter with a default changes sig but breaks no caller. "" when
	// the language doesn't tell them apart.
	req        string
	calls      []callSite
	unresolved []unresolvedSite
	// synthetic nodes (an interface method standing for its
	// implementations) are never changed themselves.
	synthetic bool
	// generated code isn't flagged as untested, nor are entry points
	// nothing in the code calls (a package's init, a class body, a view,
	// a file's top-level code).
	generated, entry bool
	// conventional: the source file's conventional test file calls it by
	// name, which counts as tested (Rails tests go through framework calls).
	conventional bool
}

// callSet is one version's functions for one language: the changed files'
// (and for the new version, everything the caller index needs).
type callSet struct {
	funcs     map[string]*fn
	unparsed  map[string]bool // paths that didn't parse
	truncated bool
	// testFiles: a source file → its conventional test files that exist.
	testFiles map[string][]string
}

func newCallSet() *callSet {
	return &callSet{funcs: map[string]*fn{}, unparsed: map[string]bool{}, testFiles: map[string][]string{}}
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
	case *pyLang:
		return &hCalls{l: &pyCalls{}}
	case *swiftLang:
		return newSwiftCallLang()
	case *ktLang:
		return &hCalls{l: &jvmCalls{}}
	case *rubyLang:
		return &hCalls{l: &rbCalls{}}
	case *nodeLang:
		return &hCalls{l: &jsCalls{}}
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
	var runs []langRun
	var idle []callLang // detected, but the change has none of their files
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
			idle = append(idle, cl)
			continue
		}
		before, after, err := callSides(cl, base, idx, oldPaths, newPaths)
		if err != nil {
			cg.Errors = append(cg.Errors, fmt.Sprintf("%s: %v", cl.name(), err))
			continue
		}
		n := len(cg.Funcs)
		callDelta(cg, cl, before, after, oldPaths, newPaths, renames)
		addSignatures(cg.Funcs[n:], before, base, idx)
		changed := 0
		for _, f := range cg.Funcs[n:] {
			if f.Status != "" {
				changed++
			}
		}
		cg.Languages = append(cg.Languages, LangInfo{Name: cl.name(), Exact: cl.exact(), Units: changed})
		runs = append(runs, langRun{cl: cl, before: before, after: after})
	}
	crossPass(cg, base, idx, files, runs, idle)
	capCallGraph(cg)
	cg.UnitLayers = funcLayers(cg.Funcs, loadRules(r, idx))
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
	// A renamed function's old name is gone too: code still calling it is
	// broken just the same.
	for _, old := range sortedKeys(oldToNew) {
		gone = append(gone, old)
	}

	// Edges of the changed files' functions: before vs after, with a
	// renamed function (as caller or callee) under its new ID on both sides.
	oldEdges, newEdges := map[edgeKey][]EdgeFile{}, map[edgeKey][]EdgeFile{}
	labels := map[edgeKey]string{}
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
			if c.label != "" && labels[k] == "" {
				labels[k] = c.label
			}
		}
	}

	g := &graphBuilder{cg: cg, lang: cl.name(), funcs: map[string]*Func{}, edges: map[string]bool{}}
	addFn := func(f *fn, st string, before bool) {
		out := f.Func
		out.Status, out.Before = st, before
		out.Unresolved = countUnresolved(f.unresolved)
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
		g.addCall(Call{From: ek.from, To: ek.to, Kind: kindOf(ek.kind), Op: op, Sites: newEdges[ek], Label: labels[ek]})
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
	// In a stable order, so the test reachedByTest names doesn't change
	// between polls.
	for _, cs := range callers {
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].from != cs[j].from {
				return cs[i].from < cs[j].from
			}
			return cs[i].site.line < cs[j].site.line
		})
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
			if st == FuncSignature && !changedNew[c.path] && !onlyDefaultsAdded(before.funcs[id], after.funcs[id]) {
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
			g.addCall(Call{From: c.from, To: id, Kind: kindOf(c.site.kind), Sites: []EdgeFile{{Path: c.path, Line: c.site.line}}, Label: c.site.label})
		}
		if len(sigSites) > 0 {
			cg.Findings = append(cg.Findings, CallFinding{Kind: FindingSignatureCallers, Func: id, Sites: sigSites})
		}
	}

	// A removed function that something still calls: unresolved calls in
	// the new version that would have gone to it.
	if len(gone) > 0 {
		want := map[string][]EdgeFile{}
		for _, id := range sortedKeys(after.funcs) { // sites in a stable order
			f := after.funcs[id]
			for _, u := range f.unresolved {
				for _, w := range u.want {
					want[w] = appendSite(want[w], EdgeFile{Path: f.Path, Line: u.line})
				}
			}
		}
		for _, id := range gone {
			sites := want[id]
			// Heuristic languages can only name a method whose receiver
			// they don't know: "~name" stands for any removed method
			// called that (and is only emitted when none is left).
			for _, s := range want["~"+bareName(id)] {
				sites = appendSite(sites, s)
			}
			// "~" + the ID is a quiet want for exactly that function: a
			// call that's most likely external unless the repo had it (a
			// route helper the routes no longer define).
			for _, s := range want["~"+id] {
				sites = appendSite(sites, s)
			}
			if len(sites) > 0 {
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
			if st == FuncRemoved || newFns[id] == nil || newFns[id].Test || newFns[id].generated || newFns[id].entry || newFns[id].conventional {
				continue
			}
			if t := reachedByTest(id, after, callers, untestedHops); t != nil {
				ref := &TestRef{ID: t.ID, Name: t.Name, Path: t.Path, Line: t.Line}
				if root := rootTest(t, after, callers); root != t {
					ref = &TestRef{ID: root.ID, Name: root.Name, Path: root.Path, Line: root.Line, Via: t.Name}
				}
				g.funcs[id].TestedBy = ref
			} else {
				cg.Findings = append(cg.Findings, CallFinding{Kind: FindingUntested, Func: id})
			}
		}
	}
	// A changed file whose conventional test file exists but didn't change.
	byFile := map[string]string{} // path → its first changed function
	for _, id := range sortedKeys(status) {
		if f := newFns[id]; f != nil && status[id] != FuncRemoved && !f.Test && !f.generated && !f.entry {
			if _, ok := byFile[f.Path]; !ok {
				byFile[f.Path] = id
			}
		}
	}
	for _, p := range sortedKeys(byFile) {
		tests := after.testFiles[p]
		if len(tests) == 0 {
			continue
		}
		touched := false
		var sites []EdgeFile
		for _, t := range tests {
			touched = touched || changedNew[t]
			sites = append(sites, EdgeFile{Path: t, Line: 1})
		}
		if !touched {
			cg.Findings = append(cg.Findings, CallFinding{Kind: FindingTestNotUpdated, Func: byFile[p], Sites: sites})
		}
	}
	g.flush()
}

// reachedByTest returns the first test function found that calls id within
// hops calls (the nearest, as the search goes outward), or nil. Going from
// an interface method to its implementation is free: a test calling
// through an interface reaches every implementation.
func reachedByTest(id string, s *callSet, callers map[string][]callerRef, hops int) *fn {
	dist := map[string]int{id: 0}
	queue := []string{id} // a 0-1 BFS: free steps go to the front
	for len(queue) > 0 {
		to := queue[0]
		queue = queue[1:]
		for _, c := range callers[to] {
			// Free steps: an interface method to its implementation (a test
			// calling through an interface reaches them all), and a Rails
			// route or URL helper to what it routes to (a request test's
			// get ticket_path(t) reaches the action).
			cost := 1
			if c.site.kind == CallImpl || strings.HasPrefix(c.from, "route:") {
				cost = 0
			}
			d := dist[to] + cost
			if d > hops {
				continue
			}
			if old, ok := dist[c.from]; ok && old <= d {
				continue
			}
			dist[c.from] = d
			if f := s.funcs[c.from]; f != nil && f.Test {
				return f
			}
			if cost == 0 {
				queue = append([]string{c.from}, queue...)
			} else {
				queue = append(queue, c.from)
			}
		}
	}
	return nil
}

// maxRootTestSearch bounds rootTest's walk through test helpers.
const maxRootTestSearch = 200

// rootTest walks up from a function in a test file through its callers in
// test files to one no test-file function calls: the test itself rather
// than a helper (delta, callGraphOf). t itself when nothing calls it, or
// when every path loops.
func rootTest(t *fn, s *callSet, callers map[string][]callerRef) *fn {
	seen := map[string]bool{t.ID: true}
	queue := []*fn{t}
	for len(queue) > 0 && len(seen) < maxRootTestSearch {
		f := queue[0]
		queue = queue[1:]
		called := false
		for _, c := range callers[f.ID] {
			from := s.funcs[c.from]
			if from == nil || !from.Test {
				continue
			}
			called = true
			if !seen[from.ID] {
				seen[from.ID] = true
				queue = append(queue, from)
			}
		}
		if !called && f != t {
			return f
		}
		if !called {
			return t
		}
	}
	return t
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
		out.Unresolved = countUnresolved(f.unresolved)
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

// onlyDefaultsAdded reports whether a signature change left the required
// parameters as they were (it only added or changed defaulted ones).
func onlyDefaultsAdded(old, cur *fn) bool {
	return old != nil && cur != nil && old.req != "" && old.req == cur.req
}

// bareName is an ID's last name: "app/models.py:User.save" → "save".
func bareName(id string) string {
	if i := strings.LastIndexAny(id, ".:#/"); i >= 0 {
		return strings.TrimSuffix(id[i+1:], ")")
	}
	return id
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
// first, then the ends of calls the change adds or drops, then other
// context. Calls whose ends were cut go too.
func capCallGraph(cg *CallGraph) {
	sort.SliceStable(cg.Findings, func(i, j int) bool {
		return findingRank(cg.Findings[i].Kind) < findingRank(cg.Findings[j].Kind)
	})
	// Sites were gathered from maps: give them a stable order.
	for _, f := range cg.Findings {
		sort.Slice(f.Sites, func(i, j int) bool {
			if f.Sites[i].Path != f.Sites[j].Path {
				return f.Sites[i].Path < f.Sites[j].Path
			}
			return f.Sites[i].Line < f.Sites[j].Line
		})
	}
	if len(cg.Funcs) <= maxCallFuncs {
		return
	}
	cg.Truncated = true
	onChangedCall := map[string]bool{}
	for _, c := range cg.Calls {
		if c.Op != "" {
			onChangedCall[c.From], onChangedCall[c.To] = true, true
		}
	}
	rank := func(f Func) int {
		switch {
		case f.Status != "":
			return 0
		case onChangedCall[f.ID]:
			return 1
		}
		return 2
	}
	sort.SliceStable(cg.Funcs, func(i, j int) bool { return rank(cg.Funcs[i]) < rank(cg.Funcs[j]) })
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
	case FindingStimulusUnbound:
		return 1
	case FindingRouteWithoutAction:
		return 2
	case FindingSignatureCallers:
		return 3
	case FindingUntested:
		return 4
	}
	return 5
}

// funcLayers maps the unit of each function to its layer's name, judged
// with the function's language, leaving out units in no layer.
func funcLayers(funcs []Func, rules *ruleSet) map[string]string {
	m := map[string]string{}
	seen := map[string]bool{}
	for _, f := range funcs {
		if seen[f.Unit] {
			continue
		}
		seen[f.Unit] = true
		if l := rules.layerOf(f.Unit, f.Lang); l != nil {
			m[f.Unit] = l.Name
		}
	}
	return m
}

// addSignatures fills in a signature change's header text on both sides,
// read from each version's file at the definition's line.
func addSignatures(funcs []Func, before *callSet, base, idx *index) {
	for i := range funcs {
		f := &funcs[i]
		if f.Status != FuncSignature {
			continue
		}
		if t := idx.read(f.Path); t != nil {
			f.Sig = defHeader(*t, f.Line)
		}
		if old := before.funcs[f.ID]; old != nil {
			if t := base.read(old.Path); t != nil {
				f.OldSig = defHeader(*t, old.Line)
			}
		}
	}
}

// Bounds on a definition header.
const (
	maxHeaderLines = 8
	maxHeaderRunes = 300
)

// defHeader is the definition starting at line (1-based) of text, as
// written up to its body: annotation lines (@…) skipped, lines joined
// until the parentheses and square brackets balance, whitespace collapsed,
// and cut at the body: the first "{" outside brackets (not "{}", as in
// interface{}), or for Python the first ":". It's text, not parsed, so it
// works the same for every language.
func defHeader(text string, line int) string {
	lines := strings.Split(text, "\n")
	i := line - 1
	if i < 0 || i >= len(lines) {
		return ""
	}
	for i < len(lines)-1 && annotationOnly(strings.TrimSpace(lines[i])) {
		i++
	}
	var parts []string
	depth := 0
	for n := 0; n < maxHeaderLines && i+n < len(lines); n++ {
		l := strings.TrimSpace(lines[i+n])
		parts = append(parts, l)
		depth += strings.Count(l, "(") + strings.Count(l, "[") - strings.Count(l, ")") - strings.Count(l, "]")
		if depth <= 0 {
			break
		}
	}
	h := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
	python := strings.HasPrefix(h, "def ") || strings.HasPrefix(h, "async def ")
	depth = 0
	for k := 0; k < len(h); k++ {
		switch c := h[k]; {
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case depth > 0:
		case c == '{' && !strings.HasPrefix(h[k:], "{}"), python && c == ':':
			h = h[:k]
			k = len(h)
		}
	}
	h = strings.TrimSpace(h)
	if r := []rune(h); len(r) > maxHeaderRunes {
		h = string(r[:maxHeaderRunes]) + "…"
	}
	return h
}

// annotationOnly reports whether l is nothing but an annotation or
// decorator: "@name" with an optional argument list ("@app.route('/x')"),
// not "@objc func x()".
func annotationOnly(l string) bool {
	if !strings.HasPrefix(l, "@") {
		return false
	}
	i := 1
	for i < len(l) && (l[i] == '_' || l[i] == '.' || l[i] == ':' || l[i] >= '0' && l[i] <= '9' || l[i] >= 'a' && l[i] <= 'z' || l[i] >= 'A' && l[i] <= 'Z') {
		i++
	}
	if i < len(l) && l[i] == '(' {
		depth := 0
		for ; i < len(l); i++ {
			if l[i] == '(' {
				depth++
			} else if l[i] == ')' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
		}
	}
	return i > 1 && strings.TrimSpace(l[i:]) == ""
}
