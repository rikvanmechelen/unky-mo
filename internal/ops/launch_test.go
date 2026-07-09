package ops

import (
	"errors"
	"os"
	"path/filepath"
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
	// Disable path resolution so test expectations match bare command names.
	resolveShellCmdFn = func(s string) string { return s }
	t.Cleanup(func() { resolveShellCmdFn = resolveShellCmd })
	return &Context{
		Tmux:         tmux,
		Claude:       claude,
		MoBinaryPath: "/usr/local/bin/mo",
		SidebarWidth: 42,
	}, tmux, claude
}

// expectInstanceID adds the SetWindowOption expectation for @mo_instance_id
// that LaunchSession now calls after CreateWindow. Returns the gomock.Call
// so callers can chain .After() ordering.
func expectInstanceID(tmux *mock_ops.MockTmuxClient) *gomock.Call {
	return tmux.EXPECT().SetWindowOption(gomock.Any(), "@mo_instance_id", gomock.Any()).Return(nil)
}

func TestLaunchSessionHappyPath(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	// Script the full ceremony. CreateWindow returns an ID-based target
	// (session:@N) so dots in window names are never misinterpreted.
	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:@1", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID("mo:@1").Return("%12", nil)
	tmux.EXPECT().SendKeys("mo:@1", "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook("mo:@1", "pane-exited", gomock.Any()).
		Do(func(target, hook, cmd string) {
			if !strings.Contains(cmd, "%12") {
				t.Errorf("hook command should reference the claude pane ID, got %q", cmd)
			}
			if !strings.Contains(cmd, "kill-window") {
				t.Errorf("hook should kill-window, got %q", cmd)
			}
		})
	tmux.EXPECT().SplitWindow("mo:@1", 42, "/ws/myproj", gomock.Any()).
		Do(func(target string, cols int, cwd, cmd string) {
			if !strings.Contains(cmd, "--instance-id=") {
				t.Errorf("sidebar command should contain --instance-id=, got %q", cmd)
			}
		}).Return("%13", nil)
	tmux.EXPECT().SelectPane("mo:@1.0").Return(nil)
	tmux.EXPECT().SwitchToWindow("mo:@1").Return(nil)

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
	if res.Target != "mo:@1" {
		t.Errorf("target: got %q", res.Target)
	}
	if res.ClaudePaneID != "%12" {
		t.Errorf("pane ID not captured: %q", res.ClaudePaneID)
	}
	if res.InstanceID == "" {
		t.Error("InstanceID should be populated")
	}
	if len(res.InstanceID) != 12 {
		t.Errorf("InstanceID should be 12 hex chars, got %q", res.InstanceID)
	}
	if !res.SwitchedTo {
		t.Error("SwitchedTo should be true")
	}
}

func TestLaunchSessionResumeCommand(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:@1", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	// The `claude --resume <id>` shell command should appear verbatim.
	tmux.EXPECT().SendKeys("mo:@1", "exec claude --resume abc-123").Return(nil)
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

func TestLaunchSessionSetsAgentKey(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:@1", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().SetWindowOption("mo:@1", "@mo_agent", "g").Return(nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys("mo:@1", "exec gemini").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "myproj",
		Cwd:           "/ws/myproj",
		ShellCmd:      "gemini",
		AgentKey:      "g",
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		t.Fatalf("LaunchSession: %v", err)
	}
	if res.AgentKey != "g" {
		t.Errorf("AgentKey: want %q, got %q", "g", res.AgentKey)
	}
}

func TestLaunchSessionOmitsAgentKeyWhenEmpty(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow("myproj", "/ws/myproj").Return("mo:@1", nil)
	expectInstanceID(tmux)
	// No SetWindowOption("@mo_agent"...) call expected
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys("mo:@1", "exec claude").Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)
	tmux.EXPECT().SwitchToWindow(gomock.Any()).Return(nil)

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
	if res.AgentKey != "" {
		t.Errorf("AgentKey should be empty when not set, got %q", res.AgentKey)
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

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	expectInstanceID(tmux)
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

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	expectInstanceID(tmux)
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

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	expectInstanceID(tmux)
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

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	expectInstanceID(tmux)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys("mo:@1", "exec claude").Return(nil) // defaulted
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())

	_, err := LaunchSession(ctx, LaunchParams{WindowName: "x"})
	if err != nil {
		t.Fatal(err)
	}
}

// Phase A5 + B1: Ordering assertion — instance ID is set after window creation,
// sidebar receives --instance-id in its command.
func TestLaunchSessionSidebarSplitAfterClaudeExec(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	create := tmux.EXPECT().CreateWindow("proj", "/ws").Return("mo:@1", nil)
	setOpt := tmux.EXPECT().SetWindowOption("mo:@1", "@mo_instance_id", gomock.Any()).Return(nil).After(create)
	paneID := tmux.EXPECT().PaneID("mo:@1").Return("%1", nil).After(setOpt)
	sendKeys := tmux.EXPECT().SendKeys("mo:@1", "exec claude").Return(nil).After(paneID)
	hook := tmux.EXPECT().SetWindowHook("mo:@1", "pane-exited", gomock.Any()).After(sendKeys)
	split := tmux.EXPECT().SplitWindow("mo:@1", 42, "/ws", gomock.Any()).Return("%2", nil).After(hook)
	tmux.EXPECT().SelectPane("mo:@1.0").Return(nil).After(split)

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "proj",
		Cwd:           "/ws",
		AttachSidebar: true,
		SwitchFocus:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.InstanceID == "" {
		t.Error("InstanceID should be populated")
	}
}

