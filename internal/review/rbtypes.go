package review

import (
	"path"
	"sort"
	"strings"
)

// ActiveRecord-aware receivers for the Ruby call graph. The scanner keeps a
// receiver chain as written ("@ticket.event", "Ticket.where.first",
// "self.items.[]"), with locals replaced by what they were assigned, and
// the resolver types it left to right:
//   - a constant is its class, @x the instance variable's type in the
//     class (or, in a view, its controller's), self the enclosing class;
//   - each step is a method call on the type so far: finders and
//     relations on models, associations, scopes, enums and the like (the
//     synthetic definitions rbMacroDefs and rbSchemaScan add), and Const.new.
// The first step that can't be typed stops it: that call and the rest of
// the chain are unresolved, never guessed.

// rbType is what a receiver holds: an instance of cls, a collection (a
// relation, an association, an array) of them, or the class itself.
type rbType struct {
	cls  string // full constant path
	kind byte   // 'i' instance, 'c' collection, 's' the class (singleton)
}

// rbSyn is a synthetic member: a method Rails generates from a macro (an
// association, an enum, a delegate, AASM) or a column.
type rbSyn struct {
	id    string
	kind  string // the macro: belongs_to, column, enum, …
	arg   string // the macro's argument (an association's class_name)
	scope string // the class whose nesting arg is looked up in
	res   byte   // the result: 'i' or 'c' of the association's class, 0 unknown
	name  string // the association's name, for its target by convention
}

// rbIvar is an instance variable assignment: @name = expr in def d of path.
type rbIvar struct {
	name, expr, path string
	def              *hDef
}

// Pseudo-call receivers the scanner emits for the resolver (see
// resolveRails). The metadata ones are refs that never resolve.
const (
	rbIvarRecv     = "<ivar>"         // Name: the expression @x was assigned; Recv: "<ivar>@x"
	rbTemplateRecv = "<template>"     // an action's implicit template; Name: which of its formats ("0"…)
	rbRenderRecv   = "<render>"       // render, partial:; Name: the spec (see resolveRender)
	rbAuthRecv     = "<authorize>"    // Pundit's authorize; Recv: + the record, Name: the query
	rbPolicyRecv   = "<policy>"       // policy(x).q; Recv: + the record
	rbScopeRecv    = "<policy_scope>" // policy_scope(x); Recv: + the record
	rbActionRecv   = "<action>"       // get :show in a functional test; Name: the action
	rbFactoryRecv  = "<factory>"      // factory :ticket; Name: the class
	rbScopeSig     = "<scope>"        // hDef.Sig of a scope :name definition (unhashed: never a parameter list)
	rbTemplateN    = 4                // implicit template calls per action (formats)
)

// rbSynName splits a synthetic definition's name, "<kind:arg>method" or
// "<kind>method". ok is false for any other name (and for <view>/<main>).
func rbSynName(n string) (kind, arg, method string, ok bool) {
	if !strings.HasPrefix(n, "<") {
		return "", "", "", false
	}
	i := strings.IndexByte(n, '>')
	if i < 0 || i == len(n)-1 {
		return "", "", "", false
	}
	kind, arg, _ = strings.Cut(n[1:i], ":")
	return kind, arg, n[i+1:], true
}

