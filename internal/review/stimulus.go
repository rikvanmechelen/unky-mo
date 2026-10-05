package review

import (
	"path"
	"regexp"
	"sort"
	"strings"
)

// Stimulus links: a Rails view's data-controller / data-action /
// data-*-target attributes bind a JS controller's class, methods and
// targets. The view side is read by the Ruby analyzer's scan (rbStimulus),
// as calls on the receiver "stimulus:<identifier>"; the controller side
// comes from the JS analyzer's cached scans of the controller files, read
// with the same symbols key so the cache is shared. Resolution maps a
// binding to the JS function's ID: a call from Ruby into JS, which the
// cross-language pass (crosscalls.go) follows.

const (
	stimRecv       = "stimulus:"    // receiver prefix of a binding
	stimController = "<controller>" // data-controller: the controller class
	stimAction     = "action:"      // data-action: action:<method>
	stimTarget     = "target:"      // data-<id>-target: target:<name>
)

// stimulusIdentifier is the identifier Stimulus registers a controller file
// under: its path below app/javascript/**/controllers/ (or app/components/),
// minus _controller, with "_" → "-" and "/" → "--". ok is false for a file
// that isn't a controller.
func stimulusIdentifier(p string) (string, bool) {
	ext := path.Ext(p)
	switch ext {
	case ".js", ".mjs", ".ts", ".jsx", ".tsx":
	default:
		return "", false
	}
	rel := ""
	slashed := "/" + p
	switch {
	case strings.Contains(slashed, "/app/javascript/"):
		rest := slashed[strings.Index(slashed, "/app/javascript/")+len("/app/javascript"):]
		i := strings.LastIndex(rest, "/controllers/")
		if i < 0 {
			return "", false
		}
		rel = rest[i+len("/controllers/"):]
	case strings.Contains(slashed, "/app/components/"):
		rel = slashed[strings.Index(slashed, "/app/components/")+len("/app/components/"):]
	default:
		return "", false
	}
	rel = strings.TrimSuffix(rel, ext)
	switch {
	case strings.HasSuffix(rel, "_controller"):
		rel = strings.TrimSuffix(rel, "_controller")
	case strings.HasSuffix(rel, "-controller"):
		rel = strings.TrimSuffix(rel, "-controller")
	default:
		return "", false
	}
	if rel == "" || strings.HasSuffix(rel, "/") {
		return "", false
	}
	return strings.ReplaceAll(strings.ReplaceAll(rel, "_", "-"), "/", "--"), true
}

// stimCtl is one controller file.
type stimCtl struct {
	ident, path string
	// class is the controller class's owner path in the file ("default" for
	// an anonymous default export), "" if none was found.
	class   string
	def     *hDef
	methods map[string]*hDef
	targets map[string]bool
	// inherits is set when the class extends something besides Stimulus'
	// Controller: methods and targets may come from the base, so a missing
	// one isn't known to be missing.
	inherits bool
	file     *hFile
}

// stimIndex is a version's controllers.
type stimIndex struct {
	byIdent map[string]*stimCtl
	byStem  map[string]*stimCtl // path without extension → controller
}

// stimulusIndex reads idx's controllers from the JS scans.
func stimulusIndex(idx *index) *stimIndex {
	x := &stimIndex{byIdent: map[string]*stimCtl{}, byStem: map[string]*stimCtl{}}
	type file struct{ p, ident, key string }
	var files []file
	var reads []string
	for _, p := range idx.paths {
		if ident, ok := stimulusIdentifier(p); ok {
			key := "hcalls:node" + path.Ext(p) // jsCalls' key in newHResolver
			files = append(files, file{p, ident, key})
			if !idx.cached(key, p) {
				reads = append(reads, p)
			}
		}
	}
	idx.prefetch(reads)
	for _, f := range files {
		v := idx.symbols(f.key, f.p, func(src string) any { s := (&jsCalls{}).scanFile(f.p, src); return &s })
		hf, _ := v.(*hFile)
		if hf == nil || !hf.OK {
			continue
		}
		c := &stimCtl{ident: f.ident, path: f.p, methods: map[string]*hDef{}, targets: map[string]bool{}, file: hf}
		c.class = stimClass(hf)
		for i := range hf.Defs {
			d := &hf.Defs[i]
			if d.IsClass && qualify(d.Owner, d.Name) == c.class {
				c.def = d
			}
			if c.class != "" && d.Owner == c.class && !d.IsClass && !d.Static {
				c.methods[d.Name] = d
			}
		}
		for _, cl := range hf.Classes {
			if cl.Name != c.class {
				continue
			}
			for _, b := range cl.Bases {
				if b != "Controller" {
					c.inherits = true
				}
			}
		}
		for _, t := range hf.Targets[c.class] {
			c.targets[t] = true
		}
		if _, dup := x.byIdent[f.ident]; !dup { // controllers/ before components/ by path order
			x.byIdent[f.ident] = c
		}
		x.byStem[strings.TrimSuffix(f.p, path.Ext(f.p))] = c
	}
	return x
}

