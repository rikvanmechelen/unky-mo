package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/tickets"
)

// ticketStartStrategy names the start-working path chosen for a ticket.
type ticketStartStrategy string

const (
	strategyMissing     ticketStartStrategy = ""         // project missing on disk / unusable
	strategyFocusExisting ticketStartStrategy = "focus"  // existing worktree for the branch
	strategyWorktree    ticketStartStrategy = "worktree" // create a new worktree
	strategyMainCheckout ticketStartStrategy = "main"    // checkout branch in main repo
)

// decideTicketStrategy is the pure decision function used by both the hint
// renderer and the action handler. Keeping the inputs as plain bools (rather
// than a Model) makes it trivially testable.
//
//   - projectOnDisk=false       → strategyMissing (stale mapping / deleted project)
//   - existingWorktreePath!=""  → strategyFocusExisting (focus or launch in it)
//   - hasSession || dirty       → strategyWorktree (main is busy or unsafe to touch)
//   - otherwise                 → strategyMainCheckout (clean, empty slate)
func decideTicketStrategy(projectOnDisk, hasSession, dirty bool, existingWorktreePath string) ticketStartStrategy {
	if !projectOnDisk {
		return strategyMissing
	}
	if existingWorktreePath != "" {
		return strategyFocusExisting
	}
	if hasSession || dirty {
		return strategyWorktree
	}
	return strategyMainCheckout
}

// ticketDetailMsg carries the result of a Provider.Detail call.
type ticketDetailMsg struct {
	detail *tickets.TicketDetail
	err    error
}

// fetchTicketDetail finds the provider for the given ticket (by name) and
// runs a single Detail lookup. 15s timeout mirrors the search fetch.
func (m Model) fetchTicketDetail(id, providerName string) tea.Cmd {
	providers := m.ticketsProviders
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, p := range providers {
			if p.Name() != providerName {
				continue
			}
			d, err := p.Detail(ctx, id)
			return ticketDetailMsg{detail: d, err: err}
		}
		return ticketDetailMsg{err: fmt.Errorf("no provider matching %q for ticket %s", providerName, id)}
	}
}

// resolvedMoProjectForTicket returns the Mo project name mapped to a Jira
// project key, checking the user's [[tickets.jira]] config map first
// (authoritative) then the companion file (picker-saved). Empty string
// means "no mapping yet — picker needed".
func (m Model) resolvedMoProjectForTicket(t *tickets.Ticket) string {
	if t == nil {
		return ""
	}
	// Config-level map wins.
	for _, inst := range m.ticketsInstances {
		// We don't filter by provider name here — jira.Instance entries are
		// by definition Jira-sourced; once we add other providers, gate on
		// the provider kind accordingly.
		if v, ok := inst.ProjectMap[t.ProjectKey]; ok && v != "" {
			return v
		}
	}
	// Fallback: companion file.
	if m.ticketProjectMap != nil {
		if inner, ok := m.ticketProjectMap[t.Provider]; ok {
			if v := inner[t.ProjectKey]; v != "" {
				return v
			}
		}
	}
	return ""
}

// renderTicketDetailScreen builds the full-screen ticket view. Width/height
// fallbacks mirror dashboardView's defaults.
func (m Model) renderTicketDetailScreen() string {
	width := m.width
	if width == 0 {
		width = 120
	}
	height := m.height
	if height == 0 {
		height = 30
	}

	var body strings.Builder

	// Header — matches the ← prefix used by the project detail view.
	header := "← "
	if m.detailTicketList != nil {
		header += m.detailTicketList.ID + "  " + m.detailTicketList.Title
	} else if m.detailTicket != nil {
		header += m.detailTicket.ID + "  " + m.detailTicket.Title
	} else {
		header += "Ticket"
	}
	body.WriteString(titleStyle.Render(header) + "\n\n")

	// Metadata grid — two columns so the popup stays compact.
	body.WriteString(m.renderTicketMeta(width))
	body.WriteString("\n")

	// Description.
	body.WriteString(headerStyle.Render("Description") + "\n")
	body.WriteString(footerDescStyle.Render(strings.Repeat("─", clamp(width-4, 10, 100))) + "\n")
	if m.detailTicketLoading {
		body.WriteString("  " + footerDescStyle.Render("Loading description…") + "\n")
	} else if m.detailTicketErr != "" {
		body.WriteString("  " + statusPermission.Render("error: ") + footerDescStyle.Render(m.detailTicketErr) + "\n")
	} else if m.detailTicket != nil {
		desc := strings.TrimSpace(m.detailTicket.DescriptionText)
		if desc == "" {
			desc = "(no description)"
		}
		body.WriteString(wrapText(desc, clamp(width-2, 40, 120)))
		body.WriteString("\n")
	}

	// Dynamic hint explaining what `s` will do.
	body.WriteString("\n")
	body.WriteString(m.renderStartHint(width))
	body.WriteString("\n")

	// Picker overlay or footer.
	if m.pickerActive {
		body.WriteString(m.renderProjectPicker(width))
	}

	// Pad to roughly fill height so the footer lives at the bottom.
	lineCount := strings.Count(body.String(), "\n")
	for i := lineCount; i < height-3; i++ {
		body.WriteString("\n")
	}

	var footer string
	if m.pickerRememberActive {
		footer = m.renderPrompt(
			fmt.Sprintf("Remember %s → %s for future tickets?", m.pickerForJiraKey, m.pickerPendingMo),
			[]footerBinding{
				{"y", "remember"},
				{"o", "just this once"},
				{"n", "cancel"},
			})
	} else if m.pickerActive {
		footer = m.renderFooter([]footerBinding{
			{"enter", "select"},
			{"/", "filter"},
			{"esc", "cancel"},
		})
	} else {
		binds := []footerBinding{
			{"s", "start working"},
			{"o", "open in browser"},
			{"y", "copy branch name"},
			{"esc", "back"},
		}
		footer = m.renderFooter(binds)
	}

	return body.String() + footer
}

