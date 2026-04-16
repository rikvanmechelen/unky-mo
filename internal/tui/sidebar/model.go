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
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

type SidebarItem struct {
	Name       string
	Path       string // project directory path
	WindowName string // tmux window target; empty for Home (window 0)
	Status     string // "none", "active", "idle", "permission"
	IsHome     bool
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
}

func NewModel(sessionName, stateFile string) Model {
	// Detect which project window we're in
	windowName := ttmux.CurrentWindowName()
	windowPath := ""

	// Look up the project path from the state file
	if sf, err := state.Read(stateFile); err == nil {
		for _, p := range sf.Projects {
			if p.WindowName == windowName || p.Name == windowName {
				windowPath = p.Path
				break
			}
		}
	}

	// Fallback: use pwd if we couldn't find it in state
	if windowPath == "" {
		windowPath, _ = os.Getwd()
	}

	m := Model{
		tmux:       ttmux.NewClient(sessionName),
		stateFile:  stateFile,
		windowName: windowName,
		windowPath: windowPath,
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
			if clicked >= 0 && clicked < len(m.items) {
				m.cursor = clicked
				m.ensureCursorVisible()
				return m, m.switchToSelected()
			}
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if len(m.items) > 0 {
				m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				m.ensureCursorVisible()
			}
		case "down", "j":
			if len(m.items) > 0 {
				m.cursor = (m.cursor + 1) % len(m.items)
				m.ensureCursorVisible()
			}
		case "enter":
			return m, m.switchToSelected()
		case "t":
			return m, m.openTerminal()
		case "`":
			return m, m.openPopup()
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
	footerLines := 3
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

		// Dot + name
		var line string
		if item.IsHome {
			name := truncateName("☗ "+item.Name, maxNameLen)
			line = cursor + homeStyle.Render(name)
		} else {
			dot := renderDot(item.Status)
			name := truncateName(item.Name, maxNameLen-2)
			isCurrent := item.WindowName == m.windowName

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

			line = cursor + dot + " " + styledName + suffix
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

	// Pad remaining space
	contentLines := strings.Count(b.String(), "\n")
	remaining := m.height - contentLines - footerLines
	for i := 0; i < remaining; i++ {
		b.WriteString("\n")
	}

	// Status message
	if m.statusMsg != "" {
		b.WriteString(dotIdle.Render(m.statusMsg) + "\n")
	}

	// Footer
	b.WriteString(footerStyle.Render(" ↑↓ nav  ⏎ switch") + "\n")
	b.WriteString(footerStyle.Render(" t term  ` popup") + "\n")
	b.WriteString(footerStyle.Render(" ctrl+r restart"))

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
	footerLines := 3
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
		return
	}

	// Check staleness (>30s means main TUI might not be running)
	if time.Since(sf.UpdatedAt) > 30*time.Second {
		m.refreshFromSessions()
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
		})
	}

	m.items = items

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
		if liveByPath[m.items[i].WindowName] {
			m.items[i].Status = "active"
		} else {
			m.items[i].Status = "none"
		}
	}
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

func (m Model) openTerminal() tea.Cmd {
	return func() tea.Msg {
		if m.windowPath == "" {
			return sidebarStatusMsg("no project path")
		}
		// Split a terminal below the Claude pane in this window
		target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, m.windowName)
		paneID, err := m.tmux.SplitWindowHorizontal(target, m.windowPath)
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("err: %v", err))
		}
		m.tmux.SelectPane(paneID)
		return sidebarStatusMsg("terminal opened")
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
