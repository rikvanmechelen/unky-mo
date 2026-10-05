package review

import (
	"fmt"
	"sort"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// The cross-language pass. Each language's delta (callDelta) sees only its
// own functions, but some calls cross languages: a Rails view's
// data-action binds a Stimulus controller's JS method. crossPass runs after
// every language's delta, over all of their new-version sets, and:
//   - fills in context nodes a language could only name (a callee in
//     another language) from the language that has them;
//   - draws callers in other languages of the change's changed functions;
//   - matches every removed function (and removed non-function callables,
//     such as Stimulus targets) against every other language's wants, so a
//     removed JS method still bound in a view is removed-called;
//   - raises the findings unresolved sites carry themselves (hFlagger):
//     route-without-action, stimulus-unbound.

// crossLang is implemented by a callLang whose calls reach other
// languages' functions. hCalls implements it for every heuristic language,
// answering through its hLang's hCross (and nothing without one).
type crossLang interface {
	// crossCalls reports whether the language's new version is worth
	// building for the pass when the change touches none of its files
	// (Ruby: when the repo has Stimulus controllers).
	crossCalls(idx *index) bool
	// foreign describes another language's function that the language's
	// last new-version build resolved a call to.
	foreign(id string) (Func, bool)
	// aliases lists the other wants a removed function of another language
	// is known by in this language's calls ("~stimulus:cart#add").
	aliases(f Func) []string
	// removedExtra lists callables that aren't functions (Stimulus targets)
	// the change removed, as removed Funcs, for files in changed.
	removedExtra(base, idx *index, changed map[string]bool) []Func
}

// langRun is one language's two sets. cross marks a language built only
// for the cross-language pass (none of its files changed).
type langRun struct {
	cl            callLang
	before, after *callSet
	cross         bool
}

// crossSide builds a language's new version alone, a panic becoming an
// error as in callSides.
func crossSide(cl callLang, idx *index) (after *callSet, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("analyzer failed: %v", p)
		}
	}()
	return cl.funcs(idx, nil, true)
}

func crossPass(cg *CallGraph, base, idx *index, files []gitfiles.OverviewFile, runs []langRun, idle []callLang) {
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Path] = true
		if f.OldPath != "" {
			changed[f.OldPath] = true
		}
	}
	status := map[string]string{}
	for _, f := range cg.Funcs {
		if f.Status != "" {
			status[f.ID] = f.Status
		}
	}
	if len(status) > 0 {
		for _, cl := range idle {
			cx, ok := cl.(crossLang)
			if !ok || !cx.crossCalls(idx) {
				continue
			}
			after, err := crossSide(cl, idx)
			if err != nil {
				cg.Errors = append(cg.Errors, fmt.Sprintf("%s: %v", cl.name(), err))
				continue
			}
			runs = append(runs, langRun{cl: cl, before: newCallSet(), after: after, cross: true})
		}
	}
	if len(runs) < 2 {
		// One language alone: only its flagged sites are left to raise.
		flaggedFindings(cg, runs, status, changed)
		return
	}
	fillContext(cg, runs)
	present := map[string]bool{}
	for _, f := range cg.Funcs {
		present[f.ID] = true
	}
	ensure := func(f *fn) {
		if !present[f.ID] {
			present[f.ID] = true
			out := f.Func
			out.Unresolved = countUnresolved(f.unresolved)
			cg.Funcs = append(cg.Funcs, out)
		}
	}

	// Callers in other languages of the changed functions.
	edges := map[string]bool{}
	for _, c := range cg.Calls {
		edges[c.From+"\x00"+c.To] = true
	}
	langOf := map[string]string{}
	for _, f := range cg.Funcs {
		langOf[f.ID] = f.Lang
	}
	for _, r := range runs {
		for _, id := range sortedKeys(r.after.funcs) {
			f := r.after.funcs[id]
			for _, c := range f.calls {
				st := status[c.to]
				if st == "" || st == FuncAdded || st == FuncRemoved || langOf[c.to] == r.cl.name() || edges[f.ID+"\x00"+c.to] {
					continue
				}
				edges[f.ID+"\x00"+c.to] = true
				kind := c.kind
				if kind == CallStatic || kind == CallDynamic {
					kind = CallApprox
				}
				ensure(f)
				cg.Calls = append(cg.Calls, Call{From: f.ID, To: c.to, Kind: kind, Sites: []EdgeFile{{Path: f.Path, Line: c.line}}})
			}
		}
	}

	// Removed functions (and other removed callables) still wanted by
	// another language's calls.
	var gone []Func
	for _, f := range cg.Funcs {
		if f.Status == FuncRemoved {
			gone = append(gone, f)
		}
	}
	extraIDs := map[string]bool{}
	for _, r := range runs {
		if cx, ok := r.cl.(crossLang); ok {
			for _, f := range cx.removedExtra(base, idx, changed) {
				if !extraIDs[f.ID] && !present[f.ID] {
					extraIDs[f.ID] = true
					gone = append(gone, f)
				}
			}
		}
	}
	wants := make([]map[string][]EdgeFile, len(runs))
	for i, r := range runs {
		wants[i] = map[string][]EdgeFile{}
		for _, id := range sortedKeys(r.after.funcs) {
			f := r.after.funcs[id]
			for _, u := range f.unresolved {
				for _, w := range u.want {
					wants[i][w] = appendSite(wants[i][w], EdgeFile{Path: f.Path, Line: u.line})
				}
			}
		}
	}
	finding := map[string]int{} // removed ID → its removed-called finding
	for i, x := range cg.Findings {
		if x.Kind == FindingRemovedCalled {
			finding[x.Func] = i
		}
	}
	for _, g := range gone {
		keys := []string{g.ID, "~" + g.ID}
		for _, r := range runs {
			if cx, ok := r.cl.(crossLang); ok {
				keys = append(keys, cx.aliases(g)...)
			}
		}
		var sites []EdgeFile
		for i, r := range runs {
			if r.cl.name() == g.Lang {
				continue // matched by the language's own delta
			}
			for _, k := range keys {
				for _, s := range wants[i][k] {
					sites = appendSite(sites, s)
				}
			}
		}
		if len(sites) == 0 {
			continue
		}
		if i, ok := finding[g.ID]; ok {
			for _, s := range sites {
				cg.Findings[i].Sites = appendSite(cg.Findings[i].Sites, s)
			}
			continue
		}
		if extraIDs[g.ID] {
			present[g.ID] = true
			cg.Funcs = append(cg.Funcs, g)
		}
		finding[g.ID] = len(cg.Findings)
		cg.Findings = append(cg.Findings, CallFinding{Kind: FindingRemovedCalled, Func: g.ID, Sites: sites})
	}
	flaggedFindings(cg, runs, status, changed)
}

