package review

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fakeCalls is a callLang for delta tests; funcs isn't used.
type fakeCalls struct{ isExact bool }

func (f fakeCalls) name() string                                   { return "fake" }
func (f fakeCalls) exact() bool                                    { return f.isExact }
func (f fakeCalls) funcs(*index, []string, bool) (*callSet, error) { return nil, nil }

// tf builds a test function: id in path at line, with a body and signature
// and the calls given as "to" or "to@kind".
func tf(id, path string, line int, body, sig string, calls ...string) *fn {
	f := &fn{Func: Func{ID: id, Name: shortName(id), Path: path, Line: line, End: line + 2, Unit: pathDir(path), Lang: "fake",
		Test: strings.HasSuffix(path, "_test.go")}, body: body, sig: sig}
	for i, c := range calls {
		to, kind, ok := strings.Cut(c, "@")
		if !ok {
			kind = CallStatic
		}
		f.calls = append(f.calls, callSite{to: to, kind: kind, line: line + 1 + i})
	}
	return f
}

func pathDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return "."
}

func set(fns ...*fn) *callSet {
	s := newCallSet()
	for _, f := range fns {
		s.add(f)
	}
	return s
}

func delta(before, after *callSet, oldPaths, newPaths []string, renames map[string]string) *CallGraph {
	cg := &CallGraph{}
	if renames == nil {
		renames = map[string]string{}
		for _, p := range oldPaths {
			renames[p] = p
		}
	}
	callDelta(cg, fakeCalls{isExact: true}, before, after, oldPaths, newPaths, renames)
	capCallGraph(cg)
	return cg
}

func statuses(cg *CallGraph) map[string]string {
	out := map[string]string{}
	for _, f := range cg.Funcs {
		if f.Status != "" {
			out[f.ID] = f.Status
		}
	}
	return out
}

