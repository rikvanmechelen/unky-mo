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
		drawn: map[string]bool{routesFile: true}, out: &routeSet{}}
	e.file(routesFile, main, rtScope{})
	return e.out
}

// --- Tokens ---

type rtTokKind byte

const (
	rtIdent rtTokKind = iota + 1 // a name, keyword or constant
	rtSym                        // :name
	rtLabel                      // name:
	rtStr                        // "…" (text is the contents)
	rtWords                      // %w[] / %i[] (words in list)
	rtRegex                      // /…/, %r{}
	rtNum
	rtIvar // @x, $x
	rtOp   // punctuation and operators
	rtNL   // end of line
)

type rtTok struct {
	kind  rtTokKind
	text  string
	words []string
	sym   bool // rtWords: %i (symbols)
	line  int
	space bool // whitespace before it
}

// rtTokens splits routes source into tokens. Comments, =begin blocks and
// heredoc bodies are dropped; a string keeps its contents as written
// (interpolation included).
func rtTokens(src string) []rtTok {
	var out []rtTok
	b := []byte(src)
	n := len(b)
	line := 1
	space := true
	var heredocs []string // terminators to skip after the line
	emit := func(k rtTokKind, text string) {
		out = append(out, rtTok{kind: k, text: text, line: line, space: space})
		space = false
	}
	for i := 0; i < n; {
		c := b[i]
		if (i == 0 || b[i-1] == '\n') && strings.HasPrefix(src[i:], "=begin") {
			end := strings.Index(src[i:], "\n=end")
			if end < 0 {
				break
			}
			line += strings.Count(src[i:i+end+1], "\n")
			i += end + len("\n=end")
			for i < n && b[i] != '\n' {
				i++
			}
			continue
		}
		switch {
		case c == '\n':
			emit(rtNL, "")
			line++
			i++
			space = true
			for len(heredocs) > 0 {
				term := heredocs[0]
				heredocs = heredocs[1:]
				for i < n {
					j := strings.IndexByte(src[i:], '\n')
					l := src[i:]
					if j >= 0 {
						l = src[i : i+j]
					}
					i += len(l)
					if i < n {
						i++
						line++
					}
					if strings.TrimSpace(l) == term {
						break
					}
				}
			}
		case c == ' ' || c == '\t' || c == '\r':
			space = true
			i++
		case c == '\\' && i+1 < n && b[i+1] == '\n':
			i += 2 // a line continuation
			line++
			space = true
		case c == '#':
			for i < n && b[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			j := rtStringEnd(b, i+1, c)
			text := src[i+1 : min(j, n)]
			if c == '"' {
				text = strings.ReplaceAll(text, `\"`, `"`)
			} else {
				text = strings.ReplaceAll(text, `\'`, `'`)
			}
			emit(rtStr, text)
			line += strings.Count(src[i:min(j+1, n)], "\n")
			i = j + 1
			// "key": value
			if i < n && b[i] == ':' && (i+1 >= n || b[i+1] != ':') {
				out[len(out)-1].kind = rtLabel
				i++
			}
		case c == '%' && i+2 < n && strings.IndexByte("wWiIqQr", b[i+1]) >= 0 && strings.IndexByte("([{<|!/^", b[i+2]) >= 0:
			open, closer := b[i+2], closingDelim(b[i+2])
			depth, j := 1, i+3
			for j < n && depth > 0 {
				switch {
				case b[j] == '\\':
					j++
				case b[j] == closer && open != closer:
					depth--
				case b[j] == open && open != closer:
					depth++
				case b[j] == closer:
					depth = 0
				}
				if depth > 0 {
					j++
				}
			}
			body := src[i+3 : min(j, n)]
			switch b[i+1] {
			case 'w', 'W', 'i', 'I':
				out = append(out, rtTok{kind: rtWords, words: strings.Fields(body), sym: b[i+1] == 'i' || b[i+1] == 'I', line: line, space: space})
				space = false
			case 'r':
				emit(rtRegex, body)
			default:
				emit(rtStr, body)
			}
			line += strings.Count(src[i:min(j+1, n)], "\n")
			i = j + 1
		case c == '/' && regexpStart(b, i):
			j := i + 1
			for j < n && b[j] != '/' && b[j] != '\n' {
				if b[j] == '\\' && j+1 < n && b[j+1] != '\n' {
					j++
				}
				j++
			}
			if j < n && b[j] == '/' {
				emit(rtRegex, src[i+1:j])
				i = j + 1
				for i < n && isWordByte(b[i]) {
					i++ // flags
				}
				continue
			}
			emit(rtOp, "/")
			i++
		case c == ':' && i+1 < n && (b[i+1] == '"' || b[i+1] == '\''):
			j := rtStringEnd(b, i+2, b[i+1])
			emit(rtSym, src[i+2:min(j, n)])
			i = j + 1
		case c == ':' && i+1 < n && (isWordByte(b[i+1]) && !(b[i+1] >= '0' && b[i+1] <= '9')) && (i == 0 || b[i-1] != ':'):
			j := i + 1
			for j < n && isWordByte(b[j]) {
				j++
			}
			if j < n && (b[j] == '?' || b[j] == '!' || b[j] == '=') && (j+1 >= n || b[j+1] != '>' && b[j+1] != '=') {
				j++
			}
			emit(rtSym, src[i+1:j])
			i = j
		case c == '<' && i+2 < n && b[i+1] == '<' && heredocRe.Match(b[i:]):
			m := heredocRe.FindSubmatch(b[i:])
			heredocs = append(heredocs, string(m[3]))
			emit(rtStr, "")
			i += len(m[0])
		case c == '@' || c == '$':
			j := i + 1
			for j < n && (isWordByte(b[j]) || b[j] == '@') {
				j++
			}
			emit(rtIvar, src[i:j])
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < n && (isWordByte(b[j]) || b[j] == '.' && j+1 < n && b[j+1] >= '0' && b[j+1] <= '9') {
				j++
			}
			emit(rtNum, src[i:j])
			i = j
		case isWordByte(c):
			j := i
			for j < n && isWordByte(b[j]) {
				j++
			}
			if j < n && (b[j] == '?' || b[j] == '!') && (j+1 >= n || b[j+1] != '=') {
				j++
			}
			word := src[i:j]
			if j < n && b[j] == ':' && (j+1 >= n || b[j+1] != ':') {
				emit(rtLabel, word)
				i = j + 1
				continue
			}
			emit(rtIdent, word)
			i = j
		default:
			op := string(c)
			for _, o := range []string{"=>", "->", "::", "||", "&&", "==", "!=", "<=", ">=", "**", "&.", "+=", "-=", "||=", "<<", "..."} {
				if strings.HasPrefix(src[i:], o) && len(o) > len(op) {
					op = o
				}
			}
			emit(rtOp, op)
			i += len(op)
		}
	}
	emit(rtNL, "")
	return out
}

// rtStringEnd is the index of the quote closing a string that starts at
// i (just past the opening quote), skipping escapes and, in a "…" string,
// #{…} interpolation.
func rtStringEnd(b []byte, i int, q byte) int {
	for i < len(b) {
		switch {
		case b[i] == '\\':
			i += 2
			continue
		case b[i] == q:
			return i
		case q == '"' && b[i] == '#' && i+1 < len(b) && b[i+1] == '{':
			depth := 0
			for i < len(b) {
				if b[i] == '{' {
					depth++
				} else if b[i] == '}' {
					depth--
					if depth == 0 {
						break
					}
				}
				i++
			}
		}
		i++
	}
	return len(b)
}

// --- Statements ---

// rtVal is an argument's value.
type rtVal struct {
	kind byte // 's' string, 'y' symbol, 'a' array, 'h' hash, 'b' true, 'f' false, 'n' nil, 'c' constant, 'i' local name, 'x' other
	s    string
	list []rtVal
	hash []rtPair
}

type rtPair struct{ key, val rtVal }

// rtStmt is one statement: a method call with its arguments and block.
type rtStmt struct {
	name     string
	recv     string // the receiver chain as written ("Rails.application.routes"), "" for none
	recvVal  rtVal  // a literal receiver (%w[a b].each)
	args     []rtVal
	opts     map[string]rtVal
	pairs    []rtPair // "path" => "controller#action"
	block    []*rtStmt
	hasBlock bool
	params   []string // the block's |params|
	assign   bool     // x = …, not a call
	line     int
}

type rtParser struct {
	t []rtTok
	i int
}

func (p *rtParser) peek() rtTok {
	if p.i < len(p.t) {
		return p.t[p.i]
	}
	return rtTok{kind: rtNL}
}

func (p *rtParser) next() rtTok {
	t := p.peek()
	if p.i < len(p.t) {
		p.i++
	}
	return t
}

func (p *rtParser) is(kind rtTokKind, text string) bool {
	t := p.peek()
	return p.i < len(p.t) && t.kind == kind && t.text == text
}

func (p *rtParser) skipNL() {
	for p.i < len(p.t) && (p.peek().kind == rtNL || p.is(rtOp, ";")) {
		p.i++
	}
}

// rtBlockKeywords open a block that takes an end at the start of a statement.
var rtBlockKeywords = map[string]bool{"if": true, "unless": true, "while": true, "until": true, "case": true, "begin": true, "for": true}

// stmts parses statements up to an "end" or "}" (left for the caller) or
// the end of input.
func (p *rtParser) stmts(brace bool) []*rtStmt {
	var out []*rtStmt
	for {
		p.skipNL()
		if p.i >= len(p.t) {
			return out
		}
		t := p.peek()
		if t.kind == rtIdent && t.text == "end" || brace && p.is(rtOp, "}") {
			return out
		}
		if !brace && p.is(rtOp, "}") {
			p.i++ // stray: keep going
			continue
		}
		if t.kind == rtIdent && (t.text == "else" || t.text == "elsif" || t.text == "when" || t.text == "rescue" || t.text == "ensure" || t.text == "in") {
			p.skipLine()
			continue
		}
		start := p.i
		if st := p.stmt(); st != nil {
			out = append(out, st)
		}
		if p.i == start {
			p.i++ // never stall
		}
	}
}

// skipLine skips to the end of the line (a condition, a separator).
func (p *rtParser) skipLine() {
	for p.i < len(p.t) && p.peek().kind != rtNL && !p.is(rtOp, ";") {
		p.i++
	}
}

// stmt parses one statement.
func (p *rtParser) stmt() *rtStmt {
	t := p.peek()
	st := &rtStmt{line: t.line, opts: map[string]rtVal{}}
	if t.kind == rtIdent && rtBlockKeywords[t.text] {
		p.i++
		st.name = t.text
		p.skipLine()
		st.block, st.hasBlock = p.stmts(false), true
		if p.is(rtIdent, "end") {
			p.i++
		}
		return st
	}
	// The head: a name, or a receiver chain ending in one.
	switch t.kind {
	case rtIdent, rtIvar:
		var chain []string
		for {
			tk := p.next()
			chain = append(chain, tk.text)
			if p.is(rtOp, ".") || p.is(rtOp, "::") || p.is(rtOp, "&.") {
				sep := p.next().text
				if p.peek().kind != rtIdent {
					break
				}
				chain[len(chain)-1] += sep
				continue
			}
			break
		}
		full := strings.Join(chain, "")
		if i := strings.LastIndexAny(full, ".:"); i >= 0 {
			st.recv, st.name = strings.TrimRight(full[:i+1], ".:&"), full[i+1:]
		} else {
			st.name = full
		}
		if t.kind == rtIvar {
			st.recv, st.name = full, ""
		}
	case rtWords, rtOp:
		// A literal receiver: %w[a b].each do |x| … end.
		if t.kind == rtWords || p.is(rtOp, "[") {
			v := p.primary()
			if p.is(rtOp, ".") {
				p.i++
				if p.peek().kind == rtIdent {
					st.recvVal, st.name = v, p.next().text
					break
				}
			}
		}
		p.skipStatement()
		return nil
	default:
		p.skipStatement()
		return nil
	}
	if p.peek().kind == rtOp && (p.peek().text == "=" || p.peek().text == "||=" || p.peek().text == "+=" || p.peek().text == "-=") {
		p.i++
		st.assign = true
		p.skipStatement()
		return st
	}
	// Arguments: in parentheses right after the name, or up to the end of
	// the statement.
	if p.is(rtOp, "(") && !p.peek().space {
		p.i++
		p.args(st, true)
		if p.is(rtOp, ")") {
			p.i++
		}
	} else if p.startsValue() {
		p.args(st, false)
	}
	p.block(st)
	// A modifier (get "x" => "y#z" if dev?) or trailing call: to the end.
	if t := p.peek(); t.kind != rtNL && !p.is(rtOp, ";") && !p.is(rtIdent, "end") && !p.is(rtOp, "}") {
		p.skipStatement()
	}
	return st
}

// block parses a do … end or { … } block after a call.
func (p *rtParser) block(st *rtStmt) {
	brace := p.is(rtOp, "{")
	if !brace && !p.is(rtIdent, "do") {
		return
	}
	p.i++
	if p.is(rtOp, "|") {
		p.i++
		for p.i < len(p.t) && !p.is(rtOp, "|") && p.peek().kind != rtNL {
			if t := p.next(); t.kind == rtIdent {
				st.params = append(st.params, t.text)
			}
		}
		if p.is(rtOp, "|") {
			p.i++
		}
	}
	st.block, st.hasBlock = p.stmts(brace), true
	if brace && p.is(rtOp, "}") || !brace && p.is(rtIdent, "end") {
		p.i++
	}
}

// startsValue reports whether the next token can begin an argument (so
// "get :x" has arguments and "member do" doesn't).
func (p *rtParser) startsValue() bool {
	t := p.peek()
	switch t.kind {
	case rtSym, rtLabel, rtStr, rtWords, rtRegex, rtNum, rtIvar:
		return true
	case rtIdent:
		switch t.text {
		case "do", "end", "if", "unless", "and", "or", "then", "rescue":
			return false
		}
		return true
	case rtOp:
		return t.text == "[" || t.text == "->" || t.text == "(" && t.space || t.text == "::" || t.text == "!" || t.text == "*" || t.text == "-" && !p.nextSpace()
	}
	return false
}

func (p *rtParser) nextSpace() bool {
	return p.i+1 < len(p.t) && p.t[p.i+1].space
}

// args parses a call's arguments into st: positional values, label and
// symbol-rocket options, and "string" => value pairs.
func (p *rtParser) args(st *rtStmt, paren bool) {
	for {
		if paren {
			p.skipNL()
		}
		t := p.peek()
		if t.kind == rtNL || p.is(rtOp, ")") || p.is(rtIdent, "do") || p.is(rtOp, ";") || p.i >= len(p.t) {
			return
		}
		if t.kind == rtLabel {
			p.i++
			p.skipNL()
			st.opts[t.text] = p.value()
		} else {
			if p.is(rtOp, "*") || p.is(rtOp, "&") {
				p.i++ // a splat
			}
			v := p.value()
			if p.is(rtOp, "=>") {
				p.i++
				p.skipNL()
				val := p.value()
				if v.kind == 'y' {
					st.opts[v.s] = val
				} else {
					st.pairs = append(st.pairs, rtPair{v, val})
				}
			} else {
				st.args = append(st.args, v)
			}
		}
		if !p.is(rtOp, ",") {
			if paren && !p.is(rtOp, ")") && p.peek().kind != rtNL {
				p.skipTo(")")
			}
			return
		}
		p.i++
		p.skipNL()
	}
}

// skipTo skips to the given closer at this depth (not consuming it).
func (p *rtParser) skipTo(closer string) {
	depth := 0
	for p.i < len(p.t) {
		t := p.peek()
		if t.kind == rtOp {
			switch t.text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth == 0 && t.text == closer {
					return
				}
				depth--
			}
		}
		if t.kind == rtIdent && t.text == "do" {
			depth++
		}
		if t.kind == rtIdent && t.text == "end" {
			depth--
		}
		p.i++
	}
}

