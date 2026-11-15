package tui

import (
	"testing"

	"github.com/rvanmech/unky-mo/internal/project"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

func TestClassifyCWD(t *testing.T) {
	names := map[string]string{"/ws/alpha": "alpha"}
	git := gitLookups{
		root: func(dir string) string {
			switch dir {
			case "/ws/alpha/sub":
				return "/ws/alpha"
			case "/src/other/pkg":
				return "/src/other"
			}
			return ""
		},
		status: func(root string) project.GitStatus { return project.GitStatus{Branch: "topic", Dirty: 2} },
	}
	for _, c := range []struct {
		cwd  string
		want sessionView
	}{
		{"/ws/alpha", sessionView{ProjectPath: "/ws/alpha", ProjectName: "alpha", Section: "projects"}},
		{"/ws/alpha.worktrees/feat", sessionView{ProjectPath: "/ws/alpha", ProjectName: "@feat", Parent: "alpha", Section: "projects", IsWorktree: true}},
		{"/ws/alpha/sub", sessionView{ProjectPath: "/ws/alpha", ProjectName: "alpha", Section: "projects"}},
		{"/src/other/pkg", sessionView{ProjectPath: "/src/other", ProjectName: "other", Section: "projects", IsStray: true, Branch: "topic", Dirty: 2}},
		{"/tmp/scratch", sessionView{ProjectPath: "/tmp/scratch", ProjectName: "scratch", Section: "external", IsStray: true}},
	} {
		var v sessionView
		classifyCWD(&v, c.cwd, names, git)
		if v.ProjectPath != c.want.ProjectPath || v.ProjectName != c.want.ProjectName || v.Parent != c.want.Parent ||
			v.Section != c.want.Section || v.IsWorktree != c.want.IsWorktree || v.IsStray != c.want.IsStray ||
			v.Branch != c.want.Branch || v.Dirty != c.want.Dirty {
			t.Errorf("%s: %+v, want %+v", c.cwd, v, c.want)
		}
	}
}

// Agent windows: only non-Claude agents, not already claimed, not window 0.
func TestAgentWindowViews(t *testing.T) {
	windows := []ttmux.Window{
		{ID: "@0", Index: "0", Name: "home", AgentKey: "g", CWD: "/ws/alpha"},
		{ID: "@1", Index: "1", Name: "alpha", AgentKey: "c", CWD: "/ws/alpha"},
		{ID: "@2", Index: "2", Name: "alpha [2]", AgentKey: "g", CWD: "/ws/alpha"},
		{ID: "@3", Index: "3", Name: "beta", AgentKey: "g", CWD: "/ws/beta"},
		{ID: "@4", Index: "4", Name: "gamma", CWD: "/ws/gamma"},
	}
	git := gitLookups{root: func(string) string { return "" }, status: func(string) project.GitStatus { return project.GitStatus{} }}
	got := agentWindowViews(windows, []sessionView{{WindowID: "@3"}}, map[string]string{"/ws/alpha": "alpha"}, git)
	if len(got) != 1 || got[0].WindowID != "@2" || got[0].ProjectName != "alpha" || got[0].Index != 2 || got[0].Status != StatusActive {
		t.Errorf("got %+v", got)
	}
}
