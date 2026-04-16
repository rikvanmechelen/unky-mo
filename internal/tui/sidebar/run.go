package sidebar

import tea "github.com/charmbracelet/bubbletea"

// Run starts the sidebar TUI. It should be run in a narrow tmux pane.
func Run(sessionName, stateFile string) error {
	m := NewModel(sessionName, stateFile)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
