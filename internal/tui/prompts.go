package tui

import (
	"charm.land/bubbles/v2/textinput"
	"github.com/rvanmech/unky-mo/internal/claude"
)

// The TUI's prompts: each open one is a non-nil pointer on Model holding
// its own state, so a flow's fields can't leak into another's
// (docs/plans/review-remediation.md, step 2.2).

// newProjectPrompt asks for the folder name of a new project (N on the
// dashboard), created under parentDir (the first workspace dir).
type newProjectPrompt struct {
	input     textinput.Model
	parentDir string
}

// worktreePrompt asks for the branch of a new worktree (W).
type worktreePrompt struct {
	input textinput.Model
}

// liftPrompt is the lift-to-new-worktree flow (w on a session row). The
// source session is captured when it opens. Once a branch is entered and
// the source has uncommitted changes, dirty asks whether to carry them.
type liftPrompt struct {
	input        textinput.Model
	sessionID    string
	sourcePath   string
	sourcePID    int    // 0 when the source session is historical
	sourceWindow string // tmux window to kill after SIGTERM; "" skips
	branch       string // the new branch, once entered
	dirty        bool
}

// newSessionPrompt is the switch / park+new / concurrent menu shown when a
// session is already running at the target (n, or enter on a historical
// session whose primary window is busy).
type newSessionPrompt struct {
	project       string
	branch        string // "" for the main checkout
	cwd           string
	primaryWin    string // the primary window's name (no suffix)
	livePID       int    // the claude to park on p
	liveID        string // its session id
	resumeSession string // set: p and c resume this session instead of starting fresh
}

// cleanupMenu removes a branch's worktree and/or the branch (x). Stage
// "kill" first confirms stopping the sessions running in the worktree;
// "action" picks what to remove.
type cleanupMenu struct {
	stage        string // "kill" | "action"
	projectPath  string
	projectName  string
	branch       string
	worktreePath string           // "" for a plain branch row
	sessions     []claude.Session // live sessions to stop, captured when it opened
}
