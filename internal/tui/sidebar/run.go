package sidebar

import tea "charm.land/bubbletea/v2"

// RunOpts configures the sidebar TUI.
type RunOpts struct {
	SessionName string
	StateFile   string
	InstanceID  string // mo-generated instance ID (from --instance-id flag); may be empty for pre-refactor launches
}

// Run starts the sidebar TUI. It should be run in a narrow tmux pane.
func Run(sessionName, stateFile string) error {
	return RunWithOpts(RunOpts{SessionName: sessionName, StateFile: stateFile})
}

// RunWithOpts starts the sidebar TUI with full options.
func RunWithOpts(opts RunOpts) error {
	m := NewModel(opts.SessionName, opts.StateFile)
	if opts.InstanceID != "" {
		m.instanceID = opts.InstanceID
	}
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}
