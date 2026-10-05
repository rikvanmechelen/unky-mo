package review

import (
	"sort"
	"strings"
)

// The Ruby call graph's Rails links that aren't Ruby calls: routes (from
// config/routes.rb, rails_routes.go) as definitions calling their actions,
// their URL helpers as definitions the views and controllers call, and
// Stimulus bindings from views into JS controllers (stimulus.go).

const (
	rbRouteDef  = "<route>"  // a route; Owner is its identity, "GET /tickets/:id"
	rbHelperDef = "<helper>" // a route's URL helper; Owner is its name, "ticket"
)

// rbRails is one version's Rails context for resolving: route helpers,
// controller paths and Stimulus controllers.
type rbRails struct {
	idx     *index
	helpers map[string]string // helper name → its definition's ID
	stim    *stimIndex
}

// rails is r's version's context, built once per resolver.
func (l *rbCalls) rails(r *hResolver) *rbRails {
	if l.ext != nil && l.ext.idx == r.idx {
		return l.ext
	}
	x := &rbRails{idx: r.idx, helpers: map[string]string{}}
	for _, p := range r.paths {
		for _, d := range r.files[p].Defs {
			if d.Name == rbHelperDef {
				x.helpers[d.Owner] = "route:" + d.Owner
			}
		}
	}
	x.stim = stimulusIndex(r.idx)
	l.ext = x
	return x
}

// extraDefs turns the version's routes into definitions in the routes
// files: one per route identity (calling each action it routes to; its
// body hash is the targets, so retargeting it is a change), and one per
// URL helper (calling the routes it names).
func (l *rbCalls) extraDefs(idx *index) map[string][]hDef {
	rs := railsRoutesOf(idx)
	if rs == nil {
		return nil
	}
	out := map[string][]hDef{}
	byID, order := routesByIdentity(rs)
	helpers := map[string]*hDef{}
	var helperOrder []string
	helperFile := map[string]string{}
	for _, k := range order {
		routes := byID[k]
		first := routes[0]
		d := hDef{Name: rbRouteDef, Owner: k, Line: first.Line, End: first.Line,
			Body: hashOf(strings.Join(routeTargets(routes), "\n"))}
		seen := map[string]bool{}
		for _, rt := range routes {
			if t := rt.target(); !seen[t] {
				seen[t] = true
				d.Calls = append(d.Calls, hCall{Recv: rt.Controller + ".new", Name: rt.Action, Line: rt.Line})
			}
			if rt.Helper == "" {
				continue
			}
			h := helpers[rt.Helper]
			if h == nil {
				h = &hDef{Name: rbHelperDef, Owner: rt.Helper, Line: rt.Line, End: rt.Line}
				helpers[rt.Helper] = h
				helperOrder = append(helperOrder, rt.Helper)
				helperFile[rt.Helper] = rt.File
			}
			h.Calls = append(h.Calls, hCall{Recv: rbRouteDef, Name: k, Line: rt.Line})
			h.Body += k + "\n"
		}
		out[first.File] = append(out[first.File], d)
	}
	for _, name := range helperOrder {
		h := helpers[name]
		h.Body = hashOf(h.Body)
		out[helperFile[name]] = append(out[helperFile[name]], *h)
	}
	return out
}

// routeHelperBase is a URL helper call's route name: ticket_path → ticket.
func routeHelperBase(name string) (string, bool) {
	for _, suf := range []string{"_path", "_url"} {
		if b, ok := strings.CutSuffix(name, suf); ok && b != "" {
			return b, true
		}
	}
	return "", false
}

// railsCall resolves the calls rbrails adds and the URL helper calls; ok is
// false for any other call.
func (l *rbCalls) railsCall(r *hResolver, d *hDef, c hCall) (string, []string, bool) {
	switch {
	case d.Name == rbRouteDef:
		to, want := l.routeTarget(r, c)
		return to, want, true
	case d.Name == rbHelperDef:
		return "route:" + c.Name, nil, true
	case strings.HasPrefix(c.Recv, stimRecv):
		to, want := l.stimulusCall(r, c)
		return to, want, true
	}
	base, ok := routeHelperBase(c.Name)
	if !ok || len(r.byName[c.Name]) > 0 || c.Recv != "" && c.Recv != "self" && c.Recv != "?" {
		return "", nil, false // a method the repo defines wins
	}
	if id := l.rails(r).helpers[base]; id != "" {
		return id, nil, true
	}
	// Quiet: Rails and gems define helpers too (polymorphic_path, devise's);
	// a helper the routes no longer define is still found by its ID.
	return "", []string{"~route:" + base}, true
}

// routeTarget resolves a route's action: the controller's method (or an
// ancestor's), else the template it renders without one. A controller
// that isn't in the repo (a framework or gem controller) is a quiet want.
func (l *rbCalls) routeTarget(r *hResolver, c hCall) (string, []string) {
	l.prepare(r)
	ctrl := strings.TrimSuffix(c.Recv, ".new")
	cp, files := l.constant("", ctrl)
	if len(files) > 0 {
		if id, _ := l.find(r, files, cp, c.Name); id != "" {
			return id, nil
		}
	}
	if tpl := templateFor(r.idx, rbUnderscore(strings.TrimSuffix(ctrl, "Controller")), c.Name); tpl != "" {
		return "view:" + tpl, nil
	}
	if len(files) == 0 {
		return "", []string{"~" + ctrl + "#" + c.Name}
	}
	return "", l.miss(r, l.chain(r, files, cp), c.Name)
}

