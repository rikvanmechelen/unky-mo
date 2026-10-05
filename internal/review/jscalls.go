package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// jsCalls is the JavaScript/TypeScript call analyzer: definitions found by
// brace depth on blanked code (functions, arrows bound to a name, classes
// and their methods, a Vue component's options object), calls resolved
// through this and the extends chain, the file's import bindings (aliases,
// namespaces, require, re-exports two hops deep, default exports), and
// the file's own scopes; a call on an unknown receiver only when exactly
// one method in the repo has its name. JSX tags and the tags in a .vue,
// .svelte or .astro template count as calls of the component.
//
// IDs are the path without its extension, "#", and the qualified name:
// src/cart/total#sum, src/ui/Button#Button.render. An index file keeps
// its own name (src/cart/index#total), and an anonymous default export,
// or a component file's template, is "default" (src/ui/Panel#default).
type jsCalls struct {
	nl    *nodeLang
	specs map[string]string // dir + "\x00" + specifier → repo file
}

func (l *jsCalls) name() string         { return "node" }
func (l *jsCalls) owns(p string) bool   { return (&nodeLang{}).owns(p) }
func (l *jsCalls) unit(p string) string { return (&nodeLang{}).unit(p) }

func (l *jsCalls) setup(idx *index) bool {
	l.nl = &nodeLang{}
	l.specs = map[string]string{}
	return l.nl.detect(idx)
}

func (l *jsCalls) id(p string, d *hDef) string {
	return strings.TrimSuffix(p, path.Ext(p)) + "#" + qualify(d.Owner, d.Name)
}

func (l *jsCalls) display(p string, d *hDef) string {
	if d.Name == "<module>" {
		return path.Base(p) + " (top level)"
	}
	parts := strings.Split(qualify(d.Owner, d.Name), ".")
	// An anonymous default export (a Stimulus controller, a component) is
	// known by its file's name.
	if parts[0] == "default" {
		base := path.Base(p)
		parts[0] = strings.TrimSuffix(base, path.Ext(base))
	}
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, ".")
}

// jsFnTail matches what makes a binding a function: "function (", an
// arrow's "(" (checked for its "=>" afterwards) or "x =>". Its groups are
// the function keyword, the arrow's "(" and the single parameter.
const jsFnTail = `(?:async\s+)?(?:(function)\b\s*\*?\s*[\w$]*\s*(?:<[^>(]*>\s*)?\(|(?:<[^>(]*>\s*)?(\()|([\w$]+)\s*=>)`

// The patterns run over whole files, so most start with a literal (Go's
// regexp skips ahead to it): the words before it (export, async, const x
// =) are read backwards from the match instead, and a match must start a
// word (jsWordAt).
var (
	jsFuncRe = regexp.MustCompile(`function\b\s*\*?\s*([\w$]*)\s*(?:<[^>(]*>\s*)?\(`)
	// const save = …, exports.save = … — the binding is read back from "=".
	jsAssignRe  = regexp.MustCompile(`=[ \t\n]*` + jsFnTail)
	jsBindingRe = regexp.MustCompile(`(?:\b(?:const|let|var)\s+([\w$]+)\s*(?::[^=;\n]+)?|\bexports\s*\.\s*([\w$]+)\s*)$`)
	jsClassRe   = regexp.MustCompile(`class\b([^{;=]*)\{`)
	jsOptionsRe = regexp.MustCompile(`export\s+default\s+(?:(?:defineComponent|Vue\s*\.\s*extend)\s*\(\s*)?\{`)
	// A class member or object method: modifiers, a name, "(" — at the
	// start of a line (decorators on the line before belong to it).
	jsMethodRe = regexp.MustCompile(`(?m)^[ \t]*(?:@[\w$.]+(?:\([^)\n]*\))?\s+)*((?:(?:public|private|protected|static|async|override|readonly|abstract|declare|get|set|accessor)\s+)*)(?:\*\s*)?(#?[\w$]+)\s*[?!]?\s*(?:<[^>(\n]*>\s*)?\(`)
	// A class property or object key holding a function: handle = () =>,
	// save: function (…).
	jsPropRe = regexp.MustCompile(`(?m)^[ \t]*((?:(?:public|private|protected|static|readonly|override)\s+)*)(#?[\w$]+)\s*[?!]?\s*(?::[^=\n]*?)?([=:])\s*` + jsFnTail)
	// TS blocks that declare and never run: their "f(x): T" aren't calls.
	jsTypeBlockRes = []*regexp.Regexp{
		regexp.MustCompile(`interface\s+[\w$]+[^{;]*\{`),
		regexp.MustCompile(`type\s+[\w$]+\s*(?:<[^=]*?>)?\s*=\s*\{`),
		regexp.MustCompile(`declare\s+(?:module|global|namespace)\b[^{;]*\{`),
		regexp.MustCompile(`enum\s+[\w$]+\s*\{`),
	}
	jsTagRe       = regexp.MustCompile(`<((?:[A-Za-z_$][\w$]*\.)*[A-Z][\w$]*)[\s/>]`) // <Name, <ns.Name
	jsHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Template bindings: ={save} (Svelte, Astro), @click="save" or
	// v-on:click="save(x)" (Vue), {format(x)} / {{ format(x) }}.
	jsMarkupRefRe  = regexp.MustCompile(`=\s*\{\s*([A-Za-z_$][\w$.]*)\s*\}`)
	jsMarkupVueRe  = regexp.MustCompile(`(?:@|v-on:)[\w.:-]+\s*=\s*"\s*([A-Za-z_$][\w$.]*)\s*(\()?`)
	jsMarkupCallRe = regexp.MustCompile(`\{\s*([A-Za-z_$][\w$.]*)\s*\(`)

	jsImportBindRe = regexp.MustCompile(`import\s+(?:type\s+)?(?:([\w$]+)\s*,?\s*)?(\{[^}]*\}|\*\s*as\s+[\w$]+)?\s*from\s*['"]([^'"\n]+)['"]`)
	jsRequireRe    = regexp.MustCompile(`require\s*\(\s*['"]([^'"\n]+)['"]\s*\)`)
	// "const x =" or "const { a, b } =" right before a require( or class.
	jsVarBindRe    = regexp.MustCompile(`\b(?:const|let|var)\s+(\{[^}]*\}|[\w$]+)\s*=\s*$`)
	jsReexportRe   = regexp.MustCompile(`export\s+(?:type\s+)?(\*\s*(?:as\s+[\w$]+\s*)?|\{[^}]*\})\s*from\s*['"]([^'"\n]+)['"]`)
	jsExportListRe = regexp.MustCompile(`export\s+(?:type\s+)?\{([^}]*)\}`)
	// export default save; export default memo(Button)
	jsExportDefRe = regexp.MustCompile(`export\s+default\s+(?:([\w$]+)|[\w$.]+\s*\(\s*([\w$]+)\s*\))[ \t]*(?:;|\n|$)`)
	jsModExportRe = regexp.MustCompile(`module\s*\.\s*exports\s*=\s*(?:([\w$]+)[ \t]*(?:;|\n|$)|\{([^}]*)\})`)
	jsExportsIsRe = regexp.MustCompile(`exports\s*\.\s*([\w$]+)\s*=\s*([\w$]+)[ \t]*(?:;|\n|$)`)
	jsClosingTag  = regexp.MustCompile(`</([A-Za-z>])`)
	jsGenericRe   = regexp.MustCompile(`<[^<>]*>`)
)

