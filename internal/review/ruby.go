package review

import (
	"path"
	"regexp"
	"strings"

	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// rubyLang analyzes a Rails app. Units are Rails' own folders (app/models,
// app/services, …, lib/tasks, config, db), as gitfiles.AreaOf names them.
// Ruby has no imports, so references are constants (and model
// associations), resolved to files by Rails' autoloading convention
// (Zeitwerk: app/models/museum_location.rb defines MuseumLocation). That's
// inferred from names, so the edges are approximate.
//
// The pieces are kept separate so Rails support can grow: railsRoots
// (where constants live), rubyRefsIn (what a file references) and
// rails_surface.go (the contract surface).
type rubyLang struct {
	// consts maps a squashed constant path ("audio/tourbuilder") to the
	// unit of the file defining it; "" when two units define it.
	consts map[string]string
}

func (l *rubyLang) name() string { return "ruby" }
func (l *rubyLang) exact() bool  { return false }

// isRailsApp reports whether the repo is a Rails app.
func isRailsApp(idx *index) bool {
	return idx.has("Gemfile") && (idx.has("config/application.rb") || idx.hasDir("app/models") || idx.hasDir("app/controllers"))
}

func (l *rubyLang) detect(idx *index) bool {
	if !isRailsApp(idx) {
		return false
	}
	l.consts = map[string]string{}
	for _, p := range idx.paths {
		if !strings.HasSuffix(p, ".rb") || isTest(p) {
			continue
		}
		for _, root := range railsRoots(p) {
			key := squashConst(strings.TrimSuffix(strings.TrimPrefix(p, root+"/"), ".rb"))
			unit := gitfiles.AreaOf(p)
			if prev, ok := l.consts[key]; ok && prev != unit {
				unit = "" // defined in two units: ambiguous
			}
			l.consts[key] = unit
		}
	}
	return true
}

// railsRoots are the autoload roots a file is under: every app/* folder and
// its concerns/, and lib (minus what isn't autoloaded: tasks, assets,
// generators). A concern is under both app/models and app/models/concerns.
func railsRoots(p string) []string {
	segs := strings.Split(p, "/")
	switch {
	case len(segs) >= 3 && segs[0] == "app":
		roots := []string{"app/" + segs[1]}
		if len(segs) >= 4 && segs[2] == "concerns" {
			roots = append(roots, "app/"+segs[1]+"/concerns")
		}
		return roots
	case len(segs) >= 2 && segs[0] == "lib" && segs[1] != "tasks" && segs[1] != "assets" && segs[1] != "generators":
		return []string{"lib"}
	}
	return nil
}

// squashConst normalizes a constant path or file path for matching:
// lowercase, without underscores, segments joined by "/". MuseumLocation,
// museum_location and HTTPClient/http_client all meet, whatever acronym
// inflections the app configured.
func squashConst(s string) string {
	s = strings.ReplaceAll(s, "::", "/")
	return strings.ToLower(strings.ReplaceAll(s, "_", ""))
}

func (l *rubyLang) owns(p string) bool {
	switch path.Ext(p) {
	case ".rb", ".rake", ".erb", ".haml", ".slim", ".jbuilder":
		return true
	}
	return false
}

func (l *rubyLang) unit(p string) string {
	if a := gitfiles.AreaOf(p); a != "." {
		return a
	}
	return ""
}

func (l *rubyLang) refs(p, src string) ([]ref, bool) {
	return l.resolve(p, l.scan(p, src)), true
}

// scan lists the constants (and association models) a file names, before
// resolution: it depends only on the file's content.
func (l *rubyLang) scan(p, src string) any {
	var code string
	switch path.Ext(p) {
	case ".erb":
		code = erbCode(src)
	case ".haml", ".slim":
		code = hamlCode(src)
	default:
		code = rubyCode(src)
	}
	return rubyRefsIn(code, src)
}

// resolve turns scanned constants into the units defining them.
func (l *rubyLang) resolve(p string, scanned any) []ref {
	consts, _ := scanned.([]ref)
	nesting := rubyNesting(p)
	var out []ref
	for _, c := range consts {
		if u, ok := l.lookup(c.to, nesting); ok {
			out = append(out, ref{to: u, line: c.line})
		}
	}
	return out
}

// rubyNesting is the lexical nesting a file's code runs in, from its path
// (Zeitwerk: app/services/audio/tour_builder.rb is Audio::TourBuilder):
// "audio/tourbuilder", "audio", "". Files outside autoload roots (views,
// config, rake tasks) only see the top level.
func rubyNesting(p string) []string {
	roots := railsRoots(p)
	if len(roots) == 0 || !strings.HasSuffix(p, ".rb") {
		return []string{""}
	}
	root := roots[len(roots)-1] // the most specific (concerns/)
	segs := strings.Split(squashConst(strings.TrimSuffix(strings.TrimPrefix(p, root+"/"), ".rb")), "/")
	var out []string
	for i := len(segs); i > 0; i-- {
		out = append(out, strings.Join(segs[:i], "/"))
	}
	return append(out, "")
}

// lookup finds the unit defining constant c, looked up like Ruby does:
// in each enclosing namespace, innermost first, then at the top level. A
// reference to something inside a class (Artwork::STATUSES) resolves to
// the class's file. ::Foo only looks at the top level.
func (l *rubyLang) lookup(c string, nesting []string) (string, bool) {
	if strings.HasPrefix(c, "::") {
		c, nesting = c[2:], []string{""}
	}
	segs := strings.Split(squashConst(c), "/")
	for _, ns := range nesting {
		for k := len(segs); k > 0; k-- {
			key := strings.Join(segs[:k], "/")
			if ns != "" {
				key = ns + "/" + key
			}
			if u, ok := l.consts[key]; ok {
				return u, u != ""
			}
		}
	}
	return "", false
}

var (
	associationRe = regexp.MustCompile(`(?m)\b(belongs_to|has_one|has_many|has_and_belongs_to_many)\s+:(\w+)`)
	classNameRe   = regexp.MustCompile(`class_name:\s*(?:["']((?:::)?[A-Z][\w:]*)["']|((?:::)?[A-Z][\w:]*)\.(?:name|to_s))`)
)

// rubyRefsIn lists the constants a file's scanned code references, plus
// the models its association macros name (has_many :tours → Tour, or the
// class_name: given, read from src since strings are blanked in code).
func rubyRefsIn(code, src string) []ref {
	out := rubyConstants(code)
	for _, m := range associationRe.FindAllStringSubmatchIndex(code, -1) {
		line := 1 + strings.Count(code[:m[0]], "\n")
		srcLine := lineAt(src, m[0])
		if strings.Contains(srcLine, "polymorphic: true") {
			continue
		}
		name := code[m[4]:m[5]]
		if cn := classNameRe.FindStringSubmatch(srcLine); cn != nil {
			out = append(out, ref{to: cn[1] + cn[2], line: line})
			continue
		}
		if macro := code[m[2]:m[3]]; macro == "has_many" || macro == "has_and_belongs_to_many" {
			name = singularize(name)
		}
		out = append(out, ref{to: name, line: line}) // squashed on lookup: tour_stop matches TourStop
	}
	return out
}

// lineAt is the line of s containing byte offset i.
func lineAt(s string, i int) string {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	end := strings.IndexByte(s[i:], '\n')
	if end < 0 {
		return s[start:]
	}
	return s[start : i+end]
}

// singularize turns an association name into its model's, for the regular
// English plurals Rails' inflector handles by default.
func singularize(w string) string {
	switch {
	case w == "people":
		return "person"
	case strings.HasSuffix(w, "ies") && len(w) > 3:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"), strings.HasSuffix(w, "zzes"):
		return w[:len(w)-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

func init() {
	ui := []string{"app/controllers", "app/views", "app/helpers", "app/components"}
	presets["rails"] = preset{
		lang:   "ruby",
		detect: isRailsApp,
		layers: []layer{
			{Name: "models", Paths: []string{"app/models"}, Deny: ui},
			{Name: "services and jobs", Paths: []string{"app/services", "app/jobs"}, Deny: ui},
		},
	}
}
