package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// swiftCalls is the Swift call analyzer. Definitions come from brace depth
// on swiftCode: types (class, struct, enum, actor, protocol), funcs, inits,
// subscripts, deinit and computed properties, with `extension T` members
// belonging to T wherever the extension is. Calls resolve through the
// enclosing type, its extensions anywhere in the module and its superclass
// chain, then the module's top level (files in one module see each other
// without imports), then the in-repo modules the file imports; a call on an
// unknown receiver only when one method in the repo has its name, or when
// the only candidates are one protocol's requirement and its
// implementations (then the call goes to the requirement).
//
// Modules are SwiftPM targets (test targets included). Everything outside
// them is one app module, named after the repo's only .xcodeproj ("App"
// without one): an app's folders, its extensions and its test bundles,
// which `@testable import` the app and so see all of it anyway. A
// `@testable import` resolves like any import, so tests reach the code
// they test (the architecture analyzer skips them, since a test importing
// its module isn't a dependency).
//
// IDs are Module.Type.method / Module.fn / Module.Type.init; overloads share
// a node. Types record their kind ("class", "protocol", …) as the
// signature, and computed properties have none: that's how the resolver
// tells them apart.
type swiftCalls struct {
	sl   *swiftLang        // targets and units, as the architecture analyzer sees them
	mods map[string]string // SwiftPM target directory → module name, test targets included
	dirs []string          // those directories, longest first
	app  string            // the module of everything outside a target
	ix   *swiftIndex       // the current resolver's index
}

func (l *swiftCalls) name() string { return "swift" }
func (l *swiftCalls) owns(p string) bool {
	return strings.HasSuffix(p, ".swift") && path.Base(p) != "Package.swift"
}
func (l *swiftCalls) unit(p string) string { return l.sl.unit(p) }

var spmAnyTargetRe = regexp.MustCompile(`\.(target|executableTarget|macro|testTarget)\(\s*name:\s*"([^"]+)"([^)]*)`)

