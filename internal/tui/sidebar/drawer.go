package sidebar

import (
	"fmt"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/ops"
)

// toggleDrawer opens or closes the terminal drawer.
func (m *Model) toggleDrawer() tea.Cmd {
	if m.drawerOpen {
		return m.closeDrawer()
	}
	return m.openDrawer()
}

// openDrawer shows the terminal drawer. Creates a terminal if none exist.
func (m *Model) openDrawer() tea.Cmd {
	if m.windowPath == "" {
		return statusCmd("no project path")
	}

	if len(m.terminals) == 0 {
		return m.createTerminalPane()
	}

	// Restore the last active terminal (or the first one)
	idx := m.activeTermIdx
	if idx < 0 || idx >= len(m.terminals) {
		idx = 0
	}

	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName(), m.windowName)
	if err := m.tmux.JoinPaneVertical(m.terminals[idx].PaneID, target); err != nil {
		return statusCmd(fmt.Sprintf("err: %v", err))
	}

	m.tmux.SelectPane(m.terminals[idx].PaneID)
	m.activeTermIdx = idx
	m.drawerOpen = true
	return statusCmd("drawer opened")
}

// closeDrawer hides the active terminal pane.
func (m *Model) closeDrawer() tea.Cmd {
	if !m.drawerOpen || m.activeTermIdx < 0 || m.activeTermIdx >= len(m.terminals) {
		return nil
	}

	activePaneID := m.terminals[m.activeTermIdx].PaneID
	if err := m.hidePane(activePaneID); err != nil {
		return statusCmd(fmt.Sprintf("err: %v", err))
	}

	m.drawerOpen = false
	return statusCmd("drawer closed")
}

// newTerminal creates a new terminal tab and switches to it.
func (m *Model) newTerminal() tea.Cmd {
	if m.windowPath == "" {
		return statusCmd("no project path")
	}

	// Hide current terminal if drawer is open
	if m.drawerOpen && m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		activePaneID := m.terminals[m.activeTermIdx].PaneID
		if err := m.hidePane(activePaneID); err != nil {
			return statusCmd(fmt.Sprintf("err: %v", err))
		}
	}

	return m.createTerminalPane()
}

// createTerminalPane splits a new terminal below the Claude pane.
func (m *Model) createTerminalPane() tea.Cmd {
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName(), m.windowName)
	paneID, err := m.tmux.SplitWindowHorizontal(target, m.windowPath)
	if err != nil {
		return statusCmd(fmt.Sprintf("err: %v", err))
	}

	m.tmux.SelectPane(paneID)
	m.termCounter++
	tab := TerminalTab{
		PaneID: paneID,
		Name:   fmt.Sprintf("term-%d", m.termCounter),
	}
	m.terminals = append(m.terminals, tab)
	m.activeTermIdx = len(m.terminals) - 1
	m.drawerOpen = true
	return statusCmd(fmt.Sprintf("%s opened", tab.Name))
}

// switchTerminalByPaneID switches to a terminal tab by its pane ID.
func (m *Model) switchTerminalByPaneID(paneID string) tea.Cmd {
	for i, t := range m.terminals {
		if t.PaneID == paneID {
			return m.switchTerminalIdx(i)
		}
	}
	return nil
}

// switchTerminalIdx switches to a terminal tab by index.
func (m *Model) switchTerminalIdx(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.terminals) {
		return nil
	}
	// Already active — just focus it
	if m.drawerOpen && idx == m.activeTermIdx {
		m.tmux.SelectPane(m.terminals[idx].PaneID)
		return nil
	}

	// Hide current if drawer is open
	if m.drawerOpen && m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		if err := m.hidePane(m.terminals[m.activeTermIdx].PaneID); err != nil {
			return statusCmd(fmt.Sprintf("err: %v", err))
		}
	}

	// Show target
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName(), m.windowName)
	if err := m.tmux.JoinPaneVertical(m.terminals[idx].PaneID, target); err != nil {
		return statusCmd(fmt.Sprintf("err: %v", err))
	}

	m.tmux.SelectPane(m.terminals[idx].PaneID)
	m.activeTermIdx = idx
	m.drawerOpen = true
	return statusCmd(m.terminals[idx].Name)
}

