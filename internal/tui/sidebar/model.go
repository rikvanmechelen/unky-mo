package sidebar

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

type SidebarItem struct {
	Name       string
	Path       string // project directory path
	WindowName string // tmux window target; empty for Home (window 0)
	Status     string // "none", "active", "idle", "permission"
	Parent     string // non-empty for worktree entries (parent project name)
	IsHome     bool
	IsHeader   bool // non-interactive section header (e.g., "── Terminals ──")
	// Terminal items
	IsTerminal bool
	PaneID     string
	IsActive   bool // terminal is currently visible in drawer
}

// TerminalTab tracks a terminal pane managed by the drawer.
type TerminalTab struct {
	PaneID string // stable tmux pane ID (e.g., "%5")
	Name   string // display name ("term-1", "term-2")
}

type sidebarStatusMsg string
type stateTickMsg time.Time

func stateTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return stateTickMsg(t)
	})
}

type Model struct {
	items         []SidebarItem
	cursor        int
	viewportStart int // for scrolling
	tmux          *ttmux.Client
	stateFile     string
	statusMsg     string
	cursorSetOnce bool // true after initial cursor placement
	// The project this sidebar belongs to (detected from tmux window name)
	windowName string
	windowPath string
	width      int
	height     int
	// Terminal drawer state
	terminals     []TerminalTab
	activeTermIdx int  // index into terminals; -1 when drawer closed
	drawerOpen    bool
	termCounter   int // incrementing counter for naming
	// Changed files tree (from git status)
	changedFiles []string // raw file paths from git status --porcelain
}

func NewModel(sessionName, stateFile string) Model {
	// Resolve this pane's own window via TMUX_PANE, not the client's current
	// focus. Using `display-message -p` without a target returns the attached
	// client's focused window, which is racy right after a new pane is created
	// (e.g. when launching a worktree session: the pane is split before the
	// client switches focus). For worktree windows this is especially broken
	// because the window name (<project>@<branch>) isn't in the state file.
	windowName := ""
	if paneID := os.Getenv("TMUX_PANE"); paneID != "" {
		out, err := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{window_name}").Output()
		if err == nil {
			windowName = strings.TrimSpace(string(out))
		}
	}
	if windowName == "" {
		windowName = ttmux.CurrentWindowName()
	}

	// The sidebar process's cwd is its pane's cwd at startup; it's a Go
	// program with no shell that could cd elsewhere, so this is always the
	// right path for the window we're in — including worktrees.
	windowPath, _ := os.Getwd()

	tc := ttmux.NewClient(sessionName)
	tc.ConfigureStatusFormat()

	m := Model{
		tmux:          tc,
		stateFile:     stateFile,
		windowName:    windowName,
		windowPath:    windowPath,
		activeTermIdx: -1,
	}
	m.items = append(m.items, SidebarItem{
		Name:   "Unky Mo Home",
		IsHome: true,
	})
	m.refreshState()
	return m
}