// setup finds the modules: SwiftPM targets from every Package.swift (the
// same parse as swiftLang.detect, plus test targets), and the app module.
// It doesn't run swiftLang.detect, which reads every file's declarations.
func (l *swiftCalls) setup(idx *index) bool {
	found := false
	for _, p := range idx.paths {
		if l.owns(p) {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	l.sl = &swiftLang{idx: idx, targets: map[string]string{}}
	l.mods = map[string]string{}
	l.dirs = nil
	l.ix = nil
	var projects []string
	for _, p := range idx.paths {
		if strings.HasSuffix(p, ".xcodeproj/project.pbxproj") {
			projects = append(projects, strings.TrimSuffix(path.Base(path.Dir(p)), ".xcodeproj"))
		}
		if path.Base(p) != "Package.swift" {
			continue
		}
		src := idx.read(p)
		if src == nil {
			continue
		}
		pkg := path.Dir(p)
		for _, m := range spmAnyTargetRe.FindAllStringSubmatch(swiftCodeKeepStrings(*src), -1) {
			sub := "Sources"
			if m[1] == "testTarget" {
				sub = "Tests"
			}
			dir := path.Join(pkg, sub, m[2])
			if pm := spmPathRe.FindStringSubmatch(m[3]); pm != nil {
				dir = path.Join(pkg, pm[1])
			}
			l.mods[dir] = m[2]
			l.dirs = append(l.dirs, dir)
			if m[1] != "testTarget" {
				l.sl.targets[m[2]] = dir
				l.sl.dirs = append(l.sl.dirs, dir)
			}
		}
	}
	sort.Slice(l.dirs, func(i, j int) bool { return len(l.dirs[i]) > len(l.dirs[j]) })
	sort.Slice(l.sl.dirs, func(i, j int) bool { return len(l.sl.dirs[i]) > len(l.sl.dirs[j]) })
	l.app = "App"
	if len(projects) == 1 {
		l.app = projects[0]
	}
	return true
}

// moduleOf is the module file p belongs to.
func (l *swiftCalls) moduleOf(p string) string {
	for _, d := range l.dirs {
		if strings.HasPrefix(p, d+"/") {
			return l.mods[d]
		}
	}
	return l.app
}

// swiftTop names the pseudo-definition holding a file's calls outside any
// definition (main.swift's statements, global initializers).
const swiftTop = "<top>"

func (l *swiftCalls) id(p string, d *hDef) string {
	if d.Name == swiftTop && d.Owner == "" {
		return l.moduleOf(p) + "." + swiftTop + p
	}
	return l.moduleOf(p) + "." + qualify(d.Owner, d.Name)
}

func (l *swiftCalls) display(d *hDef) string {
	if d.Name == swiftTop {
		return "top level"
	}
	parts := strings.Split(qualify(d.Owner, d.Name), ".")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, ".")
}

// fileOf isn't used: a Swift import names a module, not a file.
func (l *swiftCalls) fileOf(string, hImport) string { return "" }

// swiftFrame is an open body while scanning.
type swiftFrame struct {
	def    int    // index into the file's Defs, -1 for an extension
	path   string // owner path of what's inside
	class  string // the nearest type's path, for what's inside
	isType bool   // a type or extension body (where properties are members)
	proto  bool   // a protocol body: its funcs have no bodies
	depth  int    // brace depth inside the body
}

// swiftSpan is where a definition is in the blanked code: from its keyword
// (start) through its header to its body's "{" (body, -1 without one), to
// its end.
type swiftSpan struct {
	start, body, end int
	params           string
}

var (
	swiftCallRe    = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:[ \t]*[?!]?[ \t]*\.[ \t]*[A-Za-z_][A-Za-z0-9_]*)*)[ \t]*([({])`)
	swiftIdentRe   = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	swiftSelRe     = regexp.MustCompile(`#selector\s*\(\s*(?:(?:getter|setter)\s*:\s*)?([A-Za-z_][A-Za-z0-9_.]*)`)
	swiftStaticRe  = regexp.MustCompile(`\b(?:static|class)\b`)
	swiftLocalRe   = regexp.MustCompile(`\b(?:let|var|for)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	swiftTupleRe   = regexp.MustCompile(`\b(?:let|var|for)\s*\(([^)]*)\)`)
	swiftClosureRe = regexp.MustCompile(`\{\s*(?:\[[^\]]*\]\s*)?\(?([A-Za-z_][A-Za-z0-9_\s,:]*?)\)?\s+in\b`)
	swiftWhereRe   = regexp.MustCompile(`\bwhere\b`)
	swiftAttrRe    = regexp.MustCompile(`^@[A-Za-z_][A-Za-z0-9_.]*(?:\([^)]*\))?\s*`)
)

// swiftModifiers can precede a declaration ("class" too, as in class func).
var swiftModifiers = map[string]bool{
	"private": true, "fileprivate": true, "internal": true, "public": true, "open": true, "static": true,
	"class": true, "final": true, "override": true, "mutating": true, "nonmutating": true, "lazy": true,
	"weak": true, "unowned": true, "required": true, "convenience": true, "dynamic": true, "optional": true,
	"indirect": true, "nonisolated": true, "isolated": true, "package": true,
}

// swiftNoCall are names never called: keywords, plus a few global
// functions of the standard library.
var swiftNoCall = map[string]bool{
	"if": true, "guard": true, "while": true, "for": true, "switch": true, "return": true, "catch": true,
	"throw": true, "try": true, "await": true, "in": true, "as": true, "is": true, "some": true, "any": true,
	"where": true, "else": true, "repeat": true, "do": true, "defer": true, "let": true, "var": true, "func": true,
	"case": true, "default": true, "get": true, "set": true, "didSet": true, "willSet": true, "self": true,
	"super": true, "Self": true, "init": true, "deinit": true, "subscript": true, "import": true, "throws": true,
	"rethrows": true, "async": true, "type": true, "print": true, "debugPrint": true, "fatalError": true,
	"precondition": true, "preconditionFailure": true, "assert": true, "assertionFailure": true, "min": true,
	"max": true, "abs": true, "zip": true, "stride": true, "class": true, "struct": true, "enum": true,
	"protocol": true, "extension": true, "actor": true, "typealias": true, "associatedtype": true,
}

// swiftNoRef are lowercase words that are never a reference to a function
// or property: keywords and contextual names.
var swiftNoRef = map[string]bool{
	"if": true, "else": true, "guard": true, "while": true, "for": true, "in": true, "repeat": true, "do": true,
	"switch": true, "case": true, "default": true, "break": true, "continue": true, "fallthrough": true,
	"return": true, "throw": true, "throws": true, "rethrows": true, "try": true, "catch": true, "defer": true,
	"let": true, "var": true, "func": true, "init": true, "deinit": true, "subscript": true, "class": true,
	"struct": true, "enum": true, "protocol": true, "extension": true, "actor": true, "import": true,
	"typealias": true, "associatedtype": true, "where": true, "as": true, "is": true, "some": true, "any": true,
	"self": true, "super": true, "nil": true, "true": true, "false": true, "get": true, "set": true,
	"willSet": true, "didSet": true, "async": true, "await": true, "inout": true, "newValue": true,
	"oldValue": true, "error": true, "_": true, "operator": true, "prefix": true, "postfix": true, "infix": true,
	"macro": true, "consume": true, "borrowing": true, "consuming": true, "each": true,
}

// swiftDeclBefore are words after which a name is being declared, not used.
var swiftDeclBefore = map[string]bool{
	"func": true, "let": true, "var": true, "case": true, "import": true, "typealias": true,
	"associatedtype": true, "class": true, "struct": true, "enum": true, "protocol": true, "extension": true,
	"actor": true, "for": true, "some": true, "any": true, "as": true, "is": true, "indirect": true,
	"precedencegroup": true, "operator": true,
}

// swiftNoClosure are a line's first words that make a "name {" on it a
// declaration or a control statement, not a trailing closure.
var swiftNoClosure = map[string]bool{
	"if": true, "guard": true, "while": true, "for": true, "switch": true, "else": true, "catch": true,
	"func": true, "init": true, "deinit": true, "subscript": true, "class": true, "struct": true, "enum": true,
	"extension": true, "protocol": true, "actor": true, "case": true, "default": true, "get": true, "set": true,
	"didSet": true, "willSet": true, "do": true, "repeat": true, "defer": true, "var": true, "let": true,
	"precedencegroup": true,
}

func swiftIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// swiftScanner holds one file's scan.
type swiftScanner struct {
	code   string
	starts []int // offsets of line starts
	f      hFile
	spans  []swiftSpan
}

func (s *swiftScanner) lineOf(off int) int {
	return sort.Search(len(s.starts), func(i int) bool { return s.starts[i] > off })
}

func (s *swiftScanner) skipSpace(i int) int {
	for i < len(s.code) && (s.code[i] == ' ' || s.code[i] == '\t' || s.code[i] == '\n' || s.code[i] == '\r') {
		i++
	}
	return i
}

func (s *swiftScanner) ident(i int) string {
	j := i
	for j < len(s.code) && swiftIdentByte(s.code[j]) {
		j++
	}
	if j > i && s.code[i] >= '0' && s.code[i] <= '9' {
		return ""
	}
	return s.code[i:j]
}

// skipAngle skips a generic parameter list starting at i ("<").
func (s *swiftScanner) skipAngle(i int) int {
	if i >= len(s.code) || s.code[i] != '<' {
		return i
	}
	d := 0
	for ; i < len(s.code); i++ {
		switch s.code[i] {
		case '<':
			d++
		case '>':
			d--
			if d == 0 {
				return i + 1
			}
		case '{', '}', ';':
			return i
		}
	}
	return i
}

// bodyBrace finds the "{" opening a body after i, outside parentheses and
// brackets: -1 if a "}" or ";" comes first. stopAtLine also gives up at a
// newline (a property without a body).
func (s *swiftScanner) bodyBrace(i int, stopAtLine, stopAtAssign bool) int {
	d := 0
	for ; i < len(s.code); i++ {
		switch c := s.code[i]; c {
		case '(', '[':
			d++
		case ')', ']':
			d--
		case '{':
			if d <= 0 {
				return i
			}
		case '}', ';':
			if d <= 0 {
				return -1
			}
		case '\n':
			if stopAtLine && d <= 0 {
				return -1
			}
		case '=':
			if stopAtAssign && d <= 0 && !(i+1 < len(s.code) && s.code[i+1] == '=') {
				return -1
			}
		}
	}
	return -1
}

// lineEnd is the offset of the newline ending i's line (at paren depth 0).
func (s *swiftScanner) lineEnd(i int) int {
	d := 0
	for ; i < len(s.code); i++ {
		switch s.code[i] {
		case '(', '[':
			d++
		case ')', ']':
			d--
		case '\n':
			if d <= 0 {
				return i - 1
			}
		}
	}
	return len(s.code) - 1
}

// prevNonSpace is the index of the last non-blank byte before i, -1 if none.
func (s *swiftScanner) prevNonSpace(i int, sameLine bool) int {
	for i--; i >= 0; i-- {
		c := s.code[i]
		if c == '\n' && sameLine {
			return -1
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return i
		}
	}
	return -1
}

// prevWord is the identifier ending right before i (spaces skipped).
func (s *swiftScanner) prevWord(i int) string {
	j := s.prevNonSpace(i, false)
	if j < 0 || !swiftIdentByte(s.code[j]) {
		return ""
	}
	k := j
	for k > 0 && swiftIdentByte(s.code[k-1]) {
		k--
	}
	return s.code[k : j+1]
}

// statementText is the text from the start of i's statement (the last
// newline, ";", "{" or "}") to i.
func (s *swiftScanner) statementText(i int) string {
	k := strings.LastIndexAny(s.code[:i], "\n;{}")
	return s.code[k+1 : i]
}

// lineHead is the first word of i's line, past closing braces, attributes
// and modifiers.
func (s *swiftScanner) lineHead(i int) string {
	k := strings.LastIndexByte(s.code[:i], '\n')
	t := strings.TrimLeft(s.code[k+1:i], " \t}")
	for {
		t = strings.TrimLeft(t, " \t")
		if m := swiftAttrRe.FindString(t); m != "" {
			t = t[len(m):]
			continue
		}
		w := t
		if j := strings.IndexFunc(t, func(r rune) bool { return r > 127 || !swiftIdentByte(byte(r)) }); j >= 0 {
			w = t[:j]
		}
		if swiftModifiers[w] && w != "" {
			t = t[len(w):]
			continue
		}
		return w
	}
}

func (s *swiftScanner) addDef(d hDef, sp swiftSpan) int {
	s.f.Defs = append(s.f.Defs, d)
	s.spans = append(s.spans, sp)
	return len(s.f.Defs) - 1
}

// scanFile reads a Swift file's definitions, calls, imports and the bases
// its types and extensions declare.
func (l *swiftCalls) scanFile(_, src string) hFile {
	s := &swiftScanner{code: swiftCode(src), f: hFile{OK: true}}
	s.starts = append(s.starts, 0)
	for i := 0; i < len(s.code); i++ {
		if s.code[i] == '\n' {
			s.starts = append(s.starts, i+1)
		}
	}
	for _, m := range swiftImportRe.FindAllStringSubmatchIndex(s.code, -1) {
		mod := s.code[m[4]:m[5]]
		s.f.Imports = append(s.f.Imports, hImport{Local: mod, Spec: mod, Line: s.lineOf(m[0])})
	}
	s.decls()
	s.calls()
	norm := strings.Split(swiftNoComments(src), "\n")
	s.bodies(norm)
	return s.f
}

// decls walks the code by brace depth, recording definitions and their
// spans.
func (s *swiftScanner) decls() {
	code := s.code
	var stack []swiftFrame
	pending := map[int]swiftFrame{} // body "{" offset → the frame it opens
	bases := map[string]int{}       // type path → index into Classes
	addBases := func(name string, b []string) {
		if i, ok := bases[name]; ok {
			s.f.Classes[i].Bases = append(s.f.Classes[i].Bases, b...)
			return
		}
		bases[name] = len(s.f.Classes)
		s.f.Classes = append(s.f.Classes, hClass{Name: name, Bases: b})
	}
	top := func() swiftFrame {
		if len(stack) == 0 {
			return swiftFrame{def: -1, isType: true}
		}
		return stack[len(stack)-1]
	}
	depth := 0
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == '{':
			depth++
			if fr, ok := pending[i]; ok {
				fr.depth = depth
				stack = append(stack, fr)
				delete(pending, i)
			}
			i++
			continue
		case c == '}':
			if n := len(stack); n > 0 && stack[n-1].depth == depth {
				if d := stack[n-1].def; d >= 0 {
					s.spans[d].end = i
					s.f.Defs[d].End = s.lineOf(i)
				}
				stack = stack[:n-1]
			}
			depth--
			if depth < 0 {
				s.f.OK = false
				depth = 0
			}
			i++
			continue
		case !swiftIdentByte(c) || (i > 0 && swiftIdentByte(code[i-1])):
			i++
			continue
		}
		w := s.ident(i)
		if w == "" {
			i++
			continue
		}
		j := i + len(w)
		if p := s.prevNonSpace(i, false); p >= 0 && code[p] == '.' {
			i = j // a member (x.init, x.class), not a declaration
			continue
		}
		fr := top()
		static := func() bool { return swiftStaticRe.MatchString(s.statementText(i)) }
		switch w {
		case "class", "struct", "enum", "actor", "protocol":
			k := s.skipSpace(j)
			name := s.ident(k)
			if name == "" || swiftModifiers[name] || name == "func" || name == "var" || name == "let" ||
				name == "subscript" || name == "init" || name == "typealias" {
				break // class func, class var, …: a modifier
			}
			after := s.skipAngle(k + len(name))
			brace := s.bodyBrace(after, false, false)
			if brace < 0 {
				break
			}
			p := qualify(fr.path, name)
			d := s.addDef(hDef{Name: name, Owner: fr.path, Class: fr.class, IsClass: true, Line: s.lineOf(i), Sig: w},
				swiftSpan{start: i, body: brace, end: len(code) - 1})
			if b := swiftBases(code[after:brace]); len(b) > 0 {
				addBases(p, b)
			}
			pending[brace] = swiftFrame{def: d, path: p, class: p, isType: true, proto: w == "protocol"}
			j = k + len(name)
		case "extension":
			k := s.skipSpace(j)
			e := k
			for e < len(code) && (swiftIdentByte(code[e]) || code[e] == '.') {
				e++
			}
			name := code[k:e]
			if name == "" {
				break
			}
			after := s.skipAngle(e)
			brace := s.bodyBrace(after, false, false)
			if brace < 0 {
				break
			}
			addBases(name, swiftBases(code[after:brace]))
			pending[brace] = swiftFrame{def: -1, path: name, class: name, isType: true}
			j = e
		case "func", "init", "subscript":
			k, name := j, w
			if w == "func" {
				k = s.skipSpace(j)
				if name = s.ident(k); name != "" {
					k += len(name)
				} else {
					// An operator (func ==) or a `backticked` name.
					e := k
					for e < len(code) && code[e] != '(' && code[e] != '<' && code[e] != ' ' && code[e] != '\n' {
						e++
					}
					name = strings.Trim(code[k:e], "`")
					k = e
				}
			} else if k < len(code) && (code[k] == '?' || code[k] == '!') {
				k++
			}
			k = s.skipAngle(s.skipSpace(k))
			k = s.skipSpace(k)
			if name == "" || k >= len(code) || code[k] != '(' {
				break
			}
			cl := matchingParen(code, k)
			if cl < 0 {
				s.f.OK = false
				break
			}
			brace := -1
			if !fr.proto {
				brace = s.bodyBrace(cl+1, false, false)
			}
			end := brace
			if brace < 0 {
				end = s.lineEnd(cl + 1)
			}
			params := code[k+1 : cl]
			rest := code[cl+1 : max(cl+1, end)]
			sig := strings.Join(strings.Fields(params), "") + "->" + strings.Join(strings.Fields(rest), "")
			d := s.addDef(hDef{Name: name, Owner: fr.path, Class: fr.class, Static: static(), Line: s.lineOf(i), Sig: hashOf(sig)},
				swiftSpan{start: i, body: brace, end: end, params: params})
			if brace < 0 {
				s.f.Defs[d].End = s.lineOf(end)
			} else {
				pending[brace] = swiftFrame{def: d, path: qualify(fr.path, name), class: fr.class}
			}
			j = k
		case "deinit":
			k := s.skipSpace(j)
			if k >= len(code) || code[k] != '{' {
				break
			}
			d := s.addDef(hDef{Name: w, Owner: fr.path, Class: fr.class, Line: s.lineOf(i), Sig: hashOf(w)},
				swiftSpan{start: i, body: k, end: len(code) - 1})
			pending[k] = swiftFrame{def: d, path: qualify(fr.path, w), class: fr.class}
		case "var", "let":
			// A computed property (or one with observers, or a protocol's
			// requirement): a member or a global with a body.
			if !fr.isType {
				break
			}
			k := s.skipSpace(j)
			name := s.ident(k)
			if name == "" {
				break
			}
			brace := s.bodyBrace(k+len(name), true, true)
			if brace < 0 {
				break
			}
			d := s.addDef(hDef{Name: name, Owner: fr.path, Class: fr.class, Static: static(), Line: s.lineOf(i)},
				swiftSpan{start: i, body: brace, end: len(code) - 1})
			pending[brace] = swiftFrame{def: d, path: qualify(fr.path, name), class: fr.class}
			j = k + len(name)
		}
		i = j
	}
	if len(stack) > 0 || depth != 0 {
		s.f.OK = false
	}
	last := s.lineOf(len(code) - 1)
	for i := range s.f.Defs {
		if s.f.Defs[i].End == 0 {
			s.f.Defs[i].End = last
		}
	}
}

