package review

import (
	"bytes"
	"go/ast"
	"go/build"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// maxGoPackages caps how many of the module's packages one version's call
// analysis type-checks (changed packages, their importers, and what those
// import). aws-sdk-go's 890 packages take about 6 s.
const maxGoPackages = 300

// goCalls builds Go call graphs exactly: each package is type-checked from
// source (read from the index, so any version works), with every package
// outside the module replaced by an empty one. That loses about 0.1% of
// in-module calls (measured on unky-mo) and needs no Go toolchain.
type goCalls struct{ module string }

func (g *goCalls) name() string { return "go" }
func (g *goCalls) exact() bool  { return true }

func (g *goCalls) funcs(idx *index, files []string, full bool) (*callSet, error) {
	l := newGoLoader(idx, g.module)
	set := newCallSet()

	changed := map[string]bool{} // package dirs
	for _, p := range files {
		if strings.HasSuffix(p, ".go") {
			changed[pkgDir(p)] = true
		}
	}
	walk := map[string]bool{}
	for d := range changed {
		walk[d] = true
	}
	imports := l.importMap()
	if full {
		// Direct importers of a changed package are the only other places
		// that can call into it.
		for dir, deps := range imports {
			for _, d := range deps {
				if changed[d] {
					walk[dir] = true
				}
			}
		}
	}
	// Read everything the walk will type-check in one go: the walked
	// packages and everything they import from the module.
	need := goClosure(walk, imports)
	if len(need) > maxGoPackages {
		set.truncated = true
		// Keep the changed packages and as many importers as fit.
		dirs := sortedKeys(walk)
		walk = map[string]bool{}
		for d := range changed {
			walk[d] = true
		}
		for _, d := range dirs {
			if len(goClosure(walk, imports)) >= maxGoPackages {
				break
			}
			walk[d] = true
		}
		need = goClosure(walk, imports)
	}
	var reads []string
	for _, p := range idx.paths {
		if strings.HasSuffix(p, ".go") && need[pkgDir(p)] {
			reads = append(reads, p)
		}
	}
	idx.prefetch(reads)

	for _, dir := range sortedKeys(walk) {
		if l.budgetSpent() {
			set.truncated = true
			break
		}
		if pkg := l.load(dir); pkg != nil {
			l.walk(pkg, set)
		}
		if changed[dir] {
			// The changed package's own tests: changed test files need both
			// sides, and "untested" needs them to see what tests reach.
			for _, pkg := range l.loadTests(dir) {
				l.walk(pkg, set)
			}
		}
	}
	for p := range l.unparsed {
		set.unparsed[p] = true
	}
	if full {
		l.addImpls(set)
	}
	l.addCallees(set)
	return set, nil
}

// addCallees gives called functions that weren't walked (their package was
// only type-checked as a dependency) a context node with their position.
func (l *goLoader) addCallees(set *callSet) {
	for _, id := range sortedKeys(l.callees) {
		if set.funcs[id] != nil {
			continue
		}
		obj := l.callees[id]
		pos := l.fset.Position(obj.Pos())
		if pos.Filename == "" {
			continue
		}
		name := obj.Name()
		if recv := obj.Signature().Recv(); recv != nil {
			t := recv.Type()
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			if n, ok := types.Unalias(t).(*types.Named); ok {
				name = n.Obj().Name() + "." + name
			}
		}
		set.add(&fn{Func: Func{ID: id, Name: name, Path: pos.Filename, Line: pos.Line, End: pos.Line,
			Unit: pkgDir(pos.Filename), Lang: "go", Test: strings.HasSuffix(pos.Filename, "_test.go")}, synthetic: true})
	}
}

// maxImplPackages caps the extra packages loaded to find an interface's
// implementations.
const maxImplPackages = 40

// methodDirs lists the package dirs declaring a method with each name, from
// a declarations-only parse cached by blob id. An implementation needn't
// import its interface's package, so this is how they're found.
func (l *goLoader) methodDirs() map[string][]string {
	key := "gomethods"
	l.idx.prefetchFor(key, isGoSource)
	out := map[string]map[string]bool{}
	for _, p := range l.idx.paths {
		if !isGoSource(p) {
			continue
		}
		names, _ := l.idx.symbols(key, p, func(src string) any {
			f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
			if err != nil {
				return []string(nil)
			}
			var ms []string
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv != nil {
					ms = append(ms, fd.Name.Name)
				}
			}
			return ms
		}).([]string)
		for _, m := range names {
			if out[m] == nil {
				out[m] = map[string]bool{}
			}
			out[m][pkgDir(p)] = true
		}
	}
	dirs := make(map[string][]string, len(out))
	for m, ds := range out {
		dirs[m] = sortedKeys(ds)
	}
	return dirs
}

