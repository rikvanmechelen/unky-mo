package sidebar

import (
	"testing"

	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

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
