package review

import (
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
)

// nodeLang analyzes JavaScript and TypeScript. A unit is a folder one level
// below the last container in a path (src/components, app/javascript/
// controllers, packages/ui/src/hooks); a reference is an import that
// resolves to a file in the repo — relative paths, tsconfig/jsconfig paths
// and baseUrl, package.json imports, workspace packages and Rails'
// importmap. Bare names that resolve to none of those are packages from
// npm: external. Exact.
type nodeLang struct {
	idx        *index
	tsconfigs  map[string]*tsConfig // dir → the tsconfig/jsconfig there (nil: none)
	imports    map[string]string    // package.json "imports" (#alias) → target path
	workspaces map[string]string    // workspace package name → its dir
	importmap  map[string]string    // importmap pin or prefix ("controllers/") → path or dir
}

var nodeExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".astro"}

func (l *nodeLang) name() string { return "node" }
func (l *nodeLang) exact() bool  { return true }

func (l *nodeLang) owns(p string) bool {
	if strings.HasSuffix(p, ".min.js") {
		return false
	}
	ext := path.Ext(p)
	for _, e := range nodeExts {
		if ext == e {
			return true
		}
	}
	return false
}

func (l *nodeLang) detect(idx *index) bool {
	if !idx.has("package.json") && !idx.has("config/importmap.rb") && !idx.has("tsconfig.json") && !idx.has("jsconfig.json") {
		return false
	}
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
	l.tsconfigs = map[string]*tsConfig{}
	l.imports = map[string]string{}
	l.workspaces = map[string]string{}
	l.importmap = map[string]string{}
	if src := idx.read("package.json"); src != nil {
		var pkg struct {
			Imports    map[string]json.RawMessage `json:"imports"`
			Workspaces json.RawMessage            `json:"workspaces"`
		}
		if json.Unmarshal([]byte(*src), &pkg) == nil {
			for k, v := range pkg.Imports {
				if t := firstTarget(v); t != "" {
					l.imports[k] = cleanRel(t)
				}
			}
			l.addWorkspaces(workspaceGlobs(pkg.Workspaces))
		}
	}
	if src := idx.read("pnpm-workspace.yaml"); src != nil {
		var globs []string
		for _, line := range strings.Split(*src, "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "- ") {
				globs = append(globs, strings.Trim(strings.TrimSpace(t[2:]), `'"`))
			}
		}
		l.addWorkspaces(globs)
	}
	if src := idx.read("config/importmap.rb"); src != nil {
		l.readImportmap(*src)
	}
	return true
}

// firstTarget picks a path from a package.json imports/exports value: a
// string, or the first string of a conditions object ("import", "default",
// …).
func firstTarget(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(v, &m) == nil {
		for _, k := range []string{"import", "default", "require", "node", "types"} {
			if t := firstTarget(m[k]); t != "" {
				return t
			}
		}
	}
	return ""
}

func cleanRel(p string) string { return path.Clean(strings.TrimPrefix(p, "./")) }

func workspaceGlobs(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Packages
	}
	return nil
}

// addWorkspaces maps the names of the workspace packages matching globs
// ("packages/*") to their directories.
func (l *nodeLang) addWorkspaces(globs []string) {
	for _, g := range globs {
		if strings.HasPrefix(g, "!") {
			continue
		}
		g = strings.TrimSuffix(cleanRel(g), "/")
		for _, p := range l.idx.paths {
			if path.Base(p) != "package.json" || p == "package.json" {
				continue
			}
			dir := path.Dir(p)
			if ok, _ := path.Match(g, dir); !ok && !(strings.HasSuffix(g, "/**") && strings.HasPrefix(dir, strings.TrimSuffix(g, "**"))) {
				continue
			}
			if src := l.idx.read(p); src != nil {
				var pkg struct {
					Name string `json:"name"`
				}
				if json.Unmarshal([]byte(*src), &pkg) == nil && pkg.Name != "" {
					l.workspaces[pkg.Name] = dir
				}
			}
		}
	}
}

var (
	pinRe     = regexp.MustCompile(`(?m)^\s*pin\s+["']([^"']+)["'](?:\s*,\s*to:\s*["']([^"']+)["'])?`)
	pinAllRe  = regexp.MustCompile(`(?m)^\s*pin_all_from\s+["']([^"']+)["'](?:\s*,\s*under:\s*["']([^"']+)["'])?`)
	nodeRoots = map[string]bool{"src": true, "app": true, "apps": true, "javascript": true, "packages": true, "libs": true}
)

