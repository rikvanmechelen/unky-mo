package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// rbCalls is the Ruby/Rails call analyzer. Definitions come from keyword
// counting (class, module, def, and the block openers that take an end),
// cross-checked with indentation. IDs are constant paths: Admin::User#save
// for an instance method, Admin::User.find for a class method. Calls
// resolve through self, the class's included modules and superclass chain,
// and constants (Zeitwerk's file first, then any class defined with that
// exact path). Rails callbacks are refs from the class to the method, and
// ERB/HAML views are pseudo-functions whose bare calls go to app/helpers
// and their controller's helper_methods. A call on a receiver of unknown
// class is never guessed: Ruby apps have too many methods called call,
// perform or show.
type rbCalls struct {
	rl *rubyLang
	// Per resolver: constant path → the files defining it, Zeitwerk's first.
	r       *hResolver
	classes map[string][]string
	// tables maps a table to its model where self.table_name says so
	// (setup); others go by convention.
	tables map[string]string
	// Per resolver (prepareTypes, rbtypes.go): synthetic members by owner
	// and name, the names they answer, instance variable assignments by
	// class, templates by path without extensions, and memos.
	syn       map[string]map[string]rbSyn
	synNames  map[string]bool
	ivars     map[string][]rbIvar
	views     map[string][]string
	memo      map[string]rbType
	inh       map[string]bool
	factories map[string]string // factory name → class
	// helpers memoizes viewCall's app/helpers lookups by name.
	helpers       map[string][]string
	helperClasses []hClassRef
}

const (
	rbViewName = "<view>" // an ERB/HAML/Slim/Jbuilder template
	rbMainName = "<main>" // a file's top-level code (rake tasks, specs)
	rbSelf     = ".self"  // owner suffix: the class's singleton (class methods)
	// rbSelfNew is the receiver of calls on an instance of self's class: a
	// callback's method, new(…).m in a class method.
	rbSelfNew = "self.new"
	// rbHelperRecv marks a helper_method's symbol: a ref on rbSelfNew that
	// views of the controller can call.
	rbHelperRecv = "helper_method"
)

func (l *rbCalls) name() string         { return "ruby" }
func (l *rbCalls) unit(p string) string { return (&rubyLang{}).unit(p) }

// owns takes Ruby files, and templates under app/views (or app/components,
// ViewComponent's sidecars).
func (l *rbCalls) owns(p string) bool {
	switch path.Ext(p) {
	case ".rb", ".rake":
		return true
	case ".erb", ".haml", ".slim", ".jbuilder":
		return strings.Contains("/"+p, "/app/views/") || strings.Contains("/"+p, "/app/components/")
	}
	return false
}

func (l *rbCalls) setup(idx *index) bool {
	l.rl, l.r, l.classes = &rubyLang{}, nil, nil
	if !l.rl.detect(idx) {
		return false
	}
	l.tables = rbTableNames(idx)
	return true
}

// fileOf: Ruby has no imports.
func (l *rbCalls) fileOf(string, hImport) string { return "" }

func (l *rbCalls) id(p string, d *hDef) string {
	switch d.Name {
	case rbViewName:
		return "view:" + p
	case rbMainName:
		return "main:" + p
	}
	if kind, _, m, ok := rbSynName(d.Name); ok {
		// A generated method: the ID a real method of that name would have.
		owner := d.Owner
		if kind == "column" {
			owner = l.tableModel(strings.TrimPrefix(owner, "<table>"))
		}
		if strings.HasSuffix(owner, rbSelf) {
			return strings.TrimSuffix(owner, rbSelf) + "." + m
		}
		return owner + "#" + m
	}
	if d.Owner == "" && rbIsTestBlockName(d.Name) {
		return "test:" + p + "#" + d.Name // a spec's it "…" blocks have no class
	}
	if d.IsClass {
		// A class reopened outside its autoloaded file (a monkey patch in an
		// initializer) is its own node: the reopenings' bodies would
		// otherwise merge.
		if l.rl != nil {
			if f := l.rl.constFile(d.Name); f != "" && f != p {
				return d.Name + "@" + p
			}
		}
		return d.Name
	}
	if strings.HasSuffix(d.Owner, rbSelf) {
		return strings.TrimSuffix(d.Owner, rbSelf) + "." + d.Name
	}
	owner := d.Owner
	if owner == "" {
		owner = "Object" // top-level methods are Object's (private) methods
	}
	return owner + "#" + d.Name
}

func (l *rbCalls) display(p string, d *hDef) string {
	last := func(c string) string {
		if i := strings.LastIndex(c, "::"); i >= 0 {
			return c[i+2:]
		}
		return c
	}
	if kind, _, m, ok := rbSynName(d.Name); ok {
		switch {
		case kind == "column":
			return last(l.tableModel(strings.TrimPrefix(d.Owner, "<table>"))) + "." + m + " (column)"
		case strings.HasSuffix(d.Owner, rbSelf):
			return last(strings.TrimSuffix(d.Owner, rbSelf)) + "." + m + " (" + kind + ")"
		}
		return last(d.Owner) + "#" + m + " (" + kind + ")"
	}
	switch {
	case d.Owner == "" && rbIsTestBlockName(d.Name):
		return path.Base(p) + " " + d.Name
	case d.Name == rbViewName:
		if i := strings.Index("/"+p, "/app/views/"); i >= 0 {
			return p[i+len("app/views/"):]
		}
		return path.Base(p)
	case d.Name == rbMainName:
		return path.Base(p)
	case d.IsClass:
		return last(d.Name)
	case strings.HasSuffix(d.Owner, rbSelf):
		return last(strings.TrimSuffix(d.Owner, rbSelf)) + "." + d.Name
	case d.Owner == "":
		return d.Name
	}
	return last(d.Owner) + "#" + d.Name
}

