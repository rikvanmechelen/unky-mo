package tui

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// onKey routes a key: the ticket picker, the open prompt, the shared keys,
// the screen's own keys, then the screen's navigation.
func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Clear sticky error messages on any keypress
	if m.statusMsg != "" && (m.statusSticky || isErrorStatus(m.statusMsg)) {
		m.statusMsg = ""
		m.statusSticky = false
	}

	// Don't intercept keys when filtering
	if m.list.FilterState() == list.Filtering {
		return m.updateNav(msg)
	}

	// Ticket-screen picker + remember-prompt capture all input while active
	// so normal key bindings don't fire.
	if m.screen == ScreenTicket && m.pickerRememberActive {
		newModel, cmd := m.handleTicketPickerRemember(msg)
		return newModel, cmd
	}
	if m.screen == ScreenTicket && m.pickerActive {
		newModel, cmd := m.handleTicketPickerActive(msg)
		return newModel, cmd
	}
	// `y` on ticket detail yanks the derived branch name.
	if m.screen == ScreenTicket && msg.String() == "y" {
		return m.handleTicketYank()
	}

	// The open prompt takes the key (see promptKeys for the order).
	if keys := m.promptKeys(); keys != nil {
		return keys(m, msg)
	}

	if m2, cmd, ok := m.updateKeyGlobal(msg); ok {
		return m2, cmd
	}
	if m2, cmd, ok := m.updateKeyScreen(msg); ok {
		return m2, cmd
	}
	// Any other key goes to the screen's navigation.
	return m.updateNav(msg)
}

// updateNav is what a message no handler took does on the current screen:
// the dashboard's lists and the project screen's cursors move.
func (m Model) updateNav(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.screen {
	case ScreenDashboard:
		// Right panel: sessions (top) + optional tickets (bottom). Up/down
		// moves within the focused section and crosses the boundary at the
		// ends (down past the last session → first ticket; up past the first
		// ticket → last session). Wraps within each section if tickets isn't
		// rendered.
		if !m.dashFocusLeft {
			if msg, ok := msg.(tea.KeyPressMsg); ok {
				sess := len(m.dashSessionItems)
				ticketsLen := 0
				if m.ticketsShouldRender() {
					ticketsLen = m.ticketsVisibleLen()
				}
				switch msg.String() {
				case "up", "k":
					if m.dashRightFocus == dashRightSessions {
						if sess > 0 {
							if m.dashSessionCursor == 0 && ticketsLen > 0 {
								m.dashRightFocus = dashRightTickets
								m.ticketsCursor = ticketsLen - 1
							} else {
								m.dashSessionCursor = (m.dashSessionCursor - 1 + sess) % sess
							}
						}
					} else {
						if ticketsLen > 0 {
							if m.ticketsCursor == 0 && sess > 0 {
								m.dashRightFocus = dashRightSessions
								m.dashSessionCursor = sess - 1
							} else {
								m.ticketsCursor = (m.ticketsCursor - 1 + ticketsLen) % ticketsLen
							}
						}
					}
				case "down", "j":
					if m.dashRightFocus == dashRightSessions {
						if sess > 0 {
							if m.dashSessionCursor == sess-1 && ticketsLen > 0 {
								m.dashRightFocus = dashRightTickets
								m.ticketsCursor = 0
							} else {
								m.dashSessionCursor = (m.dashSessionCursor + 1) % sess
							}
						} else if ticketsLen > 0 {
							m.dashRightFocus = dashRightTickets
							m.ticketsCursor = 0
						}
					} else {
						if ticketsLen > 0 {
							if m.ticketsCursor == ticketsLen-1 && sess > 0 {
								m.dashRightFocus = dashRightSessions
								m.dashSessionCursor = 0
							} else {
								m.ticketsCursor = (m.ticketsCursor + 1) % ticketsLen
							}
						}
					}
				}
			}
			return m, nil
		}
		// Left panel: delegate to bubbles list
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	case ScreenProject:
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			switch msg.String() {
			case "up", "k":
				if m.detailFocusLeft {
					n := m.detailCombinedLen()
					if n > 0 {
						m.detailCursor = (m.detailCursor - 1 + n) % n
						m.loadRecap()
					}
				} else {
					n := len(m.detailPRs)
					if n > 0 {
						m.detailPRCursor = (m.detailPRCursor - 1 + n) % n
					}
				}
			case "down", "j":
				if m.detailFocusLeft {
					n := m.detailCombinedLen()
					if n > 0 {
						m.detailCursor = (m.detailCursor + 1) % n
						m.loadRecap()
					}
				} else {
					n := len(m.detailPRs)
					if n > 0 {
						m.detailPRCursor = (m.detailPRCursor + 1) % n
					}
				}
			}
		}
	}

	return m, nil
}
