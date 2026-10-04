package review

import (
	"encoding/json"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// maxVerify caps the candidates per category checked against the whole
// repo with git grep.
const maxVerify = 60

// pattern finds one kind of name in a file's text. name builds the name
// from a match's submatches (nil: the first non-empty group); the whole
// match is what git grep looks for to tell a new name from a moved one.
type pattern struct {
	re   *regexp.Regexp
	ok   func(path string) bool
	name func(m []string) string
}

func ext(exts ...string) func(string) bool {
	return func(p string) bool {
		for _, e := range exts {
			if strings.HasSuffix(p, e) {
				return true
			}
		}
		return false
	}
}

var (
	goFile   = ext(".go")
	jsFile   = ext(".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx")
	isRoutes = func(p string) bool { return p == "config/routes.rb" || strings.HasSuffix(p, "/config/routes.rb") }
)

var routePatterns = []pattern{
	// Go 1.22 ServeMux patterns: "GET /api/sessions/{id}".
	{re: regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) /[^"\s]*)"`), ok: goFile},
	// Express-style: app.get('/x', …), router.post("/y", …).
	{re: regexp.MustCompile("\\b(?:app|router)\\.(get|post|put|patch|delete|all)\\(\\s*['\"`](/[^'\"`]*)['\"`]"), ok: jsFile,
		name: func(m []string) string { return strings.ToUpper(m[1]) + " " + m[2] }},
	// Rails routes.rb: one route per line.
	// A trailing comment needs a space before its "#": 'health#show' isn't one.
	{re: regexp.MustCompile(`(?m)^[ \t]*((?:get|post|put|patch|delete|match|root|resources?|namespace|scope|mount)\b.*?)(?:[ \t]+#.*)?[ \t]*$`), ok: isRoutes},
}

var flagPatterns = []pattern{
	// cobra/pflag: Flags().String("name", …), Flags().BoolVarP(&v, "name", …).
	{re: regexp.MustCompile(`Flags\(\)\.\w+\(\s*(?:&[\w.\[\]]+\s*,\s*)?"([\w-]+)"`), ok: goFile},
}

var configPatterns = []pattern{
	{re: regexp.MustCompile("`[^`]*\\b(?:toml|yaml):\"([^\",-][^\",]*)"), ok: goFile},
}

var envPatterns = []pattern{
	{re: regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("(\w+)"\)`), ok: goFile},
	{re: regexp.MustCompile(`ENV(?:\.fetch\(\s*|\[\s*)["'](\w+)["']`), ok: ext(".rb")},
	{re: regexp.MustCompile(`process\.env(?:\.(\w+)|\[\s*["'](\w+)["']\s*\])`), ok: jsFile},
	{re: regexp.MustCompile(`os\.(?:getenv\(|environ\.get\(|environ\[)\s*["'](\w+)["']`), ok: ext(".py")},
}

// found is one occurrence of a name: its line and the matched text.
type found struct {
	line  int
	match string
}

func firstGroup(m []string) string {
	for _, g := range m[1:] {
		if g != "" {
			return g
		}
	}
	return ""
}

// stripGoComments blanks out a Go file's comments (keeping newlines, so
// line numbers stay right): an example in a comment isn't a route.
func stripGoComments(src string) string {
	var sc scanner.Scanner
	fset := token.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(src))
	sc.Init(f, []byte(src), nil, scanner.ScanComments)
	b := []byte(src)
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			off := f.Offset(pos)
			for i := off; i < off+len(lit) && i < len(b); i++ {
				if b[i] != '\n' {
					b[i] = ' '
				}
			}
		}
	}
	return string(b)
}

// findNames returns the names patterns find in text.
func findNames(text string, pats []pattern, p string) map[string]found {
	if strings.HasSuffix(p, ".go") {
		text = stripGoComments(text)
	}
	out := map[string]found{}
	for _, pat := range pats {
		if !pat.ok(p) {
			continue
		}
		for _, idx := range pat.re.FindAllStringSubmatchIndex(text, -1) {
			m := make([]string, len(idx)/2)
			for i := range m {
				if idx[2*i] >= 0 {
					m[i] = text[idx[2*i]:idx[2*i+1]]
				}
			}
			name := firstGroup(m)
			if pat.name != nil {
				name = pat.name(m)
			}
			name = strings.TrimSpace(name)
			if _, dup := out[name]; name == "" || dup {
				continue
			}
			out[name] = found{line: 1 + strings.Count(text[:idx[0]], "\n"), match: m[0]}
		}
	}
	return out
}

// textChanges compares the names patterns find in the changed files before
// and after. A name that only moved (it's still somewhere in the working
// tree, or was already somewhere in the base) isn't a change, which git
// grep checks across the whole repo.
func textChanges(r *repo, files []*file, pats []pattern) []Change {
	type occ struct {
		path string
		found
	}
	before, after := map[string]occ{}, map[string]occ{}
	for _, f := range files {
		if f.Kind == gitfiles.KindTest || f.Kind == gitfiles.KindGenerated {
			continue
		}
		if f.before != nil {
			for n, fd := range findNames(*f.before, pats, f.oldPath()) {
				if _, ok := before[n]; !ok {
					before[n] = occ{f.Path, fd}
				}
			}
		}
		if f.after != nil {
			for n, fd := range findNames(*f.after, pats, f.Path) {
				if _, ok := after[n]; !ok {
					after[n] = occ{f.Path, fd}
				}
			}
		}
	}
	var out []Change
	verified := 0
	for n, o := range after {
		if _, had := before[n]; had {
			continue
		}
		if verified < maxVerify {
			verified++
			if r.inBase(o.match) {
				continue
			}
		}
		out = append(out, Change{Op: OpAdded, Name: n, Path: o.path, Line: o.line})
	}
	verified = 0
	for n, o := range before {
		if _, has := after[n]; has {
			continue
		}
		if verified < maxVerify {
			verified++
			if r.inWorktree(o.match) {
				continue
			}
		}
		out = append(out, Change{Op: OpRemoved, Name: n, Path: o.path, Line: o.line})
	}
	return out
}

// isMigration reports whether p is a database migration or schema file.
func isMigration(p string) bool {
	base := path.Base(p)
	if base == "schema.rb" || base == "structure.sql" || base == "schema.sql" || base == "schema.prisma" {
		return true
	}
	dirs := "/" + path.Dir(p) + "/"
	return strings.Contains(dirs, "/db/migrate/") || strings.Contains(dirs, "/migrations/")
}

func migrations(all []gitfiles.OverviewFile) []Change {
	var out []Change
	for _, f := range all {
		if !isMigration(f.Path) {
			continue
		}
		op := OpChanged
		switch f.Status {
		case "A", "?":
			op = OpAdded
		case "D":
			op = OpRemoved
		}
		out = append(out, Change{Op: op, Name: f.Path, Path: f.Path})
	}
	return out
}

// deps compares the dependency manifests' entries: name → version.
func deps(files []*file) []Change {
	var out []Change
	for _, f := range files {
		var parse func(string) map[string]depEntry
		switch path.Base(f.Path) {
		case "go.mod":
			parse = goModRequires
		case "package.json":
			parse = packageJSONDeps
		case "Gemfile":
			parse = gemfileGems
		default:
			continue
		}
		before, after := map[string]depEntry{}, map[string]depEntry{}
		if f.before != nil {
			before = parse(*f.before)
		}
		if f.after != nil {
			after = parse(*f.after)
		}
		for n, a := range after {
			b, had := before[n]
			switch {
			case !had:
				out = append(out, Change{Op: OpAdded, Name: n, Detail: a.version, Path: f.Path, Line: a.line})
			case b.version != a.version:
				out = append(out, Change{Op: OpChanged, Name: n, Detail: strings.TrimSpace(b.version + " → " + a.version), Path: f.Path, Line: a.line})
			}
		}
		for n, b := range before {
			if _, has := after[n]; !has {
				out = append(out, Change{Op: OpRemoved, Name: n, Detail: b.version, Path: f.Path, Line: b.line})
			}
		}
	}
	return out
}

type depEntry struct {
	version string
	line    int
}

// goModRequires parses go.mod's require directives, single-line and
// blocks. Indirect requirements are marked as such in the version.
func goModRequires(src string) map[string]depEntry {
	out := map[string]depEntry{}
	inBlock := false
	for i, line := range strings.Split(src, "\n") {
		indirect := strings.Contains(line, "// indirect")
		if c := strings.Index(line, "//"); c >= 0 {
			line = line[:c]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
			continue
		case inBlock && f[0] == ")":
			inBlock = false
			continue
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inBlock = true
			continue
		case f[0] == "require" && len(f) >= 3:
			f = f[1:]
		case !inBlock:
			continue
		}
		if len(f) >= 2 {
			v := f[1]
			if indirect {
				v += " (indirect)"
			}
			out[f[0]] = depEntry{version: v, line: i + 1}
		}
	}
	return out
}

func packageJSONDeps(src string) map[string]depEntry {
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(src), &doc) != nil {
		return map[string]depEntry{}
	}
	out := map[string]depEntry{}
	for _, key := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		var m map[string]string
		if json.Unmarshal(doc[key], &m) != nil {
			continue
		}
		for n, v := range m {
			line := 0
			if i := strings.Index(src, `"`+n+`"`); i >= 0 {
				line = 1 + strings.Count(src[:i], "\n")
			}
			if key != "dependencies" {
				v += " (" + strings.TrimSuffix(key, "Dependencies") + ")"
			}
			out[n] = depEntry{version: v, line: line}
		}
	}
	return out
}

