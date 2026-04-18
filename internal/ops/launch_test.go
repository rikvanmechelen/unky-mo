package ops

import (
	"errors"
	"strings"
	"testing"

	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	"go.uber.org/mock/gomock"
)

func newTestContext(t *testing.T) (*Context, *mock_ops.MockTmuxClient, *mock_ops.MockClaudeReader) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)
	claude := mock_ops.NewMockClaudeReader(ctrl)
	return &Context{
		Tmux:         tmux,
		Claude:       claude,
		MoBinaryPath: "/usr/local/bin/mo",
		SidebarWidth: 42,
	}, tmux, claude
}

func TestLaunchSessionHappyPath(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	// Script the full ceremony.
	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:myproj", nil)
	tmux.EXPECT().PaneID("mo:myproj").Return("%12", nil)
	tmux.EXPECT().SendKeys("mo:myproj", "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook("mo:myproj", "pane-exited", gomock.Any()).
		Do(func(target, hook, cmd string) {
			if !strings.Contains(cmd, "%12") {
				t.Errorf("hook command should reference the claude pane ID, got %q", cmd)
			}
			if !strings.Contains(cmd, "kill-window") {
				t.Errorf("hook should kill-window, got %q", cmd)
			}
		})
	tmux.EXPECT().SplitWindow("mo:myproj", 42, "/ws/myproj", "/usr/local/bin/mo sidebar").Return("%13", nil)
	tmux.EXPECT().SelectPane("mo:myproj.0").Return(nil)
	tmux.EXPECT().SwitchToWindow("mo:myproj").Return(nil)

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		Cwd:           "/ws/myproj",
		ShellCmd:      "claude",
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	if res.Target != "mo:myproj" {
		t.Errorf("target: got %q", res.Target)
	}
	if res.ClaudePaneID != "%12" {
		t.Errorf("pane ID not captured: %q", res.ClaudePaneID)
	}
	if !res.SwitchedTo {
		t.Error("SwitchedTo should be true")
	}
}

func TestLaunchSessionResumeCommand(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:myproj", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	// The `claude --resume <id>` shell command should appear verbatim.
	tmux.EXPECT().SendKeys("mo:myproj", "exec claude --resume abc-123").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), "pane-exited", gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	_, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		Cwd:           "/ws/myproj",
		ShellCmd:      "claude --resume abc-123",
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLaunchSessionCreateWindowError(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)
	tmux.EXPECT().CreateWindow("myproj", "/ws").Return("", errors.New("tmux: duplicate window"))

	_, err := LaunchSession(ctx, LaunchParams{
		WindowName: "myproj",
		Cwd:        "/ws",
	})
	if err == nil || !strings.Contains(err.Error(), "create window") {
		t.Errorf("want create-window error, got %v", err)
	}
}

func TestLaunchSessionSwitchFailureStillReturnsResult(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:myproj", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), gomock.Any()).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(errors.New("no client attached"))

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "switch to window") {
		t.Errorf("want switch error, got %v", err)
	}
	if res == nil {
		t.Fatal("result should be non-nil even on switch failure — caller can still see Target")
	}
	if res.SwitchedTo {
		t.Error("SwitchedTo should be false on switch error")
	}
}

func TestLaunchSessionNoSidebar(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:myproj", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), gomock.Any()).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	// NO SplitWindow / SelectPane — AttachSidebar=false.
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	_, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		AttachSidebar: false,
		SwitchFocus:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLaunchSessionNoSwitch(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:myproj", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), gomock.Any()).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	// NO SwitchToWindow.

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		AttachSidebar: true,
		SwitchFocus:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SwitchedTo {
		t.Error("SwitchedTo should be false when SwitchFocus=false")
	}
}

func TestLaunchSessionDefaultsShellCmdToClaude(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:x", nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys("mo:x", "exec claude").Return(nil) // defaulted
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())

	_, err := LaunchSession(ctx, LaunchParams{WindowName: "x"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLaunchSessionRequiresWindowName(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	_, err := LaunchSession(ctx, LaunchParams{WindowName: ""})
	if err == nil {
		t.Error("empty WindowName should error")
	}
}

func TestLaunchSessionNilContextIsError(t *testing.T) {
	_, err := LaunchSession(nil, LaunchParams{WindowName: "x"})
	if err == nil {
		t.Error("nil ctx should error")
	}
	ctx := &Context{} // no Tmux
	_, err = LaunchSession(ctx, LaunchParams{WindowName: "x"})
	if err == nil {
		t.Error("nil Tmux should error")
	}
}
