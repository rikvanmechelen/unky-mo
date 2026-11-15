package review

import (
	"fmt"
	"regexp"
	"strings"
)

// rtRes is a resource whose scope routes are being defined in.
type rtRes struct {
	singular                bool
	path, controller, param string
	memberName, collectName string
	shallow                 bool
	shallowDepth            int
	actions                 map[string]bool
}

func (r *rtRes) memberScope() string {
	if r.singular {
		return r.path
	}
	return r.path + "/:" + r.param
}

func (r *rtRes) nestedScope() string {
	if r.singular {
		return r.path
	}
	return r.path + "/:" + r.memberName + "_" + r.param
}

// rtScope is the mapper's scope: what a route defined here inherits.
type rtScope struct {
	path, shallowPath, as, shallowPrefix, module, controller, action string
	shallow                                                          bool
	only, except                                                     *rtVal // scope only:/except: for the resources inside
	level                                                            string // "", resources, resource, member, collection, new, nested, root
	res                                                              *rtRes
	vars                                                             map[string]string // block parameters bound by a literal loop
}

type rtConcern struct {
	body []*rtStmt
	file string
}

type rtEval struct {
	read     func(p string) (string, bool)
	apiOnly  bool
	named    map[string]bool
	concerns map[string]rtConcern
	drawn    map[string]bool
	out      *routeSet
	cur      string // the file being read
	lines    []string
	// using holds the concerns being expanded, so a concern that uses
	// itself (directly or through another) isn't expanded forever.
	using map[string]bool
	// budget is how many more statements may be evaluated. Loops over word
	// lists multiply and drawn files can draw each other more than once, so
	// a small file can describe millions of routes; past the budget the
	// rest is dropped and reported once (over).
	budget int
	over   bool
}

// rtBudget is the most statements one routes evaluation runs: a large
// app's routes are a few thousand.
const rtBudget = 50_000

// file evaluates one routes file's statements in scope sc.
func (e *rtEval) file(p, src string, sc rtScope) {
	prevFile, prevLines := e.cur, e.lines
	e.cur, e.lines = p, strings.Split(src, "\n")
	ps := &rtParser{t: rtTokens(src)}
	list := ps.stmts(false)
	if ps.tooDeep {
		e.out.Skipped = append(e.out.Skipped, railsSkip{
			Text: fmt.Sprintf("nested more than %d levels deep: the rest of the file isn't read", rtMaxNesting), File: p})
	}
	e.stmts(list, sc)
	e.cur, e.lines = prevFile, prevLines
}

func (e *rtEval) stmts(list []*rtStmt, sc rtScope) {
	for _, st := range list {
		e.stmt(st, sc)
	}
}

// skip records a statement the parser couldn't read.
func (e *rtEval) skip(st *rtStmt) {
	text := ""
	if st.line-1 < len(e.lines) {
		text = strings.TrimSpace(e.lines[st.line-1])
	}
	e.out.Skipped = append(e.out.Skipped, railsSkip{Text: text, File: e.cur, Line: st.line})
}

// rtIgnored are statements that define no routes and aren't a gap.
var rtIgnored = map[string]bool{"require": true, "require_relative": true, "include": true, "extend": true,
	"puts": true, "raise": true, "return": true, "next": true, "break": true}

// rtTransparent are block methods whose routes count as if outside them.
var rtTransparent = map[string]bool{"constraints": true, "defaults": true, "with_options": true, "devise_scope": true,
	"authenticate": true, "authenticated": true, "unauthenticated": true, "if": true, "unless": true,
	"case": true, "begin": true, "while": true, "until": true, "for": true, "format": true}