// rbSynAnswers lists the methods a synthetic definition answers, on the
// instance side and on the class side, and what each returns.
func rbSynAnswers(kind, method string) (inst, class map[string]byte) {
	inst, class = map[string]byte{}, map[string]byte{}
	switch kind {
	case "belongs_to", "has_one":
		inst[method] = 'i'
		for _, n := range []string{method + "=", "reload_" + method, method + "_changed?", method + "_previously_changed?"} {
			inst[n] = 0
		}
		for _, n := range []string{"build_" + method, "create_" + method, "create_" + method + "!"} {
			inst[n] = 'i'
		}
	case "has_many", "has_and_belongs_to_many":
		inst[method] = 'c'
		ids := singularize(method) + "_ids"
		inst[method+"="], inst[ids], inst[ids+"="] = 0, 0, 0
	case "attachment":
		inst[method], inst[method+"="] = 0, 0
		inst[method+"_attachment"], inst[method+"_blob"] = 0, 0
		inst[method+"_attachments"], inst[method+"_blobs"] = 0, 0
	case "column":
		for _, n := range []string{method, method + "=", method + "?", method + "_changed?", method + "_was",
			method + "_before_last_save", method + "_in_database", method + "_previously_changed?", method + "_previously_was",
			"saved_change_to_" + method + "?", "saved_change_to_" + method, "will_save_change_to_" + method + "?",
			method + "_change", "restore_" + method + "!", "clear_" + method + "_change"} {
			inst[n] = 0
		}
	case "enum":
		// method is "active?": active?, active!, the scope active and not_active.
		base := strings.TrimSuffix(method, "?")
		inst[method], inst[base+"!"] = 0, 0
		class[base], class["not_"+base] = 'c', 'c'
	case "enum_values", "delegate":
		inst[method] = 0 // enum_values is defined on the class side (its owner)
	case "aasm_event":
		for _, n := range []string{method, method + "!", "may_" + method + "?", method + "_without_validation!"} {
			inst[n] = 0
		}
	case "aasm_state":
		base := strings.TrimSuffix(method, "?")
		inst[method] = 0
		class[base] = 'c'
	}
	return inst, class
}

// prepareTypes indexes what typing needs, once per resolver: synthetic
// members by class, instance variable assignments by class, and the
// templates by path without extensions.
func (l *rbCalls) prepareTypes(r *hResolver) {
	l.syn = map[string]map[string]rbSyn{}
	l.synNames = map[string]bool{}
	l.ivars = map[string][]rbIvar{}
	l.views = map[string][]string{}
	l.memo = map[string]rbType{}
	l.inh = map[string]bool{}
	l.factories = map[string]string{}
	l.helpers, l.helperClasses = map[string][]string{}, nil
	add := func(owner, name string, s rbSyn) {
		if l.syn[owner] == nil {
			l.syn[owner] = map[string]rbSyn{}
		}
		if _, dup := l.syn[owner][name]; !dup {
			l.syn[owner][name] = s
		}
		l.synNames[name] = true
	}
	for _, p := range r.paths {
		f := r.files[p]
		if rbIsTemplate(p) {
			stem := rbStem(p)
			l.views[stem] = append(l.views[stem], p)
		}
		for i := range f.Defs {
			d := &f.Defs[i]
			if kind, arg, m, ok := rbSynName(d.Name); ok {
				owner := strings.TrimSuffix(d.Owner, rbSelf)
				if kind == "column" {
					owner = l.tableModel(strings.TrimPrefix(d.Owner, "<table>"))
				}
				s := rbSyn{id: l.id(p, d), kind: kind, arg: arg, scope: owner, name: m}
				inst, class := rbSynAnswers(kind, m)
				iside, cside := owner, owner+rbSelf
				if strings.HasSuffix(d.Owner, rbSelf) {
					iside = cside // enum_values: a class method
				}
				for n, res := range inst {
					s.res = res
					add(iside, n, s)
				}
				for n, res := range class {
					s.res = res
					add(cside, n, s)
				}
			}
			for _, c := range d.Calls {
				if f := strings.TrimPrefix(c.Recv, rbFactoryRecv); f != c.Recv && f != "" {
					l.factories[f] = c.Name
				}
				if strings.HasPrefix(c.Recv, rbIvarRecv) && d.Class != "" && !strings.HasSuffix(d.Class, rbSelf) {
					l.ivars[d.Class] = append(l.ivars[d.Class], rbIvar{name: strings.TrimPrefix(c.Recv, rbIvarRecv), expr: c.Name, path: p, def: d})
				}
			}
		}
	}
	for stem, ps := range l.views {
		sort.SliceStable(ps, func(i, j int) bool {
			return strings.Contains(path.Base(ps[i]), ".html.") && !strings.Contains(path.Base(ps[j]), ".html.")
		})
		l.views[stem] = ps
	}
}