func callKeys(cg *CallGraph) []string {
	var out []string
	for _, c := range cg.Calls {
		k := c.Op + c.From + ">" + c.To
		if c.Kind != CallStatic {
			k += "@" + c.Kind
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func findingKeys(cg *CallGraph) []string {
	var out []string
	for _, f := range cg.Findings {
		k := f.Kind + ":" + f.Func
		for _, s := range f.Sites {
			k += " " + s.Path
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCallDeltaStatuses(t *testing.T) {
	before := set(
		tf("a.Same", "a/a.go", 1, "b1", "s"),
		tf("a.Moved", "a/a.go", 5, "b2", "s"),
		tf("a.Body", "a/a.go", 9, "b3", "s"),
		tf("a.Sig", "a/a.go", 13, "b4", "s"),
		tf("a.Gone", "a/a.go", 17, "b5", "s"),
		tf("a.OldName", "a/a.go", 21, "b6", "s"),
	)
	after := set(
		tf("a.Same", "a/a.go", 3, "b1", "s"),  // only moved down: unchanged
		tf("a.Moved", "a/b.go", 1, "b2", "s"), // moved to another changed file
		tf("a.Body", "a/a.go", 9, "b3x", "s"),
		tf("a.Sig", "a/a.go", 13, "b4x", "s2"),
		tf("a.NewName", "a/a.go", 21, "b6", "s"), // same body, same unit: a rename
		tf("a.Fresh", "a/b.go", 5, "b7", "s"),
	)
	cg := delta(before, after, []string{"a/a.go"}, []string{"a/a.go", "a/b.go"}, nil)
	want := map[string]string{"a.Body": FuncChanged, "a.Sig": FuncSignature, "a.Gone": FuncRemoved,
		"a.NewName": FuncRenamed, "a.Fresh": FuncAdded}
	if got := statuses(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}
	for _, f := range cg.Funcs {
		switch f.ID {
		case "a.NewName":
			if f.From != "a.OldName" {
				t.Errorf("rename from = %q", f.From)
			}
		case "a.Gone":
			if !f.Before || f.Line != 17 {
				t.Errorf("removed function = %+v, want its base position", f)
			}
		}
	}
}

func TestCallDeltaEdges(t *testing.T) {
	before := set(
		tf("a.F", "a/a.go", 1, "b1", "s", "b.X", "b.Y"),
		tf("a.Old", "a/a.go", 5, "b2", "s", "b.X"),
		tf("a.G", "a/a.go", 9, "b3", "s"),
		tf("b.X", "b/b.go", 1, "x", "s"),
		tf("b.Y", "b/b.go", 5, "y", "s"),
	)
	after := set(
		tf("a.F", "a/a.go", 1, "b1x", "s", "b.X", "b.Z"), // drops b.Y, adds b.Z
		tf("a.New", "a/a.go", 5, "b2", "s", "b.X"),       // renamed from Old: no edge change
		tf("a.G", "a/a.go", 9, "b3x", "s", "a.H@ref"),    // gains a reference
		tf("a.H", "a/a.go", 13, "h", "s"),
		tf("b.X", "b/b.go", 1, "x", "s"),
		tf("b.Y", "b/b.go", 5, "y", "s"),
		tf("b.Z", "b/b.go", 9, "z", "s"),
	)
	cg := delta(before, after, []string{"a/a.go"}, []string{"a/a.go"}, nil)
	want := []string{"+a.F>b.Z", "+a.G>a.H@ref", "-a.F>b.Y", "a.F>b.X", "a.New>b.X"}
	if got := callKeys(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

// A call moved from one changed function to another is one removed and one
// added edge.
func TestCallDeltaMovedCall(t *testing.T) {
	before := set(tf("a.A", "a/a.go", 1, "1", "s", "b.X"), tf("a.B", "a/a.go", 5, "2", "s"), tf("b.X", "b/b.go", 1, "x", "s"))
	after := set(tf("a.A", "a/a.go", 1, "1x", "s"), tf("a.B", "a/a.go", 5, "2x", "s", "b.X"), tf("b.X", "b/b.go", 1, "x", "s"))
	cg := delta(before, after, []string{"a/a.go"}, []string{"a/a.go"}, nil)
	if got, want := callKeys(cg), []string{"+a.B>b.X", "-a.A>b.X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestCallDeltaContextCallers(t *testing.T) {
	before := set(tf("a.F", "a/a.go", 1, "b", "s"))
	fns := []*fn{tf("a.F", "a/a.go", 1, "bx", "s")}
	for i := 0; i < maxCallers+3; i++ {
		fns = append(fns, tf("c.C"+strings.Repeat("x", i+1), "c/c.go", 10*i+1, "c", "s", "a.F"))
	}
	cg := delta(before, set(fns...), []string{"a/a.go"}, []string{"a/a.go"}, nil)
	callers := 0
	for _, c := range cg.Calls {
		if c.To == "a.F" && c.Op == "" {
			callers++
		}
	}
	if callers != maxCallers {
		t.Errorf("%d context callers, want %d", callers, maxCallers)
	}
	for _, f := range cg.Funcs {
		if f.ID == "a.F" && f.MoreCallers != 3 {
			t.Errorf("moreCallers = %d, want 3", f.MoreCallers)
		}
	}
}

func TestCallDeltaFindings(t *testing.T) {
	before := set(
		tf("a.Gone", "a/a.go", 1, "g", "s"),
		tf("a.Fixed", "a/a.go", 5, "f", "s"),
		tf("a.Sig", "a/a.go", 9, "s", "s1"),
	)
	gone := tf("c.Caller", "c/c.go", 1, "c", "s", "a.Sig")
	gone.unresolved = []unresolvedSite{{line: 3, want: []string{"a.Gone"}}}
	after := set(
		tf("a.Sig", "a/a.go", 9, "s", "s2"),
		gone,
		tf("a.Tested", "a/a.go", 20, "t", "s"),
		tf("a.Deep", "a/a.go", 30, "d", "s"),
		tf("a.Mid", "a/m.go", 1, "m", "s", "a.Tested", "a.Mid2"),
		tf("a.Mid2", "a/m.go", 5, "m", "s", "a.Deep"),
		tf("a.TestIt", "a/a_test.go", 1, "t", "s", "a.Mid"),
		tf("a.Self", "a/a.go", 40, "u", "s", "a.Sig"), // a caller in a changed file: not a signature finding
	)
	cg := delta(before, after, []string{"a/a.go"}, []string{"a/a.go"}, nil)
	want := []string{
		"removed-called:a.Gone c/c.go",   // still called from an unchanged file
		"signature-callers:a.Sig c/c.go", // its caller in c isn't part of the change
		"untested:a.Deep",                // a test reaches it only in 3 hops
		"untested:a.Self",                // nothing calls it
		"untested:a.Sig",                 // only untested callers
	}
	if got := findingKeys(cg); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v\nwant %v", got, want)
	}
	// a.Fixed is removed and nothing calls it any more: no finding.
	for _, f := range cg.Findings {
		if f.Func == "a.Fixed" {
			t.Errorf("unexpected finding %+v", f)
		}
	}
	// Removed-called comes first.
	if cg.Findings[0].Kind != FindingRemovedCalled {
		t.Errorf("first finding = %s", cg.Findings[0].Kind)
	}
}

// Without any test functions loaded, nothing is flagged untested.
func TestCallDeltaNoTestsNoUntested(t *testing.T) {
	cg := delta(set(), set(tf("a.F", "a/a.go", 1, "f", "s")), nil, []string{"a/a.go"}, nil)
	if len(cg.Findings) != 0 {
		t.Errorf("findings = %v", findingKeys(cg))
	}
}

// A file that doesn't parse on one side hides its functions on both.
func TestCallDeltaUnparsed(t *testing.T) {
	before := set(tf("a.F", "a/a.go", 1, "f", "s"), tf("b.G", "b/b.go", 1, "g", "s"))
	after := set(tf("b.G", "b/b.go", 1, "gx", "s"))
	after.unparsed["a/a.go"] = true
	cg := delta(before, after, []string{"a/a.go", "b/b.go"}, []string{"a/a.go", "b/b.go"}, nil)
	if got := statuses(cg); !reflect.DeepEqual(got, map[string]string{"b.G": FuncChanged}) {
		t.Errorf("statuses = %v", got)
	}
	if !reflect.DeepEqual(cg.Unparsed, []string{"a/a.go"}) {
		t.Errorf("unparsed = %v", cg.Unparsed)
	}
}

// A heuristic language's resolved calls are approximate.
func TestCallDeltaApprox(t *testing.T) {
	cg := &CallGraph{}
	before := set(tf("a.F", "a/a.go", 1, "f", "s"))
	after := set(tf("a.F", "a/a.go", 1, "fx", "s", "a.G", "a.H@ref"), tf("a.G", "a/a.go", 9, "g", "s"), tf("a.H", "a/a.go", 19, "h", "s"))
	callDelta(cg, fakeCalls{}, before, after, []string{"a/a.go"}, []string{"a/a.go"}, map[string]string{"a/a.go": "a/a.go"})
	if got, want := callKeys(cg), []string{"+a.F>a.G@approx", "+a.F>a.H@ref"}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestCapCallGraph(t *testing.T) {
	cg := &CallGraph{}
	for i := 0; i < maxCallFuncs+10; i++ {
		f := Func{ID: "ctx" + strings.Repeat("x", i)}
		if i%2 == 0 {
			f.Status = FuncChanged
		}
		cg.Funcs = append(cg.Funcs, f)
	}
	// A context function at the end of an added call outranks other
	// context; one only in an unchanged call may go.
	last, other := cg.Funcs[len(cg.Funcs)-1].ID, cg.Funcs[len(cg.Funcs)-3].ID
	cg.Calls = []Call{{From: cg.Funcs[0].ID, To: last, Op: OpAdded}, {From: cg.Funcs[0].ID, To: other}}
	capCallGraph(cg)
	if !cg.Truncated || len(cg.Funcs) != maxCallFuncs || len(cg.Calls) != 1 || cg.Calls[0].To != last {
		t.Fatalf("truncated=%v funcs=%d calls=%v", cg.Truncated, len(cg.Funcs), cg.Calls)
	}
	changed := 0
	for _, f := range cg.Funcs {
		if f.Status != "" {
			changed++
		}
	}
	if changed != (maxCallFuncs+10+1)/2 {
		t.Errorf("kept %d changed functions", changed)
	}
}

// A test reaching an interface method reaches its implementations at no
// extra hop; generated functions are never flagged.
func TestCallDeltaUntestedThroughInterface(t *testing.T) {
	impl := tf("a.Impl", "a/a.go", 1, "i", "s")
	gen := tf("a.Gen", "a/gen.go", 1, "g", "s")
	gen.generated = true
	after := set(
		impl, gen,
		&fn{Func: Func{ID: "(a.I).M", Path: "a/i.go"}, synthetic: true, calls: []callSite{{to: "a.Impl", kind: CallImpl}}},
		tf("a.Mid", "a/m.go", 1, "m", "s", "(a.I).M@dynamic"),
		tf("a.TestX", "a/a_test.go", 1, "t", "s", "a.Mid"),
	)
	cg := delta(set(), after, nil, []string{"a/a.go", "a/gen.go"}, nil)
	if got := findingKeys(cg); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
}