// addImpls gives each interface method called dynamically a node whose
// calls are its implementations among the loaded packages.
func (l *goLoader) addImpls(set *callSet) {
	if len(l.ifaces) == 0 {
		return
	}
	// Load the packages that declare a method of a called interface's name.
	methods := l.methodDirs()
	extra := 0
	for _, m := range l.ifaces {
		for _, d := range methods[m.obj.Name()] {
			if _, ok := l.pkgs[d]; !ok && extra < maxImplPackages && !l.budgetSpent() {
				l.load(d)
				extra++
			}
		}
	}
	var pkgs []*goPkg
	for _, d := range sortedKeys(l.pkgs) {
		pkgs = append(pkgs, l.pkgs[d])
	}
	for _, id := range sortedKeys(l.ifaces) {
		m := l.ifaces[id]
		pos := l.fset.Position(m.obj.Pos())
		f := set.funcs[id]
		if f == nil {
			name := m.obj.Name()
			if recv := m.obj.Signature().Recv(); recv != nil {
				if n, ok := types.Unalias(recv.Type()).(*types.Named); ok {
					name = n.Obj().Name() + "." + name
				}
			}
			f = &fn{Func: Func{ID: id, Name: name, Path: pos.Filename, Line: pos.Line, End: pos.Line,
				Unit: pkgDir(pos.Filename), Lang: "go"}, synthetic: true}
			set.add(f)
		}
		for _, impl := range l.implsOf(m.iface, m.obj.Name(), pkgs) {
			f.calls = append(f.calls, callSite{to: impl, kind: CallImpl, line: pos.Line})
		}
	}
}

// goClosure is dirs plus every in-module package they import, transitively.
func goClosure(dirs map[string]bool, imports map[string][]string) map[string]bool {
	out := map[string]bool{}
	stack := sortedKeys(dirs)
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if out[d] {
			continue
		}
		out[d] = true
		stack = append(stack, imports[d]...)
	}
	return out
}

// goPkg is one type-checked package (or a package's tests).
type goPkg struct {
	dir   string
	path  string // import path
	files []*ast.File
	srcs  []string // repo paths, parallel to files
	texts []string
	info  *types.Info
	types *types.Package
}

// goLoader type-checks a module's packages from one index, on demand.
type goLoader struct {
	idx      *index
	module   string
	fset     *token.FileSet
	pkgs     map[string]*goPkg // by dir
	busy     map[string]bool   // being checked: an import back to it is a cycle
	fake     map[string]*types.Package
	bctx     build.Context
	unparsed map[string]bool
	start    time.Time
	// ifaces are the interface methods called dynamically: ID → the
	// interface and the method's object.
	ifaces map[string]ifaceMethod
	// callees are the module's functions something walked calls, so the
	// ones in packages that were only type-checked still get a position.
	callees map[string]*types.Func
}

type ifaceMethod struct {
	iface *types.Interface
	obj   *types.Func
}

// goBudget bounds one version's type-checking, leaving the rest of the
// request's time for the other side and the other languages.
const goBudget = 8 * time.Second

func newGoLoader(idx *index, module string) *goLoader {
	l := &goLoader{idx: idx, module: module, fset: token.NewFileSet(), pkgs: map[string]*goPkg{},
		busy: map[string]bool{}, fake: map[string]*types.Package{}, unparsed: map[string]bool{}, start: time.Now(), ifaces: map[string]ifaceMethod{}, callees: map[string]*types.Func{}}
	// The build rules of `go build` for this machine, reading from the index.
	l.bctx = build.Default
	l.bctx.CgoEnabled = false
	l.bctx.JoinPath = path.Join
	l.bctx.IsAbsPath = func(string) bool { return false }
	l.bctx.OpenFile = func(p string) (io.ReadCloser, error) {
		t := idx.read(p)
		if t == nil {
			return nil, fs.ErrNotExist
		}
		return io.NopCloser(strings.NewReader(*t)), nil
	}
	return l
}