// rbIsTemplate reports whether p is a view or component template.
func rbIsTemplate(p string) bool {
	switch path.Ext(p) {
	case ".erb", ".haml", ".slim", ".jbuilder":
		return true
	}
	return false
}

// rbStem is a template's path without its extensions:
// app/views/tickets/show.html.erb → app/views/tickets/show.
func rbStem(p string) string {
	dir, base := path.Split(p)
	if i := strings.IndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return dir + base
}

// inherits reports whether class cls (or an ancestor or included module)
// names one of bases as written, for framework classes outside the repo
// (ActiveRecord::Base) and their usual app subclasses (ApplicationRecord).
func (l *rbCalls) inherits(r *hResolver, cls string, bases ...string) bool {
	key := cls + "\x00" + strings.Join(bases, ",")
	if v, ok := l.inh[key]; ok {
		return v
	}
	found := false
	for _, c := range l.chain(r, l.classes[cls], cls) {
		if c.path == "" {
			continue
		}
		if c.owner != cls {
			for _, b := range bases {
				if c.owner == b {
					found = true
				}
			}
		}
		for _, raw := range r.classes[c.path][c.owner] {
			_, w, ok := strings.Cut(raw, " ")
			if !ok {
				continue
			}
			w = strings.TrimPrefix(w, "::")
			for _, b := range bases {
				if w == b || strings.HasSuffix(w, "::"+b) && !strings.Contains(b, "::") {
					found = true
				}
			}
		}
	}
	l.inh[key] = found
	return found
}

func (l *rbCalls) isModel(r *hResolver, cls string) bool {
	return l.inherits(r, cls, "ActiveRecord::Base", "ApplicationRecord")
}

func (l *rbCalls) isJob(r *hResolver, cls string) bool {
	return l.inherits(r, cls, "ActiveJob::Base", "ApplicationJob", "Sidekiq::Job", "Sidekiq::Worker")
}

func (l *rbCalls) isMailer(r *hResolver, cls string) bool {
	return l.inherits(r, cls, "ActionMailer::Base", "ApplicationMailer")
}

func (l *rbCalls) isComponent(r *hResolver, cls string) bool {
	return strings.HasSuffix(cls, "Component") || l.inherits(r, cls, "ViewComponent::Base", "ApplicationComponent")
}

// lookup finds name in class owner and up its chain, a real definition
// before a synthetic one of the same class.
func (l *rbCalls) lookup(r *hResolver, files []string, owner, name string) (string, *hDef, *rbSyn) {
	for _, c := range l.chain(r, files, owner) {
		if d := r.def(c.path, c.owner, name); d != nil {
			return l.id(c.path, d), d, nil
		}
		if s, ok := l.syn[c.owner][name]; ok {
			return s.id, nil, &s
		}
	}
	return "", nil, nil
}

// synType is what a synthetic member returns.
func (l *rbCalls) synType(r *hResolver, s *rbSyn) (rbType, bool) {
	if s.res == 0 {
		return rbType{}, false
	}
	target := s.arg
	switch {
	case target == "?":
		return rbType{}, false // polymorphic
	case strings.HasPrefix(target, "~"):
		// through: the source association on the through association's class.
		through, source, _ := strings.Cut(target[1:], "/")
		if source == "" {
			source = s.name
		}
		tt, ok := l.typeStep(r, rbType{cls: s.scope, kind: 'i'}, through)
		if !ok {
			return rbType{}, false
		}
		for _, n := range []string{source, singularize(source), rbPluralize(source)} {
			if _, _, src := l.lookup(r, l.classes[tt.cls], tt.cls, n); src != nil && src.res != 0 {
				if st, ok := l.synType(r, src); ok {
					return rbType{cls: st.cls, kind: s.res}, true
				}
			}
		}
		target = ""
	}
	if target == "" {
		n := s.name
		if s.res == 'c' {
			n = singularize(n)
		}
		target = rbCamelize(n)
	}
	cp, fs := l.constant(s.scope, target)
	if len(fs) == 0 {
		return rbType{}, false
	}
	return rbType{cls: cp, kind: s.res}, true
}