// flag: a route whose action doesn't resolve while its controller is in
// the repo (and doesn't inherit from a controller outside it) is
// route-without-action; a binding to a method or target a Stimulus
// controller doesn't have is stimulus-unbound.
func (l *rbCalls) flag(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	switch {
	case d.Name == rbRouteDef:
		l.prepare(r)
		cp, files := l.constant("", strings.TrimSuffix(c.Recv, ".new"))
		if len(files) == 0 {
			return "", nil
		}
		for _, cr := range l.chain(r, files, cp) {
			for _, b := range r.classes[cr.path][cr.owner] {
				if _, ok := l.base(cr.path, b); !ok && strings.HasSuffix(b, "Controller") {
					return "", nil // its actions may come from a gem's controller
				}
			}
		}
		return FindingRouteWithoutAction, files
	case strings.HasPrefix(c.Recv, stimRecv):
		ctl := l.rails(r).stim.byIdent[strings.TrimPrefix(c.Recv, stimRecv)]
		if ctl == nil || ctl.class == "" || ctl.inherits {
			return "", nil
		}
		switch {
		case strings.HasPrefix(c.Name, stimAction) && ctl.methods[strings.TrimPrefix(c.Name, stimAction)] == nil,
			strings.HasPrefix(c.Name, stimTarget) && !ctl.targets[strings.TrimPrefix(c.Name, stimTarget)]:
			return FindingStimulusUnbound, []string{ctl.path}
		}
	}
	return "", nil
}

// stimulusCall resolves a binding: data-controller to the controller class,
// an action to its method, a target to nothing when the controller
// declares it. A controller that isn't in the repo (registered from a
// package) is a quiet want.
func (l *rbCalls) stimulusCall(r *hResolver, c hCall) (string, []string) {
	ident := strings.TrimPrefix(c.Recv, stimRecv)
	ctl := l.rails(r).stim.byIdent[ident]
	known := ctl != nil && ctl.class != ""
	switch {
	case c.Name == stimController:
		if !known || ctl.def == nil {
			return "", []string{"~" + stimRecv + ident}
		}
		return ctl.id(ctl.def), nil
	case strings.HasPrefix(c.Name, stimAction):
		m := strings.TrimPrefix(c.Name, stimAction)
		if known {
			if d := ctl.methods[m]; d != nil {
				return ctl.id(d), nil
			}
		}
		if !known || ctl.inherits {
			return "", []string{"~" + stimRecv + ident + "#" + m}
		}
		return "", []string{ctl.id(&hDef{Owner: ctl.class, Name: m})}
	case strings.HasPrefix(c.Name, stimTarget):
		t := strings.TrimPrefix(c.Name, stimTarget)
		id := stimTarget + ident + "." + t
		switch {
		case !known || ctl.inherits:
			return "", []string{"~" + id}
		case ctl.targets[t]:
			return "", nil
		}
		return "", []string{id}
	}
	return "", nil
}

// --- hCross: the Ruby side of the cross-language pass ---

// crossCalls: views bind Stimulus controllers, so the Ruby side is built
// for a change that only touches JS when there are any.
func (l *rbCalls) crossCalls(idx *index) bool {
	for _, p := range idx.paths {
		if _, ok := stimulusIdentifier(p); ok {
			return true
		}
	}
	return false
}

// foreign describes a Stimulus controller's class or method a binding
// resolved to.
func (l *rbCalls) foreign(id string) (Func, bool) {
	if l.ext == nil {
		return Func{}, false
	}
	stem, q, ok := strings.Cut(id, "#")
	if !ok {
		return Func{}, false
	}
	ctl := l.ext.stim.byStem[stem]
	if ctl == nil {
		return Func{}, false
	}
	for i := range ctl.file.Defs {
		d := &ctl.file.Defs[i]
		if qualify(d.Owner, d.Name) == q {
			js := &jsCalls{}
			return Func{ID: id, Name: js.display(ctl.path, d), Path: ctl.path, Line: d.Line, End: d.End,
				Unit: (&nodeLang{}).unit(ctl.path), Lang: js.name()}, true
		}
	}
	return Func{}, false
}

// aliases: a removed function of a Stimulus controller file is also wanted
// as "~stimulus:<identifier>[#method]" by bindings whose controller is gone.
func (l *rbCalls) aliases(f Func) []string {
	if f.Lang != (&jsCalls{}).name() {
		return nil
	}
	ident, ok := stimulusIdentifier(f.Path)
	if !ok {
		return nil
	}
	_, q, _ := strings.Cut(f.ID, "#")
	parts := strings.Split(q, ".")
	switch len(parts) {
	case 1:
		return []string{"~" + stimRecv + ident}
	case 2:
		return []string{"~" + stimRecv + ident + "#" + parts[1]}
	}
	return nil
}

// removedExtra lists the static targets the change removed from its
// controllers: a target a view still binds is removed-called.
func (l *rbCalls) removedExtra(base, idx *index, changed map[string]bool) []Func {
	touched := false
	for p := range changed {
		if _, ok := stimulusIdentifier(p); ok {
			touched = true
			break
		}
	}
	if !touched {
		return nil
	}
	before, after := stimulusIndex(base), stimulusIndex(idx)
	var out []Func
	for _, ident := range sortedKeys(before.byIdent) {
		old := before.byIdent[ident]
		if !changed[old.path] {
			continue
		}
		cur := after.byIdent[ident]
		var names []string
		for t := range old.targets {
			if cur == nil || !cur.targets[t] {
				names = append(names, t)
			}
		}
		sort.Strings(names)
		line := 1
		if old.def != nil {
			line = old.def.Line
		}
		for _, t := range names {
			out = append(out, Func{ID: stimTarget + ident + "." + t, Name: ident + "." + t + " (target)", Path: old.path,
				Line: line, Before: true, Unit: (&nodeLang{}).unit(old.path), Lang: (&jsCalls{}).name(), Status: FuncRemoved})
		}
	}
	return out
}