func (m Model) Init() tea.Cmd {
	return stateTick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sidebarStatusMsg:
		m.statusMsg = string(msg)
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return sidebarStatusMsg("")
		})

	case stateTickMsg:
		m.refreshState()
		return m, stateTick()

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// Calculate which item was clicked (Y=0 is the header line)
			clicked := m.viewportStart + msg.Y - 1 // -1 for header
			if clicked >= 0 && clicked < len(m.items) && !m.items[clicked].IsHeader {
				m.cursor = clicked
				m.ensureCursorVisible()
				return m, m.handleEnter()
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if len(m.items) > 0 {
				m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				// Skip header items
				if m.items[m.cursor].IsHeader {
					m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				}
				m.ensureCursorVisible()
			}
		case "down", "j":
			if len(m.items) > 0 {
				m.cursor = (m.cursor + 1) % len(m.items)
				// Skip header items
				if m.items[m.cursor].IsHeader {
					m.cursor = (m.cursor + 1) % len(m.items)
				}
				m.ensureCursorVisible()
			}
		case "enter":
			return m, m.handleEnter()
		case "t":
			return m, m.toggleDrawer()
		case "T":
			return m, m.newTerminal()
		case "tab":
			return m, m.cycleTerminal(1)
		case "shift+tab":
			return m, m.cycleTerminal(-1)
		case "x":
			return m, m.closeTerminal()
		case "`":
			return m, m.openPopup()
		case "s":
			return m, m.syncPush()
		case "ctrl+r":
			self, err := os.Executable()
			if err == nil {
				return m, tea.ExecProcess(exec.Command(self, "sidebar"), nil)
			}
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}

	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}

	var b strings.Builder

	// Header
	header := headerStyle.Render("── Sessions ──")
	b.WriteString(header + "\n")

	// Calculate visible range
	headerLines := 1
	footerLines := 5
	maxVisible := m.height - headerLines - footerLines
	if maxVisible < 1 {
		maxVisible = 1
	}

	end := m.viewportStart + maxVisible
	if end > len(m.items) {
		end = len(m.items)
	}

	// Render items
	maxNameLen := m.width - 5 // cursor(2) + dot(2) + padding(1)
	if maxNameLen < 5 {
		maxNameLen = 5
	}

	for i := m.viewportStart; i < end; i++ {
		item := m.items[i]
		isSelected := i == m.cursor

		// Cursor
		cursor := "  "
		if isSelected {
			cursor = "▸ "
		}

		var line string
		if item.IsHeader {
			// Section header — not selectable
			line = headerStyle.Render(item.Name)
			b.WriteString(line + "\n")
			continue
		} else if item.IsTerminal {
			// Terminal item: "  1: term-1 ◀"
			name := truncateName(item.Name, maxNameLen-2)
			var styledName string
			if item.IsActive {
				styledName = termActiveStyle.Render(name) + " " + termActiveStyle.Render("◀")
			} else {
				styledName = normalStyle.Render(name)
			}
			line = cursor + styledName
		} else if item.IsHome {
			name := truncateName("☗ "+item.Name, maxNameLen)
			line = cursor + homeStyle.Render(name)
		} else {
			dot := renderDot(item.Status)
			isCurrent := item.WindowName == m.windowName

			// Worktree entries are indented under their parent project
			indent := ""
			nameMaxLen := maxNameLen - 2
			if item.Parent != "" {
				indent = "  "
				nameMaxLen -= 2
			}
			name := truncateName(item.Name, nameMaxLen)

			// Style the name — current window always gets bold purple + underline
			var styledName string
			if isCurrent {
				styledName = currentStyle.Render(name)
			} else {
				styledName = normalStyle.Render(name)
			}

			// Append status label after styling (so underline doesn't extend)
			suffix := ""
			if item.Status == "idle" {
				suffix = " " + dotIdle.Render("idle")
			} else if item.Status == "permission" {
				suffix = " " + dotPermission.Render("perm")
			}

			line = cursor + dot + " " + indent + styledName + suffix
		}

		b.WriteString(line + "\n")
	}

	// Scroll indicators
	if m.viewportStart > 0 {
		b.WriteString(footerStyle.Render("  ▲ more") + "\n")
	} else if end < len(m.items) {
		// pad to fill, then show indicator
	}
	if end < len(m.items) {
		b.WriteString(footerStyle.Render("  ▼ more") + "\n")
	}

	// Changed files tree in the bottom half
	contentLines := strings.Count(b.String(), "\n")
	remaining := m.height - contentLines - footerLines
	if m.statusMsg != "" {
		remaining-- // reserve a line for status
	}

	if len(m.changedFiles) > 0 && remaining > 3 {
		// Header takes 2 lines (blank + title)
		maxTreeLines := remaining - 2
		if maxTreeLines > 0 {
			tree := renderFileTree(m.changedFiles, m.width, maxTreeLines)
			if tree != "" {
				b.WriteString("\n")
				b.WriteString(headerStyle.Render(fmt.Sprintf("Changed (%d)", len(m.changedFiles))) + "\n")
				treeLines := strings.Count(tree, "\n") + 1
				b.WriteString(normalStyle.Render(tree) + "\n")
				remaining -= treeLines + 2
			}
		}
	}

	// Pad remaining space
	for i := 0; i < remaining; i++ {
		b.WriteString("\n")
	}

	// Status message
	if m.statusMsg != "" {
		b.WriteString(dotIdle.Render(m.statusMsg) + "\n")
	}

	// Footer
	b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎ select") + "\n")
	b.WriteString(footerStyle.Render(" t drawer T +term") + "\n")
	b.WriteString(footerStyle.Render(" ⇥ next   x close") + "\n")
	b.WriteString(footerStyle.Render(" ` popup  s sync")  + "\n")
	b.WriteString(footerStyle.Render(" ^r refresh"))

	return b.String()
}

