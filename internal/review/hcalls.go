package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Heuristic call graphs, for the languages without a type checker here
// (Python, JS/TS, Ruby, Kotlin/Java, Swift). A scanner per language reads
// each file's definitions (with their line ranges), the calls in them, the
// names its imports bind and its classes' bases, from code whose comments
// and strings are blanked. That scan depends only on the file's content,
// so it's cached by blob id. A resolver then turns each call into a
// definition's ID, using rules the language supplies on top of a repo-wide
// index. A call it can't pin to one definition is counted, never guessed.

// hDef is one definition a scanner found: a function, method, or class (a
// class stands for its construction, and holds the calls in its body).
type hDef struct {
	Name  string `json:"n"`
	Owner string `json:"o,omitempty"` // enclosing definitions, dotted ("User", "Outer.inner"); "" at the top
	// Class is the nearest enclosing class's owner path ("" outside one):
	// where self/this calls resolve.
	Class   string  `json:"c,omitempty"`
	IsClass bool    `json:"k,omitempty"`
	Static  bool    `json:"s,omitempty"` // a class method (def self.x, static func)
	Line    int     `json:"l"`
	End     int     `json:"e"`
	Body    string  `json:"b"`
	Sig     string  `json:"g,omitempty"`
	Calls   []hCall `json:"x,omitempty"`
}

// hCall is a call (or a reference to a function as a value) in a
// definition's body.
type hCall struct {
	// Recv is the receiver as written: "" for a bare call, "self"/"this",
	// "super", a name or dotted path ("os.path", "Foo::Bar"), or "?" for a
	// receiver that's an expression (foo().bar()).
	Recv string `json:"r,omitempty"`
	Name string `json:"n"`
	Line int    `json:"l"`
	Ref  bool   `json:"f,omitempty"`
}

// hImport is a name an import binds in a file.
type hImport struct {
	Local string `json:"l"` // the bound name ("np", "save", "Button")
	Spec  string `json:"s"` // what the language resolves to a file
	Name  string `json:"n"` // the name taken from that file, "" for the module itself
	Line  int    `json:"i"`
}

// hClass is a class's bases (superclass, mixins, protocols) as written.
type hClass struct {
	Name  string   `json:"n"` // owner path
	Bases []string `json:"b,omitempty"`
}

// hFile is a scanned file. OK is false if the scanner couldn't make sense
// of it (unbalanced blocks mid-edit): its functions are then left out.
type hFile struct {
	Defs    []hDef    `json:"d,omitempty"`
	Imports []hImport `json:"i,omitempty"`
	Classes []hClass  `json:"c,omitempty"`
	OK      bool      `json:"ok"`
}

// hLang is what a heuristic language provides.
type hLang interface {
	name() string
	owns(p string) bool
	// setup prepares the language for one version of the tree (module
	// roots, path aliases), returning false if it doesn't apply.
	setup(idx *index) bool
	unit(p string) string
	// scanFile reads a file's definitions and calls. It may depend on the
	// file's extension but not the rest of its path: scans are cached by
	// blob id (and extension). Path-dependent naming belongs in id.
	scanFile(p, src string) hFile
	// fileOf resolves an import binding of file p to a repo file, "" if
	// it's outside the repo.
	fileOf(p string, imp hImport) string
	// id is a definition's ID: stable across versions for the same code.
	id(p string, d *hDef) string
	// display is a short name for the graph ("User.save").
	display(d *hDef) string
	// resolve finds a call's target ID (one definition), or returns the
	// IDs it would have had (want) when there's none.
	resolve(r *hResolver, p string, d *hDef, c hCall) (to string, want []string)
}

// hCalls adapts an hLang to callLang.
type hCalls struct{ l hLang }

func (h *hCalls) name() string { return h.l.name() }
func (h *hCalls) exact() bool  { return false }