var (
	rbKwRe     = regexp.MustCompile(`\b(class|module|def|if|unless|while|until|case|begin|for|do|end)\b`)
	rbConstPat = `(?:::)?[A-Z]\w*(?:::[A-Z]\w*)*`
	rbClassRe  = regexp.MustCompile(`^class\s+(` + rbConstPat + `)\s*(?:<\s*(` + rbConstPat + `))?`)
	rbSclassRe = regexp.MustCompile(`^class\s*<<\s*(\w+)`)
	rbModuleRe = regexp.MustCompile(`^module\s+(` + rbConstPat + `)`)
	rbDefRe    = regexp.MustCompile(`^def\s+(?:(self|[A-Z]\w*)\s*\.\s*)?([A-Za-z_]\w*[?!]?|\[\]=?|<=>|===?|=~|![=~]?|[+\-]@?|\*\*?|/|%|<<|>>|<=?|>=?|&|\||\^|~)`)
	rbMixinRe  = regexp.MustCompile(`^(include|extend|prepend)\s+(.+)$`)
	rbConstRe  = regexp.MustCompile(`^` + rbConstPat + `$`)
	rbScopeRe  = regexp.MustCompile(`^scope\s+:([A-Za-z_]\w*[?!]?)`)
	rbCbRe     = regexp.MustCompile(`^(` + strings.Join(rbCallbacks, "|") + `)\b`)
	rbClassMRe = regexp.MustCompile(`^class_methods\s+do\b`)

	rbChainRe = regexp.MustCompile(`(?:::)?[A-Za-z_]\w*[?!]?(?:\s*&?\.\s*[A-Za-z_]\w*[?!]?|::[A-Z]\w*)*`)
	rbSegRe   = regexp.MustCompile(`\s*&?\.\s*`)
	rbSendRe  = regexp.MustCompile(`(?:(` + rbConstPat + `|[a-z_]\w*)\s*&?\.\s*)?\b(send|public_send|__send__|try|method)\b\s*\(?\s*:([A-Za-z_]\w*[?!=]?)`)
	rbSymRe   = regexp.MustCompile(`:([A-Za-z_]\w*[?!]?)`)
	rbLabelRe = regexp.MustCompile(`([a-z_]\w*):`)

	rbAssignRe  = regexp.MustCompile(`(?:^|[^\w.@$:])([a-z_]\w*)\s*(?:\|\||&&|[-+*/%])?=(?:[^=~>]|$)`)
	rbMultiRe   = regexp.MustCompile(`^\s*\(?([a-z_]\w*(?:\s*,\s*\*?[a-z_]\w*)+)\)?\s*=[^=]`)
	rbBlockPRe  = regexp.MustCompile(`(?:\bdo|\{)\s*\|([^|]*)\|`)
	rbRescueRe  = regexp.MustCompile(`\brescue\b.*=>\s*([a-z_]\w*)`)
	rbForRe     = regexp.MustCompile(`^\s*for\s+([a-z_]\w*(?:\s*,\s*[a-z_]\w*)*)\s+in\b`)
	rbParamRe   = regexp.MustCompile(`(?:^|[,(])\s*[*&]{0,2}([a-z_]\w*)`)
	rbCommentRe = regexp.MustCompile(`^#`)
)

// rbCallbacks are the Rails macros whose symbols name methods to run.
var rbCallbacks = []string{
	"before_action", "after_action", "around_action",
	"prepend_before_action", "prepend_after_action", "prepend_around_action",
	"append_before_action", "append_after_action", "append_around_action",
	"skip_before_action", "skip_after_action", "skip_around_action",
	"before_validation", "after_validation", "validates_with", "validates", "validate",
	"before_save", "after_save", "around_save", "before_create", "after_create", "around_create",
	"before_update", "after_update", "around_update", "before_destroy", "after_destroy", "around_destroy",
	"after_commit", "after_create_commit", "after_update_commit", "after_destroy_commit", "after_save_commit",
	"after_rollback", "after_initialize", "after_find", "after_touch", "helper_method",
}

// rbKeywords never start a call.
var rbKeywords = map[string]bool{
	"alias": true, "and": true, "begin": true, "break": true, "case": true, "class": true, "def": true,
	"defined?": true, "do": true, "else": true, "elsif": true, "end": true, "ensure": true, "false": true,
	"for": true, "if": true, "in": true, "module": true, "next": true, "nil": true, "not": true, "or": true,
	"redo": true, "rescue": true, "retry": true, "return": true, "self": true, "super": true, "then": true,
	"true": true, "undef": true, "unless": true, "until": true, "when": true, "while": true, "yield": true,
	"__FILE__": true, "__LINE__": true, "__dir__": true, "__method__": true, "block_given?": true,
}

// rbSkip are methods that are never this repo's: Kernel, Module and Rails
// class macros. Skipping them keeps the unresolved count meaningful.
var rbSkip = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`require require_relative require_dependency include extend prepend
		private protected public module_function private_constant private_class_method public_class_method
		attr_reader attr_writer attr_accessor raise fail loop lambda proc puts p pp print binding catch throw
		has_many has_one belongs_to has_and_belongs_to_many scope delegate layout rescue_from enum serialize
		store_accessor accepts_nested_attributes_for has_secure_password has_one_attached has_many_attached
		attribute alias_method define_method included extended prepended class_methods`) {
		m[w] = true
	}
	for _, w := range rbCallbacks {
		m[w] = true
	}
	return m
}()

// rbSendNames take the method name as a symbol (handled by rbSendRe).
var rbSendNames = map[string]bool{"send": true, "public_send": true, "__send__": true, "try": true, "method": true}

// rbFrame is an open block while scanning: something that takes an end.
type rbFrame struct {
	kind   byte // 'c' class/module, 's' class << self, 'd' def, 'b' any other block
	indent int  // the opening line's indentation
	col    int  // the keyword's column
	stmt   int  // the indentation of the statement the line continues
	line   int
	def    int    // the definition its lines belong to, -1 at the top level
	scope  string // the constant nesting inside ("Admin::User")
	inst   string // the owner of a def inside ("Admin::User", or "Admin::User.self")
	inDef  bool
	mfunc  bool // module_function seen in this module
	priv   bool // private/protected seen in this class: its defs aren't actions
	aasm   bool // an aasm do / state_machine do block
}

// rbBases collects a class's bases as hClass entries: "i:" for the
// instance side of a constant, "s:" for its singleton, then the lexical
// scope the constant is looked up from, a space, and the constant as
// written ("self" for extend self).
type rbBases struct {
	incl, ext, sincl []string
	super            string
}

// scanFile reads a Ruby file's definitions, or a template as one
// pseudo-definition. Ruby files' top-level code with calls (rake tasks,
// specs) is a "<main>" definition.
func (l *rbCalls) scanFile(p, src string) hFile {
	var code string
	view := false
	switch path.Ext(p) {
	case ".erb":
		code, view = rbERBMarkers(src, erbCode(src)), true
	case ".haml", ".slim":
		code, view = hamlCode(src), true
	case ".jbuilder":
		code, view = rubyCode(src), true
	default:
		if rbIsSchema(src) {
			return rbSchemaScan(strings.Split(src, "\n"))
		}
		code = rubyCode(src)
	}
	lines := strings.Split(code, "\n")
	raw := strings.Split(src, "\n")
	if view {
		return rbScanView(raw, lines)
	}
	return rbScan(raw, lines)
}

