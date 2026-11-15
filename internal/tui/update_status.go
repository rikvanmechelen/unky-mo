package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m Model) onStickyStatusMsgEvent(msg stickyStatusMsgEvent) (tea.Model, tea.Cmd) {
	// Rendered as its own block (dashboardView), not in the list's
	// half-width status line.
	m.statusMsg = string(msg)
	m.statusSticky = true
	m.list.NewStatusMessage("")
	return m, nil
}

func (m Model) onStatusMsgEvent(msg statusMsgEvent) (tea.Model, tea.Cmd) {
	m.statusMsg = string(msg)
	m.statusSticky = false
	m.list.NewStatusMessage(m.statusMsg)
	// Auto-clear success messages after 4s; errors stay until user presses a key
	if !isErrorStatus(m.statusMsg) {
		return m, tea.Tick(4*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		})
	}
	return m, nil
}

func (m Model) onClearStatus(msg clearStatusMsg) (tea.Model, tea.Cmd) {
	if m.statusSticky {
		return m, nil // an earlier message's timer; the sticky one waits for a key
	}
	m.statusMsg = ""
	return m, nil
}
