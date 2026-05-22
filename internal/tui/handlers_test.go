package tui

import (
	"errors"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"github.com/rvanmech/unky-mo/internal/status"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

func TestPrimaryWindowForTargetNoSessions(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	cr.EXPECT().SessionsForPath("/ws/alpha").Return(nil)

	m := Model{claude: cr, tmux: tc}
	name, sess := m.primaryWindowForTarget("alpha", "", "/ws/alpha")
	if name != "" || sess != nil {
		t.Errorf("no sessions: want empty/nil, got (%q, %v)", name, sess)
	}
}

func TestPrimaryWindowForTargetPicksOldest(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	// Two sessions at same path, different StartedAt — oldest wins.
	sessions := []claude.Session{
		{PID: 10, SessionID: "newer", CWD: "/ws/alpha", StartedAt: 1100},
		{PID: 20, SessionID: "older", CWD: "/ws/alpha", StartedAt: 1000},
	}
	cr.EXPECT().SessionsForPath("/ws/alpha").Return(sessions)
	// sessionToWindowMap asks for ListWindows + per-window pane PIDs + LiveSessions.
	tc.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@1", Name: "alpha"},
	}, nil)
	cr.EXPECT().LiveSessions().Return(sessions, nil)
	tc.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{99: true}, nil)
	// Older session (PID 20) is the descendant of pane PID 99.
	cr.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).DoAndReturn(
		func(pid int, hosts map[int]bool) bool {
			return pid == 20
		}).AnyTimes()

	m := Model{claude: cr, tmux: tc}
	name, sess := m.primaryWindowForTarget("alpha", "", "/ws/alpha")
	if sess == nil {
		t.Fatal("expected primary session")
	}
	if sess.SessionID != "older" {
		t.Errorf("oldest should win; got SessionID %q", sess.SessionID)
	}
	if name != "alpha" {
		t.Errorf("window name: got %q", name)
	}
}

func TestPrimaryWindowForTargetFallsBackToComposedName(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	sessions := []claude.Session{
		{PID: 20, SessionID: "solo", CWD: "/ws/alpha/wt", StartedAt: 1000},
	}
	cr.EXPECT().SessionsForPath("/ws/alpha/wt").Return(sessions)
	// No windows resolvable → falls back to ComposeWindowName.
	tc.EXPECT().ListWindows().Return(nil, errors.New("tmux unavailable"))

	m := Model{claude: cr, tmux: tc}
	name, _ := m.primaryWindowForTarget("alpha", "feat", "/ws/alpha/wt")
	if name != "alpha@feat" {
		t.Errorf("compose fallback: want alpha@feat, got %q", name)
	}
}

func TestSessionToWindowMapEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	tc.EXPECT().ListWindows().Return(nil, nil)

	m := Model{claude: cr, tmux: tc}
	got := m.sessionToWindowMap()
	if len(got) != 0 {
		t.Errorf("empty windows → empty map; got %v", got)
	}
}

func TestSessionToWindowMapAttributesByPPIDChain(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	tc.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@1", Name: "alpha"},
		{ID: "@2", Name: "beta"},
	}, nil)
	cr.EXPECT().LiveSessions().Return([]claude.Session{
		{PID: 100, SessionID: "A-session"},
		{PID: 200, SessionID: "B-session"},
	}, nil)
	tc.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{10: true}, nil)
	tc.EXPECT().WindowPanePIDs("@2").Return(map[int]bool{20: true}, nil)
	// Session 100 belongs to pane 10; session 200 belongs to pane 20.
	cr.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).DoAndReturn(
		func(pid int, hosts map[int]bool) bool {
			switch pid {
			case 100:
				return hosts[10]
			case 200:
				return hosts[20]
			}
			return false
		}).AnyTimes()

	m := Model{claude: cr, tmux: tc}
	got := m.sessionToWindowMap()
	if got["A-session"] != "alpha" {
		t.Errorf("A-session should map to alpha, got %q", got["A-session"])
	}
	if got["B-session"] != "beta" {
		t.Errorf("B-session should map to beta, got %q", got["B-session"])
	}
}

func TestSessionToWindowMapSkipsUnreachableWindow(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	tc.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@1", Name: "alpha"},
		{ID: "@2", Name: "zombie"},
	}, nil)
	cr.EXPECT().LiveSessions().Return([]claude.Session{
		{PID: 100, SessionID: "A-session"},
	}, nil)
	tc.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{10: true}, nil)
	tc.EXPECT().WindowPanePIDs("@2").Return(nil, errors.New("window gone"))
	cr.EXPECT().IsDescendantOf(100, map[int]bool{10: true}).Return(true)

	m := Model{claude: cr, tmux: tc}
	got := m.sessionToWindowMap()
	if got["A-session"] != "alpha" {
		t.Errorf("should still resolve A-session when zombie window errors, got %q", got["A-session"])
	}
}

func TestResolveSessionWindowsWithMocks(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)

	tc.EXPECT().ListWindows().Return([]ttmux.Window{{ID: "@1", Name: "alpha"}}, nil)
	tc.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{42: true}, nil)
	cr.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).Return(true)

	got := resolveSessionWindows(tc, cr, []claude.Session{{PID: 100, SessionID: "s"}})
	if got["s"].Name != "alpha" {
		t.Errorf("want alpha, got %q", got["s"].Name)
	}
	if got["s"].ID != "@1" {
		t.Errorf("want @1, got %q", got["s"].ID)
	}
}

func TestResolveSessionWindowsNilTmux(t *testing.T) {
	got := resolveSessionWindows(nil, nil, []claude.Session{{PID: 1, SessionID: "s"}})
	if len(got) != 0 {
		t.Errorf("nil tmux → empty map, got %v", got)
	}
}

func TestFocusPrimaryIfLiveNoSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	cr.EXPECT().SessionsForPath("/ws/alpha").Return(nil)

	m := Model{claude: cr, tmux: tc}
	attempted, win, err := m.focusPrimaryIfLive("alpha", "", "/ws/alpha")
	if attempted || win != "" || err != nil {
		t.Errorf("no live session: want (false, \"\", nil); got (%v, %q, %v)", attempted, win, err)
	}
}

// TestMgrStatusToTUI verifies the status.SessionStatus → tui.SessionStatus mapping.
func TestMgrStatusToTUI(t *testing.T) {
	cases := map[status.SessionStatus]SessionStatus{
		status.StatusNone:       StatusNone,
		status.StatusActive:     StatusActive,
		status.StatusIdle:       StatusIdle,
		status.StatusPermission: StatusPermission,
		status.StatusExternal:   StatusExternal,
	}
	for in, want := range cases {
		if got := mgrStatusToTUI(in); got != want {
			t.Errorf("mgrStatusToTUI(%v) = %v, want %v", in, got, want)
		}
	}
}