func (e *rtEval) stmt(st *rtStmt, sc rtScope) {
	if e.budget <= 0 {
		if !e.over {
			e.over = true
			e.out.Skipped = append(e.out.Skipped, railsSkip{
				Text: fmt.Sprintf("more than %d routing statements: the rest isn't read", rtBudget), File: e.cur, Line: st.line})
		}
		return
	}
	e.budget--
	if st.assign || st.name == "" && st.recvVal.kind == 0 {
		return
	}
	if st.recvVal.kind == 'a' && (st.name == "each" || st.name == "each_with_index") && st.hasBlock && len(st.params) > 0 {
		// %w[a b].each do |x| … end: once per word, with x bound.
		for _, w := range st.recvVal.list {
			s := sc
			s.vars = map[string]string{}
			for k, v := range sc.vars {
				s.vars[k] = v
			}
			s.vars[st.params[0]] = w.s
			e.stmts(st.block, s)
		}
		return
	}
	if st.recv != "" || st.recvVal.kind != 0 {
		if st.name == "draw" && st.hasBlock { // Rails.application.routes.draw do
			e.stmts(st.block, sc)
			return
		}
		e.skip(st)
		return
	}
	switch st.name {
	case "draw":
		if st.hasBlock {
			e.stmts(st.block, sc)
			return
		}
		name := e.str(sc, first(st.args))
		p := routesDir + name + ".rb"
		src, ok := e.read(p)
		if name == "" || !ok || e.drawn[p] {
			e.skip(st)
			return
		}
		e.drawn[p] = true
		e.file(p, src, sc)
		delete(e.drawn, p)
	case "namespace":
		name := e.str(sc, first(st.args))
		if name == "" {
			e.skip(st)
			return
		}
		p := e.optStr(sc, st, "path", name)
		as := e.optStr(sc, st, "as", name)
		s := sc
		s.path = rtMergePath(sc.path, p)
		s.module = rtMergeModule(sc.module, e.optStr(sc, st, "module", name))
		s.as = rtMergeAs(sc.as, as)
		s.shallowPath = rtMergePath(sc.shallowPath, e.optStr(sc, st, "shallow_path", p))
		s.shallowPrefix = rtMergeAs(sc.shallowPrefix, e.optStr(sc, st, "shallow_prefix", as))
		e.stmts(st.block, s)
	case "scope":
		s := e.scope(sc, st)
		var parts []string
		for _, a := range st.args {
			if v := e.str(sc, a); v != "" {
				parts = append(parts, v)
			}
		}
		if len(parts) > 0 {
			s.path = rtMergePath(s.path, strings.Join(parts, "/"))
		}
		e.stmts(st.block, s)
	case "controller":
		s := e.scope(sc, st)
		if c := e.str(sc, first(st.args)); c != "" {
			s.controller = c
		}
		e.stmts(st.block, s)
	case "shallow":
		s := sc
		s.shallow = true
		e.stmts(st.block, s)
	case "concern":
		if name := e.str(sc, first(st.args)); name != "" && st.hasBlock {
			e.concerns[name] = rtConcern{body: st.block, file: e.cur}
		} else {
			e.skip(st)
		}
	case "concerns":
		e.useConcerns(st, sc, st.args)
	case "resources", "resource":
		e.resources(st, sc, st.name == "resource")
	case "member", "collection", "new":
		if sc.res == nil {
			e.skip(st)
			return
		}
		e.stmts(st.block, e.enter(sc, st.name))
	case "get", "post", "put", "patch", "delete", "options", "head", "match":
		e.verbStmt(st, sc)
	case "root":
		e.root(st, sc)
	default:
		switch {
		case rtTransparent[st.name] && st.hasBlock:
			e.stmts(st.block, sc)
		case rtIgnored[st.name]:
		case st.hasBlock && len(st.block) > 0 && st.name != "direct" && st.name != "resolve":
			// An unknown block method (a project's own subdomain helper):
			// its routes count; the method itself is a gap only if it maps
			// something on its own, which it can't be known to.
			e.stmts(st.block, sc)
		default:
			e.skip(st)
		}
	}
}