// ivarType is the type of instance variable name (with its @) in class
// cls: the assignments in the class, or failing that in its ancestors.
// Assignments that can't be typed are skipped; two that disagree leave it
// unknown.
func (l *rbCalls) ivarType(r *hResolver, cls, name string) (rbType, bool) {
	key := "@" + cls + "\x00" + name
	if t, ok := l.memo[key]; ok {
		return t, t.kind != 0
	}
	l.memo[key] = rbType{} // a cycle reads as unknown
	var found rbType
	ok := false
	for _, c := range l.chain(r, l.classes[cls], cls) {
		var types []rbType
		for _, a := range l.ivars[c.owner] {
			if a.name != name {
				continue
			}
			if t, ok := l.typeOf(r, a.path, a.def, a.expr); ok {
				types = append(types, t)
			}
		}
		if len(types) == 0 {
			continue
		}
		found, ok = types[0], true
		for _, t := range types[1:] {
			if t != found {
				ok = false
			}
		}
		break
	}
	if !ok {
		found = rbType{}
	}
	l.memo[key] = found
	return found, ok
}

// selfType is the type self has in definition d of file p, and the class
// whose instance variables it sees.
func (l *rbCalls) selfType(r *hResolver, p string, d *hDef) (rbType, bool) {
	if d.Name == rbViewName {
		if c := l.viewClass(p); c != "" {
			return rbType{cls: c, kind: 'i'}, true // a view sees its controller's instance variables
		}
		return rbType{}, false
	}
	switch {
	case d.Class == "":
		return rbType{}, false
	case strings.HasSuffix(d.Class, rbSelf):
		return rbType{cls: strings.TrimSuffix(d.Class, rbSelf), kind: 's'}, true
	}
	return rbType{cls: d.Class, kind: 'i'}, true
}

// viewClass is the class a template's instance variables come from: a
// ViewComponent template's component, else the view's controller (or
// mailer).
func (l *rbCalls) viewClass(p string) string {
	rel := rbAppRel(p)
	if strings.HasPrefix(rel, "app/components/") {
		stem := rbStem(strings.TrimPrefix(rel, "app/components/"))
		if path.Base(path.Dir(stem)) == path.Base(stem) {
			stem = path.Dir(stem) // foo_component/foo_component.html.erb
		}
		if c, fs := l.constant("", rbCamelize(stem)); len(fs) > 0 {
			return c
		}
		return ""
	}
	c, _ := l.viewController(rel)
	return c
}

// rbAppRel is p from its app/ folder on (the app may sit in a subfolder).
func rbAppRel(p string) string {
	if i := strings.Index("/"+p, "/app/"); i >= 0 {
		return p[i:]
	}
	return p
}

// rbAppRoot is the folder p's app/ sits in, "" or ending in "/".
func rbAppRoot(p string) string {
	if i := strings.Index("/"+p, "/app/"); i >= 0 {
		return p[:i]
	}
	return ""
}

// typeOf types a receiver chain in definition d of file p.
func (l *rbCalls) typeOf(r *hResolver, p string, d *hDef, expr string) (rbType, bool) {
	steps := strings.Split(expr, ".")
	head := steps[0]
	var t rbType
	switch {
	case head == "self":
		st, ok := l.selfType(r, p, d)
		if !ok || d.Name == rbViewName {
			return rbType{}, false // a view's self is the view context
		}
		t = st
	case strings.HasPrefix(head, "@"):
		st, ok := l.selfType(r, p, d)
		if !ok || st.kind != 'i' {
			return rbType{}, false
		}
		if t, ok = l.ivarType(r, st.cls, head); !ok {
			return rbType{}, false
		}
	case rbIsConst(head):
		cp, fs := l.constant(strings.TrimSuffix(d.Class, rbSelf), head)
		if len(fs) == 0 {
			return rbType{}, false
		}
		t = rbType{cls: cp, kind: 's'}
	default:
		return rbType{}, false
	}
	for _, s := range steps[1:] {
		var ok bool
		if t, ok = l.typeStep(r, t, s); !ok {
			return rbType{}, false
		}
	}
	return t, true
}