var jsKeywords = map[string]bool{
	"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "function": true,
	"typeof": true, "instanceof": true, "new": true, "await": true, "async": true, "yield": true, "void": true,
	"delete": true, "import": true, "require": true, "in": true, "of": true, "do": true, "else": true,
	"case": true, "throw": true, "with": true, "class": true, "extends": true, "export": true, "default": true,
	"const": true, "let": true, "var": true, "this": true, "true": true, "false": true, "null": true,
	"undefined": true, "NaN": true, "declare": true, "keyof": true, "as": true, "satisfies": true,
}

// jsGlobals are receivers that are the runtime's, never the repo's.
var jsGlobals = map[string]bool{
	"console": true, "Math": true, "JSON": true, "Object": true, "Array": true, "Promise": true, "window": true,
	"document": true, "process": true, "Number": true, "String": true, "Date": true, "Reflect": true,
	"navigator": true, "localStorage": true, "sessionStorage": true, "globalThis": true, "Symbol": true,
	"Error": true, "Intl": true, "URL": true, "Buffer": true, "module": true, "exports": true, "location": true,
	"history": true, "performance": true, "crypto": true, "customElements": true, "Boolean": true,
}

// jsCommonMethods are builtin method names (arrays, promises, maps,
// strings, events): a call of one on an unknown receiver is far more
// likely the builtin than the one repo method that shares the name.
var jsCommonMethods = map[string]bool{
	"then": true, "catch": true, "finally": true, "map": true, "filter": true, "reduce": true, "forEach": true,
	"push": true, "pop": true, "shift": true, "unshift": true, "join": true, "split": true, "slice": true,
	"splice": true, "find": true, "findIndex": true, "some": true, "every": true, "includes": true,
	"indexOf": true, "keys": true, "values": true, "entries": true, "get": true, "set": true, "has": true,
	"delete": true, "add": true, "clear": true, "toString": true, "replace": true, "trim": true, "test": true,
	"match": true, "bind": true, "call": true, "apply": true, "emit": true, "on": true, "off": true,
	"log": true, "error": true, "warn": true, "info": true, "debug": true, "concat": true, "sort": true,
	"flatMap": true, "startsWith": true, "endsWith": true, "addEventListener": true, "removeEventListener": true,
}

var jsxExts = map[string]bool{".js": true, ".jsx": true, ".tsx": true}

// jsCand is a definition found while scanning, with its offsets in the
// blanked code.
type jsCand struct {
	name        string
	start       int // the header's first byte (export, decorators, modifiers)
	nameAt      int
	open, close int // the parameter list's parentheses, -1 for a class or object
	param       string
	body        int // the body's "{", or an expression body's first byte
	end         int // the body's last byte
	class       bool
	object      bool // a Vue options object (a class for this and members)
	static      bool
	container   int // a member's class or object (an index in cands), else -1
	bases       []string
	path, cls   string // set once nested: its qualified name, enclosing class
}

// jsScanner is one file's scan.
type jsScanner struct {
	code   string // jsCode: comments, templates, regexes blanked
	cc     string // code with string contents blanked too
	s      *jsScanned
	pair   []int32 // an opener's offset → its closer's, -1 if none
	depth  []int32 // brace depth before each offset
	lines  []int   // line start offsets
	noCall map[int]bool
	typed  []bool // offsets inside a TS type block
	cands  []jsCand
	f      hFile
}

// scanFile reads a JS/TS file's definitions, calls, import bindings and
// exports. A .vue/.svelte/.astro file's script parts are scanned as code,
// and its template's component tags and bindings belong to the file's
// "default" definition (the component).
func (l *jsCalls) scanFile(p, src string) hFile {
	ext := path.Ext(p)
	component := ext == ".vue" || ext == ".svelte" || ext == ".astro"
	script, markup := src, ""
	if component {
		script = componentScript(p, src)
		b := []byte(src) // the template: what isn't script
		for i := range b {
			if script[i] != ' ' && b[i] != '\n' {
				b[i] = ' '
			}
		}
		markup = jsHTMLComment.ReplaceAllStringFunc(string(b), func(s string) string { return blankKeepLines(s) })
	} else if jsxExts[ext] {
		// "</div>" would read as the start of a regex literal.
		script = jsClosingTag.ReplaceAllString(script, "< $1")
	}
	sc := newJSScanner(script)
	sc.findDefs()
	sc.nest()
	sc.findImports()
	hashLines := strings.Split(jsHashText(script, sc.s), "\n")
	if component {
		ml := strings.Split(markup, "\n")
		for i := range hashLines {
			if i < len(ml) {
				hashLines[i] += " " + ml[i]
			}
		}
	}
	sc.build(hashLines, jsxExts[ext], markup)
	return sc.f
}

func blankKeepLines(s string) string {
	b := []byte(s)
	blank(b, 0, len(b))
	return string(b)
}

func newJSScanner(script string) *jsScanner {
	s := jsCode(script)
	b := []byte(s.code)
	fillLiterals(b, s)
	for _, sp := range s.strings {
		blank(b, sp[0]+1, sp[1]-1)
	}
	sc := &jsScanner{code: s.code, cc: string(b), s: s, noCall: map[int]bool{}, f: hFile{OK: true}}
	n := len(b)
	sc.pair = make([]int32, n)
	sc.depth = make([]int32, n+1)
	sc.lines = []int{0}
	var stack []int
	d := int32(0)
	for i := 0; i < n; i++ {
		sc.pair[i] = -1
		sc.depth[i] = d
		switch c := b[i]; c {
		case '\n':
			sc.lines = append(sc.lines, i+1)
		case '{', '(', '[':
			stack = append(stack, i)
			if c == '{' {
				d++
			}
		case '}':
			// Unclosed ( and [ inside a block (JSX text, mid-edit) end with it.
			for len(stack) > 0 && b[stack[len(stack)-1]] != '{' {
				stack = stack[:len(stack)-1]
			}
			if len(stack) == 0 {
				sc.f.OK = false
				continue
			}
			sc.pair[stack[len(stack)-1]] = int32(i)
			stack = stack[:len(stack)-1]
			d--
		case ')', ']':
			want := byte('(')
			if c == ']' {
				want = '['
			}
			if len(stack) > 0 && b[stack[len(stack)-1]] == want {
				sc.pair[stack[len(stack)-1]] = int32(i)
				stack = stack[:len(stack)-1]
			}
		}
	}
	sc.depth[n] = d
	for _, o := range stack {
		if b[o] == '{' {
			sc.f.OK = false
		}
	}
	sc.typed = make([]bool, n)
	for i, re := range jsTypeBlockRes {
		for _, m := range jsFind(re, sc.cc, [...]string{"interface", "type", "declare", "enum"}[i]) {
			if c := sc.closer(m[1] - 1); c > 0 {
				for i := m[0]; i <= c; i++ {
					sc.typed[i] = true
				}
			}
		}
	}
	return sc
}

