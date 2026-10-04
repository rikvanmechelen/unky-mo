package review

import (
	"context"
	"fmt"
	"sort"
	"strings"

	moexec "github.com/rvanmech/unky-mo/internal/exec"
)

// DraftRules writes a starting rules file for the checkout at root: the
// presets its stack gets (with only overrides set explicitly), and, as
// comments, every dependency between its units today, so a team can turn
// what it sees into layers. Nothing in it flags existing code: only
// dependencies a change adds are ever checked.
func DraftRules(ctx context.Context, cmd moexec.Commander, root string, chosen []string) (string, error) {
	r := &repo{ctx: ctx, cmd: cmd, root: root}
	idx := newIndex(r)
	if len(idx.paths) == 0 {
		return "", fmt.Errorf("no files found in %s", root)
	}
	var detected []string
	for _, name := range sortedKeys(presets) {
		if p := presets[name]; p.detect != nil && p.detect(idx) {
			detected = append(detected, name)
		}
	}
	names := detected
	if chosen != nil {
		for _, n := range chosen {
			if _, ok := presets[n]; !ok {
				return "", fmt.Errorf("unknown preset %q (have: %s)", n, strings.Join(sortedKeys(presets), ", "))
			}
		}
		names = chosen
	}

	var b strings.Builder
	b.WriteString("# Layer rules for unky-mo's Overview: a change that *adds* a dependency\n")
	b.WriteString("# breaking one of these rules shows in red. Existing dependencies are never\n")
	b.WriteString("# flagged.\n#\n")
	b.WriteString("# A unit belongs to the layer with the longest matching path; \"*\" matches one\n")
	b.WriteString("# path segment. `allow` lists the only units a layer may depend on (besides\n")
	b.WriteString("# itself); `deny` lists units it must not depend on.\n\n")
	if len(detected) > 0 {
		fmt.Fprintf(&b, "# Built-in rules detected for this repo: %s. Without a `presets` line they\n", strings.Join(detected, ", "))
		b.WriteString("# apply automatically; set it to choose (or `presets = []` to turn them off).\n")
	}
	if chosen != nil {
		fmt.Fprintf(&b, "presets = [%s]\n\n", quoteList(names))
	} else {
		// Left out, the detected presets keep applying as the stack changes.
		fmt.Fprintf(&b, "# presets = [%s]\n\n", quoteList(names))
	}
	b.WriteString("# Example:\n# [[layer]]\n# name = \"domain\"\n# paths = [\"app/models\"]\n# deny = [\"app/controllers\", \"app/views\"]\n")

	// Existing dependencies that already break a preset rule are marked:
	// they won't be flagged, but they show where the code stands.
	rs := &ruleSet{}
	for _, name := range names {
		for _, ly := range presets[name].layers {
			ly.Name, ly.lang = name+": "+ly.Name, presets[name].lang
			rs.layers = append(rs.layers, ly)
		}
	}
	broken := 0
	for _, l := range languages() {
		if !l.detect(idx) {
			continue
		}
		edges := map[string]map[string]int{}
		for _, p := range idx.paths {
			if !l.owns(p) || isTest(p) {
				continue
			}
			u := l.unit(p)
			if u == "" {
				continue
			}
			src := idx.read(p)
			if src == nil {
				continue
			}
			refs, _ := l.refs(p, *src)
			for _, rf := range refs {
				if rf.to == u {
					continue
				}
				if edges[u] == nil {
					edges[u] = map[string]int{}
				}
				edges[u][rf.to]++
			}
		}
		if len(edges) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n# Dependencies between %s units today (unit -> what it depends on):\n", l.name())
		for _, from := range sortedKeys(edges) {
			tos := sortedKeys(edges[from])
			sort.Strings(tos)
			for i, to := range tos {
				if rule := rs.check(from, to, l.name()); rule != "" {
					tos[i] = to + " [breaks " + rule + "]"
					broken++
				}
			}
			fmt.Fprintf(&b, "#   %s -> %s\n", from, strings.Join(tos, ", "))
		}
	}
	if broken > 0 {
		fmt.Fprintf(&b, "\n# %d existing dependencies break a preset rule (marked [breaks …]). They aren't\n", broken)
		b.WriteString("# flagged; a change that adds another one is.\n")
	}
	return b.String(), nil
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}