// swiftBases reads a declaration's inheritance clause (": A, B<C>, D.E"
// up to a where clause).
func swiftBases(header string) []string {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, ":") {
		return nil
	}
	header = header[1:]
	if w := swiftWhereRe.FindStringIndex(header); w != nil {
		header = header[:w[0]]
	}
	var out []string
	d, from := 0, 0
	add := func(b string) {
		if i := strings.IndexByte(b, '<'); i >= 0 {
			b = b[:i]
		}
		f := strings.Fields(b)
		for len(f) > 1 && (strings.HasPrefix(f[0], "@") || f[0] == "any") {
			f = f[1:]
		}
		if len(f) > 0 {
			b = strings.Join(strings.Fields(strings.Join(f, "")), "")
			if b != "" && b != "AnyObject" && b != "class" {
				out = append(out, b)
			}
		}
	}
	for i := 0; i < len(header); i++ {
		switch header[i] {
		case '<', '(', '[':
			d++
		case '>', ')', ']':
			d--
		case ',':
			if d == 0 {
				add(header[from:i])
				from = i + 1
			}
		}
	}
	add(header[from:])
	return out
}

// owner is the innermost definition containing offset o, -1 for none.
// Definitions are in start order and nest, so the last one containing o is
// the innermost.
func (s *swiftScanner) owner(o int) int {
	for i := len(s.spans) - 1; i >= 0; i-- {
		if sp := s.spans[i]; sp.start <= o && o <= sp.end {
			return i
		}
	}
	return -1
}