// cycleTerminal switches to the next or previous terminal tab.
func (m *Model) cycleTerminal(direction int) tea.Cmd {
	if len(m.terminals) < 2 {
		return nil
	}
	next := (m.activeTermIdx + direction + len(m.terminals)) % len(m.terminals)
	return m.switchTerminalIdx(next)
}

// closeTerminal kills the active terminal and switches to the next one.
func (m *Model) closeTerminal() tea.Cmd {
	if len(m.terminals) == 0 || m.activeTermIdx < 0 {
		return statusCmd("no terminal")
	}

	idx := m.activeTermIdx
	paneID := m.terminals[idx].PaneID
	name := m.terminals[idx].Name

	// Kill the pane
	m.tmux.KillPane(paneID)

	// Remove from list
	m.terminals = append(m.terminals[:idx], m.terminals[idx+1:]...)

	if len(m.terminals) == 0 {
		m.activeTermIdx = -1
		m.drawerOpen = false
		return statusCmd(name + " closed")
	}

	// Switch to next (or wrap)
	if idx >= len(m.terminals) {
		idx = len(m.terminals) - 1
	}

	// Show the next terminal
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName(), m.windowName)
	if err := m.tmux.JoinPaneVertical(m.terminals[idx].PaneID, target); err != nil {
		m.activeTermIdx = idx
		m.drawerOpen = false
		return statusCmd(fmt.Sprintf("err: %v", err))
	}

	m.tmux.SelectPane(m.terminals[idx].PaneID)
	m.activeTermIdx = idx
	m.drawerOpen = true
	return statusCmd(name + " closed")
}

// termSession returns the mo-terms session name scoped to this sidebar's
// window (see ops.TermSessionName — mo web derives the same name to find
// and create a window's terminals).
func (m Model) termSession() string {
	return ops.TermSessionName(m.instanceID, m.windowID, m.windowName)
}

// ensureMoTerms lazily creates the per-window mo-terms session that holds
// terminal tabs when they're not displayed in the drawer. Returns the pane
// ID of the session's initial window when this call actually creates the
// session — callers decide whether to track that pane as a tab (popup
// entry point) or kill it (drawer hide path). Returns "" when the session
// already existed.
func (m *Model) ensureMoTerms() (string, error) {
	return ops.EnsureTermSession(m.tmux, m.termSession(), m.windowPath)
}

// hidePane parks a terminal pane in the mo-terms session so it survives
// drawer close and remains reachable by the backtick popup. If this call
// lazily created mo-terms, tmux will have spawned a default initial window
// alongside the parked pane — kill it so the session only contains real
// tabs.
func (m *Model) hidePane(paneID string) error {
	ghost, err := m.ensureMoTerms()
	if err != nil {
		return err
	}
	if err := m.tmux.BreakPaneToSession(paneID, m.termSession()); err != nil {
		return err
	}
	if ghost != "" {
		_ = m.tmux.KillPane(ghost)
	}
	return nil
}

// windowTarget addresses this sidebar's own window for window options.
func (m *Model) windowTarget() string {
	if m.windowID != "" {
		return m.windowID
	}
	return fmt.Sprintf("%s:%s", m.tmux.SessionName(), m.windowName)
}

// adoptTerminals adds terminals this sidebar doesn't track yet: panes
// parked in its mo-terms session (opened by mo web, or left behind by a
// previous sidebar process before a restart), and — once, at startup — the
// pane a previous sidebar process left open in the drawer, named by the
// window's @mo_drawer_pane option. Returns the adopted drawer pane and its
// index when one was adopted ("" and -1 otherwise).
func (m *Model) adoptTerminals() (string, int) {
	known := make(map[string]bool, len(m.terminals))
	for _, t := range m.terminals {
		known[t.PaneID] = true
	}
	add := func(paneID string) int {
		m.termCounter++
		m.terminals = append(m.terminals, TerminalTab{PaneID: paneID, Name: fmt.Sprintf("term-%d", m.termCounter)})
		known[paneID] = true
		return len(m.terminals) - 1
	}

	drawerPane, drawerIdx := "", -1
	if !m.drawerAdopted {
		m.drawerAdopted = true
		if id := m.tmux.WindowOption(m.windowTarget(), ttmux.DrawerPaneOption); id != "" && !known[id] && m.paneInOwnWindow(id) {
			drawerPane, drawerIdx = id, add(id)
		}
	}
	if panes, err := m.tmux.ListSessionPanes(m.termSession()); err == nil {
		for _, p := range panes {
			if !known[p.ID] {
				add(p.ID)
			}
		}
	}
	return drawerPane, drawerIdx
}