func renderDot(status string) string {
	switch status {
	case "active":
		return dotActive.Render("●")
	case "idle":
		return dotIdle.Render("●")
	case "permission":
		return dotPermission.Render("●")
	default:
		return dotNone.Render("○")
	}
}

func truncateName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	if maxLen <= 3 {
		return name[:maxLen]
	}
	return name[:maxLen-3] + "..."
}

func (m *Model) ensureCursorVisible() {
	headerLines := 1
	footerLines := 5
	maxVisible := m.height - headerLines - footerLines
	if maxVisible < 1 {
		maxVisible = 1
	}

	if m.cursor < m.viewportStart {
		m.viewportStart = m.cursor
	}
	if m.cursor >= m.viewportStart+maxVisible {
		m.viewportStart = m.cursor - maxVisible + 1
	}
}

func (m *Model) refreshState() {
	sf, err := state.Read(m.stateFile)
	if err != nil {
		// Fallback: try to detect sessions independently
		m.refreshFromSessions()
		m.refreshTerminals()
		return
	}

	// Check staleness (>30s means main TUI might not be running)
	if time.Since(sf.UpdatedAt) > 30*time.Second {
		m.refreshFromSessions()
		m.refreshTerminals()
		return
	}

	// Rebuild items: Home + only projects with active sessions
	items := []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}
	for _, p := range sf.Projects {
		if p.Status == "none" {
			continue
		}
		items = append(items, SidebarItem{
			Name:       p.Name,
			Path:       p.Path,
			WindowName: p.WindowName,
			Status:     p.Status,
			Parent:     p.Parent,
		})
	}

	m.items = items
	m.refreshTerminals()
	m.refreshChangedFiles()

	// Set cursor to own project on first load only
	if !m.cursorSetOnce {
		for i, item := range m.items {
			if item.WindowName == m.windowName {
				m.cursor = i
				break
			}
		}
		m.cursorSetOnce = true
	}

	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
}

func (m *Model) refreshFromSessions() {
	// Fallback: read live sessions directly
	sessions, _ := claude.LiveSessions()
	liveByPath := make(map[string]bool)
	for _, s := range sessions {
		liveByPath[s.CWD] = true
	}

	// Keep existing items but update statuses
	for i := range m.items {
		if m.items[i].IsHome {
			continue
		}
		if liveByPath[m.items[i].Path] {
			m.items[i].Status = "active"
		} else {
			m.items[i].Status = "none"
		}
	}
}

func (m *Model) refreshChangedFiles() {
	if m.windowPath == "" {
		m.changedFiles = nil
		return
	}
	cmd := exec.Command("git", "-C", m.windowPath, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		m.changedFiles = nil
		return
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) < 4 {
			continue
		}
		// Strip the 2-char status prefix + space
		file := strings.TrimSpace(line[2:])
		// Handle renames: "old -> new"
		if idx := strings.Index(file, " -> "); idx >= 0 {
			file = file[idx+4:]
		}
		files = append(files, file)
	}
	m.changedFiles = files
}

// renderFileTree renders changed files as a compact tree.
func renderFileTree(files []string, maxWidth, maxLines int) string {
	if len(files) == 0 {
		return ""
	}

	// Build tree structure
	type node struct {
		name     string
		children map[string]*node
		isFile   bool
		order    []string // preserve insertion order
	}

	root := &node{children: make(map[string]*node)}

	for _, file := range files {
		parts := strings.Split(file, "/")
		current := root
		for i, part := range parts {
			if _, ok := current.children[part]; !ok {
				child := &node{name: part, children: make(map[string]*node)}
				if i == len(parts)-1 {
					child.isFile = true
				}
				current.children[part] = child
				current.order = append(current.order, part)
			}
			current = current.children[part]
		}
	}

	// Collapse single-child directories (e.g. "internal/tui/" → "internal/tui/")
	var collapse func(n *node) *node
	collapse = func(n *node) *node {
		for _, name := range n.order {
			child := n.children[name]
			n.children[name] = collapse(child)
		}
		if !n.isFile && len(n.children) == 1 {
			childName := n.order[0]
			child := n.children[childName]
			if !child.isFile {
				merged := &node{
					name:     n.name + "/" + child.name,
					children: child.children,
					order:    child.order,
					isFile:   child.isFile,
				}
				return merged
			}
		}
		return n
	}

	for _, name := range root.order {
		root.children[name] = collapse(root.children[name])
	}

	// Render tree
	var lines []string
	var render func(n *node, indent string)
	render = func(n *node, indent string) {
		if len(lines) >= maxLines {
			return
		}
		for _, name := range n.order {
			child := n.children[name]
			if len(lines) >= maxLines {
				return
			}
			if child.isFile {
				lines = append(lines, indent+child.name)
			} else {
				lines = append(lines, indent+child.name+"/")
				render(child, indent+"  ")
			}
		}
	}
	render(root, " ")

	if len(files) > len(lines) {
		lines = append(lines, fmt.Sprintf(" +%d more", len(files)-len(lines)))
	}

	return strings.Join(lines, "\n")
}