// calls finds call sites and references, and puts each in its innermost
// definition (or the file's top level).
func (s *swiftScanner) calls() {
	code := s.code
	top := -1
	add := func(off int, c hCall) {
		d := s.owner(off)
		if d < 0 {
			if top < 0 {
				top = s.addDef(hDef{Name: swiftTop, Line: c.Line, End: c.Line}, swiftSpan{start: -1, body: -1, end: -2})
			}
			d = top
			s.f.Defs[d].End = max(s.f.Defs[d].End, c.Line)
		}
		s.f.Defs[d].Calls = append(s.f.Defs[d].Calls, c)
	}
	called := map[int]bool{} // offsets of names taken as calls
	for _, m := range swiftCallRe.FindAllStringSubmatchIndex(code, -1) {
		chain, open := code[m[2]:m[3]], code[m[4]]
		parts := strings.FieldsFunc(chain, func(r rune) bool { return r == '.' || r == '?' || r == '!' || r == ' ' || r == '\t' })
		name := parts[len(parts)-1]
		recv := strings.Join(parts[:len(parts)-1], ".")
		if len(parts) > 1 && swiftNoCall[parts[0]] && parts[0] != "self" && parts[0] != "super" {
			continue
		}
		before := s.prevNonSpace(m[2], false)
		if before >= 0 {
			switch code[before] {
			case '@', '#', '\\', '$':
				continue
			case '.':
				// After an expression it's a member of an unknown value;
				// otherwise it's an implicit member (.init(, .shared.x().
				dot := before
				q := s.prevNonSpace(dot, false)
				if q >= 0 && (swiftIdentByte(code[q]) || strings.ContainsRune(")]}?!>", rune(code[q]))) &&
					!swiftNoRef[s.prevWord(dot+0)] {
					recv = strings.Trim("?."+recv, ".")
				} else {
					recv = "."
				}
			}
		}
		if recv == "" && swiftNoCall[name] {
			continue // a keyword; a member may be called anything (defaults.set(, queue.async {)
		}
		if w := s.prevWord(m[2]); swiftDeclBefore[w] && (before < 0 || code[before] != '.') {
			continue
		}
		if open == '{' {
			if !s.trailingClosure(m[2], before) {
				continue
			}
		}
		called[m[2]+len(chain)-len(name)] = true
		add(m[2], hCall{Recv: recv, Name: name, Line: s.lineOf(m[2])})
	}
	for _, m := range swiftSelRe.FindAllStringSubmatchIndex(code, -1) {
		parts := strings.Split(code[m[2]:m[3]], ".")
		add(m[0], hCall{Recv: strings.Join(parts[:len(parts)-1], "."), Name: parts[len(parts)-1], Line: s.lineOf(m[0]), Ref: true})
	}
	s.refs(called, add)
}

