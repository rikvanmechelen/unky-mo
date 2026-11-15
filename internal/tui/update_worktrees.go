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
	status := msg.status
	return m, tea.Batch(m.reloadDetail(), func() tea.Msg { return statusMsgEvent(status) })
}

func (m Model) onSessionLifted(msg sessionLiftedMsg) (tea.Model, tea.Cmd) {
	// Rebuild branches + worktrees + rows so the new session row lands on
	// the new branch, and kick off a session refresh so live-status data
	// (the old PID is now gone) is up to date.
	status := msg.status
	return m, tea.Batch(
		m.reloadDetail(),
		m.refreshSessions(),
		func() tea.Msg { return statusMsgEvent(status) },
	)
}

func (m Model) onBranchesChanged(msg branchesChangedMsg) (tea.Model, tea.Cmd) {
	if msg.status != "" {
		return m, tea.Batch(m.reloadDetail(), func() tea.Msg { return statusMsgEvent(msg.status) })
	}
	return m, m.reloadDetail()
}

// loadDetail lists path's worktrees and branches off the event loop; the
// detail screen's rows are rebuilt when they arrive (onDetailLoaded).
// recap also loads the selected session's last messages, as opening the
// screen does.
func (m Model) loadDetail(path string, recap bool) tea.Cmd {
	return func() tea.Msg {
		wts, _ := project.ListWorktrees(path)
		brs, _ := project.ListBranches(path)
		return detailLoadedMsg{path: path, worktrees: wts, branches: brs, recap: recap}
	}
}

// reloadDetail is loadDetail for the open detail screen, if any.
func (m Model) reloadDetail() tea.Cmd {
	if m.detailProject == nil {
		return nil
	}
	return m.loadDetail(m.detailProject.Path, false)
}

func (m Model) onDetailLoaded(msg detailLoadedMsg) (tea.Model, tea.Cmd) {
	if m.detailProject == nil || m.detailProject.Path != msg.path {
		return m, nil // the user has moved on
	}
	m.detailWorktrees, m.detailBranches = msg.worktrees, msg.branches
	m.detailLoading = false
	m.buildDetailRows()
	if m.detailCursor >= len(m.detailRows) {
		m.detailCursor = max(0, len(m.detailRows)-1)
	}
	if msg.recap {
		m.loadRecap()
	}
	return m, nil
}
