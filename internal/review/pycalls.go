package review

import (
	"regexp"
	"strings"
)

// pyCalls is the Python call analyzer: definitions from indentation (nested
// functions and classes included), calls resolved through self/cls and the
// class's bases, the file's imports (aliases too), module aliases, and the
// file's own scopes; a call on an unknown receiver only when exactly one
// method in the repo has its name.
type pyCalls struct{ pl *pyLang }

func (l *pyCalls) name() string         { return "python" }
func (l *pyCalls) owns(p string) bool   { return strings.HasSuffix(p, ".py") }
func (l *pyCalls) unit(p string) string { return (&pyLang{}).unit(p) }

func (l *pyCalls) setup(idx *index) bool {
	l.pl = &pyLang{}
	return l.pl.detect(idx)
}

func (l *pyCalls) id(p string, d *hDef) string { return p + ":" + qualify(d.Owner, d.Name) }

func (l *pyCalls) display(d *hDef) string {
	parts := strings.Split(qualify(d.Owner, d.Name), ".")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, ".")
}

var (
	pyDefLineRe   = regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)
	pyClassLineRe = regexp.MustCompile(`^(\s*)class\s+([A-Za-z_]\w*)\s*(\(|:)`)
	pyDecoLineRe  = regexp.MustCompile(`^\s*@\s*([A-Za-z_][\w.]*)`)
	pyCallChainRe = regexp.MustCompile(`([A-Za-z_]\w*(?:\s*\.\s*[A-Za-z_]\w*)*)\s*\(`)
	pyIdentRe     = regexp.MustCompile(`(?:self\.|cls\.)?[A-Za-z_]\w*`)
	pyCommentRe   = regexp.MustCompile(`^#`)
	pyImportLnRe  = regexp.MustCompile(`^\s*import\s+(.+)$`)
	pyFromLnRe    = regexp.MustCompile(`^\s*from\s+(\.*[\w.]*)\s+import\s+(.+)$`)
)

var pyKeywords = map[string]bool{
	"if": true, "elif": true, "while": true, "for": true, "return": true, "not": true, "and": true, "or": true,
	"in": true, "is": true, "lambda": true, "yield": true, "assert": true, "del": true, "with": true, "except": true,
	"raise": true, "await": true, "def": true, "class": true, "import": true, "from": true, "as": true, "print": true,
	"else": true, "try": true, "finally": true, "pass": true, "None": true, "True": true, "False": true, "global": true,
	"nonlocal": true, "async": true, "match": true, "case": true,
}

// pyFrame is an open definition while scanning.
type pyFrame struct {
	indent int
	def    int    // index into the file's Defs
	path   string // its qualified name: the owner of what's inside it
	class  bool
}

