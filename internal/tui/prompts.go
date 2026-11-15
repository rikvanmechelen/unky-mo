package tui

import "charm.land/bubbles/v2/textinput"

// The TUI's prompts: each open one is a non-nil pointer on Model holding
// its own state, so a flow's fields can't leak into another's
// (docs/plans/review-remediation.md, step 2.2).

// newProjectPrompt asks for the folder name of a new project (N on the
// dashboard), created under parentDir (the first workspace dir).
type newProjectPrompt struct {
	input     textinput.Model
	parentDir string
}
