package tui

import (
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

// mouseEnterMsg is an internal message sent after a mouse click selects an
// item. It causes the Update loop to run the same enter-key logic as if the
// user had pressed enter, without duplicating that code in the mouse handler.
type mouseEnterMsg struct{}

func fireMouseEnter() tea.Msg { return mouseEnterMsg{} }

// dashboardLayout holds Y-coordinate boundaries for the dashboard's clickable
// regions, mirroring dashboardView()'s rendering logic. Computed fresh on each
// click so it always agrees with what's on screen.
type dashboardLayout struct {
	leftWidth  int // columns occupied by the left panel (project list)
	rightX0    int // first X column of the right panel

	// Left panel (project list): items start at listItemsY0 and occupy one
	// row each (Height=1, Spacing=0). The list header (title bar + status
	// bar with their bottom padding) occupies listHeaderLines rows above.
	listHeaderLines int
	listPageStart   int // first visible item index on the current page
	listPageEnd     int // one past the last visible item index

	// Right panel (sessions + tickets). Session rows are interleaved with
	// non-clickable section labels ("Projects", "External") that we skip.
	// sessionRowY maps absolute Y → dashSessionItems index. ticketRowY maps
	// absolute Y → flat ticket-cursor index. Both exclude header/label rows.
	sessionRowY map[int]int
	ticketRowY  map[int]int
}

// computeDashboardLayout recomputes the dashboard's coordinate map without
// building any styled strings, matching dashboardView()'s structure line by
// line so click targets stay in sync with the rendered output.
func (m Model) computeDashboardLayout() dashboardLayout {
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 120
	}
	dividerWidth := 3
	rightWidth := (totalWidth - dividerWidth) / 2
	leftWidth := totalWidth - rightWidth - dividerWidth
	if leftWidth < 40 {
		leftWidth = 40
		rightWidth = totalWidth - leftWidth - dividerWidth
		if rightWidth < 20 {
			rightWidth = 20
		}
	}

	// List header: TitleBar (1 line + 1 bottom padding) + StatusBar (1 line
	// + 1 bottom padding) = 4 lines before items.
	const listHeaderLines = 4

	visibleItems := m.list.VisibleItems()
	pageStart, pageEnd := m.list.Paginator.GetSliceBounds(len(visibleItems))

	l := dashboardLayout{
		leftWidth:       leftWidth,
		rightX0:         leftWidth + dividerWidth,
		listHeaderLines: listHeaderLines,
		listPageStart:   pageStart,
		listPageEnd:     pageEnd,
		sessionRowY:     map[int]int{},
		ticketRowY:      map[int]int{},
	}

	// Right panel Y offsets. dashboardView() writes:
	//   "\n\n\n"                          → 3 blank lines (Y 0–2 within the panel body)
	//   headerStyle "Active Sessions" \n  → Y 3
	//   then session items with optional section labels
	y := 4 // first potential session-item row

	if len(m.dashSessionItems) == 0 {
		y++ // "No active sessions" placeholder line
	} else {
		prevSection := ""
		for i, item := range m.dashSessionItems {
			section := item.Section
			if section == "" {
				section = "projects"
			}
			if section != prevSection {
				if prevSection != "" {
					y++ // blank line between sections
				}
				y++ // section label ("Projects" / "External")
				prevSection = section
			}
			l.sessionRowY[y] = i
			y++
		}
	}

	// Tickets panel (rendered by renderTicketsPanel). Only present when
	// ticketsShouldRender() is true.
	if m.ticketsShouldRender() {
		y++ // blank separator "\n"
		y++ // "Tickets" header

		if len(m.ticketsProviders) == 0 {
			// Onboarding state — no clickable rows.
		} else if !m.ticketsLoaded {
			// "Loading…" — not clickable.
		} else if len(m.ticketsGroups) == 0 {
			// "No assigned tickets" — not clickable.
		} else {
			// Error rows (one per errored provider).
			for _, r := range m.ticketsErrors {
				if r.Err != nil {
					y++
				}
			}

			rowIdx := 0
			for _, g := range m.ticketsGroups {
				y++ // bucket header
				visible := m.visibleCountFor(g)
				for i := 0; i < visible; i++ {
					l.ticketRowY[y] = rowIdx
					y++
					rowIdx++
				}
				if m.hasOverflowRow(g) {
					l.ticketRowY[y] = rowIdx
					y++
					rowIdx++
				}
			}
		}
	}

	return l
}

// projectLayout holds Y-coordinate boundaries for the project detail view.
type projectLayout struct {
	leftWidth int
	rightX0   int
	headerY   int // number of full-width header rows before the panels

	// Left panel: detailRowY maps absolute Y → detailRows index.
	detailRowY map[int]int
	// Right panel: prRowY maps absolute Y → detailPRs index.
	prRowY map[int]int
}

