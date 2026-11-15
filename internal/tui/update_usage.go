package tui

import tea "charm.land/bubbletea/v2"

func (m Model) onUsageTick(msg usageTickMsg) (tea.Model, tea.Cmd) {
	return m, tea.Batch(usageTick(), m.fetchUsage())
}

func (m Model) onUsageRefresh(msg usageRefreshMsg) (tea.Model, tea.Cmd) {
	if msg.authErr {
		m.usageAuth = true
	} else {
		m.usage = msg.snap
		m.usageAuth = false
	}
	m.usageReady = true
	m.writeStateFile()
	return m, nil
}
