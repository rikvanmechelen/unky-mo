package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/state"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// SessionStatus represents the state of a Claude session for a project.
type SessionStatus int

const (
	StatusNone       SessionStatus = iota
	StatusActive                   // Claude is processing
	StatusIdle                     // Waiting for user input
	StatusPermission               // Needs permission approval
)

// ProjectItem wraps a project for the list component.
type ProjectItem struct {
	project project.Project
	status  SessionStatus
}

func (i ProjectItem) Title() string       { return i.project.Name }
func (i ProjectItem) Description() string { return i.project.Path }
func (i ProjectItem) FilterValue() string { return i.project.Name + " " + i.project.Language }

// Screen identifies which view is active.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenProject
	ScreenWorktrees
	ScreenHelp
)

// sessionTickMsg triggers a session status refresh.
type sessionTickMsg time.Time

func sessionTick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return sessionTickMsg(t)
	})
}

// sessionStateMap holds the notification-based status overrides for projects.
// Key is project path, value is the status from notifications.
type sessionStateMap map[string]SessionStatus

// notificationMsg wraps a notification received from the Unix socket.
type notificationMsg notify.Notification

// Model is the root Bubbletea model.
type Model struct {
	screen         Screen
	list           list.Model
	projects       []project.Project
	tmux           *ttmux.Client
	notifServer    *notify.Server
	notifState     sessionStateMap // status overrides from notification system
	statusMsg      string
	activeSessions int
	attentionCount int
	// Detail views
	detailProject        *project.Project
	detailSession        *claude.Session
	detailWorktrees      []project.Worktree
	detailRecentSessions []claude.RecentSession
	detailSessionCursor  int
	// State file for sidebar instances
	stateFilePath string
	width         int
	height        int
	ready         bool
}

func NewModel(projects []project.Project, tmuxClient *ttmux.Client, notifServer *notify.Server, stateFilePath string) Model {
	items := make([]list.Item, len(projects))
	for i, p := range projects {
		items[i] = ProjectItem{project: p, status: StatusNone}
	}

	l := list.New(items, projectDelegate{}, 0, 0)
	l.Title = "Unky Mo"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)        // We use our own footer
	l.InfiniteScrolling = true  // Circular navigation
	l.Styles.Title = titleStyle

	return Model{
		screen:        ScreenDashboard,
		list:          l,
		projects:      projects,
		tmux:          tmuxClient,
		notifServer:   notifServer,
		notifState:    make(sessionStateMap),
		stateFilePath: stateFilePath,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{sessionTick(), m.refreshSessions()}
	if m.notifServer != nil {
		cmds = append(cmds, m.waitForNotification())
	}
	return tea.Batch(cmds...)
}