// rbERBMarkers blanks the "=" and "-" of <%= and <%- that erbCode keeps
// as code: "<%= a.b %><%= c %>" would read as the setter call a.b = c.
func rbERBMarkers(src, code string) string {
	b := []byte(code)
	for i := 0; i+2 < len(src) && i+2 < len(b); i++ {
		if src[i] == '<' && src[i+1] == '%' && (src[i+2] == '=' || src[i+2] == '-') {
			b[i+2] = ' '
		}
	}
	return string(b)
}

// rbScanView is a template: one definition holding every call.
func rbScanView(raw, code []string) hFile {
	d := hDef{Name: rbViewName, Line: 1}
	locals := map[string]string{}
	var all []int
	for i, line := range code {
		all = append(all, i)
		if strings.TrimSpace(raw[i]) != "" {
			d.End = i + 1
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		rbAddLocals(line, locals)
		d.Calls = append(d.Calls, rbLineCalls(line, i+1, locals, "")...)
		d.Calls = append(d.Calls, rbRailsCalls(raw[i], line, i+1, locals, "")...)
	}
	d.Body = hLinesBody(raw, all, nil)
	return hFile{Defs: []hDef{d}, OK: true}
}

// rbScan reads a Ruby file's classes, modules and methods.
func rbScan(raw, code []string) hFile {
	f := hFile{OK: true}
	var defs []hDef
	var stack []rbFrame
	top := rbFrame{def: -1}
	cur := func() rbFrame {
		if len(stack) == 0 {
			return top
		}
		return stack[len(stack)-1]
	}
	own := map[int][]int{}                // definition → its own lines (0-based), without nested definitions'
	locals := map[int]map[string]string{} // definition → local → its class ("User.new") or ""
	localsOf := func(d int) map[string]string {
		if locals[d] == nil {
			locals[d] = map[string]string{}
		}
		return locals[d]
	}
	override := map[int]int{} // line → a scope's definition
	bases := map[string]*rbBases{}
	var order []string
	basesOf := func(c string) *rbBases {
		if bases[c] == nil {
			bases[c] = &rbBases{}
			order = append(order, c)
		}
		return bases[c]
	}
	main := -1
	stmt, cont := 0, false // the current statement's indentation; whether the next line continues it
	pending := 0           // open brackets of the statement
	// Rails: private methods (not actions), methods that render or redirect
	// themselves (no implicit template), callback lambdas and blocks (their
	// calls run on the instance) and AASM event blocks (their callbacks).
	priv, renders, responds := map[int]bool{}, map[int]bool{}, map[int]bool{}
	instLine, instCol, instUntil, instDepth := -1, 0, -1, 0
	evDef, evDepth := -1, 0

	for i := 0; i < len(code); i++ {
		if strings.TrimSpace(raw[i]) == "__END__" {
			break
		}
		line := code[i]
		trim := strings.TrimSpace(line)
		fr := cur()
		owner := fr.def
		if o, ok := override[i]; ok {
			owner = o
		}
		if trim == "" {
			// A heredoc's body is the definition's too; a comment isn't.
			if rt := strings.TrimSpace(raw[i]); owner >= 0 && rt != "" && !strings.HasPrefix(rt, "#") {
				own[owner] = append(own[owner], i)
			}
			continue
		}
		// From the source line: a line starting with a string ("k" => lambda do)
		// is blanked in code.
		indent := len(raw[i]) - len(strings.TrimLeft(raw[i], " \t"))
		if !cont && !strings.HasPrefix(trim, ".") && !strings.HasPrefix(trim, "&.") {
			stmt = indent
		}
		pending += depth(line)
		cont = pending > 0 || strings.HasSuffix(trim, ",") || strings.HasSuffix(trim, "\\") ||
			strings.HasSuffix(trim, "&&") || strings.HasSuffix(trim, "||") || strings.HasSuffix(trim, ".")
		if pending < 0 {
			pending = 0
		}

		txt := []byte(line)
		opened := false
		loopDo := false
		push := func(kind byte, col, def int, scope, inst string, inDef bool) {
			stack = append(stack, rbFrame{kind: kind, indent: indent, col: col, stmt: stmt, line: i, def: def, scope: scope, inst: inst, inDef: inDef})
		}
		pushBlock := func(col int) {
			p := cur()
			push('b', col, p.def, p.scope, p.inst, p.inDef)
		}
		for _, m := range rbKwRe.FindAllStringIndex(line, -1) {
			kw := line[m[0]:m[1]]
			if !rbKeywordAt(line, m[0], m[1]) {
				continue
			}
			switch kw {
			case "end":
				if len(stack) == 0 {
					f.OK = false
					continue
				}
				t := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if t.line != i && m[0] == indent && indent != t.indent && indent != t.col && indent != t.stmt {
					f.OK = false // the keywords and the indentation disagree
				}
				if t.kind == 'c' || t.kind == 'd' {
					defs[t.def].End = i + 1
				}
			case "if", "unless", "while", "until", "case", "begin", "for":
				// The source line, not the code: in x += "s" if y the string is blanked,
				// which would put the if right after the assignment.
				if m[0] > len(raw[i]) || !rbStatementStart(raw[i][:m[0]]) {
					continue // a modifier: x if y
				}
				loopDo = kw == "while" || kw == "until" || kw == "for"
				pushBlock(m[0])
			case "do":
				if loopDo {
					loopDo = false // while x do: the loop's, not a block
					continue
				}
				p := cur()
				if rbClassMRe.MatchString(trim) && p.kind == 'c' && !p.inDef {
					// ActiveSupport::Concern's class_methods: its defs are
					// the includer's class methods.
					push('b', m[0], p.def, p.scope, p.scope+rbSelf, false)
					continue
				}
				if name := rbTestBlockName(strings.TrimSpace(raw[i])); name != "" && !p.inDef {
					// test "…" do / it "…" do / setup do: a test definition.
					defs = append(defs, hDef{Name: name, Owner: p.inst, Class: p.inst, Line: i + 1, Sig: hashOf("")})
					idx := len(defs) - 1
					if !opened {
						owner, opened = idx, true
					}
					push('d', m[0], idx, p.scope, p.inst, true)
					continue
				}
				pushBlock(m[0])
				if strings.HasPrefix(trim, "aasm") || strings.HasPrefix(trim, "state_machine") {
					stack[len(stack)-1].aasm = true
				}
			case "class", "module":
				rest := line[m[0]:]
				p := cur()
				if sm := rbSclassRe.FindStringSubmatch(rest); kw == "class" && sm != nil {
					inst := p.inst
					if sm[1] == "self" && p.scope != "" {
						inst = p.scope + rbSelf
					}
					push('s', m[0], p.def, p.scope, inst, p.inDef)
					blank(txt, m[0], len(txt))
					continue
				}
				var cm []string
				if kw == "class" {
					cm = rbClassRe.FindStringSubmatch(rest)
				} else if mm := rbModuleRe.FindStringSubmatch(rest); mm != nil {
					cm = []string{mm[0], mm[1], ""}
				}
				if cm == nil {
					pushBlock(m[0]) // keep the count
					continue
				}
				c := strings.TrimPrefix(cm[1], "::")
				if p.scope != "" && !strings.HasPrefix(cm[1], "::") {
					c = p.scope + "::" + cm[1]
				}
				b := basesOf(c)
				if cm[2] != "" && b.super == "" {
					b.super = p.scope + " " + cm[2]
				}
				defs = append(defs, hDef{Name: c, Class: c + rbSelf, IsClass: true, Line: i + 1})
				push('c', m[0], len(defs)-1, c, c, false)
				if !opened {
					owner, opened = len(defs)-1, true
				}
				stop := strings.IndexByte(line[m[0]:], ';')
				if stop < 0 {
					stop = len(line) - m[0]
				}
				blank(txt, m[0], m[0]+stop)
			case "def":
				rest := line[m[0]:]
				dm := rbDefRe.FindStringSubmatchIndex(rest)
				if dm == nil {
					pushBlock(m[0])
					continue
				}
				name := rest[dm[4]:dm[5]]
				static := dm[2] >= 0
				j := m[0] + dm[1]
				if j+1 < len(line) && line[j] == '=' && line[j+1] == '(' && rbIdentEnd(name) {
					name += "=" // a setter
					j++
				}
				for j < len(line) && line[j] == ' ' {
					j++
				}
				params, hdrEnd := "", j
				switch {
				case j < len(line) && line[j] == '(':
					if cl := matchingParen(line, j); cl > j {
						params, hdrEnd = line[j+1:cl], cl+1
					} else {
						// The parameters run onto the next lines.
						text := line[j:]
						for k := i + 1; k < len(code) && k <= i+20; k++ {
							text += "\n" + code[k]
							if cl := matchingParen(text, 0); cl > 0 {
								params = text[1:cl]
								break
							}
						}
						hdrEnd = len(line)
					}
				case j < len(line) && line[j] != ';' && line[j] != '=':
					stop := strings.IndexByte(line[j:], ';')
					if stop < 0 {
						stop = len(line) - j
					}
					params, hdrEnd = line[j:j+stop], j+stop
				}
				after := strings.TrimLeft(line[hdrEnd:], " ")
				endless := strings.HasPrefix(after, "=") && !strings.HasPrefix(after, "==") &&
					!strings.HasPrefix(after, "=~") && !strings.HasPrefix(after, "=>")
				if endless {
					hdrEnd = len(line) - len(after) + 1
				}
				blank(txt, m[0], hdrEnd)
				p := cur()
				downer := p.inst
				switch {
				case static && p.scope != "":
					downer = p.scope + rbSelf
				case static:
					downer = ""
				case p.kind == 'c' && p.mfunc:
					downer = p.scope + rbSelf
				}
				d := hDef{Name: name, Owner: downer, Class: downer, Static: strings.HasSuffix(downer, rbSelf), Line: i + 1,
					Sig: hashOf(strings.Join(strings.Fields(params), ""))}
				defs = append(defs, d)
				idx := len(defs) - 1
				if (p.kind == 'c' && p.priv) || strings.HasPrefix(trim, "private ") || strings.HasPrefix(trim, "protected ") {
					priv[idx] = true
				}
				ls := localsOf(idx)
				for _, pm := range rbParamRe.FindAllStringSubmatch(params, -1) {
					ls[pm[1]] = ""
				}
				if !opened {
					owner, opened = idx, true
				}
				if endless {
					defs[idx].End = i + 1
				} else {
					push('d', m[0], idx, p.scope, p.inst, true)
				}
			}
		}

		// Class-body macros.
		var refs []hCall
		if !fr.inDef && fr.scope != "" && owner == fr.def {
			switch {
			case rbMixinRe.MatchString(trim):
				mm := rbMixinRe.FindStringSubmatch(trim)
				b := basesOf(fr.scope)
				for _, w := range strings.Split(mm[2], ",") {
					w = strings.TrimSpace(w)
					if w != "self" && !rbConstRe.MatchString(w) {
						continue
					}
					ref := fr.scope + " " + w
					switch {
					case mm[1] == "extend" || fr.kind == 's':
						b.ext = append(b.ext, "i:"+ref)
					default:
						b.incl = append(b.incl, "i:"+ref)
						b.sincl = append(b.sincl, "s:"+ref) // a concern's class_methods
					}
				}
				blank(txt, 0, len(txt))
			case trim == "module_function" && fr.kind == 'c' && len(stack) > 0:
				stack[len(stack)-1].mfunc = true
			case rbScopeRe.MatchString(trim):
				// scope :active, -> { … } defines a class method.
				name := rbScopeRe.FindStringSubmatch(trim)[1]
				end := rbStatementEnd(code, i)
				defs = append(defs, hDef{Name: name, Owner: fr.scope + rbSelf, Class: fr.scope + rbSelf, Static: true,
					Line: i + 1, End: end + 1, Sig: rbScopeSig})
				owner = len(defs) - 1
				for k := i + 1; k <= end; k++ {
					override[k] = owner
				}
			case (trim == "private" || trim == "protected" || trim == "public") && fr.kind == 'c' && len(stack) > 0:
				stack[len(stack)-1].priv = trim != "public"
			case rbMacroRe.MatchString(trim):
				defs = append(defs, rbMacroDefs(code, raw, i, fr.scope)...)
			case fr.kind == 'b' && rbInAASM(stack) && (rbEventRe.MatchString(trim) || rbStateRe.MatchString(trim)):
				ds := rbAASMDefs(code, raw, i, fr.scope, trim)
				defs = append(defs, ds...)
				if top := len(stack) - 1; len(ds) == 1 && strings.HasPrefix(ds[0].Name, "<aasm_event>") && top >= 0 && stack[top].line == i && stack[top].kind == 'b' {
					evDef, evDepth = len(defs)-1, len(stack)
				}
			case rbCbRe.MatchString(trim):
				macro := rbCbRe.FindString(trim)
				refs = rbCallbackRefs(code, i, macro)
				if macro != "helper_method" {
					// A lambda's or block's calls run on the record; the
					// macro's other arguments are read in the class.
					end := rbStatementEnd(code, i)
					for k := i; k <= end && k < len(code); k++ {
						if loc := rbLambdaRe.FindStringIndex(code[k]); loc != nil {
							instLine, instCol, instUntil = k, loc[0], end
							break
						}
					}
					if top := len(stack) - 1; top >= 0 && stack[top].line == i && stack[top].kind == 'b' {
						instDepth = len(stack)
					}
				}
			}
		}
		if evDef >= 0 {
			switch {
			case len(stack) < evDepth:
				evDef = -1
			case i > defs[evDef].Line-1:
				defs[evDef].Calls = append(defs[evDef].Calls, rbLabelSymRefs(trim, i+1, rbAASMLabels)...)
			}
		}

		text := string(txt)
		ls := localsOf(owner)
		rbAddLocals(text, ls)
		superName := ""
		if owner >= 0 && !defs[owner].IsClass {
			superName = defs[owner].Name
		}
		var lineCalls []hCall
		switch {
		case i == instLine:
			// The lambda starts mid-line: what's before it is the class's.
			b := []byte(text)
			blank(b, instCol, len(b))
			lineCalls = rbLineCalls(string(b), i+1, ls, superName)
			b = []byte(text)
			blank(b, 0, instCol)
			lineCalls = append(lineCalls, rbInstCalls(rbLineCalls(string(b), i+1, ls, superName))...)
		case (i > instLine && i <= instUntil) || (instDepth > 0 && len(stack) >= instDepth):
			lineCalls = rbInstCalls(rbLineCalls(text, i+1, ls, superName))
		default:
			lineCalls = rbLineCalls(text, i+1, ls, superName)
		}
		if instDepth > 0 && len(stack) < instDepth {
			instDepth = 0
		}
		calls := append(lineCalls, refs...)
		action := ""
		if owner >= 0 && !defs[owner].IsClass {
			calls = append(calls, rbIvarCalls(text, i+1, ls)...)
			if !defs[owner].Static && !priv[owner] {
				action = defs[owner].Name
			}
		}
		calls = append(calls, rbRailsCalls(raw[i], text, i+1, ls, action)...)
		if owner >= 0 {
			renders[owner] = renders[owner] || rbRenders(text)
			responds[owner] = responds[owner] || len(rbFind(rbRespondRe, text, "respond_to")) > 0
		}
		if owner < 0 {
			if len(calls) == 0 {
				continue
			}
			if main < 0 {
				defs = append(defs, hDef{Name: rbMainName, Line: i + 1})
				main = len(defs) - 1
			}
			owner = main
			defs[main].End = i + 1
		}
		defs[owner].Calls = append(defs[owner].Calls, calls...)
		own[owner] = append(own[owner], i)
	}
	if len(stack) > 0 {
		f.OK = false
		for _, t := range stack {
			if (t.kind == 'c' || t.kind == 'd') && defs[t.def].End == 0 {
				defs[t.def].End = len(code)
			}
		}
	}
	rbAddTemplateCalls(defs, priv, renders, responds)
	for i := range defs {
		if _, _, _, syn := rbSynName(defs[i].Name); syn && defs[i].Body != "" {
			continue // its macro's lines, hashed when it was read
		}
		defs[i].Body = hLinesBody(raw, own[i], rbCommentRe)
	}
	// A class or module whose body is only other definitions (a namespace,
	// or a class of methods) adds nothing as a node of its own.
	for i, d := range defs {
		if d.IsClass && len(d.Calls) == 0 && !rbHasContent(code, own[i], d) {
			continue
		}
		f.Defs = append(f.Defs, defs[i])
	}
	for _, c := range order {
		b := bases[c]
		inst := append([]string{}, b.incl...)
		single := append(append([]string{}, b.ext...), b.sincl...)
		if b.super != "" {
			inst = append(inst, "i:"+b.super)
			single = append(single, "s:"+b.super)
		}
		f.Classes = append(f.Classes, hClass{Name: c, Bases: inst}, hClass{Name: c + rbSelf, Bases: single})
	}
	return f
}

// rbHasContent reports whether a class has code of its own besides its
// opening and end lines.
func rbHasContent(code []string, own []int, d hDef) bool {
	for _, i := range own {
		if i != d.Line-1 && i != d.End-1 && strings.TrimSpace(code[i]) != "" {
			return true
		}
	}
	return false
}

// rbKeywordAt reports whether the keyword at line[s:e] is one: not a
// method (x.class), symbol (:end), label (if:) or part of a name (end?).
func rbKeywordAt(line string, s, e int) bool {
	if s > 0 {
		switch line[s-1] {
		case '.', ':', '@', '$':
			return false
		}
	}
	if e < len(line) {
		switch line[e] {
		case '?', '!':
			return false
		case ':':
			return e+1 < len(line) && line[e+1] == ':'
		}
	}
	return true
}

// rbStatementStart reports whether a keyword after before starts a
// statement (so it opens a block that takes an end) rather than being a
// modifier (return if x).
func rbStatementStart(before string) bool {
	b := strings.TrimRight(before, " \t")
	if b == "" {
		return true
	}
	switch b[len(b)-1] {
	case ';', '(', '[', '{':
		return true
	case '=':
		// x = if …, @x ||= begin …; not ==, !=, <=, >=.
		return len(b) < 2 || !strings.ContainsRune("=!<>", rune(b[len(b)-2]))
	}
	for _, kw := range []string{"else", "then", "do", "begin"} {
		if strings.HasSuffix(b, kw) && (len(b) == len(kw) || !isWordByte(b[len(b)-len(kw)-1])) {
			return true
		}
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func rbIdentEnd(name string) bool { return name != "" && isWordByte(name[len(name)-1]) }

// rbStatementEnd is the last line of the statement starting at line i: it
// runs on while brackets are open or a line ends with a comma.
func rbStatementEnd(code []string, i int) int {
	d := 0
	for k := i; k < len(code); k++ {
		d += depth(code[k])
		t := strings.TrimSpace(code[k])
		if d <= 0 && !strings.HasSuffix(t, ",") && !strings.HasSuffix(t, "\\") {
			return k
		}
	}
	return len(code) - 1
}

// rbCallbackRefs reads a callback macro's method symbols, refs on an
// instance of the class: the positional ones (except for validates, whose
// positional symbols are attributes), and those under if: and unless:.
func rbCallbackRefs(code []string, i int, macro string) []hCall {
	recv := rbSelfNew
	if macro == "helper_method" {
		recv = rbHelperRecv
	}
	positional := macro != "validates" && macro != "validates_with"
	cond := false
	var out []hCall
	end := rbStatementEnd(code, i)
	for k := i; k <= end; k++ {
		line := code[k]
		if k == i {
			line = strings.Repeat(" ", strings.Index(line, macro)+len(macro)) + line[strings.Index(line, macro)+len(macro):]
		}
		type tok struct {
			at    int
			label bool
			name  string
		}
		var toks []tok
		for _, m := range rbSymRe.FindAllStringSubmatchIndex(line, -1) {
			if m[0] > 0 && (line[m[0]-1] == ':' || isWordByte(line[m[0]-1])) {
				continue
			}
			if m[1] < len(line) && line[m[1]] == ':' {
				continue
			}
			toks = append(toks, tok{m[0], false, line[m[2]:m[3]]})
		}
		for _, m := range rbLabelRe.FindAllStringSubmatchIndex(line, -1) {
			if m[0] > 0 && (line[m[0]-1] == ':' || isWordByte(line[m[0]-1])) {
				continue
			}
			if m[1] < len(line) && line[m[1]] == ':' {
				continue
			}
			toks = append(toks, tok{m[0], true, line[m[2]:m[3]]})
		}
		sort.Slice(toks, func(a, b int) bool { return toks[a].at < toks[b].at })
		for _, t := range toks {
			if t.label {
				positional = false
				cond = t.name == "if" || t.name == "unless"
				continue
			}
			if positional || cond {
				out = append(out, hCall{Recv: recv, Name: t.name, Line: k + 1, Ref: true})
			}
		}
	}
	return out
}

// rbAddLocals records the locals a line assigns (x = …, a, b = …, block
// parameters, rescue => e, for x in), with the expression (rbParseChain)
// of those assigned a receiver chain, and of iterators' block parameters.
func rbAddLocals(line string, locals map[string]string) {
	typed := map[string]string{}
	for _, m := range rbAssignRe.FindAllStringSubmatchIndex(line, -1) {
		name := line[m[2]:m[3]]
		if rbKeywords[name] {
			continue
		}
		eq := strings.IndexByte(line[m[3]:], '=') + m[3]
		if op := strings.TrimSpace(line[m[3]:eq]); op == "" || op == "||" {
			typed[name] = rbExprAt(line, eq+1, locals) // x = …, x ||= …
		}
	}
	for name, t := range typed {
		locals[name] = t
	}
	for _, m := range rbAssignRe.FindAllStringSubmatch(line, -1) {
		if _, ok := typed[m[1]]; !ok && !rbKeywords[m[1]] {
			locals[m[1]] = ""
		}
	}
	if m := rbMultiRe.FindStringSubmatch(line); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			locals[strings.TrimLeft(strings.TrimSpace(n), "*")] = ""
		}
	}
	for _, m := range rbBlockPRe.FindAllStringSubmatch(line, -1) {
		for _, pm := range rbParamRe.FindAllStringSubmatch(m[1], -1) {
			locals[pm[1]] = ""
		}
	}
	if m := rbRescueRe.FindStringSubmatch(line); m != nil {
		locals[m[1]] = ""
	}
	if m := rbForRe.FindStringSubmatch(line); m != nil {
		for _, n := range strings.Split(m[1], ",") {
			locals[strings.TrimSpace(n)] = ""
		}
	}
	rbBindBlocks(line, locals)
}

func rbIsConst(s string) bool {
	s = strings.TrimPrefix(s, "::")
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

// rbLineCalls finds the calls in a line of scanned code: chains like
// Const.m, Const::Path.m, self.m, x.m (x unknown unless a local assigned
// Const.new), bare m and m(…) unless m is a local, super, and the method
// symbols of send/public_send/method (a ref).
func rbLineCalls(line string, lineNo int, locals map[string]string, superName string) []hCall {
	var out []hCall
	recvOf := func(w string) string {
		switch {
		case w == "" || w == "self":
			return "self"
		case rbIsConst(w):
			return w
		}
		if t := locals[w]; t != "" {
			return t
		}
		return "?"
	}
	for _, m := range rbSendRe.FindAllStringSubmatch(line, -1) {
		out = append(out, hCall{Recv: recvOf(m[1]), Name: m[3], Line: lineNo, Ref: m[2] == "method"})
	}
	// recvAt maps the ")", "]" or "}" closing a call's arguments, an index
	// or a block to the chain it ends: the receiver of what hangs off it.
	recvAt := map[int]string{}
	// after records what follows a chain ending at e: arguments, an index
	// or a brace block, each ending the chain's value at its close.
	after := func(e int, recv string) {
		if recv == "?" || recv == "" {
			return
		}
		for e < len(line) {
			o := e
			if line[o] == ' ' && o+1 < len(line) && line[o+1] == '{' {
				o++ // a block: x.each { … }
			}
			if line[o] != '(' && line[o] != '[' && line[o] != '{' {
				return
			}
			cl := matchingParen(line, o)
			if cl < 0 {
				return
			}
			if line[o] == '[' {
				recv += ".[]"
			}
			recvAt[cl] = recv
			e = cl + 1
		}
	}
	for _, m := range rbChainRe.FindAllStringIndex(line, -1) {
		s, e := m[0], m[1]
		if s > 0 {
			pc := line[s-1]
			if isWordByte(pc) || (pc == ':' && line[s] != ':') {
				continue // inside a word; a symbol
			}
		}
		if e < len(line) && line[e] == ':' && (e+1 >= len(line) || line[e+1] != ':') {
			continue // a label (key: value)
		}
		segs := rbSegRe.Split(line[s:e], -1)
		recv, k := "", 0
		if j := lastNonSpace(line[:s]); j >= 0 && line[j] == '.' {
			// Hangs off an expression: x(…).m, Const.new(…).m, xs[0].m.
			recv = "?"
			if q := lastNonSpace(strings.TrimSuffix(line[:j], "&")); q >= 0 && recvAt[q] != "" {
				recv = recvAt[q]
			}
		} else {
			head := segs[0]
			k = 1
			paren := len(segs) == 1 && e < len(line) && line[e] == '('
			switch {
			case s > 0 && line[s-1] == '@' && (s < 2 || line[s-2] != '@'):
				recv = "@" + head // an instance variable
			case s > 0 && (line[s-1] == '@' || line[s-1] == '$'):
				recv = "?" // a class or global variable
			case rbIsConst(head):
				recv = head
			case head == "self":
				recv = "self"
			case head == "super":
				if superName != "" {
					out = append(out, hCall{Recv: "super", Name: superName, Line: lineNo})
				}
				recv = "?"
			case rbKeywords[head]:
				recv = "?"
			default:
				typ, local := locals[head]
				switch {
				case local && !paren:
					recv = "?"
					if typ != "" {
						recv = typ
					}
				case head == "new":
					// new(…) in a class method: an instance of self's class.
					out = append(out, hCall{Name: head, Line: lineNo})
					recv = rbSelfNew
				case (head == "policy" || head == "policy_scope") && paren:
					// Pundit: policy(@x).show? is XPolicy#show?, and
					// policy_scope(X) a relation of X.
					out = append(out, hCall{Name: head, Line: lineNo})
					recv = "?"
					if cl := matchingParen(line, e); cl > e {
						if inner, _ := rbParseChain(line[:cl], e+1, locals, nil); inner != "" {
							if head == "policy" {
								recvAt[cl] = rbPolicyRecv + inner
							} else {
								recvAt[cl] = inner + ".all"
							}
						}
					}
					continue
				default:
					if !rbSkip[head] && !rbSendNames[head] {
						out = append(out, hCall{Name: head, Line: lineNo})
						recv = "self." + head
					} else {
						recv = "?"
					}
				}
			}
		}
		for ; k < len(segs); k++ {
			name := segs[k]
			if rbSendNames[name] || rbKeywords[name] {
				recv = "?" // send(:m) is read above; x.class isn't this repo's
				continue
			}
			if k == len(segs)-1 && rbIdentEnd(name) {
				// self.x = 1 calls x=.
				rest := strings.TrimLeft(line[e:], " ")
				if strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") && !strings.HasPrefix(rest, "=~") && !strings.HasPrefix(rest, "=>") {
					name += "="
				}
			}
			if strings.HasPrefix(recv, rbPolicyRecv) {
				out = append(out, hCall{Recv: recv, Name: "<" + name, Line: lineNo}) // see resolveRails
			} else {
				out = append(out, hCall{Recv: recv, Name: name, Line: lineNo})
			}
			switch {
			case recv == "?" || strings.HasPrefix(recv, rbPolicyRecv) || strings.HasSuffix(name, "="):
				recv = "?"
			case strings.Count(recv, ".") >= rbMaxSteps:
				recv = "?"
			default:
				recv += "." + name
			}
		}
		after(e, recv)
	}
	return out
}

// prepare indexes the classes of r's version: constant path → files.
func (l *rbCalls) prepare(r *hResolver) {
	if l.r == r {
		return
	}
	l.r, l.classes = r, map[string][]string{}
	for _, p := range r.paths {
		for _, c := range r.files[p].Classes {
			if strings.HasSuffix(c.Name, rbSelf) {
				continue
			}
			if fs := l.classes[c.Name]; len(fs) == 0 || fs[len(fs)-1] != p {
				l.classes[c.Name] = append(fs, p)
			}
		}
	}
	for c, fs := range l.classes {
		home := l.rl.constFile(c)
		sort.SliceStable(fs, func(i, j int) bool { return fs[i] == home && fs[j] != home })
	}
	l.prepareTypes(r)
}

// constant resolves a constant as written in scope (the class nesting it
// appears in), like Ruby: each enclosing namespace, innermost first, then
// the top level. It returns the constant's full path and the files
// defining it as a class or module.
func (l *rbCalls) constant(scope, w string) (string, []string) {
	var scopes []string
	if strings.HasPrefix(w, "::") {
		w, scopes = w[2:], []string{""}
	} else {
		for s := scope; s != ""; {
			scopes = append(scopes, s)
			if i := strings.LastIndex(s, "::"); i >= 0 {
				s = s[:i]
			} else {
				s = ""
			}
		}
		scopes = append(scopes, "")
	}
	for _, s := range scopes {
		c := w
		if s != "" {
			c = s + "::" + w
		}
		if fs := l.classes[c]; len(fs) > 0 {
			return c, fs
		}
	}
	return "", nil
}

// base resolves a class's base entry (see rbBases).
func (l *rbCalls) base(p, b string) (hClassRef, bool) {
	if len(b) < 3 || b[1] != ':' {
		return hClassRef{}, false
	}
	side, rest := b[0], b[2:]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return hClassRef{}, false
	}
	scope, w := rest[:sp], rest[sp+1:]
	suffix := ""
	if side == 's' {
		suffix = rbSelf
	}
	if w == "self" {
		return hClassRef{p, scope + suffix}, true
	}
	c, fs := l.constant(scope, w)
	if len(fs) == 0 {
		return hClassRef{}, false
	}
	return hClassRef{fs[0], c + suffix}, true
}