// scope applies scope options (path:, module:, as:, controller:,
// shallow_path:, shallow_prefix:, shallow:, only:/except:).
func (e *rtEval) scope(sc rtScope, st *rtStmt) rtScope {
	s := sc
	if v, ok := st.opts["path"]; ok {
		s.path = rtMergePath(s.path, e.str(sc, v))
	}
	if v, ok := st.opts["module"]; ok {
		s.module = rtMergeModule(s.module, e.str(sc, v))
	}
	if v, ok := st.opts["as"]; ok {
		s.as = rtMergeAs(s.as, e.str(sc, v))
	}
	if v, ok := st.opts["controller"]; ok {
		s.controller = e.str(sc, v)
	}
	if v, ok := st.opts["action"]; ok {
		s.action = e.str(sc, v)
	}
	if v, ok := st.opts["shallow_path"]; ok {
		s.shallowPath = rtMergePath(s.shallowPath, e.str(sc, v))
	}
	if v, ok := st.opts["shallow_prefix"]; ok {
		s.shallowPrefix = rtMergeAs(s.shallowPrefix, e.str(sc, v))
	}
	if v, ok := st.opts["shallow"]; ok {
		s.shallow = v.kind == 'b'
	}
	_, o := st.opts["only"]
	_, x := st.opts["except"]
	if o || x {
		s.only, s.except = nil, nil
		if v, ok := st.opts["only"]; ok {
			s.only = &v
		}
		if v, ok := st.opts["except"]; ok {
			s.except = &v
		}
	}
	return s
}

func (e *rtEval) useConcerns(st *rtStmt, sc rtScope, names []rtVal) {
	for _, v := range names {
		for _, n := range e.strs(sc, v) {
			c, ok := e.concerns[n]
			if !ok || e.using[n] {
				e.skip(st)
				continue
			}
			e.using[n] = true
			prev := e.cur
			e.cur = c.file
			e.stmts(c.body, sc)
			e.cur = prev
			delete(e.using, n)
		}
	}
}

// resources follows the mapper's resources/resource.
func (e *rtEval) resources(st *rtStmt, sc rtScope, singular bool) {
	var names []string
	for _, a := range st.args {
		names = append(names, e.strs(sc, a)...)
	}
	if len(names) == 0 {
		e.skip(st)
		return
	}
	for _, name := range names {
		s := sc
		if v, ok := st.opts["shallow"]; ok && v.kind == 'b' {
			s.shallow = true
		}
		if s.level == "resources" || s.level == "resource" {
			s = e.enter(s, "nested")
		}
		// Scope options: everything but the resource's own.
		if v, ok := st.opts["module"]; ok {
			s.module = rtMergeModule(s.module, e.str(sc, v))
		}
		if v, ok := st.opts["shallow_path"]; ok {
			s.shallowPath = rtMergePath(s.shallowPath, e.str(sc, v))
		}
		if v, ok := st.opts["shallow_prefix"]; ok {
			s.shallowPrefix = rtMergeAs(s.shallowPrefix, e.str(sc, v))
		}
		r := &rtRes{singular: singular, param: e.optStr(sc, st, "param", "id")}
		as := e.optStr(sc, st, "as", name)
		r.path = e.optStr(sc, st, "path", name)
		if singular {
			r.memberName, r.collectName = as, as
			r.controller = e.optStr(sc, st, "controller", pluralize(name))
		} else {
			r.memberName = as
			if !rtUncountable[as] {
				r.memberName = singularize(as)
			}
			r.collectName = as
			if r.memberName == as {
				r.collectName = as + "_index"
			}
			r.controller = e.optStr(sc, st, "controller", name)
		}
		r.shallow = s.shallow && !singular
		if s.res != nil {
			r.shallowDepth = s.res.shallowDepth
		}
		if r.shallow {
			r.shallowDepth++
		}
		r.actions = e.actions(sc, st, singular)
		rs := s
		rs.level = "resources"
		if singular {
			rs.level = "resource"
		}
		rs.res = r
		rs.controller = r.controller
		e.stmts(st.block, rs)
		if v, ok := st.opts["concerns"]; ok {
			e.useConcerns(st, rs, []rtVal{v})
		}
		line := st.line
		add := func(level, verb, action string) {
			if r.actions[action] {
				e.route(e.enter(rs, level), []string{verb}, action, true, "", false, rtVal{}, nil, line)
			}
		}
		if singular {
			add("new", "GET", "new")
			add("member", "GET", "edit")
			add("member", "GET", "show")
			add("member", "PATCH", "update")
			add("member", "PUT", "update")
			add("member", "DELETE", "destroy")
			add("collection", "POST", "create")
		} else {
			add("collection", "GET", "index")
			add("collection", "POST", "create")
			add("new", "GET", "new")
			add("member", "GET", "edit")
			add("member", "GET", "show")
			add("member", "PATCH", "update")
			add("member", "PUT", "update")
			add("member", "DELETE", "destroy")
		}
	}
}

