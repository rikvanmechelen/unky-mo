package ops

import (
	"fmt"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// LaunchParams controls LaunchSession behaviour.
type LaunchParams struct {
	WindowName string // tmux window name (e.g. "myproj" or "myproj@feat")
	Cwd        string // working directory for both the Claude pane and sidebar
	ShellCmd   string // command to exec in the claude pane; typically "claude" or "claude --resume <id>"
	// AttachSidebar adds a second pane running `mo sidebar`. Default true —
	// CLI and TUI both want it. Set false for environments where the sidebar
	// isn't useful (tests; future headless runs).
	AttachSidebar bool
	// SwitchFocus calls tmux select-window on the new window. Default true.
	SwitchFocus bool
}

// LaunchResult reports the outcome of a successful launch.
type LaunchResult struct {
	Target       string // "session:window"
	ClaudePaneID string // "%N" — useful for the pane-exited hook + tests
	InstanceID   string // mo-generated instance ID stored as @mo_instance_id window option
	SwitchedTo   bool   // true when SwitchFocus was requested and tmux switched
}

// LaunchSession creates a new tmux window, execs the Claude command in pane
// .0, wires up the sidebar pane (when requested), installs the
// pane-exited hook so closing Claude closes the window, and switches focus.
//
// Ported from tui.Model.launchClaudeInWindow (internal/tui/app.go). The
// CLI's `mo start` previously duplicated this ceremony inline — it now calls
// here too.
func LaunchSession(ctx *Context, p LaunchParams) (*LaunchResult, error) {
	if ctx == nil || ctx.Tmux == nil {
		return nil, fmt.Errorf("ops.LaunchSession: nil context or tmux")
	}
	if p.WindowName == "" {
		return nil, fmt.Errorf("ops.LaunchSession: WindowName is required")
	}
	if p.ShellCmd == "" {
		p.ShellCmd = "claude"
	}

	target, err := ctx.Tmux.CreateWindow(p.WindowName, p.Cwd)
	if err != nil {
		return nil, fmt.Errorf("create window: %w", err)
	}
	res := &LaunchResult{Target: target}

	// Generate and set the instance ID — a stable key for binding sidebar,
	// terminals, and state file rows to this window. Stored as a tmux window
	// user-option so it survives renames and is readable via ListWindows.
	instanceID := ttmux.GenerateInstanceID()
	if err := ctx.Tmux.SetWindowOption(target, "@mo_instance_id", instanceID); err == nil {
		res.InstanceID = instanceID
	}

	// Capture the claude pane ID before we split off the sidebar — once the
	// split happens, .0 is still the claude pane but grabbing the ID early
	// makes the pane-exited hook bulletproof.
	if id, err := ctx.Tmux.PaneID(target); err == nil {
		res.ClaudePaneID = id
	}

	// Use "exec" so the shell is replaced — when Claude exits, the pane
	// closes immediately instead of leaving a lingering shell prompt.
	if err := ctx.Tmux.SendKeys(target, "exec "+p.ShellCmd); err != nil {
		return res, fmt.Errorf("send launch command: %w", err)
	}

	// Auto-close the window only when the Claude pane itself exits (not
	// when the user closes a terminal drawer or sidebar pane).
	if res.ClaudePaneID != "" {
		hook := fmt.Sprintf(`if-shell -F "#{==:#{hook_pane},%s}" "kill-window"`, res.ClaudePaneID)
		ctx.Tmux.SetWindowHook(target, "pane-exited", hook)
	}

	if p.AttachSidebar && ctx.MoBinaryPath != "" {
		width := ctx.SidebarWidth
		if width <= 0 {
			width = 42
		}
		sidebarCmd := fmt.Sprintf("%s sidebar", ctx.MoBinaryPath)
		if res.InstanceID != "" {
			sidebarCmd += " --instance-id=" + res.InstanceID
		}
		if _, err := ctx.Tmux.SplitWindow(target, width, p.Cwd, sidebarCmd); err == nil {
			// Refocus to the main (left) pane so the claude pane is foregrounded.
			_ = ctx.Tmux.SelectPane(target + ".0")
		}
	}

	if p.SwitchFocus {
		if err := ctx.Tmux.SwitchToWindow(target); err != nil {
			return res, fmt.Errorf("switch to window: %w", err)
		}
		res.SwitchedTo = true
	}

	return res, nil
}
