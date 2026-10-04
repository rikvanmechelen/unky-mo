package review

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"strconv"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// goImport is one in-module import of a file: the imported package's
// directory and the import's line.
type goImport struct {
	to   string
	line int
}

// goImports parses a Go file's in-module imports. ok is false if the file
// doesn't parse (mid-edit): its imports are then unknown, not empty.
func goImports(src, module string) (imports []goImport, ok bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ImportsOnly)
	if err != nil {
		return nil, false
	}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		var to string
		switch {
		case p == module:
			to = "."
		case strings.HasPrefix(p, module+"/"):
			to = p[len(module)+1:]
		default:
			continue
		}
		imports = append(imports, goImport{to: to, line: fset.Position(spec.Pos()).Line})
	}
	return imports, true
}

// isGoSource reports whether p is a non-test Go file: test-only imports and
// exports aren't part of a package's architecture or API.
func isGoSource(p string) bool {
	return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
}

// pkgDir is the package (directory) of a file, "." at the root.
func pkgDir(p string) string { return path.Dir(p) }

// goLang analyzes Go: a unit is a package (directory), a reference an
// import of another package of the same module. Exact.
type goLang struct{ module string }

func (g *goLang) name() string { return "go" }
func (g *goLang) exact() bool  { return true }

func (g *goLang) detect(idx *index) bool {
	g.module = modulePath(idx.r)
	return g.module != ""
}

func (g *goLang) owns(p string) bool   { return strings.HasSuffix(p, ".go") }
func (g *goLang) unit(p string) string { return pkgDir(p) }
func (g *goLang) refsKey() string      { return g.module }

func (g *goLang) refs(_, src string) ([]ref, bool) {
	imps, ok := goImports(src, g.module)
	if !ok {
		return nil, false
	}
	refs := make([]ref, len(imps))
	for i, imp := range imps {
		refs[i] = ref{to: imp.to, line: imp.line}
	}
	return refs, true
}

// goDecl is one exported top-level declaration: its signature (types only,
// so renaming a parameter isn't a change), a short label for display, and
// its line.
type goDecl struct {
	sig, detail string
	line        int
}

// exportedDecls parses a Go file's package name and exported API: funcs,
// methods on exported types ("Type.Method"), types, consts and vars.
func exportedDecls(src string) (pkg string, decls map[string]goDecl, ok bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	if err != nil {
		return "", nil, false
	}
	decls = map[string]goDecl{}
	line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := recvName(d.Recv.List[0].Type)
				if !ast.IsExported(recv) {
					continue
				}
				name = recv + "." + name
			}
			sig := "func" + typeParams(fset, d.Type.TypeParams) + "(" + fieldTypes(fset, d.Type.Params) + ")" + results(fset, d.Type.Results)
			decls[name] = goDecl{sig: sig, detail: "func " + name + strings.TrimPrefix(sig, "func"), line: line(d)}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					decls[s.Name.Name] = goDecl{sig: typeSig(fset, s), detail: typeDetail(s), line: line(s)}
				case *ast.ValueSpec:
					kw := d.Tok.String()
					for i, n := range s.Names {
						if !n.IsExported() {
							continue
						}
						sig := kw
						if s.Type != nil {
							sig += " " + node(fset, s.Type)
						}
						if i < len(s.Values) {
							sig += " = " + node(fset, s.Values[i])
						}
						decls[n.Name] = goDecl{sig: sig, detail: kw + " " + n.Name, line: line(n)}
					}
				}
			}
		}
	}
	return f.Name.Name, decls, true
}

func node(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}

func recvName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// fieldTypes prints a parameter list's types, one per parameter, without
// names.
func fieldTypes(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		t := node(fset, f.Type)
		for n := max(1, len(f.Names)); n > 0; n-- {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, ", ")
}

func typeParams(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	return "[" + fieldTypes(fset, fl) + "]"
}