func TestLaunchSessionThreadsInstanceIDIntoSidebarArgs(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	var capturedID string
	tmux.EXPECT().SetWindowOption(gomock.Any(), "@mo_instance_id", gomock.Any()).
		Do(func(target, opt, value string) {
			capturedID = value
		}).Return(nil)
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), gomock.Any()).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(target string, cols int, cwd, cmd string) {
			wantSuffix := "--instance-id=" + capturedID
			if !strings.Contains(cmd, wantSuffix) {
				t.Errorf("sidebar command should contain %q, got %q", wantSuffix, cmd)
			}
		}).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "proj",
		Cwd:           "/ws",
		AttachSidebar: true,
		SwitchFocus:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.InstanceID != capturedID {
		t.Errorf("result InstanceID %q should match window option %q", res.InstanceID, capturedID)
	}
}

func TestLaunchSessionSetWindowOptionFailureOmitsInstanceID(t *testing.T) {
	ctx, tmux, _ := newTestContext(t)

	tmux.EXPECT().CreateWindow(gomock.Any(), gomock.Any()).Return("mo:@1", nil)
	tmux.EXPECT().SetWindowOption(gomock.Any(), "@mo_instance_id", gomock.Any()).
		Return(errors.New("window not found"))
	tmux.EXPECT().PaneID(gomock.Any()).Return("%1", nil)
	tmux.EXPECT().SendKeys(gomock.Any(), gomock.Any()).Return(nil)
	tmux.EXPECT().SetWindowHook(gomock.Any(), gomock.Any(), gomock.Any())
	// Sidebar command should NOT contain --instance-id when SetWindowOption failed.
	tmux.EXPECT().SplitWindow(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Do(func(target string, cols int, cwd, cmd string) {
			if strings.Contains(cmd, "--instance-id") {
				t.Errorf("sidebar should not get --instance-id when SetWindowOption failed, got %q", cmd)
			}
		}).Return("%2", nil)
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil)

	res, err := LaunchSession(ctx, LaunchParams{
		WindowName:    "proj",
		Cwd:           "/ws",
		AttachSidebar: true,
		SwitchFocus:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.InstanceID != "" {
		t.Errorf("InstanceID should be empty when SetWindowOption fails, got %q", res.InstanceID)
	}
}

func TestLaunchSessionRequiresWindowName(t *testing.T) {
	ctx, _, _ := newTestContext(t)
	_, err := LaunchSession(ctx, LaunchParams{WindowName: ""})
	if err == nil {
		t.Error("empty WindowName should error")
	}
}

func TestResolveShellCmdAbsolutePathUnchanged(t *testing.T) {
	got := resolveShellCmd("/usr/bin/claude --resume abc")
	if got != "/usr/bin/claude --resume abc" {
		t.Errorf("absolute path should be unchanged, got %q", got)
	}
}

func TestResolveShellCmdUnknownBinaryUnchanged(t *testing.T) {
	got := resolveShellCmd("nonexistent-binary-xyzzy --flag")
	if got != "nonexistent-binary-xyzzy --flag" {
		t.Errorf("unknown binary should be unchanged, got %q", got)
	}
}

func TestResolveAsdfShimParsesPluginComment(t *testing.T) {
	dir := t.TempDir()

	// Create a fake asdf directory structure.
	shimDir := filepath.Join(dir, ".asdf", "shims")
	os.MkdirAll(shimDir, 0o755)
	installDir := filepath.Join(dir, ".asdf", "installs", "nodejs", "20.0.0", "bin")
	os.MkdirAll(installDir, 0o755)
	os.WriteFile(filepath.Join(installDir, "claude"), []byte("#!/bin/sh\n"), 0o755)

	shimPath := filepath.Join(shimDir, "claude")
	shimContent := "#!/usr/bin/env bash\n# asdf-plugin: nodejs 20.0.0\nexec asdf exec \"claude\" \"$@\"\n"
	os.WriteFile(shimPath, []byte(shimContent), 0o755)

	// Override HOME so resolveAsdfShim finds our fake install dir.
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", orig)

	got := resolveAsdfShim(shimPath, "claude")
	want := filepath.Join(installDir, "claude")
	if got != want {
		t.Errorf("resolveAsdfShim: got %q, want %q", got, want)
	}
}

func TestResolveAsdfShimNonShimPathReturnsEmpty(t *testing.T) {
	got := resolveAsdfShim("/usr/local/bin/claude", "claude")
	if got != "" {
		t.Errorf("non-shim path should return empty, got %q", got)
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