// fillLiterals fills the template and regex literals jsCode blanked with
// "0"s: still no code inside them, but an expression rather than nothing,
// so "const re = /x/" can't run on into the next line. code is jsCode's
// output; comments stay blank.
func fillLiterals(code []byte, s *jsScanned) {
	for _, l := range s.literals {
		for i := l[0]; i < l[1] && i < len(code); i++ {
			if code[i] != '\n' {
				code[i] = '0'
			}
		}
	}
}

func (sc *jsScanner) closer(open int) int {
	if open < 0 || open >= len(sc.pair) {
		return -1
	}
	return int(sc.pair[open])
}

func (sc *jsScanner) lineOf(off int) int {
	return sort.Search(len(sc.lines), func(i int) bool { return sc.lines[i] > off })
}

func (sc *jsScanner) skipSpace(i int) int {
	for i < len(sc.cc) && (sc.cc[i] == ' ' || sc.cc[i] == '\t' || sc.cc[i] == '\n' || sc.cc[i] == '\r') {
		i++
	}
	return i
}

// bodyAfter finds the "{" of the body that follows a parameter list ending
// before i, past a return type annotation. ok is false when there's none:
// a TS overload, an abstract or declared method, an interface member.
func (sc *jsScanner) bodyAfter(i int) (int, bool) {
	cc := sc.cc
	k := sc.skipSpace(i)
	if k >= len(cc) {
		return 0, false
	}
	if cc[k] == '{' {
		return k, sc.closer(k) > k
	}
	if cc[k] != ':' {
		return 0, false
	}
	// A return type: up to the "{" that isn't part of it.
	angle := 0
	typeStart := true // a "{" here is an object type, not the body
	for k++; k < len(cc) && k < i+400; k++ {
		c := cc[k]
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			continue
		case c == '\n':
			j := sc.skipSpace(k)
			if typeStart || j >= len(cc) || strings.IndexByte("{|&.>=", cc[j]) >= 0 {
				continue
			}
			return 0, false
		case c == '=' && k+1 < len(cc) && cc[k+1] == '>':
			k++
			typeStart = true
			continue
		case c == '{' && (typeStart || angle > 0):
			if k = sc.closer(k); k < 0 {
				return 0, false
			}
			typeStart = false
			continue
		case c == '{':
			return k, sc.closer(k) > k
		case c == '(' || c == '[':
			if k = sc.closer(k); k < 0 {
				return 0, false
			}
		case c == '<':
			angle++
			typeStart = true
			continue
		case c == '>':
			angle--
		case c == ';' || c == '}' || c == ')' || c == ']':
			return 0, false
		case c == '|' || c == '&' || c == ',':
			typeStart = true
			continue
		}
		typeStart = false
	}
	return 0, false
}

// arrowAfter returns the offset just past the "=>" that follows a
// parameter list ending before i (past a return type), -1 if it isn't an
// arrow.
func (sc *jsScanner) arrowAfter(i int) int {
	cc := sc.cc
	k := sc.skipSpace(i)
	if k < len(cc) && cc[k] == ':' {
		angle := 0
		for k++; k < len(cc) && k < i+400; k++ {
			switch c := cc[k]; {
			case c == '=' && k+1 < len(cc) && cc[k+1] == '>':
				if angle == 0 {
					return k + 2
				}
				k++
			case c == '(' || c == '[' || c == '{':
				if k = sc.closer(k); k < 0 {
					return -1
				}
			case c == '<':
				angle++
			case c == '>':
				angle--
			case c == ';' || c == '\n' || c == ')' || c == '}' || c == ']':
				return -1
			}
		}
		return -1
	}
	if k+1 < len(cc) && cc[k] == '=' && cc[k+1] == '>' {
		return k + 2
	}
	return -1
}

// arrowBody returns an arrow's body from just past its "=>": a block, or
// an expression up to the end of its statement.
func (sc *jsScanner) arrowBody(i int) (body, end int, ok bool) {
	cc := sc.cc
	k := sc.skipSpace(i)
	if k >= len(cc) {
		return 0, 0, false
	}
	if cc[k] == '{' {
		c := sc.closer(k)
		return k, c, c > k
	}
	last := k
	for j := k; j < len(cc); j++ {
		switch c := cc[j]; c {
		case '(', '[', '{':
			cl := sc.closer(j)
			if cl < 0 {
				return k, len(cc) - 1, true
			}
			j, last = cl, cl
		case ')', ']', '}', ',':
			return k, last, true
		case ';':
			return k, j, true
		case '\n':
			// The expression goes on if its line ends in an operator or
			// the next line starts with one.
			p := lastNonSpace(cc[k:j])
			nx := sc.skipSpace(j)
			if (p >= 0 && strings.IndexByte("=>(,[{+-*/%&|?:.!<", cc[k+p]) >= 0) ||
				(nx < len(cc) && strings.IndexByte(".?:+-*/%&|=>", cc[nx]) >= 0) {
				continue
			}
			return k, last, true
		case ' ', '\t', '\r':
		default:
			last = j
		}
	}
	return k, last, true
}

// fnTail builds a candidate from a jsFnTail match: m are the match's
// submatch indexes and g the index of the tail's first group.
func (sc *jsScanner) fnTail(m []int, g int, cand jsCand) (jsCand, bool) {
	switch {
	case m[2*g] >= 0: // function (…) {
		cand.open = m[1] - 1
		cand.close = sc.closer(cand.open)
		if cand.close < 0 {
			return cand, false
		}
		body, ok := sc.bodyAfter(cand.close + 1)
		if !ok {
			return cand, false
		}
		cand.body, cand.end = body, sc.closer(body)
	case m[2*(g+1)] >= 0: // (…) => …
		cand.open = m[2*(g+1)]
		cand.close = sc.closer(cand.open)
		if cand.close < 0 {
			return cand, false
		}
		arrow := sc.arrowAfter(cand.close + 1)
		if arrow < 0 {
			return cand, false
		}
		var ok bool
		if cand.body, cand.end, ok = sc.arrowBody(arrow); !ok {
			return cand, false
		}
	case m[2*(g+2)] >= 0: // x => …
		cand.open, cand.close = -1, -1
		cand.param = sc.cc[m[2*(g+2)]:m[2*(g+2)+1]]
		var ok bool
		if cand.body, cand.end, ok = sc.arrowBody(m[1]); !ok {
			return cand, false
		}
	default:
		return cand, false
	}
	return cand, true
}

