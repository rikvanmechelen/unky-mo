package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/notify"
)

func (m Model) onSessionTick(msg sessionTickMsg) (tea.Model, tea.Cmd) {
	m.pruneOrphanedTermSessions()
	return m, tea.Batch(sessionTick(), m.refreshSessions(), m.refreshGitStatuses())
}

func (m Model) onSuspendComplete(msg suspendCompleteMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("suspend failed: %v", msg.err))
		}
	}
	m.disableHomeRespawn()
	return m, tea.Quit
}

func (m Model) onSessionRefresh(msg sessionRefreshMsg) (tea.Model, tea.Cmd) {
	m.updateProjectStatuses(msg)
	// Rebuild the detail view so session rows reflect fresh live data
	// (needed e.g. after a lift, where a refresh kicked off by the handler
	// must land without the user leaving + re-entering the screen).
	if m.screen == ScreenProject && m.detailProject != nil {
		m.buildDetailRows()
	}
	return m, nil
}

func (m Model) onNotification(msg notificationMsg) (tea.Model, tea.Cmd) {
	if msg.Type == notify.NotifyRestart {
		// `mo restart` or the web dashboard's "Restart mo" — the same
		// as ctrl+alt+r, but immune to whatever input has the keyboard.
		return m.beginRestart()
	}
	m.routeNotificationToStatusMgr(notify.Notification(msg))
	cmds := []tea.Cmd{m.refreshSessions()}
	if m.notifServer != nil {
		cmds = append(cmds, m.waitForNotification())
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onStatusChange(msg statusChangeMsg) (tea.Model, tea.Cmd) {
	// Status manager detected a change — refresh immediately.
	cmds := []tea.Cmd{m.refreshSessions()}
	if m.statusSub != nil {
		cmds = append(cmds, m.waitForStatusChange())
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onSessionImported(msg sessionImportedMsg) (tea.Model, tea.Cmd) {
	m.statusMsg = msg.status
	m.list.NewStatusMessage(m.statusMsg)
	// Refresh immediately so the newly-imported session appears in the
	// dashboard / sidebar without waiting for the next 5s tick, and so the
	// state file the sidebar polls is up to date within a cycle.
	return m, tea.Batch(
		m.refreshSessions(),
		tea.Tick(4*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		}),
	)
}
