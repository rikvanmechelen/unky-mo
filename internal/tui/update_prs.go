package tui

import tea "charm.land/bubbletea/v2"

func (m Model) onPRFetch(msg prFetchMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.detailPRErr = msg.err.Error()
	} else {
		m.detailPRs = msg.prs
	}
	return m, nil
}

func (m Model) onPRDetail(msg prDetailMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.detailPRDetail = msg.detail
	}
	return m, nil
}

func (m Model) onSyncPull(msg syncPullMsg) (tea.Model, tea.Cmd) {
	m.syncedSessions = msg.synced
	m.remoteSynced = msg.remoteOnly
	if m.detailProject != nil {
		// Rebuild rows to reflect newly-pulled sessions AND remote-only
		// entries that the detail view surfaces as "pull + create worktree"
		// rows under their matching branch.
		m.buildDetailRows()
		m.loadRecap()
	}
	if msg.err != nil {
		return m, func() tea.Msg {
			return statusMsgEvent("sync failed: " + msg.err.Error())
		}
	}
	return m, nil
}
