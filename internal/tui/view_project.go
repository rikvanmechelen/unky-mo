package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/usage"
)

func (m Model) projectDetailView() string {
	if m.detailProject == nil {
		return "No project selected"
	}
	p := m.detailProject

	// Header (full width)
	title := titleStyle.Render(" ← " + p.Name + " ")
	lang := p.Language
	if lang == "" {
		lang = "unknown"
	}
	header := title + "  " + langStyle.Render("["+lang+"]") + "  " + footerDescStyle.Render(p.Path) + "\n\n"

	// Calculate panel widths
	dividerWidth := 3 // " │ "
	totalWidth := m.width
	if totalWidth == 0 {
		totalWidth = 80
	}
	rightWidth := totalWidth * 2 / 5
	if rightWidth < 25 {
		rightWidth = 25
	}
	leftWidth := totalWidth - rightWidth - dividerWidth

	leftStr := m.renderBranchPanel(leftWidth)

	rightStr := m.renderPRPanel(rightWidth)

	// === Combine panels ===
	// Pad both panels to the same height
	leftLines := strings.Count(leftStr, "\n")
	rightLines := strings.Count(rightStr, "\n")
	maxLines := leftLines
	if rightLines > maxLines {
		maxLines = rightLines
	}
	for i := leftLines; i < maxLines; i++ {
		leftStr += "\n"
	}
	for i := rightLines; i < maxLines; i++ {
		rightStr += "\n"
	}

	leftPanel := lipgloss.NewStyle().Width(leftWidth).Render(leftStr)
	divider := lipgloss.NewStyle().Foreground(colorMuted).Render(" │ ")
	rightPanel := lipgloss.NewStyle().Width(rightWidth).Render(rightStr)

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, divider, rightPanel)

	// Status message
	if m.statusMsg != "" {
		body += "\n" + notifBadgeStyle.Render(m.statusMsg)
	}

	footer := m.projectFooter(p)

	// Pad content to fill screen, then add footer
	content := header + body
	contentLines := strings.Count(content, "\n")
	footerLines := 6
	if contentLines < m.height-footerLines {
		content += strings.Repeat("\n", m.height-footerLines-contentLines)
	}

	return content + footer
}

// renderBranchPanel is the project screen's left panel: each branch with
// its sessions, and the selected session's last messages.
func (m Model) renderBranchPanel(leftWidth int) string {
	var left strings.Builder

	focusIndicator := ""
	if m.detailFocusLeft {
		focusIndicator = " ◀"
	}

	// Render using detailRows
	lastKind := ""
	for i, row := range m.detailRows {
		selected := m.detailFocusLeft && m.detailCursor == i

		switch row.kind {
		case "branch":
			if lastKind == "" {
				left.WriteString(headerStyle.Render("Branches"+focusIndicator) + "\n")
			} else if lastKind != "branch" {
				left.WriteString("\n")
			}
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			marker := "·"
			switch {
			case row.branch.IsMain:
				marker = "●"
			case row.branch.WorktreePath != "":
				marker = "⎇"
			}
			age := formatAge(time.Since(row.branch.LastCommit))
			label := fmt.Sprintf("%s%s %s  %s", cursor, marker, row.branch.Name, age)
			var tags []string
			if row.branch.Merged {
				tags = append(tags, "merged")
			}
			if row.branch.RemoteGone {
				tags = append(tags, "gone")
			}
			if selected {
				line := label
				if len(tags) > 0 {
					line = label + "  " + footerDescStyle.Render("["+strings.Join(tags, ", ")+"]")
				}
				left.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				line := headerStyle.Render(label)
				if len(tags) > 0 {
					line += "  " + footerDescStyle.Render("["+strings.Join(tags, ", ")+"]")
				}
				left.WriteString(line + "\n")
			}

		case "br-session":
			left.WriteString("  " + m.renderSessionRow(row.session, row.path, selected, leftWidth-2) + "\n")

		case "br-empty":
			left.WriteString("    " + footerDescStyle.Render("(no sessions)") + "\n")

		case "br-remote":
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			title := row.remoteMeta.Title
			if title == "" {
				title = "(untitled)"
			}
			age := formatAge(time.Since(row.remoteMeta.PushedAt))
			host := row.remoteMeta.Hostname
			if host == "" {
				host = "?"
			}
			line := fmt.Sprintf("%s⇅ %s %s  %s%s  %s",
				cursor, statusNone.Render("○"), age, title,
				"  "+footerDescStyle.Render(host),
				footerDescStyle.Render("(remote only)"))
			if selected {
				left.WriteString("  " + selectedItemStyle.Render(line) + "\n")
			} else {
				left.WriteString("  " + normalItemStyle.Render(line) + "\n")
			}
		}

		lastKind = row.kind
	}

	if len(m.detailRows) == 0 {
		left.WriteString(headerStyle.Render("Branches"+focusIndicator) + "\n")
		if m.detailLoading {
			left.WriteString("  " + footerDescStyle.Render("Loading branches…") + "\n")
		} else {
			left.WriteString("  " + footerDescStyle.Render("No branches found") + "\n")
		}
	}

	// Session recap preview
	if len(m.detailRecap) > 0 && m.detailFocusLeft {
		left.WriteString("\n" + headerStyle.Render("Last messages") + "\n")
		for _, msg := range m.detailRecap {
			role := footerKeyStyle.Render("You:")
			if msg.Role == "assistant" {
				role = statusActive.Render("Claude:")
			}
			content := msg.Content
			maxLen := leftWidth - 12
			if maxLen > 0 && len(content) > maxLen {
				content = content[:maxLen-3] + "..."
			}
			left.WriteString(fmt.Sprintf("  %s %s\n", role, footerDescStyle.Render(content)))
		}
	}
	return left.String()
}

