package sidebar

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

type SidebarItem struct {
	Name       string
	WindowName string // tmux window target; empty for Home (window 0)
	Status     string // "none", "active", "idle", "permission"
	IsHome     bool
}

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
	width         int
	height        int
}

func NewModel(sessionName, stateFile string) Model {
	m := Model{
		tmux:      ttmux.NewClient(sessionName),
		stateFile: stateFile,
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

	case stateTickMsg:
		m.refreshState()
		return m, stateTick()

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
			if isSelected {
				line = cursor + selectedStyle.Render(name)
			} else {
				line = cursor + homeStyle.Render(name)
			}
		} else {
			dot := renderDot(item.Status)
			name := truncateName(item.Name, maxNameLen-2)

			// Add short status label for idle/permission
			if item.Status == "idle" && maxNameLen > len(name)+5 {
				name += " " + dotIdle.Render("idle")
			} else if item.Status == "permission" && maxNameLen > len(name)+5 {
				name += " " + dotPermission.Render("perm")
			}

			if isSelected {
				line = cursor + dot + " " + selectedStyle.Render(name)
			} else {
				line = cursor + dot + " " + normalStyle.Render(name)
			}
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

	// Footer
	b.WriteString(footerStyle.Render(" ↑↓ navigate") + "\n")
	b.WriteString(footerStyle.Render(" ⏎  switch") + "\n")
	b.WriteString(footerStyle.Render(" q  quit"))

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

	// Rebuild items: Home + projects from state file
	items := []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}
	for _, p := range sf.Projects {
		items = append(items, SidebarItem{
			Name:       p.Name,
			WindowName: p.WindowName,
			Status:     p.Status,
		})
	}

	m.items = items
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

func (m Model) switchToSelected() tea.Cmd {
	return func() tea.Msg {
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return nil
		}

		item := m.items[m.cursor]
		if item.IsHome {
			// Switch to window 0 (the main TUI)
			target := fmt.Sprintf("%s:0", m.tmux.SessionName)
			m.tmux.SwitchToWindow(target)
		} else if item.WindowName != "" {
			target := fmt.Sprintf("%s:%s", m.tmux.SessionName, item.WindowName)
			m.tmux.SwitchToWindow(target)
		}

		return nil
	}
}