func (sc *jsScanner) setExport(name, local string) {
	if name == local || local == "" {
		return
	}
	if sc.f.Exports == nil {
		sc.f.Exports = map[string]string{}
	}
	sc.f.Exports[name] = local
}

// jsWordAt reports whether a match at i starts a word (not "xfunction",
// not a property ".class").
func jsWordAt(s string, i int) bool {
	return i == 0 || !isIdentByte(s[i-1]) && s[i-1] != '.' && s[i-1] != '#'
}

// jsFind is re's matches in s that start a word, skipping the scan when s
// doesn't contain lit.
func jsFind(re *regexp.Regexp, s, lit string) [][]int {
	if !strings.Contains(s, lit) {
		return nil
	}
	var out [][]int
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		if jsWordAt(s, m[0]) {
			out = append(out, m)
		}
	}
	return out
}

// wordsBefore walks back from i over the words in allowed ("export",
// "async"), returning where the first one starts and which there are.
func wordsBefore(s string, i int, allowed ...string) (int, map[string]bool) {
	seen := map[string]bool{}
	start := i
	for {
		j := i
		for j > 0 && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n' || s[j-1] == '\r') {
			j--
		}
		k := j
		for k > 0 && isIdentByte(s[k-1]) {
			k--
		}
		w := s[k:j]
		ok := false
		for _, a := range allowed {
			ok = ok || w == a
		}
		if !ok || !jsWordAt(s, k) {
			return start, seen
		}
		seen[w] = true
		start, i = k, k
	}
}

// findDefs collects the candidate definitions.
func (sc *jsScanner) findDefs() {
	cc := sc.cc
	add := func(c jsCand) {
		c.container = -1
		sc.cands = append(sc.cands, c)
	}
	for _, m := range jsFind(jsFuncRe, cc, "function") {
		name := cc[m[2]:m[3]]
		start, words := wordsBefore(cc, m[0], "export", "default", "declare", "async")
		isDefault := words["default"] && words["export"]
		c := jsCand{name: name, start: start, nameAt: m[2], open: m[1] - 1}
		if name == "" {
			if !isDefault {
				continue // an anonymous function expression: part of its parent
			}
			c.name, c.nameAt = "default", m[0]
		}
		c.close = sc.closer(c.open)
		if c.close < 0 {
			continue
		}
		body, ok := sc.bodyAfter(c.close + 1)
		if !ok {
			sc.noCall[c.nameAt] = true // an overload or declaration
			continue
		}
		c.body, c.end = body, sc.closer(body)
		if isDefault {
			sc.setExport("default", c.name)
		}
		add(c)
	}
	for _, m := range jsAssignRe.FindAllStringSubmatchIndex(cc, -1) {
		if m[0] > 0 && strings.IndexByte("=!<>+-*/%&|^?:", cc[m[0]-1]) >= 0 {
			continue // ==, +=, …
		}
		from := max(0, m[0]-200)
		b := jsBindingRe.FindStringSubmatchIndex(cc[from:m[0]])
		if b == nil {
			continue
		}
		g := 2 // const name
		if b[2] < 0 {
			g = 4 // exports.name
		}
		start := from + b[0]
		if g == 2 {
			start, _ = wordsBefore(cc, start, "export")
		} else if p := strings.TrimRight(cc[:start], " \t\n"); strings.HasSuffix(p, "module.") {
			start = len(p) - len("module.") // module.exports.name
		}
		nameAt := from + b[g]
		if c, ok := sc.fnTail(m, 1, jsCand{name: cc[nameAt : from+b[g+1]], start: start, nameAt: nameAt}); ok {
			add(c)
		}
	}
	addClass := func(start, nameAt, open int, header string, name string, isDefault bool) {
		header = jsGenericRe.ReplaceAllString(jsGenericRe.ReplaceAllString(header, " "), " ")
		fields := strings.Fields(header)
		var bases []string
		for i := 0; i < len(fields); i++ {
			switch {
			case fields[i] == "extends" && i+1 < len(fields):
				b := fields[i+1]
				if j := strings.IndexAny(b, "(,"); j >= 0 {
					b = b[:j]
				}
				if b != "" {
					bases = append(bases, b)
				}
				i++
			case fields[i] == "implements":
				i = len(fields)
			case i == 0 && name == "":
				name = fields[0]
			}
		}
		if name == "" {
			if !isDefault {
				return
			}
			name = "default"
		}
		if name != "default" && !jsIdent(name) {
			return
		}
		end := sc.closer(open)
		if end < 0 {
			return
		}
		if isDefault {
			sc.setExport("default", name)
		}
		add(jsCand{name: name, start: start, nameAt: nameAt, open: -1, close: -1, body: open, end: end, class: true, bases: bases})
	}
	for _, m := range jsFind(jsClassRe, cc, "class") {
		header := cc[m[2]:m[3]]
		if strings.ContainsAny(header, "\"'") {
			continue
		}
		nameAt := m[2] + len(header) - len(strings.TrimLeft(header, " \t\n"))
		start, words := wordsBefore(cc, m[0], "export", "default", "declare", "abstract")
		// const Cart = class { … }
		name := ""
		from := max(0, start-100)
		if b := jsVarBindRe.FindStringSubmatchIndex(cc[from:start]); b != nil && jsIdent(cc[from+b[2]:from+b[3]]) {
			nameAt = from + b[2]
			name = cc[nameAt : from+b[3]]
			start, _ = wordsBefore(cc, from+b[0], "export")
		}
		addClass(start, nameAt, m[1]-1, header, name, words["default"] && words["export"])
	}
	for _, m := range jsFind(jsOptionsRe, cc, "export") {
		if end := sc.closer(m[1] - 1); end > 0 {
			add(jsCand{name: "default", start: m[0], nameAt: m[0], open: -1, close: -1, body: m[1] - 1, end: end, class: true, object: true})
		}
	}
	// Members of the classes and options objects found so far.
	containers := len(sc.cands)
	for ci := 0; ci < containers; ci++ {
		con := sc.cands[ci]
		if !con.class {
			continue
		}
		object := con.object
		inner := cc[con.body+1 : con.end]
		base := con.body + 1
		memberDepth := int(sc.depth[con.body]) + 1
		at := func(off int) bool { return object || int(sc.depth[off]) == memberDepth }
		for _, m := range jsMethodRe.FindAllStringSubmatchIndex(inner, -1) {
			name := inner[m[4]:m[5]]
			nameAt := base + m[4]
			if jsKeywords[name] && name != "constructor" || !at(nameAt) {
				continue
			}
			mods := inner[m[2]:m[3]]
			c := jsCand{name: name, start: base + m[0], nameAt: nameAt, open: base + m[1] - 1,
				static: strings.Contains(mods, "static"), container: ci}
			c.close = sc.closer(c.open)
			if c.close < 0 {
				continue
			}
			body, ok := sc.bodyAfter(c.close + 1)
			if !ok {
				if !object {
					sc.noCall[nameAt] = true // abstract, overload, declared
				}
				continue
			}
			c.body, c.end = body, sc.closer(body)
			sc.cands = append(sc.cands, c)
		}
		for _, m := range jsPropRe.FindAllStringSubmatchIndex(inner, -1) {
			name := inner[m[4]:m[5]]
			nameAt := base + m[4]
			sep := inner[m[6]:m[7]]
			if jsKeywords[name] || !at(nameAt) || (object != (sep == ":")) {
				continue
			}
			shifted := make([]int, len(m))
			for i, v := range m {
				shifted[i] = v
				if v >= 0 {
					shifted[i] = v + base
				}
			}
			c := jsCand{name: name, start: base + m[0], nameAt: nameAt,
				static: strings.Contains(inner[m[2]:m[3]], "static"), container: ci}
			if c, ok := sc.fnTail(shifted, 4, c); ok {
				sc.cands = append(sc.cands, c)
			}
		}
	}
}

