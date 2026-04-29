package ops

import (
	"fmt"
	"sort"

	"github.com/rvanmech/unky-mo/internal/claude"
)

// ResumeParams controls ResumeInDir.
type ResumeParams struct {
	SessionID  string // optional — when set, used to locate an already-running window
	WindowName string // starting-point tmux window name; may be overridden if session is live elsewhere
	Cwd        string // working directory for launch fallback
	ShellCmd   string // resume command (e.g. "claude --resume <id>"); empty defaults to "claude --resume <SessionID>"
	AgentKey   string // coding agent mnemonic for the @mo_agent window option
}

// ResumeResult reports what happened.
type ResumeResult struct {
	Target    string // final window name (may differ from params if session lives elsewhere)
	Switched  bool   // true when we focused an existing window
	Relaunched bool  // true when we had to spawn a new `claude --resume` window
}

// ResumeInDir switches to the tmux window currently hosting a session, or
// spawns a fresh `claude --resume <id>` window if no live window is found.
// Ported from tui.Model.resumeInDir.
func ResumeInDir(ctx *Context, p ResumeParams) (*ResumeResult, error) {
	if ctx == nil || ctx.Tmux == nil {
		return nil, fmt.Errorf("ops.ResumeInDir: nil context or tmux")
	}
	windowName := p.WindowName
	// If the session is live somewhere (possibly renamed), prefer that window.
	if p.SessionID != "" {
		if real := SessionToWindowMap(ctx)[p.SessionID]; real != "" {
			windowName = real
		}
	}
	res := &ResumeResult{Target: windowName}
	if windowName != "" && ctx.Tmux.WindowExists(windowName) {
		if err := ctx.Tmux.SwitchToWindow(resolveTarget(ctx, windowName)); err != nil {
			return res, fmt.Errorf("switch to window: %w", err)
		}
		res.Switched = true
		return res, nil
	}
	// Fall back: spawn a new window running the resume command.
	shellCmd := p.ShellCmd
	if shellCmd == "" {
		shellCmd = fmt.Sprintf("claude --resume %s", p.SessionID)
	}
	launch, err := LaunchSession(ctx, LaunchParams{
		WindowName:    windowName,
		Cwd:           p.Cwd,
		ShellCmd:      shellCmd,
		AgentKey:      p.AgentKey,
		AttachSidebar: true,
		SwitchFocus:   true,
	})
	if err != nil {
		return res, err
	}
	res.Relaunched = true
	res.Target = launch.Target
	return res, nil
}

// SessionToWindowMap returns a claudeSessionID → tmux window-name mapping by
// walking every window's pane PIDs and matching via IsDescendantOf. Lifted
// out of tui.Model.sessionToWindowMap so ResumeInDir (and future ops) can
// reuse it.
func SessionToWindowMap(ctx *Context) map[string]string {
	out := map[string]string{}
	if ctx == nil || ctx.Tmux == nil {
		return out
	}
	windows, err := ctx.Tmux.ListWindows()
	if err != nil || len(windows) == 0 {
		return out
	}
	var sessions []claude.Session
	if ctx.Claude != nil {
		sessions, _ = ctx.Claude.LiveSessions()
	}
	if len(sessions) == 0 {
		return out
	}
	for _, w := range windows {
		panePIDs, err := ctx.Tmux.WindowPanePIDs(w.ID)
		if err != nil || len(panePIDs) == 0 {
			continue
		}
		for i := range sessions {
			if _, ok := out[sessions[i].SessionID]; ok {
				continue
			}
			if ctx.Claude.IsDescendantOf(sessions[i].PID, panePIDs) {
				out[sessions[i].SessionID] = w.Name
			}
		}
	}
	return out
}

// PrimaryWindowForTarget returns the tmux window currently hosting the
// *oldest* live session at cwd, along with that session. Falls back to the
// bare composed name when no live session exists or none match a window.
// Ported from tui.Model.primaryWindowForTarget.
func PrimaryWindowForTarget(ctx *Context, project, branch, cwd string) (string, *claude.Session) {
	if ctx == nil || ctx.Claude == nil {
		return "", nil
	}
	sessions := ctx.Claude.SessionsForPath(cwd)
	if len(sessions) == 0 {
		return "", nil
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].StartedAt < sessions[j].StartedAt
	})
	primary := sessions[0]
	byID := SessionToWindowMap(ctx)
	name, ok := byID[primary.SessionID]
	if !ok {
		name = composeBareName(project, branch)
	}
	return name, &primary
}

func composeBareName(project, branch string) string {
	if branch == "" {
		return project
	}
	return project + "@" + branch
}
