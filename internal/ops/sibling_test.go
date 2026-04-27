package ops

import (
	"errors"
	"testing"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

func TestLaunchSiblingPicksNextOrdinal(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	// Existing windows: project bare + project [2] → next should be project [3].
	tmux.EXPECT().ListWindows().Return([]ttmux.Window{
		{ID: "@1", Name: "alpha"},
		{ID: "@2", Name: "alpha [2]"},
	}, nil)
	// From there ops.LaunchSibling should call LaunchSession with window "alpha [3]".
	tmux.EXPECT().CreateWindow("alpha [3]", "/ws/alpha").Return("mo:@3", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	res, err := LaunchSibling(ctx, SiblingParams{
		ProjectName: "alpha",
		Branch:      "",
		Cwd:         "/ws/alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Target != "mo:@3" {
		t.Errorf("sibling target: got %q, want %q", res.Target, "mo:@3")
	}
}

func TestLaunchSiblingPassesResumeCommand(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().ListWindows().Return(nil, nil)
	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@2", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec claude --resume sess-abc").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	_, err := LaunchSibling(ctx, SiblingParams{
		ProjectName: "alpha",
		Cwd:         "/ws/alpha",
		ResumeID:    "sess-abc",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLaunchSiblingListWindowsFailure(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().ListWindows().Return(nil, errors.New("tmux down"))

	_, err := LaunchSibling(ctx, SiblingParams{
		ProjectName: "alpha",
		Cwd:         "/ws/alpha",
	})
	if err == nil {
		t.Error("want error on ListWindows failure")
	}
}

func TestLaunchSiblingRequiresProject(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	_, err := LaunchSibling(ctx, SiblingParams{ProjectName: "", Cwd: "/x"})
	if err == nil {
		t.Error("empty project should error")
	}
	_, err = LaunchSibling(ctx, SiblingParams{ProjectName: "alpha", Cwd: ""})
	if err == nil {
		t.Error("empty cwd should error")
	}
}

func TestParkAndLaunchKillsPrimaryWindowFirst(t *testing.T) {
	ctx, tmux, claude := newTestContext(t)

	// No real PID → SignalAndWaitExit should be a no-op (but claude.IsAlive
	// is invoked when PID > 0; for PID=0 it isn't). Use PID=0 here.
	tmux.EXPECT().SessionName().Return("mo")
	tmux.EXPECT().KillWindow("mo:alpha").Return(nil)
	// Then LaunchSession ceremony.
	tmux.EXPECT().CreateWindow("alpha", "/ws/alpha").Return("mo:@1", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	_, err := ParkAndLaunch(ctx, ParkParams{
		PID:               0, // skip signal path
		PrimaryWindowName: "alpha",
		Cwd:               "/ws/alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = claude
}

func TestSignalAndWaitExitNoopOnZeroPID(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	// No mocks expected — should simply return.
	SignalAndWaitExit(ctx, 0)
	SignalAndWaitExit(ctx, -5)
}