func (m Model) waitForNotification() tea.Cmd {
	return func() tea.Msg {
		n := <-m.notifServer.Messages()
		return notificationMsg(n)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		footerHeight := 3
		headerHeight := 0
		m.list.SetSize(msg.Width, msg.Height-footerHeight-headerHeight)
		m.ready = true
		return m, nil

	case tea.KeyMsg:
		// Don't intercept keys when filtering
		if m.list.FilterState() == list.Filtering {
			break
		}

		switch {
		case key.Matches(msg, keys.Help):
			if m.screen == ScreenHelp {
				m.screen = ScreenDashboard
			} else {
				m.screen = ScreenHelp
			}
			return m, nil

		case key.Matches(msg, keys.Enter):
			if m.screen == ScreenDashboard {
				p := m.currentProject()
				if p != nil {
					m.detailProject = p
					m.detailSession = claude.SessionForPath(p.Path)
					m.detailWorktrees, _ = project.ListWorktrees(p.Path)
					m.detailRecentSessions = claude.RecentSessions(p.Path, 10)
					m.detailSessionCursor = 0
					m.screen = ScreenProject
				}
				return m, nil
			}
			if m.screen == ScreenProject {
				// Resume the selected recent session
				if len(m.detailRecentSessions) > 0 && m.detailSessionCursor < len(m.detailRecentSessions) {
					return m, m.resumeSpecificSession(m.detailRecentSessions[m.detailSessionCursor].SessionID)
				}
				return m, nil
			}

		case key.Matches(msg, keys.Back):
			if m.screen != ScreenDashboard {
				m.screen = ScreenDashboard
				return m, nil
			}

		case key.Matches(msg, keys.New):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				return m, m.launchSession()
			}

		case key.Matches(msg, keys.Attach):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				return m, m.attachSession()
			}

		case key.Matches(msg, keys.Resume):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				return m, m.resumeSession()
			}

		case key.Matches(msg, keys.Worktree):
			if m.screen == ScreenDashboard || m.screen == ScreenProject {
				p := m.currentProject()
				if p != nil {
					m.detailProject = p
					m.detailWorktrees, _ = project.ListWorktrees(p.Path)
					m.screen = ScreenWorktrees
				}
				return m, nil
			}

		case key.Matches(msg, keys.Restart):
			self, err := os.Executable()
			if err == nil {
				return m, tea.ExecProcess(exec.Command(self), nil)
			}

		case key.Matches(msg, keys.Quit):
			if m.screen != ScreenDashboard {
				m.screen = ScreenDashboard
				return m, nil
			}
			return m, tea.Quit
		}

	case sessionTickMsg:
		return m, tea.Batch(sessionTick(), m.refreshSessions())

	case sessionRefreshMsg:
		m.updateProjectStatuses(msg)
		return m, nil

	case notificationMsg:
		m.handleNotification(notify.Notification(msg))
		cmds := []tea.Cmd{m.refreshSessions()}
		if m.notifServer != nil {
			cmds = append(cmds, m.waitForNotification())
		}
		return m, tea.Batch(cmds...)

	case statusMsgEvent:
		m.statusMsg = string(msg)
		m.list.NewStatusMessage(m.statusMsg)
		// Clear status after 4 seconds
		return m, tea.Tick(4*time.Second, func(t time.Time) tea.Msg {
			return clearStatusMsg{}
		})

	case clearStatusMsg:
		m.statusMsg = ""
		return m, nil
	}

	switch m.screen {
	case ScreenDashboard:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	case ScreenProject:
		// Handle up/down for session list cursor (circular)
		if msg, ok := msg.(tea.KeyMsg); ok {
			n := len(m.detailRecentSessions)
			if n > 0 {
				switch msg.String() {
				case "up", "k":
					m.detailSessionCursor = (m.detailSessionCursor - 1 + n) % n
				case "down", "j":
					m.detailSessionCursor = (m.detailSessionCursor + 1) % n
				}
			}
		}
	}

	return m, nil
}

func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}

	switch m.screen {
	case ScreenProject:
		return m.projectDetailView()
	case ScreenWorktrees:
		return m.worktreeView()
	case ScreenHelp:
		return m.helpView()
	default:
		return m.dashboardView()
	}
}

func (m Model) dashboardView() string {
	// Build status summary for the title bar
	var statusParts []string
	if m.activeSessions > 0 {
		statusParts = append(statusParts, statusActive.Render(fmt.Sprintf("%d active", m.activeSessions)))
	}
	if m.attentionCount > 0 {
		statusParts = append(statusParts, notifBadgeStyle.Render(fmt.Sprintf("▲%d", m.attentionCount)))
	}
	if len(statusParts) > 0 {
		summary := strings.Join(statusParts, "  ")
		padding := m.width - lipgloss.Width(m.list.View()) // approximate
		if padding < 0 {
			padding = 0
		}
		m.list.Title = "Unky Mo  " + summary
	}

	footer := m.renderFooter([]footerBinding{
		{"↑↓", "navigate"},
		{"enter", "open"},
		{"n", "new session"},
		{"a", "attach"},
		{"/", "filter"},
		{"?", "help"},
		{"ctrl+r", "restart"},
		{"q", "quit"},
	})

	return lipgloss.JoinVertical(lipgloss.Left,
		m.list.View(),
		footer,
	)
}

func (m Model) helpView() string {
	title := titleStyle.Render(" Help ")

	sections := []struct {
		name  string
		binds []footerBinding
	}{
		{"Navigation", []footerBinding{
			{"↑/k", "Move up"},
			{"↓/j", "Move down"},
			{"enter/→", "Open project detail"},
			{"esc/←", "Go back"},
			{"/", "Filter projects"},
		}},
		{"Sessions", []footerBinding{
			{"n", "Start new Claude session"},
			{"a", "Attach to session (switch tmux window)"},
			{"r", "Resume most recent session"},
			{"s", "View all active sessions"},
		}},
		{"Other", []footerBinding{
			{"w", "View git worktrees"},
			{"?", "Toggle this help"},
			{"ctrl+r", "Restart (reload new binary)"},
			{"q", "Quit"},
		}},
	}

	var b strings.Builder
	b.WriteString(title + "\n\n")

	for _, sec := range sections {
		b.WriteString(headerStyle.Render(sec.name) + "\n")
		for _, bind := range sec.binds {
			k := footerKeyStyle.Width(12).Render(bind.key)
			d := footerDescStyle.Render(bind.desc)
			b.WriteString(fmt.Sprintf("  %s %s\n", k, d))
		}
		b.WriteString("\n")
	}

	b.WriteString(footerDescStyle.Render("Press ? or esc to close"))

	return b.String()
}