// chain lists the classes a lookup from owner (in files) visits, in order.
func (l *rbCalls) chain(r *hResolver, files []string, owner string) []hClassRef {
	var queue, out []hClassRef
	for _, f := range files {
		queue = append(queue, hClassRef{f, owner})
	}
	seen := map[hClassRef]bool{}
	for len(queue) > 0 && len(seen) < 16 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		for _, b := range r.classes[c.path][c.owner] {
			if bc, ok := l.base(c.path, b); ok {
				queue = append(queue, bc)
			}
		}
	}
	return out
}

// find looks name up in class owner (its files in order) and up its
// chain, generated members (associations, columns, …) included. On a miss
// it returns the IDs the call would have had.
func (l *rbCalls) find(r *hResolver, files []string, owner, name string) (string, []string) {
	if id, _, _ := l.lookup(r, files, owner, name); id != "" {
		return id, nil
	}
	return "", l.miss(r, l.chain(r, files, owner), name)
}

// miss is the want of a call that didn't resolve. While a method of that
// name is still defined somewhere the call is counted anyway, so it can
// name the IDs along its chain; otherwise it's any removed method of that
// name, kept quiet (it's most likely a framework method).
func (l *rbCalls) miss(r *hResolver, chain []hClassRef, name string) []string {
	if len(r.byName[name]) == 0 && !l.synNames[name] {
		return r.anyMethodWant(name)
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range chain {
		id := l.id(c.path, &hDef{Owner: c.owner, Name: name})
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// filesOf lists the files of the class owner is a side of, p first when
// it defines it (where the calling code is).
func (l *rbCalls) filesOf(r *hResolver, p, owner string) []string {
	out := []string{}
	if _, ok := r.classes[p][owner]; ok {
		out = append(out, p)
	}
	for _, f := range l.classes[strings.TrimSuffix(owner, rbSelf)] {
		if f != p {
			out = append(out, f)
		}
	}
	return out
}

func (l *rbCalls) resolve(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	l.prepare(r)
	if strings.HasPrefix(c.Recv, "<") {
		return l.resolveRails(r, p, d, c)
	}
	recv := c.Recv
	if recv == rbHelperRecv {
		recv = rbSelfNew
	}
	ctx := d.Class // where self is: "C", "C.self", or "" (top level, views)
	scope := strings.TrimSuffix(ctx, rbSelf)
	switch {
	case d.Name == rbViewName && (recv == "" || recv == "self"):
		return l.viewCall(r, p, c.Name)
	case recv == "" || recv == "self":
		if ctx == "" {
			if t := r.def(p, "", c.Name); t != nil && !t.IsClass {
				return l.id(p, t), nil
			}
			if rbTestFramework(c.Name) {
				return hExternal, nil
			}
			return "", l.miss(r, nil, c.Name)
		}
		if c.Name == "new" && strings.HasSuffix(ctx, rbSelf) {
			return l.construct(r, l.filesOf(r, p, scope), scope)
		}
		id, want := l.find(r, l.filesOf(r, p, ctx), ctx, c.Name)
		if id == "" && l.framework(r, ctx, c.Name) {
			return hExternal, nil
		}
		return id, want
	case recv == rbSelfNew:
		if scope == "" {
			return "", nil
		}
		id, want := l.find(r, l.filesOf(r, p, scope), scope, c.Name)
		if id == "" && l.framework(r, scope, c.Name) {
			return hExternal, nil
		}
		return id, want
	case recv == "super":
		for _, b := range r.classes[p][ctx] {
			if bc, ok := l.base(p, b); ok {
				if def, dp := r.method(bc, c.Name, l.base); def != nil {
					return l.id(dp, def), nil
				}
			}
		}
		return "", nil
	case rbChainRecv(recv):
		return l.resolveTyped(r, p, d, c)
	case rbIsConst(recv):
		inst := strings.HasSuffix(recv, ".new")
		cp, files := l.constant(scope, strings.TrimSuffix(recv, ".new"))
		if len(files) == 0 {
			// Not a class here: a gem's, or one the change removed.
			w := strings.TrimPrefix(strings.TrimSuffix(recv, ".new"), "::")
			if !inst {
				w += rbSelf
			}
			return "", l.miss(r, []hClassRef{{"", w}}, c.Name)
		}
		t := rbType{cls: cp, kind: 's'}
		if inst {
			t.kind = 'i'
		}
		id, want, _, _ := l.callOn(r, t, c.Name)
		return id, want
	}
	// An unknown receiver: never guessed, but a removed method nothing else
	// is named like is still caught.
	return "", r.anyMethodWant(c.Name)
}

// framework reports whether a self call in owner (a class or its
// singleton) that the repo doesn't define is the framework's: a model's
// ActiveRecord methods, a controller's or mailer's, a test's assertions.
func (l *rbCalls) framework(r *hResolver, owner, name string) bool {
	cls := strings.TrimSuffix(owner, rbSelf)
	inst := owner == cls
	switch {
	case l.isModel(r, cls):
		if inst {
			return rbRecordFw[name]
		}
		return rbFinders[name] || rbRelations[name] || rbModelFw[name]
	case inst && l.inherits(r, cls, "ActionController::Base", "ActionController::API", "ApplicationController"):
		return rbControllerFw[name]
	case inst && l.isMailer(r, cls):
		return rbMailerFw[name]
	case inst && l.isJob(r, cls):
		return name == "perform_later" || name == "retry_job" || name == "arguments" || name == "job_id"
	case strings.HasSuffix(cls, "Test") || strings.HasSuffix(cls, "Spec") || l.inherits(r, cls, "ActiveSupport::TestCase", "Minitest::Test"):
		return rbTestFramework(name)
	}
	return false
}

// construct maps Const.new to what runs: initialize, if the class or an
// ancestor in the repo defines it. A ViewComponent without one runs its
// class (where its template call is).
func (l *rbCalls) construct(r *hResolver, files []string, c string) (string, []string) {
	if id, _ := l.find(r, files, c, "initialize"); id != "" {
		return id, nil
	}
	if strings.HasSuffix(c, "Component") {
		for _, f := range files {
			if d := r.def(f, "", c); d != nil && d.IsClass {
				return l.id(f, d), nil
			}
		}
	}
	return hExternal, nil // Class#new
}

// viewCall resolves a bare call in a template: a ViewComponent template's
// component class, the helpers in app/helpers (Rails mixes them all into
// views), and the helper_methods of the view's controller and its
// ancestors. A name more than one of them defines stays unresolved.
func (l *rbCalls) viewCall(r *hResolver, p, name string) (string, []string) {
	rel := p
	if i := strings.Index("/"+p, "/app/"); i >= 0 {
		rel = p[i:]
	}
	if strings.HasPrefix(rel, "app/components/") {
		stem := path.Base(rel)
		if i := strings.IndexByte(stem, '.'); i >= 0 {
			stem = stem[:i]
		}
		comp := path.Join(path.Dir(strings.TrimPrefix(rel, "app/components/")), stem)
		if c, fs := l.constant("", rbCamelize(comp)); len(fs) > 0 {
			if id, _ := l.find(r, fs, c, name); id != "" {
				return id, nil
			}
		}
	}
	found := map[string]bool{}
	for _, id := range l.helperIDs(r, name) {
		found[id] = true
	}
	if c, fs := l.viewController(rel); len(fs) > 0 {
		for _, cr := range l.chain(r, fs, c) {
			cd := r.def(cr.path, "", cr.owner)
			if cd == nil {
				continue
			}
			for _, hc := range cd.Calls {
				if hc.Recv == rbHelperRecv && hc.Name == name {
					if id, _ := l.find(r, fs, c, name); id != "" {
						found[id] = true
					}
				}
			}
		}
	}
	switch len(found) {
	case 0:
		return "", r.anyMethodWant(name)
	case 1:
		for id := range found {
			return id, nil
		}
	}
	return "", nil
}

// viewController is the controller (or mailer) whose views a template is
// among: app/views/admin/users/show.html.erb → Admin::UsersController,
// app/views/user_mailer/x → UserMailer, else ApplicationController.
func (l *rbCalls) viewController(rel string) (string, []string) {
	dir := path.Dir(strings.TrimPrefix(rel, "app/views/"))
	var cands []string
	if strings.HasPrefix(rel, "app/views/") && dir != "." {
		cands = append(cands, rbCamelize(dir)+"Controller", rbCamelize(dir))
	}
	for _, c := range append(cands, "ApplicationController") {
		if fs := l.classes[c]; len(fs) > 0 {
			return c, fs
		}
	}
	return "", nil
}

// rbCamelize turns a path into a constant path: admin/user_tours →
// Admin::UserTours.
func rbCamelize(p string) string {
	return controllerName("app/controllers/" + p + ".rb")
}
