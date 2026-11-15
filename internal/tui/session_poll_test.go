package tui

import (
	"path/filepath"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/status"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

// refreshSessions turns `claude agents` output into one view per session:
// placed by cwd, given its window by pane ancestry, its status from the
// status manager, and an external one (not under mo's tmux) marked so.
func TestRefreshSessionsViews(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctrl := gomock.NewController(t)
	tc := mock_ops.NewMockTmuxClient(ctrl)
	cr := mock_ops.NewMockClaudeReader(ctrl)
	outside := t.TempDir() // no repo: an external stray
	sessions := []claude.Session{
		{PID: 10, SessionID: "s-alpha", CWD: "/ws/alpha"},
		{PID: 11, SessionID: "s-feat", CWD: "/ws/alpha.worktrees/feat"},
		{PID: 12, SessionID: "s-ext", CWD: outside, Name: "my scratch"},
	}
	cr.EXPECT().LiveSessions().Return(sessions, nil).AnyTimes()
	cr.EXPECT().ProjectsDirForPath(gomock.Any()).Return(t.TempDir()).AnyTimes()
	tc.EXPECT().PanePIDs().Return(map[int]bool{100: true}, nil).AnyTimes()
	cr.EXPECT().IsDescendantOf(gomock.Any(), gomock.Any()).DoAndReturn(func(pid int, hosts map[int]bool) bool {
		if hosts[100] { // all of mo's panes: s-ext runs outside them
			return pid != 12
		}
		return hosts[pid] // one window's panes
	}).AnyTimes()
	tc.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@1", Index: "1", Name: "alpha"},
		{ID: "@2", Index: "2", Name: "alpha@feat"},
	}, nil).AnyTimes()
	tc.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{10: true}, nil).AnyTimes()
	tc.EXPECT().WindowPanePIDs("@2").Return(map[int]bool{11: true}, nil).AnyTimes()

	mgr := status.NewManager()
	mgr.ProcessHookEvent(status.HookEvent{SessionID: "s-alpha", Type: status.EventUserPromptSubmit})
	mgr.ProcessHookEvent(status.HookEvent{SessionID: "s-feat", Type: status.EventStop})

	projects := []project.Project{{Name: "alpha", Path: "/ws/alpha"}}
	m := NewModelWithDeps(projects, tc, cr, nil, nil, mgr, filepath.Join(t.TempDir(), "s.json"), config.TicketsConfig{}, nil, nil, nil)
	msg, ok := m.refreshSessions()().(sessionRefreshMsg)
	if !ok {
		t.Fatal("not a sessionRefreshMsg")
	}
	byID := map[string]sessionView{}
	for _, v := range msg.views {
		byID[v.SessionID] = v
	}
	if v := byID["s-alpha"]; v.ProjectName != "alpha" || v.WindowID != "@1" || v.Status != StatusActive || v.IsStray {
		t.Errorf("alpha: %+v", v)
	}
	if v := byID["s-feat"]; v.ProjectName != "@feat" || v.Parent != "alpha" || !v.IsWorktree || v.WindowID != "@2" || v.Status != StatusIdle {
		t.Errorf("feat: %+v", v)
	}
	if v := byID["s-ext"]; v.Status != StatusExternal || v.Section != "external" || v.ProjectName != "my scratch" || !v.External {
		t.Errorf("ext: %+v", v)
	}
	if msg.externalSessions[outside] != "s-ext" || msg.externalPIDs[outside] != 12 {
		t.Errorf("import caches: %v %v", msg.externalSessions, msg.externalPIDs)
	}
}
