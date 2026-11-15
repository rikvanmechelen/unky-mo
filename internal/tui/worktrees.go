package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	moSync "github.com/rvanmech/unky-mo/internal/sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/config"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/ops"
	"github.com/rvanmech/unky-mo/internal/project"
)

func (m Model) createWorktreeFromPR(pr gh.PullRequest) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		// The worktree at what origin has for the PR's branch.
		wtPath, warning, err := ops.WorktreeFromRemote(m.ops, p.Path, pr.Branch)
		if err != nil {
			var existsErr *project.ErrWorktreeExists
			if errors.As(err, &existsErr) {
				return worktreeExistsMsg{
					branch:       pr.Branch,
					worktreePath: existsErr.WorktreePath,
					prBranch:     true,
				}
			}
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}

		windowName := p.Name + "@" + pr.Branch
		var status string
		// Prefer focusing a live (possibly renamed) session at this target.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, pr.Branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Worktree ready but failed to switch: %v", err)
			} else {
				status = "Switched to " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Worktree ready but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		if warning != "" {
			status = warning + ". " + status
		}
		return worktreeCreatedMsg{status: status}
	}
}

func (m Model) autoSyncPull(projectName, projectPath string) tea.Cmd {
	return func() tea.Msg {
		syncDir := moSync.DefaultSyncDir()
		result := syncPullMsg{
			synced:     make(map[string]moSync.SessionMeta),
			remoteOnly: make(map[string]moSync.SessionMeta),
		}

		// Stay quiet when the user hasn't set up sync on this machine.
		if !moSync.IsConfigured(syncDir) {
			return result
		}

		// List what's in the sync repo for this project
		sessions, err := moSync.List(syncDir)
		if err != nil {
			result.err = err
			return result
		}

		// Map each sync project name → local filesystem path. Main-project
		// entries map to projectPath; worktree entries ("<project>@<branch>")
		// resolve to a local worktree whose branch matches the suffix.
		pathFor := map[string]string{projectName: projectPath}
		worktrees, _ := project.ListWorktrees(projectPath)
		prefix := projectName + "@"
		for _, wt := range worktrees {
			if wt.Path != projectPath && wt.Branch != "" {
				pathFor[prefix+wt.Branch] = wt.Path
			}
		}

		for _, s := range sessions {
			// Only care about entries belonging to this project (main + worktrees).
			if s.ProjectName != projectName && !strings.HasPrefix(s.ProjectName, prefix) {
				continue
			}

			localPath, ok := pathFor[s.ProjectName]
			if !ok {
				// Worktree doesn't exist locally — remember it so the detail
				// view can surface a "pull + create worktree" action.
				branch := strings.TrimPrefix(s.ProjectName, prefix)
				result.remoteOnly[branch] = s
				continue
			}
			result.synced[s.SessionID] = s

			// Pull the session if we don't have it locally
			localDir := m.claude.ProjectsDirForPath(localPath)
			jsonlPath := filepath.Join(localDir, s.SessionID+".jsonl")
			if _, err := os.Stat(jsonlPath); os.IsNotExist(err) {
				if _, err := moSync.Pull(s.ProjectName, localPath, syncDir); err != nil {
					if result.err == nil {
						result.err = fmt.Errorf("%s: %w", s.ProjectName, err)
					}
					continue
				}
				result.pulled = true
			}
		}

		return result
	}
}

// focusExistingWorktree tries to switch to the existing worktree's session
// window. If no live session exists, launches a new one in the worktree dir.
func (m Model) focusExistingWorktree(projectName, branch, wtPath string) tea.Cmd {
	return func() tea.Msg {
		windowName := projectName + "@" + branch
		if attempted, realWin, err := m.focusPrimaryIfLive(projectName, branch, wtPath); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
	}
}

