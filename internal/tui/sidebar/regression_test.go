package sidebar

import (
	"path/filepath"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// writeRegressionStateFile writes sf to a temp path and returns the path.
// Wrapper around writeStateFile from state_test.go for tests that don't
// otherwise care where the file lives.
func writeRegressionStateFile(t *testing.T, sf *state.StateFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	writeStateFile(t, path, sf)
	return path
}

// Phase 5 regression tests document the WindowID-vs-WindowName drift bugs.
// Tests that correspond to a shipped fix pass on the default build. Tests
// that document still-broken behaviour are tagged `//go:build sidebarregression`
// (see regression_todo_test.go) so they stay out of CI until the fix lands.

func TestSwitchToSelectedUsesWindowIDWhenAvailable(t *testing.T) {
	// Regression: when the main TUI renames a window (/rename custom-title,
	// or park → sibling ordinal), the SidebarItem's WindowName can be stale
	// but its WindowID is still correct. switchToSelected must target by
	// ID so tmux can resolve the actual window.
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	m := Model{
		tmux: tmux,
		items: []SidebarItem{
			{Name: "Home", IsHome: true},
			{Name: "alpha", WindowName: "alpha", WindowID: "@5"},
		},
		cursor: 1,
	}

	// The window was just renamed to "alpha [wip]" but our cached
	// WindowName is still "alpha". tmux should be targeted by @5.
	tmux.EXPECT().SwitchToWindow("@5").Return(nil)

	cmd := m.switchToSelected()
	if cmd == nil {
		t.Fatal("switchToSelected returned nil")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("switchToSelected should return nil msg, got %v", msg)
	}
}

func TestSwitchToSelectedFallsBackToNameWhenNoID(t *testing.T) {
	// Cold rows (placeholders for projects without a live session) don't
	// carry a WindowID. The fallback path builds sessionName:windowName.
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	m := Model{
		tmux: tmux,
		items: []SidebarItem{
			{Name: "Home", IsHome: true},
			{Name: "alpha", WindowName: "alpha" /* no WindowID */},
		},
		cursor: 1,
	}
	tmux.EXPECT().SwitchToWindow("mo:alpha").Return(nil)

	m.switchToSelected()()
}

func TestSwitchToSelectedHomeStillTargetsWindow0(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	m := Model{
		tmux:   tmux,
		items:  []SidebarItem{{Name: "Home", IsHome: true}},
		cursor: 0,
	}
	tmux.EXPECT().SwitchToWindow("mo:0").Return(nil)

	m.switchToSelected()()
}

// Regression: syncPush and refreshSyncStatus used m.windowName directly as the
// sync project name. When the window was renamed (e.g. "/rename wip" → "alpha [wip]"),
// the bracket suffix leaked into the DirHash, making pull impossible. Fixed by
// introducing syncProjectName() which strips the suffix via ParseWindowName.
func TestSyncProjectNameSurvivesWindowRename(t *testing.T) {
	// Simulate: window was "alpha", user renamed to "alpha [wip]".
	m := Model{windowName: "alpha [wip]"}
	if got := m.syncProjectName(); got != "alpha" {
		t.Errorf("syncProjectName() = %q after rename, want %q", got, "alpha")
	}

	// Worktree variant: "unky-mo@feat [debug]" → "unky-mo@feat".
	m.windowName = "unky-mo@feat [debug]"
	if got := m.syncProjectName(); got != "unky-mo@feat" {
		t.Errorf("syncProjectName() = %q, want %q", got, "unky-mo@feat")
	}
}

func TestSwitchToSelectedNoTargetForBlankRow(t *testing.T) {
	// A row with neither IsHome nor WindowID nor WindowName should no-op.
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	// NO SwitchToWindow expectation — test fails if it's called.

	m := Model{
		tmux:   tmux,
		items:  []SidebarItem{{Name: "placeholder"}},
		cursor: 0,
	}
	m.switchToSelected()()
}

// TestActiveShellsForConcurrentSiblingsUsesOwnSessionPID guards the
// "shell shows in both moma-chatbot sidebars" bug. When two concurrent
// Claude sessions share a CWD (e.g. main + concurrent in the same project),
// each sidebar must look up shells by its OWN session's PID, not by path —
// otherwise a path-based lookup returns an arbitrary first match and both
// sidebars end up displaying the same shell list.
func TestActiveShellsForConcurrentSiblingsUsesOwnSessionPID(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	cr := mock_sidebar.NewMockClaudeReader(ctrl)

	// Two siblings at the same path. Session 100 lives in window @1; session
	// 200 lives in window @2. Both reachable via SessionsForPath.
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1", CWD: "/ws/api"},
		{PID: 200, SessionID: "s2", CWD: "/ws/api"},
	}
	cr.EXPECT().SessionsForPath("/ws/api").Return(candidates).AnyTimes()
	cr.EXPECT().LiveSessions().Return(candidates, nil).AnyTimes()

	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	// Window @2's pane tree contains PID 99 (its shell). Only session 200
	// is a descendant of that pane.
	pane2 := map[int]bool{99: true}
	tmux.EXPECT().WindowPanePIDs("mo:beta").Return(pane2, nil).AnyTimes()
	cr.EXPECT().IsDescendantOf(100, gomock.Any()).Return(false).AnyTimes()
	cr.EXPECT().IsDescendantOf(200, gomock.Any()).Return(true).AnyTimes()

	// Distinct shell lists per session. ActiveShells(100) must NOT be called
	// from the @2 sidebar — gomock fails the test if it is, since we only
	// expect ActiveShells(200).
	ownShells := []claude.ActiveShell{{PID: 5000, Command: "rspec"}}
	cr.EXPECT().ActiveShells(200).Return(ownShells).MinTimes(1)
	// A no-op for ProjectsDirForPath so the JSONL-token path doesn't blow up.
	cr.EXPECT().ProjectsDirForPath(gomock.Any()).Return(t.TempDir()).AnyTimes()

	// State file: one project row matching the @2 window. Empty AgentKey ⇒
	// resolves as Claude. Empty InstanceID ⇒ ownSessionID falls through to
	// ownWindowSession (which exercises the same disambiguation).
	sf := writeRegressionStateFile(t, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "api", Path: "/ws/api", WindowID: "@2", WindowName: "beta", Status: "active"},
		},
	})

	m := &Model{
		tmux:          tmux,
		claude:        cr,
		resolver:      FakeWindowResolver{Name: "beta", ID: "@2"},
		stateFile:     sf,
		windowName:    "beta",
		windowID:      "@2",
		windowPath:    "/ws/api",
		activeTermIdx: -1,
		focusSection:  "sessions",
	}

	m.refreshState()

	if len(m.activeShells) != 1 {
		t.Fatalf("expected 1 shell from own session 200; got %d: %+v",
			len(m.activeShells), m.activeShells)
	}
	if m.activeShells[0].Command != "rspec" {
		t.Errorf("got shell from wrong session: %+v", m.activeShells[0])
	}
}