// renderPRPanel is the project screen's right panel: the open pull
// requests, the expanded one's detail under it.
func (m Model) renderPRPanel(rightWidth int) string {
	var right strings.Builder

	prFocusIndicator := ""
	if !m.detailFocusLeft {
		prFocusIndicator = " ◀"
	}
	right.WriteString(headerStyle.Render("Pull Requests"+prFocusIndicator) + "\n")

	if m.detailPRErr != "" {
		right.WriteString("  " + footerDescStyle.Render(m.detailPRErr) + "\n")
	} else if m.detailPRs == nil {
		right.WriteString("  " + footerDescStyle.Render("Loading...") + "\n")
	} else if len(m.detailPRs) == 0 {
		right.WriteString("  " + footerDescStyle.Render("No open pull requests") + "\n")
	} else {
		for i, pr := range m.detailPRs {
			selected := !m.detailFocusLeft && m.detailPRCursor == i
			expanded := m.detailPRExpanded == i
			cursor := "  "
			if selected {
				cursor = "▸ "
			}

			num := fmt.Sprintf("#%d", pr.Number)
			prTitle := pr.Title
			maxTitle := rightWidth - len(num) - 5
			if maxTitle < 10 {
				maxTitle = 10
			}
			if len(prTitle) > maxTitle {
				prTitle = prTitle[:maxTitle-3] + "..."
			}

			line := fmt.Sprintf("%s%s %s", cursor, langStyle.Render(num), prTitle)
			if selected {
				right.WriteString(selectedItemStyle.Render(line) + "\n")
			} else {
				right.WriteString(normalItemStyle.Render(line) + "\n")
			}

			// Expanded detail view
			if expanded {
				right.WriteString(m.renderPRDetail(pr, rightWidth) + "\n")
			}
		}
	}
	return right.String()
}

// projectFooter is the open prompt's question and keys, or the project
// screen's key hints.
func (m Model) projectFooter(p *project.Project) string {
	var footer string
	switch {
	case m.worktree != nil:
		question := fmt.Sprintf("Create new worktree for %s — branch name:", p.Name)
		footer = m.renderInputPrompt(question, m.worktree.input.View(), []footerBinding{
			{"enter", "create"},
			{"esc", "cancel"},
		})
	case m.lift != nil && !m.lift.dirty:
		question := fmt.Sprintf("Lift session into new worktree — branch name:")
		footer = m.renderInputPrompt(question, m.lift.input.View(), []footerBinding{
			{"enter", "continue"},
			{"esc", "cancel"},
		})
	case m.lift != nil && m.lift.dirty:
		q := fmt.Sprintf("Source has uncommitted changes — carry into %s?", m.lift.branch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"s", "stash + pop"},
			{"l", "leave in source"},
		}))
	case m.worktreeExists != nil:
		q := fmt.Sprintf("Worktree for %s already exists", m.worktreeExists.branch)
		footer = m.renderPrompt(q, withCancel([]footerBinding{
			{"f", "focus existing"},
			{"r", "remove + recreate"},
		}))
	case m.cleanup != nil:
		q, binds := m.cleanupPrompt()
		footer = m.renderPrompt(q, binds)
	case m.newSession != nil:
		footer = m.renderPrompt(m.newMenuPromptText(), []footerBinding{
			{"s", "switch"},
			{"p", "park+new"},
			{"c", "concurrent"},
			{"esc", "cancel"},
		})
	case m.agentPicker != nil:
		footer = m.renderPrompt("Launch with:", withCancel(m.agentPickerBindings()))
	default:
		var bindings []footerBinding
		if !m.detailFocusLeft && len(m.detailPRs) > 0 {
			enterLabel := "expand"
			if m.detailPRExpanded >= 0 {
				enterLabel = "close"
			}
			bindings = []footerBinding{
				{"o", "github"},
				{"w", "worktree"},
				{"c", "checkout"},
				{"enter", enterLabel},
				{"←→", "switch panel"},
				{"esc", "back"},
			}
		} else {
			bindings = []footerBinding{
				{"↑↓", "select"},
				{"←→", "panel"},
				{"enter", "resume"},
				{"A", "agent"},
				{"w", "worktree"},
				{"m", "main"},
				{"M", "stash+main"},
				{"W", "new branch"},
				{"n", "session"},
				{"x", "remove"},
				{"o", "PR"},
				{"s", "suspend"},
				{"esc", "back"},
			}
		}
		footer = m.renderFooter(bindings)
	}
	return footer
}

