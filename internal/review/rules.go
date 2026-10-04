package review

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
)

// RulesPath is the layer rules file, relative to the repo root. It lives in
// the repo so a team shares it and reviews changes to it like code.
const RulesPath = ".unky-mo/architecture.toml"

// layer is one [[layer]] of the rules file: the packages under any of Paths
// may import only Allow (when set) and never Deny, besides their own layer.
// Paths are package directories relative to the repo root; a prefix covers
// its subpackages.
type layer struct {
	Name  string   `toml:"name"`
	Paths []string `toml:"paths"`
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

type ruleSet struct {
	info   Rules
	layers []layer
}

// loadRules reads the rules file of the checkout at root. A missing file
// means no rules; a broken one is reported in info.Error and ignored.
func loadRules(root string) *ruleSet {
	rs := &ruleSet{info: Rules{Path: RulesPath}}
	c, err := gitfiles.ReadFile(root, RulesPath)
	if err != nil || !c.Exists {
		return rs
	}
	rs.info.Found = true
	if c.Binary || c.TooLarge {
		rs.info.Error = "not a text file"
		return rs
	}
	var doc struct {
		Layer []layer `toml:"layer"`
	}
	md, err := toml.Decode(c.Text, &doc)
	if err != nil {
		rs.info.Error = err.Error()
		return rs
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		rs.info.Error = fmt.Sprintf("unknown key %q", undec[0].String())
		return rs
	}
	for i, l := range doc.Layer {
		if len(l.Paths) == 0 {
			rs.info.Error = fmt.Sprintf("layer %d (%q) has no paths", i+1, l.Name)
			return rs
		}
		if l.Name == "" {
			doc.Layer[i].Name = l.Paths[0]
		}
	}
	rs.layers = doc.Layer
	rs.info.Layers = len(doc.Layer)
	return rs
}

// under reports whether package p is prefix or one of its subpackages.
func under(p, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return p == prefix || prefix == "." || strings.HasPrefix(p, prefix+"/")
}

func longestMatch(p string, prefixes []string) int {
	best := -1
	for _, pre := range prefixes {
		if under(p, pre) && len(pre) > best {
			best = len(pre)
		}
	}
	return best
}

// layerOf is the layer whose paths match p most specifically, or nil.
func (rs *ruleSet) layerOf(p string) *layer {
	var best *layer
	bestLen := -1
	for i := range rs.layers {
		if n := longestMatch(p, rs.layers[i].Paths); n > bestLen {
			best, bestLen = &rs.layers[i], n
		}
	}
	return best
}

// check returns the rule an import from package from to package to breaks,
// in words, or "" if it's allowed.
func (rs *ruleSet) check(from, to string) string {
	l := rs.layerOf(from)
	if l == nil || rs.layerOf(to) == l {
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