// removeAndRecreateWorktree removes the existing worktree and recreates it.
// For PR branches, re-fetches and resets to the remote branch.
func (m Model) removeAndRecreateWorktree(projectPath, projectName, branch string, prBranch bool) tea.Cmd {
	return func() tea.Msg {
		if err := project.RemoveWorktree(projectPath, branch); err != nil {
			return statusMsgEvent(fmt.Sprintf("Remove failed: %v", err))
		}
		var wtPath, warning string
		var err error
		if prBranch {
			wtPath, warning, err = ops.WorktreeFromRemote(m.ops, projectPath, branch)
		} else {
			wtPath, err = project.CreateWorktree(projectPath, branch)
		}
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Recreate failed: %v", err))
		}
		windowName := projectName + "@" + branch
		var status string
		if attempted, realWin, err := m.focusPrimaryIfLive(projectName, branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Recreated worktree but failed to switch: %v", err)
			} else {
				status = "Switched to " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Recreated worktree but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchAgentInWindow(windowName, wtPath, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		if warning != "" {
			status = warning + ". " + status
		}
		return worktreeCreatedMsg{status: status}
	}
}

// killCleanupSessions is a thin TUI adapter over ops.StopSessions. The
// callers always follow up with runCleanup for the actual worktree removal.
func (m Model) killCleanupSessions(sessions []claude.Session) tea.Cmd {
	return func() tea.Msg {
		n := ops.StopSessions(m.ops, sessions)
		return statusMsgEvent(fmt.Sprintf("Killed %d session(s)", n))
	}
}

// runCleanup is a thin TUI adapter over ops.CleanupWorktree.
func (m Model) runCleanup(projectPath, branch string, alsoDeleteBranch bool) tea.Cmd {
	return func() tea.Msg {
		res, err := ops.CleanupWorktree(m.ops, ops.CleanupParams{
			ProjectPath:  projectPath,
			Branch:       branch,
			DeleteBranch: alsoDeleteBranch,
		})
		if err != nil {
			return branchesChangedMsg{status: err.Error()}
		}
		return branchesChangedMsg{status: res.Status}
	}
}

// launchWorktreeSession opens a Claude session in the given worktree's directory.
// Uses `<project>@<branch>` as the tmux window name. If the window already
// exists, switches to it rather than launching a duplicate.
func (m Model) launchWorktreeSession(wt project.Worktree) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		branch := wt.Branch
		if branch == "" {
			if len(wt.HEAD) >= 8 {
				branch = wt.HEAD[:8]
			} else {
				branch = "detached"
			}
		}
		// Prefer focusing a live (possibly renamed) primary session at this
		// target. Only fall back to the composed bare name for leftover
		// dead-window edge cases.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wt.Path); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		windowName := p.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wt.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
	}
}

// pullRemoteSessionAndLaunch handles a "br-remote" row: create the worktree
// for the branch (if needed), decrypt the synced JSONL into that worktree's
// Claude projects dir, and launch a resumed Claude session there.
func (m Model) pullRemoteSessionAndLaunch(branch string, meta moSync.SessionMeta) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		wtPath, err := project.CreateWorktree(p.Path, branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		syncDir := moSync.DefaultSyncDir()
		if _, err := moSync.Pull(meta.ProjectName, wtPath, syncDir); err != nil {
			return statusMsgEvent(fmt.Sprintf("Pull failed: %v", err))
		}
		windowName := p.Name + "@" + branch
		var status string
		// Prefer focusing a live (possibly renamed) session at this target.
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wtPath); attempted {
			if err != nil {
				status = fmt.Sprintf("Pulled but failed to switch: %v", err)
			} else {
				status = "Resumed " + realWin
			}
		} else if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				status = fmt.Sprintf("Pulled but failed to switch: %v", err)
			} else {
				status = "Resumed " + windowName
			}
		} else {
			launch := m.launchResumeInWindow(windowName, wtPath, meta.SessionID)
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
	}
}

// createWorktreeAndLaunch is a thin TUI adapter over ops.CreateWorktreeAndLaunch.
func (m Model) createWorktreeAndLaunch(branch string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.CreateWorktreeAndLaunch(m.ops, ops.WorktreeParams{
			ProjectName: p.Name,
			ProjectPath: p.Path,
			Branch:      branch,
		})
		if err != nil {
			return worktreeCreatedMsg{status: err.Error()}
		}
		if res.ExistsConflict {
			return worktreeExistsMsg{
				branch:       branch,
				worktreePath: res.WorktreePath,
				prBranch:     false,
			}
		}
		return worktreeCreatedMsg{status: res.Status}
	}
}

// liftSessionToWorktree is a thin TUI adapter over ops.LiftSessionToWorktree.
// Triggered by `w` on a br-session row once the user has picked a new branch
// name and decided what to do with any dirty state at the source.
func (m Model) liftSessionToWorktree(sessionID, sourcePath, sourceWindow string, sourcePID int, newBranch string, stashAndPop bool) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.LiftSessionToWorktree(m.ops, ops.LiftParams{
			ProjectName:  p.Name,
			SourcePath:   sourcePath,
			SessionID:    sessionID,
			SourcePID:    sourcePID,
			SourceWindow: sourceWindow,
			NewBranch:    newBranch,
			StashAndPop:  stashAndPop,
		})
		if err != nil {
			return sessionLiftedMsg{status: fmt.Sprintf("Lift failed: %v", err)}
		}
		return sessionLiftedMsg{status: res.Status}
	}
}

// startLiftSessionPrompt opens the new-branch text input for the lift flow
// and snapshots the source session (PID, window) so later cursor moves can't
// invalidate the target.
func (m Model) startLiftSessionPrompt(row *detailRow) (tea.Model, tea.Cmd) {
	if row == nil || row.session == nil {
		return m, nil
	}
	pid := 0
	window := ""
	if row.session.IsLive {
		// Recover the live PID by matching SessionID against LiveSessions().
		// RecentSession itself only carries IsLive/SessionID; the PID + pane
		// window live on the live-session record.
		if live, err := m.claude.LiveSessions(); err == nil {
			for i := range live {
				if live[i].SessionID == row.session.SessionID {
					pid = live[i].PID
					break
				}
			}
		}
		window = row.tmuxWindow
	}
	ti := textinput.New()
	ti.Placeholder = "new branch name"
	ti.Focus()
	ti.CharLimit = 128
	ti.SetWidth(40)
	m.lift = &liftPrompt{
		input:        ti,
		sessionID:    row.session.SessionID,
		sourcePath:   row.path,
		sourcePID:    pid,
		sourceWindow: window,
	}
	return m, textinput.Blink
}

