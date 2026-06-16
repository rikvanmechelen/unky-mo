package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/rvanmech/unky-mo/internal/config"
	"github.com/rvanmech/unky-mo/internal/ops"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

func TestOrphanedTermSessions(t *testing.T) {
	windows := []ttmux.Window{
		{ID: "@5", Name: "moma-apps-rails", InstanceID: "a1b2c3d4e5f6"},
		{ID: "@17", Name: "unky-mo [extra-terminal]", InstanceID: "deadbeef0123"},
	}
	sessions := []string{
		"mo",                       // not a mo-terms session, ignored
		"mo-terms-5",               // paired with @5 → keep
		"mo-terms-17",              // paired with @17 → keep
		"mo-terms-14",              // no paired window → orphan
		"mo-terms-1",               // no paired window → orphan
		"mo-terms-a1b2c3d4e5f6",   // paired with window's instanceID → keep
		"mo-terms-deadbeef0123",    // paired with window's instanceID → keep
		"mo-terms-aabbccddeeff",    // no paired instance ID → orphan
		"mo-terms-alpha",           // non-hex non-digit suffix, left alone
		"mo-terms",                 // empty suffix, ignored
		"some-other-server",        // unrelated, ignored
	}
	got := orphanedTermSessions(sessions, windows)
	want := []string{"mo-terms-14", "mo-terms-1", "mo-terms-aabbccddeeff"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, s := range got {
		if s != want[i] {
			t.Errorf("orphan[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestOrphanedTermSessionsEmptyInputs(t *testing.T) {
	if got := orphanedTermSessions(nil, nil); len(got) != 0 {
		t.Errorf("nil inputs should produce no orphans, got %v", got)
	}
	// No windows alive — every mo-terms-N becomes an orphan.
	got := orphanedTermSessions([]string{"mo-terms-1", "mo-terms-2"}, nil)
	if len(got) != 2 {
		t.Errorf("expected 2 orphans with no live windows, got %v", got)
	}
}

// Phase A7: Additional orphan pruning characterization tests.

func TestOrphanedTermSessionsNonHexNonDigitLeftAlone(t *testing.T) {
	// Suffixes that are neither all-digits nor 12-char hex are left alone
	// (e.g. windowName-based fallback from unit tests).
	sessions := []string{
		"mo-terms-alpha",         // not digits, not hex12 → left alone
		"mo-terms-a1b2c3d4e5f6", // hex12 — matched against live instance IDs
		"mo-terms-5",             // digit — window @5 is alive
	}
	windows := []ttmux.Window{{ID: "@5", InstanceID: "a1b2c3d4e5f6"}}
	got := orphanedTermSessions(sessions, windows)
	if len(got) != 0 {
		t.Errorf("alive digit + alive hex12 + non-matching suffix should produce no orphans, got %v", got)
	}
}

func TestOrphanedTermSessionsAllWindowsAlive(t *testing.T) {
	sessions := []string{"mo-terms-1", "mo-terms-2", "mo-terms-3"}
	windows := []ttmux.Window{
		{ID: "@1"}, {ID: "@2"}, {ID: "@3"},
	}
	got := orphanedTermSessions(sessions, windows)
	if len(got) != 0 {
		t.Errorf("all windows alive should produce no orphans, got %v", got)
	}
}

func TestOrphanedTermSessionsMultiDigitID(t *testing.T) {
	// Window @17 has a multi-digit ID — ensure the suffix matching works.
	sessions := []string{"mo-terms-17", "mo-terms-170"}
	windows := []ttmux.Window{{ID: "@17"}}
	got := orphanedTermSessions(sessions, windows)
	if len(got) != 1 || got[0] != "mo-terms-170" {
		t.Errorf("want [mo-terms-170], got %v", got)
	}
}

func TestOrphanedTermSessionsWindowWithEmptyID(t *testing.T) {
	// Windows without IDs (shouldn't happen in practice) are skipped
	// gracefully — they don't match any suffix.
	sessions := []string{"mo-terms-1"}
	windows := []ttmux.Window{{ID: "", Name: "alpha"}}
	got := orphanedTermSessions(sessions, windows)
	if len(got) != 1 || got[0] != "mo-terms-1" {
		t.Errorf("want [mo-terms-1], got %v", got)
	}
}

func TestRestartSidebarsNewPathKillsAndRespawns(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)

	tmux.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@0", Index: "0", Name: "mo", CWD: "/home"},                                     // TUI — skipped
		{ID: "@5", Index: "1", Name: "alpha", InstanceID: "a1b2c3d4e5f6", CWD: "/ws/alpha"},  // has instance ID
	}, nil)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().KillPane("mo:1.1").Return(nil)
	tmux.EXPECT().SplitWindow("mo:1", 42, "/ws/alpha", gomock.Any()).
		Do(func(target string, cols int, cwd, cmd string) {
			if !strings.Contains(cmd, "--instance-id=a1b2c3d4e5f6") {
				t.Errorf("sidebar command should contain instance ID, got %q", cmd)
			}
		}).Return("%2", nil)
	tmux.EXPECT().SelectPane("mo:1.0").Return(nil)

	m := Model{
		tmux: tmux,
		ops:  &ops.Context{MoBinaryPath: "/usr/local/bin/mo", SidebarWidth: 42},
	}
	m.restartSidebars()
}

func TestRestartSidebarsLegacyFallbackSendsRawKeys(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)

	tmux.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@0", Index: "0", Name: "mo", CWD: "/home"},                // TUI — skipped
		{ID: "@5", Index: "1", Name: "alpha", CWD: "/ws/alpha"},         // no instance ID — legacy
	}, nil)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().SendRawKeys("mo:1.1", "M-C-r")

	m := Model{
		tmux: tmux,
		ops:  &ops.Context{MoBinaryPath: "/usr/local/bin/mo", SidebarWidth: 42},
	}
	m.restartSidebars()
}