// scanFile reads a Python file's definitions. A definition runs from its
// def/class line to the last code line before the next one indented no
// deeper. Module-level calls belong to a "<module>" definition.
func (l *pyCalls) scanFile(_, src string) hFile {
	f := hFile{OK: true}
	raw := strings.Split(src, "\n")
	code := strings.Split(pyCode(src), "\n")
	var stack []pyFrame
	lastCode := 0
	module := -1
	var moduleText strings.Builder
	var decos []hCall
	var decoLines []int
	// own holds each definition's own lines (0-based), without its nested
	// definitions': a class doesn't change when one of its methods does.
	own := map[int][]int{}
	closeTo := func(indent int) {
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			top := stack[len(stack)-1]
			f.Defs[top.def].End = lastCode
			f.Defs[top.def].Body = hLinesBody(raw, own[top.def], pyCommentRe)
			stack = stack[:len(stack)-1]
		}
	}
	span := func(from, to int) []int {
		var out []int
		for k := from; k <= to; k++ {
			out = append(out, k)
		}
		return out
	}
	owner := func() (string, string) {
		o, c := "", ""
		for _, fr := range stack {
			o = fr.path
			if fr.class {
				c = fr.path
			}
		}
		return o, c
	}
	for i := 0; i < len(code); i++ {
		start := i
		line := code[i]
		for depth(line) > 0 || strings.HasSuffix(strings.TrimRight(line, " \t"), "\\") {
			if i+1 >= len(code) {
				break
			}
			line = strings.TrimSuffix(strings.TrimRight(line, " \t"), "\\") + " " + code[i+1]
			i++
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		closeTo(indent)
		o, c := owner()
		if m := pyDecoLineRe.FindStringSubmatch(line); m != nil {
			parts := strings.Split(m[1], ".")
			decos = append(decos, hCall{Recv: strings.Join(parts[:len(parts)-1], "."), Name: parts[len(parts)-1], Line: start + 1, Ref: true})
			decoLines = append(decoLines, span(start, i)...)
			lastCode = i + 1
			continue
		}
		if m := pyClassLineRe.FindStringSubmatch(line); m != nil {
			name := m[2]
			var bases []string
			if m[3] == "(" {
				open := strings.Index(line, "(")
				if cl := matchingParen(line, open); cl > open {
					for _, b := range strings.Split(line[open+1:cl], ",") {
						b = strings.Join(strings.Fields(b), "")
						if b != "" && !strings.Contains(b, "=") {
							bases = append(bases, b)
						}
					}
				}
			}
			f.Defs = append(f.Defs, hDef{Name: name, Owner: o, Class: c, IsClass: true, Line: start + 1, Calls: decos})
			own[len(f.Defs)-1] = append(decoLines, span(start, i)...)
			decos, decoLines = nil, nil
			p := qualify(o, name)
			f.Classes = append(f.Classes, hClass{Name: p, Bases: bases})
			stack = append(stack, pyFrame{indent: indent, def: len(f.Defs) - 1, path: p, class: true})
			lastCode = i + 1
			continue
		}
		if m := pyDefLineRe.FindStringSubmatch(line); m != nil {
			name := m[2]
			open := strings.Index(line, "(")
			cl := matchingParen(line, open)
			sig := ""
			rest := ""
			if cl > open {
				// Strings are blanked here: a changed string default only
				// counts if its length changes.
				sig = strings.Join(strings.Fields(line[open+1:cl]), "")
				if colon := strings.Index(line[cl:], ":"); colon >= 0 {
					rest = line[cl+colon+1:]
				}
			}
			d := hDef{Name: name, Owner: o, Class: c, Line: start + 1, Sig: hashOf(sig), Calls: decos}
			for _, dc := range decos {
				if dc.Recv == "" && (dc.Name == "staticmethod" || dc.Name == "classmethod") {
					d.Static = true
				}
			}
			decos = nil
			d.Calls = append(d.Calls, pyLineCalls(rest, start+1)...)
			f.Defs = append(f.Defs, d)
			own[len(f.Defs)-1] = append(decoLines, span(start, i)...)
			decoLines = nil
			stack = append(stack, pyFrame{indent: indent, def: len(f.Defs) - 1, path: qualify(o, name)})
			lastCode = i + 1
			continue
		}
		decos, decoLines = nil, nil
		if len(stack) > 0 {
			top := stack[len(stack)-1].def
			own[top] = append(own[top], span(start, i)...)
		}
		if m := pyFromLnRe.FindStringSubmatch(line); m != nil {
			if len(stack) == 0 {
				f.Imports = append(f.Imports, pyFromBindings(m[1], m[2], start+1)...)
			}
			lastCode = i + 1
			continue
		} else if m := pyImportLnRe.FindStringSubmatch(line); m != nil {
			if len(stack) == 0 {
				f.Imports = append(f.Imports, pyImportBindings(m[1], start+1)...)
			}
			lastCode = i + 1
			continue
		}
		calls := pyLineCalls(line, start+1)
		lastCode = i + 1
		if len(stack) == 0 {
			if len(calls) == 0 {
				continue
			}
			if module < 0 {
				f.Defs = append(f.Defs, hDef{Name: "<module>", Line: start + 1})
				module = len(f.Defs) - 1
			}
			f.Defs[module].Calls = append(f.Defs[module].Calls, calls...)
			f.Defs[module].End = i + 1
			moduleText.WriteString(strings.Join(strings.Fields(raw[start]), " ") + "\n")
			continue
		}
		top := stack[len(stack)-1]
		f.Defs[top.def].Calls = append(f.Defs[top.def].Calls, calls...)
	}
	closeTo(0)
	if module >= 0 {
		f.Defs[module].Body = hashOf(moduleText.String())
	}
	return f
}

// matchingParen finds the ")" closing the "(" at open, -1 if none.
func matchingParen(s string, open int) int {
	if open < 0 {
		return -1
	}
	d := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
			if d == 0 {
				return i
			}
		}
	}
	return -1
}