type footerBinding struct {
	key  string
	desc string
}

func (m Model) renderFooter(bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	line := strings.Join(parts, "  ")
	return footerStyle.Width(m.width).Render(line)
}

// sessionRefreshMsg carries the detected status for each project path with a live session.
type sessionRefreshMsg map[string]SessionStatus

func (m Model) refreshSessions() tea.Cmd {
	return func() tea.Msg {
		sessions, _ := claude.LiveSessions()
		statuses := make(map[string]SessionStatus)
		for _, s := range sessions {
			if claude.IsSessionIdle(s.CWD, s.SessionID) {
				statuses[s.CWD] = StatusIdle
			} else {
				statuses[s.CWD] = StatusActive
			}
		}
		return sessionRefreshMsg(statuses)
	}
}

func (m *Model) updateProjectStatuses(polled sessionRefreshMsg) {
	items := m.list.Items()
	activeCount := 0
	attentionCount := 0

	for i, item := range items {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}

		// Notification-based status takes priority for permission prompts
		if notifStatus, ok := m.notifState[pi.project.Path]; ok && notifStatus == StatusPermission {
			pi.status = StatusPermission
			attentionCount++
			activeCount++
		} else if polledStatus, ok := polled[pi.project.Path]; ok {
			// Use poll-based status (detects idle from JSONL)
			pi.status = polledStatus
			if polledStatus == StatusIdle {
				attentionCount++
			}
			activeCount++
		} else {
			pi.status = StatusNone
		}

		items[i] = pi
	}

	m.activeSessions = activeCount
	m.attentionCount = attentionCount
	m.list.SetItems(items)
	m.writeStateFile()
}

func (m *Model) writeStateFile() {
	if m.stateFilePath == "" {
		return
	}

	var projects []state.ProjectState
	for _, item := range m.list.Items() {
		pi, ok := item.(ProjectItem)
		if !ok {
			continue
		}
		var statusStr string
		switch pi.status {
		case StatusActive:
			statusStr = "active"
		case StatusIdle:
			statusStr = "idle"
		case StatusPermission:
			statusStr = "permission"
		default:
			statusStr = "none"
		}
		projects = append(projects, state.ProjectState{
			Name:       pi.project.Name,
			Path:       pi.project.Path,
			WindowName: pi.project.Name,
			Status:     statusStr,
		})
	}

	sessionName := ""
	if m.tmux != nil {
		sessionName = m.tmux.SessionName
	}

	sf := &state.StateFile{
		TmuxSession: sessionName,
		Projects:    projects,
	}
	state.Write(m.stateFilePath, sf)
}

