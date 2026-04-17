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
	"github.com/charmbracelet/bubbles/textinput"
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
	// detailCursor is a single index into the combined list:
	// [recent sessions..., visible worktrees...]. Down-arrow flows naturally
	// from sessions into worktrees.
	detailCursor int
	// Resume-confirmation prompt: non-empty means we're asking the user whether
	// to disconnect the currently-running session and resume this one instead.
	pendingResumeSessionID string
	// worktreeInput is non-nil when the user is entering a branch name for a
	// new worktree. While set, key events route to the text input.
	worktreeInput *textinput.Model
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

		// New-worktree input captures all input while active.
		if m.worktreeInput != nil && m.screen == ScreenProject {
			switch msg.String() {
			case "enter":
				branch := strings.TrimSpace(m.worktreeInput.Value())
				m.worktreeInput = nil
				if branch == "" {
					return m, nil
				}
				return m, m.createWorktreeAndLaunch(branch)
			case "esc", "escape":
				m.worktreeInput = nil
				return m, nil
			}
			updated, cmd := m.worktreeInput.Update(msg)
			m.worktreeInput = &updated
			return m, cmd
		}

		// Resume-confirmation prompt captures all input while active.
		if m.pendingResumeSessionID != "" && m.screen == ScreenProject {
			switch msg.String() {
			case "y", "Y", "enter":
				sessionID := m.pendingResumeSessionID
				m.pendingResumeSessionID = ""
				return m, m.disconnectAndResumeSession(sessionID)
			case "n", "N", "esc", "escape":
				m.pendingResumeSessionID = ""
				return m, nil
			}
			return m, nil
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
					m.detailCursor = 0
					m.screen = ScreenProject
				}
				return m, nil
			}
			if m.screen == ScreenProject {
				sessionN := len(m.detailRecentSessions)
				worktrees := m.visibleWorktrees()
				// Session row selected: resume with confirm-if-conflict flow.
				if m.detailCursor < sessionN {
					if sessionN == 0 {
						return m, nil
					}
					selectedID := m.detailRecentSessions[m.detailCursor].SessionID
					if m.tmux != nil && m.detailProject != nil && m.tmux.WindowExists(m.detailProject.Name) {
						if m.detailSession == nil || m.detailSession.SessionID != selectedID {
							m.pendingResumeSessionID = selectedID
							return m, nil
						}
					}
					return m, m.resumeSpecificSession(selectedID)
				}
				// Worktree row selected: launch or switch to its session window.
				wtIdx := m.detailCursor - sessionN
				if wtIdx >= 0 && wtIdx < len(worktrees) {
					return m, m.launchWorktreeSession(worktrees[wtIdx])
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

		case key.Matches(msg, keys.NewWorktree):
			if m.screen == ScreenProject && m.detailProject != nil {
				ti := textinput.New()
				ti.Placeholder = "new branch name"
				ti.Focus()
				ti.CharLimit = 128
				ti.Width = 40
				m.worktreeInput = &ti
				return m, textinput.Blink
			}

		case key.Matches(msg, keys.Restart):
			self, err := os.Executable()
			if err == nil {
				// Restart all sidebar panes first
				m.restartSidebars()
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

	case worktreeCreatedMsg:
		if m.detailProject != nil {
			m.detailWorktrees, _ = project.ListWorktrees(m.detailProject.Path)
		}
		status := msg.status
		return m, func() tea.Msg { return statusMsgEvent(status) }
	}

	switch m.screen {
	case ScreenDashboard:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	case ScreenProject:
		// Handle up/down across the unified [sessions..., worktrees...] list.
		if msg, ok := msg.(tea.KeyMsg); ok {
			n := m.detailCombinedLen()
			if n > 0 {
				switch msg.String() {
				case "up", "k":
					m.detailCursor = (m.detailCursor - 1 + n) % n
				case "down", "j":
					m.detailCursor = (m.detailCursor + 1) % n
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
			{"w", "Create new git worktree + session"},
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

	sessionN := len(m.detailRecentSessions)
	worktrees := m.visibleWorktrees()

	// Recent sessions
	b.WriteString(headerStyle.Render("Sessions") + "\n")
	if sessionN == 0 {
		b.WriteString("  " + footerDescStyle.Render("No sessions found") + "\n")
	} else {
		for i, rs := range m.detailRecentSessions {
			selected := m.detailCursor == i
			cursor := "  "
			if selected {
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

			if selected {
				b.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				b.WriteString(normalItemStyle.Render(line) + "\n")
			}
		}
	}
	b.WriteString("\n")

	// Worktrees (main checkout filtered out — it's the project itself)
	b.WriteString(headerStyle.Render(fmt.Sprintf("Worktrees (%d)", len(worktrees))) + "\n")
	if len(worktrees) == 0 {
		b.WriteString("  " + footerDescStyle.Render("none") + "\n")
	} else {
		for i, wt := range worktrees {
			selected := m.detailCursor == sessionN+i
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			branch := wt.Branch
			if branch == "" && len(wt.HEAD) >= 8 {
				branch = "(detached " + wt.HEAD[:8] + ")"
			}
			line := fmt.Sprintf("%s%s  %s", cursor, branch, footerDescStyle.Render(wt.Path))
			if selected {
				b.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				b.WriteString(normalItemStyle.Render(line) + "\n")
			}
		}
	}

	// Status message
	if m.statusMsg != "" {
		b.WriteString("\n" + notifBadgeStyle.Render(m.statusMsg) + "\n")
	}

	// Footer — replaced by a prompt when a pending resume confirm or worktree
	// input is active.
	var footer string
	switch {
	case m.worktreeInput != nil:
		question := fmt.Sprintf("Create new worktree for %s — branch name:", p.Name)
		footer = m.renderInputPrompt(question, m.worktreeInput.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	case m.pendingResumeSessionID != "":
		question := fmt.Sprintf("A session is already running for %s. Disconnect it and start the selected session?", p.Name)
		footer = m.renderPrompt(question, []footerBinding{
			{"y", "yes"},
			{"n", "no"},
		})
	default:
		footer = m.renderFooter([]footerBinding{
			{"↑↓", "select"},
			{"enter", "open"},
			{"n", "new session"},
			{"a", "attach"},
			{"w", "new worktree"},
			{"esc", "back"},
			{"?", "help"},
		})
	}

	// Pad content to fill screen, then add footer
	content := b.String()
	contentLines := strings.Count(content, "\n")
	footerLines := 3
	if contentLines < m.height-footerLines {
		content += strings.Repeat("\n", m.height-footerLines-contentLines)
	}

	return content + footer
}

// visibleWorktrees returns worktrees shown in the detail view — all known
// worktrees except the main checkout (whose path matches the project's path).
func (m Model) visibleWorktrees() []project.Worktree {
	if m.detailProject == nil {
		return nil
	}
	out := make([]project.Worktree, 0, len(m.detailWorktrees))
	for _, wt := range m.detailWorktrees {
		if wt.Path == m.detailProject.Path {
			continue
		}
		out = append(out, wt)
	}
	return out
}

// detailCombinedLen is the number of rows navigable in the project detail view
// (sessions + visible worktrees), used for cursor bounds.
func (m Model) detailCombinedLen() int {
	return len(m.detailRecentSessions) + len(m.visibleWorktrees())
}

// renderPrompt renders a two-row confirmation bar: the question on top, key
// hints on the bottom. Uses the same styling as the normal footer.
func (m Model) renderPrompt(question string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + footerDescStyle.Render(hints)
	return footerStyle.Width(m.width).Render(body)
}

// renderInputPrompt renders a three-row input bar: the question, a text-input
// line, and key hints. Used by the new-worktree flow.
func (m Model) renderInputPrompt(question, inputView string, bindings []footerBinding) string {
	var parts []string
	for _, b := range bindings {
		k := footerKeyStyle.Render(b.key)
		d := footerDescStyle.Render(b.desc)
		parts = append(parts, k+":"+d)
	}
	hints := strings.Join(parts, "  ")
	body := footerKeyStyle.Render(question) + "\n" + inputView + "\n" + footerDescStyle.Render(hints)
	return footerStyle.Width(m.width).Render(body)
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

// restartSidebars sends ctrl+r to all sidebar panes (pane .1 in each window)
// so they reload the new binary.
func (m Model) restartSidebars() {
	if m.tmux == nil {
		return
	}
	windows, err := m.tmux.ListWindows()
	if err != nil {
		return
	}
	for _, w := range windows {
		if w.Index == "0" {
			continue // skip the TUI window itself
		}
		target := fmt.Sprintf("%s:%s.1", m.tmux.SessionName, w.Index)
		// Send ctrl+r to the sidebar pane — its own handler will exec the new binary
		m.tmux.SendRawKeys(target, "C-r")
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

// worktreeCreatedMsg signals that a new worktree was created; Update refreshes
// detailWorktrees and surfaces the carried status string.
type worktreeCreatedMsg struct{ status string }

// currentProject returns the project for the current context —
// detailProject on the project detail screen, list selection on the dashboard.
func (m Model) currentProject() *project.Project {
	if m.detailProject != nil && m.screen == ScreenProject {
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
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchClaudeInWindow(windowName, p.Path, "claude")
	}
}

// launchClaudeInWindow creates a new tmux window for the given name + cwd, sends
// the given shell command (typically "claude" or "claude --resume <id>"),
// attaches the sidebar pane, and switches focus. Returns a statusMsgEvent.
func (m Model) launchClaudeInWindow(windowName, cwd, shellCmd string) tea.Msg {
	target, err := m.tmux.CreateWindow(windowName, cwd)
	if err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to create window: %v", err))
	}
	if err := m.tmux.SendKeys(target, shellCmd); err != nil {
		return statusMsgEvent(fmt.Sprintf("Failed to launch claude: %v", err))
	}
	m.addSidebarPane(target)
	if err := m.tmux.SwitchToWindow(target); err != nil {
		return statusMsgEvent(fmt.Sprintf("Launched but failed to switch: %v", err))
	}
	return statusMsgEvent("Launched Claude in " + windowName)
}

// launchWorktreeSession opens a Claude session in the given worktree's directory.
// Uses `<project>@<branch>` as the tmux window name. If the window already
// exists, switches to it rather than launching a duplicate.
func (m Model) launchWorktreeSession(wt project.Worktree) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		branch := wt.Branch
		if branch == "" {
			if len(wt.HEAD) >= 8 {
				branch = wt.HEAD[:8]
			} else {
				branch = "detached"
			}
		}
		windowName := p.Name + "@" + branch
		if m.tmux.WindowExists(windowName) {
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				return statusMsgEvent(fmt.Sprintf("Failed to switch: %v", err))
			}
			return statusMsgEvent("Switched to " + windowName)
		}
		return m.launchClaudeInWindow(windowName, wt.Path, "claude")
	}
}

// createWorktreeAndLaunch creates a new git worktree for the branch (creating
// the branch if needed) and launches a Claude session in it. Emits a
// worktreeCreatedMsg so Update can refresh detailWorktrees.
func (m Model) createWorktreeAndLaunch(branch string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}
		wtPath, err := project.CreateWorktree(p.Path, branch)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Worktree failed: %v", err))
		}
		windowName := p.Name + "@" + branch
		var status string
		if m.tmux.WindowExists(windowName) {
			if err := m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName); err != nil {
				status = fmt.Sprintf("Worktree created but failed to switch: %v", err)
			} else {
				status = "Switched to " + windowName
			}
		} else {
			launch := m.launchClaudeInWindow(windowName, wtPath, "claude")
			if se, ok := launch.(statusMsgEvent); ok {
				status = string(se)
			}
		}
		return worktreeCreatedMsg{status: status}
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
		return m.launchResumeInWindow(windowName, p.Path, sessionID)
	}
}

// disconnectAndResumeSession kills the project's existing tmux window (along
// with any Claude process and sidebar pane running in it) and starts the
// selected session fresh.
func (m Model) disconnectAndResumeSession(sessionID string) tea.Cmd {
	return func() tea.Msg {
		p := m.detailProject
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		windowName := p.Name
		// Kill the existing window; ignore error if it's already gone.
		_ = m.tmux.KillWindow(m.tmux.SessionName + ":" + windowName)
		return m.launchResumeInWindow(windowName, p.Path, sessionID)
	}
}

// launchResumeInWindow creates a fresh tmux window for the project, runs
// `claude --resume <id>` in it, attaches a sidebar pane, and switches focus.
// Returns a statusMsgEvent describing the outcome.
func (m Model) launchResumeInWindow(windowName, projectPath, sessionID string) tea.Msg {
	target, err := m.tmux.CreateWindow(windowName, projectPath)
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

func (m Model) openTerminal() tea.Cmd {
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
			return statusMsgEvent("No session window for " + windowName + ". Start a session first.")
		}

		target := fmt.Sprintf("%s:%s.0", m.tmux.SessionName, windowName)
		paneID, err := m.tmux.SplitWindowHorizontal(target, p.Path)
		if err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to open terminal: %v", err))
		}

		m.tmux.SelectPane(paneID)
		m.tmux.SwitchToWindow(m.tmux.SessionName + ":" + windowName)

		return statusMsgEvent("Opened terminal in " + windowName)
	}
}

func (m Model) openPopup() tea.Cmd {
	return func() tea.Msg {
		p := m.currentProject()
		if p == nil {
			return statusMsgEvent("No project selected")
		}
		if m.tmux == nil {
			return statusMsgEvent("tmux not available")
		}

		title := fmt.Sprintf(" %s ", p.Name)
		if err := m.tmux.Popup(p.Path, title); err != nil {
			return statusMsgEvent(fmt.Sprintf("Failed to open popup: %v", err))
		}
		return nil
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
		// Ensure mouse support is enabled on this session every launch.
		// Covers fresh Linux installs where no global `set -g mouse on` exists.
		tc.EnableMouse()
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
