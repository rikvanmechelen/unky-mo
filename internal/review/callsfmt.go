package review

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Text and Graphviz renderings of a call graph, for `mo calls`.

var callMark = map[string]string{
	FuncAdded: "+", FuncRemoved: "-", FuncRenamed: ">", FuncSignature: "!", FuncChanged: "~",
}

// WriteCallsText writes the changed functions by unit with their status, the
// calls each gains and drops, how many callers it has, then the findings.
func WriteCallsText(w io.Writer, cg *CallGraph) {
	funcs := map[string]Func{}
	for _, f := range cg.Funcs {
		funcs[f.ID] = f
	}
	name := func(id string) string {
		if f, ok := funcs[id]; ok && f.Name != "" {
			return f.Name
		}
		return shortName(id)
	}
	out := map[string][]Call{}
	callers := map[string]int{}
	for _, c := range cg.Calls {
		if c.Kind == CallImpl {
			continue
		}
		if c.Op != "" {
			out[c.From] = append(out[c.From], c)
		}
		if c.Op != OpRemoved {
			callers[c.To]++
		}
	}
	byUnit := map[string][]Func{}
	for _, f := range cg.Funcs {
		if f.Status != "" {
			byUnit[f.Unit] = append(byUnit[f.Unit], f)
		}
	}
	if len(byUnit) == 0 {
		fmt.Fprintln(w, "No functions added, removed or changed.")
	}
	for _, u := range sortedKeys(byUnit) {
		label := u
		if label == "" {
			label = "(root)"
		}
		fmt.Fprintln(w, label)
		fs := byUnit[u]
		sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
		for _, f := range fs {
			line := fmt.Sprintf("  %s %s  %s:%d", callMark[f.Status], f.Name, f.Path, f.Line)
			if f.Status == FuncRenamed {
				line += "  (was " + shortName(f.From) + ")"
			}
			if n := callers[f.ID] + f.MoreCallers; n > 0 && f.Status != FuncRemoved {
				line += fmt.Sprintf("  · %d caller%s", n, plural(n))
			}
			fmt.Fprintln(w, line)
			for _, c := range out[f.ID] {
				kind := ""
				switch c.Kind {
				case CallRef:
					kind = " (as a value)"
				case CallDynamic:
					kind = " (through an interface)"
				case CallApprox:
					kind = " (inferred)"
				}
				if c.Label != "" {
					kind += " for " + c.Label
				}
				fmt.Fprintf(w, "      %s calls %s%s\n", c.Op, name(c.To), kind)
			}
		}
	}
	if len(cg.Findings) > 0 {
		fmt.Fprintln(w)
	}
	for _, x := range cg.Findings {
		var what string
		switch x.Kind {
		case FindingRemovedCalled:
			what = "removed but still called"
		case FindingSignatureCallers:
			what = "signature changed; callers not updated"
		case FindingUntested:
			what = "no test reaches it within 2 calls"
		}
		fmt.Fprintf(w, "%s: %s", name(x.Func), what)
		var sites []string
		for _, s := range x.Sites {
			sites = append(sites, s.Path+":"+strconv.Itoa(s.Line))
		}
		if len(sites) > 0 {
			fmt.Fprintf(w, " (%s)", strings.Join(sites, ", "))
		}
		fmt.Fprintln(w)
	}
	var notes []string
	for _, l := range cg.Languages {
		if !l.Exact && l.Units > 0 {
			notes = append(notes, l.Name)
		}
	}
	if len(notes) > 0 {
		fmt.Fprintf(w, "\nCalls in %s are inferred from names, so they're approximate.\n", strings.Join(notes, ", "))
	}
	if cg.Truncated {
		fmt.Fprintln(w, "This change is large: only part of it, or of its callers, is shown.")
	}
	for _, p := range cg.Unparsed {
		fmt.Fprintf(w, "Not read (doesn't parse right now): %s\n", p)
	}
	for _, e := range cg.Errors {
		fmt.Fprintf(w, "Error: %s\n", e)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// WriteCallsDOT writes the graph in Graphviz's dot language, one cluster
// per unit, colored like the web view: new calls green, removed dashed
// red, approximate dotted, references dashed.
func WriteCallsDOT(w io.Writer, cg *CallGraph) {
	fmt.Fprintln(w, "digraph calls {")
	fmt.Fprintln(w, `  rankdir=LR; node [shape=box, fontname="monospace", fontsize=10]; edge [color="#999999"];`)
	byUnit := map[string][]Func{}
	for _, f := range cg.Funcs {
		byUnit[f.Unit] = append(byUnit[f.Unit], f)
	}
	for i, u := range sortedKeys(byUnit) {
		fmt.Fprintf(w, "  subgraph cluster_%d {\n    label=%s; color=\"#cccccc\";\n", i, strconv.Quote(u))
		for _, f := range byUnit[u] {
			attrs := []string{"label=" + strconv.Quote(strings.TrimSpace(callMark[f.Status]+" "+f.Name))}
			switch f.Status {
			case FuncAdded:
				attrs = append(attrs, `color="#00b140"`, "penwidth=2")
			case FuncRemoved:
				attrs = append(attrs, `color="#e4002b"`, "style=dashed")
			case FuncChanged, FuncRenamed:
				attrs = append(attrs, "penwidth=1.5")
			case FuncSignature:
				attrs = append(attrs, "penwidth=2.5")
			case "":
				attrs = append(attrs, `color="#bbbbbb"`, `fontcolor="#777777"`)
			}
			fmt.Fprintf(w, "    %s [%s];\n", strconv.Quote(f.ID), strings.Join(attrs, ", "))
		}
		fmt.Fprintln(w, "  }")
	}
	for _, c := range cg.Calls {
		var attrs []string
		switch c.Op {
		case OpAdded:
			attrs = append(attrs, `color="#00b140"`, "penwidth=2")
		case OpRemoved:
			attrs = append(attrs, `color="#e4002b"`, "style=dashed")
		}
		switch c.Kind {
		case CallRef:
			attrs = append(attrs, "style=dashed")
		case CallApprox:
			attrs = append(attrs, "style=dotted")
		case CallDynamic:
			attrs = append(attrs, "arrowhead=open")
		case CallImpl:
			attrs = append(attrs, "style=dotted", "arrowhead=empty")
		}
		fmt.Fprintf(w, "  %s -> %s [%s];\n", strconv.Quote(c.From), strconv.Quote(c.To), strings.Join(attrs, ", "))
	}
	fmt.Fprintln(w, "}")
}
