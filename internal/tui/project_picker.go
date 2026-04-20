package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// pickerItem wraps a Mo project for the bubbles/list picker. Kept separate
// from ProjectItem (the dashboard list item) so we can render it without
// the session-status machinery.
type pickerItem struct {
	name     string
	path     string
	language string
}

func (i pickerItem) Title() string       { return i.name }
func (i pickerItem) Description() string { return i.path }
func (i pickerItem) FilterValue() string { return i.name + " " + i.language }

// startProjectPicker builds the picker list and activates it for the given
// Jira project key. Used both for first-time mapping (when config has no
// entry) and override (picker manually re-triggered).
func (m *Model) startProjectPicker(providerName, jiraKey string) {
	items := make([]list.Item, 0, len(m.projects))
	for _, p := range m.projects {
		items = append(items, pickerItem{
			name:     p.Name,
			path:     p.Path,
			language: p.Language,
		})
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)

	l := list.New(items, delegate, pickerListWidth(m.width), pickerListHeight(m.height))
	l.Title = fmt.Sprintf("Map %s → which Mo project?", jiraKey)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.InfiniteScrolling = true

	m.pickerList = l
	m.pickerForProvider = providerName
	m.pickerForJiraKey = jiraKey
	m.pickerActive = true
	m.pickerRememberActive = false
	m.pickerPendingMo = ""
}

// pickerSelectedProject returns the currently highlighted Mo project name
// from the picker, or "" when nothing is selectable.
func (m Model) pickerSelectedProject() string {
	if !m.pickerActive {
		return ""
	}
	sel := m.pickerList.SelectedItem()
	if it, ok := sel.(pickerItem); ok {
		return it.name
	}
	return ""
}

// updateProjectPicker forwards list msgs to the embedded list.Model. Called
// from the main Update only while pickerActive is true and the user hasn't
// yet confirmed a pick (so the remember prompt can take over).
func (m *Model) updateProjectPicker(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.pickerList, cmd = m.pickerList.Update(msg)
	return cmd
}

// renderPickerOverlay renders the picker as a bordered panel. It's drawn
// inline at the bottom of the ticket detail body (not a true overlay — the
// ticket text above still shows for context, which is the spirit of the
// mockup).
func (m Model) renderPickerOverlay(width int) string {
	if !m.pickerActive {
		return ""
	}

	inner := m.pickerList.View()

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorMuted).
		Padding(0, 1).
		Width(pickerListWidth(width) + 4)

	return "\n" + box.Render(inner) + "\n"
}

// pickerListWidth clamps the picker list to a readable chunk of the screen.
func pickerListWidth(screenWidth int) int {
	w := screenWidth - 8
	if w < 40 {
		return 40
	}
	if w > 80 {
		return 80
	}
	return w
}

// pickerListHeight caps the picker rows so it doesn't push the footer off.
func pickerListHeight(screenHeight int) int {
	if screenHeight <= 0 {
		return 10
	}
	h := screenHeight / 2
	if h < 8 {
		return 8
	}
	if h > 15 {
		return 15
	}
	return h
}

// hasProject reports whether the given Mo project name is in the list.
// Used by the picker-confirm path to sanity-check before any mutation.
func (m Model) hasProject(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, p := range m.projects {
		if p.Name == name {
			return true
		}
	}
	return false
}
