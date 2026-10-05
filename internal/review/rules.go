package review

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// RulesPath is the layer rules file, relative to the repo root. It lives in
// the repo so a team shares it and reviews changes to it like code.
const RulesPath = ".unky-mo/architecture.toml"

// layer is one [[layer]] of the rules file (or of a preset): the units under
// any of Paths may import only Allow (when set) and never Deny, besides
// their own layer. Paths are unit paths relative to the repo root; a prefix
// covers its subunits, and a "*" segment matches any one segment
// ("*/model" is every Gradle module's model package).
type layer struct {
	Name  string   `toml:"name"`
	Paths []string `toml:"paths"`
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
	// lang limits a preset's layer to one language's units ("" for the
	// repo's own layers: they apply to every language).
	lang string
}

// preset is a built-in rule set for a kind of project. Presets apply on
// their own when the stack is detected, so they only hold rules that are
// wrong in any codebase of that kind.
type preset struct {
	lang   string // the language whose units it judges
	detect func(idx *index) bool
	layers []layer
}

// presets are the built-in rule sets, by name. Each language step adds its
// stack's.
var presets = map[string]preset{}

type ruleSet struct {
	info   Rules
	layers []layer
}

// loadRules reads the rules file of the change's new version (the
// checkout, or the head commit) and adds the presets it names — or, when it
// doesn't say (or there's no file), the presets whose stack idx shows. A
// broken file is reported in info.Error and ignored; the automatic presets
// still apply.
func loadRules(r *repo, idx *index) *ruleSet {
	rs := &ruleSet{info: Rules{Path: RulesPath, Presets: []string{}}}
	var names []string
	chosen := false
	if text := r.readAfter(RulesPath); text != nil {
		rs.info.Found = true
		var doc struct {
			Presets *[]string `toml:"presets"`
			Layer   []layer   `toml:"layer"`
		}
		layers, err := decodeRules(*text, &doc)
		if err != "" {
			rs.info.Error = err
		} else {
			rs.layers = layers
			if doc.Presets != nil {
				names, chosen = *doc.Presets, true
			}
		}
	}
	rs.info.Layers = len(rs.layers)
	if !chosen {
		for _, name := range sortedKeys(presets) {
			if p := presets[name]; p.detect != nil && p.detect(idx) {
				names = append(names, name)
			}
		}
		rs.info.AutoPresets = len(names) > 0
	}
	for _, name := range names {
		p, ok := presets[name]
		if !ok {
			if rs.info.Error == "" {
				rs.info.Error = fmt.Sprintf("unknown preset %q", name)
			}
			continue
		}
		rs.info.Presets = append(rs.info.Presets, name)
		for _, l := range p.layers {
			l.Name = name + ": " + l.Name
			l.lang = p.lang
			rs.layers = append(rs.layers, l) // after the repo's own: they win ties
		}
	}
	sort.Strings(rs.info.Presets)
	rs.info.Order = []string{}
	for _, l := range rs.layers {
		if !slices.Contains(rs.info.Order, l.Name) {
			rs.info.Order = append(rs.info.Order, l.Name)
		}
	}
	return rs
}

// decodeRules parses a rules file into its layers, or says why it can't.
func decodeRules(text string, doc *struct {
	Presets *[]string `toml:"presets"`
	Layer   []layer   `toml:"layer"`
}) ([]layer, string) {
	md, err := toml.Decode(text, doc)
	if err != nil {
		return nil, err.Error()
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		return nil, fmt.Sprintf("unknown key %q", undec[0].String())
	}
	for i, l := range doc.Layer {
		if len(l.Paths) == 0 {
			return nil, fmt.Sprintf("layer %d (%q) has no paths", i+1, l.Name)
		}
		if l.Name == "" {
			doc.Layer[i].Name = l.Paths[0]
		}
	}
	return doc.Layer, ""
}

// under reports whether unit p is pattern or one of its subunits. Each
// pattern segment is a glob ("*" any one segment, "ui-*" any segment
// starting so), "**" matches any number of segments ("**/data" is a data
// package at any depth), and "." matches everything.
func under(p, pattern string) bool {
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "." || pattern == "" {
		return true
	}
	return matchSegs(strings.Split(p, "/"), strings.Split(pattern, "/"))
}

// matchSegs reports whether pattern segments qs match a prefix of ps.
func matchSegs(ps, qs []string) bool {
	if len(qs) == 0 {
		return true
	}
	if qs[0] == "**" {
		for i := 0; i <= len(ps); i++ {
			if matchSegs(ps[i:], qs[1:]) {
				return true
			}
		}
		return false
	}
	if len(ps) == 0 {
		return false
	}
	if ok, _ := path.Match(qs[0], ps[0]); !ok {
		return false
	}
	return matchSegs(ps[1:], qs[1:])
}

// longestMatch is the length of the most specific pattern p is under, or
// -1. Literal segments count more than "*", so "app/model" beats "*/model".
func longestMatch(p string, patterns []string) int {
	best := -1
	for _, pat := range patterns {
		if !under(p, pat) {
			continue
		}
		n := 0
		for _, seg := range strings.Split(strings.TrimSuffix(pat, "/"), "/") {
			switch seg {
			case "**":
			case "*":
				n++
			default:
				n += 1 + len(seg)
			}
		}
		if n > best {
			best = n
		}
	}
	return best
}

// layerOf is the layer whose paths match unit p of language lang most
// specifically, or nil. On a tie the earlier layer wins: the repo's own
// before the presets'.
func (rs *ruleSet) layerOf(p, lang string) *layer {
	var best *layer
	bestLen := -1
	for i := range rs.layers {
		if l := rs.layers[i].lang; l != "" && l != lang {
			continue
		}
		if n := longestMatch(p, rs.layers[i].Paths); n > bestLen {
			best, bestLen = &rs.layers[i], n
		}
	}
	return best
}

// check returns the rule a dependency of language lang from unit from to
// unit to breaks, in words, or "" if it's allowed.
func (rs *ruleSet) check(from, to, lang string) string {
	l := rs.layerOf(from, lang)
	if l == nil || rs.layerOf(to, lang) == l {
		return ""
	}
	if longestMatch(to, l.Deny) >= 0 {
		return fmt.Sprintf("%s must not import %s", l.Name, to)
	}
	if len(l.Allow) > 0 && longestMatch(to, l.Allow) < 0 {
		return fmt.Sprintf("%s may only import %s", l.Name, strings.Join(l.Allow, ", "))
	}
	return ""
}