func jsIdent(s string) bool {
	if s == "" || jsKeywords[s] {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) || (i == 0 && s[i] >= '0' && s[i] <= '9') {
			return false
		}
	}
	return true
}

// nest orders the candidates, drops duplicates (a header two patterns
// matched) and members that aren't directly in their container, and works
// out each one's qualified name and enclosing class.
func (sc *jsScanner) nest() {
	idx := make([]int, len(sc.cands))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ca, cb := sc.cands[idx[a]], sc.cands[idx[b]]
		if ca.start != cb.start {
			return ca.start < cb.start
		}
		return ca.end > cb.end
	})
	var kept []jsCand
	keptOf := map[int]int{} // original index → index in kept
	var stack []int         // indexes in kept
	for _, oi := range idx {
		c := sc.cands[oi]
		if c.end < c.body {
			continue
		}
		for len(stack) > 0 && kept[stack[len(stack)-1]].end < c.start {
			stack = stack[:len(stack)-1]
		}
		dup := false
		for _, si := range stack {
			if s := kept[si]; c.start >= s.start && c.start < s.body {
				dup = true
			}
		}
		if dup {
			continue
		}
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		if c.container >= 0 {
			ki, ok := keptOf[c.container]
			if !ok || ki != parent {
				continue
			}
		}
		if parent >= 0 && c.end > kept[parent].end {
			c.end = kept[parent].end
		}
		owner, cls := "", ""
		if parent >= 0 {
			owner = kept[parent].path
			cls = kept[parent].cls
			if kept[parent].class {
				cls = kept[parent].path
			}
		}
		c.path = qualify(owner, c.name)
		c.cls = cls
		keptOf[oi] = len(kept)
		kept = append(kept, c)
		stack = append(stack, len(kept)-1)
		if c.nameAt >= 0 {
			sc.noCall[c.nameAt] = true
		}
	}
	sc.cands = kept
}

// findImports reads the import bindings, re-exports and exports.
func (sc *jsScanner) findImports() {
	code := sc.code
	line := func(off int) int { return sc.lineOf(off) }
	for _, m := range jsFind(jsImportBindRe, code, "import") {
		if sc.s.inString(m[0]) {
			continue
		}
		spec, ln := code[m[6]:m[7]], line(m[0])
		if m[2] >= 0 && code[m[2]:m[3]] != "type" {
			sc.f.Imports = append(sc.f.Imports, hImport{Local: code[m[2]:m[3]], Spec: spec, Name: "default", Line: ln})
		}
		if m[4] >= 0 {
			if what := code[m[4]:m[5]]; strings.HasPrefix(what, "*") {
				f := strings.Fields(strings.TrimPrefix(what, "*"))
				sc.f.Imports = append(sc.f.Imports, hImport{Local: f[len(f)-1], Spec: spec, Line: ln})
			} else {
				sc.f.Imports = append(sc.f.Imports, jsBindings(what, spec, ln, "as")...)
			}
		}
		sc.exclude(m[0], m[1])
	}
	for _, m := range jsFind(jsRequireRe, code, "require") {
		from := max(0, m[0]-300)
		b := jsVarBindRe.FindStringSubmatchIndex(code[from:m[0]])
		if sc.s.inString(m[0]) || b == nil {
			continue
		}
		spec, ln := code[m[2]:m[3]], line(from+b[0])
		if what := code[from+b[2] : from+b[3]]; strings.HasPrefix(what, "{") {
			sc.f.Imports = append(sc.f.Imports, jsBindings(what, spec, ln, ":")...)
		} else {
			sc.f.Imports = append(sc.f.Imports, hImport{Local: what, Spec: spec, Line: ln})
		}
		sc.exclude(from+b[0], m[1])
	}
	for _, m := range jsFind(jsReexportRe, code, "export") {
		if sc.s.inString(m[0]) {
			continue
		}
		spec, ln := code[m[4]:m[5]], line(m[0])
		what := strings.TrimSpace(code[m[2]:m[3]])
		if strings.HasPrefix(what, "*") {
			local := "*"
			if f := strings.Fields(strings.TrimPrefix(what, "*")); len(f) == 2 {
				local = f[1] // export * as ns
			}
			sc.f.Reexports = append(sc.f.Reexports, hImport{Local: local, Spec: spec, Line: ln})
		} else {
			sc.f.Reexports = append(sc.f.Reexports, jsBindings(what, spec, ln, "as")...)
		}
		sc.exclude(m[0], m[1])
	}
	for _, m := range jsFind(jsExportListRe, code, "export") {
		if sc.s.inString(m[0]) || strings.HasPrefix(code[sc.skipSpace(m[1]):], "from") {
			continue
		}
		for _, b := range jsBindings(code[m[2]:m[3]], "", 0, "as") {
			sc.setExport(b.Local, b.Name)
		}
		sc.exclude(m[0], m[1])
	}
	for _, m := range jsFind(jsExportDefRe, code, "export") {
		local := ""
		if m[2] >= 0 {
			local = code[m[2]:m[3]]
		} else {
			local = code[m[4]:m[5]]
		}
		if !jsKeywords[local] {
			sc.setExport("default", local)
		}
	}
	for _, m := range jsFind(jsModExportRe, code, "module") {
		if m[2] >= 0 {
			sc.setExport("default", code[m[2]:m[3]])
			continue
		}
		for _, b := range jsBindings("{"+code[m[4]:m[5]]+"}", "", 0, ":") {
			sc.setExport(b.Local, b.Name)
		}
		sc.exclude(m[0], m[1])
	}
	if !strings.Contains(code, "exports") {
		return
	}
	for _, m := range jsExportsIsRe.FindAllStringSubmatchIndex(code, -1) {
		module := strings.HasSuffix(strings.TrimRight(code[:m[0]], " \t\n"), "module.")
		if rhs := code[m[4]:m[5]]; !jsKeywords[rhs] && (module || jsWordAt(code, m[0])) {
			sc.setExport(code[m[2]:m[3]], rhs)
		}
	}
}