// trailingClosure reports whether "name {" at i (with before the previous
// non-blank byte) is a call with a trailing closure rather than a
// declaration or a control statement's condition.
func (s *swiftScanner) trailingClosure(i, before int) bool {
	head := s.lineHead(i)
	if swiftNoClosure[head] {
		if head != "var" && head != "let" {
			return false
		}
		// let x = make { … }, not var body: some View { … }
		st := s.statementText(i)
		if !strings.Contains(strings.NewReplacer("==", "", "!=", "", ">=", "", "<=", "").Replace(st), "=") {
			return false
		}
	}
	if before < 0 || s.lineOf(before) != s.lineOf(i) {
		return true
	}
	switch s.code[before] {
	case '(', ',', '[', '{', '.':
		return true
	case '=':
		return before == 0 || !strings.ContainsRune("=!<>", rune(s.code[before-1]))
	}
	switch s.prevWord(i) {
	case "return", "try", "await", "in":
		return true
	}
	return false
}

// refs finds lowercase names used as values: a function passed along
// (action: save) or a computed property read (body showing header). Each
// is a reference the resolver keeps only if it names a function or
// property in scope; names the definition binds itself are skipped.
func (s *swiftScanner) refs(called map[int]bool, add func(int, hCall)) {
	code := s.code
	locals := map[int]map[string]bool{}
	localsOf := func(d int) map[string]bool {
		if d < 0 {
			return nil
		}
		if l, ok := locals[d]; ok {
			return l
		}
		sp := s.spans[d]
		l := map[string]bool{}
		for _, id := range swiftIdentRe.FindAllString(sp.params, -1) {
			l[id] = true
		}
		text := code[sp.start : sp.end+1]
		for _, m := range swiftLocalRe.FindAllStringSubmatch(text, -1) {
			l[m[1]] = true
		}
		for _, re := range []*regexp.Regexp{swiftTupleRe, swiftClosureRe} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				for _, part := range strings.Split(m[1], ",") {
					if f := swiftIdentRe.FindString(part); f != "" {
						l[f] = true
					}
				}
			}
		}
		locals[d] = l
		return l
	}
	seen := map[int]map[string]bool{}
	for _, m := range swiftIdentRe.FindAllStringIndex(code, -1) {
		name := code[m[0]:m[1]]
		if c := name[0]; !(c >= 'a' && c <= 'z') || swiftNoRef[name] || swiftModifiers[name] || called[m[0]] {
			continue
		}
		if m[0] > 0 && swiftIdentByte(code[m[0]-1]) {
			continue
		}
		recv := ""
		if b := s.prevNonSpace(m[0], false); b >= 0 {
			switch code[b] {
			case '@', '#', '\\', '$':
				continue
			case '.':
				// self.name only.
				if q := s.prevNonSpace(b, false); q < 0 || s.prevWord(b) != "self" ||
					(q-4 >= 0 && (swiftIdentByte(code[q-4]) || code[q-4] == '.')) {
					continue
				}
				recv = "self"
			}
		}
		if swiftDeclBefore[s.prevWord(m[0])] && recv == "" {
			continue
		}
		k := m[1]
		for k < len(code) && (code[k] == ' ' || code[k] == '\t') {
			k++
		}
		if k < len(code) && (code[k] == '(' || (code[k] == ':' && !(k+1 < len(code) && code[k+1] == ':'))) {
			continue
		}
		d := s.owner(m[0])
		if d >= 0 {
			sp := s.spans[d]
			if sp.body >= 0 && m[0] < sp.body || sp.body < 0 && sp.start >= 0 {
				continue // in the header: parameter names and types
			}
			if s.f.Defs[d].IsClass {
				continue // a type's stored properties' initializers: not worth it
			}
			if recv == "" && localsOf(d)[name] {
				continue
			}
		}
		if seen[d] == nil {
			seen[d] = map[string]bool{}
		}
		if seen[d][recv+"."+name] {
			continue
		}
		seen[d][recv+"."+name] = true
		add(m[0], hCall{Recv: recv, Name: name, Line: s.lineOf(m[0]), Ref: true})
	}
}