// actions is a resource's default actions after only:/except: (its own,
// or the enclosing scope's).
func (e *rtEval) actions(sc rtScope, st *rtStmt, singular bool) map[string]bool {
	all := []string{"index", "create", "new", "show", "update", "destroy", "edit"}
	if singular {
		all = all[1:]
	}
	only, hasOnly := st.opts["only"]
	except, hasExcept := st.opts["except"]
	if !hasOnly && !hasExcept {
		if sc.only != nil {
			only, hasOnly = *sc.only, true
		}
		if sc.except != nil {
			except, hasExcept = *sc.except, true
		}
	}
	out := map[string]bool{}
	if hasOnly {
		for _, a := range e.strs(sc, only) {
			out[a] = true
		}
		if only.kind == 'y' && only.s == "all" {
			hasOnly = false
		}
	}
	if !hasOnly {
		for _, a := range all {
			out[a] = true
		}
	}
	if hasExcept {
		for _, a := range e.strs(sc, except) {
			delete(out, a)
		}
	}
	if e.apiOnly {
		delete(out, "new")
		delete(out, "edit")
	}
	return out
}

// enter is the scope for routes at a level of sc's resource.
func (e *rtEval) enter(sc rtScope, level string) rtScope {
	r := sc.res
	s := sc
	s.level = level
	shallow := sc.shallow && r != nil && !r.singular
	switch level {
	case "collection":
		s.path = rtMergePath(sc.path, r.path)
	case "new":
		s.path = rtMergePath(sc.path, r.path+"/new")
	case "member":
		if shallow {
			s.path, s.as = rtMergePath(sc.shallowPath, r.memberScope()), sc.shallowPrefix
		} else {
			s.path = rtMergePath(sc.path, r.memberScope())
		}
	case "nested":
		base, as := sc.path, sc.as
		if shallow && r.shallowDepth >= 1 {
			base, as = sc.shallowPath, sc.shallowPrefix
		}
		s.path = rtMergePath(base, r.nestedScope())
		s.as = rtMergeAs(as, r.memberName)
	}
	return s
}

var rtVerbs = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "options": "OPTIONS", "head": "HEAD"}

// verbStmt reads get/post/…/match: each path (a string, or a symbol
// naming the action) and "path" => target pair is one route.
func (e *rtEval) verbStmt(st *rtStmt, sc rtScope) {
	var verbs []string
	if v, ok := rtVerbs[st.name]; ok {
		verbs = []string{v}
	} else {
		via, ok := st.opts["via"]
		if !ok {
			e.skip(st)
			return
		}
		for _, v := range e.strs(sc, via) {
			if v == "all" {
				verbs = append(verbs, "ANY")
			} else {
				verbs = append(verbs, strings.ToUpper(v))
			}
		}
	}
	if len(verbs) == 0 {
		e.skip(st)
		return
	}
	to := st.opts["to"]
	for _, a := range st.args {
		switch a.kind {
		case 'y':
			e.decomposed(st, sc, verbs, a.s, true, to)
		case 's', 'i':
			p := e.str(sc, a)
			if a.kind == 'i' && p == "" {
				e.skip(st) // a variable the parser can't follow
				continue
			}
			e.decomposed(st, sc, verbs, p, false, to)
		default:
			e.skip(st)
		}
	}
	for _, pr := range st.pairs {
		p := e.str(sc, pr.key)
		if p == "" && pr.key.kind != 's' {
			e.skip(st)
			continue
		}
		e.decomposed(st, sc, verbs, p, false, pr.val)
	}
	if len(st.args) == 0 && len(st.pairs) == 0 {
		e.skip(st)
	}
}

// decomposed is the mapper's decomposed_match: on: picks the resource
// level, else a route inside resources is nested and inside a singular
// resource a member route.
func (e *rtEval) decomposed(st *rtStmt, sc rtScope, verbs []string, p string, sym bool, to rtVal) {
	if on, ok := st.opts["on"]; ok && sc.res != nil {
		sc = e.enter(sc, e.str(sc, on))
	} else if sc.level == "resources" {
		sc = e.enter(sc, "nested")
	} else if sc.level == "resource" {
		sc = e.enter(sc, "member")
	}
	pathOpt, hasPath := "", false
	if v, ok := st.opts["path"]; ok && sym {
		pathOpt, hasPath = e.str(sc, v), true
	}
	if !sym {
		pathOpt, hasPath = p, true
	}
	e.route(sc, verbs, p, sym, pathOpt, hasPath, to, st, st.line)
}