func TestRestartSidebarsMixedWindows(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)

	tmux.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@0", Index: "0", Name: "mo", CWD: "/home"},                                     // skipped
		{ID: "@5", Index: "1", Name: "alpha", InstanceID: "a1b2c3d4e5f6", CWD: "/ws/alpha"},  // new path
		{ID: "@6", Index: "2", Name: "beta", CWD: "/ws/beta"},                                 // legacy
	}, nil)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	// Window 1 (alpha): kill + respawn
	tmux.EXPECT().KillPane("mo:1.1").Return(nil)
	tmux.EXPECT().SplitWindow("mo:1", 42, "/ws/alpha", gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane("mo:1.0").Return(nil)

	// Window 2 (beta): legacy raw keys
	tmux.EXPECT().SendRawKeys("mo:2.1", "M-C-r")

	m := Model{
		tmux: tmux,
		ops:  &ops.Context{MoBinaryPath: "/usr/local/bin/mo", SidebarWidth: 42},
	}
	m.restartSidebars()
}

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

func TestViewToProjectStateThreadsInstanceID(t *testing.T) {
	v := sessionView{
		SessionID:  "s1",
		InstanceID: "a1b2c3d4e5f6",
		WindowName: "alpha",
		WindowID:   "@5",
		Status:     StatusActive,
		CWD:        "/ws/alpha",
	}
	ps := viewToProjectState(v, "", "alpha")
	if ps.InstanceID != "a1b2c3d4e5f6" {
		t.Errorf("InstanceID not threaded through: got %q", ps.InstanceID)
	}
	if ps.SessionID != "s1" || ps.WindowID != "@5" {
		t.Errorf("other fields dropped: %+v", ps)
	}
}

func TestStateFileInstanceIDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	sf := &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{
				Name:       "alpha",
				WindowID:   "@5",
				InstanceID: "a1b2c3d4e5f6",
				SessionID:  "sess-1",
				Status:     "active",
			},
			{
				Name:     "beta",
				WindowID: "@6",
				// No InstanceID — pre-refactor window
				Status: "idle",
			},
		},
	}
	if err := state.Write(path, sf); err != nil {
		t.Fatal(err)
	}
	got, err := state.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Projects[0].InstanceID != "a1b2c3d4e5f6" {
		t.Errorf("InstanceID lost on round-trip: got %q", got.Projects[0].InstanceID)
	}
	if got.Projects[1].InstanceID != "" {
		t.Errorf("empty InstanceID should stay empty: got %q", got.Projects[1].InstanceID)
	}
}

func TestStateFileAgentKeyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.json"

	sf := &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{
				Name:     "alpha",
				WindowID: "@5",
				AgentKey: "g",
				Status:   "active",
			},
			{
				Name:     "beta",
				WindowID: "@6",
				// No AgentKey — default agent
				Status: "idle",
			},
		},
	}
	if err := state.Write(path, sf); err != nil {
		t.Fatal(err)
	}
	got, err := state.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Projects[0].AgentKey != "g" {
		t.Errorf("AgentKey lost on round-trip: got %q", got.Projects[0].AgentKey)
	}
	if got.Projects[1].AgentKey != "" {
		t.Errorf("empty AgentKey should stay empty: got %q", got.Projects[1].AgentKey)
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
	wantBindings := []string{"Move up", "New session", "Toggle this help", "Quit", "Pick coding agent"}
	for _, b := range wantBindings {
		if !contains(out, b) {
			t.Errorf("helpView missing binding %q", b)
		}
	}
}

func TestAgentResumeCmd(t *testing.T) {
	claude := config.AgentConfig{Name: "Claude", Cmd: "claude", ResumeCmd: "claude --resume"}
	if got := agentResumeCmd(claude, "abc-123"); got != "claude --resume abc-123" {
		t.Errorf("claude resume: got %q", got)
	}
	gemini := config.AgentConfig{Name: "Gemini", Cmd: "gemini"}
	if got := agentResumeCmd(gemini, "abc-123"); got != "gemini" {
		t.Errorf("gemini (no resume): got %q", got)
	}
}

func TestDefaultAgent(t *testing.T) {
	m := Model{
		agents: []config.AgentConfig{
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
		},
	}
	got := m.defaultAgent()
	if got.Key != "c" {
		t.Errorf("want default Claude, got %q", got.Key)
	}
}

func TestDefaultAgentFallback(t *testing.T) {
	m := Model{
		agents: []config.AgentConfig{
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
		},
	}
	got := m.defaultAgent()
	if got.Key != "g" {
		t.Errorf("want first (Gemini), got %q", got.Key)
	}
}

func TestAgentTagForKey(t *testing.T) {
	m := Model{
		agents: []config.AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
			{Name: "Gemini CLI", Key: "g", Cmd: "gemini"},
		},
	}
	if got := m.agentTagForKey(""); got != "" {
		t.Errorf("empty key: want empty, got %q", got)
	}
	if got := m.agentTagForKey("c"); got != "" {
		t.Errorf("default agent: want empty, got %q", got)
	}
	if got := m.agentTagForKey("g"); got != "(gemini cli)" {
		t.Errorf("gemini: want (gemini cli), got %q", got)
	}
	if got := m.agentTagForKey("z"); got != "(z)" {
		t.Errorf("unknown: want (z), got %q", got)
	}
}

func TestAgentPickerBindings(t *testing.T) {
	m := Model{
		agents: []config.AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
			{Name: "Gemini CLI", Key: "g", Cmd: "gemini"},
			{Name: "Codex CLI", Key: "x", Cmd: "codex"},
		},
	}
	got := m.agentPickerBindings()
	if len(got) != 3 {
		t.Fatalf("want 3 bindings, got %d", len(got))
	}
	if got[0].key != "c" || got[0].desc != "Claude" {
		t.Errorf("binding[0] = %+v, want {c, Claude}", got[0])
	}
	if got[1].key != "g" || got[1].desc != "Gemini CLI" {
		t.Errorf("binding[1] = %+v, want {g, Gemini CLI}", got[1])
	}
	if got[2].key != "x" || got[2].desc != "Codex CLI" {
		t.Errorf("binding[2] = %+v, want {x, Codex CLI}", got[2])
	}
}

func TestAgentPickerBindingsSingle(t *testing.T) {
	m := Model{
		agents: []config.AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	got := m.agentPickerBindings()
	if len(got) != 1 {
		t.Fatalf("want 1 binding, got %d", len(got))
	}
	if got[0].key != "c" || got[0].desc != "Claude" {
		t.Errorf("binding = %+v, want {c, Claude}", got[0])
	}
}

// --- mergeRefreshedProjects tests ---

func TestMergeRefreshedProjectsAddsNewProjects(t *testing.T) {
	existing := []list.Item{
		ProjectItem{project: project.Project{Name: "alpha", Path: "/ws/alpha"}, status: StatusActive},
	}
	refreshed := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "beta", Path: "/ws/beta", Language: "go"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}
	// Alpha should keep its status.
	alpha := got[0].(ProjectItem)
	if alpha.project.Name != "alpha" || alpha.status != StatusActive {
		t.Errorf("alpha: want (alpha, StatusActive), got (%s, %d)", alpha.project.Name, alpha.status)
	}
	// Beta should be new with StatusNone.
	beta := got[1].(ProjectItem)
	if beta.project.Name != "beta" || beta.status != StatusNone {
		t.Errorf("beta: want (beta, StatusNone), got (%s, %d)", beta.project.Name, beta.status)
	}
	if beta.project.Language != "go" {
		t.Errorf("beta language: want 'go', got %q", beta.project.Language)
	}
}