// pyLineCalls finds calls and function references in a logical line.
func pyLineCalls(line string, lineNo int) []hCall {
	// super().m( is a call to the bases' m.
	var out []hCall
	for _, m := range pySuperRe.FindAllStringSubmatch(line, -1) {
		out = append(out, hCall{Recv: "super", Name: m[1], Line: lineNo})
	}
	line = pySuperRe.ReplaceAllString(line, "        ")
	out = append(out, hCallsIn(line, lineNo, pyCallChainRe, ".", pyKeywords)...)
	// Names passed as values: after "(", "," or a lone "=", and before ","
	// or ")".
	for _, m := range pyIdentRe.FindAllStringIndex(line, -1) {
		j := lastNonSpace(line[:m[0]])
		if j < 0 || !strings.ContainsRune("(,=", rune(line[j])) || (line[j] == '=' && j > 0 && strings.ContainsRune("=!<>", rune(line[j-1]))) {
			continue
		}
		k := m[1]
		for k < len(line) && line[k] == ' ' {
			k++
		}
		if k >= len(line) || (line[k] != ',' && line[k] != ')') {
			continue
		}
		word := line[m[0]:m[1]]
		recv, name := "", word
		if i := strings.LastIndex(word, "."); i >= 0 {
			recv, name = word[:i], word[i+1:]
		}
		if pyKeywords[name] {
			continue
		}
		out = append(out, hCall{Recv: recv, Name: name, Line: lineNo, Ref: true})
	}
	return out
}

var pySuperRe = regexp.MustCompile(`super\s*\([^()]*\)\s*\.\s*([A-Za-z_]\w*)\s*\(`)

// pyFromBindings reads `from m import a, b as c`.
func pyFromBindings(module, names string, line int) []hImport {
	var out []hImport
	for _, n := range strings.Split(strings.Trim(strings.TrimSpace(names), "()"), ",") {
		f := strings.Fields(n)
		switch {
		case len(f) == 0 || f[0] == "*":
		case len(f) == 3 && f[1] == "as":
			out = append(out, hImport{Local: f[2], Spec: module, Name: f[0], Line: line})
		default:
			out = append(out, hImport{Local: f[0], Spec: module, Name: f[0], Line: line})
		}
	}
	return out
}

// pyImportBindings reads `import a.b, c as d`: "a" binds the top package
// (a.b is reached through it), "d" binds c.
func pyImportBindings(list string, line int) []hImport {
	var out []hImport
	for _, part := range strings.Split(list, ",") {
		f := strings.Fields(part)
		switch {
		case len(f) == 0:
		case len(f) == 3 && f[1] == "as":
			out = append(out, hImport{Local: f[2], Spec: f[0], Line: line})
		default:
			top := strings.Split(f[0], ".")[0]
			out = append(out, hImport{Local: top, Spec: top, Line: line})
		}
	}
	return out
}

// moduleFile resolves a dotted module (with leading dots for a relative
// one) from file p to its file, "" outside the repo or for a namespace
// package.
func (l *pyCalls) moduleFile(p, spec string) string {
	level := len(spec) - len(strings.TrimLeft(spec, "."))
	for _, t := range l.pl.targets(p, pyImport{Level: level, Module: spec[level:]}) {
		if !strings.HasSuffix(t, "/") {
			return t
		}
	}
	return ""
}

// fileOf resolves a binding to the file it takes its name from. A name
// that's itself a submodule resolves to that submodule (with Name then
// meaning the module).
func (l *pyCalls) fileOf(p string, imp hImport) string {
	if imp.Name != "" {
		sub := imp.Spec + "." + imp.Name
		if strings.HasSuffix(imp.Spec, ".") {
			sub = imp.Spec + imp.Name
		}
		if f := l.moduleFile(p, sub); f != "" {
			return f
		}
	}
	return l.moduleFile(p, imp.Spec)
}

// isModuleBinding reports whether a binding names a module (import x, or
// from pkg import submodule).
func (l *pyCalls) isModuleBinding(p string, imp hImport) bool {
	if imp.Name == "" {
		return true
	}
	sub := imp.Spec + "." + imp.Name
	if strings.HasSuffix(imp.Spec, ".") {
		sub = imp.Spec + imp.Name
	}
	return l.moduleFile(p, sub) != ""
}

// lookupName resolves a bare name visible in file p from scope d: d's own
// nested definitions, enclosing functions (class bodies don't count, as in
// Python), the module, then the imports. ref is where it is.
func (l *pyCalls) lookupName(r *hResolver, p string, d *hDef, name string) (hDefRef, []string) {
	scopes := []string{""}
	if d != nil {
		scopes = enclosingScopes(d)
	}
	for _, sc := range scopes {
		if _, isClass := r.classes[p][sc]; isClass && sc != "" {
			continue
		}
		if def := r.def(p, sc, name); def != nil {
			return hDefRef{p, def}, nil
		}
	}
	imp, ok := r.imports[p][name]
	if !ok || l.isModuleBinding(p, imp) {
		return hDefRef{}, nil
	}
	return l.exported(r, l.fileOf(p, imp), imp.Name, 1)
}