// computeProjectLayout mirrors projectDetailView()'s structure.
func (m Model) computeProjectLayout() projectLayout {
	dividerWidth := 3
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 80
	}
	rightWidth := totalWidth * 2 / 5
	if rightWidth < 25 {
		rightWidth = 25
	}
	leftWidth := totalWidth - rightWidth - dividerWidth

	// Header: title line + blank = 2 rows.
	const headerY = 2

	l := projectLayout{
		leftWidth:  leftWidth,
		rightX0:    leftWidth + dividerWidth,
		headerY:    headerY,
		detailRowY: map[int]int{},
		prRowY:     map[int]int{},
	}

	// Left panel rows (Y is relative to panel body, which starts at headerY).
	// The panel renders detailRows with occasional headers and blank lines.
	py := 0 // panel-local Y
	lastKind := ""
	for i, row := range m.detailRows {
		switch row.kind {
		case "branch":
			if lastKind == "" {
				py++ // "Branches" header
			} else if lastKind != "branch" {
				py++ // blank separator
			}
			l.detailRowY[headerY+py] = i
			py++
		case "br-session", "br-empty", "br-remote":
			l.detailRowY[headerY+py] = i
			py++
		}
		lastKind = row.kind
	}
	if len(m.detailRows) == 0 {
		py += 2 // "Branches" header + "No branches found"
	}

	// Right panel rows. First row is "Pull Requests" header, then PR items.
	py = 0
	py++ // "Pull Requests" header
	if m.detailPRErr != "" {
		py++ // error line
	} else if m.detailPRs == nil {
		py++ // "Loading..."
	} else if len(m.detailPRs) == 0 {
		py++ // "No open pull requests"
	} else {
		for i := range m.detailPRs {
			l.prRowY[headerY+py] = i
			py++
			// If this PR is expanded, account for the detail block.
			if m.detailPRExpanded == i && m.detailPRDetail != nil {
				// Estimate expanded PR detail lines. The exact count depends
				// on content but we don't need precision — unexpanded PRs are
				// the primary click targets. Count newlines in a lightweight
				// upper bound.
				py += m.estimatePRDetailLines(rightWidth)
			}
		}
	}

	return l
}

// estimatePRDetailLines returns a rough line count for the expanded PR detail
// block. This is an approximation — the exact count depends on styled content
// width — but it's good enough for click routing because clicks on the detail
// block itself are no-ops.
func (m Model) estimatePRDetailLines(width int) int {
	if m.detailPRDetail == nil {
		return 0
	}
	// Each field (author, branch, status, reviews, +/-, body preview) is
	// roughly one line. Body wrapping can add more but we don't need to be
	// exact — if the user clicks into the detail block it simply won't match
	// any prRowY entry and will be a no-op.
	lines := 6
	if m.detailPRDetail.Body != "" {
		bodyLines := len(m.detailPRDetail.Body) / max(width-4, 20)
		if bodyLines > 5 {
			bodyLines = 5
		}
		lines += bodyLines + 1
	}
	return lines
}

// isModalActive returns true when any modal, prompt, or input overlay is
// active in the main TUI. Mouse clicks should be suppressed during these.
func (m Model) isModalActive() bool {
	return m.pendingCleanupActive ||
		m.pendingNewMenuActive ||
		m.pendingImportSessionID != "" ||
		m.pendingWTExistsActive ||
		m.pendingLiftDirtyActive ||
		m.worktreeInput != nil ||
		m.liftSessionInput != nil ||
		(m.screen == ScreenTicket && (m.pickerActive || m.pickerRememberActive))
}

// handleMouseClick routes a left-click at (x, y) to the appropriate panel and
// item. Clicks during modals, on headers, dividers, or empty space are no-ops.
func (m Model) handleMouseClick(x, y int) (tea.Model, tea.Cmd) {
	if m.isModalActive() {
		return m, nil
	}

	switch m.screen {
	case ScreenDashboard:
		return m.handleDashboardClick(x, y)
	case ScreenProject:
		return m.handleProjectClick(x, y)
	}
	return m, nil
}

// handleDashboardClick routes a click on the dashboard screen.
func (m Model) handleDashboardClick(x, y int) (tea.Model, tea.Cmd) {
	if m.list.FilterState() == list.Filtering {
		return m, nil
	}

	l := m.computeDashboardLayout()

	if x < l.leftWidth {
		// Left panel: project list.
		itemY := y - l.listHeaderLines
		if itemY < 0 {
			return m, nil
		}
		idx := l.listPageStart + itemY
		if idx < l.listPageStart || idx >= l.listPageEnd {
			return m, nil
		}
		m.list.Select(idx)
		m.dashFocusLeft = true
		return m, fireMouseEnter
	}

	if x >= l.rightX0 {
		// Right panel: sessions or tickets.
		if si, ok := l.sessionRowY[y]; ok && si >= 0 && si < len(m.dashSessionItems) {
			m.dashFocusLeft = false
			m.dashRightFocus = dashRightSessions
			m.dashSessionCursor = si
			return m, fireMouseEnter
		}
		if ti, ok := l.ticketRowY[y]; ok {
			m.dashFocusLeft = false
			m.dashRightFocus = dashRightTickets
			m.ticketsCursor = ti
			return m, fireMouseEnter
		}
	}

	return m, nil
}

