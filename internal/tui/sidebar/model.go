package sidebar

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"github.com/rvanmech/unky-mo/internal/usage"
)

type SidebarItem struct {
	Name       string
	Path       string // project directory path
	WindowName string // tmux window target; empty for Home (window 0)
	Status     string // "none", "active", "idle", "permission", "external"
	Parent     string // non-empty for worktree entries (parent project name)
	Section    string // "projects" (default) or "external" — groups stray sessions
	Branch     string // git branch (populated for git-backed strays)
	Dirty      int    // dirty file count (populated for git-backed strays)
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
	// Sync status: "synced", "stale", or "" (not synced / no sync repo)
	syncStatus string
	// Active Claude shells (Bash tool subprocesses)
	activeShells []claude.ActiveShell
	// Changed files (from git status)
	changedFiles   []string // raw file paths from git status --porcelain
	changedAdded   int      // total lines added
	changedRemoved int      // total lines removed
	// Focus section: "sessions", "shells", or "files"
	focusSection string
	shellCursor  int
	fileCursor   int
	// Claude usage snapshot (populated from state file; nil until main TUI
	// has fetched at least once).
	usage *state.UsageState
	// Cumulative tokens for the live Claude session in this window; 0 when
	// no session is currently running here.
	sessionTokens int
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
		focusSection:  "sessions",
	}
	m.items = append(m.items, SidebarItem{
		Name:   "Unky Mo Home",
		IsHome: true,
	})
	m.refreshState()
	m.refreshSyncStatus()
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
		// Update sync status after a successful push
		if strings.HasPrefix(string(msg), "synced ") {
			m.syncStatus = "synced"
		}
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
			if m.focusSection == "files" {
				if m.fileCursor > 0 {
					m.fileCursor--
				} else if len(m.activeShells) > 0 {
					m.focusSection = "shells"
					m.shellCursor = len(m.activeShells) - 1
				} else {
					m.focusSection = "sessions"
					if len(m.items) > 0 {
						m.cursor = len(m.items) - 1
						m.ensureCursorVisible()
					}
				}
			} else if m.focusSection == "shells" {
				if m.shellCursor > 0 {
					m.shellCursor--
				} else {
					m.focusSection = "sessions"
					if len(m.items) > 0 {
						m.cursor = len(m.items) - 1
						m.ensureCursorVisible()
					}
				}
			} else if len(m.items) > 0 {
				m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				if m.items[m.cursor].IsHeader {
					m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
				}
				m.ensureCursorVisible()
			}
		case "down", "j":
			if m.focusSection == "files" {
				if m.fileCursor < len(m.changedFiles)-1 {
					m.fileCursor++
				} else {
					m.focusSection = "sessions"
					m.cursor = 0
					m.ensureCursorVisible()
				}
			} else if m.focusSection == "shells" {
				if m.shellCursor < len(m.activeShells)-1 {
					m.shellCursor++
				} else if len(m.changedFiles) > 0 {
					m.focusSection = "files"
					m.fileCursor = 0
				} else {
					m.focusSection = "sessions"
					m.cursor = 0
					m.ensureCursorVisible()
				}
			} else if len(m.items) > 0 {
				next := (m.cursor + 1) % len(m.items)
				if m.items[next].IsHeader {
					next = (next + 1) % len(m.items)
				}
				// If we wrapped around, enter shells or files section
				if next <= m.cursor {
					if len(m.activeShells) > 0 {
						m.focusSection = "shells"
						m.shellCursor = 0
					} else if len(m.changedFiles) > 0 {
						m.focusSection = "files"
						m.fileCursor = 0
					}
				} else {
					m.cursor = next
					m.ensureCursorVisible()
				}
			}
		case "enter":
			if m.focusSection == "shells" && m.shellCursor < len(m.activeShells) {
				return m, m.showShellOutput(m.activeShells[m.shellCursor])
			}
			if m.focusSection == "files" && m.fileCursor < len(m.changedFiles) {
				return m, m.showDiffPopup(m.changedFiles[m.fileCursor])
			}
			return m, m.handleEnter()
		case "d":
			if m.focusSection == "files" && m.fileCursor < len(m.changedFiles) {
				return m, m.showDiffPopup(m.changedFiles[m.fileCursor])
			}
		case "v":
			if m.focusSection == "files" && m.fileCursor < len(m.changedFiles) {
				return m, m.openFileInEditor(m.changedFiles[m.fileCursor])
			}
		case "o":
			if m.focusSection == "files" && m.fileCursor < len(m.changedFiles) {
				return m, m.openFileExternal(m.changedFiles[m.fileCursor])
			}
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
			// Force an immediate state refresh instead of waiting for the 1s tick.
			m.refreshState()
			return m, func() tea.Msg { return sidebarStatusMsg("Refreshed") }
		case "alt+ctrl+r":
			// Bubbletea stringifies physical ctrl+alt+r as "alt+ctrl+r".
			// Restart the sidebar process so it picks up a freshly-installed binary.
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
	b.WriteString(renderSectionHeader("Sessions", m.width) + "\n")

	// Calculate visible range
	headerLines := 1
	footerLines := 5
	if m.usage != nil {
		footerLines += 3 // rule + usage line + rule
	}
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
			// Section header — not selectable; rendered as a full-width
			// centered title with extending rule like the top "Sessions".
			b.WriteString(renderSectionHeader(item.Name, m.width) + "\n")
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
			} else if item.Status == "external" {
				suffix = " " + dotExternal.Render("ext")
			}
			// Git-backed strays carry branch info; show it so the row is
			// distinguishable from other repos of the same basename.
			if item.Branch != "" {
				info := item.Branch
				if item.Dirty > 0 {
					info += fmt.Sprintf(" *%d", item.Dirty)
				}
				suffix = " " + normalStyle.Render(info) + suffix
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

	// Active shells section
	contentLines := strings.Count(b.String(), "\n")
	remaining := m.height - contentLines - footerLines
	if m.statusMsg != "" {
		remaining-- // reserve a line for status
	}

	if len(m.activeShells) > 0 && remaining > 3 {
		b.WriteString("\n")
		b.WriteString(headerStyle.Render(fmt.Sprintf("Shells (%d)", len(m.activeShells))) + "\n")
		for i, sh := range m.activeShells {
			isFocused := m.focusSection == "shells" && m.shellCursor == i
			display := claude.FormatShellCommand(sh.Command, m.width-6)
			cursor := " "
			if isFocused {
				cursor = "▸"
			}
			if isFocused {
				b.WriteString(cursor + dotIdle.Render("●") + " " + selectedStyle.Render(display) + "\n")
			} else {
				b.WriteString(cursor + dotIdle.Render("●") + " " + normalStyle.Render(display) + "\n")
			}
		}
		// Recalculate remaining
		contentLines = strings.Count(b.String(), "\n")
		remaining = m.height - contentLines - footerLines
		if m.statusMsg != "" {
			remaining--
		}
	}

	// Changed files tree
	if len(m.changedFiles) > 0 && remaining > 3 {
		maxFileLines := remaining - 2
		if maxFileLines > 0 {
			b.WriteString("\n")
			noun := "files"
			if len(m.changedFiles) == 1 {
				noun = "file"
			}
			stats := fmt.Sprintf("%d %s", len(m.changedFiles), noun)
			if m.changedAdded > 0 || m.changedRemoved > 0 {
				stats += " " + dotIdle.Render(fmt.Sprintf("+%d", m.changedAdded)) + " " + dotPermission.Render(fmt.Sprintf("-%d", m.changedRemoved))
			}
			b.WriteString(headerStyle.Render("Changed") + " " + stats + "\n")

			// Build tree lines with file index mapping
			treeLines := buildFileTreeLines(m.changedFiles)
			rendered := 0
			for _, tl := range treeLines {
				if rendered >= maxFileLines {
					more := len(m.changedFiles) - rendered
					if more > 0 {
						b.WriteString(footerStyle.Render(fmt.Sprintf("  +%d more", more)) + "\n")
						rendered++
					}
					break
				}
				isFocused := m.focusSection == "files" && tl.fileIndex >= 0 && m.fileCursor == tl.fileIndex
				if isFocused {
					b.WriteString(selectedStyle.Render("▸"+tl.indent+tl.display) + "\n")
				} else if tl.fileIndex >= 0 {
					b.WriteString(normalStyle.Render(" "+tl.indent+tl.display) + "\n")
				} else {
					// Directory line (not selectable)
					b.WriteString(footerStyle.Render(" "+tl.indent+tl.display+"/") + "\n")
				}
				rendered++
			}
			remaining -= rendered + 2
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

	// Claude usage line — 5h bar with reset countdown. Rendered just above the
	// keybinding footer so the footer stays anchored at the bottom edge.
	// Wrapped in white horizontal rules to visually separate it from the
	// changed-files tree above and the keybinding footer below.
	if line := m.renderUsageLine(); line != "" {
		rule := usageRuleStyle.Render(strings.Repeat("─", m.width))
		b.WriteString(rule + "\n")
		b.WriteString(line + "\n")
		b.WriteString(rule + "\n")
	}

	// Footer — context-sensitive
	if m.focusSection == "shells" && len(m.activeShells) > 0 {
		b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎ view output") + "\n")
		b.WriteString(footerStyle.Render(" ` popup  s sync") + "\n")
		b.WriteString(footerStyle.Render(" ^r refresh"))
	} else if m.focusSection == "files" && len(m.changedFiles) > 0 {
		b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎/d diff") + "\n")
		b.WriteString(footerStyle.Render(" v edit   o open") + "\n")
		b.WriteString(footerStyle.Render(" ` popup  s sync") + "\n")
		b.WriteString(footerStyle.Render(" ^r refresh"))
	} else {
		b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎ select") + "\n")
		b.WriteString(footerStyle.Render(" t drawer T +term") + "\n")
		syncLabel := "  s sync"
		switch m.syncStatus {
		case "synced":
			syncLabel = "  s " + dotIdle.Render("synced")
		case "stale":
			syncLabel = "  s " + dotActive.Render("sync ↑")
		}
		b.WriteString(footerStyle.Render(" ⇥ next   x close") + "\n")
		b.WriteString(footerStyle.Render(" ` popup") + syncLabel + "\n")
		b.WriteString(footerStyle.Render(" ^r refresh"))
	}

	return b.String()
}

// renderSectionHeader renders "────── Label ──────" centered in width,
// used for every top-level section label in the sidebar.
func renderSectionHeader(label string, width int) string {
	inner := " " + label + " "
	if width <= len(inner)+2 {
		return headerStyle.Render("── " + label + " ──")
	}
	total := width - len(inner)
	left := total / 2
	right := total - left
	return headerStyle.Render(strings.Repeat("─", left) + inner + strings.Repeat("─", right))
}

func renderDot(status string) string {
	switch status {
	case "active":
		return dotActive.Render("●")
	case "idle":
		return dotIdle.Render("●")
	case "permission":
		return dotPermission.Render("●")
	case "external":
		return dotExternal.Render("●")
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
	if m.usage != nil {
		footerLines += 3
	}
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

// renderUsageLine returns the sidebar's 5h rate-limit bar + reset countdown,
// optionally followed by the current session's token count when a Claude
// session is running in this window and the sidebar has room for it.
// Returns "" when usage data isn't yet available or the sidebar is too narrow.
func (m Model) renderUsageLine() string {
	if m.usage == nil {
		return ""
	}
	if m.usage.AuthError {
		return usageLineStyle.Render("usage: run `claude`")
	}
	// Minimum useful width: "5h ▓▓▓▓▓▓ 52% (2h15m)" ≈ 22 chars.
	if m.width < 22 {
		return ""
	}

	// Width budget:
	//   bar-only layout:  "5h  NN% (HhMMm)"         ≈ 15 fixed chars
	//   with tokens:      "5h  NN% (HhMMm) · X.XM tok" ≈ 26 fixed chars
	// Fall back to bar-only when the sidebar is too narrow for both.
	showTokens := m.sessionTokens > 0 && m.width >= 32

	var barW int
	if showTokens {
		barW = m.width - 27
		if barW < 5 {
			barW = 5
		}
		if barW > 14 {
			barW = 14
		}
	} else {
		barW = m.width - 16
		if barW < 6 {
			barW = 6
		}
		if barW > 18 {
			barW = 18
		}
	}

	filled, empty := usage.SplitBar(float64(m.usage.FiveHourPct), barW)
	filledColored := pickBarStyle(m.usage.FiveHourPct).Render(filled) + usageBarEmpty.Render(empty)

	resets := usage.FormatResetIn(time.Now(), m.usage.FiveHourResetsAt)
	out := fmt.Sprintf("5h %s %d%% (%s)", filledColored, m.usage.FiveHourPct, resets)
	if showTokens {
		out += " · " + usage.FormatTokensShort(m.sessionTokens) + " tok"
	}
	if m.usage.Stale {
		out += " *"
	}
	return usageLineStyle.Render(out)
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

	// Rebuild items: Home + projects section + optional External section.
	// Group into two passes so the external header only renders when there
	// is at least one stray, and so external rows always sit below projects.
	items := []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}
	var projectItems, externalItems []SidebarItem
	for _, p := range sf.Projects {
		if p.Status == "none" {
			continue
		}
		row := SidebarItem{
			Name:       p.Name,
			Path:       p.Path,
			WindowName: p.WindowName,
			Status:     p.Status,
			Parent:     p.Parent,
			Section:    p.Section,
			Branch:     p.Branch,
			Dirty:      p.Dirty,
		}
		if p.Section == "external" {
			externalItems = append(externalItems, row)
		} else {
			projectItems = append(projectItems, row)
		}
	}
	items = append(items, projectItems...)
	if len(externalItems) > 0 {
		items = append(items, SidebarItem{Name: "External", IsHeader: true, Section: "external"})
		items = append(items, externalItems...)
	}

	m.items = items
	m.usage = sf.Usage
	m.sessionTokens = 0
	if live := m.ownWindowSession(); live != nil {
		jsonl := filepath.Join(claude.ProjectsDirForPath(m.windowPath), live.SessionID+".jsonl")
		m.sessionTokens = usage.SessionTokens(jsonl)
	}
	m.refreshTerminals()
	m.refreshChangedFiles()
	m.activeShells = claude.ActiveShellsForSession(m.windowPath)
	// Sync status is checked on init and after push, not every tick
	// (moSync.List does git pull which is too slow for 1s polling)

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

// ownWindowSession returns the Claude session running in this sidebar's own
// tmux window, picked by matching session PIDs against the window's pane
// PIDs. This is what distinguishes concurrent sessions sharing a CWD — plain
// path lookup (SessionForPath) returns an arbitrary first match.
func (m *Model) ownWindowSession() *claude.Session {
	candidates := claude.SessionsForPath(m.windowPath)
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return &candidates[0]
	}
	if m.windowName == "" || m.tmux == nil {
		return &candidates[0]
	}
	target := fmt.Sprintf("%s:%s", m.tmux.SessionName, m.windowName)
	panePIDs, err := m.tmux.WindowPanePIDs(target)
	if err != nil || len(panePIDs) == 0 {
		return &candidates[0]
	}
	for i := range candidates {
		if claude.IsDescendantOf(candidates[i].PID, panePIDs) {
			return &candidates[i]
		}
	}
	return &candidates[0]
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

	// Get line-level stats
	m.changedAdded = 0
	m.changedRemoved = 0
	numstat, err := exec.Command("git", "-C", m.windowPath, "diff", "--numstat").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(numstat)), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] != "-" {
				var added, removed int
				fmt.Sscan(parts[0], &added)
				fmt.Sscan(parts[1], &removed)
				m.changedAdded += added
				m.changedRemoved += removed
			}
		}
	}
}

