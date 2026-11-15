package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m Model) onTicketsTick(msg ticketsTickMsg) (tea.Model, tea.Cmd) {
	if len(m.ticketsProviders) == 0 {
		return m, nil
	}
	m.ticketsLoading = true
	return m, tea.Batch(ticketsTick(m.ticketsRefreshPeriod), m.fetchTickets())
}

func (m Model) onTicketsRefresh(msg ticketsRefreshMsg) (tea.Model, tea.Cmd) {
	m.ticketsGroups = msg.groups
	m.ticketsErrors = msg.results
	m.ticketsLoaded = true
	m.ticketsLoading = false
	m.ticketsLastFetch = time.Now()
	if m.ticketsCursor >= m.ticketsVisibleLen() {
		m.ticketsCursor = max(0, m.ticketsVisibleLen()-1)
	}
	return m, nil
}

func (m Model) onTicketDetail(msg ticketDetailMsg) (tea.Model, tea.Cmd) {
	m.detailTicketLoading = false
	if msg.err != nil {
		m.detailTicketErr = msg.err.Error()
		return m, nil
	}
	m.detailTicket = msg.detail
	return m, nil
}