// exclude marks a statement whose names aren't references (an import or
// export list).
func (sc *jsScanner) exclude(from, to int) {
	for i := from; i < to && i < len(sc.typed); i++ {
		sc.typed[i] = true
	}
}

// jsBindings reads "{ a, b as c, default as D, type T }" (or "{ a: b }"
// with sep ":", for require and module.exports): Local is the bound name,
// Name the original.
func jsBindings(list, spec string, line int, sep string) []hImport {
	var out []hImport
	for _, part := range strings.Split(strings.Trim(strings.TrimSpace(list), "{}"), ",") {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "type "))
		name, local := part, part
		if a, b, ok := strings.Cut(part, " "+sep+" "); ok || sep == ":" {
			if sep == ":" {
				a, b, ok = strings.Cut(part, ":")
			}
			if ok {
				name, local = strings.TrimSpace(a), strings.TrimSpace(b)
			}
		}
		if !jsIdent(name) && name != "default" || !jsIdent(local) && local != "default" {
			continue
		}
		out = append(out, hImport{Local: local, Spec: spec, Name: name, Line: line})
	}
	return out
}

// jsSite is a call or reference found in the code, at the offset of its
// name.
type jsSite struct {
	at int
	c  hCall
}

// build turns the candidates into hDefs, attributing each call to the
// innermost definition around it; what's outside them all belongs to a
// "<module>" definition ("default" in a component file, with the
// template).
func (sc *jsScanner) build(hashLines []string, jsx bool, markup string) {
	cc := sc.cc
	n := len(cc)
	owner := make([]int32, n)
	for i := range owner {
		owner[i] = -1
	}
	for ci, c := range sc.cands {
		for i := c.start; i <= c.end && i < n; i++ {
			owner[i] = int32(ci)
		}
	}
	defs := make([]hDef, len(sc.cands))
	for ci, c := range sc.cands {
		d := hDef{Name: c.name, Class: c.cls, IsClass: c.class, Static: c.static,
			Line: sc.lineOf(c.start), End: sc.lineOf(c.end)}
		if i := strings.LastIndex(c.path, "."); i >= 0 {
			d.Owner = c.path[:i]
		}
		switch {
		case c.open >= 0 && c.close > c.open:
			d.Sig = hashOf(strings.Join(strings.Fields(sc.code[c.open+1:c.close]), ""))
		case !c.class:
			d.Sig = hashOf(c.param)
		}
		defs[ci] = d
		if c.class {
			sc.f.Classes = append(sc.f.Classes, hClass{Name: c.path, Bases: c.bases})
		}
	}
	module := -1
	var moduleLines []int
	toModule := func(ln int, c hCall) {
		if module < 0 {
			defs = append(defs, hDef{Name: "<module>", Line: ln})
			module = len(defs) - 1
		}
		defs[module].Calls = append(defs[module].Calls, c)
		moduleLines = append(moduleLines, ln-1)
	}
	for _, s := range sc.sites(jsx) {
		if s.at >= n {
			continue
		}
		di := owner[s.at]
		if di < 0 {
			toModule(s.c.Line, s.c)
			continue
		}
		c := sc.cands[di]
		if s.c.Ref && c.open >= 0 && s.at > c.open && s.at < c.close {
			continue // a parameter
		}
		defs[di].Calls = append(defs[di].Calls, s.c)
	}

	// Own lines: each definition's lines but its nested ones'.
	lineOwner := make([]int, len(hashLines))
	for i := range lineOwner {
		lineOwner[i] = -1
	}
	for ci := range sc.cands {
		for ln := defs[ci].Line - 1; ln < defs[ci].End && ln < len(lineOwner); ln++ {
			lineOwner[ln] = ci
		}
	}
	own := make([][]int, len(defs))
	for ln, ci := range lineOwner {
		if ci >= 0 {
			own[ci] = append(own[ci], ln)
		}
	}

	if markup != "" {
		// The template belongs to the component: the options object if
		// there is one, else a "default" definition, which also takes the
		// script's top-level calls (<script setup> runs per instance).
		comp := -1
		for ci, c := range sc.cands {
			if c.name == "default" && c.path == "default" {
				comp = ci
			}
		}
		if comp < 0 {
			if module < 0 {
				defs = append(defs, hDef{Name: "default"})
				module = len(defs) - 1
				own = append(own, nil)
			}
			defs[module].Name = "default"
			comp = module
		}
		calls, lines := markupCalls(markup)
		defs[comp].Calls = append(defs[comp].Calls, calls...)
		if comp == module {
			moduleLines = append(moduleLines, lines...)
		} else {
			own[comp] = append(own[comp], lines...)
		}
	}
	if module >= 0 {
		for len(own) < len(defs) {
			own = append(own, nil)
		}
		sort.Ints(moduleLines)
		moduleLines = sortedUniqueInts(moduleLines)
		own[module] = moduleLines
		if len(moduleLines) > 0 {
			defs[module].Line = moduleLines[0] + 1
			defs[module].End = moduleLines[len(moduleLines)-1] + 1
		}
	}
	for i := range defs {
		defs[i].Body = hLinesBody(hashLines, own[i], nil)
	}
	sc.f.Defs = defs
}