// decideLiftDirty runs after the user submits a branch name. If the source
// is dirty, we open the [s]/[l]/[n] menu; otherwise we run the lift directly.
func (m Model) decideLiftDirty(newBranch string) (tea.Model, tea.Cmd) {
	p := *m.lift
	p.branch = newBranch
	m.lift = &p
	dirty, err := project.IsDirty(p.sourcePath)
	if err != nil {
		// Surface the error and bail — don't press on with an uncertain state.
		cmd := func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("Lift failed: %v", err))
		}
		m.lift = nil
		return m, cmd
	}
	if dirty {
		p.dirty = true
		m.lift = &p
		return m, nil
	}
	cmd := m.runLiftSession(false)
	m.lift = nil
	return m, cmd
}

// runLiftSession hands control to the ops adapter with the captured source
// info and the user's stash-or-leave decision.
func (m Model) runLiftSession(stashAndPop bool) tea.Cmd {
	l := m.lift
	return m.liftSessionToWorktree(l.sessionID, l.sourcePath, l.sourceWindow, l.sourcePID, l.branch, stashAndPop)
}

// resumeBranchSmart picks the most natural action for the given branch:
// switch to the worktree window if one exists, launch in main if the branch
// is the main checkout, otherwise create a worktree and launch.
func (m Model) resumeBranchSmart(b project.Branch) tea.Cmd {
	p := m.detailProject
	if p == nil {
		return func() tea.Msg { return statusMsgEvent("No project selected") }
	}
	switch {
	case b.WorktreePath != "":
		wt := project.Worktree{Path: b.WorktreePath, Branch: b.Name}
		return m.launchWorktreeSession(wt)
	case b.IsMain:
		return func() tea.Msg {
			return m.launchAgentInWindow(p.Name, p.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key)
		}
	default:
		return m.createWorktreeAndLaunch(b.Name)
	}
}

// launchWorktreeSessionWithAgent mirrors launchWorktreeSession but uses the
// given agent command instead of "claude".
func (m Model) launchWorktreeSessionWithAgent(wt project.Worktree, agent config.AgentConfig) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		branch := wt.Branch
		if branch == "" {
			if len(wt.HEAD) >= 8 {
				branch = wt.HEAD[:8]
			} else {
				branch = "detached"
			}
		}
		if attempted, realWin, err := m.focusPrimaryIfLive(p.Name, branch, wt.Path); attempted {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + realWin)
		}
		windowName := p.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed {
			if err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchAgentInWindow(windowName, wt.Path, agent.Cmd, agent.Name, agent.Key)
	}
}

// createWorktreeAndLaunchWithAgent mirrors createWorktreeAndLaunch but passes
// the agent command through to the ops layer.
func (m Model) createWorktreeAndLaunchWithAgent(branch string, agent config.AgentConfig) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		res, err := ops.CreateWorktreeAndLaunch(m.ops, ops.WorktreeParams{
			ProjectName: p.Name,
			ProjectPath: p.Path,
			Branch:      branch,
			ShellCmd:    agent.Cmd,
			AgentKey:    agent.Key,
		})
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		if res.ExistsConflict {
			return worktreeExistsMsg{
				branch:       branch,
				worktreePath: res.WorktreePath,
			}
		}
		return worktreeCreatedMsg{status: res.Status}
	}
}

// openBranchInMain checks out the given branch in the main project repo and
// launches a Claude session there. Refuses when the main path has a live
// Claude session or when the working tree is dirty (unless force, which
// stashes first).
func (m Model) openBranchInMain(branch string, force bool) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		if m.claude.SessionForPath(p.Path) != nil {
			return statusMsgEvent("Main has an active Claude session; close it first")
		}
		dirty, err := project.IsDirty(p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("git status failed: %v", err))
		}
		if dirty && !force {
			return statusMsgEvent("Main checkout is dirty; press M to stash and switch, or commit/stash manually")
		}
		if dirty && force {
			if err := project.StashMain(p.Path, branch); err != nil {
				return statusMsgEvent(fmt.Sprintf("stash failed: %v", err))
			}
		}
		if err := project.CheckoutInMain(p.Path, branch); err != nil {
			return statusMsgEvent(fmt.Sprintf("checkout failed: %v", err))
		}
		status := "Switched main to " + branch
		if existed, err := m.focusIfExists(p.Name); existed {
			if err != nil {
				status = fmt.Sprintf("checked out %s but failed to switch window: %v", branch, err)
			}
		} else {
			if se, ok := m.launchAgentInWindow(p.Name, p.Path, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key).(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return branchesChangedMsg{status: status}
	}
}
