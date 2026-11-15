package sidebar

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/usage"
)

func (m Model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
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
		b.WriteString(m.renderItem(m.items[i], i == m.cursor, maxNameLen) + "\n")
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
			display := m.claude.FormatShellCommand(sh.Command, m.width-6)
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

	// File tree section — two modes: Changed (default) and Full
	showFileSection := false
	switch m.fileTreeMode {
	case fileTreeModeChanged:
		showFileSection = len(m.changedFiles) > 0
	case fileTreeModeFull:
		showFileSection = m.dirTree != nil && len(m.dirTree.children) > 0
	}
	if showFileSection && remaining > 3 {
		maxFileLines := remaining - 2
		if maxFileLines > 0 {
			b.WriteString("\n")

			switch m.fileTreeMode {
			case fileTreeModeChanged:
				// Header: ──── Changed 4 files +12 -3 ────
				noun := "files"
				if len(m.changedFiles) == 1 {
					noun = "file"
				}
				label := fmt.Sprintf("Changed %d %s", len(m.changedFiles), noun)
				if m.changedAdded > 0 || m.changedRemoved > 0 {
					label += fmt.Sprintf(" +%d -%d", m.changedAdded, m.changedRemoved)
				}
				b.WriteString(renderSectionHeader(label, m.width) + "\n")

				// Build tree lines with file index mapping
				treeLines := buildFileTreeLines(m.changedFiles)
				m.ensureFileCursorVisible(maxFileLines)
				rendered := 0
				for i := m.fileViewStart; i < len(treeLines); i++ {
					if rendered >= maxFileLines {
						break
					}
					tl := treeLines[i]
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
				// Scroll indicators
				if m.fileViewStart > 0 {
					remaining-- // reserve a line
				}
				if m.fileViewStart+maxFileLines < len(treeLines) {
					remaining-- // reserve a line
				}
				remaining -= rendered + 2

			case fileTreeModeFull:
				// Header: ──── Files (5 changed) ────
				label := "Files"
				if len(m.changedFiles) > 0 {
					label += fmt.Sprintf(" (%d changed)", len(m.changedFiles))
				}
				b.WriteString(renderSectionHeader(label, m.width) + "\n")

				treeLines := m.visibleFileLines()
				m.ensureFileCursorVisible(maxFileLines)
				rendered := 0
				for lineIdx := m.fileViewStart; lineIdx < len(treeLines); lineIdx++ {
					if rendered >= maxFileLines {
						break
					}
					tl := treeLines[lineIdx]
					isFocused := m.focusSection == "files" && lineIdx == m.fileCursor

					// Git status marker
					statusStr := ""
					if tl.gitStatus != "" {
						statusStr = renderGitStatusMarker(tl.gitStatus) + " "
					}

					if tl.isDir {
						// Directory with expand/collapse icon
						icon := "▸"
						if node := findDirNodeFromRoot(m.dirTree, tl.path); node != nil && node.expanded {
							icon = "▾"
						}
						if isFocused {
							b.WriteString(selectedStyle.Render(icon+tl.indent+tl.display+"/") + " " + statusStr + "\n")
						} else {
							b.WriteString(footerStyle.Render(icon+tl.indent+tl.display+"/") + " " + statusStr + "\n")
						}
					} else if isFocused {
						b.WriteString(selectedStyle.Render("▸"+tl.indent+statusStr+tl.display) + "\n")
					} else {
						b.WriteString(normalStyle.Render(" "+tl.indent+statusStr+tl.display) + "\n")
					}
					rendered++
				}
				// Scroll indicators
				if m.fileViewStart > 0 {
					remaining-- // reserve a line
				}
				if m.fileViewStart+maxFileLines < len(treeLines) {
					remaining-- // reserve a line
				}
				remaining -= rendered + 2
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

	b.WriteString(m.renderFooter())

	v := tea.NewView(b.String())
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// renderItem is one row of the sessions list: a section header, a
// terminal tab, Home, or a session with its status, agent and branch.
func (m Model) renderItem(item SidebarItem, isSelected bool, maxNameLen int) string {
	// Cursor
	cursor := "  "
	if isSelected {
		cursor = "▸ "
	}

	var line string
	if item.IsHeader {
		// Section header — not selectable; rendered as a full-width
		// centered title with extending rule like the top "Sessions".
		return renderSectionHeader(item.Name, m.width)
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
		isCurrent := itemMatchesOwnWindow(item, m.instanceID, m.windowID, m.windowName)

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

		// Append agent tag for non-default agents.
		suffix := ""
		if item.IsTeamLead {
			suffix = " " + normalStyle.Render("[team]")
		} else if tag := agentTag(item.AgentKey); tag != "" {
			suffix = " " + normalStyle.Render(tag)
		}
		if item.Status == "idle" {
			suffix += " " + dotIdle.Render("idle")
		} else if item.Status == "permission" {
			suffix += " " + dotPermission.Render("perm")
		} else if item.Status == "question" {
			suffix += " " + dotPermission.Render("ask")
		} else if item.Status == "external" {
			suffix += " " + dotExternal.Render("ext")
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
	return line
}

// renderFooter is the key hints for the focused section (shells, files
// in either mode, or sessions with the sync state).
func (m Model) renderFooter() string {
	var b strings.Builder
	if m.focusSection == "shells" && len(m.activeShells) > 0 {
		b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎ view output") + "\n")
		b.WriteString(footerStyle.Render(" ` popup  s sync  o open") + "\n")
		b.WriteString(footerStyle.Render(" ^r refresh"))
	} else if m.focusSection == "files" && m.fileLineCount() > 0 {
		if m.fileTreeMode == fileTreeModeFull {
			_, isDir, hasStatus := m.resolveFilePath()
			if isDir {
				b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎ expand/collapse") + "\n")
			} else if hasStatus {
				b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎/d diff  v edit") + "\n")
			} else {
				b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎/v edit  o open") + "\n")
			}
			b.WriteString(footerStyle.Render(" . changed  ` popup") + "\n")
			b.WriteString(footerStyle.Render(" ^r refresh"))
		} else {
			b.WriteString(footerStyle.Render(" ↑↓ nav   ⏎/d diff") + "\n")
			b.WriteString(footerStyle.Render(" v edit   o open  . files") + "\n")
			b.WriteString(footerStyle.Render(" ` popup  s sync") + "\n")
			b.WriteString(footerStyle.Render(" ^r refresh"))
		}
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
		b.WriteString(footerStyle.Render(" ⇥ next   x close  o open") + "\n")
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
	case "permission", "question":
		return dotPermission.Render("●")
	case "external":
		return dotExternal.Render("●")
	case "unknown":
		return dotNone.Render("◌") // live, status unknown (the TUI isn't writing state)
	default:
		return dotNone.Render("○")
	}
}

// agentTag returns a short parenthesized label for non-default agents.
// Empty or "c" (Claude) returns "" since Claude is the implied default.
func agentTag(key string) string {
	if key == "" || key == "c" {
		return ""
	}
	// Map well-known keys to friendly names; fall back to the key itself.
	switch key {
	case "g":
		return "(gemini)"
	case "x":
		return "(codex)"
	case "p":
		return "(pi)"
	default:
		return "(" + key + ")"
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
