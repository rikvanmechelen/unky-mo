package review

import (
	"regexp"
	"sort"
	"strings"
)

// jvmCalls is the Kotlin and Java call analyzer: definitions from brace
// depth (classes, objects, companion objects, Kotlin funs including
// extension and expression-bodied ones, Java methods and constructors),
// calls resolved through the enclosing class and its supertypes, extension
// functions on a known receiver type, the file's imports (aliases and
// wildcards), and the file's package, which both languages see without
// imports. A receiver's type is known when it's a class name or a
// parameter, property or local declared with a type (or a constructor
// call) in an enclosing definition. Otherwise a call on an unknown receiver
// resolves only when exactly one method in the repo has its name.
//
// IDs are package-qualified: com.acme.Repo.load, com.acme.format,
// com.acme.Exporter.<init>. Overloads share one ID (and one node), and an
// extension function is its package's function (com.acme.label), as the
// JVM sees it.
type jvmCalls struct {
	kl       *ktLang
	pkg      map[string]string   // file → its package
	pkgFiles map[string][]string // package → its files

	// Built per resolver (one per version), on its first resolve.
	r   *hResolver
	top map[string]map[string][]hDefRef // package → top-level name → classes and functions (no extensions)
	ext map[string]map[string][]hDefRef // receiver's simple name → name → top-level extension functions
}

func (l *jvmCalls) name() string         { return "kotlin" }
func (l *jvmCalls) owns(p string) bool   { return (&ktLang{}).owns(p) }
func (l *jvmCalls) unit(p string) string { return l.kl.unit(p) }

func (l *jvmCalls) setup(idx *index) bool {
	l.kl = &ktLang{}
	if !l.kl.detect(idx) {
		return false
	}
	l.pkg, l.pkgFiles, l.r = map[string]string{}, map[string][]string{}, nil
	idx.prefetchFor("ktpkg", l.owns)
	for _, p := range idx.paths {
		if !l.owns(p) {
			continue
		}
		pkg, _ := idx.symbols("ktpkg", p, func(src string) any { return jvmPackage(src) }).(string)
		l.pkg[p] = pkg
		l.pkgFiles[pkg] = append(l.pkgFiles[pkg], p)
	}
	return true
}

func (l *jvmCalls) id(p string, d *hDef) string {
	return qualify(l.pkg[p], qualify(d.Owner, d.Name))
}

func (l *jvmCalls) display(_ string, d *hDef) string {
	if recv := jvmExtRecv(d); recv != "" {
		return lastSegment(recv) + "." + d.Name
	}
	parts := strings.Split(qualify(d.Owner, d.Name), ".")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, ".")
}

// jvmExtRecv is an extension function's receiver type as written ("" for
// anything else). The scanner puts it in Class, where this resolves, so an
// extension is a def whose Class isn't one it's nested in.
func jvmExtRecv(d *hDef) string {
	if d.Class == "" || d.IsClass || d.Owner == d.Class || strings.HasPrefix(d.Owner, d.Class+".") {
		return ""
	}
	return d.Class
}

func lastSegment(s string) string {
	return s[strings.LastIndex(s, ".")+1:]
}

// jvmTok is a token of blanked Kotlin/Java code.
type jvmTok struct {
	s     string
	line  int
	pos   int
	nl    bool // the first token on its line
	ident bool // an identifier or keyword (backticks stripped)
}

var jvmOps = []string{"===", "!==", "...", "::", "?.", "?:", "!!", "->", "==", "!=", "<=", ">=", "&&", "||",
	"+=", "-=", "*=", "/=", "%=", "..", "++", "--"}

