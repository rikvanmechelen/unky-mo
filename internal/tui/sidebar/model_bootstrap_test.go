package sidebar

import (
	"os"
	"testing"

	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// Phase A1: Characterization tests for NewModelWithDeps — lock in how the
// constructor wires resolver output and environment into the Model.

func TestNewModelWithDeps_UsesResolverWindowID(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)

	// refreshState fallback path (no state file) calls LiveSessions.
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	resolver := FakeWindowResolver{Name: "myproj", ID: "@5"}
	m := NewModelWithDeps("mo", "/nonexistent/state.json", tmux, claude, resolver)

	if m.windowID != "@5" {
		t.Errorf("windowID = %q, want @5", m.windowID)
	}
	if m.windowName != "myproj" {
		t.Errorf("windowName = %q, want myproj", m.windowName)
	}
}

func TestNewModelWithDeps_FallsBackToWindowNameOnEmptyID(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	resolver := FakeWindowResolver{Name: "my-window", ID: ""}
	m := NewModelWithDeps("mo", "/nonexistent/state.json", tmux, claude, resolver)

	if m.windowID != "" {
		t.Errorf("windowID should be empty, got %q", m.windowID)
	}
	if m.windowName != "my-window" {
		t.Errorf("windowName = %q, want my-window", m.windowName)
	}
}

func TestNewModelWithDeps_CapturesWorkingDir(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	resolver := FakeWindowResolver{Name: "proj", ID: "@1"}
	m := NewModelWithDeps("mo", "/nonexistent/state.json", tmux, claude, resolver)

	cwd, _ := os.Getwd()
	if m.windowPath != cwd {
		t.Errorf("windowPath = %q, want %q", m.windowPath, cwd)
	}
}

func TestNewModelWithDeps_StartsWithHomeItem(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	resolver := FakeWindowResolver{Name: "proj", ID: "@1"}
	m := NewModelWithDeps("mo", "/nonexistent/state.json", tmux, claude, resolver)

	if len(m.items) == 0 {
		t.Fatal("items should have at least the Home row")
	}
	if !m.items[0].IsHome {
		t.Errorf("first item should be Home, got %+v", m.items[0])
	}
}

func TestNewModelWithDeps_InitializesDefaultFields(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	resolver := FakeWindowResolver{Name: "proj", ID: "@7"}
	m := NewModelWithDeps("mo", "/nonexistent/state.json", tmux, claude, resolver)

	if m.activeTermIdx != -1 {
		t.Errorf("activeTermIdx = %d, want -1", m.activeTermIdx)
	}
	if m.focusSection != "sessions" {
		t.Errorf("focusSection = %q, want sessions", m.focusSection)
	}
	if m.stateFile != "/nonexistent/state.json" {
		t.Errorf("stateFile = %q, want /nonexistent/state.json", m.stateFile)
	}
}
