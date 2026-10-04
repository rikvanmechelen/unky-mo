package review

import (
	"path"
	"strings"

	"github.com/BurntSushi/toml"
)

// pyLang analyzes Python. A unit is a package (a file's directory); a
// reference is an import that resolves to a module in the repo, under one
// of its source roots or relative to the importing package. Everything
// else (the standard library, installed packages) is external. Exact.
type pyLang struct {
	idx   *index
	roots []string // where absolute imports start, most specific first
}

func (l *pyLang) name() string { return "python" }
func (l *pyLang) exact() bool  { return true }
func (l *pyLang) owns(p string) bool {
	return strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".pyi")
}
func (l *pyLang) unit(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

func (l *pyLang) detect(idx *index) bool {
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
	l.idx = idx
	seen := map[string]bool{}
	add := func(r string) {
		r = path.Clean(r)
		if !seen[r] {
			seen[r] = true
			l.roots = append(l.roots, r)
		}
	}
	if src := idx.read("pyproject.toml"); src != nil {
		var doc struct {
			Tool struct {
				Setuptools struct {
					PackageDir map[string]string `toml:"package-dir"`
				} `toml:"setuptools"`
				Poetry struct {
					Packages []struct {
						From string `toml:"from"`
					} `toml:"packages"`
				} `toml:"poetry"`
			} `toml:"tool"`
		}
		if _, err := toml.Decode(*src, &doc); err == nil {
			if d, ok := doc.Tool.Setuptools.PackageDir[""]; ok {
				add(d)
			}
			for _, p := range doc.Tool.Poetry.Packages {
				if p.From != "" {
					add(p.From)
				}
			}
		}
	}
	if idx.hasDir("src") {
		add("src")
	}
	add(".")
	return true
}

func (l *pyLang) refs(p, src string) ([]ref, bool) {
	return l.resolve(p, l.scan(p, src)), true
}

func (l *pyLang) scan(_, src string) any { return pyImports(src) }

func (l *pyLang) resolve(p string, scanned any) []ref {
	imps, _ := scanned.([]pyImport)
	var out []ref
	for _, imp := range imps {
		for _, target := range l.targets(p, imp) {
			if u := l.unitOf(target); u != "" {
				out = append(out, ref{to: u, line: imp.Line})
			}
		}
	}
	return out
}

// targets finds the repo modules an import refers to: the module itself,
// or for `from m import a, b` the submodules a and b when they exist.
func (l *pyLang) targets(p string, imp pyImport) []string {
	var bases []string
	if imp.Level > 0 {
		dir := path.Dir(p)
		for i := 1; i < imp.Level; i++ {
			dir = path.Dir(dir)
		}
		bases = []string{dir}
	} else {
		bases = l.roots
	}
	modPath := strings.ReplaceAll(imp.Module, ".", "/")
	for _, base := range bases {
		mp := path.Join(base, modPath)
		var out []string
		for _, n := range imp.Names {
			if m := l.module(path.Join(mp, n)); m != "" {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
		if imp.Module == "" && imp.Level == 0 {
			continue
		}
		if m := l.module(mp); m != "" {
			return []string{m}
		}
	}
	return nil
}

// module finds the file or package a module path means: m.py, m.pyi,
// m/__init__.py, or a namespace package directory m/.
func (l *pyLang) module(m string) string {
	m = path.Clean(m)
	if strings.HasPrefix(m, "../") || m == ".." || m == "." {
		return ""
	}
	for _, c := range []string{m + ".py", m + ".pyi", m + "/__init__.py"} {
		if l.idx.has(c) {
			return c
		}
	}
	if l.idx.hasDir(m) {
		return m + "/"
	}
	return ""
}

// unitOf is the unit of a resolved module: a package dir is its own unit.
func (l *pyLang) unitOf(target string) string {
	if strings.HasSuffix(target, "/") {
		return strings.TrimSuffix(target, "/")
	}
	if strings.HasSuffix(target, "/__init__.py") {
		return path.Dir(target)
	}
	return l.unit(target)
}