// fileTreeLine is a single rendered line of the file tree.
type fileTreeLine struct {
	display   string // the text to show (filename or directory name)
	indent    string // leading spaces
	fileIndex int    // index into changedFiles (-1 for directory nodes)
}

// buildFileTreeLines creates an indented tree from file paths.
// Directory nodes that have a single child directory are collapsed.
func buildFileTreeLines(files []string) []fileTreeLine {
	type treeNode struct {
		name     string
		children map[string]*treeNode
		order    []string
		fileIdx  int // index into files, -1 for dirs
	}

	root := &treeNode{children: make(map[string]*treeNode), fileIdx: -1}

	for i, file := range files {
		parts := strings.Split(file, "/")
		cur := root
		for j, part := range parts {
			if _, ok := cur.children[part]; !ok {
				child := &treeNode{
					name:     part,
					children: make(map[string]*treeNode),
					fileIdx:  -1,
				}
				if j == len(parts)-1 {
					child.fileIdx = i
				}
				cur.children[part] = child
				cur.order = append(cur.order, part)
			}
			cur = cur.children[part]
		}
	}

	// Collapse single-child directories
	var collapse func(n *treeNode) *treeNode
	collapse = func(n *treeNode) *treeNode {
		for _, name := range n.order {
			n.children[name] = collapse(n.children[name])
		}
		if n.fileIdx < 0 && len(n.children) == 1 {
			childName := n.order[0]
			child := n.children[childName]
			if child.fileIdx < 0 {
				merged := &treeNode{
					name:     n.name + "/" + child.name,
					children: child.children,
					order:    child.order,
					fileIdx:  -1,
				}
				return merged
			}
		}
		return n
	}
	for _, name := range root.order {
		root.children[name] = collapse(root.children[name])
	}

	// Flatten to lines
	var lines []fileTreeLine
	var walk func(n *treeNode, indent string)
	walk = func(n *treeNode, indent string) {
		for _, name := range n.order {
			child := n.children[name]
			if child.fileIdx >= 0 {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: child.fileIdx,
				})
			} else {
				lines = append(lines, fileTreeLine{
					display:   child.name,
					indent:    indent,
					fileIndex: -1,
				})
				walk(child, indent+"  ")
			}
		}
	}
	walk(root, " ")

	return lines
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