var (
	rtWordPath  = regexp.MustCompile(`^[\w\-/]+$`)
	rtShorthand = regexp.MustCompile(`^/?[-\w]+/[-\w/]+$`)
)

// route is the mapper's add_route: action is the path as written (or the
// symbol), p the path to add to the scope's.
func (e *rtEval) route(sc rtScope, verbs []string, action string, sym bool, p string, hasPath bool, to rtVal, st *rtStmt, line int) {
	opt := func(k string) (rtVal, bool) {
		if st == nil {
			return rtVal{}, false
		}
		v, ok := st.opts[k]
		return v, ok
	}
	full := ""
	switch {
	case hasPath:
		full = rtMergePath(sc.path, p)
	case rtCanonical(sc.level, action):
		full = rtMergePath(sc.path, "")
	default:
		full = rtMergePath(sc.path, action)
	}
	defaultAction := sc.action
	if v, ok := opt("action"); ok {
		defaultAction = e.str(sc, v)
	}
	if rtWordPath.MatchString(action) {
		if defaultAction == "" && !strings.Contains(action, "/") {
			defaultAction = strings.ReplaceAll(action, "-", "_")
		}
	} else {
		action = ""
	}
	// The name.
	helper := ""
	if v, ok := opt("as"); ok {
		if v.kind != 'n' && v.kind != 'f' {
			helper = e.nameFor(sc, e.str(sc, v), action, true)
		}
	} else {
		helper = e.nameFor(sc, "", action, false)
	}
	// The target.
	controller := sc.controller
	if v, ok := opt("controller"); ok {
		controller = e.str(sc, v)
	}
	act := defaultAction
	if to.kind == 0 && !sym && st != nil {
		if _, ok := st.opts["action"]; !ok {
			if q := strings.TrimSuffix(p, "(.:format)"); rtShorthand.MatchString(q) {
				q = strings.TrimPrefix(q, "/")
				i := strings.LastIndexByte(q, '/')
				to = rtVal{kind: 's', s: strings.ReplaceAll(q[:i]+"#"+q[i+1:], "-", "_")}
			}
		}
	}
	if to.kind == 'i' {
		if s := e.str(sc, to); s != "" {
			to = rtVal{kind: 's', s: s}
		}
	}
	switch to.kind {
	case 0:
	case 's':
		s := e.str(sc, to)
		c, a, ok := strings.Cut(s, "#")
		if !ok {
			if st != nil {
				e.skip(st)
			}
			return
		}
		if c != "" {
			controller = c
		}
		act = a
	case 'y':
		act = to.s
	default: // redirect(…), a lambda, a Rack app
		if st != nil {
			e.skip(st)
		}
		return
	}
	if controller == "" || act == "" || strings.Contains(controller, "#{") || strings.Contains(act, "#{") {
		if st != nil {
			e.skip(st)
		}
		return
	}
	if strings.HasPrefix(controller, "/") {
		controller = strings.TrimPrefix(controller, "/")
	} else if sc.module != "" {
		controller = sc.module + "/" + controller
	}
	if helper != "" {
		e.named[helper] = true
	}
	for _, v := range verbs {
		e.out.Routes = append(e.out.Routes, railsRoute{Verb: v, Path: full, Controller: rbCamelize(controller) + "Controller",
			CtrlPath: controller, Action: act, Helper: helper, File: e.cur, Line: line})
	}
}

// root is GET / in the scope, named root.
func (e *rtEval) root(st *rtStmt, sc rtScope) {
	to := first(st.args)
	if v, ok := st.opts["to"]; ok {
		to = v
	}
	if to.kind == 0 {
		if _, ok := st.opts["controller"]; !ok {
			e.skip(st)
			return
		}
	}
	s := sc
	if sc.level == "resources" || sc.level == "resource" {
		s.level = "root"
		s.path = rtMergePath(sc.path, sc.res.path)
	}
	if _, ok := st.opts["as"]; !ok {
		st.opts["as"] = rtVal{kind: 'y', s: "root"}
	}
	e.route(s, []string{"GET"}, "/", false, "/", true, to, st, st.line)
}

