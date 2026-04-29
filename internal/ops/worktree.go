package ops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/project"
)

// WorktreeParams drives CreateWorktreeAndLaunch.
type WorktreeParams struct {
	ProjectName string
	ProjectPath string // main repo path
	Branch      string // branch name (created if missing)
	ShellCmd    string // command to exec; empty defaults to "claude" via LaunchSession
	AgentKey    string // coding agent mnemonic; stored as @mo_agent on the tmux window
}

// WorktreeResult reports the outcome.
type WorktreeResult struct {
	WorktreePath   string
	WindowName     string
	Launched       bool // true if a new Claude session was spawned (vs focused existing)
	Status         string
	ExistsConflict bool // true when the worktree already existed for this branch
}

// CreateWorktreeAndLaunch creates a git worktree for the branch (creating
// the branch if needed), then either focuses an existing session at that
// worktree OR launches a fresh Claude session in it.
//
// Ported from tui.Model.createWorktreeAndLaunch.
func CreateWorktreeAndLaunch(ctx *Context, p WorktreeParams) (*WorktreeResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.ProjectPath == "" || p.Branch == "" {
		return nil, fmt.Errorf("ops.CreateWorktreeAndLaunch: ProjectPath and Branch required")
	}

	wtPath, err := project.CreateWorktree(p.ProjectPath, p.Branch)
	if err != nil {
		var existsErr *project.ErrWorktreeExists
		if errors.As(err, &existsErr) {
			return &WorktreeResult{
				WorktreePath:   existsErr.WorktreePath,
				WindowName:     p.ProjectName + "@" + p.Branch,
				ExistsConflict: true,
				Status:         existsErr.Error(),
			}, nil
		}
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	windowName := p.ProjectName + "@" + p.Branch
	res := &WorktreeResult{WorktreePath: wtPath, WindowName: windowName}

	// If a live session already exists at this worktree, prefer focusing its
	// real window (may have been renamed).
	if realWin, sess := PrimaryWindowForTarget(ctx, p.ProjectName, p.Branch, wtPath); sess != nil && realWin != "" {
		if err := ctx.Tmux.SwitchToWindow(resolveTarget(ctx, realWin)); err != nil {
			res.Status = fmt.Sprintf("Worktree created but failed to switch: %v", err)
			return res, nil
		}
		res.Status = "Switched to " + realWin
		res.WindowName = realWin
		return res, nil
	}
	// Otherwise: focus bare window if it happens to exist, else launch fresh.
	if ctx.Tmux.WindowExists(windowName) {
		if err := ctx.Tmux.SwitchToWindow(resolveTarget(ctx, windowName)); err != nil {
			res.Status = fmt.Sprintf("Worktree created but failed to switch: %v", err)
			return res, nil
		}
		res.Status = "Switched to " + windowName
		return res, nil
	}
	if _, err := LaunchSession(ctx, LaunchParams{
		WindowName:    windowName,
		Cwd:           wtPath,
		ShellCmd:      p.ShellCmd,
		AgentKey:      p.AgentKey,
		AttachSidebar: true,
		SwitchFocus:   true,
	}); err != nil {
		res.Status = fmt.Sprintf("Launch failed: %v", err)
		return res, err
	}
	res.Launched = true
	res.Status = "Launched Claude in " + windowName
	return res, nil
}

// OpenBranchParams drives OpenBranchInMain.
type OpenBranchParams struct {
	ProjectName string
	ProjectPath string
	Branch      string
	Stash       bool // if true, stash dirty changes before checkout
}

// OpenBranchResult describes the outcome.
type OpenBranchResult struct {
	Launched bool
	Status   string
}

// OpenBranchInMain checks out the given branch in the main project repo,
// optionally stashing dirty state first, then focuses or launches a Claude
// session for that project. Refuses on a dirty repo without Stash=true.
//
// Ported from tui.Model.openBranchInMain (the callers previously passed a
// force-bool; here we accept an explicit Stash param).
func OpenBranchInMain(ctx *Context, p OpenBranchParams) (*OpenBranchResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.ProjectPath == "" || p.Branch == "" {
		return nil, fmt.Errorf("ops.OpenBranchInMain: ProjectPath and Branch required")
	}

	dirty, err := project.IsDirty(p.ProjectPath)
	if err != nil {
		return nil, fmt.Errorf("check dirty: %w", err)
	}
	if dirty {
		if !p.Stash {
			return &OpenBranchResult{Status: "Main checkout is dirty — commit, stash, or pass Stash=true"}, nil
		}
		if err := project.StashMain(p.ProjectPath, p.Branch); err != nil {
			return nil, fmt.Errorf("stash: %w", err)
		}
	}
	if err := project.CheckoutInMain(p.ProjectPath, p.Branch); err != nil {
		return nil, fmt.Errorf("checkout: %w", err)
	}

	res := &OpenBranchResult{}
	windowName := p.ProjectName
	// Focus existing primary if live, else the bare window, else launch.
	if realWin, sess := PrimaryWindowForTarget(ctx, p.ProjectName, "", p.ProjectPath); sess != nil && realWin != "" {
		if err := ctx.Tmux.SwitchToWindow(resolveTarget(ctx, realWin)); err == nil {
			res.Status = "Switched to " + realWin
			return res, nil
		}
	}
	if ctx.Tmux.WindowExists(windowName) {
		if err := ctx.Tmux.SwitchToWindow(resolveTarget(ctx, windowName)); err == nil {
			res.Status = "Switched to " + windowName
			return res, nil
		}
	}
	if _, err := LaunchSession(ctx, LaunchParams{
		WindowName:    windowName,
		Cwd:           p.ProjectPath,
		AttachSidebar: true,
		SwitchFocus:   true,
	}); err != nil {
		return res, fmt.Errorf("launch: %w", err)
	}
	res.Launched = true
	res.Status = "Checked out " + p.Branch + " and launched Claude"
	return res, nil
}

// CleanupParams drives CleanupWorktree + optional branch deletion.
type CleanupParams struct {
	ProjectPath      string
	Branch           string
	DeleteBranch     bool
	Sessions         []claude.Session // if non-nil, SIGINT + kill their windows first
}

// CleanupResult describes what happened.
type CleanupResult struct {
	KilledSessions    int
	WorktreeRemoved   bool
	BranchDeleted     bool
	Status            string
}

// CleanupWorktree optionally SIGINTs live sessions and kills their tmux
// windows, then removes the worktree and (if DeleteBranch) deletes the
// local branch. Ported from tui.Model.killCleanupSessions + runCleanup.
func CleanupWorktree(ctx *Context, p CleanupParams) (*CleanupResult, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if p.ProjectPath == "" || p.Branch == "" {
		return nil, fmt.Errorf("ops.CleanupWorktree: ProjectPath and Branch required")
	}
	res := &CleanupResult{}

	// Stage 1: kill live sessions (if the caller provided any).
	if len(p.Sessions) > 0 {
		windowIDBySession := sessionToWindowIDMapForSessions(ctx, p.Sessions)
		for _, s := range p.Sessions {
			SignalAndWaitExit(ctx, s.PID)
			if wID, ok := windowIDBySession[s.SessionID]; ok {
				_ = ctx.Tmux.KillWindow(ctx.Tmux.SessionName() + ":" + wID)
			}
			res.KilledSessions++
		}
	}

	// Stage 2: remove worktree (ignore "no worktree" — plain branch rows).
	var parts []string
	if err := project.RemoveWorktree(p.ProjectPath, p.Branch); err == nil {
		res.WorktreeRemoved = true
		parts = append(parts, "worktree removed")
	} else if !strings.Contains(err.Error(), "no worktree found") {
		return res, fmt.Errorf("remove worktree: %w", err)
	}

	// Stage 3: delete branch if requested.
	if p.DeleteBranch {
		if err := project.DeleteBranch(p.ProjectPath, p.Branch); err != nil {
			return res, fmt.Errorf("delete branch: %w", err)
		}
		res.BranchDeleted = true
		parts = append(parts, "branch deleted")
	}

	res.Status = "Cleanup: " + p.Branch
	if len(parts) > 0 {
		res.Status += " (" + strings.Join(parts, ", ") + ")"
	}
	return res, nil
}

// sessionToWindowIDMapForSessions walks tmux windows and attributes each of
// the given sessions to the window whose pane PIDs claim them via the PPID
// chain. Returns session ID → window ID (@N) so callers can build safe tmux
// targets even for dotted window names.
func sessionToWindowIDMapForSessions(ctx *Context, sessions []claude.Session) map[string]string {
	out := map[string]string{}
	if ctx == nil || ctx.Tmux == nil {
		return out
	}
	windows, err := ctx.Tmux.ListWindows()
	if err != nil {
		return out
	}
	for _, w := range windows {
		panePIDs, err := ctx.Tmux.WindowPanePIDs(w.ID)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if _, already := out[s.SessionID]; already {
				continue
			}
			if ctx.Claude != nil && ctx.Claude.IsDescendantOf(s.PID, panePIDs) {
				out[s.SessionID] = w.ID
			}
		}
	}
	return out
}

// requireContext validates that ctx + ctx.Tmux are non-nil.
func requireContext(ctx *Context) error {
	if ctx == nil {
		return fmt.Errorf("ops: nil context")
	}
	if ctx.Tmux == nil {
		return fmt.Errorf("ops: nil Tmux")
	}
	return nil
}