// bodies sets each definition's body hash: its own lines (comments
// blanked, strings kept), without its nested definitions'.
func (s *swiftScanner) bodies(norm []string) {
	for i := range s.f.Defs {
		d := &s.f.Defs[i]
		sp := s.spans[i]
		if sp.start < 0 { // the top level: the lines its calls are on
			var own []int
			for _, c := range d.Calls {
				own = append(own, c.Line-1)
			}
			d.Body = hLinesBody(norm, own, nil)
			continue
		}
		nested := map[int]bool{}
		for j := i + 1; j < len(s.spans); j++ {
			if o := s.spans[j]; o.start > sp.start && o.end <= sp.end && o.start >= 0 {
				for k := s.f.Defs[j].Line; k <= s.f.Defs[j].End; k++ {
					nested[k-1] = true
				}
			}
		}
		var own []int
		for k := d.Line - 1; k <= d.End-1; k++ {
			if !nested[k] {
				own = append(own, k)
			}
		}
		d.Body = hLinesBody(norm, own, nil)
	}
}

// --- Resolution ---

// swiftType is a type in a module: one it declares, or one it extends.
type swiftType struct{ mod, path string }

// swiftBase is a base as written, with the file it was written in.
type swiftBase struct{ file, name string }

// swiftIndex is one version's module-level view of the definitions.
type swiftIndex struct {
	r       *hResolver
	mods    map[string]bool
	members map[swiftType]map[string][]hDefRef // a type's members (in that module's files)
	decls   map[swiftType]hDefRef              // type declarations
	known   map[swiftType]bool                 // declared or extended
	bases   map[swiftType][]swiftBase
	top     map[string]map[string][]hDefRef // module → top-level name → definitions
}

func (l *swiftCalls) index(r *hResolver) *swiftIndex {
	if l.ix != nil && l.ix.r == r {
		return l.ix
	}
	ix := &swiftIndex{r: r, mods: map[string]bool{}, members: map[swiftType]map[string][]hDefRef{},
		decls: map[swiftType]hDefRef{}, known: map[swiftType]bool{}, bases: map[swiftType][]swiftBase{},
		top: map[string]map[string][]hDefRef{}}
	for _, mod := range l.mods {
		ix.mods[mod] = true
	}
	ix.mods[l.app] = true
	for _, p := range r.paths {
		mod := l.moduleOf(p)
		f := r.files[p]
		for i := range f.Defs {
			d := &f.Defs[i]
			ref := hDefRef{p, d}
			if d.Owner == "" {
				if ix.top[mod] == nil {
					ix.top[mod] = map[string][]hDefRef{}
				}
				ix.top[mod][d.Name] = append(ix.top[mod][d.Name], ref)
			} else {
				t := swiftType{mod, d.Owner}
				if ix.members[t] == nil {
					ix.members[t] = map[string][]hDefRef{}
				}
				ix.members[t][d.Name] = append(ix.members[t][d.Name], ref)
			}
			if d.IsClass {
				t := swiftType{mod, qualify(d.Owner, d.Name)}
				if _, dup := ix.decls[t]; !dup {
					ix.decls[t] = ref
				}
				ix.known[t] = true
			}
		}
		for _, c := range f.Classes {
			t := swiftType{mod, c.Name}
			ix.known[t] = true
			for _, b := range c.Bases {
				ix.bases[t] = append(ix.bases[t], swiftBase{p, b})
			}
		}
	}
	l.ix = ix
	return ix
}

// homes are the modules whose members t has: its own, and, for a type the
// module only extends, the modules that declare it.
func (ix *swiftIndex) homes(t swiftType) []swiftType {
	out := []swiftType{t}
	if _, ok := ix.decls[t]; ok {
		return out
	}
	for mod := range ix.mods {
		o := swiftType{mod, t.path}
		if mod != t.mod && ix.known[o] {
			out = append(out, o)
		}
	}
	sort.Slice(out[1:], func(i, j int) bool { return out[1+i].mod < out[1+j].mod })
	return out
}

// imports reports whether file p can see module mod.
func (l *swiftCalls) imports(ix *swiftIndex, p, mod string) bool {
	for _, m := range l.imported(ix, p) {
		if m == mod {
			return true
		}
	}
	return false
}

// imported lists the in-repo modules file p imports, the file's own first.
func (l *swiftCalls) imported(ix *swiftIndex, p string) []string {
	out := []string{l.moduleOf(p)}
	for _, imp := range ix.r.files[p].Imports {
		if ix.mods[imp.Spec] && imp.Spec != out[0] {
			out = append(out, imp.Spec)
		}
	}
	return out
}

// typeAt resolves a type name as written in file p inside scope (nil at
// the top level): nested types of the enclosing types first, then the
// module's and the imported modules' top level. A leading module name
// (Analytics.Tracker) is allowed.
func (l *swiftCalls) typeAt(ix *swiftIndex, p string, scope *hDef, name string) (swiftType, bool) {
	parts := strings.Split(name, ".")
	mod := l.moduleOf(p)
	var t swiftType
	found := false
	scopes := []string{""}
	if scope != nil {
		scopes = enclosingScopes(scope)
	}
	for _, sc := range scopes {
		if c := (swiftType{mod, qualify(sc, parts[0])}); ix.known[c] {
			t, found = c, true
			break
		}
	}
	if !found {
		mods := l.imported(ix, p)
		for _, m := range mods[1:] {
			if c := (swiftType{m, parts[0]}); ix.known[c] {
				t, found = c, true
				break
			}
		}
		if !found && len(parts) > 1 {
			for _, m := range mods {
				if m == parts[0] && ix.known[swiftType{m, parts[1]}] {
					t, found = swiftType{m, parts[1]}, true
					parts = parts[1:]
					break
				}
			}
		}
	}
	if !found {
		return swiftType{}, false
	}
	for _, part := range parts[1:] {
		t = swiftType{t.mod, qualify(t.path, part)}
		if !ix.known[t] {
			return swiftType{}, false
		}
	}
	return t, true
}