func (m Model) renderSessionRow(rs *claude.RecentSession, launchPath string, selected bool, maxWidth int) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}

	var statusStr string
	if rs.IsLive {
		statusStr = statusActive.Render("●")
	} else {
		statusStr = statusNone.Render("○")
	}

	age := formatAge(time.Since(rs.LastActive))
	name := rs.DisplayName()

	tokStr := ""
	if launchPath != "" && rs.SessionID != "" {
		jsonl := filepath.Join(m.claude.ProjectsDirForPath(launchPath), rs.SessionID+".jsonl")
		tokStr = usage.FormatTokensShort(usage.SessionTokens(jsonl))
	}

	maxName := maxWidth - 24
	if maxName < 10 {
		maxName = 10
	}
	if len(name) > maxName {
		name = name[:maxName-3] + "..."
	}

	// Sync indicator lives as a leading column so synced and non-synced rows
	// align. Host + push age hang off the end as secondary metadata.
	syncPrefix := "  "
	syncTrailer := ""
	if meta, ok := m.syncedSessions[rs.SessionID]; ok {
		syncPrefix = "⇅ "
		if meta.Hostname != "" {
			syncTrailer = "  " + footerDescStyle.Render(meta.Hostname)
		}
		if !meta.PushedAt.IsZero() {
			syncTrailer += "  " + footerDescStyle.Render(formatAge(time.Since(meta.PushedAt)))
		}
	}

	tokCol := ""
	if tokStr != "" {
		tokCol = "  " + footerDescStyle.Render(tokStr)
	}

	line := fmt.Sprintf("%s%s%s %s  %s%s%s", cursor, syncPrefix, statusStr, age, name, tokCol, syncTrailer)
	if selected {
		return selectedItemStyle.Render(line)
	}
	return normalItemStyle.Render(line)
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

func (m Model) renderPRDetail(pr gh.PullRequest, maxWidth int) string {
	var b strings.Builder
	indent := "  │ "

	if m.detailPRDetail != nil && m.detailPRDetail.Number == pr.Number {
		d := m.detailPRDetail

		// Title + author + branch
		b.WriteString(indent + selectedItemStyle.Render(d.Title) + "\n")
		b.WriteString(indent + footerDescStyle.Render(fmt.Sprintf("by %s  %s → %s", d.Author.Login, d.Branch, d.BaseBranch)) + "\n")

		// Stats + review
		stats := fmt.Sprintf("+%d -%d", d.Additions, d.Deletions)
		review := d.ReviewDecision
		if review == "" {
			review = "no reviews"
		}
		b.WriteString(indent + statusActive.Render(stats) + "  " + footerDescStyle.Render(review) + "\n")

		// Body (first few lines)
		if d.Body != "" {
			bodyLines := strings.Split(d.Body, "\n")
			maxLines := 4
			if len(bodyLines) > maxLines {
				bodyLines = bodyLines[:maxLines]
			}
			for _, line := range bodyLines {
				line = strings.TrimSpace(line)
				maxLen := maxWidth - len(indent) - 1
				if maxLen > 0 && len(line) > maxLen {
					line = line[:maxLen-3] + "..."
				}
				b.WriteString(indent + footerDescStyle.Render(line) + "\n")
			}
		}

		b.WriteString(indent + "\n")
		b.WriteString(indent + footerKeyStyle.Render("o") + ":" + footerDescStyle.Render("github") + "  ")
		b.WriteString(footerKeyStyle.Render("w") + ":" + footerDescStyle.Render("worktree") + "  ")
		b.WriteString(footerKeyStyle.Render("c") + ":" + footerDescStyle.Render("checkout") + "  ")
		b.WriteString(footerKeyStyle.Render("⏎") + ":" + footerDescStyle.Render("close") + "\n")
	} else {
		b.WriteString(indent + footerDescStyle.Render("Loading...") + "\n")
	}

	return b.String()
}