// skipStatement skips to the end of the statement: a newline outside
// brackets and blocks that doesn't follow a comma or an operator.
func (p *rtParser) skipStatement() {
	depth := 0
	for p.i < len(p.t) {
		t := p.peek()
		switch {
		case t.kind == rtNL || t.kind == rtOp && t.text == ";":
			if depth <= 0 && !p.continues() {
				return
			}
		case t.kind == rtOp && (t.text == "(" || t.text == "[" || t.text == "{"):
			depth++
		case t.kind == rtOp && (t.text == ")" || t.text == "]" || t.text == "}"):
			if depth == 0 {
				return
			}
			depth--
		case t.kind == rtIdent && (t.text == "do" || rtBlockKeywords[t.text] && p.i > 0 && p.t[p.i-1].kind == rtNL):
			depth++
		case t.kind == rtIdent && t.text == "end":
			if depth == 0 {
				return
			}
			depth--
		}
		p.i++
	}
}

// continues reports whether the statement goes on past the newline at
// p.i: the token before it is a comma, an operator or an opener.
func (p *rtParser) continues() bool {
	if p.i == 0 {
		return false
	}
	prev := p.t[p.i-1]
	if prev.kind != rtOp {
		return false
	}
	switch prev.text {
	case ")", "]", "}":
		return false
	}
	return true
}

