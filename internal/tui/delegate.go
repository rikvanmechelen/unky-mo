package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	default:
		statusStr = statusNone.Render(symbolNone + " no session")
	}

	// Compose the line
	nameWidth := 28
	langWidth := 8
	statusWidth := 16

	nameCol := fmt.Sprintf("%-*s", nameWidth, name)
	langCol := fmt.Sprintf("%-*s", langWidth, langBadge)

	if isSelected {
		nameCol = selectedItemStyle.Render(nameCol)
		langCol = langStyle.Copy().Bold(true).Render(langCol)
	} else {
		nameCol = normalItemStyle.Render(nameCol)
		langCol = langStyle.Render(langCol)
	}

	line := cursor + nameCol + " " + langCol + " " + statusStr

	// Pad to full width
	lineLen := lipgloss.Width(line)
	if lineLen < width {
		line += strings.Repeat(" ", width-lineLen)
	}

	_ = statusWidth // used implicitly in statusStr rendering
	fmt.Fprint(w, line)
}