func (l *goLoader) budgetSpent() bool { return time.Since(l.start) > goBudget }

// importMap lists each package dir's in-module imports (non-test files),
// from the import scan the architecture analysis caches by blob id.
func (l *goLoader) importMap() map[string][]string {
	key := "refs:go\x00" + l.module
	l.idx.prefetchFor(key, func(p string) bool { return isGoSource(p) })
	deps := map[string]map[string]bool{}
	for _, p := range l.idx.paths {
		if !isGoSource(p) {
			continue
		}
		v := l.idx.symbols(key, p, func(src string) any {
			rs, _ := (&goLang{module: l.module}).refs(p, src)
			return rs
		})
		rs, _ := v.([]ref)
		d := pkgDir(p)
		if deps[d] == nil {
			deps[d] = map[string]bool{}
		}
		for _, r := range rs {
			if r.to != d {
				deps[d][r.to] = true
			}
		}
	}
	out := make(map[string][]string, len(deps))
	for d, m := range deps {
		out[d] = sortedKeys(m)
	}
	return out
}

// Import implements types.Importer: in-module packages are type-checked
// from source; anything else is an empty package.
func (l *goLoader) Import(p string) (*types.Package, error) {
	if dir, ok := l.dirOf(p); ok {
		if pkg := l.load(dir); pkg != nil && pkg.types != nil {
			return pkg.types, nil
		}
	}
	return l.fakePkg(p), nil
}

func (l *goLoader) fakePkg(p string) *types.Package {
	if pkg, ok := l.fake[p]; ok {
		return pkg
	}
	pkg := types.NewPackage(p, guessPkgName(p))
	pkg.MarkComplete()
	l.fake[p] = pkg
	return pkg
}

// guessPkgName guesses an outside package's name from its path: a wrong
// guess only leaves selectors on it unresolved, which they are anyway.
func guessPkgName(p string) string {
	base := path.Base(p)
	if len(base) >= 2 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
		base = path.Base(path.Dir(p))
	}
	base = strings.TrimPrefix(base, "go-")
	base = strings.TrimSuffix(base, ".go")
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, base)
}

// dirOf maps an import path to a package dir of the module.
func (l *goLoader) dirOf(p string) (string, bool) {
	switch {
	case p == l.module:
		return ".", true
	case strings.HasPrefix(p, l.module+"/"):
		return p[len(l.module)+1:], true
	}
	return "", false
}

func (l *goLoader) importPath(dir string) string {
	if dir == "." {
		return l.module
	}
	return l.module + "/" + dir
}

// goFiles lists a package dir's .go files that `go build` would compile
// here, split into package files and test files.
func (l *goLoader) goFiles(dir string) (srcs, tests []string) {
	for _, p := range l.idx.under(dir) {
		if pkgDir(p) != dir || !strings.HasSuffix(p, ".go") {
			continue
		}
		if ok, err := l.bctx.MatchFile(dir, path.Base(p)); err != nil || !ok {
			continue
		}
		if strings.HasSuffix(p, "_test.go") {
			tests = append(tests, p)
		} else {
			srcs = append(srcs, p)
		}
	}
	return srcs, tests
}

// parse parses the given files. A file that doesn't parse is recorded and
// left out.
func (l *goLoader) parse(paths []string) (files []*ast.File, srcs, texts []string) {
	for _, p := range paths {
		t := l.idx.read(p)
		if t == nil {
			continue
		}
		f, err := parser.ParseFile(l.fset, p, *t, parser.SkipObjectResolution)
		if err != nil {
			l.unparsed[p] = true
			continue
		}
		files, srcs, texts = append(files, f), append(srcs, p), append(texts, *t)
	}
	return files, srcs, texts
}

