package review

import (
	"bytes"
	"strings"
	"testing"
)

func fmtGraph() *CallGraph {
	return &CallGraph{
		Funcs: []Func{
			{ID: "a.F", Name: "F", Unit: "a", Path: "a/a.go", Line: 3, Status: FuncSignature},
			{ID: "a.Gone", Name: "Gone", Unit: "a", Path: "a/a.go", Line: 9, Status: FuncRemoved, Before: true},
			{ID: "a.New", Name: "New", Unit: "a", Path: "a/a.go", Line: 12, Status: FuncRenamed, From: "a.Old"},
			{ID: "b.G", Name: "G", Unit: "b", Path: "b/b.go", Line: 1},
			{ID: "c.H", Name: "H", Unit: "c", Path: "c/c.go", Line: 7},
		},
		Calls: []Call{
			{From: "a.F", To: "b.G", Kind: CallStatic, Op: OpAdded},
			{From: "a.F", To: "a.New", Kind: CallRef, Op: OpRemoved},
			{From: "c.H", To: "a.F", Kind: CallApprox},
		},
		Findings: []CallFinding{
			{Kind: FindingRemovedCalled, Func: "a.Gone", Sites: []EdgeFile{{Path: "c/c.go", Line: 8}}},
			{Kind: FindingSignatureCallers, Func: "a.F", Sites: []EdgeFile{{Path: "c/c.go", Line: 8}}},
		},
		Languages: []LangInfo{{Name: "python", Units: 2}},
	}
}

func TestWriteCallsText(t *testing.T) {
	var b bytes.Buffer
	WriteCallsText(&b, fmtGraph())
	out := b.String()
	for _, want := range []string{
		"a\n",
		"  ! F  a/a.go:3  · 1 caller\n",
		"      + calls G\n",
		"      - calls New (as a value)\n",
		"  - Gone  a/a.go:9\n",
		"  > New  a/a.go:12  (was a.Old)\n", // its only call was removed: no callers
		"Gone: removed but still called (c/c.go:8)\n",
		"F: signature changed; callers not updated (c/c.go:8)\n",
		"Calls in python are inferred from names",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text lacks %q:\n%s", want, out)
		}
	}
	// Context functions aren't listed as changes.
	if strings.Contains(out, "  H  ") {
		t.Errorf("context listed:\n%s", out)
	}
}

func TestWriteCallsDOT(t *testing.T) {
	var b bytes.Buffer
	WriteCallsDOT(&b, fmtGraph())
	out := b.String()
	for _, want := range []string{
		"digraph calls {",
		`label="a"`,
		`"a.Gone" [label="- Gone", color="#e4002b", style=dashed];`,
		`"a.F" -> "b.G" [color="#00b140", penwidth=2];`,
		`"c.H" -> "a.F" [style=dotted];`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dot lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Error("dot isn't closed")
	}
}