func (m *Model) refreshSyncStatus() {
	m.syncStatus = ""
	if m.windowName == "" || m.windowPath == "" {
		return
	}
	syncDir := moSync.DefaultSyncDir()
	// Read local sync repo metadata without network (no git pull)
	sessions, err := moSync.ListLocal(syncDir)
	if err != nil {
		return
	}
	for _, s := range sessions {
		if s.ProjectName == m.windowName {
			// Found a synced session — check if local JSONL is newer
			localDir := claude.ProjectsDirForPath(m.windowPath)
			localPath := localDir + "/" + s.SessionID + ".jsonl"
			info, err := os.Stat(localPath)
			if err != nil {
				m.syncStatus = "stale"
				return
			}
			if info.ModTime().After(s.PushedAt) {
				m.syncStatus = "stale"
			} else {
				m.syncStatus = "synced"
			}
			return
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

func (m Model) showShellOutput(shell claude.ActiveShell) tea.Cmd {
	if shell.OutputFile == "" {
		return func() tea.Msg { return sidebarStatusMsg("no output file found") }
	}

	// Build a header script that shows status info then tails the output
	displayCmd := claude.FormatShellCommand(shell.Command, 60)
	script := fmt.Sprintf(
		`echo "Shell details"; echo ""; echo "Status: running"; echo "Command: %s"; echo "Output:"; echo ""; tail -f %s`,
		displayCmd, shell.OutputFile,
	)

	title := fmt.Sprintf(" Shell: %s ", claude.FormatShellCommand(shell.Command, 30))
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "95%", "-h", "95%",
		"-T", title,
		"sh", "-c", script)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("shell view err: %v", err))
		}
		return nil
	})
}