// load type-checks a package dir (non-test files), once.
func (l *goLoader) load(dir string) *goPkg {
	if pkg, ok := l.pkgs[dir]; ok {
		return pkg
	}
	if l.busy[dir] {
		return nil // an import cycle: the importer gets an empty package
	}
	l.busy[dir] = true
	defer delete(l.busy, dir)
	paths, _ := l.goFiles(dir)
	files, srcs, texts := l.parse(paths)
	files, srcs, texts = majorityPackage(files, srcs, texts, "")
	pkg := l.check(dir, l.importPath(dir), files, srcs, texts)
	l.pkgs[dir] = pkg
	return pkg
}

// loadTests type-checks a package's tests: the package with its in-package
// test files, and its external _test package.
func (l *goLoader) loadTests(dir string) []*goPkg {
	_, tests := l.goFiles(dir)
	if len(tests) == 0 {
		return nil
	}
	base := l.load(dir)
	if base == nil || len(base.files) == 0 {
		return nil
	}
	name := base.files[0].Name.Name
	files, srcs, texts := l.parse(tests)
	var inFiles, exFiles []*ast.File
	var inSrcs, exSrcs, inTexts, exTexts []string
	for i, f := range files {
		if f.Name.Name == name {
			inFiles, inSrcs, inTexts = append(inFiles, f), append(inSrcs, srcs[i]), append(inTexts, texts[i])
		} else if f.Name.Name == name+"_test" {
			exFiles, exSrcs, exTexts = append(exFiles, f), append(exSrcs, srcs[i]), append(exTexts, texts[i])
		}
	}
	var out []*goPkg
	if len(inFiles) > 0 {
		// Checked together with the package's files (so test code sees
		// unexported names); only the test files are walked.
		all := append(append([]*ast.File{}, base.files...), inFiles...)
		allSrcs := append(append([]string{}, base.srcs...), inSrcs...)
		allTexts := append(append([]string{}, base.texts...), inTexts...)
		pkg := l.check(dir, l.importPath(dir), all, allSrcs, allTexts)
		n := len(base.files)
		pkg.files, pkg.srcs, pkg.texts = pkg.files[n:], pkg.srcs[n:], pkg.texts[n:]
		out = append(out, pkg)
	}
	if len(exFiles) > 0 {
		out = append(out, l.check(dir, l.importPath(dir)+"_test", exFiles, exSrcs, exTexts))
	}
	return out
}

// majorityPackage keeps the files of the most common package name (a
// stray file with another name would make the whole check fail).
func majorityPackage(files []*ast.File, srcs, texts []string, _ string) ([]*ast.File, []string, []string) {
	count := map[string]int{}
	best := ""
	for _, f := range files {
		count[f.Name.Name]++
		if count[f.Name.Name] > count[best] {
			best = f.Name.Name
		}
	}
	if len(count) <= 1 {
		return files, srcs, texts
	}
	var of []*ast.File
	var os, ot []string
	for i, f := range files {
		if f.Name.Name == best {
			of, os, ot = append(of, f), append(os, srcs[i]), append(ot, texts[i])
		}
	}
	return of, os, ot
}

func (l *goLoader) check(dir, importPath string, files []*ast.File, srcs, texts []string) *goPkg {
	info := &types.Info{
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Types:      map[ast.Expr]types.TypeAndValue{},
	}
	conf := types.Config{Importer: l, Error: func(error) {}, FakeImportC: true}
	tp, _ := conf.Check(importPath, l.fset, files, info) // errors are expected: outside packages are empty
	return &goPkg{dir: dir, path: importPath, files: files, srcs: srcs, texts: texts, info: info, types: tp}
}

// id is a function's ID: its full name with the module's prefix cut, so
// "(*github.com/x/m/internal/review.goLang).refs" is
// "(*internal/review.goLang).refs" and the root package's "m.F" is "F".
func (l *goLoader) id(obj *types.Func) string {
	if o := obj.Origin(); o != nil {
		obj = o
	}
	return l.trim(obj.FullName())
}

func (l *goLoader) trim(s string) string {
	s = strings.ReplaceAll(s, l.module+"/", "")
	return strings.ReplaceAll(s, l.module+".", "")
}

// inModule reports whether obj belongs to one of the module's packages.
func (l *goLoader) inModule(obj types.Object) bool {
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	_, ok := l.dirOf(strings.TrimSuffix(obj.Pkg().Path(), "_test"))
	return ok
}