// swiftWant says which definitions member must be: any, functions and
// properties (a value), or types.
type swiftWant int

const (
	swiftAny swiftWant = iota
	swiftValue
)

func (w swiftWant) ok(d *hDef) bool { return w == swiftAny || !d.IsClass }

// member finds name among t's own members (all its modules' extensions).
func (ix *swiftIndex) member(t swiftType, name string, w swiftWant) (hDefRef, bool) {
	for _, h := range ix.homes(t) {
		for _, ref := range ix.members[h][name] {
			if w.ok(ref.def) {
				return ref, true
			}
		}
	}
	return hDefRef{}, false
}

// method finds name in t or up its superclasses and protocols (protocol
// extensions give default implementations).
func (l *swiftCalls) method(ix *swiftIndex, t swiftType, name string, w swiftWant, self bool) (hDefRef, bool) {
	seen := map[swiftType]bool{}
	queue := []swiftType{t}
	if !self {
		queue = l.basesOf(ix, t)
	}
	for len(queue) > 0 && len(seen) < 16 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		if ref, ok := ix.member(c, name, w); ok {
			return ref, true
		}
		queue = append(queue, l.basesOf(ix, c)...)
	}
	return hDefRef{}, false
}

// basesOf resolves t's bases (from every module that declares or extends
// it) to in-repo types.
func (l *swiftCalls) basesOf(ix *swiftIndex, t swiftType) []swiftType {
	var out []swiftType
	parent, last := "", t.path
	if i := strings.LastIndex(t.path, "."); i >= 0 {
		parent, last = t.path[:i], t.path[i+1:]
	}
	scope := &hDef{Owner: parent, Name: last}
	for _, h := range ix.homes(t) {
		for _, b := range ix.bases[h] {
			if bt, ok := l.typeAt(ix, b.file, scope, b.name); ok && bt != t {
				out = append(out, bt)
			}
		}
	}
	return out
}

// conforms reports whether t has p among its bases, at any depth.
func (l *swiftCalls) conforms(ix *swiftIndex, t, p swiftType) bool {
	seen := map[swiftType]bool{}
	queue := l.basesOf(ix, t)
	for len(queue) > 0 && len(seen) < 16 {
		c := queue[0]
		queue = queue[1:]
		if c == p {
			return true
		}
		if !seen[c] {
			seen[c] = true
			queue = append(queue, l.basesOf(ix, c)...)
		}
	}
	return false
}

// construct is what Type( runs: its init (or an inherited one), else the
// type itself.
func (l *swiftCalls) construct(ix *swiftIndex, t swiftType) string {
	if ref, ok := l.method(ix, t, "init", swiftValue, true); ok {
		return l.id(ref.path, ref.def)
	}
	for _, h := range ix.homes(t) {
		if ref, ok := ix.decls[h]; ok {
			return l.id(ref.path, ref.def)
		}
	}
	return ""
}

// ownerType is the type a member belongs to, and whether it's a protocol.
func (ix *swiftIndex) ownerType(l *swiftCalls, ref hDefRef) (swiftType, bool) {
	t := swiftType{l.moduleOf(ref.path), ref.def.Owner}
	for _, h := range ix.homes(t) {
		if d, ok := ix.decls[h]; ok {
			return h, d.def.Sig == "protocol"
		}
	}
	return t, false
}

// uniqueMethod is the one method named name in the repo. Several are still
// one when all but one are implementations of the remaining one, a
// protocol's requirement: the call is then taken to go through the
// protocol.
func (l *swiftCalls) uniqueMethod(ix *swiftIndex, name string) (string, bool) {
	ids := map[string]hDefRef{}
	for _, ref := range ix.r.byName[name] {
		if ref.def.Class != "" && !ref.def.IsClass && ref.def.Owner == ref.def.Class {
			ids[l.id(ref.path, ref.def)] = ref
		}
	}
	if len(ids) == 1 {
		for id := range ids {
			return id, true
		}
	}
	var proto string
	var pt swiftType
	for id, ref := range ids {
		if t, isProto := ix.ownerType(l, ref); isProto {
			if proto != "" {
				return "", false
			}
			proto, pt = id, t
		}
	}
	if proto == "" {
		return "", false
	}
	for id, ref := range ids {
		if id == proto {
			continue
		}
		if t, _ := ix.ownerType(l, ref); !l.conforms(ix, t, pt) {
			return "", false
		}
	}
	return proto, true
}

// want is what an unresolved call names: the exact ID it would have, when
// something by that name is still in the repo (so it's this repo's code),
// else any removed function of the name.
func swiftWantOf(r *hResolver, id, name string) []string {
	if r.byName[name] != nil {
		return []string{id}
	}
	return []string{"~" + name}
}