// jvmTokens splits blanked code into tokens. Numbers are dropped.
func jvmTokens(code string) []jvmTok {
	var out []jvmTok
	line, last := 1, 0
	isStart := func(c byte) bool { return c == '_' || c == '$' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z') }
	isPart := func(c byte) bool { return isStart(c) || (c >= '0' && c <= '9') }
	n := len(code)
	for i := 0; i < n; {
		c := code[i]
		switch {
		case c == '\n':
			line++
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f':
			i++
			continue
		case c >= '0' && c <= '9':
			for i < n && (isPart(code[i]) || (code[i] == '.' && i+1 < n && code[i+1] >= '0' && code[i+1] <= '9')) {
				i++
			}
			continue
		}
		t := jvmTok{line: line, pos: i, nl: line != last}
		switch {
		case isStart(c):
			j := i
			for j < n && isPart(code[j]) {
				j++
			}
			t.s, t.ident, i = code[i:j], true, j
		case c == '`':
			end := strings.IndexAny(code[i+1:], "`\n")
			if end < 0 || code[i+1+end] != '`' {
				t.s, i = "`", i+1
				break
			}
			t.s, t.ident, i = code[i+1:i+1+end], true, i+2+end
		default:
			t.s = code[i : i+1]
			for _, op := range jvmOps {
				if strings.HasPrefix(code[i:], op) {
					t.s = op
					break
				}
			}
			i += len(t.s)
		}
		out = append(out, t)
		last = line
	}
	return out
}

// jvmFrame is an open brace (or expression body) while scanning.
type jvmFrame struct {
	def    int    // the definition calls in it belong to, -1 for none
	own    bool   // the frame is that definition's body: closing it ends it
	path   string // owner path of what's declared in it
	class  string // where this resolves
	body   bool   // a class body: declarations, not statements
	static bool   // its functions are static (an object, a companion object)
	end    int    // last token of an expression body, -1 for a brace
}

type jvmScanner struct {
	java  bool
	code  string
	toks  []jvmTok
	f     hFile
	stack []jvmFrame
	// binds: owner path → variable → its type as written, for the
	// receivers of calls in that definition and the ones nested in it.
	binds map[string]map[string]string
	sigs  map[int]string // def → its parameter text
}

var (
	jvmImportLnRe = regexp.MustCompile(`(?m)^[ \t]*import[ \t]+(?:static[ \t]+)?([\w.$]+?)(\.\*)?(?:[ \t]+as[ \t]+(\w+))?[ \t]*;?[ \t]*$`)

	// jvmKeywords are never called (bare) or used as a trailing lambda.
	jvmKeywords = map[string]bool{
		"if": true, "else": true, "when": true, "while": true, "for": true, "do": true, "try": true, "catch": true,
		"finally": true, "return": true, "throw": true, "is": true, "as": true, "in": true, "fun": true, "class": true,
		"object": true, "interface": true, "val": true, "var": true, "this": true, "super": true, "null": true,
		"true": true, "false": true, "typealias": true, "package": true, "import": true, "init": true,
		"constructor": true, "companion": true, "where": true, "by": true, "break": true, "continue": true,
		"switch": true, "case": true, "default": true, "new": true, "synchronized": true, "assert": true,
		"instanceof": true, "yield": true, "void": true, "static": true, "enum": true,
	}
	// jvmExprWords can come before a call in Java; any other word before
	// name( makes it a declaration (void run().
	jvmExprWords = map[string]bool{"return": true, "throw": true, "else": true, "case": true, "yield": true,
		"assert": true, "do": true, "new": true, "default": true}
	// jvmCommonMethods are too common to pin on the repo's one method of
	// the name when the receiver is unknown (Object's and the standard
	// library's).
	jvmCommonMethods = map[string]bool{"toString": true, "equals": true, "hashCode": true, "invoke": true,
		"copy": true, "compareTo": true, "iterator": true, "next": true, "hasNext": true, "close": true,
		"let": true, "apply": true, "also": true, "run": true, "with": true, "use": true, "get": true, "set": true,
		"map": true, "filter": true, "forEach": true, "first": true, "last": true, "add": true, "remove": true,
		"contains": true, "isEmpty": true, "collect": true, "emit": true, "launch": true}
	// jvmContinuesAfter / jvmContinuesWith: a line ending with (or the next
	// starting with) one of these continues an expression body.
	jvmContinuesAfter = map[string]bool{"=": true, ".": true, "?.": true, ",": true, "->": true, "?:": true,
		"&&": true, "||": true, "+": true, "-": true, "*": true, "/": true, "%": true, "==": true, "!=": true,
		"===": true, "!==": true, "<": true, ">": true, "<=": true, ">=": true, "..": true, "::": true, "is": true,
		"as": true, "in": true, "else": true, "!": true}
	jvmContinuesWith = map[string]bool{".": true, "?.": true, "?:": true, "&&": true, "||": true, "else": true,
		"as": true, "->": true, "::": true}
)

// scanFile reads a Kotlin or Java file's definitions and the calls in
// them. Calls outside any definition (top-level property initializers)
// are left out.
func (l *jvmCalls) scanFile(p, src string) hFile {
	code := jvmCode(src)
	s := &jvmScanner{java: strings.HasSuffix(p, ".java"), code: code, toks: jvmTokens(code), f: hFile{OK: true},
		binds: map[string]map[string]string{}, sigs: map[int]string{}}
	s.run()
	for _, m := range jvmImportLnRe.FindAllStringSubmatchIndex(code, -1) {
		chain := code[m[2]:m[3]]
		line := 1 + strings.Count(code[:m[0]], "\n")
		if m[4] >= 0 {
			s.f.Imports = append(s.f.Imports, hImport{Local: "*" + chain, Spec: chain, Name: "*", Line: line})
			continue
		}
		i := strings.LastIndex(chain, ".")
		if i < 0 {
			continue
		}
		imp := hImport{Local: chain[i+1:], Spec: chain[:i], Name: chain[i+1:], Line: line}
		if m[6] >= 0 {
			imp.Local = code[m[6]:m[7]]
		}
		s.f.Imports = append(s.f.Imports, imp)
	}
	s.finish(strings.Split(jvmScan(src, true), "\n"))
	return s.f
}

func (s *jvmScanner) tok(i int) jvmTok {
	if i < 0 || i >= len(s.toks) {
		return jvmTok{}
	}
	return s.toks[i]
}

func (s *jvmScanner) top() jvmFrame { return s.stack[len(s.stack)-1] }

func (s *jvmScanner) push(f jvmFrame) { s.stack = append(s.stack, f) }

// pop closes the innermost frame at line.
func (s *jvmScanner) pop(line int) {
	fr := s.top()
	s.stack = s.stack[:len(s.stack)-1]
	if fr.own {
		s.f.Defs[fr.def].End = line
	}
}

// popExprs closes the expression bodies that ended before token i.
func (s *jvmScanner) popExprs(i int) {
	for len(s.stack) > 1 && s.top().end >= 0 && s.top().end < i {
		s.pop(s.toks[s.top().end].line)
	}
}

func (s *jvmScanner) run() {
	s.stack = []jvmFrame{{def: -1, end: -1}}
	for i := 0; i < len(s.toks); i++ {
		s.popExprs(i)
		t := s.toks[i]
		top := s.top()
		switch {
		case t.s == "{":
			s.push(jvmFrame{def: top.def, path: top.path, class: top.class, end: -1})
		case t.s == "}":
			if len(s.stack) == 1 || top.end >= 0 {
				s.f.OK = false
				return
			}
			s.pop(t.line)
		case t.s == "::":
			s.methodRef(i)
		case !t.ident:
		case (t.s == "package" || t.s == "import") && t.nl:
			for i+1 < len(s.toks) && !s.toks[i+1].nl {
				i++
			}
		case s.isClassDecl(i):
			i = s.classDecl(i)
		case !s.java && t.s == "fun":
			i = s.funDecl(i)
		case !s.java && t.s == "constructor" && top.body && s.tok(i+1).s == "(":
			i = s.funcAt(i, i+1, "<init>", "")
		case s.java && top.body && s.isJavaMethod(i):
			i = s.javaMethod(i)
		default:
			s.word(i)
		}
		if !s.f.OK {
			return
		}
	}
	s.popExprs(len(s.toks))
	if len(s.stack) != 1 {
		s.f.OK = false
	}
}

// after is the index after the bracket closing the one at i. An unclosed
// one means the file doesn't parse.
func (s *jvmScanner) after(i int) int {
	k := s.match(i)
	if k < 0 {
		s.f.OK = false
		return len(s.toks)
	}
	return k + 1
}

// match finds the bracket closing the one at i, -1 if none.
func (s *jvmScanner) match(i int) int {
	depth := 0
	for j := i; j < len(s.toks); j++ {
		switch s.toks[j].s {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// skipAngle skips type parameters or arguments (<…>) starting at i,
// returning i unchanged when there are none or they don't close.
func (s *jvmScanner) skipAngle(i int) int {
	if s.tok(i).s != "<" {
		return i
	}
	depth := 0
	for j := i; j < len(s.toks); j++ {
		switch s.toks[j].s {
		case "<":
			depth++
		case ">":
			depth--
			if depth == 0 {
				return j + 1
			}
		case "{", "}", ";", "=", "==", "&&", "||":
			return i
		}
	}
	return i
}

// chain reads a dotted name (a type: annotations before it skipped) at i,
// returning it and the index after it.
func (s *jvmScanner) chain(i int) (string, int) {
	for s.tok(i).s == "@" {
		i += 2
		if s.tok(i).s == "(" && !s.tok(i).nl {
			i = s.after(i)
		}
	}
	var parts []string
	for s.tok(i).ident {
		parts = append(parts, s.tok(i).s)
		if s.tok(i+1).s != "." || !s.tok(i+2).ident {
			i++
			break
		}
		i += 2
	}
	return strings.Join(parts, "."), i
}

// params is the text of a parameter list (open and close are its parens),
// whitespace removed.
func (s *jvmScanner) params(open, close int) string {
	return strings.Join(strings.Fields(s.code[s.toks[open].pos+1:s.toks[close].pos]), "")
}

func (s *jvmScanner) bind(path, name, typ string) {
	if typ == "" || name == "" {
		return
	}
	if s.binds[path] == nil {
		s.binds[path] = map[string]string{}
	}
	s.binds[path][name] = typ
}

// bindParams records a parameter list's typed names for path: Kotlin's
// "name: Type", Java's "Type name".
func (s *jvmScanner) bindParams(open, close int, path string) {
	depth := 0
	for j := open; j < close; j++ {
		switch s.toks[j].s {
		case "(", "[", "{", "<":
			depth++
			continue
		case ")", "]", "}", ">":
			depth--
			continue
		}
		if depth != 1 || !s.toks[j].ident {
			continue
		}
		if !s.java && s.tok(j+1).s == ":" {
			typ, _ := s.chain(j + 2)
			s.bind(path, s.toks[j].s, typ)
		}
		if nx := s.tok(j + 1).s; s.java && (nx == "," || nx == ")") && s.tok(j-1).ident {
			s.bind(path, s.toks[j].s, s.tok(j-1).s)
		}
	}
}

// isClassDecl reports whether token i starts a class-like declaration.
func (s *jvmScanner) isClassDecl(i int) bool {
	prev, next := s.tok(i-1).s, s.tok(i+1)
	switch s.toks[i].s {
	case "class", "interface":
		return prev != "::" && prev != "." && next.ident
	case "object":
		return !s.java && (next.ident || prev == "companion")
	case "enum":
		return s.java && next.ident
	case "record":
		return s.java && next.ident && s.tok(i+2).s == "(" && s.top().body
	}
	return false
}

// classDecl reads a class, interface, object, enum or record header at i
// and opens its body. It returns the index the scan goes on after.
func (s *jvmScanner) classDecl(i int) int {
	top := s.top()
	kw := s.toks[i].s
	companion := kw == "object" && s.tok(i-1).s == "companion"
	j := i + 1
	name := ""
	if s.tok(j).ident {
		name = s.tok(j).s
		j++
	}
	if name == "" && !companion {
		return i
	}
	path := qualify(top.path, name)
	j = s.skipAngle(j)
	var bases []string
	sig := ""
	if s.java {
		if s.tok(j).s == "(" { // a record's components
			k := s.match(j)
			if k < 0 {
				s.f.OK = false
				return len(s.toks)
			}
			sig = s.params(j, k)
			j = k + 1
		}
		for j < len(s.toks) && s.toks[j].s != "{" && s.toks[j].s != ";" {
			if s.toks[j].s != "extends" && s.toks[j].s != "implements" {
				j++
				continue
			}
			j++
			for {
				b, nj := s.chain(j)
				if b != "" {
					bases = append(bases, b)
				}
				j = s.skipAngle(nj)
				if s.tok(j).s != "," {
					break
				}
				j++
			}
		}
	} else {
		// Annotations and modifiers before a primary constructor.
	mods:
		for j < len(s.toks) && !s.toks[j].nl {
			switch t := s.toks[j].s; {
			case t == "@":
				j += 2
				if s.tok(j).s == "(" && !s.tok(j).nl {
					j = s.after(j)
				}
			case t == "private" || t == "public" || t == "protected" || t == "internal" || t == "constructor":
				j++
			default:
				break mods
			}
		}
		if s.tok(j).s == "(" && !s.tok(j).nl {
			k := s.match(j)
			if k < 0 {
				s.f.OK = false
				return len(s.toks)
			}
			sig = s.params(j, k)
			s.bindParams(j, k, path)
			j = k + 1
		}
		if s.tok(j).s == ":" {
			j++
			for {
				b, nj := s.chain(j)
				if b != "" {
					bases = append(bases, b)
				}
				j = s.skipAngle(nj)
				if s.tok(j).s == "(" {
					j = s.after(j)
				}
				if s.tok(j).s == "by" { // delegation: skip the delegate
					for j++; j < len(s.toks) && s.toks[j].s != "," && s.toks[j].s != "{" && !s.toks[j].nl; {
						if s.toks[j].s == "(" {
							j = s.after(j)
						} else {
							j++
						}
					}
				}
				if s.tok(j).s != "," {
					break
				}
				j++
			}
		}
		if s.tok(j).s == "where" {
			for j++; j < len(s.toks) && s.toks[j].s != "{" && !s.toks[j].nl; j++ {
			}
		}
	}
	if companion {
		// A companion's members belong to the class, as static ones.
		if s.tok(j).s == "{" {
			s.push(jvmFrame{def: top.def, path: top.path, class: top.class, body: true, static: true, end: -1})
			return j
		}
		return j - 1
	}
	s.f.Defs = append(s.f.Defs, hDef{Name: name, Owner: top.path, Class: top.class, IsClass: true,
		Line: s.toks[i].line, End: s.toks[i].line})
	idx := len(s.f.Defs) - 1
	s.sigs[idx] = sig
	s.f.Classes = append(s.f.Classes, hClass{Name: path, Bases: bases})
	if s.tok(j).s == "{" {
		s.push(jvmFrame{def: idx, own: true, path: path, class: path, body: true, static: kw == "object", end: -1})
		return j
	}
	s.f.Defs[idx].End = s.tok(j - 1).line
	return j - 1
}

// funDecl reads a Kotlin fun at i: fun [<T>] [Recv.]name(…).
func (s *jvmScanner) funDecl(i int) int {
	j := s.skipAngle(i + 1)
	var chain []string
	for s.tok(j).ident {
		chain = append(chain, s.tok(j).s)
		j = s.skipAngle(j + 1)
		if s.tok(j).s == "?" {
			j++
		}
		if s.tok(j).s != "." {
			break
		}
		j++
	}
	if len(chain) == 0 || s.tok(j).s != "(" {
		return i // fun interface, or a function type: not read here
	}
	return s.funcAt(i, j, chain[len(chain)-1], strings.Join(chain[:len(chain)-1], "."))
}

// funcAt adds a Kotlin function (or secondary constructor) declared at
// token i whose parameters open at open, and opens its body: a block, an
// expression, or none (abstract). recv is an extension's receiver type.
func (s *jvmScanner) funcAt(i, open int, name, recv string) int {
	top := s.top()
	k := s.match(open)
	if k < 0 {
		s.f.OK = false
		return len(s.toks)
	}
	d := hDef{Name: name, Owner: top.path, Class: top.class, Static: top.static, Line: s.toks[i].line}
	sig := s.params(open, k)
	if recv != "" {
		d.Class, d.Static, sig = recv, false, recv+"."+sig
	}
	path := qualify(top.path, name)
	s.bindParams(open, k, path)
	m := k + 1
	if s.tok(m).s == ":" {
		if name == "<init>" { // : this(…) / super(…)
			m += 2
			if s.tok(m).s == "(" {
				m = s.after(m)
			}
		} else {
			m = s.typeEnd(m + 1)
		}
	}
	if s.tok(m).s == "where" {
		for m++; m < len(s.toks) && s.toks[m].s != "{" && s.toks[m].s != "=" && !s.toks[m].nl; m++ {
		}
	}
	return s.openBody(d, sig, m, path)
}

// openBody adds d and opens its body at token m ({ or =), or ends it at
// the token before m when it has none.
func (s *jvmScanner) openBody(d hDef, sig string, m int, path string) int {
	s.f.Defs = append(s.f.Defs, d)
	idx := len(s.f.Defs) - 1
	s.sigs[idx] = sig
	fr := jvmFrame{def: idx, own: true, path: path, class: d.Class, end: -1}
	switch s.tok(m).s {
	case "{":
		s.push(fr)
		return m
	case "=":
		if !s.java {
			fr.end = s.exprEnd(m)
			s.push(fr)
			return m
		}
	}
	s.f.Defs[idx].End = s.tok(m - 1).line
	return m - 1
}

// typeEnd skips a return type starting at j: up to a body, an expression
// body or the end of the line.
func (s *jvmScanner) typeEnd(j int) int {
	depth := 0
	for start := j; j < len(s.toks); j++ {
		t := s.toks[j]
		if depth == 0 && (t.s == "{" || t.s == "=" || t.s == "where" || (t.nl && j > start)) {
			return j
		}
		switch t.s {
		case "(", "<", "[":
			depth++
		case ")", ">", "]":
			depth--
		}
	}
	return j
}

// exprEnd finds the last token of an expression body starting after the
// "=" at eq: a ";", the brace closing the enclosing block, or a line
// break the expression doesn't continue over.
func (s *jvmScanner) exprEnd(eq int) int {
	depth := 0
	for j := eq + 1; j < len(s.toks); j++ {
		t := s.toks[j]
		if depth == 0 && j > eq+1 && t.nl && !jvmContinuesAfter[s.toks[j-1].s] && !jvmContinuesWith[t.s] {
			return j - 1
		}
		switch t.s {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return j - 1
			}
			depth--
		case ";":
			if depth == 0 {
				return j
			}
		}
	}
	return len(s.toks) - 1
}

// isJavaMethod reports whether the identifier at i, in a class body,
// declares a method or constructor: Type name(…) or Class(…), followed by
// a body, throws, ";" or an annotation default.
func (s *jvmScanner) isJavaMethod(i int) bool {
	name := s.toks[i].s
	if s.tok(i+1).s != "(" || jvmKeywords[name] {
		return false
	}
	prev := s.tok(i - 1)
	ctor := name == lastSegment(s.top().path)
	switch {
	case prev.ident && !jvmExprWords[prev.s] && !jvmKeywords[prev.s]:
	case prev.ident && (prev.s == "void" || prev.s == "static"):
	case prev.s == ">" || prev.s == "]":
	case ctor && (prev.s == "{" || prev.s == "}" || prev.s == ";" || prev.s == ")" || i == 0):
	default:
		return false
	}
	k := s.match(i + 1)
	switch s.tok(k + 1).s {
	case "{", ";", "throws", "default":
		return k > 0
	}
	return false
}

// javaMethod adds the Java method or constructor declared at i.
func (s *jvmScanner) javaMethod(i int) int {
	top := s.top()
	name := s.toks[i].s
	if name == lastSegment(top.path) {
		name = "<init>"
	}
	d := hDef{Name: name, Owner: top.path, Class: top.class, Line: s.toks[i].line}
	for b := i - 1; b >= 0 && s.toks[b].s != ";" && s.toks[b].s != "{" && s.toks[b].s != "}"; b-- {
		if s.toks[b].s == "static" {
			d.Static = true
		}
	}
	k := s.match(i + 1)
	path := qualify(top.path, name)
	s.bindParams(i+1, k, path)
	m := k + 1
	for m < len(s.toks) && s.toks[m].s != "{" && s.toks[m].s != ";" {
		m++ // throws …, default …
	}
	return s.openBody(d, s.params(i+1, k), m, path)
}

// addCall records c in the innermost definition.
func (s *jvmScanner) addCall(c hCall) {
	if d := s.top().def; d >= 0 {
		s.f.Defs[d].Calls = append(s.f.Defs[d].Calls, c)
	}
}

// word handles an identifier in code: a typed declaration (for receiver
// types), a call (name(, name<T>(, new Type(, new Type<>(, Kotlin's
// trailing lambda name {), or nothing.
func (s *jvmScanner) word(i int) {
	t, prev, next := s.toks[i], s.tok(i-1), s.tok(i+1)
	if prev.s == "@" || prev.s == "::" {
		return // an annotation, or a method reference's name
	}
	path := s.top().path
	if !s.java && (t.s == "val" || t.s == "var") && next.ident {
		// val x: Type / val x = Type(…)
		switch s.tok(i + 2).s {
		case ":":
			typ, _ := s.chain(i + 3)
			s.bind(path, next.s, typ)
		case "=":
			if typ, j := s.chain(i + 3); s.tok(j).s == "(" && isUpper(lastSegment(typ)) {
				s.bind(path, next.s, typ)
			}
		}
		return
	}
	if s.java && prev.ident && isUpper(prev.s) && !jvmKeywords[t.s] {
		switch next.s {
		case "=", ";", ",", ")", ":":
			s.bind(path, t.s, prev.s) // Type name = …, for (Type name : …)
		}
	}
	call := next.s == "("
	if !call && (!s.java || prev.s == "new") {
		switch {
		case next.s == "<":
			if k := s.skipAngle(i + 1); k > i+1 && s.tok(k).s == "(" && s.typeArgs(i+1, k) {
				call = true
			}
		case next.s == "{" && prev.s != ":" && !jvmKeywords[t.s] && !s.java:
			call = true
		}
	}
	if !call {
		return
	}
	recv := s.receiver(i)
	if recv == "" {
		if jvmKeywords[t.s] && t.s != "new" {
			return
		}
		if !s.java && (t.s == "get" || t.s == "set") {
			return // a property accessor
		}
		if s.java && prev.ident && !jvmExprWords[prev.s] {
			return // a declaration: void run(
		}
	}
	if t.s == "this" || t.s == "super" || t.s == "new" {
		return
	}
	s.addCall(hCall{Recv: recv, Name: t.s, Line: t.line})
}

// typeArgs reports whether the tokens between from and to look like type
// arguments, not a comparison.
func (s *jvmScanner) typeArgs(from, to int) bool {
	for j := from; j < to; j++ {
		if t := s.toks[j]; !t.ident && !strings.Contains("<>.,?*:", t.s) {
			return false
		}
	}
	return true
}

func isUpper(s string) bool { return s != "" && s[0] >= 'A' && s[0] <= 'Z' }

// receiver reads the receiver of the name at i as written: "" for none, a
// dotted chain ("repo", "Foo.Companion", "this", "super"), or "?…" when it
// hangs off an expression. A leading "this." is dropped.
func (s *jvmScanner) receiver(i int) string {
	var parts []string
	j := i - 1
	for s.tok(j).s == "." || s.tok(j).s == "?." {
		if !s.tok(j - 1).ident {
			parts = append([]string{"?"}, parts...)
			break
		}
		parts = append([]string{s.tok(j - 1).s}, parts...)
		j -= 2
	}
	if len(parts) == 0 {
		return ""
	}
	if parts[0] != "?" {
		switch s.tok(j).s {
		case ")", "]", "!!", ".", "?.":
			parts = append([]string{"?"}, parts...)
		}
	}
	if len(parts) > 1 && parts[0] == "this" {
		parts = parts[1:]
	}
	return strings.Join(parts, ".")
}

// methodRef records a method reference at the "::" at i: ::name,
// Type::name, this::name, Type::new.
func (s *jvmScanner) methodRef(i int) {
	next := s.tok(i + 1)
	if !next.ident || next.s == "class" {
		return
	}
	name := next.s
	if name == "new" {
		name = "<init>"
	}
	recv := ""
	if prev := s.tok(i - 1); prev.ident {
		var parts []string
		j := i - 1
		for s.tok(j).ident {
			parts = append([]string{s.tok(j).s}, parts...)
			if s.tok(j-1).s != "." {
				break
			}
			j -= 2
		}
		recv = strings.Join(parts, ".")
	} else if prev.s == ")" || prev.s == "]" {
		recv = "?"
	}
	s.addCall(hCall{Recv: recv, Name: name, Line: next.line, Ref: true})
}

// finish types call receivers from the declarations, hashes bodies (each
// definition's own lines, without its nested ones') and signatures, and
// merges overloads into one definition.
func (s *jvmScanner) finish(lines []string) {
	f := &s.f
	owner := map[int]int{} // line → innermost definition
	for i, d := range f.Defs {
		for ln := d.Line; ln <= d.End; ln++ {
			owner[ln] = i
		}
	}
	own := map[int][]int{}
	for ln, i := range owner {
		own[i] = append(own[i], ln-1)
	}
	for i := range f.Defs {
		d := &f.Defs[i]
		sort.Ints(own[i])
		d.Body = hLinesBody(lines, own[i], nil)
		if sig := s.sigs[i]; sig != "" || !d.IsClass {
			d.Sig = sig // hashed after merging
		}
		scopes := enclosingScopes(d)
		for k, c := range d.Calls {
			if c.Recv == "" || c.Recv == "this" || c.Recv == "super" || strings.ContainsAny(c.Recv, ".?") {
				continue
			}
			for _, sc := range scopes {
				if typ := s.binds[sc][c.Recv]; typ != "" {
					d.Calls[k].Recv = typ
					break
				}
			}
		}
	}
	// Overloads: one definition, the first's range, every body and
	// signature.
	first := map[string]int{}
	var defs []hDef
	var bodies, sigs, reqs [][]string
	for _, d := range f.Defs {
		key := qualify(d.Owner, d.Name)
		if k, ok := first[key]; ok && defs[k].IsClass == d.IsClass {
			defs[k].Calls = append(defs[k].Calls, d.Calls...)
			bodies[k] = append(bodies[k], d.Body)
			sigs[k] = append(sigs[k], d.Sig)
			reqs[k] = append(reqs[k], requiredParams(d.Sig))
			continue
		}
		first[key] = len(defs)
		defs = append(defs, d)
		bodies = append(bodies, []string{d.Body})
		sigs = append(sigs, []string{d.Sig})
		reqs = append(reqs, []string{requiredParams(d.Sig)})
	}
	for k := range defs {
		if len(bodies[k]) > 1 {
			defs[k].Body = hashOf(strings.Join(bodies[k], "\n"))
		}
		if !defs[k].IsClass || defs[k].Sig != "" {
			defs[k].Sig = hashOf(strings.Join(sigs[k], "|"))
			defs[k].Req = hashOf(strings.Join(reqs[k], "|"))
		}
	}
	f.Defs = defs
}

// ensure builds the per-version indexes for r.
func (l *jvmCalls) ensure(r *hResolver) {
	if l.r == r {
		return
	}
	l.r = r
	l.top, l.ext = map[string]map[string][]hDefRef{}, map[string]map[string][]hDefRef{}
	add := func(m map[string]map[string][]hDefRef, k, name string, ref hDefRef) {
		if m[k] == nil {
			m[k] = map[string][]hDefRef{}
		}
		m[k][name] = append(m[k][name], ref)
	}
	for _, p := range r.paths {
		for i := range r.files[p].Defs {
			d := &r.files[p].Defs[i]
			if d.Owner != "" {
				continue
			}
			if recv := jvmExtRecv(d); recv != "" {
				add(l.ext, lastSegment(recv), d.Name, hDefRef{p, d})
			} else {
				add(l.top, l.pkg[p], d.Name, hDefRef{p, d})
			}
		}
	}
}

// topDef is a package's top-level class or function called name.
func (l *jvmCalls) topDef(pkg, name string) hDefRef {
	if refs := l.top[pkg][name]; len(refs) > 0 {
		return refs[0]
	}
	return hDefRef{}
}

// inRepo reports whether a dotted name is in a package the repo declares.
func (l *jvmCalls) inRepo(name string) bool {
	for name != "" {
		if _, ok := l.pkgFiles[name]; ok {
			return true
		}
		i := strings.LastIndex(name, ".")
		if i < 0 {
			return false
		}
		name = name[:i]
	}
	return false
}

// nested descends from class c through nested class names.
func (l *jvmCalls) nested(r *hResolver, c hClassRef, names []string) (hClassRef, bool) {
	for _, n := range names {
		d := r.def(c.path, c.owner, n)
		if d == nil || !d.IsClass {
			return hClassRef{}, false
		}
		c.owner = qualify(c.owner, n)
	}
	return c, true
}

// classByFQN finds a class by its qualified name (com.acme.Outer.Inner).
func (l *jvmCalls) classByFQN(r *hResolver, fqn string) (hClassRef, bool) {
	parts := strings.Split(fqn, ".")
	for k := len(parts) - 1; k >= 1; k-- {
		for _, ref := range l.top[strings.Join(parts[:k], ".")][parts[k]] {
			if ref.def.IsClass {
				if c, ok := l.nested(r, hClassRef{ref.path, ref.def.Name}, parts[k+1:]); ok {
					return c, true
				}
			}
		}
	}
	return hClassRef{}, false
}

// classByName resolves a type as written in file p, from scopes (the
// enclosing definitions): nested classes in scope, imports, the package,
// wildcard imports, then a qualified name.
func (l *jvmCalls) classByName(r *hResolver, p string, scopes []string, name string) (hClassRef, bool) {
	parts := strings.Split(name, ".")
	if c, ok := l.simpleClass(r, p, scopes, parts[0]); ok {
		if c, ok := l.nested(r, c, parts[1:]); ok {
			return c, true
		}
	}
	return l.classByFQN(r, name)
}

func (l *jvmCalls) simpleClass(r *hResolver, p string, scopes []string, n string) (hClassRef, bool) {
	for _, sc := range scopes {
		if d := r.def(p, sc, n); d != nil && d.IsClass {
			return hClassRef{p, qualify(sc, n)}, true
		}
	}
	if imp, ok := r.imports[p][n]; ok {
		return l.classByFQN(r, imp.Spec+"."+imp.Name)
	}
	if ref := l.topDef(l.pkg[p], n); ref.def != nil && ref.def.IsClass {
		return hClassRef{ref.path, ref.def.Name}, true
	}
	for _, imp := range r.files[p].Imports {
		if imp.Name == "*" {
			if c, ok := l.classByFQN(r, imp.Spec+"."+n); ok {
				return c, true
			}
		}
	}
	return hClassRef{}, false
}

// baseOf resolves a supertype as written in file p: like any type, or a
// class nested anywhere in the file.
func (l *jvmCalls) baseOf(r *hResolver) func(p, name string) (hClassRef, bool) {
	return func(p, name string) (hClassRef, bool) {
		if c, ok := l.classByName(r, p, []string{""}, name); ok {
			return c, true
		}
		for _, k := range sortedKeys(r.classes[p]) {
			if strings.HasSuffix("."+k, "."+name) {
				return hClassRef{p, k}, true
			}
		}
		return hClassRef{}, false
	}
}

// construct is what a call to class c runs: its constructor (Java's, or a
// Kotlin secondary one), else the class itself (a primary constructor and
// init blocks are the class's own lines).
func (l *jvmCalls) construct(r *hResolver, c hClassRef) string {
	if d := r.def(c.path, c.owner, "<init>"); d != nil {
		return r.l.id(c.path, d)
	}
	return qualify(l.pkg[c.path], c.owner)
}

// target is a found definition's ID for call c: a class called is
// constructed, a class referenced is itself.
func (l *jvmCalls) target(r *hResolver, ref hDefRef, c hCall) string {
	if ref.def.IsClass && !c.Ref {
		return l.construct(r, hClassRef{ref.path, qualify(ref.def.Owner, ref.def.Name)})
	}
	return r.l.id(ref.path, ref.def)
}

// extension finds an extension function name on receiver type typ that
// file p sees: in its package or imported.
func (l *jvmCalls) extension(r *hResolver, p, typ, name string) string {
	for _, ref := range l.ext[lastSegment(typ)][name] {
		pkg := l.pkg[ref.path]
		visible := pkg == l.pkg[p]
		for _, imp := range r.files[p].Imports {
			visible = visible || (imp.Spec == pkg && (imp.Name == name || imp.Name == "*"))
		}
		if visible {
			return r.l.id(ref.path, ref.def)
		}
	}
	return ""
}

// onType resolves c on a receiver of type typ: the class's methods (up
// its supertypes, companion members included), a nested class, or an
// extension function. ok is false when typ isn't a type the repo knows.
func (l *jvmCalls) onType(r *hResolver, p string, scopes []string, typ string, c hCall) (string, []string, bool) {
	cls, found := l.classByName(r, p, scopes, typ)
	if found {
		if c.Name == "<init>" {
			return l.construct(r, cls), nil, true
		}
		if d, dp := r.method(cls, c.Name, l.baseOf(r)); d != nil {
			return l.target(r, hDefRef{dp, d}, c), nil, true
		}
	}
	if id := l.extension(r, p, typ, c.Name); id != "" {
		return id, nil, true
	}
	if found {
		return "", []string{qualify(l.pkg[cls.path], qualify(cls.owner, c.Name))}, true
	}
	return "", nil, false
}

// bare resolves a call without a receiver: local functions and the
// enclosing classes' members (supertypes included), an extension's
// receiver, the file's top level, imports, the package, wildcard imports.
func (l *jvmCalls) bare(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	scopes := enclosingScopes(d)
	for _, sc := range scopes[:len(scopes)-1] {
		if _, isClass := r.classes[p][sc]; isClass {
			if def, dp := r.method(hClassRef{p, sc}, c.Name, l.baseOf(r)); def != nil {
				return l.target(r, hDefRef{dp, def}, c), nil
			}
			// An extension on the class, with this implicit.
			if id := l.extension(r, p, sc, c.Name); id != "" {
				return id, nil
			}
			continue
		}
		if def := r.def(p, sc, c.Name); def != nil {
			return l.target(r, hDefRef{p, def}, c), nil
		}
	}
	if recv := jvmExtRecv(d); recv != "" {
		if to, _, _ := l.onType(r, p, scopes, recv, c); to != "" {
			return to, nil
		}
	}
	if def := r.def(p, "", c.Name); def != nil && jvmExtRecv(def) == "" {
		return l.target(r, hDefRef{p, def}, c), nil
	}
	if imp, ok := r.imports[p][c.Name]; ok {
		if ref := l.topDef(imp.Spec, imp.Name); ref.def != nil {
			return l.target(r, ref, c), nil
		}
		// A static import, or an object's member.
		if cls, ok := l.classByFQN(r, imp.Spec); ok {
			if def, dp := r.method(cls, imp.Name, l.baseOf(r)); def != nil {
				return l.target(r, hDefRef{dp, def}, c), nil
			}
		}
		if l.inRepo(imp.Spec) {
			return "", []string{imp.Spec + "." + imp.Name}
		}
		return "", nil
	}
	if ref := l.topDef(l.pkg[p], c.Name); ref.def != nil {
		return l.target(r, ref, c), nil
	}
	for _, imp := range r.files[p].Imports {
		if imp.Name != "*" {
			continue
		}
		if ref := l.topDef(imp.Spec, c.Name); ref.def != nil {
			return l.target(r, ref, c), nil
		}
		if cls, ok := l.classByFQN(r, imp.Spec); ok {
			if def := r.def(cls.path, cls.owner, c.Name); def != nil {
				return l.target(r, hDefRef{cls.path, def}, c), nil
			}
		}
	}
	// Only a name nothing in the repo defines any more can have meant a
	// removed one.
	if len(r.byName[c.Name]) == 0 {
		return "", []string{"~" + c.Name}
	}
	return "", nil
}

func (l *jvmCalls) resolve(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	l.ensure(r)
	scopes := enclosingScopes(d)
	switch {
	case c.Recv == "this":
		if recv := jvmExtRecv(d); recv != "" {
			to, want, _ := l.onType(r, p, scopes, recv, c)
			return to, want
		}
		if d.Class == "" {
			return "", nil
		}
		if def, dp := r.method(hClassRef{p, d.Class}, c.Name, l.baseOf(r)); def != nil {
			return l.target(r, hDefRef{dp, def}, c), nil
		}
		return "", []string{r.idOf(p, d.Class, c.Name)}
	case c.Recv == "super":
		for _, b := range r.classes[p][d.Class] {
			if bc, ok := l.baseOf(r)(p, b); ok {
				if def, dp := r.method(bc, c.Name, l.baseOf(r)); def != nil {
					return l.target(r, hDefRef{dp, def}, c), nil
				}
			}
		}
		return "", nil
	case c.Recv == "":
		return l.bare(r, p, d, c)
	case !strings.HasPrefix(c.Recv, "?"):
		// pkg.fn(), pkg.Type()
		if _, ok := l.pkgFiles[c.Recv]; ok {
			if ref := l.topDef(c.Recv, c.Name); ref.def != nil {
				return l.target(r, ref, c), nil
			}
			return "", []string{c.Recv + "." + c.Name}
		}
		if to, want, ok := l.onType(r, p, scopes, c.Recv, c); ok {
			return to, want
		}
		if isUpper(c.Recv) {
			return "", nil // a type from outside the repo
		}
	}
	// obj.m(): only a method name no other class in the repo uses.
	if c.Name == "<init>" || jvmCommonMethods[c.Name] {
		return "", nil
	}
	if id, ok := r.uniqueMethod(c.Name); ok {
		return id, nil
	}
	return "", r.anyMethodWant(c.Name)
}

// fileOf resolves an import to the file declaring what it names, "" when
// that's outside the repo (or before the indexes are built).
func (l *jvmCalls) fileOf(_ string, imp hImport) string {
	if l.r == nil {
		return ""
	}
	if ref := l.topDef(imp.Spec, imp.Name); ref.def != nil {
		return ref.path
	}
	if c, ok := l.classByFQN(l.r, imp.Spec); ok {
		return c.path
	}
	return ""
}