// walk adds every function declared in pkg's files to set.
func (l *goLoader) walk(pkg *goPkg, set *callSet) {
	if pkg == nil || pkg.types == nil {
		return
	}
	pkgID := l.trim(pkg.path) // "." never happens: the root's path is the module
	if pkg.path == l.module {
		pkgID = ""
	}
	initID := strings.TrimPrefix(pkgID+".init", ".")
	for i, f := range pkg.files {
		src, text := pkg.srcs[i], pkg.texts[i]
		test := strings.HasSuffix(src, "_test.go")
		generated := gitfiles.IsGenerated(text)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				obj, _ := pkg.info.Defs[d.Name].(*types.Func)
				id := initID
				name := "init"
				if obj != nil && !(d.Recv == nil && d.Name.Name == "init") {
					id = l.id(obj)
					name = goDisplayName(d)
				}
				fn := &fn{Func: Func{ID: id, Name: name, Path: src, Line: l.fset.Position(d.Pos()).Line,
					End: l.fset.Position(d.End()).Line, Unit: pkgDir(src), Lang: "go", Test: test}}
				fn.generated = generated
				if d.Body != nil {
					fn.body = hashOf(goTokens(l.fset, text, d.Body))
					l.calls(pkg, d.Body, fn)
				}
				fn.sig = hashOf(fieldTypes(l.fset, d.Recv) + typeParams(l.fset, d.Type.TypeParams) +
					"(" + fieldTypes(l.fset, d.Type.Params) + ")" + results(l.fset, d.Type.Results))
				set.add(fn)
			case *ast.GenDecl:
				// Package-level variable initializers run in the package's
				// init: their calls belong to it.
				if d.Tok != token.VAR {
					continue
				}
				fn := &fn{Func: Func{ID: initID, Name: "init", Path: src, Line: l.fset.Position(d.Pos()).Line,
					End: l.fset.Position(d.End()).Line, Unit: pkgDir(src), Lang: "go", Test: test}}
				l.calls(pkg, d, fn)
				if len(fn.calls)+len(fn.unresolved) > 0 {
					fn.body = hashOf(goTokens(l.fset, text, d))
					set.add(fn)
				}
			}
		}
	}
}

// goDisplayName is "Recv.Method" or "Func".
func goDisplayName(d *ast.FuncDecl) string {
	if d.Recv != nil && len(d.Recv.List) == 1 {
		if r := recvName(d.Recv.List[0].Type); r != "" {
			return r + "." + d.Name.Name
		}
	}
	return d.Name.Name
}

