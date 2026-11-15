package review

import "strings"

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
	// depth counts the nesting of blocks and values being parsed. Past
	// rtMaxNesting the rest of the file is dropped (tooDeep): routes are
	// repository content, and an unbounded recursive descent over a file of
	// a million "[" is a stack overflow, which no recover catches.
	depth   int
	tooDeep bool
}

// rtMaxNesting is far deeper than any real routes file nests.
const rtMaxNesting = 200

// enter counts one more level of nesting; false (and the rest of the input
// dropped) past rtMaxNesting. Each enter that returns true is paired with a
// leave.
func (p *rtParser) enter() bool {
	if p.depth >= rtMaxNesting {
		p.tooDeep = true
		p.i = len(p.t)
		return false
	}
	p.depth++
	return true
}

func (p *rtParser) leave() { p.depth-- }

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
	if !p.enter() {
		return out
	}
	defer p.leave()
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
	if !p.enter() {
		return rtVal{kind: 'x'}
	}
	defer p.leave()
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
	if !p.enter() {
		return rtVal{kind: 'x'}
	}
	defer p.leave()
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