func (m Model) projectDetailView() string {
	if m.detailProject == nil {
		return "No project selected"
	}
	p := m.detailProject

	var b strings.Builder

	// Header
	title := titleStyle.Render(" ← " + p.Name + " ")
	lang := p.Language
	if lang == "" {
		lang = "unknown"
	}
	b.WriteString(title + "  " + langStyle.Render("["+lang+"]") + "\n\n")

	// Path
	b.WriteString(headerStyle.Render("Path") + "\n")
	b.WriteString("  " + footerDescStyle.Render(p.Path) + "\n\n")

	// Description
	if p.Description != "" {
		b.WriteString(headerStyle.Render("Description") + "\n")
		b.WriteString("  " + footerDescStyle.Render(p.Description) + "\n\n")
	}

	// Recent sessions
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if len(m.detailRecentSessions) == 0 {
		b.WriteString("  " + footerDescStyle.Render("No sessions found") + "\n")
	} else {
		for i, rs := range m.detailRecentSessions {
			cursor := "  "
			if i == m.detailSessionCursor {
				cursor = "▸ "
			}

			// Status
			var statusStr string
			if rs.IsLive {
				statusStr = statusActive.Render("● active")
			} else {
				statusStr = statusNone.Render("○")
			}

			// Age
			age := formatAge(time.Since(rs.LastActive))

			// Session name (customTitle from Claude, e.g. "unky-mo-session-orchestrator")
			name := rs.DisplayName()

			// Summary line underneath for context
			summary := rs.Summary
			if summary == "" {
				summary = "(no prompt)"
			}

			// Branch
			branchStr := ""
			if rs.GitBranch != "" {
				branchStr = langStyle.Render(" [" + rs.GitBranch + "]")
			}

			line := fmt.Sprintf("%s%s %s  %s%s", cursor, statusStr, age, name, branchStr)
			if summary != "" && summary != "(no prompt)" {
				// Truncate summary for the second line
				maxSummary := 60
				if len(summary) > maxSummary {
					summary = summary[:maxSummary-3] + "..."
				}
				line += "\n" + footerDescStyle.Render("      "+summary)
			}

			if i == m.detailSessionCursor {
				b.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				b.WriteString(normalItemStyle.Render(line) + "\n")
			}
		}
	}
	b.WriteString("\n")

	// Worktrees
	b.WriteString(headerStyle.Render(fmt.Sprintf("Worktrees (%d)", len(m.detailWorktrees))) + "\n")
	if len(m.detailWorktrees) == 0 {
		b.WriteString("  " + footerDescStyle.Render("none") + "\n")
	} else {
		for _, wt := range m.detailWorktrees {
			branch := wt.Branch
			if branch == "" {
				branch = wt.HEAD[:8]
			}
			b.WriteString(fmt.Sprintf("  %s  %s\n", normalItemStyle.Render(branch), footerDescStyle.Render(wt.Path)))
		}
	}

	// Status message
	if m.statusMsg != "" {
		b.WriteString("\n" + notifBadgeStyle.Render(m.statusMsg) + "\n")
	}

	// Footer
	footer := m.renderFooter([]footerBinding{
		{"↑↓", "select session"},
		{"enter", "resume selected"},
		{"n", "new session"},
		{"a", "attach"},
		{"w", "worktrees"},
		{"esc", "back"},
		{"?", "help"},
	})

	// Pad content to fill screen, then add footer
	content := b.String()
	contentLines := strings.Count(content, "\n")
	footerLines := 3
	if contentLines < m.height-footerLines {
		content += strings.Repeat("\n", m.height-footerLines-contentLines)
	}

	return content + footer
}

func (m Model) worktreeView() string {
	if m.detailProject == nil {
		return "No project selected"
	}
	p := m.detailProject

	var b strings.Builder

	title := titleStyle.Render(" ← Worktrees: " + p.Name + " ")
	b.WriteString(title + "\n\n")

	if len(m.detailWorktrees) == 0 {
		b.WriteString(footerDescStyle.Render("  No worktrees found for this project.\n"))
		b.WriteString(footerDescStyle.Render("  Use 'git worktree add' to create one.\n"))
	} else {
		for i, wt := range m.detailWorktrees {
			branch := wt.Branch
			if branch == "" && len(wt.HEAD) >= 8 {
				branch = "(detached " + wt.HEAD[:8] + ")"
			}

			prefix := "  "
			if i == 0 {
				prefix = "  " // main worktree
			}

			nameStr := selectedItemStyle.Render(branch)
			pathStr := footerDescStyle.Render(wt.Path)

			b.WriteString(fmt.Sprintf("%s%s\n", prefix, nameStr))
			b.WriteString(fmt.Sprintf("    %s\n\n", pathStr))
		}
	}

	footer := m.renderFooter([]footerBinding{
		{"esc", "back"},
		{"?", "help"},
	})

	content := b.String()
	contentLines := strings.Count(content, "\n")
	if contentLines < m.height-3 {
		content += strings.Repeat("\n", m.height-3-contentLines)
	}

	return content + footer
}

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (m *Model) handleNotification(n notify.Notification) {
	switch n.Type {
	case notify.NotifyIdlePrompt:
		m.notifState[n.ProjectPath] = StatusIdle
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs input", filepath.Base(n.ProjectPath)))
	case notify.NotifyPermissionPrompt:
		m.notifState[n.ProjectPath] = StatusPermission
		m.list.NewStatusMessage(fmt.Sprintf("● %s needs permission", filepath.Base(n.ProjectPath)))
	case notify.NotifySessionStop:
		// Clear notification status when session completes a turn
		delete(m.notifState, n.ProjectPath)
	}
}