// renderTicketMeta formats the two-column metadata grid. Source order:
// Status / Priority, Reporter / Assignee, Sprint / Updated, Project / Maps-to.
func (m Model) renderTicketMeta(width int) string {
	t := m.detailTicketList
	if m.detailTicket != nil {
		t = &m.detailTicket.Ticket
	}
	if t == nil {
		return ""
	}

	colW := clamp((width-4)/2, 30, 60)

	rows := [][2][2]string{
		{
			{"Status:", valueOrDash(t.RawStatus)},
			{"Priority:", priorityLabel(t.Priority)},
		},
	}
	if m.detailTicket != nil {
		rows = append(rows, [2][2]string{
			{"Reporter:", valueOrDash(m.detailTicket.Reporter)},
			{"Assignee:", valueOrDash(m.detailTicket.AssigneeDisplay)},
		})
	}
	sprintCell := "—"
	if t.InSprint {
		sprintCell = t.SprintName
		if sprintCell == "" {
			sprintCell = "yes"
		}
		sprintCell = statusActive.Render("⚡ ") + sprintCell
	}
	rows = append(rows, [2][2]string{
		{"Sprint:", sprintCell},
		{"Updated:", relativeTime(t.UpdatedAt)},
	})
	mapsTo := m.resolvedMoProjectForTicket(t)
	if mapsTo == "" {
		mapsTo = footerDescStyle.Render("(unmapped — pick on start)")
	}
	rows = append(rows, [2][2]string{
		{"Project:", valueOrDash(t.ProjectKey)},
		{"Maps to:", mapsTo},
	})

	var b strings.Builder
	for _, r := range rows {
		left := fmt.Sprintf("  %s %s", headerStyle.Render(r[0][0]), r[0][1])
		right := fmt.Sprintf("%s %s", headerStyle.Render(r[1][0]), r[1][1])
		// Pad left column to colW (measured visible width).
		pad := colW - lipgloss.Width(left)
		if pad < 1 {
			pad = 1
		}
		b.WriteString(left + strings.Repeat(" ", pad) + right + "\n")
	}
	return b.String()
}

// renderStartHint shows what `s` will do right now. Computed at render time
// because the right answer depends on the live session state.
func (m Model) renderStartHint(width int) string {
	t := m.detailTicketList
	if m.detailTicket != nil {
		t = &m.detailTicket.Ticket
	}
	if t == nil {
		return ""
	}
	moProject := m.resolvedMoProjectForTicket(t)
	branch := tickets.BranchNameForTicket(t.ID, t.Title)
	if moProject == "" {
		return "  " + footerDescStyle.Render(fmt.Sprintf("s → pick Mo project for %s, then start %s", t.ProjectKey, branch))
	}

	p := m.moProjectByName(moProject)
	if p == nil {
		return "  " + footerDescStyle.Render(fmt.Sprintf("s → project %q not configured", moProject))
	}
	existing := existingWorktreePath(p.Path, branch)
	hasSession := claude.SessionForPath(p.Path) != nil
	dirty, _ := project.IsDirty(p.Path)
	onDisk := m.moProjectExistsOnDisk(moProject)

	var msg string
	switch decideTicketStrategy(onDisk, hasSession, dirty, existing) {
	case strategyMissing:
		msg = fmt.Sprintf("s → project %q not found on disk — mapping may be stale", moProject)
	case strategyFocusExisting:
		msg = fmt.Sprintf("s → switch to existing worktree %s", branch)
	case strategyWorktree:
		reason := "session already running"
		if !hasSession && dirty {
			reason = "main has uncommitted changes"
		} else if hasSession && dirty {
			reason = "session running; main also dirty"
		}
		msg = fmt.Sprintf("s → create worktree %s (%s)", branch, reason)
	case strategyMainCheckout:
		msg = fmt.Sprintf("s → checkout %s in main repo of %s", branch, moProject)
	}
	return "  " + footerDescStyle.Render(msg)
}

