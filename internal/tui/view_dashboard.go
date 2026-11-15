package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/rvanmech/unky-mo/internal/usage"
)

func (m Model) View() tea.View {
	var content string
	if !m.ready {
		content = "Loading..."
	} else {
		switch m.screen {
		case ScreenProject:
			content = m.projectDetailView()
		case ScreenHelp:
			content = m.helpView()
		case ScreenTicket:
			content = m.renderTicketDetailScreen()
		default:
			content = m.dashboardView()
		}
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
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
		_ = summary
		m.list.Title = "Unky Mo  " + strings.Join(statusParts, "  ")
	}

	// Split the dashboard 50/50 between the project list and the
	// sessions+tickets panel. On narrow terminals, clamp the left side so
	// the list stays usable; on very wide terminals the even split keeps
	// each half readable without stretching.
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

	// A sticky notice (e.g. the web CA isn't trusted, with the command that
	// fixes it) gets its own full-width block: the list's status line only
	// spans the left panel and would cut the instructions off.
	stickyNotice := ""
	if m.statusSticky && m.statusMsg != "" {
		stickyNotice = notifBadgeStyle.Width(totalWidth).Render(m.statusMsg)
	}

	// Resize list to fit the left panel
	footerHeight := 6
	if stickyNotice != "" {
		footerHeight += lipgloss.Height(stickyNotice)
	}
	m.list.SetSize(leftWidth, m.height-footerHeight)

	// === Left panel: project list ===
	leftStr := m.list.View()

	// === Right panel: active sessions (top) + tickets (bottom) ===
	var right strings.Builder

	right.WriteString(m.renderSessionsPanel(rightWidth))

	if panel := m.renderTicketsPanel(rightWidth); panel != "" {
		right.WriteString(panel)
	}

	rightStr := right.String()

	// Pad panels to same height
	leftLines := strings.Count(leftStr, "\n")
	rightLines := strings.Count(rightStr, "\n")
	maxLines := leftLines
	if rightLines > maxLines {
		maxLines = rightLines
	}
	for i := rightLines; i < maxLines; i++ {
		rightStr += "\n"
	}

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(leftStr)
	divider := lipgloss.NewStyle().Foreground(colorMuted).Render(" │ ")
	rightPanel := lipgloss.NewStyle().Width(rightWidth).Render(rightStr)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, divider, rightPanel)

	usageStrip := m.renderUsageStrip(totalWidth)

	footer := m.dashboardFooter()

	parts := []string{body}
	if stickyNotice != "" {
		parts = append(parts, stickyNotice)
	}
	parts = append(parts, usageStrip, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderSessionsPanel is the dashboard's Active Sessions list: projects'
// sessions, then external ones, each with its status and agent.
func (m Model) renderSessionsPanel(rightWidth int) string {
	var right strings.Builder
	sessionsFocused := !m.dashFocusLeft && m.dashRightFocus == dashRightSessions
	focusIndicator := ""
	if sessionsFocused {
		focusIndicator = " ◀"
	}
	// Pad top to align with the first project row in the list
	// (list has: title, blank, status line, blank = 4 lines before items)
	right.WriteString("\n\n\n")
	right.WriteString(headerStyle.Render("Active Sessions"+focusIndicator) + "\n")

	if len(m.dashSessionItems) == 0 {
		right.WriteString("  " + footerDescStyle.Render("No active sessions") + "\n")
	} else {
		prevSection := ""
		for i, item := range m.dashSessionItems {
			section := item.Section
			if section == "" {
				section = "projects"
			}
			if section != prevSection {
				label := "Projects"
				if section == "external" {
					label = "External"
				}
				// Blank line between sections; label above the first row.
				if prevSection != "" {
					right.WriteString("\n")
				}
				right.WriteString("  " + footerDescStyle.Render(label) + "\n")
				prevSection = section
			}

			selected := sessionsFocused && m.dashSessionCursor == i
			cursor := "  "
			if selected {
				cursor = "▸ "
			}

			var dot string
			switch item.Status {
			case StatusActive:
				dot = statusActive.Render(symbolActive)
			case StatusIdle:
				dot = statusIdle.Render(symbolIdle)
			case StatusPermission, StatusQuestion:
				dot = statusPermission.Render(symbolPermission)
			case StatusExternal:
				dot = statusExternal.Render(symbolActive)
			default:
				dot = statusNone.Render(symbolNone)
			}

			name := item.Name
			maxName := rightWidth - 6
			if maxName > 0 && len(name) > maxName {
				name = name[:maxName-3] + "..."
			}

			var styledName string
			if selected {
				styledName = selectedItemStyle.Render(name)
			} else {
				styledName = normalItemStyle.Render(name)
			}

			suffix := ""
			if tag := m.agentTagForKey(item.AgentKey); tag != "" {
				suffix = " " + footerDescStyle.Render(tag)
			}
			if item.Status == StatusIdle {
				suffix += " " + statusIdle.Render("idle")
			} else if item.Status == StatusPermission {
				suffix += " " + statusPermission.Render("perm")
			} else if item.Status == StatusQuestion {
				suffix += " " + statusPermission.Render("ask")
			} else if item.Status == StatusExternal {
				suffix += " " + statusExternal.Render("external")
			}
			// Branch/dirty info for git-backed strays.
			if item.Git != nil && item.Git.Branch != "" {
				gitInfo := item.Git.Branch
				if item.Git.Dirty > 0 {
					gitInfo += fmt.Sprintf(" *%d", item.Git.Dirty)
				}
				suffix = " " + footerDescStyle.Render(gitInfo) + suffix
			}

			right.WriteString(cursor + dot + " " + styledName + suffix + "\n")
		}
	}
	return right.String()
}

// dashboardFooter is the open prompt's question and keys, or the
// dashboard's key hints.
func (m Model) dashboardFooter() string {
	var footer string
	if m.cleanup != nil {
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	} else if m.newSession != nil {
		footer = m.renderPrompt(m.newMenuPromptText(), withCancel([]footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
		}))
	} else if m.suspendQuit != nil {
		question := fmt.Sprintf("%d active session(s) — suspend all and quit?", m.activeSessions)
		footer = m.renderPrompt(question, withCancel([]footerBinding{
			{"S", "suspend + quit"},
		}))
	} else if m.importExternal != nil {
		question := fmt.Sprintf("%q is running outside Unky Mo. Import it? (kills the external claude and resumes here) [y/N]", m.importExternal.project)
		footer = m.renderPrompt(question, yesNoBindings("import"))
	} else if m.agentPicker != nil {
		footer = m.renderPrompt("Launch with:", withCancel(m.agentPickerBindings()))
	} else if m.newProject != nil {
		question := fmt.Sprintf("New project in %s — folder name:", m.newProject.parentDir)
		footer = m.renderInputPrompt(question, m.newProject.input.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	} else {
		footer = m.renderFooter([]footerBinding{
			{"↑↓", "navigate"},
			{"←→", "switch panel"},
			{"enter", "open"},
			{"A", "agent"},
			{"n", "new session"},
			{"N", "new project"},
			{"a", "attach"},
			{"/", "filter"},
			{"?", "help"},
			{"s", "suspend"},
			{"q", "quit"},
		})
	}
	return footer
}

// renderUsageStrip renders the dashboard footer line showing 5h + weekly
// Claude rate-limit-window utilization. Width is clamped so bars never
// underflow on very narrow terminals.
func (m Model) renderUsageStrip(width int) string {
	if width <= 0 {
		width = 80
	}
	if !m.usageReady {
		return usageStripStyle.Width(width).Render("usage: computing…")
	}
	if m.usageAuth {
		return usageStripStyle.Width(width).Render("usage: auth expired — run `claude`")
	}

	fivePct := usage.PctFromUtil(m.usage.FiveHour.Utilization)
	sevenPct := usage.PctFromUtil(m.usage.SevenDay.Utilization)
	now := time.Now()
	fiveResets := usage.FormatResetIn(now, m.usage.FiveHour.ResetsAt)

	// Reserve non-bar characters: "5h  NN% (NhNNm)  ·  Week  NN%" ≈ 36 chars
	// plus borders/padding (~4). Leave the rest for two equal bars.
	fixed := 40
	barWidth := (width - fixed) / 2
	if barWidth < 6 {
		barWidth = 6
	}
	if barWidth > 24 {
		barWidth = 24
	}

	fiveBar := colorBar(m.usage.FiveHour.Utilization, barWidth, fivePct)
	sevenBar := colorBar(m.usage.SevenDay.Utilization, barWidth, sevenPct)

	left := fmt.Sprintf("%s %s %d%% (%s)",
		usageLabelStyle.Render("5h"), fiveBar, fivePct, fiveResets)
	right := fmt.Sprintf("%s %s %d%%",
		usageLabelStyle.Render("Week"), sevenBar, sevenPct)

	line := left + "  ·  " + right
	if m.usage.Source == "stale" {
		line += "  " + barEmptyStyle.Render("(stale)")
	}
	return usageStripStyle.Width(width).Render(line)
}

// colorBar splits the bar into filled/empty segments and colors them
// independently so the filled portion can switch to warn/danger styles
// at high utilization without tinting the empty portion.
func colorBar(util float64, width, pct int) string {
	filled, empty := usage.SplitBar(util, width)
	return pickBarStyle(pct).Render(filled) + barEmptyStyle.Render(empty)
}
