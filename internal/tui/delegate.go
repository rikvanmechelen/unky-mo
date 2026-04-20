package tui

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	symbolActive     = "●"
	symbolIdle       = "●"
	symbolPermission = "●"
	symbolNone       = "○"
)

type projectDelegate struct{}

func (d projectDelegate) Height() int                             { return 1 }
func (d projectDelegate) Spacing() int                            { return 0 }
func (d projectDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d projectDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	pi, ok := item.(ProjectItem)
	if !ok {
		return
	}

	isSelected := index == m.Index()
	width := m.Width()

	// Cursor
	cursor := "  "
	if isSelected {
		cursor = "▸ "
	}

	// Project name
	name := pi.project.Name
	if len(name) > 28 {
		name = name[:25] + "..."
	}

	// Language badge
	lang := pi.project.Language
	if lang == "" {
		lang = "?"
	}
	langBadge := fmt.Sprintf("[%-4s]", lang)

	// Status indicator
	var statusStr string
	switch pi.status {
	case StatusActive:
		statusStr = statusActive.Render(symbolActive + " active")
	case StatusIdle:
		statusStr = statusIdle.Render(symbolIdle + " needs input")
	case StatusPermission:
		statusStr = statusPermission.Render(symbolPermission + " permission!")
	case StatusExternal:
		statusStr = statusExternal.Render(symbolActive + " external")
	default:
		statusStr = statusNone.Render(symbolNone + " no session")
	}

	// Git info
	var gitStr string
	if pi.git.Branch != "" {
		branch := pi.git.Branch
		if len(branch) > 18 {
			branch = branch[:15] + "..."
		}
		gitStr = branch
		if pi.git.Dirty > 0 {
			gitStr += fmt.Sprintf(" *%d", pi.git.Dirty)
		}
		if pi.git.Ahead > 0 {
			gitStr += fmt.Sprintf(" ↑%d", pi.git.Ahead)
		}
		if pi.git.Behind > 0 {
			gitStr += fmt.Sprintf(" ↓%d", pi.git.Behind)
		}
	}

	// Compose the line
	nameWidth := 28
	langWidth := 8
	gitWidth := 22

	nameCol := fmt.Sprintf("%-*s", nameWidth, name)
	langCol := fmt.Sprintf("%-*s", langWidth, langBadge)
	gitCol := fmt.Sprintf("%-*s", gitWidth, gitStr)

	if isSelected {
		nameCol = selectedItemStyle.Render(nameCol)
		langCol = langStyle.Bold(true).Render(langCol)
		gitCol = footerDescStyle.Bold(true).Render(gitCol)
	} else {
		nameCol = normalItemStyle.Render(nameCol)
		langCol = langStyle.Render(langCol)
		gitCol = footerDescStyle.Render(gitCol)
	}

	line := cursor + nameCol + " " + langCol + " " + gitCol + " " + statusStr

	// Pad to full width
	lineLen := lipgloss.Width(line)
	if lineLen < width {
		line += strings.Repeat(" ", width-lineLen)
	}

	_ = gitWidth // widths used in formatting above
	fmt.Fprint(w, line)
}