// value parses one value, with whatever operators and calls follow it.
func (p *rtParser) value() rtVal {
	v := p.primary()
	for {
		t := p.peek()
		if t.kind != rtOp {
			return v
		}
		switch t.text {
		case "::":
			p.i++
			if n := p.peek(); n.kind == rtIdent {
				p.i++
				if v.kind == 'c' && n.text[0] >= 'A' && n.text[0] <= 'Z' {
					v.s += "::" + n.text
					continue
				}
			}
			v = rtVal{kind: 'x', s: v.s}
		case ".", "&.":
			p.i++
			if n := p.peek(); n.kind == rtIdent {
				p.i++
				if p.is(rtOp, "(") && !p.peek().space {
					p.i++
					p.skipTo(")")
					p.i++
				}
				if p.is(rtOp, "{") {
					p.i++
					p.skipTo("}")
					p.i++
				}
			}
			v = rtVal{kind: 'x', s: v.s}
		case "||", "&&", "+", "-", "*", "/", "==", "!=", "?", "%", "<<", "..", "...", "<", ">", "<=", ">=":
			p.i++
			p.skipNL()
			p.primary()
			v = rtVal{kind: 'x', s: v.s}
		case ":":
			p.i++ // the else of a ternary
			p.primary()
			v = rtVal{kind: 'x', s: v.s}
		default:
			return v
		}
	}
}