func (m Model) selectedItem() *SidebarItem {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.IsHome || item.Path == "" {
		return nil
	}
	return &item
}

// statusCmd returns a tea.Cmd that sends a status message, or nil if empty.
func statusCmd(s string) tea.Cmd {
	if s == "" {
		return nil
	}
	return func() tea.Msg { return sidebarStatusMsg(s) }
}

// handleEnter dispatches enter based on whether a session or terminal is selected.
func (m *Model) handleEnter() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	if item.IsTerminal {
		return m.switchTerminalByPaneID(item.PaneID)
	}
	return m.switchToSelected()
}

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

	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, m.windowName)
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
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, m.windowName)
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
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, m.windowName)
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
	target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, m.windowName)
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

// hidePane moves a terminal pane out of the main window into hidden storage.
// If other hidden panes exist, consolidates into the same hidden window.
// The hidden window is marked with @mo_hidden so ConfigureStatusFormat filters
// it from the tmux status bar.
func (m *Model) hidePane(paneID string) error {
	// Find another hidden pane to consolidate with
	for i, t := range m.terminals {
		if t.PaneID == paneID {
			continue
		}
		// Check if this pane is hidden (not the active visible one)
		isActive := m.drawerOpen && i == m.activeTermIdx
		if !isActive {
			if err := m.tmux.JoinPaneConsolidate(paneID, t.PaneID); err != nil {
				return err
			}
			// Mark using the destination pane ID — tmux resolves it to the
			// containing window.
			m.tmux.SetWindowOption(t.PaneID, "@mo_hidden", "1")
			return nil
		}
	}
	// No other hidden panes — break into a new window and mark it hidden.
	if err := m.tmux.BreakPane(paneID); err != nil {
		return err
	}
	// The pane is now in its new window — mark it directly via pane ID.
	m.tmux.SetWindowOption(paneID, "@mo_hidden", "1")
	return nil
}

// refreshTerminals verifies tracked terminals are still alive and appends
// terminal items to the sidebar item list.
func (m *Model) refreshTerminals() {
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

	// Append terminal items to the sidebar list
	if len(m.terminals) > 0 {
		m.items = append(m.items, SidebarItem{
			Name:     "── Terminals ──",
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

func (m Model) syncPush() tea.Cmd {
	return func() tea.Msg {
		if m.windowName == "" || m.windowPath == "" {
			return sidebarStatusMsg("no project")
		}
		syncDir := moSync.DefaultSyncDir()
		if err := moSync.Push(m.windowName, m.windowPath, syncDir); err != nil {
			return sidebarStatusMsg(fmt.Sprintf("sync err: %v", err))
		}
		return sidebarStatusMsg("synced " + m.windowName)
	}
}

func (m Model) openPopup() tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	title := fmt.Sprintf(" %s ", m.windowName)
	c := exec.Command("tmux", "display-popup", "-E", "-d", m.windowPath, "-w", "80%", "-h", "80%", "-T", title)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("err: %v", err))
		}
		return nil
	})
}

func (m Model) switchToSelected() tea.Cmd {
	return func() tea.Msg {
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return nil
		}

		item := m.items[m.cursor]
		var target string
		if item.IsHome {
			target = fmt.Sprintf("%s:0", m.tmux.SessionName)
		} else if item.WindowName != "" {
			target = fmt.Sprintf("%s:%s", m.tmux.SessionName, item.WindowName)
		} else {
			return nil
		}

		m.tmux.SwitchToWindow(target)

		return nil
	}
}