// fillContext replaces the bare nodes a language added for callees it
// doesn't have (no path) with the real function from the language that
// does, and drops duplicates of a function two languages both added.
func fillContext(cg *CallGraph, runs []langRun) {
	lookup := func(id string) (Func, bool) {
		for _, r := range runs {
			if f := r.after.funcs[id]; f != nil {
				out := f.Func
				out.Unresolved = countUnresolved(f.unresolved)
				return out, true
			}
		}
		for _, r := range runs {
			if f := r.before.funcs[id]; f != nil {
				out := f.Func
				out.Before = true
				return out, true
			}
		}
		for _, r := range runs {
			if cx, ok := r.cl.(crossLang); ok {
				if f, ok := cx.foreign(id); ok {
					return f, true
				}
			}
		}
		return Func{}, false
	}
	for i, f := range cg.Funcs {
		if f.Path == "" && f.Status == "" {
			if real, ok := lookup(f.ID); ok {
				cg.Funcs[i] = real
			}
		}
	}
	// One node per ID: the one with a status, else the first with a path.
	best := map[string]int{}
	for i, f := range cg.Funcs {
		j, ok := best[f.ID]
		switch {
		case !ok:
			best[f.ID] = i
		case cg.Funcs[j].Status == "" && (f.Status != "" || cg.Funcs[j].Path == "" && f.Path != ""):
			best[f.ID] = i
		}
	}
	out := cg.Funcs[:0]
	for i, f := range cg.Funcs {
		if best[f.ID] == i {
			out = append(out, f)
		}
	}
	cg.Funcs = out
}

// flaggedFindings raises the findings unresolved sites carry (hFlagger):
// for a site whose function the change touched, or when one of the files
// the site depends on changed. A route already listed under removed-called
// (its action was removed) isn't raised again; a view binding a removed
// Stimulus method is both, since the binding is what's left to fix.
func flaggedFindings(cg *CallGraph, runs []langRun, status map[string]string, changed map[string]bool) {
	removedAt := map[EdgeFile]bool{}
	for _, x := range cg.Findings {
		if x.Kind == FindingRemovedCalled {
			for _, s := range x.Sites {
				removedAt[s] = true
			}
		}
	}
	present := map[string]bool{}
	for _, f := range cg.Funcs {
		present[f.ID] = true
	}
	type key struct{ kind, fn string }
	sites := map[key][]EdgeFile{}
	var order []key
	for _, r := range runs {
		for _, id := range sortedKeys(r.after.funcs) {
			f := r.after.funcs[id]
			for _, u := range f.unresolved {
				if u.flag == "" {
					continue
				}
				relevant := status[f.ID] != "" && status[f.ID] != FuncRemoved
				for _, a := range u.about {
					relevant = relevant || changed[a]
				}
				s := EdgeFile{Path: f.Path, Line: u.line}
				if !relevant || u.flag == FindingRouteWithoutAction && removedAt[s] {
					continue
				}
				k := key{u.flag, f.ID}
				if sites[k] == nil {
					order = append(order, k)
				}
				sites[k] = appendSite(sites[k], s)
				if !present[f.ID] {
					present[f.ID] = true
					out := f.Func
					out.Unresolved = countUnresolved(f.unresolved)
					cg.Funcs = append(cg.Funcs, out)
				}
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].kind < order[j].kind })
	for _, k := range order {
		cg.Findings = append(cg.Findings, CallFinding{Kind: k.kind, Func: k.fn, Sites: sites[k]})
	}
}
