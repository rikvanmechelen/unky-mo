package sidebar

import tea "charm.land/bubbletea/v2"

// sidebarLayout describes where each rendered region lives on the rendered
// pane. Y values are 0-indexed rows; -1 means the region isn't rendered.
// Both View() and handleMouseClick walk the same layout, so a click lands on
// the row the user visually sees.
type sidebarLayout struct {
	// Items area spans rows [itemsY0, itemsY1). Row Y=itemsY0+k maps to
	// m.items[m.viewportStart+k].
	itemsY0 int
	itemsY1 int

	scrollUpY   int // "▲ more", -1 when absent
	scrollDownY int // "▼ more", -1 when absent

	shellsHdrY int // "Shells (N)" header, -1 when absent
	shellsY0   int // first shell row (shellsHdrY+1 when present)
	shellsY1   int // exclusive

	filesHdrY int // "Changed ..." header, -1 when absent
	// filesRows maps Y → changedFiles index. Directory rows and the
	// "+N more" overflow row are *not* keyed — clicks on them are no-ops.
	filesRows map[int]int
}

// computeLayout mirrors View()'s line-by-line render without building any
// strings, so tests and the click router can agree on row positions.
func (m Model) computeLayout() sidebarLayout {
	l := sidebarLayout{
		scrollUpY:   -1,
		scrollDownY: -1,
		shellsHdrY:  -1,
		shellsY0:    -1,
		shellsY1:    -1,
		filesHdrY:   -1,
		filesRows:   map[int]int{},
	}
	if m.width == 0 {
		return l
	}

	headerLines := 1
	footerLines := 5
	if m.usage != nil {
		footerLines += 3
	}
	maxVisible := m.height - headerLines - footerLines
	if maxVisible < 1 {
		maxVisible = 1
	}
	end := m.viewportStart + maxVisible
	if end > len(m.items) {
		end = len(m.items)
	}

	// Y=0 is the top "Sessions" header; items start at Y=1.
	l.itemsY0 = 1
	l.itemsY1 = 1 + (end - m.viewportStart)
	y := l.itemsY1

	if m.viewportStart > 0 {
		l.scrollUpY = y
		y++
	}
	if end < len(m.items) {
		l.scrollDownY = y
		y++
	}

	// View uses strings.Count(b, "\n") to measure written lines. Every path
	// that reached here wrote exactly `y` "\n"-terminated lines, so y ==
	// contentLines.
	contentLines := y
	remaining := m.height - contentLines - footerLines
	if m.statusMsg != "" {
		remaining--
	}

	if len(m.activeShells) > 0 && remaining > 3 {
		y++ // blank separator line
		l.shellsHdrY = y
		y++
		l.shellsY0 = y
		y += len(m.activeShells)
		l.shellsY1 = y
		contentLines = y
		remaining = m.height - contentLines - footerLines
		if m.statusMsg != "" {
			remaining--
		}
	}

	if len(m.changedFiles) > 0 && remaining > 3 {
		maxFileLines := remaining - 2
		if maxFileLines > 0 {
			y++ // blank separator
			l.filesHdrY = y
			y++
			treeLines := buildFileTreeLines(m.changedFiles)
			rendered := 0
			for _, tl := range treeLines {
				if rendered >= maxFileLines {
					if len(m.changedFiles)-rendered > 0 {
						// "+N more" overflow row — not mapped, clicks are no-ops.
						y++
					}
					break
				}
				if tl.fileIndex >= 0 {
					l.filesRows[y] = tl.fileIndex
				}
				y++
				rendered++
			}
		}
	}
	return l
}

// handleMouseClick routes a left-press at row y to the right section/row.
// Sections: items (sessions + terminals), scroll indicators, shells, files.
// Clicks on headers, directory rows, "+N more", and empty space are no-ops.
func (m Model) handleMouseClick(y int) (tea.Model, tea.Cmd) {
	l := m.computeLayout()

	// Items (sessions + terminals)
	if y >= l.itemsY0 && y < l.itemsY1 {
		idx := m.viewportStart + (y - l.itemsY0)
		if idx < 0 || idx >= len(m.items) || m.items[idx].IsHeader {
			return m, nil
		}
		m.focusSection = "sessions"
		m.cursor = idx
		m.ensureCursorVisible()
		return m, m.handleEnter()
	}

	// Scroll indicators: click to scroll by a page.
	if y == l.scrollUpY {
		pageSize := l.itemsY1 - l.itemsY0
		if pageSize < 1 {
			pageSize = 1
		}
		m.viewportStart -= pageSize
		if m.viewportStart < 0 {
			m.viewportStart = 0
		}
		return m, nil
	}
	if y == l.scrollDownY {
		pageSize := l.itemsY1 - l.itemsY0
		if pageSize < 1 {
			pageSize = 1
		}
		m.viewportStart += pageSize
		if m.viewportStart > len(m.items)-1 {
			m.viewportStart = len(m.items) - 1
			if m.viewportStart < 0 {
				m.viewportStart = 0
			}
		}
		return m, nil
	}

	// Shells rows: focus the row and fire its enter-equivalent (show output).
	if y >= l.shellsY0 && y < l.shellsY1 && l.shellsY0 >= 0 {
		idx := y - l.shellsY0
		if idx >= 0 && idx < len(m.activeShells) {
			m.focusSection = "shells"
			m.shellCursor = idx
			return m, m.showShellOutput(m.activeShells[idx])
		}
	}

	// Changed files: focus the row and fire diff popup.
	if fi, ok := l.filesRows[y]; ok && fi >= 0 && fi < len(m.changedFiles) {
		m.focusSection = "files"
		m.fileCursor = fi
		return m, m.showDiffPopup(m.changedFiles[fi])
	}

	return m, nil
}
