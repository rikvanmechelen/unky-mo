package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Enter       key.Binding
	Back        key.Binding
	New         key.Binding
	Attach      key.Binding
	Resume      key.Binding
	NewWorktree key.Binding
	Help        key.Binding
	Restart     key.Binding
	Quit        key.Binding
}

var keys = keyMap{
	Enter: key.NewBinding(
		key.WithKeys("enter", "right", "l"),
		key.WithHelp("enter", "open"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc", "backspace", "left", "h"),
		key.WithHelp("esc", "back"),
	),
	New: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new session"),
	),
	Attach: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "attach"),
	),
	Resume: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	),
	NewWorktree: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "new worktree"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Restart: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "restart"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}
