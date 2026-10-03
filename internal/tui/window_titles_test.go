package tui

import (
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

// titleFixture wires syncWindowTitles' collaborators: each window hosts one
// live session (window i's pane PID is 100+i, session PID matches) with the
// given custom title. RenameWindow is left to each test — gomock fails on
// any rename the test didn't expect.
func titleFixture(t *testing.T, windows []ttmux.Window, titles []string) (*mock_ops.MockTmuxClient, Model) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)
	cl := mock_ops.NewMockClaudeReader(ctrl)

	sessions := make([]claude.Session, len(windows))
	for i, w := range windows {
		pid := 100 + i
		sessions[i] = claude.Session{PID: pid, SessionID: w.ID + "-sess", CWD: "/ws/foo"}
		tmux.EXPECT().WindowPanePIDs(w.ID).Return(map[int]bool{pid: true}, nil).AnyTimes()
		cl.EXPECT().CustomTitleFor("/ws/foo", w.ID+"-sess").Return(titles[i]).AnyTimes()
	}
	tmux.EXPECT().ListWindows().Return(windows, nil)
	cl.EXPECT().LiveSessions().Return(sessions, nil)
	cl.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).DoAndReturn(
		func(pid int, hosts map[int]bool) bool { return hosts[pid] }).AnyTimes()

	return tmux, Model{tmux: tmux, claude: cl}
}

// Regression: an untitled "foo [2]" next to a live "foo" used to be renamed
// to "foo [3]" (its own name counted as taken), then back to "foo [2]" on
// the next tick, forever.
func TestSyncWindowTitlesKeepsUntitledOrdinalSibling(t *testing.T) {
	_, m := titleFixture(t, []ttmux.Window{
		{ID: "@1", Name: "foo"},
		{ID: "@2", Name: "foo [2]"},
	}, []string{"", ""})

	m.syncWindowTitles() // no RenameWindow expected
}

func TestSyncWindowTitlesClearedTitleTakesNextOrdinal(t *testing.T) {
	tmux, m := titleFixture(t, []ttmux.Window{
		{ID: "@1", Name: "foo"},
		{ID: "@2", Name: "foo [2]"},
		{ID: "@3", Name: "foo [debug]"},
	}, []string{"", "", ""})
	tmux.EXPECT().RenameWindow("@3", "foo [3]").Return(nil)

	m.syncWindowTitles()
}

func TestSyncWindowTitlesOrdinalRevertsToFreeBareName(t *testing.T) {
	tmux, m := titleFixture(t, []ttmux.Window{
		{ID: "@2", Name: "foo [2]"},
	}, []string{""})
	tmux.EXPECT().RenameWindow("@2", "foo").Return(nil)

	m.syncWindowTitles()
}

func TestSyncWindowTitlesAppliesCustomTitle(t *testing.T) {
	tmux, m := titleFixture(t, []ttmux.Window{
		{ID: "@1", Name: "foo"},
		{ID: "@2", Name: "foo [2]"},
	}, []string{"", "debug"})
	tmux.EXPECT().RenameWindow("@2", "foo [debug]").Return(nil)

	m.syncWindowTitles()
}