func TestMergeRefreshedProjectsPreservesStatuses(t *testing.T) {
	existing := []list.Item{
		ProjectItem{project: project.Project{Name: "alpha", Path: "/ws/alpha"}, status: StatusIdle},
		ProjectItem{project: project.Project{Name: "beta", Path: "/ws/beta"}, status: StatusPermission},
	}
	refreshed := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "beta", Path: "/ws/beta"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	if len(got) != 2 {
		t.Fatalf("want 2 items, got %d", len(got))
	}
	if got[0].(ProjectItem).status != StatusIdle {
		t.Errorf("alpha status: want Idle, got %d", got[0].(ProjectItem).status)
	}
	if got[1].(ProjectItem).status != StatusPermission {
		t.Errorf("beta status: want Permission, got %d", got[1].(ProjectItem).status)
	}
}

func TestMergeRefreshedProjectsRemovesStaleProjects(t *testing.T) {
	existing := []list.Item{
		ProjectItem{project: project.Project{Name: "alpha", Path: "/ws/alpha"}, status: StatusActive},
		ProjectItem{project: project.Project{Name: "deleted", Path: "/ws/deleted"}, status: StatusNone},
	}
	refreshed := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	if len(got) != 1 {
		t.Fatalf("want 1 item (stale removed), got %d", len(got))
	}
	if got[0].(ProjectItem).project.Name != "alpha" {
		t.Errorf("want alpha, got %s", got[0].(ProjectItem).project.Name)
	}
}

func TestMergeRefreshedProjectsPreservesGitStatus(t *testing.T) {
	existing := []list.Item{
		ProjectItem{
			project: project.Project{Name: "alpha", Path: "/ws/alpha"},
			status:  StatusActive,
			git:     project.GitStatus{Branch: "main", Dirty: 3},
		},
	}
	refreshed := []project.Project{
		{Name: "alpha", Path: "/ws/alpha"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	alpha := got[0].(ProjectItem)
	if alpha.git.Branch != "main" || alpha.git.Dirty != 3 {
		t.Errorf("git status not preserved: got %+v", alpha.git)
	}
}

func TestMergeRefreshedProjectsSortedByName(t *testing.T) {
	existing := []list.Item{}
	refreshed := []project.Project{
		{Name: "zulu", Path: "/ws/zulu"},
		{Name: "alpha", Path: "/ws/alpha"},
		{Name: "mike", Path: "/ws/mike"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	if len(got) != 3 {
		t.Fatalf("want 3 items, got %d", len(got))
	}
	names := make([]string, len(got))
	for i, item := range got {
		names[i] = item.(ProjectItem).project.Name
	}
	for i := 0; i+1 < len(names); i++ {
		if names[i] >= names[i+1] {
			t.Errorf("not sorted: %v", names)
			break
		}
	}
}

func TestMergeRefreshedProjectsUpdatesProjectMetadata(t *testing.T) {
	// If a project's language detection changes on re-scan, pick up the new value.
	existing := []list.Item{
		ProjectItem{project: project.Project{Name: "alpha", Path: "/ws/alpha", Language: ""}, status: StatusActive},
	}
	refreshed := []project.Project{
		{Name: "alpha", Path: "/ws/alpha", Language: "go"},
	}

	got := mergeRefreshedProjects(existing, refreshed)
	alpha := got[0].(ProjectItem)
	if alpha.project.Language != "go" {
		t.Errorf("language not updated: want 'go', got %q", alpha.project.Language)
	}
	// Status should still be preserved.
	if alpha.status != StatusActive {
		t.Errorf("status not preserved: want Active, got %d", alpha.status)
	}
}

func TestMergeRefreshedProjectsEmptyRefreshClearsAll(t *testing.T) {
	existing := []list.Item{
		ProjectItem{project: project.Project{Name: "alpha", Path: "/ws/alpha"}, status: StatusActive},
	}

	got := mergeRefreshedProjects(existing, nil)
	if len(got) != 0 {
		t.Fatalf("want 0 items on empty refresh, got %d", len(got))
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