// goTokens is a node's source as its tokens, without comments or layout, so
// reformatting or editing a comment doesn't change its hash.
func goTokens(fset *token.FileSet, text string, n ast.Node) string {
	from, to := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
	if from < 0 || to > len(text) || from > to {
		return ""
	}
	src := []byte(text[from:to])
	var s scanner.Scanner
	fs := token.NewFileSet()
	s.Init(fs.AddFile("", -1, len(src)), src, nil, 0)
	var b bytes.Buffer
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit == "\n" {
			continue // automatic semicolons follow line breaks
		}
		b.WriteString(tok.String())
		if lit != "" {
			b.WriteByte(' ')
			b.WriteString(lit)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// calls records n's calls and function references into fn.
func (l *goLoader) calls(pkg *goPkg, n ast.Node, f *fn) {
	info := pkg.info
	callee := map[*ast.Ident]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun := ast.Unparen(c.Fun)
		switch x := fun.(type) {
		case *ast.IndexExpr:
			fun = x.X
		case *ast.IndexListExpr:
			fun = x.X
		}
		switch x := fun.(type) {
		case *ast.Ident:
			callee[x] = true
			if info.Uses[x] == nil && info.Defs[x] == nil && types.Universe.Lookup(x.Name) == nil {
				// An undefined name: maybe a function of this package
				// that's gone.
				f.unresolved = append(f.unresolved, unresolvedSite{line: l.line(x), want: []string{l.trim(pkg.types.Path()) + "." + x.Name}})
			} else if v, ok := info.Uses[x].(*types.Var); ok && l.inModule(v) && isFuncType(v.Type()) && v.Parent() == v.Pkg().Scope() {
				f.unresolved = append(f.unresolved, unresolvedSite{line: l.line(x)}) // a package-level func variable
			}
		case *ast.SelectorExpr:
			callee[x.Sel] = true
			if info.Uses[x.Sel] != nil {
				if v, ok := info.Uses[x.Sel].(*types.Var); ok && l.inModule(v) && isFuncType(v.Type()) {
					f.unresolved = append(f.unresolved, unresolvedSite{line: l.line(x.Sel)}) // a func field
				}
				break
			}
			if want := l.wantSelector(pkg, x); want != nil {
				f.unresolved = append(f.unresolved, unresolvedSite{line: l.line(x.Sel), want: want})
			}
		}
		return true
	})
	ast.Inspect(n, func(n ast.Node) bool {
		var id *ast.Ident
		var sel *ast.SelectorExpr
		switch x := n.(type) {
		case *ast.SelectorExpr:
			id, sel = x.Sel, x
		case *ast.Ident:
			id = x
		default:
			return true
		}
		obj, ok := info.Uses[id].(*types.Func)
		if !ok || !l.inModule(obj) {
			return true
		}
		kind := CallRef
		if callee[id] {
			kind = CallStatic
		}
		if sel != nil {
			if s := info.Selections[sel]; s != nil && types.IsInterface(s.Recv()) {
				if kind == CallStatic {
					kind = CallDynamic
				}
				if it, ok := s.Recv().Underlying().(*types.Interface); ok {
					l.ifaces[l.id(obj)] = ifaceMethod{iface: it, obj: obj}
				}
			}
			// The selector's Sel is visited again as an Ident: skip it then.
			f.calls = append(f.calls, callSite{to: l.id(obj), kind: kind, line: l.line(id)})
			l.callees[l.id(obj)] = obj
			return false
		}
		f.calls = append(f.calls, callSite{to: l.id(obj), kind: kind, line: l.line(id)})
		l.callees[l.id(obj)] = obj
		return true
	})
}

// wantSelector is what an unresolved x.Sel call would point at if the
// function existed: pkg.Name for a package selector, (T).M and (*T).M for a
// method of a module type.
func (l *goLoader) wantSelector(pkg *goPkg, x *ast.SelectorExpr) []string {
	if id, ok := x.X.(*ast.Ident); ok {
		if pn, ok := pkg.info.Uses[id].(*types.PkgName); ok {
			if _, in := l.dirOf(pn.Imported().Path()); in {
				return []string{l.trim(pn.Imported().Path()) + "." + x.Sel.Name}
			}
			return nil
		}
	}
	t := pkg.info.Types[x.X].Type
	if t == nil {
		return nil
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || !l.inModule(named.Obj()) {
		return nil
	}
	tn := l.trim(named.Obj().Pkg().Path()) + "." + named.Obj().Name()
	if named.Obj().Pkg().Path() == l.module {
		tn = named.Obj().Name()
	}
	return []string{"(" + tn + ")." + x.Sel.Name, "(*" + tn + ")." + x.Sel.Name}
}

func (l *goLoader) line(n ast.Node) int { return l.fset.Position(n.Pos()).Line }

func isFuncType(t types.Type) bool {
	_, ok := t.Underlying().(*types.Signature)
	return ok
}

// implsOf lists IDs of methods implementing an interface method, among the
// named types of the given packages.
func (l *goLoader) implsOf(iface *types.Interface, method string, pkgs []*goPkg) []string {
	var out []string
	for _, pkg := range pkgs {
		if pkg == nil || pkg.types == nil {
			continue
		}
		scope := pkg.types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			t := tn.Type()
			if types.IsInterface(t) {
				continue
			}
			var recv types.Type
			switch {
			case types.Implements(t, iface):
				recv = t
			case types.Implements(types.NewPointer(t), iface):
				recv = types.NewPointer(t)
			default:
				continue
			}
			obj, _, _ := types.LookupFieldOrMethod(recv, false, pkg.types, method)
			if m, ok := obj.(*types.Func); ok && l.inModule(m) {
				out = append(out, l.id(m))
			}
		}
	}
	sort.Strings(out)
	return out
}
