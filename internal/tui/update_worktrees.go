package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/project"
)

func (m Model) onWorktreeExists(msg worktreeExistsMsg) (tea.Model, tea.Cmd) {
	if m.detailProject != nil {
		m.worktreeExists = &worktreeExistsPrompt{
			branch:      msg.branch,
			projectPath: m.detailProject.Path,
			projectName: m.detailProject.Name,
			wtPath:      msg.worktreePath,
			prBranch:    msg.prBranch,
		}
	}
	return m, nil
}

func (m Model) onWorktreeCreated(msg worktreeCreatedMsg) (tea.Model, tea.Cmd) {
	if m.detailProject != nil {
		m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
		m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
		m.buildDetailRows()
	}
	status := msg.status
	return m, func() tea.Msg { return statusMsgEvent(status) }
}

func (m Model) onSessionLifted(msg sessionLiftedMsg) (tea.Model, tea.Cmd) {
	// Rebuild branches + worktrees + rows so the new session row lands on
	// the new branch, and kick off a session refresh so live-status data
	// (the old PID is now gone) is up to date.
	if m.detailProject != nil {
		m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
		m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
		m.buildDetailRows()
	}
	status := msg.status
	return m, tea.Batch(
		m.refreshSessions(),
		func() tea.Msg { return statusMsgEvent(status) },
	)
}

func (m Model) onBranchesChanged(msg branchesChangedMsg) (tea.Model, tea.Cmd) {
	if m.detailProject != nil {
		m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
		m.detailBranches, _ = project.ListBranches(m.detailProject.Path)
		m.buildDetailRows()
	}
	if msg.status != "" {
		return m, func() tea.Msg { return statusMsgEvent(msg.status) }
	}
	return m, nil
}
