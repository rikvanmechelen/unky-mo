package ops

import (
	"fmt"
	"os"
	"syscall"
	"time"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// ParkParams drives ParkAndLaunch.
type ParkParams struct {
	PID               int    // claude PID of the session to park (SIGINT + wait)
	PrimaryWindowName string // name of the window hosting that session; will be killed
	Cwd               string // working dir for the replacement window
	ResumeID          string // if non-empty, replacement runs `claude --resume <id>`
}

// ParkAndLaunch signals the current primary's Claude to exit, waits for it
// to die, kills its tmux window (so the sidebar + any terminal-drawer panes
// go with it), then launches a fresh session in a new window under the same
// primary name. Ported from tui.Model.parkAndLaunchPrimary.
func ParkAndLaunch(ctx *Context, p ParkParams) (*LaunchResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.PrimaryWindowName == "" {
		return nil, fmt.Errorf("ops.ParkAndLaunch: PrimaryWindowName required")
	}
	SignalAndWaitExit(ctx, p.PID)
	_ = ctx.Tmux.KillWindow(ctx.Tmux.SessionName() + ":" + p.PrimaryWindowName)
	shellCmd := "claude"
	if p.ResumeID != "" {
		shellCmd = "claude --resume " + p.ResumeID
	}
	return LaunchSession(ctx, LaunchParams{
		WindowName:    p.PrimaryWindowName,
		Cwd:           p.Cwd,
		ShellCmd:      shellCmd,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
}

// SiblingParams drives LaunchSibling.
type SiblingParams struct {
	ProjectName string
	Branch      string // "" for main-checkout siblings
	Cwd         string
	ResumeID    string // optional; resume a specific historical session in the new sibling
}

// LaunchSibling always creates a new concurrent sibling window for the
// target, even if a primary already exists. Names the new window
// "project [N]" (or "project@branch [N]") using the lowest unused ordinal.
// Ported from tui.Model.launchSiblingSession.
func LaunchSibling(ctx *Context, p SiblingParams) (*LaunchResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.ProjectName == "" || p.Cwd == "" {
		return nil, fmt.Errorf("ops.LaunchSibling: ProjectName and Cwd required")
	}
	windows, err := ctx.Tmux.ListWindows()
	if err != nil {
		return nil, fmt.Errorf("list windows: %w", err)
	}
	names := make([]string, 0, len(windows))
	for _, w := range windows {
		names = append(names, w.Name)
	}
	ordinal := ttmux.NextAvailableOrdinal(names, p.ProjectName, p.Branch)
	windowName := ttmux.ComposeWindowName(p.ProjectName, p.Branch, ordinal)
	shellCmd := "claude"
	if p.ResumeID != "" {
		shellCmd = "claude --resume " + p.ResumeID
	}
	return LaunchSession(ctx, LaunchParams{
		WindowName:    windowName,
		Cwd:           p.Cwd,
		ShellCmd:      shellCmd,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
}

// SignalAndWaitExit sends SIGINT to pid (so Claude flushes its JSONL
// cleanly), waits up to ~2s for it to exit, then falls back to SIGTERM for
// another ~1s. No-op on pid <= 0. Exported so callers in any ops can use it.
//
// Overridable via ctx for tests: uses ctx.Claude.IsAlive to poll, so test
// fakes can short-circuit the wait.
func SignalAndWaitExit(ctx *Context, pid int) {
	if pid <= 0 {
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGINT)
	}
	isAlive := func(pid int) bool { return false }
	if ctx != nil && ctx.Claude != nil {
		isAlive = ctx.Claude.IsAlive
	}
	for i := 0; i < 20; i++ {
		if !isAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(syscall.SIGTERM)
	}
	for i := 0; i < 10; i++ {
		if !isAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