// nameFor is the mapper's name_for_action: the route's name from the
// scope's name prefix, the parent resource and the action (or as:). An
// implicit name that's taken, or doesn't start with a letter, is none.
func (e *rtEval) nameFor(sc rtScope, as, action string, explicit bool) string {
	prefix := ""
	switch {
	case explicit:
		prefix = as
	case !rtCanonical(sc.level, action):
		prefix = action
	}
	if prefix != "" && prefix != "/" {
		prefix = strings.ReplaceAll(strings.Trim(rtMergePath("", strings.ReplaceAll(prefix, "-", "_")), "/"), "/", "_")
	} else {
		prefix = ""
	}
	member, coll := "", ""
	if sc.res != nil {
		if !explicit && action == "" {
			return ""
		}
		member, coll = sc.res.memberName, sc.res.collectName
	}
	var parts []string
	switch sc.level {
	case "nested":
		parts = []string{sc.as, prefix}
	case "collection":
		parts = []string{prefix, sc.as, coll}
	case "new":
		parts = []string{prefix, "new", sc.as, member}
	case "member":
		parts = []string{prefix, sc.as, member}
	case "root":
		parts = []string{sc.as, coll, prefix}
	default:
		parts = []string{sc.as, member, prefix}
	}
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	name := strings.Join(keep, "_")
	if name == "" {
		return ""
	}
	if !explicit && (e.named[name] || !(name[0] == '_' || name[0] >= 'a' && name[0] <= 'z' || name[0] >= 'A' && name[0] <= 'Z')) {
		return ""
	}
	return name
}

func rtCanonical(level, action string) bool {
	switch level {
	case "collection", "member", "new":
		switch action {
		case "index", "create", "new", "show", "update", "destroy":
			return true
		}
	}
	return false
}

// str is a value as a string: a string, symbol or bound loop variable,
// with #{x} interpolations of bound variables filled in.
func (e *rtEval) str(sc rtScope, v rtVal) string {
	switch v.kind {
	case 's':
		s := v.s
		for k, val := range sc.vars {
			s = strings.ReplaceAll(s, "#{"+k+"}", val)
		}
		return s
	case 'y', 'c':
		return v.s
	case 'i':
		if val, ok := sc.vars[v.s]; ok {
			return val
		}
	}
	return ""
}

// strs is a value as a list of strings (a list, or one value).
func (e *rtEval) strs(sc rtScope, v rtVal) []string {
	if v.kind == 'a' {
		var out []string
		for _, x := range v.list {
			if s := e.str(sc, x); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if s := e.str(sc, v); s != "" {
		return []string{s}
	}
	return nil
}

func (e *rtEval) optStr(sc rtScope, st *rtStmt, k, def string) string {
	if v, ok := st.opts[k]; ok {
		return e.str(sc, v)
	}
	return def
}

func first(vs []rtVal) rtVal {
	if len(vs) == 0 {
		return rtVal{}
	}
	return vs[0]
}

// rtMergePath joins scope paths the way the mapper normalizes them: one
// leading slash, no doubled or trailing ones.
func rtMergePath(parent, child string) string {
	p := "/" + parent + "/" + child
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

func rtMergeAs(parent, child string) string {
	switch {
	case parent == "":
		return child
	case child == "":
		return parent
	}
	return parent + "_" + child
}

func rtMergeModule(parent, child string) string {
	switch {
	case parent == "":
		return child
	case child == "":
		return parent
	}
	return parent + "/" + child
}

// pluralize is the regular English plural (singularize's inverse).
func pluralize(w string) string {
	switch {
	case w == "person":
		return "people"
	case rtUncountable[w]:
		return w
	case strings.HasSuffix(w, "y") && len(w) > 1 && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"), strings.HasSuffix(w, "z"):
		return w + "es"
	}
	return w + "s"
}

var rtUncountable = map[string]bool{"equipment": true, "information": true, "rice": true, "money": true, "species": true,
	"series": true, "fish": true, "sheep": true, "jeans": true, "police": true, "news": true}