// readImportmap maps Rails importmap pins to files under app/javascript:
// pin "application" → app/javascript/application.js; pin_all_from
// "app/javascript/controllers", under: "controllers" → a prefix.
func (l *nodeLang) readImportmap(src string) {
	for _, m := range pinAllRe.FindAllStringSubmatch(src, -1) {
		under := m[2]
		if under == "" {
			under = strings.TrimPrefix(m[1], "app/javascript/")
		}
		l.importmap[strings.TrimSuffix(under, "/")+"/"] = strings.TrimSuffix(m[1], "/")
	}
	for _, m := range pinRe.FindAllStringSubmatch(src, -1) {
		to := m[2]
		if to == "" {
			to = m[1] + ".js"
		}
		if strings.Contains(to, "://") {
			continue
		}
		if p := "app/javascript/" + strings.TrimPrefix(to, "/"); l.idx.has(p) {
			l.importmap[m[1]] = p
		}
	}
}

func (l *nodeLang) unit(p string) string {
	segs := strings.Split(path.Dir(p), "/")
	if segs[0] == "." {
		return ""
	}
	last := -1
	for i, s := range segs {
		if nodeRoots[s] {
			last = i
		}
	}
	if last < 0 {
		return segs[0]
	}
	if last+1 < len(segs) {
		return strings.Join(segs[:last+2], "/")
	}
	return strings.Join(segs, "/") // a file directly in src/: the root itself
}

func (l *nodeLang) refs(p, src string) ([]ref, bool) {
	return l.resolve(p, l.scan(p, src)), true
}

// scan lists a file's import specifiers, before resolution.
func (l *nodeLang) scan(p, src string) any {
	switch path.Ext(p) {
	case ".vue", ".svelte", ".astro":
		src = componentScript(p, src)
	}
	return jsImports(jsCode(src))
}

func (l *nodeLang) resolve(p string, scanned any) []ref {
	specs, _ := scanned.([]ref)
	var out []ref
	for _, s := range specs {
		if target := l.resolveSpec(p, s.to); target != "" {
			if u := l.unit(target); u != "" {
				out = append(out, ref{to: u, line: s.line})
			}
		}
	}
	return out
}

// resolveSpec finds the repo file an import specifier in file p refers
// to, or "" for a package from npm (or anything unresolvable).
func (l *nodeLang) resolveSpec(p, spec string) string {
	spec, _, _ = strings.Cut(spec, "?") // Vite query suffixes (?raw, ?url)
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") || spec == "." || spec == ".." {
		return l.file(path.Join(path.Dir(p), spec))
	}
	if strings.HasPrefix(spec, "#") {
		for pat, target := range l.imports {
			if t := matchStar(pat, target, spec); t != "" {
				return l.file(t)
			}
		}
		return ""
	}
	if tc := l.tsconfigFor(path.Dir(p)); tc != nil {
		pats := sortedKeys(tc.paths)
		sort.SliceStable(pats, func(i, j int) bool { return len(pats[i]) > len(pats[j]) })
		for _, pat := range pats {
			for _, target := range tc.paths[pat] {
				if t := matchStar(pat, target, spec); t != "" {
					if f := l.file(path.Join(tc.pathsBase, t)); f != "" {
						return f
					}
				}
			}
		}
		if tc.baseURL != "" {
			if f := l.file(path.Join(tc.baseURL, spec)); f != "" {
				return f
			}
		}
	}
	// Workspace packages: the longest name that spec is or starts with.
	best := ""
	for name := range l.workspaces {
		if (spec == name || strings.HasPrefix(spec, name+"/")) && len(name) > len(best) {
			best = name
		}
	}
	if best != "" {
		dir := l.workspaces[best]
		if sub := strings.TrimPrefix(spec, best); sub != "" {
			if f := l.file(path.Join(dir, sub)); f != "" {
				return f
			}
			return l.file(path.Join(dir, "src", sub))
		}
		if f := l.file(path.Join(dir, "src")); f != "" {
			return f
		}
		return l.file(dir)
	}
	// Rails importmap: an exact pin, or a pin_all_from prefix.
	if t, ok := l.importmap[spec]; ok {
		return t
	}
	for prefix, dir := range l.importmap {
		if strings.HasSuffix(prefix, "/") && strings.HasPrefix(spec, prefix) {
			if f := l.file(path.Join(dir, strings.TrimPrefix(spec, prefix))); f != "" {
				return f
			}
		}
	}
	return ""
}