func (m Model) showDiffPopup(file string) tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	title := fmt.Sprintf(" diff: %s ", filepath.Base(file))
	diffCmd := fmt.Sprintf("git diff --color=always HEAD -- %s | less -R", file)
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "95%", "-h", "95%",
		"-d", m.windowPath,
		"-T", title,
		"sh", "-c", diffCmd)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("diff err: %v", err))
		}
		return nil
	})
}

func (m Model) openFileInEditor(file string) tea.Cmd {
	if m.windowPath == "" {
		return func() tea.Msg { return sidebarStatusMsg("no project path") }
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	absPath := filepath.Join(m.windowPath, file)
	title := fmt.Sprintf(" %s ", filepath.Base(file))
	c := exec.Command("tmux", "display-popup", "-E",
		"-w", "90%", "-h", "90%",
		"-d", m.windowPath,
		"-T", title,
		editor, absPath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return sidebarStatusMsg(fmt.Sprintf("editor err: %v", err))
		}
		return nil
	})
}

func (m Model) openFileExternal(file string) tea.Cmd {
	absPath := filepath.Join(m.windowPath, file)
	// Try VS Code first, fall back to macOS open
	return func() tea.Msg {
		if err := exec.Command("code", "--goto", absPath+":1:1").Run(); err == nil {
			return sidebarStatusMsg("opened in VS Code")
		}
		if err := exec.Command("open", absPath).Run(); err == nil {
			return sidebarStatusMsg("opened in default editor")
		}
		return sidebarStatusMsg("no external editor found")
	}
}

func (m Model) syncPush() tea.Cmd {
	return func() tea.Msg {
		if m.windowName == "" || m.windowPath == "" {
			return sidebarStatusMsg("no project")
		}
		live := claude.SessionForPath(m.windowPath)
		if live == nil {
			return sidebarStatusMsg("no live session to sync")
		}
		syncDir := moSync.DefaultSyncDir()
		if err := moSync.Push(m.windowName, m.windowPath, syncDir, live.SessionID); err != nil {
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