// paneInOwnWindow reports whether paneID is currently a pane of this
// sidebar's window (i.e. shown in the drawer, not parked).
func (m *Model) paneInOwnWindow(paneID string) bool {
	panes, err := m.tmux.ListWindowPanes(m.windowTarget())
	if err != nil {
		return false
	}
	for _, p := range panes {
		if p.ID == paneID {
			return true
		}
	}
	return false
}

// publishDrawerPane records the terminal shown in the drawer (or "" when
// the drawer is closed) in the window's @mo_drawer_pane option, so mo web
// and a restarted sidebar can find it. Writes only on change.
func (m *Model) publishDrawerPane() {
	pane := ""
	if m.drawerOpen && m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		pane = m.terminals[m.activeTermIdx].PaneID
	}
	if pane == m.publishedDrawerPane {
		return
	}
	if err := m.tmux.SetWindowOption(m.windowTarget(), ttmux.DrawerPaneOption, pane); err == nil {
		m.publishedDrawerPane = pane
	}
}

// refreshTerminals verifies tracked terminals are still alive and appends
// terminal items to the sidebar item list. Any existing terminal items in
// m.items are stripped first so the section isn't duplicated when called
// from the fallback (stale state file) path.
func (m *Model) refreshTerminals() {
	// Strip existing terminal items so we don't accumulate duplicates.
	n := 0
	for _, item := range m.items {
		if !item.IsTerminal && !(item.IsHeader && item.Name == "Terminals") {
			m.items[n] = item
			n++
		}
	}
	m.items = m.items[:n]

	// Remember the active pane so we can detect if it gets pruned
	activePaneID := ""
	if m.drawerOpen && m.activeTermIdx >= 0 && m.activeTermIdx < len(m.terminals) {
		activePaneID = m.terminals[m.activeTermIdx].PaneID
	}

	// Prune dead terminals
	alive := m.terminals[:0]
	newActiveIdx := -1
	for _, t := range m.terminals {
		if m.tmux.IsPaneAlive(t.PaneID) {
			if t.PaneID == activePaneID {
				newActiveIdx = len(alive)
			}
			alive = append(alive, t)
		}
	}
	m.terminals = alive

	// A drawer pane left open by a previous sidebar process becomes this
	// one's open drawer tab (only when nothing is open here already).
	if drawerPane, idx := m.adoptTerminals(); drawerPane != "" && activePaneID == "" {
		activePaneID, newActiveIdx = drawerPane, idx
		m.drawerOpen = true
	}

	if len(m.terminals) == 0 {
		m.activeTermIdx = -1
		m.drawerOpen = false
	} else if activePaneID != "" && newActiveIdx == -1 {
		// The visible terminal was pruned — the drawer pane is gone from
		// the window layout, so mark the drawer as closed.
		m.activeTermIdx = 0
		m.drawerOpen = false
	} else if newActiveIdx >= 0 {
		m.activeTermIdx = newActiveIdx
	} else if m.activeTermIdx >= len(m.terminals) {
		m.activeTermIdx = len(m.terminals) - 1
	}

	m.publishDrawerPane()

	// Append terminal items to the sidebar list
	if len(m.terminals) > 0 {
		m.items = append(m.items, SidebarItem{
			Name:     "Terminals",
			IsHeader: true,
		})
		for i, t := range m.terminals {
			m.items = append(m.items, SidebarItem{
				Name:       fmt.Sprintf("%d: %s", i+1, t.Name),
				IsTerminal: true,
				PaneID:     t.PaneID,
				IsActive:   m.drawerOpen && i == m.activeTermIdx,
			})
		}
	}
}
