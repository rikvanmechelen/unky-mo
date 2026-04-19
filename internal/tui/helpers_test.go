package tui

import (
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
)

func TestYesNoBindings(t *testing.T) {
	got := yesNoBindings("kill + continue")
	if len(got) != 2 {
		t.Fatalf("want 2 bindings, got %d", len(got))
	}
	if got[0].key != "y" || got[0].desc != "kill + continue" {
		t.Errorf("first binding = %+v, want {y, kill + continue}", got[0])
	}
	if got[1].key != "n" || got[1].desc != "cancel" {
		t.Errorf("second binding = %+v, want {n, cancel}", got[1])
	}
}

func TestWithCancelAppends(t *testing.T) {
	in := []footerBinding{{"w", "worktree only"}, {"b", "worktree + branch"}}
	got := withCancel(in)
	if len(got) != 3 {
		t.Fatalf("want 3 bindings, got %d", len(got))
	}
	if got[2].key != "n" || got[2].desc != "cancel" {
		t.Errorf("last binding = %+v, want {n, cancel}", got[2])
	}
}

func TestWithCancelIdempotent(t *testing.T) {
	in := []footerBinding{{"s", "switch"}, {"n", "cancel"}}
	got := withCancel(in)
	if len(got) != 2 {
		t.Errorf("want 2 bindings (unchanged), got %d: %+v", len(got), got)
	}
}

func TestRankOrderingPriority(t *testing.T) {
	// Permission > Idle > Active > External > None.
	want := []SessionStatus{StatusPermission, StatusIdle, StatusActive, StatusExternal, StatusNone}
	for i := 0; i+1 < len(want); i++ {
		if rank(want[i]) <= rank(want[i+1]) {
			t.Errorf("rank(%v)=%d must be > rank(%v)=%d", want[i], rank(want[i]), want[i+1], rank(want[i+1]))
		}
	}
}