func (h *hCalls) funcs(idx *index, files []string, full bool) (*callSet, error) {
	set := newCallSet()
	if !h.l.setup(idx) {
		return set, nil
	}
	r := newHResolver(h.l, idx)
	emit := files
	if full {
		emit = r.paths
	}
	for _, p := range emit {
		f := r.files[p]
		if f == nil {
			continue
		}
		if !f.OK {
			set.unparsed[p] = true
			continue
		}
		test := isTest(p)
		for i := range f.Defs {
			d := &f.Defs[i]
			fn := &fn{Func: Func{ID: h.l.id(p, d), Name: h.l.display(d), Path: p, Line: d.Line, End: d.End,
				Unit: h.l.unit(p), Lang: h.l.name(), Test: test}, body: d.Body, sig: d.Sig}
			for _, c := range d.Calls {
				to, want := h.l.resolve(r, p, d, c)
				switch {
				case to != "" && to != fn.ID:
					kind := CallStatic
					if c.Ref {
						kind = CallRef
					}
					fn.calls = append(fn.calls, callSite{to: to, kind: kind, line: c.Line})
				case to == "" && !c.Ref:
					// Count it if it looks like this repo's code: the name
					// is defined here, or it was meant to be (an import).
					counted := r.byName[c.Name] != nil
					for _, w := range want {
						counted = counted || !strings.HasPrefix(w, "~")
					}
					if counted || len(want) > 0 {
						fn.unresolved = append(fn.unresolved, unresolvedSite{line: c.Line, want: want, quiet: !counted})
					}
				}
			}
			set.add(fn)
		}
	}
	return set, nil
}

// hDefRef locates a definition in the index.
type hDefRef struct {
	path string
	def  *hDef
}

// hResolver is one version's index for a heuristic language.
type hResolver struct {
	l     hLang
	idx   *index
	paths []string // owned files, sorted
	files map[string]*hFile
	// byKey: path → owner-qualified name ("User.save", "helper") → def.
	byKey map[string]map[string]*hDef
	// byName: bare name → every definition with it.
	byName map[string][]hDefRef
	// classes: path → class owner path → bases.
	classes map[string]map[string][]string
	// imports: path → local name → binding.
	imports map[string]map[string]hImport
}

func newHResolver(l hLang, idx *index) *hResolver {
	r := &hResolver{l: l, idx: idx, files: map[string]*hFile{}, byKey: map[string]map[string]*hDef{},
		byName: map[string][]hDefRef{}, classes: map[string]map[string][]string{}, imports: map[string]map[string]hImport{}}
	key := func(p string) string { return "hcalls:" + l.name() + path.Ext(p) }
	var reads []string
	for _, p := range idx.paths {
		if l.owns(p) && !idx.cached(key(p), p) {
			reads = append(reads, p)
		}
	}
	idx.prefetch(reads)
	for _, p := range idx.paths {
		if !l.owns(p) {
			continue
		}
		v := idx.symbols(key(p), p, func(src string) any { f := l.scanFile(p, src); return &f })
		f, _ := v.(*hFile)
		if f == nil {
			continue
		}
		r.paths = append(r.paths, p)
		r.files[p] = f
		keys := map[string]*hDef{}
		for i := range f.Defs {
			d := &f.Defs[i]
			keys[qualify(d.Owner, d.Name)] = d
			r.byName[d.Name] = append(r.byName[d.Name], hDefRef{p, d})
		}
		r.byKey[p] = keys
		cls := map[string][]string{}
		for _, c := range f.Classes {
			cls[c.Name] = c.Bases
		}
		r.classes[p] = cls
		imps := map[string]hImport{}
		for _, imp := range f.Imports {
			imps[imp.Local] = imp
		}
		r.imports[p] = imps
	}
	sort.Strings(r.paths)
	return r
}

func qualify(owner, name string) string {
	if owner == "" {
		return name
	}
	return owner + "." + name
}

// def looks up owner.name in file p.
func (r *hResolver) def(p, owner, name string) *hDef {
	return r.byKey[p][qualify(owner, name)]
}

// idOf is the ID of owner.name in p, whether or not it exists (for want).
func (r *hResolver) idOf(p, owner, name string) string {
	if d := r.def(p, owner, name); d != nil {
		return r.l.id(p, d)
	}
	return r.l.id(p, &hDef{Owner: owner, Name: name})
}

// hClassRef locates a class: its file and owner path.
type hClassRef struct{ path, owner string }

// method finds name in class c or, failing that, up its bases (as the
// language resolves them through base), at most depth classes deep.
// self says the lookup is for a self/this call, where the class's own
// methods and its bases' count.
func (r *hResolver) method(c hClassRef, name string, base func(p, name string) (hClassRef, bool)) (*hDef, string) {
	seen := map[hClassRef]bool{}
	queue := []hClassRef{c}
	for len(queue) > 0 && len(seen) < 16 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		if d := r.def(c.path, c.owner, name); d != nil {
			return d, c.path
		}
		for _, b := range r.classes[c.path][c.owner] {
			if bc, ok := base(c.path, b); ok {
				queue = append(queue, bc)
			}
		}
	}
	return nil, ""
}