// primary parses a literal, a name (or call), a list, a hash or a lambda.
func (p *rtParser) primary() rtVal {
	t := p.next()
	switch t.kind {
	case rtStr:
		for p.peek().kind == rtStr && p.peek().space { // "a" "b"
			t.text += p.next().text
		}
		return rtVal{kind: 's', s: t.text}
	case rtSym:
		return rtVal{kind: 'y', s: t.text}
	case rtLabel:
		return rtVal{kind: 'x', s: t.text}
	case rtWords:
		v := rtVal{kind: 'a'}
		for _, w := range t.words {
			k := byte('s')
			if t.sym {
				k = 'y'
			}
			v.list = append(v.list, rtVal{kind: k, s: w})
		}
		return v
	case rtRegex:
		return rtVal{kind: 'r', s: t.text}
	case rtNum, rtIvar:
		return rtVal{kind: 'x', s: t.text}
	case rtIdent:
		switch t.text {
		case "true":
			return rtVal{kind: 'b'}
		case "false":
			return rtVal{kind: 'f'}
		case "nil":
			return rtVal{kind: 'n'}
		case "lambda", "proc":
			p.lambdaBody()
			return rtVal{kind: 'x', s: t.text}
		}
		if t.text[0] >= 'A' && t.text[0] <= 'Z' {
			return rtVal{kind: 'c', s: t.text}
		}
		v := rtVal{kind: 'i', s: t.text}
		if p.is(rtOp, "(") && !p.peek().space {
			p.i++
			p.skipTo(")")
			p.i++
			v.kind = 'x'
		}
		if p.is(rtOp, "{") {
			p.i++
			p.skipTo("}")
			p.i++
			v.kind = 'x'
		}
		return v
	case rtOp:
		switch t.text {
		case "[":
			v := rtVal{kind: 'a'}
			for {
				p.skipNL()
				if p.is(rtOp, "]") || p.i >= len(p.t) {
					p.i++
					return v
				}
				start := p.i
				v.list = append(v.list, p.value())
				p.skipNL()
				if p.is(rtOp, ",") {
					p.i++
				} else if !p.is(rtOp, "]") {
					p.skipTo("]")
				}
				if p.i == start {
					p.i++
				}
			}
		case "{":
			v := rtVal{kind: 'h'}
			for {
				p.skipNL()
				if p.is(rtOp, "}") || p.i >= len(p.t) {
					p.i++
					return v
				}
				start := p.i
				var key rtVal
				if k := p.peek(); k.kind == rtLabel {
					p.i++
					key = rtVal{kind: 'y', s: k.text}
				} else {
					key = p.value()
					if p.is(rtOp, "=>") {
						p.i++
					}
				}
				p.skipNL()
				v.hash = append(v.hash, rtPair{key, p.value()})
				p.skipNL()
				if p.is(rtOp, ",") {
					p.i++
				} else if !p.is(rtOp, "}") {
					p.skipTo("}")
				}
				if p.i == start {
					p.i++
				}
			}
		case "->":
			if p.is(rtOp, "(") {
				p.i++
				p.skipTo(")")
				p.i++
			}
			p.lambdaBody()
			return rtVal{kind: 'x', s: "lambda"}
		case "(":
			p.skipTo(")")
			p.i++
			return rtVal{kind: 'x'}
		case "!", "-", "*", "::":
			return p.primary()
		}
	}
	return rtVal{kind: 'x'}
}

// lambdaBody skips a { … } or do … end body.
func (p *rtParser) lambdaBody() {
	switch {
	case p.is(rtOp, "{"):
		p.i++
		p.skipTo("}")
		p.i++
	case p.is(rtIdent, "do"):
		p.i++
		depth := 1
		for p.i < len(p.t) && depth > 0 {
			t := p.next()
			if t.kind != rtIdent {
				continue
			}
			switch {
			case t.text == "do":
				depth++
			case rtBlockKeywords[t.text] && p.i >= 2 && p.t[p.i-2].kind == rtNL:
				depth++
			case t.text == "end":
				depth--
			}
		}
	}
}

// --- Evaluation ---

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
}

// file evaluates one routes file's statements in scope sc.
func (e *rtEval) file(p, src string, sc rtScope) {
	prevFile, prevLines := e.cur, e.lines
	e.cur, e.lines = p, strings.Split(src, "\n")
	ps := &rtParser{t: rtTokens(src)}
	e.stmts(ps.stmts(false), sc)
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
			if !ok {
				e.skip(st)
				continue
			}
			prev := e.cur
			e.cur = c.file
			e.stmts(c.body, sc)
			e.cur = prev
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
