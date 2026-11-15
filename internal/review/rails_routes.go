package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Rails routes, read from config/routes.rb (and the files it draws) the way
// ActionDispatch's mapper builds them, without Ruby: a small tokenizer, a
// statement parser for the routing DSL, and an evaluator that keeps the
// mapper's scope stack (path, module, name prefix, controller, the parent
// resource and its scope level). What it can't read (an engine mount, a
// redirect, a lambda endpoint, devise_for, Ruby it doesn't know) is counted
// in Skipped, never guessed.

// railsRoute is one route: a verb and a path to a controller action.
type railsRoute struct {
	Verb string // GET, POST, …, ANY for via: :all
	Path string // "/admin/tickets/:id", without (.:format)
	// Controller is the constant ("Admin::TicketsController"), CtrlPath
	// the controller as Rails names it ("admin/tickets").
	Controller, CtrlPath string
	Action               string
	// Helper is the route's name without _path/_url ("admin_ticket"), ""
	// when it has none.
	Helper string
	File   string
	Line   int
}

// railsSkip is a routing statement the parser didn't read.
type railsSkip struct {
	Text string
	File string
	Line int
}

// routeSet is a version's routes.
type routeSet struct {
	Routes  []railsRoute
	Skipped []railsSkip
}

// routesFile is the main routes file; draw(:x) reads routesDir/x.rb.
const (
	routesFile = "config/routes.rb"
	routesDir  = "config/routes/"
)

// isRoutesFile reports whether p is a file the routes parser reads.
func isRoutesFile(p string) bool {
	return p == routesFile || strings.HasPrefix(p, routesDir) && strings.HasSuffix(p, ".rb")
}

// railsRoutesOf parses idx's routes, cached by the blob ids of the routes
// files (and config/application.rb, for api_only). nil when there's no
// config/routes.rb.
func railsRoutesOf(idx *index) *routeSet {
	if !idx.has(routesFile) {
		return nil
	}
	files := append([]string{routesFile, "config/application.rb"}, idx.under("config/routes")...)
	var key strings.Builder
	key.WriteString("rails-routes")
	cacheable := true
	for _, p := range files {
		if !idx.has(p) {
			continue
		}
		oid := idx.blobs[p]
		if oid == "" {
			cacheable = false
			break
		}
		key.WriteString("\x00" + p + "\x00" + oid)
	}
	if cacheable {
		if v, ok := symCache.get(key.String()); ok {
			rs, _ := v.(*routeSet)
			return rs
		}
	}
	read := func(p string) (string, bool) {
		if !idx.has(p) {
			return "", false
		}
		if s := idx.read(p); s != nil {
			return *s, true
		}
		return "", false
	}
	main, ok := read(routesFile)
	if !ok {
		return nil
	}
	apiOnly := false
	if app, ok := read("config/application.rb"); ok {
		apiOnly = apiOnlyRe.MatchString(rubyCode(app))
	}
	rs := parseRailsRoutes(main, read, apiOnly)
	if cacheable {
		symCache.put(key.String(), rs)
	}
	return rs
}

var apiOnlyRe = regexp.MustCompile(`\bapi_only\s*=\s*true\b`)

// parseRailsRoutes reads the routes in main (config/routes.rb's text).
// read returns another routes file's text (for draw); apiOnly drops the
// new and edit actions, as config.api_only does.
func parseRailsRoutes(main string, read func(p string) (string, bool), apiOnly bool) *routeSet {
	e := &rtEval{read: read, apiOnly: apiOnly, named: map[string]bool{}, concerns: map[string]rtConcern{},
		drawn: map[string]bool{routesFile: true}, out: &routeSet{}, using: map[string]bool{}, budget: rtBudget}
	e.file(routesFile, main, rtScope{})
	return e.out
}

// --- Tokens ---

// --- Statements ---

// --- Evaluation ---

// routeIdentity is how the surface and the call graph name a route.
func (r railsRoute) identity() string { return r.Verb + " " + r.Path }

// target is the route's controller action.
func (r railsRoute) target() string { return r.Controller + "#" + r.Action }

// routesByIdentity groups a set's routes by identity, in order.
func routesByIdentity(rs *routeSet) (map[string][]railsRoute, []string) {
	out := map[string][]railsRoute{}
	var order []string
	if rs == nil {
		return out, nil
	}
	for _, r := range rs.Routes {
		k := r.identity()
		if out[k] == nil {
			order = append(order, k)
		}
		out[k] = append(out[k], r)
	}
	return out, order
}

// routeTargets lists a route identity's distinct targets, sorted.
func routeTargets(rs []railsRoute) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rs {
		if t := r.target(); !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// routeDetail is a route's surface detail: → Controller#action (helper_path).
func routeDetail(rs []railsRoute) string {
	d := "→ " + strings.Join(routeTargets(rs), ", ")
	for _, r := range rs {
		if r.Helper != "" {
			return d + " (" + r.Helper + "_path)"
		}
	}
	return d
}

// routeChanges diffs two versions' routes by identity (VERB path): added,
// removed, and retargeted. Statements the parser didn't read are diffed by
// their text.
func routeChanges(before, after *routeSet) []Change {
	b, _ := routesByIdentity(before)
	a, order := routesByIdentity(after)
	var out []Change
	for _, k := range order {
		rs := a[k]
		old, had := b[k]
		switch {
		case !had:
			out = append(out, Change{Op: OpAdded, Name: k, Detail: routeDetail(rs), Path: rs[0].File, Line: rs[0].Line})
		case strings.Join(routeTargets(old), ",") != strings.Join(routeTargets(rs), ","):
			out = append(out, Change{Op: OpChanged, Name: k, Detail: routeDetail(rs) + " (was " + strings.Join(routeTargets(old), ", ") + ")", Path: rs[0].File, Line: rs[0].Line})
		}
	}
	_, bOrder := routesByIdentity(before)
	for _, k := range bOrder {
		if _, has := a[k]; !has {
			rs := b[k]
			out = append(out, Change{Op: OpRemoved, Name: k, Detail: routeDetail(rs), Path: rs[0].File, Line: rs[0].Line})
		}
	}
	skipped := func(rs *routeSet) map[string]railsSkip {
		m := map[string]railsSkip{}
		if rs != nil {
			for _, s := range rs.Skipped {
				if _, ok := m[s.Text]; !ok && s.Text != "" {
					m[s.Text] = s
				}
			}
		}
		return m
	}
	sb, sa := skipped(before), skipped(after)
	for t, s := range sa {
		if _, ok := sb[t]; !ok {
			out = append(out, Change{Op: OpAdded, Name: t, Detail: "route not read", Path: s.File, Line: s.Line})
		}
	}
	for t, s := range sb {
		if _, ok := sa[t]; !ok {
			out = append(out, Change{Op: OpRemoved, Name: t, Detail: "route not read", Path: s.File, Line: s.Line})
		}
	}
	return out
}

// routesChanged reports whether a change touches the routes files.
func routesChanged(files []*file) bool {
	for _, f := range files {
		if isRoutesFile(f.Path) || isRoutesFile(f.oldPath()) {
			return true
		}
	}
	return false
}

// templateFor is the template an action renders implicitly, "" if none:
// app/views/<controller path>/<action>.*.
func templateFor(idx *index, ctrlPath, action string) string {
	dir := path.Join("app/views", ctrlPath)
	for _, p := range idx.under(dir) {
		if path.Dir(p) != dir {
			continue
		}
		base := path.Base(p)
		if strings.HasPrefix(base, action+".") {
			return p
		}
	}
	return ""
}