// addSidebarPane splits off a sidebar pane in the given window target
// and refocuses back to the main (left) pane.
func (m Model) addSidebarPane(target string) {
	if m.tmux == nil {
		return
	}
	moPath, err := os.Executable()
	if err != nil {
		return
	}
	sidebarCmd := fmt.Sprintf("%s sidebar", moPath)
	m.tmux.SplitWindow(target, 22, sidebarCmd)
	// Refocus to the main pane (left/first pane)
	m.tmux.SelectPane(target + ".0")
}

type statusMsgEvent string
type clearStatusMsg struct{}

// currentProject returns the project for the current context —
// detailProject on the project/worktree screens, list selection on the dashboard.
func (m Model) currentProject() *project.Project {
	if m.detailProject != nil && (m.screen == ScreenProject || m.screen == ScreenWorktrees) {
		return m.detailProject
	}
	item, ok := m.list.SelectedItem().(ProjectItem)
	if !ok {
		return nil
	}
	return &item.project
}

func (m Model) launchSession() tea.Cmd {
	return func() tea.Msg {
		p := m.currentProject()
		if p == nil {
			return statusMsgEvent("No project selected")
		}

		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		windowName := p.Name
		if m.tmux.WindowExists(windowName) {
			// Window already exists, switch to it
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}

		target, err := m.tmux.CreateWindow(windowName, p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
		}

		if err := m.tmux.SendKeys(target, "claude"); err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to launch claude: %v", err))
		}

		m.addSidebarPane(target)

		if err := m.tmux.SwitchToWindow(target); err != nil {
			return statusMsgEvent(fmt.Sprintf("Launched but failed to switch: %v", err))
		}

		return statusMsgEvent("Launched Claude in " + windowName)
	}
}

func (m Model) resumeSession() tea.Cmd {
	return func() tea.Msg {
		p := m.currentProject()
		if p == nil {
			return statusMsgEvent("No project selected")
		}

		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		session := claude.SessionForPath(p.Path)
		if session == nil {
			return statusMsgEvent("No session to resume for " + p.Name)
		}

		windowName := p.Name
		if m.tmux.WindowExists(windowName) {
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}

		target, err := m.tmux.CreateWindow(windowName, p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
		}

		resumeCmd := fmt.Sprintf("claude --resume %s", session.SessionID)
		if err := m.tmux.SendKeys(target, resumeCmd); err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to resume: %v", err))
		}

		m.addSidebarPane(target)

		if err := m.tmux.SwitchToWindow(target); err != nil {
			return statusMsgEvent(fmt.Sprintf("Resumed but failed to switch: %v", err))
		}

		return statusMsgEvent("Resumed session in " + windowName)
	}
}

func (m Model) resumeSpecificSession(sessionID string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}

		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		windowName := p.Name
		if m.tmux.WindowExists(windowName) {
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}

		target, err := m.tmux.CreateWindow(windowName, p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
		}

		cmd := fmt.Sprintf("claude --resume %s", sessionID)
		if err := m.tmux.SendKeys(target, cmd); err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to resume: %v", err))
		}

		m.addSidebarPane(target)

		if err := m.tmux.SwitchToWindow(target); err != nil {
			return statusMsgEvent(fmt.Sprintf("Resumed but failed to switch: %v", err))
		}

		return statusMsgEvent("Resumed session in " + windowName)
	}
}

func (m Model) attachSession() tea.Cmd {
	return func() tea.Msg {
		p := m.currentProject()
		if p == nil {
			return statusMsgEvent("No project selected")
		}

		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		windowName := p.Name
		if !m.tmux.WindowExists(windowName) {
			return statusMsgEvent("No session for " + windowName)
		}

		if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to attach: %v", err))
		}

		return statusMsgEvent("Attached to " + windowName)
	}
}

func Run(projects []project.Project, tmuxSession, socketPath, stateFilePath string) error {
	var tc *ttmux.Client
	if ttmux.IsInsideTmux() {
		// Use the actual current session — the user may be in a session
		// with a different name than the configured one.
		session := ttmux.CurrentSessionName()
		if session == "" {
			session = tmuxSession
		}
		tc = ttmux.NewClient(session)
	}

	// Start notification server
	ns := notify.NewServer(socketPath)
	if err := ns.Start(); err != nil {
		// Non-fatal: continue without notifications
		ns = nil
	}
	if ns != nil {
		defer ns.Stop()
	}

	m := NewModel(projects, tc, ns, stateFilePath)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()

	// Clean up state file on exit
	state.Remove(stateFilePath)

	return err
}
