package review

import (
	"path"
	"sort"
)

// refCacher is implemented by languages whose references of a file depend
// only on its content and a repo-wide key (Go: the module path), so the
// engine can cache an unchanged file's references by blob id.
type refCacher interface {
	refsKey() string
}

// twoPhase is implemented by languages whose references resolve against the
// whole tree (Ruby constants, JS module paths): scan reads a file's raw
// references, which depend only on its content and can be cached by blob
// id; resolve turns them into unit references, cheaply, on every analysis.
type twoPhase interface {
	scan(path, src string) any
	resolve(path string, scanned any) []ref
}

// side collects one version's references of a unit's changed files:
// referenced unit → the files (and lines) referencing it.
type side map[string][]EdgeFile

func (s side) add(refs []ref, file string) {
	for _, r := range refs {
		s[r.to] = append(s[r.to], EdgeFile{Path: file, Line: r.line})
	}
}

// edgeDelta works out lang's touched units and the edges between units
// that appear or disappear. An edge only counts as added (removed) when no
// unchanged file of the unit references it as well: a reference moving
// between files isn't an architecture change. Test files never count.
func edgeDelta(idx *index, lang language, a *Analysis, files []*file, rules *ruleSet) {
	before := map[string]side{} // unit → its changed files' base references
	after := map[string]side{}
	changed := map[string]bool{} // paths (old and new) of changed files
	hasBefore := map[string]bool{}
	hasAfter := map[string]bool{}
	unparsed := map[string]bool{} // units with a file that doesn't parse
	get := func(m map[string]side, u string) side {
		if m[u] == nil {
			m[u] = side{}
		}
		return m[u]
	}
	for _, f := range files {
		changed[f.Path], changed[f.oldPath()] = true, true
		if f.before != nil && lang.owns(f.oldPath()) && !isTest(f.oldPath()) {
			if u := lang.unit(f.oldPath()); u != "" {
				hasBefore[u] = true
				if refs, ok := lang.refs(f.oldPath(), *f.before); ok {
					get(before, u).add(refs, f.Path)
				} else {
					unparsed[u] = true
				}
			}
		}
		if f.after != nil && lang.owns(f.Path) && !isTest(f.Path) {
			if u := lang.unit(f.Path); u != "" {
				hasAfter[u] = true
				if refs, ok := lang.refs(f.Path, *f.after); ok {
					get(after, u).add(refs, f.Path)
				} else {
					unparsed[u] = true
				}
			}
		}
	}
	touched := map[string]bool{}
	for u := range hasBefore {
		touched[u] = true
	}
	for u := range hasAfter {
		touched[u] = true
	}
	if len(touched) == 0 {
		return
	}

	// The unchanged files of each touched unit, and what they reference.
	unchanged := map[string]side{}
	hasUnchanged := map[string]bool{}
	cacher, _ := lang.(refCacher)
	phased, _ := lang.(twoPhase)
	for _, p := range idx.paths {
		if changed[p] || !lang.owns(p) || isTest(p) {
			continue
		}
		u := lang.unit(p)
		if !touched[u] {
			continue
		}
		hasUnchanged[u] = true
		var refs []ref
		if phased != nil {
			v := idx.symbols("scan:"+lang.name()+path.Ext(p), p, func(src string) any { return phased.scan(p, src) })
			if v != nil {
				refs = phased.resolve(p, v)
			}
		} else if cacher != nil {
			v := idx.symbols("refs:"+lang.name()+"\x00"+cacher.refsKey(), p, func(src string) any {
				rs, _ := lang.refs(p, src)
				return rs
			})
			refs, _ = v.([]ref)
		} else if src := idx.read(p); src != nil {
			refs, _ = lang.refs(p, *src)
		}
		get(unchanged, u).add(refs, p)
	}

	units := make([]string, 0, len(touched))
	for u := range touched {
		units = append(units, u)
	}
	sort.Strings(units)
	approx := !lang.exact()
	for _, u := range units {
		status := "changed"
		switch {
		case !hasBefore[u] && !hasUnchanged[u]:
			status = "added"
		case !hasAfter[u] && !hasUnchanged[u]:
			status = "removed"
		}
		a.Packages = append(a.Packages, Package{Path: u, Status: status, Lang: lang.name()})
		if unparsed[u] {
			continue // a file mid-edit would show its references as all removed
		}
		b, af, un := before[u], after[u], unchanged[u]
		for _, to := range sortedKeys(af) {
			if to == u {
				continue
			}
			if _, had := b[to]; had {
				continue
			}
			if _, kept := un[to]; kept {
				continue
			}
			e := Edge{From: u, To: to, Op: OpAdded, Files: af[to], Lang: lang.name(), Approx: approx}
			e.Violation = rules.check(u, to, lang.name())
			a.Edges = append(a.Edges, e)
		}
		for _, to := range sortedKeys(b) {
			if to == u {
				continue
			}
			if _, has := af[to]; has {
				continue
			}
			if _, kept := un[to]; kept {
				continue
			}
			e := Edge{From: u, To: to, Op: OpRemoved, Files: b[to], Lang: lang.name(), Approx: approx}
			e.Fixed = rules.check(u, to, lang.name())
			a.Edges = append(a.Edges, e)
		}
		// Everything else the unit references now, for context.
		if status == "removed" {
			continue
		}
		now := map[string]bool{}
		for to := range af {
			now[to] = true
		}
		for to := range un {
			now[to] = true
		}
		for _, to := range sortedKeys(now) {
			if to == u {
				continue
			}
			if _, had := b[to]; !had {
				if _, kept := un[to]; !kept {
					continue // added: already in Edges
				}
			}
			a.Existing = append(a.Existing, Edge{From: u, To: to, Lang: lang.name(), Approx: approx})
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
