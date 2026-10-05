package review

import (
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Rails conventions for the Ruby call graph, scanner side: receiver
// chains as expressions (rbParseChain), the methods model macros generate
// (rbMacroDefs), test blocks, and the calls Rails makes by convention
// (rbRailsCalls: render, Pundit, functional tests, factories). Resolution
// side: resolveRails.

var (
	rbConstHeadRe  = regexp.MustCompile(`^(?:::)?[A-Z]\w*(?:::[A-Z]\w*)*`)
	rbIvarAsgnRe   = regexp.MustCompile(`(?:^|[^\w@])@([a-z_]\w*)\s*(?:\|\|)?=(?:[^=~>]|$)`)
	rbRenderRe     = regexp.MustCompile(`(?:^|[^\w.:@])render\b\s*\(?\s*`)
	rbPartialRe    = regexp.MustCompile(`\bpartial:\s*`)
	rbJPartialRe   = regexp.MustCompile(`\bjson\.partial!\s*\(?\s*`)
	rbAuthRe       = regexp.MustCompile(`(?:^|[^\w.:@])authorize\b\s*\(?\s*`)
	rbPScopeRe     = regexp.MustCompile(`(?:^|[^\w.:@])policy_scope\s*\(\s*`)
	rbFuncTestRe   = regexp.MustCompile(`^\s*(?:get|post|put|patch|delete|head)\s*\(?\s*:([a-z_]\w*)`)
	rbFactoryRe    = regexp.MustCompile(`^\s*factory\s*\(?\s*:([a-z_]\w*)`)
	rbFactoryUseRe = regexp.MustCompile(`(?:^|[^\w.:@])(?:create|build|build_stubbed|create_list|build_list|create_pair|build_pair|attributes_for)\s*\(?\s*:([a-z_]\w*)`)
	rbBroadcastRe  = regexp.MustCompile(`^broadcasts(?:_to)?\b`)
	rbLabelAtRe    = regexp.MustCompile(`^([a-z_]\w*):(?:\s|$)`)
	rbSymAtRe      = regexp.MustCompile(`^:([A-Za-z_]\w*[?!]?)`)
	rbQueryRe      = regexp.MustCompile(`^\s*,\s*:([A-Za-z_]\w*[?!]?)`)
	rbRendersRe    = regexp.MustCompile(`(?:^|[^\w.:@])(render|redirect_to|redirect_back|redirect_back_or_to|head|send_data|send_file|respond_with)\b`)
	rbRespondRe    = regexp.MustCompile(`(?:^|[^\w.:@])respond_to\s+do\b`)
	rbTestNameRe   = regexp.MustCompile(`^(test|it|specify|scenario)\s*\(?\s*(["'])(.*?)["']`)
	rbSetupRe      = regexp.MustCompile(`^(setup|teardown)\s+do\b`)

	rbAssocRe    = regexp.MustCompile(`^(belongs_to|has_one|has_many|has_and_belongs_to_many)\s*\(?\s*:(\w+)`)
	rbAttachRe   = regexp.MustCompile(`^has_(?:one|many)_attached\s*\(?\s*:(\w+)`)
	rbEnumRe     = regexp.MustCompile(`^enum\b\s*\(?\s*`)
	rbDelegateRe = regexp.MustCompile(`^delegate\b\s*\(?\s*`)
	rbEventRe    = regexp.MustCompile(`^event\s*\(?\s*:(\w+)`)
	rbStateRe    = regexp.MustCompile(`^state\s*\(?\s*:\w+`)
	rbMacroRe    = regexp.MustCompile(`^(?:belongs_to|has_one|has_many|has_and_belongs_to_many|has_one_attached|has_many_attached|enum|delegate)\b`)
	rbThroughRe  = regexp.MustCompile(`\bthrough:\s*:(\w+)`)
	rbSourceRe   = regexp.MustCompile(`\bsource:\s*:(\w+)`)
	rbToRe       = regexp.MustCompile(`\bto:\s*:(@?\w+)`)
	rbPrefixRe   = regexp.MustCompile(`\b(_?prefix|_?suffix):\s*(?:(true)|:(\w+))`)
)

// rbIterators pass a collection's elements to their block.
var rbIterators = rbSet(`each each_with_index each_with_object map flat_map filter_map collect select filter reject find detect find_each sort_by group_by min_by max_by sum partition index_by any? all? none? count each_entry`)

// rbAASMLabels are the AASM options naming methods to run.
var rbAASMLabels = rbSet(`after before guard guards success error after_commit after_enter before_enter after_exit before_exit enter exit if unless`)

// rbViewExts are the template extensions a missing template is wanted
// under, so a removed one still rendered is found.
var rbViewExts = []string{".html.erb", ".html.haml", ".html.slim", ".erb", ".haml", ".slim", ".json.jbuilder", ".jbuilder", ".turbo_stream.erb", ".text.erb"}

func rbIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' }

func rbSkipSpaces(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// rbReadIdent reads a method name from s[j:] (with a trailing ? or !).
func rbReadIdent(s string, j int) int {
	for j < len(s) && isWordByte(s[j]) {
		j++
	}
	if j < len(s) && (s[j] == '?' || s[j] == '!') && !(j+1 < len(s) && s[j+1] == '=') {
		j++
	}
	return j
}

func rbDoAt(s string, k int) bool {
	return strings.HasPrefix(s[k:], "do") && (k+2 == len(s) || !isWordByte(s[k+2])) && (k == 0 || !isWordByte(s[k-1]))
}

// rbBlockParam is the first parameter of the block opening at s[k] ("{"
// or "do"), "" if it has none.
func rbBlockParam(s string, k int) string {
	j := k + 1
	if s[k] != '{' {
		j = k + 2
	}
	j = rbSkipSpaces(s, j)
	if j >= len(s) || s[j] != '|' {
		return ""
	}
	j = rbSkipSpaces(s, j+1)
	for j < len(s) && (s[j] == '*' || s[j] == '&' || s[j] == '(') {
		j++
	}
	e := j
	for e < len(s) && isWordByte(s[e]) {
		e++
	}
	if e == j || !rbIdentStart(s[j]) {
		return ""
	}
	return s[j:e]
}

// rbMaxSteps caps a chain's length: past it, it's unknown.
const rbMaxSteps = 8

// rbParseChain reads the receiver chain starting at s[i:] as an expression
// the resolver types: "Ticket.find", "@ticket.event", "self.items.[]".
// Arguments and brace blocks are skipped, [i] is the step "[]", a local is
// replaced by what it holds (unknown if nothing typed), and a bare call is
// "self.name". bind (if set) is told the element type of a block parameter
// whose iterator's receiver is known. It returns "" for an unknown chain,
// and where the chain ends.
func rbParseChain(s string, i int, locals map[string]string, bind func(name, typ string)) (string, int) {
	n := len(s)
	i = rbSkipSpaces(s, i)
	if i >= n {
		return "", i
	}
	expr, known := "", true
	switch c := s[i]; {
	case c == '@':
		if i+1 >= n || !rbIdentStart(s[i+1]) {
			return "", i + 1
		}
		j := rbReadIdent(s, i+1)
		expr, i = s[i:j], j
	case c >= 'A' && c <= 'Z' || strings.HasPrefix(s[i:], "::"):
		m := rbConstHeadRe.FindString(s[i:])
		if m == "" {
			return "", i + 1
		}
		expr, i = m, i+len(m)
	case rbIdentStart(c):
		j := rbReadIdent(s, i)
		id := s[i:j]
		paren := j < n && s[j] == '('
		switch t, local := locals[id]; {
		case id == "self":
			expr = "self"
		case rbKeywords[id] || rbSkip[id] || rbSendNames[id]:
			known = false
		case id == "policy_scope" && paren:
			// policy_scope(Ticket) is a relation of tickets.
			cl := matchingParen(s, j)
			if cl < 0 {
				return "", n
			}
			if inner, _ := rbParseChain(s[:cl], j+1, locals, nil); inner != "" {
				expr = inner + ".all"
			} else {
				known = false
			}
			j = cl + 1
		case local && !paren:
			expr, known = t, t != ""
		default:
			expr = "self." + id
			if e, end := rbFactoryExpr(s, id, j); e != "" {
				expr, j = e, end
			}
		}
		i = j
	default:
		return "", i
	}
	prev, last, steps := "", "", 0
	step := func(name string) {
		if known {
			prev, expr = expr, expr+"."+name
		}
		last = name
		steps++
	}
	for {
		if i < n && (s[i] == '(' || s[i] == '[') {
			cl := matchingParen(s, i)
			if cl < 0 {
				// The arguments run onto the next lines: the call's value.
				if s[i] == '[' {
					return "", n
				}
				i = n
				break
			}
			if s[i] == '[' {
				step("[]")
			}
			i = cl + 1
			continue
		}
		k := rbSkipSpaces(s, i)
		if k < n && (s[k] == '{' || rbDoAt(s, k)) {
			if bind != nil && known && prev != "" && rbIterators[last] {
				if p := rbBlockParam(s, k); p != "" {
					bind(p, prev+".[]")
				}
			}
			if s[k] != '{' {
				i = k
				break
			}
			cl := matchingParen(s, k)
			if cl < 0 {
				i = n
				break
			}
			i = cl + 1
			continue
		}
		dot := -1
		switch {
		case k < n && s[k] == '.' && !(k+1 < n && s[k+1] == '.'):
			dot = k + 1
		case k+1 < n && s[k] == '&' && s[k+1] == '.':
			dot = k + 2
		}
		if dot < 0 {
			break
		}
		d := rbSkipSpaces(s, dot)
		if d >= n || !(rbIdentStart(s[d]) || s[d] >= 'A' && s[d] <= 'Z') {
			break
		}
		j := rbReadIdent(s, d)
		step(s[d:j])
		i = j
	}
	if !known || steps > rbMaxSteps {
		return "", i
	}
	return expr, i
}

// rbFactoryBuilds are FactoryBot's methods returning a record ('i') or a
// list of them ('c').
var rbFactoryBuilds = map[string]byte{"create": 'i', "build": 'i', "build_stubbed": 'i',
	"create_list": 'c', "build_list": 'c', "build_stubbed_list": 'c', "create_pair": 'c', "build_pair": 'c'}

// rbFactoryExpr reads a FactoryBot call whose name id ends at s[j]
// (create(:ticket, …), create :ticket): the expression "factory:ticket"
// ("factories:ticket" for a list) and where the call ends.
func rbFactoryExpr(s, id string, j int) (string, int) {
	kind, ok := rbFactoryBuilds[id]
	if !ok || j >= len(s) || (s[j] != '(' && s[j] != ' ') {
		return "", j
	}
	end := len(s)
	if s[j] == '(' {
		if cl := matchingParen(s, j); cl > j {
			end = cl + 1
		}
	}
	sm := rbSymAtRe.FindStringSubmatch(s[rbSkipSpaces(s, j+1):])
	if sm == nil {
		return "", j
	}
	if kind == 'c' {
		return "factories:" + sm[1], end
	}
	return "factory:" + sm[1], end
}

// rbExprAt is the expression assigned at s[i:], when the chain is all
// there is (up to a modifier): x = Ticket.find(id) types x, x = a + b
// doesn't.
func rbExprAt(s string, i int, locals map[string]string) string {
	e, end := rbParseChain(s, i, locals, nil)
	if e == "" {
		return ""
	}
	rest := strings.TrimSpace(s[end:])
	for _, ok := range []string{"", ";", ")", "if ", "unless ", "||", "rescue", "while ", "until "} {
		if rest == ok || ok != "" && strings.HasPrefix(rest, ok) {
			return e
		}
	}
	// A call's arguments without parentheses (x = Foo.new a, b): its value.
	if end < len(s) && s[end] == ' ' && strings.Contains(e, ".") {
		c := rest[0]
		w := rest
		if i := strings.IndexAny(w, " (,"); i >= 0 {
			w = w[:i]
		}
		if (isWordByte(c) || c == '@' || c == ':' || c == '"' || c == '\'') && !rbKeywords[w] && w != "do" {
			return e
		}
	}
	return ""
}

// rbBindBlocks types the block parameters of iterators on a line from
// their receivers: @tickets.each do |t| makes t a ticket.
func rbBindBlocks(line string, locals map[string]string) {
	if !strings.Contains(line, "|") {
		return
	}
	for _, m := range rbChainRe.FindAllStringIndex(line, -1) {
		s := m[0]
		if s > 0 && line[s-1] == '@' {
			s--
		}
		if s > 0 {
			if pc := line[s-1]; isWordByte(pc) || pc == '.' || pc == '@' || pc == '$' || (pc == ':' && line[s] != ':') {
				continue
			}
		}
		if j := lastNonSpace(line[:s]); j >= 0 && line[j] == '.' {
			continue
		}
		rbParseChain(line, s, locals, func(name, typ string) { locals[name] = typ })
	}
}

// rbIvarCalls are the instance variable assignments on a line of a
// method, as rbIvarRecv pseudo-calls carrying the assigned expression.
func rbIvarCalls(line string, lineNo int, locals map[string]string) []hCall {
	var out []hCall
	if !strings.Contains(line, "@") {
		return nil
	}
	for _, m := range rbIvarAsgnRe.FindAllStringSubmatchIndex(line, -1) {
		eq := strings.IndexByte(line[m[3]:], '=') + m[3] + 1
		if e := rbExprAt(line, eq, locals); e != "" {
			out = append(out, hCall{Recv: rbIvarRecv + "@" + line[m[2]:m[3]], Name: e, Line: lineNo, Ref: true})
		}
	}
	return out
}

// rbFind is re's matches in s, skipping the regexp when s lacks word.
func rbFind(re *regexp.Regexp, s, word string) [][]int {
	if !strings.Contains(s, word) {
		return nil
	}
	return re.FindAllStringIndex(s, -1)
}

// rbRenders reports whether a line renders, redirects or sends a response
// itself (so its action has no implicit template).
func rbRenders(s string) bool {
	for _, w := range []string{"render", "redirect", "head", "send_", "respond_with"} {
		if strings.Contains(s, w) {
			return rbRendersRe.MatchString(s)
		}
	}
	return false
}

// rbFactoryUses are the factory names a line builds with FactoryBot
// (create(:ticket), build_list(:ticket, 2)).
func rbFactoryUses(s string) [][]string {
	if !strings.Contains(s, "create") && !strings.Contains(s, "build") && !strings.Contains(s, "attributes_for") {
		return nil
	}
	return rbFactoryUseRe.FindAllStringSubmatch(s, -1)
}

// rbRawAt is where an argument starts in the source line, for a regexp
// match on code ending at end: the match's trailing spaces may be a
// blanked string.
func rbRawAt(code, raw string, end int) int {
	for end > 0 && code[end-1] == ' ' {
		end--
	}
	return rbSkipSpaces(raw, end)
}

// rbStringAt reads a plain string literal starting at s[i] (from the
// source line: strings are blanked in code).
func rbStringAt(s string, i int) (string, bool) {
	if i >= len(s) || (s[i] != '"' && s[i] != '\'') {
		return "", false
	}
	j := strings.IndexByte(s[i+1:], s[i])
	if j < 0 {
		return "", false
	}
	v := s[i+1 : i+1+j]
	if v == "" || strings.Contains(v, "#{") || strings.Contains(v, "\\") {
		return "", false
	}
	return v, true
}

// rbRailsCalls finds the calls Rails makes by convention on a line: raw
// is the source line and code the scanned one (same offsets). action is
// the public method the line is in ("" elsewhere), for Pundit's default
// query.
func rbRailsCalls(raw, code string, lineNo int, locals map[string]string, action string) []hCall {
	var out []hCall
	call := func(recv, name string) { out = append(out, hCall{Recv: recv, Name: name, Line: lineNo}) }
	for _, m := range rbFind(rbRenderRe, code, "render") {
		at := m[1]
		if s, ok := rbStringAt(raw, rbRawAt(code, raw, at)); ok {
			call(rbRenderRecv, "s:"+s)
			continue
		}
		if sm := rbSymAtRe.FindStringSubmatch(code[at:]); sm != nil {
			call(rbRenderRecv, "a:"+sm[1])
			continue
		}
		if lm := rbLabelAtRe.FindStringSubmatch(code[at:]); lm != nil {
			v := rbSkipSpaces(code, at+len(lm[1])+1)
			rv := rbSkipSpaces(raw, at+len(lm[1])+1)
			switch lm[1] {
			case "template":
				if s, ok := rbStringAt(raw, rv); ok {
					call(rbRenderRecv, "t:"+s)
				}
			case "action":
				if s, ok := rbStringAt(raw, rv); ok {
					call(rbRenderRecv, "a:"+s)
				} else if sm := rbSymAtRe.FindStringSubmatch(code[v:]); sm != nil {
					call(rbRenderRecv, "a:"+sm[1])
				}
			case "collection":
				if !strings.Contains(code, "partial:") {
					if e, _ := rbParseChain(code, v, locals, nil); e != "" {
						call(rbRenderRecv, "e:"+e)
					}
				}
			}
			continue
		}
		if e, _ := rbParseChain(code, at, locals, nil); e != "" && !strings.HasSuffix(e, ".new") && !strings.HasSuffix(e, ".with_collection") {
			call(rbRenderRecv, "e:"+e)
		}
	}
	for _, m := range rbFind(rbPartialRe, code, "partial:") {
		if m[0] > 0 && (isWordByte(code[m[0]-1]) || code[m[0]-1] == ':') {
			continue
		}
		if s, ok := rbStringAt(raw, rbRawAt(code, raw, m[1])); ok {
			call(rbRenderRecv, "p:"+s)
		}
	}
	for _, m := range rbFind(rbJPartialRe, code, "json.partial!") {
		if s, ok := rbStringAt(raw, rbRawAt(code, raw, m[1])); ok {
			call(rbRenderRecv, "p:"+s)
		}
	}
	for _, m := range rbFind(rbAuthRe, code, "authorize") {
		at := m[1]
		expr, end := "", at
		if sm := rbSymAtRe.FindStringSubmatch(code[at:]); sm != nil {
			expr, end = ":"+sm[1], at+len(sm[0])
		} else {
			expr, end = rbParseChain(code, at, locals, nil)
		}
		q := ""
		if action != "" {
			q = action + "?"
		}
		if qm := rbQueryRe.FindStringSubmatch(code[end:]); qm != nil {
			q = qm[1]
		}
		if expr != "" && q != "" {
			call(rbAuthRecv+expr, "<"+q)
		}
	}
	for _, m := range rbFind(rbPScopeRe, code, "policy_scope") {
		if e, _ := rbParseChain(code, m[1], locals, nil); e != "" {
			call(rbScopeRecv+e, "<resolve")
		}
	}
	if sm := rbFuncTestRe.FindStringSubmatch(code); sm != nil {
		call(rbActionRecv, "<"+sm[1])
	}
	if sm := rbFactoryRe.FindStringSubmatch(code); sm != nil {
		cls := rbCamelize(sm[1])
		if i := strings.Index(code, "class:"); i >= 0 {
			v := rbSkipSpaces(code, i+len("class:"))
			if s, ok := rbStringAt(raw, rbSkipSpaces(raw, i+len("class:"))); ok {
				cls = s
			} else if c := rbConstHeadRe.FindString(code[v:]); c != "" {
				cls = c
			} else if sym := rbSymAtRe.FindStringSubmatch(code[v:]); sym != nil {
				cls = rbCamelize(sym[1])
			}
		}
		out = append(out, hCall{Recv: rbFactoryRecv + sm[1], Name: strings.TrimPrefix(cls, "::"), Line: lineNo, Ref: true})
	}
	for _, m := range rbFactoryUses(code) {
		// create(:ticket): the factory's model.
		out = append(out, hCall{Recv: rbFactoryRecv, Name: "<" + m[1], Line: lineNo, Ref: true})
	}
	if rbBroadcastRe.MatchString(strings.TrimSpace(code)) {
		call(rbRenderRecv, "m:")
	}
	return out
}

// rbTestBlockName is the definition name of a test block opening on a
// line (test "x" do, it "x" do, setup do), "" for any other line.
func rbTestBlockName(trim string) string {
	if m := rbTestNameRe.FindStringSubmatch(trim); m != nil && m[3] != "" {
		return m[1] + " " + m[2] + m[3] + m[2]
	}
	if m := rbSetupRe.FindStringSubmatch(trim); m != nil {
		return m[1]
	}
	return ""
}

// rbIsTestBlockName reports whether a definition is a test block.
func rbIsTestBlockName(n string) bool {
	if n == "setup" || n == "teardown" {
		return true
	}
	for _, k := range []string{"test ", "it ", "specify ", "scenario "} {
		if strings.HasPrefix(n, k) && len(n) > len(k) && strings.ContainsAny(n[len(k):len(k)+1], `"'`) {
			return true
		}
	}
	return false
}

// rbSynDef is a synthetic definition on lines i..end of a class body.
func rbSynDef(raw []string, i, end int, owner, name, bodyKey string) hDef {
	var own []int
	for k := i; k <= end; k++ {
		own = append(own, k)
	}
	body := hLinesBody(raw, own, rbCommentRe)
	if bodyKey != "" {
		body = hashOf(bodyKey)
	}
	return hDef{Name: name, Owner: owner, Class: strings.TrimSuffix(owner, rbSelf), Static: strings.HasSuffix(owner, rbSelf),
		Line: i + 1, End: end + 1, Body: body, Sig: hashOf("")}
}

// rbMacroDefs reads a model macro's generated methods, as synthetic
// definitions (see rbSynName): associations, attachments, enums, delegates.
// They're entry points, with IDs like real methods (Ticket#event), so a
// real method of the same name merges with them.
func rbMacroDefs(code, raw []string, i int, scope string) []hDef {
	end := rbStatementEnd(code, i)
	text := strings.TrimSpace(strings.Join(code[i:end+1], " "))
	off := len(strings.Join(code[i:end+1], " ")) - len(strings.TrimLeft(strings.Join(code[i:end+1], " "), " \t"))
	src := strings.Join(raw[i:end+1], " ")
	if off <= len(src) {
		src = src[off:]
	}
	var out []hDef
	switch {
	case rbAssocRe.MatchString(text):
		m := rbAssocRe.FindStringSubmatch(text)
		arg := ""
		switch {
		case strings.Contains(text, "polymorphic:") && strings.Contains(text, "true"):
			arg = "?"
		case classNameRe.MatchString(src):
			cn := classNameRe.FindStringSubmatch(src)
			arg = cn[1] + cn[2]
		case rbThroughRe.MatchString(text):
			arg = "~" + rbThroughRe.FindStringSubmatch(text)[1]
			if sm := rbSourceRe.FindStringSubmatch(text); sm != nil {
				arg += "/" + sm[1]
			}
		}
		name := "<" + m[1] + ">" + m[2]
		if arg != "" {
			name = "<" + m[1] + ":" + arg + ">" + m[2]
		}
		out = append(out, rbSynDef(raw, i, end, scope, name, ""))
	case rbAttachRe.MatchString(text):
		out = append(out, rbSynDef(raw, i, end, scope, "<attachment>"+rbAttachRe.FindStringSubmatch(text)[1], ""))
	case rbEnumRe.MatchString(text):
		out = append(out, rbEnumDefs(text, src, raw, i, end, scope)...)
	case rbDelegateRe.MatchString(text):
		rest := text[len(rbDelegateRe.FindString(text)):]
		to := rbToRe.FindStringSubmatch(rest)
		if to == nil {
			break
		}
		prefix := ""
		if pm := rbPrefixRe.FindStringSubmatch(rest); pm != nil && strings.TrimPrefix(pm[1], "_") == "prefix" {
			if pm[2] == "true" {
				prefix = strings.TrimPrefix(to[1], "@") + "_"
			} else {
				prefix = pm[3] + "_"
			}
		}
		recv := "self." + to[1]
		if strings.HasPrefix(to[1], "@") {
			recv = to[1]
		}
		for _, sm := range rbSymRe.FindAllStringSubmatchIndex(rest, -1) {
			if lab := rbLabelRe.FindStringIndex(rest); lab != nil && sm[0] > lab[0] {
				break // the options
			}
			meth := rest[sm[2]:sm[3]]
			d := rbSynDef(raw, i, end, scope, "<delegate:"+to[1]+">"+prefix+meth, "delegate "+meth+" "+to[1]+" "+prefix)
			if to[1] != "class" {
				d.Calls = []hCall{{Recv: recv, Name: meth, Line: i + 1}}
				if recv != to[1] {
					d.Calls = append([]hCall{{Name: to[1], Line: i + 1}}, d.Calls...) // the association reader
				}
			}
			out = append(out, d)
		}
	}
	return out
}

// rbEnumDefs reads enum status: {…} / enum :status, {…} (or […], %i[…],
// or the values as keywords): for each value its predicate (standing for
// the bang method and the scopes too), and the plural class method.
func rbEnumDefs(text, src string, raw []string, i, end int, scope string) []hDef {
	rest := text[len(rbEnumRe.FindString(text)):]
	srcRest := src
	if j := len(text) - len(rest); j <= len(src) {
		srcRest = src[j:]
	}
	type enum struct {
		attr          string
		group         string // the values' {…} or […], as code
		srcGroup      string
		prefix, suffx string
	}
	var enums []enum
	options := map[string]bool{"prefix": true, "suffix": true, "_prefix": true, "_suffix": true, "default": true,
		"_default": true, "scopes": true, "_scopes": true, "validate": true, "instance_methods": true, "_instance_methods": true}
	groupAt := func(k int) (string, string, int) {
		// %i[a b] is blanked in code: look at the source.
		if rk := rbSkipSpaces(srcRest, min(k, len(srcRest))); rk < len(srcRest) && (strings.HasPrefix(srcRest[rk:], "%i[") || strings.HasPrefix(srcRest[rk:], "%w[")) {
			if cl := strings.IndexByte(srcRest[rk:], ']'); cl > 0 && rk+cl+1 <= len(rest) {
				return rest[rk : rk+cl+1], srcRest[rk : rk+cl+1], rk + cl + 1
			}
		}
		k = rbSkipSpaces(rest, k)
		if k < len(rest) && (rest[k] == '{' || rest[k] == '[') {
			if cl := matchingParen(rest, k); cl > k {
				sg := ""
				if cl+1 <= len(srcRest) {
					sg = srcRest[k : cl+1]
				}
				return rest[k : cl+1], sg, cl + 1
			}
		}
		return "", "", k
	}
	if sm := rbSymAtRe.FindStringSubmatch(rest); sm != nil {
		// enum :status, {…} or enum :status, active: 0, archived: 1
		k := len(sm[0])
		k = rbSkipSpaces(rest, k)
		if k < len(rest) && rest[k] == ',' {
			k++
		}
		g, sg, _ := groupAt(k)
		if g == "" {
			// The values as keywords, up to the options.
			var vals []string
			for _, lm := range rbLabelRe.FindAllStringSubmatch(rest[k:], -1) {
				if !options[lm[1]] {
					vals = append(vals, lm[1]+":")
				}
			}
			g = "{" + strings.Join(vals, " ") + "}"
		}
		enums = append(enums, enum{attr: sm[1], group: g, srcGroup: sg})
	} else {
		for _, lm := range rbLabelRe.FindAllStringSubmatchIndex(rest, -1) {
			name := rest[lm[2]:lm[3]]
			if options[name] {
				continue
			}
			if g, sg, _ := groupAt(lm[1]); g != "" {
				enums = append(enums, enum{attr: name, group: g, srcGroup: sg})
			}
		}
	}
	for _, pm := range rbPrefixRe.FindAllStringSubmatch(rest, -1) {
		v := pm[3]
		if pm[2] == "true" {
			v = "\x00" // the attribute's name
		}
		for k := range enums {
			if strings.HasSuffix(pm[1], "prefix") {
				enums[k].prefix = v
			} else {
				enums[k].suffx = v
			}
		}
	}
	var out []hDef
	for _, e := range enums {
		var vals []string
		switch {
		case strings.HasPrefix(e.srcGroup, "%"):
			vals = strings.Fields(strings.Trim(e.srcGroup[2:], "[]"))
		case strings.HasPrefix(e.group, "{"):
			inner := e.group[1 : len(e.group)-1]
			for _, lm := range rbLabelRe.FindAllStringSubmatchIndex(inner, -1) {
				if lm[0] > 0 && (isWordByte(inner[lm[0]-1]) || inner[lm[0]-1] == ':') {
					continue
				}
				vals = append(vals, inner[lm[2]:lm[3]])
			}
		default:
			for _, sm := range rbSymRe.FindAllStringSubmatch(e.group, -1) {
				vals = append(vals, sm[1])
			}
		}
		affix := func(a string) string {
			if a == "\x00" {
				return e.attr
			}
			return a
		}
		for _, v := range vals {
			m := v
			if e.prefix != "" {
				m = affix(e.prefix) + "_" + m
			}
			if e.suffx != "" {
				m = m + "_" + affix(e.suffx)
			}
			out = append(out, rbSynDef(raw, i, end, scope, "<enum>"+m+"?", "enum "+e.attr+" "+m))
		}
		if len(vals) > 0 {
			out = append(out, rbSynDef(raw, i, end, scope+rbSelf, "<enum_values>"+rbPluralize(e.attr), ""))
		}
	}
	return out
}

// rbAASMDefs reads an AASM (or state_machines) event or state line: an
// event's methods (ship, ship!, may_ship?) and a state's predicate, with
// the callback symbols on the line as refs.
func rbAASMDefs(code, raw []string, i int, scope string, trim string) []hDef {
	end := rbStatementEnd(code, i)
	refs := rbLabelSymRefs(trim, i+1, rbAASMLabels)
	if m := rbEventRe.FindStringSubmatch(trim); m != nil {
		d := rbSynDef(raw, i, end, scope, "<aasm_event>"+m[1], "")
		d.Calls = refs
		return []hDef{d}
	}
	var out []hDef
	rest := trim[len("state"):]
	if lab := rbLabelRe.FindStringIndex(rest); lab != nil {
		rest = rest[:lab[0]]
	}
	for _, sm := range rbSymRe.FindAllStringSubmatch(rest, -1) {
		d := rbSynDef(raw, i, end, scope, "<aasm_state>"+sm[1]+"?", "state "+sm[1])
		if len(out) == 0 {
			d.Calls = refs
		}
		out = append(out, d)
	}
	return out
}

// rbLabelSymRefs reads the method symbols given to the named options on a
// line (after: :notify, guard: [:a, :b]), as refs on an instance.
func rbLabelSymRefs(line string, lineNo int, labels map[string]bool) []hCall {
	var out []hCall
	for _, m := range rbLabelRe.FindAllStringSubmatchIndex(line, -1) {
		if !labels[line[m[2]:m[3]]] || (m[0] > 0 && (isWordByte(line[m[0]-1]) || line[m[0]-1] == ':')) || (m[1] < len(line) && line[m[1]] == ':') {
			continue
		}
		v := rbSkipSpaces(line, m[1])
		var syms [][]string
		if v < len(line) && line[v] == '[' {
			cl := matchingParen(line, v)
			if cl < 0 {
				cl = len(line)
			}
			syms = rbSymRe.FindAllStringSubmatch(line[v:cl], -1)
		} else if sm := rbSymAtRe.FindStringSubmatch(line[v:]); sm != nil {
			syms = [][]string{sm}
		}
		for _, sm := range syms {
			out = append(out, hCall{Recv: rbSelfNew, Name: sm[1], Line: lineNo, Ref: true})
		}
	}
	return out
}

var rbLambdaRe = regexp.MustCompile(`->|\blambda\b|\bproc\b`)

// rbInstCalls moves calls to the instance side (see rbInstRecv).
func rbInstCalls(calls []hCall) []hCall {
	for k := range calls {
		calls[k].Recv = rbInstRecv(calls[k].Recv)
	}
	return calls
}

// rbInstRecv moves a call in a callback's lambda or block to the instance
// side: the block runs on the record, not on the class.
func rbInstRecv(r string) string {
	switch {
	case r == rbSelfNew || strings.HasPrefix(r, rbSelfNew+"."):
		return r
	case r == "" || r == "self":
		return rbSelfNew
	case strings.HasPrefix(r, "self."):
		return rbSelfNew + r[len("self"):]
	}
	return r
}

// rbTemplateCalls are an action's implicit template calls: one per format
// it may have (show.html.erb, show.json.jbuilder, …), resolved to what
// exists.
func rbTemplateCalls(line int) []hCall {
	out := make([]hCall, rbTemplateN)
	for k := range out {
		out[k] = hCall{Recv: rbTemplateRecv, Name: strconv.Itoa(k), Line: line}
	}
	return out
}

// resolveRails resolves the scanner's pseudo-calls (see rbIvarRecv…).
//
// A pseudo-call that may not resolve has a name starting with "<", which
// no definition has, so it's only counted when its want names an ID.
func (l *rbCalls) resolveRails(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	name := strings.TrimPrefix(c.Name, "<")
	switch {
	case c.Recv == rbTemplateRecv:
		return l.implicitTemplate(r, p, d, c.Name)
	case c.Recv == rbRenderRecv:
		return l.resolveRender(r, p, d, c.Name)
	case strings.HasPrefix(c.Recv, rbAuthRecv):
		return l.resolvePolicy(r, p, d, strings.TrimPrefix(c.Recv, rbAuthRecv), "", name)
	case strings.HasPrefix(c.Recv, rbPolicyRecv):
		return l.resolvePolicy(r, p, d, strings.TrimPrefix(c.Recv, rbPolicyRecv), "", name)
	case strings.HasPrefix(c.Recv, rbScopeRecv):
		return l.resolvePolicy(r, p, d, strings.TrimPrefix(c.Recv, rbScopeRecv), "::Scope", name)
	case c.Recv == rbActionRecv:
		// ActionController::TestCase finds its controller from the test's
		// name, dropping segments: Admin::TicketsController::ShowTest.
		cls := strings.TrimSuffix(d.Class, rbSelf)
		if !strings.HasSuffix(cls, "Test") {
			return "", nil
		}
		for n := strings.TrimSuffix(cls, "Test"); n != ""; {
			if strings.HasSuffix(n, "Controller") {
				if cp, fs := l.constant("", n); len(fs) > 0 {
					return l.find(r, fs, cp, name)
				}
			}
			i := strings.LastIndex(n, "::")
			if i < 0 {
				break
			}
			n = n[:i]
		}
	case c.Recv == rbFactoryRecv && strings.HasPrefix(c.Name, "<"):
		cls := l.factories[name]
		if cls == "" {
			cls = rbCamelize(name)
		}
		return l.classNode(r, cls)
	case strings.HasPrefix(c.Recv, rbFactoryRecv):
		return l.classNode(r, c.Name)
	}
	return "", nil
}

// classNode is the ID of class cls's body, if it has one (a ref to the
// class), "" otherwise.
func (l *rbCalls) classNode(r *hResolver, cls string) (string, []string) {
	cp, fs := l.constant("", cls)
	for _, f := range fs {
		if cd := r.def(f, "", cp); cd != nil && cd.IsClass {
			return l.id(f, cd), nil
		}
	}
	return "", nil
}

// viewsDir is the views folder of a controller or mailer class
// ("admin/tickets", "user_mailer"), "" for other classes.
func (l *rbCalls) viewsDir(r *hResolver, cls string) string {
	switch {
	case strings.HasSuffix(cls, "Controller"):
		return strings.TrimSuffix(rbUnderscore(cls), "_controller")
	case strings.HasSuffix(cls, "Mailer") || l.isMailer(r, cls):
		return rbUnderscore(cls)
	}
	return ""
}

// implicitTemplate is the k-th template an action renders without saying
// so (app/views/<controller>/<action>.*), or a component's template.
func (l *rbCalls) implicitTemplate(r *hResolver, p string, d *hDef, k string) (string, []string) {
	n, _ := strconv.Atoi(k)
	cls := strings.TrimSuffix(d.Class, rbSelf)
	var stems []string
	if dir := l.viewsDir(r, cls); dir != "" && d.Class == cls {
		stems = []string{rbAppRoot(p) + "app/views/" + dir + "/" + d.Name}
	} else if l.isComponent(r, cls) {
		for _, f := range l.classes[cls] {
			stem := strings.TrimSuffix(f, ".rb")
			stems = append(stems, stem, stem+"/"+path.Base(stem))
		}
	}
	for _, s := range stems {
		if ps := l.views[s]; len(ps) > 0 {
			if n < len(ps) {
				return "view:" + ps[n], nil
			}
			break
		}
	}
	return "", nil
}

// resolveRender resolves a render spec: "s:x" (a string: a template from
// a controller, a partial from a view), "p:x" a partial, "t:x" a template,
// "a:x" an action's template, "e:expr" a record or collection's partial,
// "m:" the model's own partial (Turbo broadcasts).
func (l *rbCalls) resolveRender(r *hResolver, p string, d *hDef, spec string) (string, []string) {
	kind, arg, _ := strings.Cut(spec, ":")
	rel := rbAppRel(p)
	cls := strings.TrimSuffix(d.Class, rbSelf)
	inView := d.Name == rbViewName || strings.HasPrefix(rel, "app/helpers/") || strings.HasPrefix(rel, "app/components/")
	ctrlDir := l.viewsDir(r, cls)
	viewDir := ""
	if d.Name == rbViewName && strings.HasPrefix(rel, "app/views/") {
		viewDir = path.Dir(strings.TrimPrefix(rel, "app/views/"))
	}
	partial := func(name string) string {
		dir, base := path.Split(name)
		if dir == "" {
			dir = viewDir
			if dir == "" {
				dir = ctrlDir
			}
			if dir == "" {
				return ""
			}
		}
		return path.Join(dir, "_"+base)
	}
	template := func(name string) string {
		if !strings.Contains(name, "/") {
			if ctrlDir == "" {
				return ""
			}
			return ctrlDir + "/" + name
		}
		return name
	}
	partialOf := func(c string) string {
		u := rbUnderscore(c)
		dir, base := path.Split(u)
		return dir + rbPluralize(base) + "/_" + base
	}
	stem := ""
	switch kind {
	case "s":
		if inView {
			stem = partial(arg)
		} else {
			stem = template(arg)
		}
	case "p":
		stem = partial(arg)
	case "t":
		stem = template(arg)
	case "a":
		if ctrlDir != "" {
			stem = ctrlDir + "/" + arg
		}
	case "e":
		t, ok := l.typeOf(r, p, d, arg)
		if !ok || t.kind == 's' || l.isComponent(r, t.cls) {
			return "", nil
		}
		stem = partialOf(t.cls)
	case "m":
		if cls != "" {
			stem = partialOf(cls)
		}
	}
	if stem == "" {
		return "", nil
	}
	root := rbAppRoot(p) + "app/views/"
	full := root + strings.TrimPrefix(stem, "/")
	if ps := l.views[full]; len(ps) > 0 {
		return "view:" + ps[0], nil
	}
	if (kind == "s" || kind == "p") && !strings.Contains(arg, "/") {
		// A relative partial falls back to app/views/application, the
		// view path every controller inherits.
		if ps := l.views[root+"application/_"+arg]; len(ps) > 0 {
			return "view:" + ps[0], nil
		}
	}
	want := make([]string, 0, len(rbViewExts))
	for _, e := range rbViewExts {
		want = append(want, "view:"+full+e)
	}
	return "", want
}

// resolvePolicy resolves a Pundit call on a record (an expression, or
// :headless): the record's class's policy (suffix "::Scope" for its scope),
// method q.
func (l *rbCalls) resolvePolicy(r *hResolver, p string, d *hDef, expr, suffix, q string) (string, []string) {
	var pol string
	if strings.HasPrefix(expr, ":") {
		pol = rbCamelize(expr[1:]) + "Policy"
	} else {
		t, ok := l.typeOf(r, p, d, expr)
		if !ok {
			return "", nil
		}
		pol = t.cls + "Policy"
	}
	pol += suffix
	cp, fs := l.constant("", pol)
	if len(fs) == 0 {
		return "", []string{pol + "#" + q}
	}
	return l.find(r, fs, cp, q)
}

// rbInAASM reports whether a scan is inside an aasm (or state_machine)
// block.
func rbInAASM(stack []rbFrame) bool {
	for _, f := range stack {
		if f.aasm {
			return true
		}
	}
	return false
}

// rbAddTemplateCalls gives each public action of a controller or mailer
// that doesn't render or redirect itself (or uses respond_to) its implicit
// template calls, and each ViewComponent its template (from initialize,
// or the class when it has none). The classes are told by name here; the
// resolver checks the rest.
func rbAddTemplateCalls(defs []hDef, priv, renders, responds map[int]bool) {
	for k := range defs {
		d := &defs[k]
		switch {
		case d.IsClass && strings.HasSuffix(d.Name, "Component"):
			target := k
			for j := range defs {
				if defs[j].Owner == d.Name && defs[j].Name == "initialize" {
					target = j
				}
			}
			defs[target].Calls = append(defs[target].Calls, rbTemplateCalls(defs[target].Line)...)
		case !d.IsClass && !d.Static && !priv[k] && (strings.HasSuffix(d.Owner, "Controller") || strings.HasSuffix(d.Owner, "Mailer")) &&
			rbIdentEnd(d.Name) && d.Name != "initialize" && (!renders[k] || responds[k]):
			d.Calls = append(d.Calls, rbTemplateCalls(d.Line)...)
		}
	}
}