// --- tiny helpers ---

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func valueOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func priorityLabel(p tickets.Priority) string {
	switch p {
	case tickets.PriorityHighest:
		return "Highest"
	case tickets.PriorityHigh:
		return "High"
	case tickets.PriorityMedium:
		return "Medium"
	case tickets.PriorityLow:
		return "Low"
	case tickets.PriorityLowest:
		return "Lowest"
	}
	return "—"
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

// renderProjectPicker renders the picker overlay. Implemented in
// project_picker.go — stubbed here if the user triggers it before the
// bubbles/list model is initialized.
func (m Model) renderProjectPicker(width int) string {
	return m.renderPickerOverlay(width)
}

// moProjectByName returns the Project struct for a given name, or nil.
func (m Model) moProjectByName(name string) *project.Project {
	for i := range m.projects {
		if m.projects[i].Name == name {
			return &m.projects[i]
		}
	}
	return nil
}

// moProjectExistsOnDisk reports whether the mapped Mo project's directory
// is still present. A stale mapping (project deleted) surfaces in the
// `s → ...` hint rather than crashing on start.
func (m Model) moProjectExistsOnDisk(name string) bool {
	p := m.moProjectByName(name)
	if p == nil {
		return false
	}
	info, err := os.Stat(p.Path)
	return err == nil && info.IsDir()
}

// moProjectHasLiveSession reports whether Claude is currently running at
// the main checkout of the given Mo project — drives the worktree-vs-main
// default when the user hits `s`.
func (m Model) moProjectHasLiveSession(name string) bool {
	p := m.moProjectByName(name)
	if p == nil {
		return false
	}
	return claude.SessionForPath(p.Path) != nil
}

// --- Key handlers & state machine ---

// activeTicket returns the ticket context being acted on: prefers the full
// detail (if the fetch has completed) over the list-level Ticket captured
// at screen entry.
func (m Model) activeTicket() *tickets.Ticket {
	if m.detailTicket != nil {
		return &m.detailTicket.Ticket
	}
	return m.detailTicketList
}

// handleTicketStartWorking is the entry point for the `s` key on ScreenTicket.
// Resolves the project mapping; if missing, opens the picker. Otherwise
// proceeds straight to the branch/worktree flow.
func (m Model) handleTicketStartWorking() (Model, tea.Cmd) {
	t := m.activeTicket()
	if t == nil {
		return m, func() tea.Msg { return statusMsgEvent("No ticket loaded") }
	}
	moProject := m.resolvedMoProjectForTicket(t)
	if moProject == "" {
		m.startProjectPicker(t.Provider, t.ProjectKey)
		return m, nil
	}
	return m.startWorkOnTicket(t, moProject)
}

// handleTicketPickerActive routes keys to the picker's list.Model, except
// for enter (confirm pick → show remember prompt) and esc (already handled
// earlier in Update via the Back binding). Enter inside an active filter
// confirms the filter (handled by the list itself); only enter with the
// filter inactive / applied selects the highlighted project.
func (m Model) handleTicketPickerActive(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.String() == "enter" && m.pickerList.FilterState() != list.Filtering {
		picked := m.pickerSelectedProject()
		if picked == "" {
			return m, nil
		}
		m.pickerPendingMo = picked
		m.pickerRememberActive = true
		m.pickerActive = false
		return m, nil
	}
	cmd := m.updateProjectPicker(msg)
	return m, cmd
}

// handleTicketPickerRemember handles keys on the remember-or-not prompt
// after a pick. y = remember, o = just-this-once, n/esc = cancel back to
// the picker. See CLAUDE.md → Prompt conventions.
func (m Model) handleTicketPickerRemember(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	provider := m.pickerForProvider
	jiraKey := m.pickerForJiraKey
	pickedMo := m.pickerPendingMo

	switch msg.String() {
	case "y", "Y":
		if err := tickets.SaveProjectMapEntry(provider, jiraKey, pickedMo); err != nil {
			return m, func() tea.Msg { return statusMsgEvent(fmt.Sprintf("save mapping failed: %v", err)) }
		}
		// Reload so the in-memory map reflects the new entry.
		if reloaded, err := tickets.LoadCompanionProjectMap(); err == nil {
			m.ticketProjectMap = reloaded
		}
	case "o", "O":
		// One-time use: stash the mapping only for this session so the
		// start flow that follows can find it. Safe because we only ever
		// read ticketProjectMap here and in resolvedMoProjectForTicket.
		if m.ticketProjectMap == nil {
			m.ticketProjectMap = map[string]map[string]string{}
		}
		if m.ticketProjectMap[provider] == nil {
			m.ticketProjectMap[provider] = map[string]string{}
		}
		m.ticketProjectMap[provider][jiraKey] = pickedMo
	case "n", "N", "esc", "escape":
		// Cancel: drop the pending pick and reopen the picker so the user
		// can choose differently (or esc again to leave the flow).
		m.pickerRememberActive = false
		m.pickerPendingMo = ""
		m.pickerActive = true
		return m, nil
	default:
		return m, nil
	}

	m.pickerRememberActive = false
	m.pickerPendingMo = ""

	t := m.activeTicket()
	if t == nil {
		return m, nil
	}
	return m.startWorkOnTicket(t, pickedMo)
}

// handleTicketYank copies the derived branch name to the system clipboard.
func (m Model) handleTicketYank() (Model, tea.Cmd) {
	t := m.activeTicket()
	if t == nil {
		return m, nil
	}
	branch := tickets.BranchNameForTicket(t.ID, t.Title)
	if err := clipboard.WriteAll(branch); err != nil {
		return m, func() tea.Msg { return statusMsgEvent(fmt.Sprintf("copy failed: %v", err)) }
	}
	return m, func() tea.Msg { return statusMsgEvent("Copied " + branch) }
}

// startWorkOnTicket is the terminal step once mapping + picker have resolved.
// Temporarily sets m.detailProject so the existing createWorktreeAndLaunch /
// openBranchInMain helpers (which read it) can be reused unchanged, then
// transitions back to dashboard with a status message.
func (m Model) startWorkOnTicket(t *tickets.Ticket, moProjectName string) (Model, tea.Cmd) {
	moProject := m.moProjectByName(moProjectName)
	if moProject == nil {
		return m, func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("Mo project %q not configured", moProjectName))
		}
	}
	if _, err := os.Stat(moProject.Path); err != nil {
		return m, func() tea.Msg {
			return statusMsgEvent(fmt.Sprintf("Mo project %q not found on disk (%s)", moProjectName, moProject.Path))
		}
	}

	branch := tickets.BranchNameForTicket(t.ID, t.Title)
	if branch == "" {
		return m, func() tea.Msg { return statusMsgEvent("Could not derive branch name") }
	}

	existing := existingWorktreePath(moProject.Path, branch)
	hasSession := claude.SessionForPath(moProject.Path) != nil
	dirty, _ := project.IsDirty(moProject.Path)

	// Set detailProject so the existing createWorktreeAndLaunch /
	// openBranchInMain helpers (which read it) can be reused unchanged.
	// Copy the value so later mutations on detailProject don't leak back
	// into m.projects.
	projCopy := *moProject
	m.detailProject = &projCopy

	switch decideTicketStrategy(true, hasSession, dirty, existing) {
	case strategyFocusExisting:
		windowName := moProject.Name + "@" + branch
		if existed, err := m.focusIfExists(windowName); existed && err == nil {
			m.screen = ScreenDashboard
			return m, func() tea.Msg {
				return statusMsgEvent("Switched to existing worktree " + windowName)
			}
		}
		if m.tmux != nil {
			if se, ok := m.launchAgentInWindow(windowName, existing, m.defaultAgent().Cmd, m.defaultAgent().Name, m.defaultAgent().Key).(statusMsgEvent); ok {
				m.screen = ScreenDashboard
				return m, func() tea.Msg { return statusMsgEvent(string(se)) }
			}
		}
		return m, nil
	case strategyWorktree:
		m.screen = ScreenDashboard
		return m, m.createWorktreeAndLaunch(branch)
	case strategyMainCheckout:
		m.screen = ScreenDashboard
		return m, m.openBranchInMain(branch, false)
	}
	return m, nil
}

// existingWorktreePath returns the filesystem path to an existing worktree
// for the given branch, or "" if none exists.
func existingWorktreePath(projectPath, branch string) string {
	wts, err := project.ListWorktrees(projectPath)
	if err != nil {
		return ""
	}
	for _, w := range wts {
		if w.Branch == branch {
			return w.Path
		}
	}
	return ""
}

// wrapText wraps text to width characters at whitespace boundaries. Handles
// existing newlines as hard breaks.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	var out strings.Builder
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out.WriteString("\n")
			continue
		}
		words := strings.Fields(paragraph)
		line := ""
		for _, w := range words {
			if line == "" {
				line = w
				continue
			}
			if len(line)+1+len(w) > width {
				out.WriteString(line + "\n")
				line = w
			} else {
				line += " " + w
			}
		}
		if line != "" {
			out.WriteString(line + "\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}