// stimClass is the controller class of a scanned JS file: the default
// export's class, else the only top-level class.
func stimClass(f *hFile) string {
	classes := map[string]bool{}
	var top []string
	for _, d := range f.Defs {
		if d.IsClass {
			classes[qualify(d.Owner, d.Name)] = true
			if d.Owner == "" {
				top = append(top, d.Name)
			}
		}
	}
	if n := f.Exports["default"]; classes[n] {
		return n
	}
	if classes["default"] {
		return "default"
	}
	if len(top) == 1 {
		return top[0]
	}
	return ""
}

func (c *stimCtl) id(d *hDef) string { return (&jsCalls{}).id(c.path, d) }

// --- The view side ---

var (
	stimAttrRe = regexp.MustCompile(`\bdata-(controller|action|[a-z0-9][a-z0-9-]*?-target)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	// "data-controller" => "x" (a HAML/Ruby hash with string keys).
	stimRocketRe = regexp.MustCompile(`["'](?:data-)(controller|action|[a-z0-9][a-z0-9-]*?-target)["']\s*=>\s*(?:"([^"]*)"|'([^']*)')`)
	// data_controller: "x" (helpers that dasherize keyword keys).
	stimDataKwRe = regexp.MustCompile(`\bdata_(controller|action|[a-z0-9_]+?_target)\s*:\s*(?:"([^"]*)"|'([^']*)')`)
	// data: { … } / :data => { … } / "data" => { … }
	stimDataHashRe = regexp.MustCompile(`(?:\bdata\s*:|:data\s*=>|["']data["']\s*=>)\s*\{`)
	// One entry of a data hash: controller: "x", "cart-target": "y",
	// :action => "z".
	stimHashKeyRe = regexp.MustCompile(`(?:["':]?)([a-z0-9][a-z0-9_-]*)["']?\s*(?::|=>)\s*(?:"([^"]*)"|'([^']*)')`)
	stimIdentRe   = regexp.MustCompile(`^[a-z0-9]+(?:-{1,2}[a-z0-9]+)*$`)
	stimMethodRe  = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// stimBinding is one binding found in a template's source.
type stimBinding struct {
	ident, name string // name: stimController, stimAction+method, stimTarget+target
	line        int
}

// stimulusBindings reads the Stimulus bindings in a template's or Ruby
// file's original source (attributes and Ruby hash forms), in order.
func stimulusBindings(src string) []stimBinding {
	if !strings.Contains(src, "data") {
		return nil
	}
	lineAt := func(off int) int { return 1 + strings.Count(src[:off], "\n") }
	type hit struct {
		at       int
		key, val string
	}
	var hits []hit
	for _, re := range []*regexp.Regexp{stimAttrRe, stimRocketRe, stimDataKwRe} {
		for _, m := range re.FindAllStringSubmatchIndex(src, -1) {
			val := ""
			if m[4] >= 0 {
				val = src[m[4]:m[5]]
			} else if m[6] >= 0 {
				val = src[m[6]:m[7]]
			}
			hits = append(hits, hit{m[0], strings.ReplaceAll(src[m[2]:m[3]], "_", "-"), val})
		}
	}
	for _, m := range stimDataHashRe.FindAllStringIndex(src, -1) {
		open := m[1] - 1
		closeAt := matchingBrace(src, open)
		if closeAt < 0 {
			continue
		}
		body := src[open+1 : closeAt]
		for _, k := range stimHashKeyRe.FindAllStringSubmatchIndex(body, -1) {
			key := strings.ReplaceAll(body[k[2]:k[3]], "_", "-")
			if key != "controller" && key != "action" && !strings.HasSuffix(key, "-target") {
				continue
			}
			val := ""
			if k[4] >= 0 {
				val = body[k[4]:k[5]]
			} else if k[6] >= 0 {
				val = body[k[6]:k[7]]
			}
			hits = append(hits, hit{open + 1 + k[0], key, val})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	var out []stimBinding
	for _, h := range hits {
		if strings.Contains(h.val, "<%") || strings.Contains(h.val, "#{") || strings.Contains(h.val, "{{") {
			continue // built at runtime
		}
		ln := lineAt(h.at)
		switch {
		case h.key == "controller":
			for _, id := range strings.Fields(h.val) {
				if stimIdentRe.MatchString(id) {
					out = append(out, stimBinding{id, stimController, ln})
				}
			}
		case h.key == "action":
			for _, desc := range strings.Fields(h.val) {
				if i := strings.Index(desc, "->"); i >= 0 {
					desc = desc[i+2:]
				}
				id, method, ok := strings.Cut(desc, "#")
				if j := strings.IndexByte(method, ':'); j >= 0 {
					method = method[:j] // :prevent, :stop, :once
				}
				if ok && stimIdentRe.MatchString(id) && stimMethodRe.MatchString(method) {
					out = append(out, stimBinding{id, stimAction + method, ln})
				}
			}
		default: // <identifier>-target
			id := strings.TrimSuffix(h.key, "-target")
			for _, t := range strings.Fields(h.val) {
				if stimIdentRe.MatchString(id) && stimMethodRe.MatchString(t) {
					out = append(out, stimBinding{id, stimTarget + t, ln})
				}
			}
		}
	}
	return out
}

// matchingBrace is the index of the "}" closing the "{" at open, skipping
// quoted strings, or -1.
func matchingBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return -1
			}
			i += j + 1
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// rbStimulus adds a scanned file's Stimulus bindings to its definitions as
// calls on "stimulus:<identifier>": a template's to its one definition, a
// Ruby file's (a ViewComponent's, a helper's) to the innermost definition
// around each.
func rbStimulus(f *hFile, src string, view bool) {
	bs := stimulusBindings(src)
	if len(bs) == 0 || len(f.Defs) == 0 {
		return
	}
	for _, b := range bs {
		c := hCall{Recv: stimRecv + b.ident, Name: b.name, Line: b.line}
		if view {
			f.Defs[0].Calls = append(f.Defs[0].Calls, c)
			continue
		}
		best := -1
		for i, d := range f.Defs {
			if d.Line <= b.line && b.line <= d.End && (best < 0 || d.End-d.Line < f.Defs[best].End-f.Defs[best].Line) {
				best = i
			}
		}
		if best >= 0 {
			f.Defs[best].Calls = append(f.Defs[best].Calls, c)
		}
	}
}

// --- The JS side: static targets ---

var stimTargetsRe = regexp.MustCompile(`static\s+targets\s*=\s*\[`)
var jsQuotedRe = regexp.MustCompile(`["']([\w$-]+)["']`)

// stimulusTargets records each class's static targets = [...] in the scan
// (Stimulus targets are declared, never defined as functions).
func (sc *jsScanner) stimulusTargets() {
	if !strings.Contains(sc.cc, "targets") {
		return
	}
	for _, m := range stimTargetsRe.FindAllStringIndex(sc.cc, -1) {
		open := m[1] - 1
		closeAt := sc.closer(open)
		if closeAt < 0 {
			continue
		}
		owner, size := "", -1
		for _, c := range sc.cands {
			if c.class && c.body < m[0] && m[0] < c.end && (size < 0 || c.end-c.body < size) {
				owner, size = c.path, c.end-c.body
			}
		}
		if owner == "" {
			continue
		}
		if sc.f.Targets == nil {
			sc.f.Targets = map[string][]string{}
		}
		for _, q := range jsQuotedRe.FindAllStringSubmatch(sc.code[open:closeAt], -1) {
			sc.f.Targets[owner] = append(sc.f.Targets[owner], q[1])
		}
	}
}