// matchStar applies a "prefix*suffix" pattern mapping (tsconfig paths,
// package.json imports) to spec, or returns "" if spec doesn't match.
func matchStar(pattern, target, spec string) string {
	pre, post, star := strings.Cut(pattern, "*")
	if !star {
		if spec == pattern {
			return target
		}
		return ""
	}
	if !strings.HasPrefix(spec, pre) || !strings.HasSuffix(spec, post) || len(spec) < len(pre)+len(post) {
		return ""
	}
	return strings.Replace(target, "*", spec[len(pre):len(spec)-len(post)], 1)
}

// file finds the file module path p means, the way Node and TypeScript
// look: p itself, p with an extension, p/index with one — and, for TS's
// ESM style, a ".js" import of a ".ts" file.
func (l *nodeLang) file(p string) string {
	p = path.Clean(p)
	if strings.HasPrefix(p, "../") || p == ".." {
		return ""
	}
	if l.idx.has(p) {
		return p
	}
	for _, e := range nodeExts {
		if l.idx.has(p + e) {
			return p + e
		}
	}
	for _, e := range nodeExts {
		if l.idx.has(p + "/index" + e) {
			return p + "/index" + e
		}
	}
	for js, tss := range map[string][]string{".js": {".ts", ".tsx"}, ".jsx": {".tsx"}, ".mjs": {".mts"}, ".cjs": {".cts"}} {
		if strings.HasSuffix(p, js) {
			for _, ts := range tss {
				if f := strings.TrimSuffix(p, js) + ts; l.idx.has(f) {
					return f
				}
			}
		}
	}
	return ""
}

// tsConfig is what resolution needs from a tsconfig.json/jsconfig.json:
// baseUrl and paths, made relative to the repo root.
type tsConfig struct {
	baseURL   string              // "" when not set
	paths     map[string][]string // pattern → targets
	pathsBase string              // what paths targets are relative to
}

// tsconfigFor is the config governing files in dir: the nearest
// tsconfig.json or jsconfig.json up the tree.
func (l *nodeLang) tsconfigFor(dir string) *tsConfig {
	if tc, ok := l.tsconfigs[dir]; ok {
		return tc
	}
	var tc *tsConfig
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		p := path.Join(dir, name)
		if l.idx.has(p) {
			tc = l.readTSConfig(p, 0)
			break
		}
	}
	if tc == nil && dir != "." {
		tc = l.tsconfigFor(path.Dir(dir))
	}
	l.tsconfigs[dir] = tc
	return tc
}

func (l *nodeLang) readTSConfig(p string, depth int) *tsConfig {
	src := l.idx.read(p)
	if src == nil || depth > 5 {
		return nil
	}
	var doc struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal([]byte(stripJSONC(*src)), &doc) != nil {
		return nil
	}
	dir := path.Dir(p)
	tc := &tsConfig{paths: map[string][]string{}, pathsBase: dir}
	// extends: a relative path within the repo (packages from npm aren't read).
	var parents []string
	var one string
	if json.Unmarshal(doc.Extends, &one) == nil {
		parents = []string{one}
	} else {
		_ = json.Unmarshal(doc.Extends, &parents)
	}
	for _, e := range parents {
		if !strings.HasPrefix(e, ".") {
			continue
		}
		ep := path.Join(dir, e)
		if !strings.HasSuffix(ep, ".json") {
			ep += ".json"
		}
		if parent := l.readTSConfig(ep, depth+1); parent != nil {
			*tc = *parent
			tc.paths = map[string][]string{}
			for k, v := range parent.paths {
				tc.paths[k] = v
			}
		}
	}
	if doc.CompilerOptions.BaseURL != nil {
		tc.baseURL = path.Join(dir, *doc.CompilerOptions.BaseURL)
		tc.pathsBase = tc.baseURL
	}
	if doc.CompilerOptions.Paths != nil {
		tc.paths = doc.CompilerOptions.Paths
		if doc.CompilerOptions.BaseURL == nil && tc.baseURL == "" {
			tc.pathsBase = dir
		}
	}
	return tc
}

// stripJSONC turns JSON with comments and trailing commas (tsconfig's
// dialect) into plain JSON.
func stripJSONC(s string) string {
	var b strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
			b.WriteByte(c)
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 3
		default:
			b.WriteByte(c)
		}
	}
	return trailingCommaRe.ReplaceAllString(b.String(), "$1")
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

func init() {
	shared := []string{"src/components", "src/lib", "src/utils", "src/hooks", "components", "lib", "utils", "hooks"}
	presets["node"] = preset{
		lang:   "node",
		detect: func(idx *index) bool { return idx.has("package.json") },
		layers: []layer{
			{Name: "shared code", Paths: shared, Deny: []string{"src/pages", "src/routes", "src/views", "src/app", "pages", "routes", "views", "app"}},
		},
	}
}