func results(fset *token.FileSet, fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	t := fieldTypes(fset, fl)
	if len(fl.List) == 1 && len(fl.List[0].Names) <= 1 {
		return " " + t
	}
	return " (" + t + ")"
}

// typeSig is what a type promises other packages: for a struct, its
// exported and embedded fields; otherwise the whole type expression.
func typeSig(fset *token.FileSet, s *ast.TypeSpec) string {
	prefix := "type" + typeParams(fset, s.TypeParams)
	if s.Assign.IsValid() {
		prefix += " ="
	}
	st, ok := s.Type.(*ast.StructType)
	if !ok {
		return prefix + " " + node(fset, s.Type)
	}
	var fields []string
	for _, f := range st.Fields.List {
		t := node(fset, f.Type)
		if len(f.Names) == 0 {
			fields = append(fields, t)
			continue
		}
		for _, n := range f.Names {
			if n.IsExported() {
				fields = append(fields, n.Name+" "+t)
			}
		}
	}
	return prefix + " struct{" + strings.Join(fields, "; ") + "}"
}

func typeDetail(s *ast.TypeSpec) string {
	switch s.Type.(type) {
	case *ast.StructType:
		return "type " + s.Name.Name + " struct"
	case *ast.InterfaceType:
		return "type " + s.Name.Name + " interface"
	case *ast.FuncType:
		return "type " + s.Name.Name + " func"
	}
	return "type " + s.Name.Name
}

// goExports compares the exported API of each package's changed files
// between the two versions. A declaration that moves between changed files
// of one package isn't a change. Package main has no importers, so it has
// no API.
func goExports(files []*file) []Change {
	type pkgDecls struct {
		name          string
		before, after map[string]goDecl
		paths         map[string]string // decl → current path of its file
	}
	pkgs := map[string]*pkgDecls{}
	get := func(dir string) *pkgDecls {
		if pkgs[dir] == nil {
			pkgs[dir] = &pkgDecls{before: map[string]goDecl{}, after: map[string]goDecl{}, paths: map[string]string{}}
		}
		return pkgs[dir]
	}
	broken := map[string]bool{}
	for _, f := range files {
		if f.Kind != gitfiles.KindLogic && f.Kind != gitfiles.KindFormat && f.Kind != gitfiles.KindRenamed {
			continue
		}
		if f.before != nil && isGoSource(f.oldPath()) {
			dir := pkgDir(f.oldPath())
			name, decls, ok := exportedDecls(*f.before)
			if !ok {
				broken[dir] = true
				continue
			}
			p := get(dir)
			if p.name == "" {
				p.name = name
			}
			for n, d := range decls {
				p.before[n] = d
				if _, ok := p.paths[n]; !ok {
					p.paths[n] = f.Path
				}
			}
		}
		if f.after != nil && isGoSource(f.Path) {
			dir := pkgDir(f.Path)
			name, decls, ok := exportedDecls(*f.after)
			if !ok {
				broken[dir] = true
				continue
			}
			p := get(dir)
			p.name = name
			for n, d := range decls {
				p.after[n] = d
				p.paths[n] = f.Path
			}
		}
	}
	var out []Change
	for dir, p := range pkgs {
		if broken[dir] || p.name == "main" {
			continue
		}
		for n, d := range p.after {
			old, had := p.before[n]
			switch {
			case !had:
				out = append(out, Change{Op: OpAdded, Name: p.name + "." + n, Detail: d.detail, Path: p.paths[n], Line: d.line})
			case old.sig != d.sig:
				out = append(out, Change{Op: OpChanged, Name: p.name + "." + n, Detail: d.detail, Path: p.paths[n], Line: d.line})
			}
		}
		for n, d := range p.before {
			if _, has := p.after[n]; !has {
				out = append(out, Change{Op: OpRemoved, Name: p.name + "." + n, Detail: d.detail, Path: p.paths[n], Line: d.line})
			}
		}
	}
	return out
}