func (l *swiftCalls) resolve(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	ix := l.index(r)
	mod := l.moduleOf(p)
	w := swiftAny
	if c.Ref {
		w = swiftValue
	}
	idOf := func(ref hDefRef) string { return l.id(ref.path, ref.def) }
	// target turns a found definition into the call's target: a type
	// called is constructed, a type used as a value isn't a reference.
	target := func(ref hDefRef) string {
		if !ref.def.IsClass {
			return idOf(ref)
		}
		if c.Ref {
			return ""
		}
		return l.construct(ix, swiftType{l.moduleOf(ref.path), qualify(ref.def.Owner, ref.def.Name)})
	}
	switch {
	case c.Recv == ".":
		return "", nil // an implicit member: its type isn't known here
	case c.Recv == "self":
		if d.Class == "" {
			return "", nil
		}
		t := swiftType{mod, d.Class}
		if c.Name == "init" {
			if ref, ok := l.method(ix, t, "init", swiftValue, true); ok {
				return idOf(ref), nil
			}
			return "", nil
		}
		if ref, ok := l.method(ix, t, c.Name, w, true); ok {
			return target(ref), nil
		}
		if c.Ref {
			return "", nil
		}
		return "", swiftWantOf(r, mod+"."+qualify(d.Class, c.Name), c.Name)
	case c.Recv == "super":
		if d.Class == "" {
			return "", nil
		}
		if ref, ok := l.method(ix, swiftType{mod, d.Class}, c.Name, w, false); ok {
			return idOf(ref), nil
		}
		return "", nil
	case c.Recv == "":
		// Nested functions, then each enclosing type (with its
		// extensions and bases), then the module and its imports.
		for _, sc := range enclosingScopes(d) {
			if sc == "" {
				break
			}
			if t := (swiftType{mod, sc}); ix.known[t] {
				if ref, ok := l.method(ix, t, c.Name, w, true); ok {
					return target(ref), nil
				}
			} else if def := r.def(p, sc, c.Name); def != nil && w.ok(def) {
				return target(hDefRef{p, def}), nil
			}
		}
		for _, m := range l.imported(ix, p) {
			for _, ref := range ix.top[m][c.Name] {
				if ref.def.Name != swiftTop && w.ok(ref.def) {
					return target(ref), nil
				}
			}
		}
		if c.Ref {
			return "", nil
		}
		if d.Class != "" {
			return "", swiftWantOf(r, mod+"."+qualify(d.Class, c.Name), c.Name)
		}
		return "", swiftWantOf(r, mod+"."+c.Name, c.Name)
	case !strings.HasPrefix(c.Recv, "?"):
		// Type.m(), Module.fn(), Module.Type.m()
		if ix.mods[c.Recv] && l.imports(ix, p, c.Recv) {
			for _, ref := range ix.top[c.Recv][c.Name] {
				if w.ok(ref.def) {
					return target(ref), nil
				}
			}
			return "", swiftWantOf(r, c.Recv+"."+c.Name, c.Name)
		}
		if t, ok := l.typeAt(ix, p, d, c.Recv); ok {
			if c.Name == "init" {
				if id := l.construct(ix, t); id != "" {
					return id, nil
				}
				return "", nil
			}
			if ref, ok := l.method(ix, t, c.Name, w, true); ok {
				return target(ref), nil
			}
			if c.Ref {
				return "", nil
			}
			return "", swiftWantOf(r, t.mod+"."+qualify(t.path, c.Name), c.Name)
		}
	}
	// obj.m(): only a method name no other type in the repo uses.
	if c.Ref || c.Name == "init" {
		return "", nil
	}
	if id, ok := l.uniqueMethod(ix, c.Name); ok {
		return id, nil
	}
	return "", r.anyMethodWant(c.Name)
}

// swiftCallLang is hCalls plus two Swift steps on its result, which
// hCalls doesn't know about:
//   - a reference to a computed property is a read, so a call (static);
//   - a protocol's members get impl edges to the conforming types'
//     implementations (declared in the type or any extension, conformance
//     declared on the type or an extension), so callers of the protocol
//     reach them, tests included.
type swiftCallLang struct {
	hCalls
	s *swiftCalls
}

func newSwiftCallLang() *swiftCallLang {
	s := &swiftCalls{}
	return &swiftCallLang{hCalls: hCalls{l: s}, s: s}
}

func (w *swiftCallLang) funcs(idx *index, files []string, full bool) (*callSet, error) {
	set, err := w.hCalls.funcs(idx, files, full)
	if err != nil || len(set.funcs) == 0 {
		return set, err
	}
	l := w.s
	ix := l.ix
	if ix == nil || ix.r.idx != idx {
		ix = l.index(newHResolver(l, idx))
	}
	props := map[string]bool{}
	for _, p := range ix.r.paths {
		for i := range ix.r.files[p].Defs {
			if d := &ix.r.files[p].Defs[i]; !d.IsClass && d.Sig == "" && d.Name != swiftTop {
				props[l.id(p, d)] = true
			}
		}
	}
	for _, f := range set.funcs {
		for i := range f.calls {
			if f.calls[i].kind == CallRef && props[f.calls[i].to] {
				f.calls[i].kind = CallStatic
			}
		}
	}
	// Protocols, and the types that conform to each.
	var protos, types []swiftType
	for t, ref := range ix.decls {
		if ref.def.Sig == "protocol" {
			protos = append(protos, t)
		}
	}
	for t := range ix.known {
		if ix.bases[t] != nil {
			types = append(types, t)
		}
	}
	less := func(s []swiftType) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].mod != s[j].mod {
				return s[i].mod < s[j].mod
			}
			return s[i].path < s[j].path
		})
	}
	less(protos)
	less(types)
	for _, p := range protos {
		var reqs []string
		for name := range ix.members[p] {
			reqs = append(reqs, name)
		}
		sort.Strings(reqs)
		for _, t := range types {
			if d, ok := ix.decls[t]; (ok && d.def.Sig == "protocol") || !l.conforms(ix, t, p) {
				continue
			}
			for _, name := range reqs {
				req, ok := ix.member(p, name, swiftValue)
				if !ok {
					continue
				}
				rf := set.funcs[l.id(req.path, req.def)]
				impl, ok := ix.member(t, name, swiftValue)
				if rf == nil || !ok {
					continue
				}
				if to := l.id(impl.path, impl.def); to != rf.ID {
					rf.calls = append(rf.calls, callSite{to: to, kind: CallImpl, line: rf.Line})
				}
			}
		}
	}
	return set, nil
}
