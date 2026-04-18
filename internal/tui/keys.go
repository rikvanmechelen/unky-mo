package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Enter            key.Binding
	Back             key.Binding
	New              key.Binding
	Attach           key.Binding
	Resume           key.Binding
	NewWorktree      key.Binding // "w" — create worktree for the branch under cursor
	NewBranchPrompt  key.Binding // "W" — type a brand-new branch name
	OpenInMain       key.Binding // "m" — checkout branch in main repo (safe)
	OpenInMainForce  key.Binding // "M" — stash then checkout in main repo
	Tab              key.Binding
	OpenInBrowser    key.Binding
	Checkout         key.Binding
	Help             key.Binding
	Restart          key.Binding
	Quit             key.Binding
}

var keys = keyMap{
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "open"),
	),
	Back: key.NewBinding(
		key.WithKeys("esc", "backspace"),
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
		key.WithHelp("w", "worktree"),
	),
	NewBranchPrompt: key.NewBinding(
		key.WithKeys("W"),
		key.WithHelp("W", "new branch"),
	),
	OpenInMain: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "main"),
	),
	OpenInMainForce: key.NewBinding(
		key.WithKeys("M"),
		key.WithHelp("M", "stash+main"),
	),
	Tab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch panel"),
	),
	OpenInBrowser: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open in browser"),
	),
	Checkout: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "checkout branch"),
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
