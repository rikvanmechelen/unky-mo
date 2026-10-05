package review

import (
	"path"
	"regexp"
	"strings"
)

// ktLang analyzes Kotlin and Java (Android and other Gradle projects). A
// unit is a module plus one package level below its root package —
// feature/moma/data, feature/moma/data/di, core/model, app-mobile/navigation
// (a source set other than main adds its name: app-mobile/staging/debug).
// Gradle modules carry the architecture, so the module path is the unit's
// stem; the package level splits a module's parts. A reference is an
// import (or qualified name) of a package declared in the repo, found
// through every file's package line. Exact.
type ktLang struct {
	idx     *index
	rootPkg map[string]string // source root → its root package's dir path ("com/moma/android")
	pkgUnit map[string]string // package → unit ("" when declared in two units)
}

func (l *ktLang) name() string { return "kotlin" }
func (l *ktLang) exact() bool  { return true }
func (l *ktLang) owns(p string) bool {
	return strings.HasSuffix(p, ".kt") || strings.HasSuffix(p, ".java")
}

var srcSetRe = regexp.MustCompile(`^(?:(.+)/)?src/([^/]+)/(kotlin|java)/`)

func (l *ktLang) detect(idx *index) bool {
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

	// Each source root's root package: the longest common package of its
	// files (from their directories, which Gradle projects keep in step).
	pkgDirs := map[string][]string{}
	for _, p := range idx.paths {
		if !l.owns(p) {
			continue
		}
		if root, rel, _, _ := sourceRoot(p); root != "" {
			pkgDirs[root] = append(pkgDirs[root], path.Dir(rel))
		}
	}
	l.rootPkg = map[string]string{}
	for root, ds := range pkgDirs {
		common := strings.Split(ds[0], "/")
		for _, d := range ds[1:] {
			segs := strings.Split(d, "/")
			k := 0
			for k < len(common) && k < len(segs) && common[k] == segs[k] {
				k++
			}
			common = common[:k]
		}
		l.rootPkg[root] = strings.Trim(strings.Join(common, "/"), ".")
	}

	// Package → unit, from every file's declared package.
	l.pkgUnit = map[string]string{}
	idx.prefetchFor("ktpkg", func(p string) bool { return l.owns(p) && !isTest(p) && l.unit(p) != "" })
	for _, p := range idx.paths {
		if !l.owns(p) || isTest(p) {
			continue
		}
		u := l.unit(p)
		if u == "" {
			continue
		}
		pkg, _ := idx.symbols("ktpkg", p, func(src string) any { return jvmPackage(src) }).(string)
		if pkg == "" {
			continue
		}
		if prev, ok := l.pkgUnit[pkg]; ok && prev != u {
			u = ""
		}
		l.pkgUnit[pkg] = u
	}
	return true
}

// sourceRoot splits a source file's path into its source root
// (app/src/main/kotlin), the rest (com/moma/android/data/Repo.kt), its
// module ("app", "." at the repo root) and source set ("main").
func sourceRoot(p string) (root, rel, module, set string) {
	m := srcSetRe.FindStringSubmatchIndex(p)
	if m == nil {
		return "", "", "", ""
	}
	module = "."
	if m[2] >= 0 {
		module = p[m[2]:m[3]]
	}
	return p[:m[1]-1], p[m[1]:], module, p[m[4]:m[5]]
}

func (l *ktLang) unit(p string) string {
	root, rel, module, set := sourceRoot(p)
	if root == "" {
		return ""
	}
	unit := module
	if set != "main" {
		unit = path.Join(module, set)
	}
	base, dir := l.rootPkg[root], path.Dir(rel)
	if dir != "." && dir != base && (base == "" || strings.HasPrefix(dir, base+"/")) {
		next := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(dir, base), "/"), "/", 2)[0]
		unit = path.Join(unit, next)
	}
	return unit
}

func (l *ktLang) refs(p, src string) ([]ref, bool) {
	return l.resolve(p, l.scan(p, src)), true
}

func (l *ktLang) scan(_, src string) any { return jvmRefs(src) }

// resolve maps raw names to units: the longest declared package the name
// starts with (a type import names a class in it; a wildcard the package).
func (l *ktLang) resolve(_ string, scanned any) []ref {
	refs, _ := scanned.([]jvmRef)
	var out []ref
	for _, r := range refs {
		segs := strings.Split(r.Name, ".")
		start := len(segs) - 1
		if r.Package {
			start = len(segs)
		}
		for k := start; k > 0; k-- {
			if u, ok := l.pkgUnit[strings.Join(segs[:k], ".")]; ok {
				if u != "" {
					out = append(out, ref{to: u, line: r.Line})
				}
				break
			}
		}
	}
	return out
}

func init() {
	ui := []string{"**/ui", "**/ui-*", "**/presentation"}
	presets["android"] = preset{
		lang: "kotlin",
		detect: func(idx *index) bool {
			for _, p := range idx.paths {
				if path.Base(p) == "AndroidManifest.xml" {
					return true
				}
			}
			return false
		},
		layers: []layer{
			{Name: "model and domain", Paths: []string{"**/model", "**/domain"}, Deny: append([]string{"**/data"}, ui...)},
			{Name: "data", Paths: []string{"**/data"}, Deny: ui},
		},
	}
}