var gemRe = regexp.MustCompile(`^\s*gem\s+['"]([^'"]+)['"](?:\s*,\s*['"]([^'"]+)['"])?`)

func gemfileGems(src string) map[string]depEntry {
	out := map[string]depEntry{}
	for i, line := range strings.Split(src, "\n") {
		if m := gemRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = depEntry{version: m[2], line: i + 1}
		}
	}
	return out
}

// surface collects every category of contract change. goModule says
// whether the repo is a Go module (exported API only means something then).
func surface(r *repo, goModule bool, all []gitfiles.OverviewFile, files []*file) Surface {
	var s Surface
	if goModule {
		s.Exports = goExports(files)
	}
	s.Routes = textChanges(r, files, routePatterns)
	s.Flags = textChanges(r, files, flagPatterns)
	s.Config = textChanges(r, files, configPatterns)
	s.Env = textChanges(r, files, envPatterns)
	s.Deps = deps(files)
	s.Migrations = migrations(all)
	s.Exports, s.Routes, s.Flags, s.Config = sortChanges(s.Exports), sortChanges(s.Routes), sortChanges(s.Flags), sortChanges(s.Config)
	s.Env, s.Deps, s.Migrations = sortChanges(s.Env), sortChanges(s.Deps), sortChanges(s.Migrations)
	return s
}
