package tui

import (
	"time"

	gh "github.com/rvanmech/unky-mo/internal/github"
	"github.com/rvanmech/unky-mo/internal/notify"
	"github.com/rvanmech/unky-mo/internal/project"
	"github.com/rvanmech/unky-mo/internal/status"
	moSync "github.com/rvanmech/unky-mo/internal/sync"
	"github.com/rvanmech/unky-mo/internal/tickets"
	"github.com/rvanmech/unky-mo/internal/usage"
)

// SessionStatus represents the state of a Claude session for a project.
type SessionStatus int

const (
	StatusNone       SessionStatus = iota
	StatusActive                   // Claude is processing
	StatusIdle                     // Waiting for user input
	StatusPermission               // Needs permission approval
	StatusQuestion                 // Blocked on an interactive tool (e.g. AskUserQuestion)
	StatusExternal                 // Live claude running outside mo's tmux — not joinable, offer to import
)

// Screen identifies which view is active.
type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenProject
	ScreenHelp
	ScreenTicket
)

// sessionTickMsg triggers a session status refresh.
type sessionTickMsg time.Time

// usageTickMsg triggers a Claude usage refresh. 60s is well below the cache
// TTL of the endpoint but keeps the reset countdown in the dashboard fresh
// across the longer sessionTick cadence.
type usageTickMsg time.Time

// usageRefreshMsg carries the result of a usage.Fetch call.
type usageRefreshMsg struct {
	snap    usage.Snapshot
	authErr bool
}

// ticketsTickMsg fires on the user-configurable refresh cadence (default 5m).
type ticketsTickMsg time.Time

// ticketsRefreshMsg carries the outcome of a tickets fetch across all
// providers. Errors are kept per-provider so one unhealthy provider doesn't
// hide a healthy one.
type ticketsRefreshMsg struct {
	groups  []tickets.BucketGroup
	results []tickets.FetchResult
}

// gitStatusMsg carries refreshed git statuses for all projects.
type gitStatusMsg map[string]project.GitStatus

// projectsRefreshMsg carries a freshly-scanned project list. Handled in
// Update to merge new projects into the dashboard without losing status dots.
type projectsRefreshMsg struct {
	projects []project.Project
}

// notificationMsg wraps a notification received from the Unix socket.
type notificationMsg notify.Notification

// statusChangeMsg wraps a status change from the status manager.
type statusChangeMsg status.StatusChange

// suspendCompleteMsg signals that SuspendAll finished.
type suspendCompleteMsg struct {
	err error
}

// dashRightSection identifies the focused section in the dashboard right
// panel when the right panel itself is focused.
type dashRightSection int

const (
	dashRightSessions dashRightSection = iota
	dashRightTickets
)

// sessionRefreshMsg carries the per-session view list plus the CWD-keyed
// import-state caches (unchanged — the import-external flow looks them up
// by CWD via dashSessionItem.ProjectPath).
type sessionRefreshMsg struct {
	views            []sessionView
	externalPIDs     map[string]int    // path → orphan PID to kill on import
	externalSessions map[string]string // path → sessionID to resume
}

type statusMsgEvent string

// stickyStatusMsgEvent is a status message that stays until a keypress
// even though it isn't an error.
type stickyStatusMsgEvent string

type clearStatusMsg struct{}

// worktreeExistsMsg signals that a worktree for the requested branch already
// exists. The TUI opens a multi-option prompt so the user can choose to focus
// the existing session, remove and recreate, or cancel.
type worktreeExistsMsg struct {
	branch       string
	worktreePath string
	prBranch     bool // true when triggered from a PR (needs git fetch + reset on recreate)
}

// worktreeCreatedMsg signals that a new worktree was created; Update refreshes
// detailWorktrees and surfaces the carried status string.
type worktreeCreatedMsg struct{ status string }

// branchesChangedMsg signals that local branch state (checkouts, stashes,
// new branches) has moved and the detail view's branch list should be
// rebuilt. A non-empty status is surfaced to the user.
type branchesChangedMsg struct{ status string }

// prFetchMsg carries the result of an async PR fetch.
type prFetchMsg struct {
	prs []gh.PullRequest
	err error
}

// prDetailMsg carries the result of fetching a single PR's detail.
type prDetailMsg struct {
	detail *gh.PRDetail
	err    error
}

// syncPullMsg carries the result of an auto-sync-pull when opening a project.
type syncPullMsg struct {
	synced     map[string]moSync.SessionMeta // sync metadata for sessions pulled into a local path, keyed by session ID
	remoteOnly map[string]moSync.SessionMeta // sessions for branches with no local worktree, keyed by branch name
	pulled     bool                          // true if a new session was pulled
	err        error                         // non-nil when sync is configured but failed
}

// detailLoadedMsg carries a project's worktrees and branches, listed off
// the event loop (loadDetail).
type detailLoadedMsg struct {
	path      string
	worktrees []project.Worktree
	branches  []project.Branch
	recap     bool
}

// newProjectCreatedMsg is returned by doCreateProject on success.
type newProjectCreatedMsg struct {
	name string
	path string
}

// sessionLiftedMsg signals a completed lift. Distinct from worktreeCreatedMsg
// because its handler also kicks off a session refresh so the newly-relocated
// JSONL (now under the new worktree's encoded-cwd dir) attributes to the new
// branch immediately, without the user having to leave and re-enter
// ScreenProject.
type sessionLiftedMsg struct{ status string }

// sessionImportedMsg signals that an external claude was successfully pulled
// into mo's tmux. Update handles it by reporting the status *and* kicking
// off an immediate sessionRefresh so the main TUI + state file (and the
// sidebar that polls it) pick the new window up without waiting for the
// next 5s tick.
type sessionImportedMsg struct{ status string }
