package ops

import (
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

// NoSwitch is what the web dashboard sets: every op must launch / locate the
// window exactly as before, but never call SwitchToWindow — the mock
// controller fails the test on any unexpected call, so leaving the
// SwitchToWindow expectation out is the assertion.

// expectLaunchCeremonyNoSwitch scripts LaunchSession minus the focus switch.
func expectLaunchCeremonyNoSwitch(tmux *mock_ops.MockTmuxClient, window, target, shellCmd string) {
	tmux.EXPECT().CreateWindow(window, gomock.Any()).Return(target, nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec "+shellCmd).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
}

func TestLaunchSiblingNoSwitch(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().ListWindows().Return([]ttmux.Window{{ID: "@1", Name: "alpha"}}, nil)
	expectLaunchCeremonyNoSwitch(tmux, "alpha [2]", "mo:@2", "claude")

	res, err := LaunchSibling(ctx, SiblingParams{ProjectName: "alpha", Cwd: "/ws/alpha", NoSwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SwitchedTo {
		t.Error("SwitchedTo should be false with NoSwitch")
	}
}

func TestParkAndLaunchNoSwitch(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().SessionName().Return("mo")
	tmux.EXPECT().KillWindow("mo:alpha").Return(nil)
	expectLaunchCeremonyNoSwitch(tmux, "alpha", "mo:@3", "claude --resume sess-1")

	res, err := ParkAndLaunch(ctx, ParkParams{
		PrimaryWindowName: "alpha",
		Cwd:               "/ws/alpha",
		ResumeID:          "sess-1",
		NoSwitch:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != "mo:@3" {
		t.Errorf("target: got %q", res.Target)
	}
}

func TestResumeInDirNoSwitchExistingWindow(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().WindowExists("alpha").Return(true)

	res, err := ResumeInDir(ctx, ResumeParams{WindowName: "alpha", Cwd: "/ws/alpha", NoSwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Switched || res.Relaunched {
		t.Errorf("existing window with NoSwitch should be a no-op, got %+v", res)
	}
	if res.Target != "alpha" {
		t.Errorf("target: got %q", res.Target)
	}
}

func TestResumeInDirNoSwitchRelaunch(t *testing.T) {
	ctx, tmux, cr := newTestContext(t)
	// SessionToWindowMap: one window, no live sessions → no remap.
	tmux.EXPECT().ListWindows().Return([]ttmux.Window{{ID: "@1", Name: "other"}}, nil)
	cr.EXPECT().LiveSessions().Return(nil, nil)
	tmux.EXPECT().WindowExists("alpha").Return(false)
	expectLaunchCeremonyNoSwitch(tmux, "alpha", "mo:@4", "claude --resume sess-2")

	res, err := ResumeInDir(ctx, ResumeParams{SessionID: "sess-2", WindowName: "alpha", Cwd: "/ws/alpha", NoSwitch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Relaunched || res.Target != "mo:@4" {
		t.Errorf("got %+v", res)
	}
}

func TestCreateWorktreeAndLaunchNoSwitch(t *testing.T) {
	repo := newGitRepo(t)
	ctx, tmux, cr := newTestContext(t)
	cr.EXPECT().SessionsForPath(gomock.Any()).Return(nil).AnyTimes()
	tmux.EXPECT().WindowExists("alpha@feat").Return(false)
	expectLaunchCeremonyNoSwitch(tmux, "alpha@feat", "mo:@5", "claude")

	res, err := CreateWorktreeAndLaunch(ctx, WorktreeParams{
		ProjectName: "alpha",
		ProjectPath: repo,
		Branch:      "feat",
		NoSwitch:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Launched || res.Target != "mo:@5" {
		t.Errorf("got %+v", res)
	}
}

func TestCreateWorktreeAndLaunchNoSwitchExistingWindow(t *testing.T) {
	repo := newGitRepo(t)
	ctx, tmux, cr := newTestContext(t)
	cr.EXPECT().SessionsForPath(gomock.Any()).Return(nil).AnyTimes()
	tmux.EXPECT().WindowExists("alpha@feat").Return(true)

	res, err := CreateWorktreeAndLaunch(ctx, WorktreeParams{
		ProjectName: "alpha",
		ProjectPath: repo,
		Branch:      "feat",
		NoSwitch:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Launched || res.Target != "" || res.WindowName != "alpha@feat" {
		t.Errorf("got %+v", res)
	}
}

func TestAgentShellCmd(t *testing.T) {
	claudeAgent := &config.AgentConfig{Key: "c", Cmd: "claude", ResumeCmd: "claude --resume"}
	noResume := &config.AgentConfig{Key: "g", Cmd: "gemini"}
	cases := []struct {
		name     string
		agent    *config.AgentConfig
		resumeID string
		want     string
	}{
		{"fresh", claudeAgent, "", "claude"},
		{"resume", claudeAgent, "abc", "claude --resume abc"},
		{"agent without resume launches fresh", noResume, "abc", "gemini"},
		{"nil agent fresh", nil, "", "claude"},
		{"nil agent resume", nil, "abc", "claude --resume abc"},
	}
	for _, c := range cases {
		if got := AgentShellCmd(c.agent, c.resumeID); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStopSessionsKillsResolvedWindows(t *testing.T) {
	ctx, tmux, cr := newTestContext(t)
	// PID 0 skips the signal phase; the window is still resolved and killed.
	sessions := []claude.Session{{SessionID: "s1", PID: 0}}
	tmux.EXPECT().ListWindows().Return([]ttmux.Window{{ID: "@1", Name: "a"}, {ID: "@2", Name: "b"}}, nil)
	tmux.EXPECT().WindowPanePIDs("@1").Return(map[int]bool{10: true}, nil)
	tmux.EXPECT().WindowPanePIDs("@2").Return(map[int]bool{20: true}, nil)
	cr.EXPECT().IsDescendantOf(0, map[int]bool{10: true}).Return(false)
	cr.EXPECT().IsDescendantOf(0, map[int]bool{20: true}).Return(true)
	tmux.EXPECT().SessionName().Return("mo")
	tmux.EXPECT().KillWindow("mo:@2").Return(nil)

	if n := StopSessions(ctx, sessions); n != 1 {
		t.Errorf("stopped: got %d, want 1", n)
	}
}

func TestStopSessionsEmptyIsNoop(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	if n := StopSessions(ctx, nil); n != 0 {
		t.Errorf("got %d", n)
	}
}
