package ops

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// ImportParams drives ImportExternalSession.
type ImportParams struct {
	PID        int    // orphan claude PID to SIGTERM
	SessionID  string // session ID to resume after the orphan exits
	Cwd        string // working directory for the new window
	WindowName string // tmux window name for the new window (typically project's display name)
	ShellCmd   string // resume command; empty defaults to "claude --resume <SessionID>"
	AgentKey   string // coding agent mnemonic for the @mo_agent window option
}

// ImportResult describes the outcome.
type ImportResult struct {
	Target       string
	ClaudePaneID string
}

// ImportExternalSession terminates an orphan Claude running outside mo's
// tmux session (e.g. one started from a VS Code terminal) and resumes its
// conversation in a fresh tmux window. The SIGTERM gives Claude a moment
// to flush its JSONL before the new process attaches to the same session ID.
//
// Ported from tui.Model.importExternalSession.
func ImportExternalSession(ctx *Context, p ImportParams) (*ImportResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("ops.ImportExternalSession: SessionID required")
	}
	if p.PID > 0 {
		if proc, err := os.FindProcess(p.PID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
		// Wait up to ~2s for the orphan to exit cleanly.
		isAlive := func(pid int) bool { return false }
		if ctx.Claude != nil {
			isAlive = ctx.Claude.IsAlive
		}
		for i := 0; i < 20; i++ {
			if !isAlive(p.PID) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	shellCmd := p.ShellCmd
	if shellCmd == "" {
		shellCmd = fmt.Sprintf("claude --resume %s", p.SessionID)
	}
	launch, err := LaunchSession(ctx, LaunchParams{
		WindowName:    p.WindowName,
		Cwd:           p.Cwd,
		ShellCmd:      shellCmd,
		AgentKey:      p.AgentKey,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		return nil, err
	}
	// Give the newly-spawned claude a moment to write its PID file so the
	// subsequent session-refresh actually sees it. Non-blocking on failure.
	if ctx.Claude != nil {
		for i := 0; i < 30; i++ {
			if ctx.Claude.SessionForPath(p.Cwd) != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return &ImportResult{Target: launch.Target, ClaudePaneID: launch.ClaudePaneID}, nil
}