// handleProjectClick routes a click on the project detail screen.
func (m Model) handleProjectClick(x, y int) (tea.Model, tea.Cmd) {
	l := m.computeProjectLayout()

	if y < l.headerY {
		return m, nil
	}

	if x < l.leftWidth {
		// Left panel: branches + sessions.
		if ri, ok := l.detailRowY[y]; ok && ri >= 0 && ri < len(m.detailRows) {
			m.detailFocusLeft = true
			m.detailCursor = ri
			m.loadRecap()
			return m, fireMouseEnter
		}
		return m, nil
	}

	if x >= l.rightX0 {
		// Right panel: pull requests.
		if pi, ok := l.prRowY[y]; ok && pi >= 0 && pi < len(m.detailPRs) {
			m.detailFocusLeft = false
			m.detailPRCursor = pi
			return m, fireMouseEnter
		}
		return m, nil
	}

	return m, nil
}

// handleMouseWheel scrolls the focused panel's cursor. The X coordinate
// determines which panel receives the scroll; the button direction determines
// up vs down. Each wheel tick moves the cursor by wheelScrollLines rows.
func (m Model) handleMouseWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	if m.isModalActive() {
		return m, nil
	}

	const wheelScrollLines = 3

	up := msg.Button == tea.MouseWheelUp

	switch m.screen {
	case ScreenDashboard:
		return m.handleDashboardWheel(msg.X, up, wheelScrollLines)
	case ScreenProject:
		return m.handleProjectWheel(msg.X, up, wheelScrollLines)
	}
	return m, nil
}

// handleDashboardWheel scrolls the dashboard panel under the mouse.
func (m Model) handleDashboardWheel(x int, up bool, n int) (tea.Model, tea.Cmd) {
	l := m.computeDashboardLayout()

	if x < l.leftWidth {
		// Left panel: scroll the project list.
		for i := 0; i < n; i++ {
			if up {
				m.list.CursorUp()
			} else {
				m.list.CursorDown()
			}
		}
		m.dashFocusLeft = true
		return m, nil
	}

	if x >= l.rightX0 {
		// Right panel: scroll sessions or tickets depending on current focus.
		sess := len(m.dashSessionItems)
		ticketsLen := 0
		if m.ticketsShouldRender() {
			ticketsLen = m.ticketsVisibleLen()
		}

		for i := 0; i < n; i++ {
			if up {
				m.scrollDashRightUp(sess, ticketsLen)
			} else {
				m.scrollDashRightDown(sess, ticketsLen)
			}
		}
		m.dashFocusLeft = false
		return m, nil
	}

	return m, nil
}

// scrollDashRightUp moves the dashboard right-panel cursor up by one,
// crossing the sessions/tickets boundary. Mirrors the keyboard "up" handler.
func (m *Model) scrollDashRightUp(sess, ticketsLen int) {
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
}

// scrollDashRightDown moves the dashboard right-panel cursor down by one,
// crossing the sessions/tickets boundary. Mirrors the keyboard "down" handler.
func (m *Model) scrollDashRightDown(sess, ticketsLen int) {
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

// handleProjectWheel scrolls the project detail panel under the mouse.
func (m Model) handleProjectWheel(x int, up bool, n int) (tea.Model, tea.Cmd) {
	l := m.computeProjectLayout()

	if x < l.leftWidth {
		// Left panel: branches.
		total := m.detailCombinedLen()
		if total > 0 {
			for i := 0; i < n; i++ {
				if up {
					m.detailCursor = (m.detailCursor - 1 + total) % total
				} else {
					m.detailCursor = (m.detailCursor + 1) % total
				}
			}
			m.loadRecap()
		}
		m.detailFocusLeft = true
		return m, nil
	}

	if x >= l.rightX0 {
		// Right panel: PRs.
		total := len(m.detailPRs)
		if total > 0 {
			for i := 0; i < n; i++ {
				if up {
					m.detailPRCursor = (m.detailPRCursor - 1 + total) % total
				} else {
					m.detailPRCursor = (m.detailPRCursor + 1) % total
				}
			}
		}
		m.detailFocusLeft = false
		return m, nil
	}

	return m, nil
}