func TestStatusToString(t *testing.T) {
	cases := map[SessionStatus]string{
		StatusActive:     "active",
		StatusIdle:       "idle",
		StatusPermission: "permission",
		StatusExternal:   "external",
		StatusNone:       "none",
	}
	for in, want := range cases {
		if got := statusToString(in); got != want {
			t.Errorf("statusToString(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestParseWindowIndex(t *testing.T) {
	cases := []struct {
		name string
		want int
	}{
		{"proj", 0},               // bare primary
		{"proj [2]", 2},           // ordinal sibling
		{"proj [3]", 3},           // ordinal
		{"proj [debug]", -1},      // custom-title suffix
		{"proj@feat", 0},          // bare worktree
		{"proj@feat [2]", 2},      // worktree sibling
		{"proj@feat [foo]", -1},   // worktree custom title
		{"", 0},                   // unparseable → treated as bare
	}
	for _, c := range cases {
		got := parseWindowIndex(c.name)
		if got != c.want {
			t.Errorf("parseWindowIndex(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestComposeFallbackWindow(t *testing.T) {
	cases := []struct {
		name string
		v    sessionView
		want string
	}{
		{
			"bare project",
			sessionView{ProjectName: "alpha"},
			"alpha",
		},
		{
			"worktree — strips leading @ on ProjectName",
			sessionView{ProjectName: "@feat", Parent: "alpha", IsWorktree: true},
			"alpha@feat",
		},
		{
			"stray — returns ProjectName verbatim",
			sessionView{ProjectName: "stray-dir", IsStray: true},
			"stray-dir",
		},
	}
	for _, c := range cases {
		if got := composeFallbackWindow(c.v); got != c.want {
			t.Errorf("%s: composeFallbackWindow = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSortViewsForDisplay(t *testing.T) {
	// Input: custom-title, ordinal-3, ordinal-2, bare (scrambled).
	views := []sessionView{
		{Index: -1, WindowName: "alpha [debug]"},
		{Index: 3, WindowName: "alpha [3]"},
		{Index: 2, WindowName: "alpha [2]"},
		{Index: 0, WindowName: "alpha"},
	}
	sortViewsForDisplay(views)
	want := []string{"alpha", "alpha [2]", "alpha [3]", "alpha [debug]"}
	for i, v := range views {
		if v.WindowName != want[i] {
			t.Errorf("position %d: got %q, want %q", i, v.WindowName, want[i])
		}
	}
}

func TestSortViewsForDisplayStableByWindowNameOnTies(t *testing.T) {
	// Two bare windows (Index=0) — tie-break alphabetical by WindowName.
	views := []sessionView{
		{Index: 0, WindowName: "beta"},
		{Index: 0, WindowName: "alpha"},
	}
	sortViewsForDisplay(views)
	if views[0].WindowName != "alpha" {
		t.Errorf("want alpha first, got %q", views[0].WindowName)
	}
}

func TestKnownProjectPath(t *testing.T) {
	m := map[string]string{
		"/ws/alpha": "alpha",
		"/ws/beta":  "beta",
	}
	if knownProjectPath(m, "/ws/alpha") != "alpha" {
		t.Error("alpha lookup failed")
	}
	if knownProjectPath(m, "/nowhere") != "" {
		t.Error("unknown path should return empty")
	}
}

func TestWorktreeParent(t *testing.T) {
	names := map[string]string{"/ws/alpha": "alpha"}

	cases := []struct {
		name                       string
		cwd                        string
		wantPath, wantName, wantBr string
	}{
		{
			name:     "known parent",
			cwd:      "/ws/alpha.worktrees/feat",
			wantPath: "/ws/alpha",
			wantName: "alpha",
			wantBr:   "feat",
		},
		{
			name:     "unknown parent → name empty",
			cwd:      "/ws/unknown.worktrees/bugfix",
			wantPath: "/ws/unknown",
			wantName: "",
			wantBr:   "bugfix",
		},
		{
			name:     "not a worktree path at all",
			cwd:      "/ws/alpha",
			wantPath: "",
			wantName: "",
			wantBr:   "",
		},
	}
	for _, c := range cases {
		path, n, br := worktreeParent(c.cwd, names)
		if path != c.wantPath || n != c.wantName || br != c.wantBr {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)",
				c.name, path, n, br, c.wantPath, c.wantName, c.wantBr)
		}
	}
}

func TestViewToDashItemStrayAttachesGitStatus(t *testing.T) {
	v := sessionView{
		WindowName:  "stray",
		Status:      StatusActive,
		CWD:         "/tmp/some-repo",
		Section:     "projects",
		IsStray:     true,
		Branch:      "main",
		Dirty:       3,
	}
	item := viewToDashItem(v)
	if item.Git == nil {
		t.Fatal("stray with branch should get Git populated")
	}
	if item.Git.Branch != "main" || item.Git.Dirty != 3 {
		t.Errorf("git fields wrong: %+v", item.Git)
	}
}

func TestViewToDashItemNonStrayHasNoGit(t *testing.T) {
	v := sessionView{
		WindowName: "alpha",
		Status:     StatusIdle,
		CWD:        "/ws/alpha",
		Section:    "projects",
	}
	item := viewToDashItem(v)
	if item.Git != nil {
		t.Errorf("non-stray should not have Git, got %+v", item.Git)
	}
	if item.Status != StatusIdle || item.WindowName != "alpha" {
		t.Errorf("fields not copied: %+v", item)
	}
}

func TestViewToProjectStateFoldsSuffixIntoName(t *testing.T) {
	v := sessionView{
		SessionID:  "s1",
		CWD:        "/ws/alpha",
		WindowName: "alpha [2]",
		Status:     StatusActive,
		Section:    "projects",
		Index:      2,
	}
	ps := viewToProjectState(v, "", "alpha")
	if ps.Name != "alpha [2]" {
		t.Errorf("suffix should be folded into Name, got %q", ps.Name)
	}
	if ps.Index != 2 || ps.Status != "active" || ps.SessionID != "s1" {
		t.Errorf("unexpected ps: %+v", ps)
	}
}

func TestViewToProjectStatePrimaryKeepsBase(t *testing.T) {
	v := sessionView{
		WindowName: "alpha",
		Status:     StatusIdle,
		Index:      0,
	}
	ps := viewToProjectState(v, "", "alpha")
	if ps.Name != "alpha" {
		t.Errorf("bare primary: want Name=alpha, got %q", ps.Name)
	}
}

func TestViewToProjectStateStrayPopulatesGitFields(t *testing.T) {
	v := sessionView{
		WindowName: "stray",
		IsStray:    true,
		Branch:     "main",
		Dirty:      5,
	}
	ps := viewToProjectState(v, "", "stray")
	if ps.Branch != "main" || ps.Dirty != 5 {
		t.Errorf("stray fields missing from ProjectState: %+v", ps)
	}
}

// Cross-check: the state file's ProjectState is the exact shape we expect.
func TestViewToProjectStateMatchesStateStructShape(t *testing.T) {
	v := sessionView{WindowName: "x", Status: StatusActive}
	ps := viewToProjectState(v, "parent", "x")
	var _ state.ProjectState = ps
	if ps.Parent != "parent" {
		t.Errorf("Parent not threaded through")
	}
}

// TestHelpViewContainsAllSectionHeaders — helpView is essentially static
// (doesn't read Model fields beyond the receiver). Lock in the section
// headers so a future refactor doesn't accidentally drop a category.
func TestHelpViewContainsAllSectionHeaders(t *testing.T) {
	var m Model
	out := m.helpView()
	wantHeaders := []string{"Help", "Navigation", "Sessions", "Branches (project detail)", "Other"}
	for _, h := range wantHeaders {
		if !contains(out, h) {
			t.Errorf("helpView missing section %q", h)
		}
	}
	// Spot-check a few binding descriptions so a removal surfaces.
	wantBindings := []string{"Move up", "New session", "Toggle this help", "Quit"}
	for _, b := range wantBindings {
		if !contains(out, b) {
			t.Errorf("helpView missing binding %q", b)
		}
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