// commonMethods are names the standard libraries use on collections,
// strings and streams: an unknown receiver calling one is far more likely a
// list or a string than the one repo method that happens to share it.
var commonMethods = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`append add addAll insert remove removeAll removeAt removeFirst removeLast
		pop push shift unshift splice slice concat clear count size length isEmpty contains includes has
		get set put delete update keys values items entries first last min max sum sort sorted reverse
		reversed map flatMap compactMap filter reduce forEach each find findIndex index indexOf some every
		join split trim strip lower upper lowercased uppercased replace replacing startswith endswith
		hasPrefix hasSuffix format toString description equals hashCode hash copy clone close open read
		write flush then catch finally subscribe next resume cancel`) {
		commonMethods[n] = true
	}
}

// uniqueMethod is the one method (a definition inside a class) named name
// in the whole tree, if there's exactly one.
func (r *hResolver) uniqueMethod(name string) (string, bool) {
	if commonMethods[name] {
		return "", false
	}
	var found []hDefRef
	for _, d := range r.byName[name] {
		if d.def.Class != "" && !d.def.IsClass {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return r.l.id(found[0].path, found[0].def), true
}

// anyMethodWant is the want of a call on an unknown receiver: any removed
// method of that name ("~name"), but only when no method of the name is
// left, so a removed save() isn't blamed for calls to another save().
func (r *hResolver) anyMethodWant(name string) []string {
	for _, d := range r.byName[name] {
		if d.def.Class != "" && !d.def.IsClass {
			return nil
		}
	}
	return []string{"~" + name}
}

// enclosingScopes lists d's enclosing owner paths, innermost first, ending
// with the file's top level (""): where a bare name is looked up.
func enclosingScopes(d *hDef) []string {
	var out []string
	own := qualify(d.Owner, d.Name)
	for own != "" {
		out = append(out, own)
		i := strings.LastIndex(own, ".")
		if i < 0 {
			own = ""
		} else {
			own = own[:i]
		}
	}
	return append(out, "")
}

// hBody turns a definition's source lines into its body hash input: every
// line trimmed and whitespace collapsed, comment-only lines dropped.
func hBody(lines []string, from, to int, comment *regexp.Regexp) string {
	var b strings.Builder
	for i := from; i <= to && i < len(lines); i++ {
		t := strings.Join(strings.Fields(lines[i]), " ")
		if t == "" || (comment != nil && comment.MatchString(t)) {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return hashOf(b.String())
}

// hLinesBody is hBody over a definition's own lines (0-based indexes).
func hLinesBody(lines []string, own []int, comment *regexp.Regexp) string {
	var b strings.Builder
	for _, i := range own {
		if i < 0 || i >= len(lines) {
			continue
		}
		t := strings.Join(strings.Fields(lines[i]), " ")
		if t == "" || (comment != nil && comment.MatchString(t)) {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return hashOf(b.String())
}

// hCallsIn finds call sites in one line of blanked code: name( and
// recv.name( with recv a dotted chain of identifiers (or "?" when it's an
// expression), skipping the language's keywords.
func hCallsIn(line string, lineNo int, ident *regexp.Regexp, sep string, keywords map[string]bool) []hCall {
	var out []hCall
	for _, m := range ident.FindAllStringSubmatchIndex(line, -1) {
		chain := line[m[2]:m[3]]
		parts := strings.Split(chain, sep)
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		name := parts[len(parts)-1]
		if keywords[name] || (len(parts) == 1 && keywords[parts[0]]) {
			continue
		}
		recv := strings.Join(parts[:len(parts)-1], sep)
		// A chain right after a "." or ")" hangs off an expression.
		if j := lastNonSpace(line[:m[2]]); j >= 0 && (line[j] == '.' || line[j] == ')' || line[j] == ']') {
			if recv == "" {
				recv = "?"
			} else {
				recv = "?" + sep + recv
			}
		}
		out = append(out, hCall{Recv: recv, Name: name, Line: lineNo})
	}
	return out
}

func lastNonSpace(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' {
			return i
		}
	}
	return -1
}