// typeStep is the type name returns on t, memoized.
func (l *rbCalls) typeStep(r *hResolver, t rbType, name string) (rbType, bool) {
	key := string(t.kind) + t.cls + "\x00" + name
	if res, ok := l.memo[key]; ok {
		return res, res.kind != 0
	}
	_, _, res, ok := l.callOn(r, t, name)
	if !ok {
		res = rbType{}
	}
	l.memo[key] = res
	return res, ok
}

// Methods ActiveRecord gives models, by what they return. A model call
// that isn't the repo's and is one of these is the framework's: neither
// resolved nor counted.
var (
	rbFinders   = rbSet(`find find_by find_by! find_or_create_by find_or_create_by! find_or_initialize_by create create! create_or_find_by create_or_find_by! new build first first! last last! take take! sole find_sole_by second third find_signed find_signed! sample detect`)
	rbRelations = rbSet(`where all order reorder includes joins left_joins left_outer_joins limit offset not or and preload eager_load distinct select rewhere unscope unscoped none readonly lock merge group having references extending in_order_of excluding without strict_loading from reverse_order page per to_a load reload records reject sort_by filter uniq compact shuffle each find_each each_with_index reverse sort sort_by order_by includes? where_not`)
	rbModelFw   = rbSet(`count exists? pluck pick ids sum maximum minimum average delete_all destroy_all update_all insert_all insert_all! upsert_all transaction table_name column_names columns_hash model_name human_attribute_name find_in_batches in_batches connection primary_key name to_s any? none? many? empty? size length calculate reflect_on_association reflect_on_all_associations base_class descendants attribute_names arel_table sanitize_sql inheritance_column delete destroy update update! touch_all increment_counter decrement_counter map collect flat_map filter_map group_by index_by each_slice in_groups_of include? inject reduce each_with_object zip join present? blank? presence to_ary to_json as_json inspect tap then klass proxy_association loaded? load_async cache_key where_values_hash scoping`)
	rbRecordFw  = rbSet(`save save! update update! destroy destroy! delete reload persisted? new_record? destroyed? previously_new_record? valid? invalid? validate validate! errors id id= attributes attributes= assign_attributes touch increment increment! decrement decrement! toggle toggle! update_column update_columns update_attribute update_attribute! changed? changed changes saved_changes previous_changes changed_attributes changes_to_save to_param to_key to_model model_name to_s to_json as_json serializable_hash inspect present? blank? nil? is_a? kind_of? instance_of? respond_to? dup clone freeze frozen? lock! with_lock transaction becomes becomes! marked_for_destruction? mark_for_destruction has_attribute? read_attribute write_attribute attribute_names slice tap then try try! presence hash eql? cache_key cache_key_with_version cache_version strict_loading! signed_id to_global_id to_gid to_sgid to_gid_param to_signed_global_id attachment_changes broadcast_replace broadcast_remove broadcast_append broadcast_prepend broadcast_update broadcast_replace_to broadcast_remove_to broadcast_append_to broadcast_prepend_to broadcast_update_to broadcast_refresh broadcast_refresh_to broadcast_replace_later_to broadcast_append_later_to broadcast_update_later_to broadcast_render broadcast_render_to readonly? encrypt decrypt public_send send class`)
)

func rbSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// callOn resolves name called on a value of type t: the definition it runs
// (id), else the IDs it would have had (want), and what it returns (res,
// ok). A framework method resolves to nothing and isn't counted.
func (l *rbCalls) callOn(r *hResolver, t rbType, name string) (id string, want []string, res rbType, ok bool) {
	files := l.classes[t.cls]
	switch t.kind {
	case 's':
		if name == "new" {
			id, _ := l.construct(r, files, t.cls)
			return id, nil, rbType{cls: t.cls, kind: 'i'}, true
		}
		if id, d, s := l.lookup(r, files, t.cls+rbSelf, name); id != "" {
			switch {
			case s != nil:
				res, ok = l.synType(r, s)
			case d.Sig == rbScopeSig:
				res, ok = rbType{cls: t.cls, kind: 'c'}, true
			}
			return id, nil, res, ok
		}
		if l.isJob(r, t.cls) {
			switch name {
			case "perform_later", "perform_now", "perform_async", "perform_in", "perform_at", "perform_inline":
				id, want := l.find(r, files, t.cls, "perform")
				return id, want, rbType{}, false
			case "set":
				return hExternal, nil, t, true
			}
		}
		if l.isMailer(r, t.cls) {
			if name == "with" {
				return hExternal, nil, t, true
			}
			// A mailer's class method is its instance method of that name.
			id, want := l.find(r, files, t.cls, name)
			return id, want, rbType{}, false
		}
		if name == "with_collection" && l.isComponent(r, t.cls) {
			id, _ := l.construct(r, files, t.cls)
			return id, nil, rbType{}, false
		}
		if l.isModel(r, t.cls) {
			switch {
			case rbFinders[name], name == "[]":
				return hExternal, nil, rbType{cls: t.cls, kind: 'i'}, true
			case rbRelations[name]:
				return hExternal, nil, rbType{cls: t.cls, kind: 'c'}, true
			case rbModelFw[name]:
				return hExternal, nil, rbType{}, false
			}
		}
		return "", l.miss(r, l.chain(r, files, t.cls+rbSelf), name), rbType{}, false
	case 'c':
		switch {
		case name == "[]" || rbFinders[name]:
			return hExternal, nil, rbType{cls: t.cls, kind: 'i'}, true
		case name == "<<" || name == "push" || name == "concat":
			return hExternal, nil, rbType{}, false
		}
		// A relation runs its model's class methods (scopes) in its scope.
		if id, d, s := l.lookup(r, files, t.cls+rbSelf, name); id != "" {
			switch {
			case s != nil:
				res, ok = l.synType(r, s)
			case d.Sig == rbScopeSig:
				res, ok = rbType{cls: t.cls, kind: 'c'}, true
			}
			return id, nil, res, ok
		}
		switch {
		case rbRelations[name]:
			return hExternal, nil, rbType{cls: t.cls, kind: 'c'}, true
		case rbModelFw[name]:
			return hExternal, nil, rbType{}, false
		}
		return "", l.miss(r, l.chain(r, files, t.cls+rbSelf), name), rbType{}, false
	}
	if id, _, s := l.lookup(r, files, t.cls, name); id != "" {
		if s != nil {
			res, ok = l.synType(r, s)
		}
		return id, nil, res, ok
	}
	if l.isModel(r, t.cls) && rbRecordFw[name] {
		if name == "reload" || name == "becomes" || name == "tap" || name == "presence" {
			return hExternal, nil, t, true
		}
		return hExternal, nil, rbType{}, false
	}
	return "", l.miss(r, l.chain(r, files, t.cls), name), rbType{}, false
}

// resolveTyped resolves a call on a receiver chain.
func (l *rbCalls) resolveTyped(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	t, ok := l.typeOf(r, p, d, c.Recv)
	if !ok {
		return "", r.anyMethodWant(c.Name)
	}
	id, want, _, _ := l.callOn(r, t, c.Name)
	return id, want
}

// rbChainRecv reports whether a receiver is a chain for resolveTyped
// rather than one of the plain forms (Const, Const.new, self.new).
func rbChainRecv(recv string) bool {
	if strings.HasPrefix(recv, "@") {
		return true
	}
	i := strings.IndexByte(recv, '.')
	if i < 0 || recv == rbSelfNew {
		return false
	}
	return !(rbIsConst(recv) && recv[i:] == ".new")
}

