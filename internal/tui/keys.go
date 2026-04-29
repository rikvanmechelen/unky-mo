package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Enter            key.Binding
	AgentLaunch      key.Binding // "A" — pick which coding agent to launch
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
	Refresh          key.Binding // "ctrl+r" — force-refresh in-process state (sessions, branches, state file)
	Restart          key.Binding // ctrl+alt+r or ctrl+cmd+r — exec freshly-installed binary + restart sidebars
	Suspend          key.Binding // "s" — suspend (tmux detach-client); session keeps running
	Cleanup          key.Binding // "x" — remove the worktree / branch under cursor
	Quit             key.Binding
}

var keys = keyMap{
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "open"),
	),
	AgentLaunch: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "agent"),
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
	Refresh: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "refresh"),
	),
	Restart: key.NewBinding(
		// Bubbletea v2 modifier order: ctrl, alt, shift, meta, hyper, super.
		// "super" is the Command key on macOS.
		key.WithKeys("ctrl+alt+r", "ctrl+super+r"),
		key.WithHelp("ctrl+alt+r", "restart"),
	),
	Suspend: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "suspend"),
	),
	Cleanup: key.NewBinding(
		key.WithKeys("x"),
		key.WithHelp("x", "remove"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}