func sortedUniqueInts(s []int) []int {
	var out []int
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// sites finds the calls, references and (in JSX) component tags in the
// blanked code, in order.
func (sc *jsScanner) sites(jsx bool) []jsSite {
	text := strings.ReplaceAll(sc.cc, "?.", " .") // optional chaining reads as a plain call
	var out []jsSite
	// Calls: from each "(", back over an optional <generic> to a chain of
	// names (recv.name). A chain right after a "." that has no name
	// before it (foo().bar(), a line starting with .then) hangs off an
	// expression: its receiver is "?".
	for open := 0; open < len(text); open++ {
		if text[open] != '(' {
			continue
		}
		end := skipBack(text, open, false)
		if end > 0 && text[end-1] == '>' {
			k := strings.LastIndexByte(text[max(0, end-100):end-1], '<')
			if k < 0 {
				continue
			}
			k += max(0, end-100)
			if strings.TrimFunc(text[k+1:end-1], func(r rune) bool {
				return r < 0x80 && (isIdentByte(byte(r)) || strings.ContainsRune(" \t\n.,[]", r))
			}) != "" {
				continue // a comparison, not type arguments
			}
			end = skipBack(text, k, false)
		}
		start := identBefore(text, end)
		if start == end {
			continue
		}
		parts := []string{text[start:end]}
		at := start
		recvExpr := false
		for p := start; ; {
			q := skipBack(text, p, true)
			if q == 0 || text[q-1] != '.' || (q >= 2 && text[q-2] == '.') { // not "...spread"
				break
			}
			q = skipBack(text, q-1, true)
			s := identBefore(text, q)
			if s == q {
				recvExpr = true
				break
			}
			parts = append([]string{text[s:q]}, parts...)
			p = s
		}
		name := parts[len(parts)-1]
		if sc.noCall[at] || sc.typed[at] || jsKeywords[name] || jsKeywords[parts[0]] && parts[0] != "this" {
			continue
		}
		// name(…) { … } is an object method's definition, not a call.
		if cl := sc.closer(open); cl > 0 {
			if _, ok := sc.bodyAfter(cl + 1); ok {
				continue
			}
		}
		recv := strings.Join(parts[:len(parts)-1], ".")
		if recvExpr {
			recv = strings.TrimSuffix("?."+recv, ".")
		}
		c := hCall{Recv: recv, Name: name, Line: sc.lineOf(at)}
		if recv == "" && name == "super" {
			c = hCall{Recv: "super", Name: "constructor", Line: c.Line}
		}
		out = append(out, jsSite{at, c})
	}
	// Functions passed as values: after "(", "," or a lone "=", before
	// ",", ")" or ";" — or a JSX attribute's ={name}.
	for i := 0; i < len(text); i++ {
		kind := text[i]
		switch kind {
		case '(', ',':
		case '=':
			if i > 0 && strings.IndexByte("=!<>+-*/%&|^?:", text[i-1]) >= 0 || i+1 < len(text) && (text[i+1] == '=' || text[i+1] == '>') {
				continue
			}
		case '{':
			if j := lastNonSpace(text[:i]); j < 0 || text[j] != '=' {
				continue
			}
		default:
			continue
		}
		s := skipFwd(text, i+1, true)
		j := s
		if strings.HasPrefix(text[j:], "this") && identAt(text, j) == j+4 {
			if k := skipFwd(text, j+4, true); k < len(text) && text[k] == '.' {
				j = skipFwd(text, k+1, true)
			}
		}
		e := identAt(text, j)
		if e == j {
			continue
		}
		if k := skipFwd(text, e, true); k+1 < len(text) && text[k] == '.' && text[k+1] != '.' {
			if k2 := skipFwd(text, k+1, true); identAt(text, k2) > k2 {
				e = identAt(text, k2)
			}
		}
		k := skipFwd(text, e, false)
		if k >= len(text) || sc.typed[s] || sc.noCall[s] {
			continue
		}
		next, ok := text[k], false
		switch kind {
		case '(', ',':
			ok = next == ',' || next == ')'
		case '=':
			ok = next == ';' || next == '\n' || next == ',' || next == ')'
		case '{':
			ok = next == '}'
		}
		if !ok {
			continue
		}
		word := strings.Join(strings.Fields(text[s:e]), "")
		recv, name := "", word
		if d := strings.LastIndex(word, "."); d >= 0 {
			recv, name = word[:d], word[d+1:]
		}
		if jsKeywords[name] || jsKeywords[recv] && recv != "this" {
			continue
		}
		out = append(out, jsSite{s, hCall{Recv: recv, Name: name, Line: sc.lineOf(s), Ref: true}})
	}
	if jsx {
		for _, m := range jsTagRe.FindAllStringSubmatchIndex(text, -1) {
			if sc.typed[m[0]] {
				continue
			}
			if j := m[0] - 1; j >= 0 && (isIdentByte(text[j]) || text[j] == ')' || text[j] == ']') {
				continue // a generic: Array<Item>
			}
			out = append(out, jsSite{m[2], tagCall(text[m[2]:m[3]], sc.lineOf(m[2]))})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

// skipBack moves back from i over spaces and tabs (and newlines with nl).
func skipBack(s string, i int, nl bool) int {
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == '\r' || nl && s[i-1] == '\n') {
		i--
	}
	return i
}

// skipFwd moves forward from i over spaces and tabs (and newlines with nl).
func skipFwd(s string, i int, nl bool) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || nl && s[i] == '\n') {
		i++
	}
	return i
}

// identBefore is the start of the name ("#private" too) ending at i, i if
// there's none.
func identBefore(s string, i int) int {
	k := i
	for k > 0 && isIdentByte(s[k-1]) {
		k--
	}
	if k == i || s[k] >= '0' && s[k] <= '9' {
		return i
	}
	if k > 0 && s[k-1] == '#' {
		k--
	}
	return k
}

// identAt is the end of the name ("#private" too) starting at i, i if
// there's none.
func identAt(s string, i int) int {
	k := i
	if k < len(s) && s[k] == '#' {
		k++
	}
	if k >= len(s) || !isIdentByte(s[k]) || s[k] >= '0' && s[k] <= '9' {
		return i
	}
	for k < len(s) && isIdentByte(s[k]) {
		k++
	}
	return k
}

func tagCall(tag string, line int) hCall {
	recv, name := "", tag
	if i := strings.LastIndex(tag, "."); i >= 0 {
		recv, name = tag[:i], tag[i+1:]
	}
	return hCall{Recv: recv, Name: name, Line: line}
}

// markupCalls reads a component template's tags and bindings, with the
// (0-based) lines that hold markup.
func markupCalls(markup string) ([]hCall, []int) {
	var calls []hCall
	var lines []int
	for i, ln := range strings.Split(markup, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lines = append(lines, i)
		for _, m := range jsTagRe.FindAllStringSubmatch(ln, -1) {
			calls = append(calls, tagCall(m[1], i+1))
		}
		for _, m := range jsMarkupRefRe.FindAllStringSubmatch(ln, -1) {
			c := tagCall(m[1], i+1)
			c.Ref = true
			calls = append(calls, c)
		}
		for _, m := range jsMarkupVueRe.FindAllStringSubmatch(ln, -1) {
			c := tagCall(m[1], i+1)
			c.Ref = m[2] == ""
			calls = append(calls, c)
		}
		for _, m := range jsMarkupCallRe.FindAllStringSubmatch(ln, -1) {
			if !jsKeywords[m[1]] {
				calls = append(calls, tagCall(m[1], i+1))
			}
		}
	}
	return calls, lines
}

// jsHashText is the script with its comments blanked, for body hashes: a
// comment edit isn't a change, but one in a template literal or regex is.
// s is jsCode's scan of it: its code with the literals put back.
func jsHashText(script string, s *jsScanned) string {
	b := []byte(s.code)
	for _, l := range s.literals {
		copy(b[l[0]:l[1]], script[l[0]:l[1]])
	}
	return string(b)
}

// fileOf resolves a binding's specifier from file p, cached per directory
// (relative specifiers and tsconfig lookups only depend on it).
func (l *jsCalls) fileOf(p string, imp hImport) string {
	key := path.Dir(p) + "\x00" + imp.Spec
	if f, ok := l.specs[key]; ok {
		return f
	}
	f := l.nl.resolveSpec(p, imp.Spec)
	l.specs[key] = f
	return f
}

// exportOf finds what file exports as name: a top-level definition (under
// its local name), or one it imports and passes on, or a re-export
// (export { a } from, export * from) — followed hops more files deep.
// external is true when the name comes from a package outside the repo.
func (l *jsCalls) exportOf(r *hResolver, file, name string, hops int) (ref hDefRef, want []string, external bool) {
	f := r.files[file]
	if f == nil {
		return hDefRef{}, nil, true
	}
	local := name
	if x, ok := f.Exports[name]; ok {
		local = x
	}
	if def := r.def(file, "", local); def != nil {
		return hDefRef{file, def}, nil, false
	}
	follow := func(imp hImport, name string) bool {
		target := l.fileOf(file, imp)
		if target == "" {
			external = true
			return false
		}
		got, w, ext := l.exportOf(r, target, name, hops-1)
		ref, external = got, ext
		want = append(want, w...)
		return got.def != nil
	}
	if hops > 0 {
		if imp, ok := r.imports[file][local]; ok && imp.Name != "" && follow(imp, imp.Name) {
			return ref, nil, false
		}
		for _, re := range f.Reexports {
			switch {
			case re.Local == name && re.Name != "":
				if follow(re, re.Name) {
					return ref, nil, false
				}
			case re.Local == "*" && name != "default":
				if follow(re, name) {
					return ref, nil, false
				}
			}
		}
	}
	if external {
		return hDefRef{}, nil, true
	}
	return hDefRef{}, append([]string{r.idOf(file, "", local)}, want...), false
}

// lookupName resolves a bare name in file p from inside d: d's nested
// definitions and the enclosing functions' (class bodies don't count),
// the file's top level, then its imports.
func (l *jsCalls) lookupName(r *hResolver, p string, d *hDef, name string) (hDefRef, []string, bool) {
	scopes := []string{""}
	if d != nil {
		scopes = enclosingScopes(d)
	}
	for _, sc := range scopes {
		if _, isClass := r.classes[p][sc]; isClass && sc != "" {
			continue
		}
		if def := r.def(p, sc, name); def != nil {
			return hDefRef{p, def}, nil, false
		}
	}
	imp, ok := r.imports[p][name]
	if !ok {
		return hDefRef{}, nil, false
	}
	file := l.fileOf(p, imp)
	if file == "" {
		return hDefRef{}, nil, true
	}
	if imp.Name == "" {
		// A whole module called: CommonJS's module.exports = fn.
		return l.exportOf(r, file, "default", 2)
	}
	return l.exportOf(r, file, imp.Name, 2)
}

// classAt resolves a class name as written in file p (Base, ns.Base).
func (l *jsCalls) classAt(r *hResolver, p, name string) (hClassRef, bool) {
	parts := strings.Split(name, ".")
	var ref hDefRef
	switch len(parts) {
	case 1:
		ref, _, _ = l.lookupName(r, p, nil, name)
	case 2:
		if imp, ok := r.imports[p][parts[0]]; ok && imp.Name == "" {
			ref, _, _ = l.exportOf(r, l.fileOf(p, imp), parts[1], 2)
		}
	}
	if ref.def != nil && ref.def.IsClass {
		return hClassRef{ref.path, qualify(ref.def.Owner, ref.def.Name)}, true
	}
	return hClassRef{}, false
}

func (l *jsCalls) baseOf(r *hResolver) func(p, name string) (hClassRef, bool) {
	return func(p, name string) (hClassRef, bool) { return l.classAt(r, p, name) }
}

// target is the ID a call of ref reaches: a class's constructor (its own
// or inherited), else the class or function itself.
func (l *jsCalls) target(r *hResolver, ref hDefRef, c hCall) string {
	if ref.def.IsClass && !c.Ref {
		cls := hClassRef{ref.path, qualify(ref.def.Owner, ref.def.Name)}
		if init, ip := r.method(cls, "constructor", l.baseOf(r)); init != nil {
			return r.l.id(ip, init)
		}
	}
	return r.l.id(ref.path, ref.def)
}

func (l *jsCalls) resolve(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	// this in a class body (a property initializer) is the class itself.
	cls := d.Class
	if d.IsClass {
		cls = qualify(d.Owner, d.Name)
	}
	switch {
	case c.Recv == "this":
		if cls == "" {
			return "", nil
		}
		if def, dp := r.method(hClassRef{p, cls}, c.Name, l.baseOf(r)); def != nil {
			return r.l.id(dp, def), nil
		}
		return "", []string{r.idOf(p, cls, c.Name)}
	case c.Recv == "super":
		for _, b := range r.classes[p][cls] {
			if bc, ok := l.classAt(r, p, b); ok {
				if def, dp := r.method(bc, c.Name, l.baseOf(r)); def != nil {
					return r.l.id(dp, def), nil
				}
				if def := r.byKey[bc.path][bc.owner]; def != nil && c.Name == "constructor" {
					return r.l.id(bc.path, def), nil // a base without a constructor of its own
				}
			}
		}
		return "", nil
	case c.Recv == "":
		// A Vue options object's template names its own methods.
		if d.IsClass && d.Owner == "" && d.Name == "default" {
			if def := r.def(p, "default", c.Name); def != nil {
				return r.l.id(p, def), nil
			}
		}
		ref, want, _ := l.lookupName(r, p, d, c.Name)
		if ref.def == nil {
			return "", want
		}
		return l.target(r, ref, c), nil
	case !strings.HasPrefix(c.Recv, "?"):
		chain := strings.Split(c.Recv, ".")
		if imp, ok := r.imports[p][chain[0]]; ok {
			file := l.fileOf(p, imp)
			if file == "" {
				return "", nil // a package from npm
			}
			if imp.Name == "" && len(chain) == 1 {
				ref, want, ext := l.exportOf(r, file, c.Name, 2)
				if ref.def == nil {
					if ext {
						return "", nil
					}
					return "", want
				}
				return l.target(r, ref, c), nil
			}
		}
		if jsGlobals[chain[0]] {
			return "", nil
		}
		if cls, ok := l.classAt(r, p, c.Recv); ok {
			if def, dp := r.method(cls, c.Name, l.baseOf(r)); def != nil {
				return r.l.id(dp, def), nil
			}
			return "", []string{r.idOf(cls.path, cls.owner, c.Name)}
		}
	}
	// obj.m(): only a method name no other class in the repo uses.
	if c.Name == "constructor" || jsCommonMethods[c.Name] {
		return "", nil
	}
	if id, ok := r.uniqueMethod(c.Name); ok {
		return id, nil
	}
	return "", r.anyMethodWant(c.Name)
}