// rbPluralize is singularize's inverse for the regular English plurals.
func rbPluralize(w string) string {
	switch {
	case w == "person":
		return "people"
	case strings.HasSuffix(w, "y") && len(w) > 1 && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"), strings.HasSuffix(w, "z"):
		return w + "es"
	}
	return w + "s"
}

// rbUnderscore turns a constant path into a file path: Admin::TicketsController
// → admin/tickets_controller.
func rbUnderscore(c string) string {
	var b strings.Builder
	segs := strings.Split(strings.TrimPrefix(c, "::"), "::")
	for i, s := range segs {
		if i > 0 {
			b.WriteByte('/')
		}
		for j := 0; j < len(s); j++ {
			ch := s[j]
			if ch >= 'A' && ch <= 'Z' {
				if j > 0 && (s[j-1] >= 'a' && s[j-1] <= 'z' || s[j-1] >= '0' && s[j-1] <= '9' ||
					(j+1 < len(s) && s[j+1] >= 'a' && s[j+1] <= 'z' && s[j-1] >= 'A' && s[j-1] <= 'Z')) {
					b.WriteByte('_')
				}
				ch += 'a' - 'A'
			}
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// Framework methods a controller, a mailer or a test calls on itself.
var (
	rbControllerFw = rbSet(`params render render_to_string redirect_to redirect_back redirect_back_or_to head respond_to respond_with session cookies flash request response action_name controller_name controller_path url_for send_data send_file authorize policy_scope policy skip_authorization skip_policy_scope verify_authorized pundit_user helpers logger reset_session form_authenticity_token protect_from_forgery default_url_options expires_in fresh_when stale? headers status performed? sign_in sign_out authenticate_user! user_signed_in? current_user t l translate localize stream_from turbo_stream`)
	rbMailerFw     = rbSet(`mail params attachments headers default_url_options t l translate localize message`)
	rbTestFw       = rbSet(`test it specify setup teardown describe context let let! before after subject create build build_stubbed create_list build_list create_pair build_pair attributes_for get post put patch delete head follow_redirect! travel_to travel travel_back freeze_time assert_response assert_redirected_to assert_template assert_difference assert_no_difference assert_changes assert_no_changes assert_emails assert_enqueued_jobs assert_enqueued_with assert_performed_jobs perform_enqueued_jobs assert_select response request session cookies flash expect eq be be_a include let subject described_class allow receive and_return have_http_status visit click_on click_link click_button fill_in within page sign_in sign_out stub stubs expects returns mock file_fixture fixture_file_upload json_response`)
)

// rbTestFramework reports whether a name is a test framework's
// (Minitest/RSpec assertions, FactoryBot, request helpers).
func rbTestFramework(name string) bool {
	return rbTestFw[name] || strings.HasPrefix(name, "assert") || strings.HasPrefix(name, "refute")
}

// helperIDs lists the methods called name in app/helpers (all mixed into
// views), memoized per resolver: views call the same helpers many times.
func (l *rbCalls) helperIDs(r *hResolver, name string) []string {
	if ids, ok := l.helpers[name]; ok {
		return ids
	}
	if l.helperClasses == nil {
		l.helperClasses = []hClassRef{}
		for _, hp := range r.paths {
			if !strings.Contains("/"+hp, "/app/helpers/") {
				continue
			}
			for _, cls := range r.files[hp].Classes {
				if !strings.HasSuffix(cls.Name, rbSelf) {
					l.helperClasses = append(l.helperClasses, hClassRef{hp, cls.Name})
				}
			}
		}
	}
	var ids []string
	for _, c := range l.helperClasses {
		if def, dp := r.method(c, name, l.base); def != nil {
			ids = append(ids, l.id(dp, def))
		}
	}
	l.helpers[name] = ids
	return ids
}