// exported finds a module's top-level name, following one re-export (a
// package __init__ importing it from elsewhere).
func (l *pyCalls) exported(r *hResolver, file, name string, hops int) (hDefRef, []string) {
	if file == "" {
		return hDefRef{}, nil
	}
	if def := r.def(file, "", name); def != nil {
		return hDefRef{file, def}, nil
	}
	if imp, ok := r.imports[file][name]; ok && hops > 0 && !l.isModuleBinding(file, imp) {
		if ref, want := l.exported(r, l.fileOf(file, imp), imp.Name, hops-1); ref.def != nil {
			return ref, want
		}
	}
	return hDefRef{}, []string{r.idOf(file, "", name)}
}

// classAt resolves a class name as written in file p (Base, mod.Base).
func (l *pyCalls) classAt(r *hResolver, p, name string) (hClassRef, bool) {
	parts := strings.Split(name, ".")
	if len(parts) == 1 {
		ref, _ := l.lookupName(r, p, nil, name)
		if ref.def != nil && ref.def.IsClass {
			return hClassRef{ref.path, qualify(ref.def.Owner, ref.def.Name)}, true
		}
		return hClassRef{}, false
	}
	if file := l.moduleOfChain(r, p, parts[:len(parts)-1]); file != "" {
		if def := r.def(file, "", parts[len(parts)-1]); def != nil && def.IsClass {
			return hClassRef{file, def.Name}, true
		}
	}
	return hClassRef{}, false
}

// moduleOfChain resolves a dotted receiver that starts with an imported
// module ("os.path", "pkg.sub") to a repo file.
func (l *pyCalls) moduleOfChain(r *hResolver, p string, chain []string) string {
	imp, ok := r.imports[p][chain[0]]
	if !ok || !l.isModuleBinding(p, imp) {
		return ""
	}
	spec := imp.Spec
	if imp.Name != "" {
		spec = imp.Spec + "." + imp.Name
		if strings.HasSuffix(imp.Spec, ".") {
			spec = imp.Spec + imp.Name
		}
	}
	if len(chain) > 1 {
		spec += "." + strings.Join(chain[1:], ".")
	}
	return l.moduleFile(p, spec)
}

// construct maps a call to a class to what runs: its __init__, else the
// class itself.
func (l *pyCalls) construct(r *hResolver, ref hDefRef) string {
	cls := hClassRef{ref.path, qualify(ref.def.Owner, ref.def.Name)}
	if init, ip := r.method(cls, "__init__", l.baseOf(r)); init != nil {
		return r.l.id(ip, init)
	}
	return r.l.id(ref.path, ref.def)
}

func (l *pyCalls) baseOf(r *hResolver) func(p, name string) (hClassRef, bool) {
	return func(p, name string) (hClassRef, bool) { return l.classAt(r, p, name) }
}

func (l *pyCalls) resolve(r *hResolver, p string, d *hDef, c hCall) (string, []string) {
	switch {
	case c.Recv == "self" || c.Recv == "cls":
		if d.Class == "" {
			return "", nil
		}
		if def, dp := r.method(hClassRef{p, d.Class}, c.Name, l.baseOf(r)); def != nil {
			return r.l.id(dp, def), nil
		}
		return "", []string{r.idOf(p, d.Class, c.Name)}
	case c.Recv == "super":
		for _, b := range r.classes[p][d.Class] {
			if bc, ok := l.classAt(r, p, b); ok {
				if def, dp := r.method(bc, c.Name, l.baseOf(r)); def != nil {
					return r.l.id(dp, def), nil
				}
			}
		}
		return "", nil
	case c.Recv == "":
		ref, want := l.lookupName(r, p, d, c.Name)
		if ref.def == nil {
			return "", want
		}
		if ref.def.IsClass && !c.Ref {
			return l.construct(r, ref), nil
		}
		return r.l.id(ref.path, ref.def), nil
	case !strings.HasPrefix(c.Recv, "?"):
		chain := strings.Split(c.Recv, ".")
		// mod.f(), pkg.sub.f()
		if file := l.moduleOfChain(r, p, chain); file != "" {
			ref, want := l.exported(r, file, c.Name, 1)
			if ref.def == nil {
				return "", want
			}
			if ref.def.IsClass && !c.Ref {
				return l.construct(r, ref), nil
			}
			return r.l.id(ref.path, ref.def), nil
		}
		// Cls.m(), mod.Cls.m()
		if cls, ok := l.classAt(r, p, c.Recv); ok {
			if def, dp := r.method(cls, c.Name, l.baseOf(r)); def != nil {
				return r.l.id(dp, def), nil
			}
			return "", []string{r.idOf(cls.path, cls.owner, c.Name)}
		}
	}
	// obj.m(): only a method name no other class in the repo uses.
	if strings.HasPrefix(c.Name, "__") {
		return "", nil
	}
	if id, ok := r.uniqueMethod(c.Name); ok {
		return id, nil
	}
	return "", r.anyMethodWant(c.Name)
}
