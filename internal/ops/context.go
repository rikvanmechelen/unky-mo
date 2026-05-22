// Package ops contains the domain operations the TUI and CLI both call
// into. Each operation is a plain function taking a *Context and typed
// parameters — no bubbletea types, no UI state. The CLI wraps them in
// cobra RunE bodies; the TUI wraps them in tea.Cmd closures.
//
// The package is the testable heart of the app: every operation can be
// unit-tested against the gomock-generated fakes under mocks/.
package ops

import (
	"github.com/rvanmech/unky-mo/internal/claude"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// Context holds the side-effect seams every operation needs. Constructed
// once at startup and threaded through every call. No UI state belongs here.
type Context struct {
	Tmux   TmuxClient       // tmux command wrapper (interface for testability)
	Claude ClaudeReader     // claude session reader (interface for testability)
	Cmd    moexec.Commander // shared exec seam for git / gh / any shell-outs
	// MoBinaryPath is the path to the `mo` executable so ops can launch the
	// sidebar (`mo sidebar`). Defaults to os.Executable() at construction.
	MoBinaryPath string
	// SidebarWidth is the tmux pane column count for the sidebar split.
	// Default 42 (the TUI's historical value).
	SidebarWidth int
}

// TmuxClient is the subset of *ttmux.Client that ops actually uses. The
// interface lives here (not in internal/tui) so the tui package can depend
// on it without circling back on ops.
//
//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_ops github.com/rvanmech/unky-mo/internal/ops TmuxClient,ClaudeReader
type TmuxClient interface {
	SessionName() string
	CreateWindow(name, cwd string) (string, error)
	DetachClient() error
	KillPane(paneID string) error
	KillSession(name string) error
	KillWindow(target string) error
	ListSessions() ([]string, error)
	ListWindows() ([]ttmux.Window, error)
	PaneID(target string) (string, error)
	PanePIDs() (map[int]bool, error)
	RenameWindow(target, newName string) error
	SelectPane(target string) error
	SendKeys(target, keys string) error
	SendRawKeys(target, keys string) error
	SetWindowHook(target, hookName, command string)
	SetWindowOption(target, option, value string) error
	SplitWindow(target string, cols int, cwd, command string) (string, error)
	SwitchToWindow(target string) error
	WindowExists(name string) bool
	WindowPanePIDs(target string) (map[int]bool, error)
	ListWindowPanes(target string) ([]ttmux.PaneInfo, error)
}

// ClaudeReader is the subset of the internal/claude package that ops reads
// from. Tests substitute canned session data.
type ClaudeReader interface {
	LiveSessions() ([]claude.Session, error)
	SessionForPath(path string) *claude.Session
	SessionsForPath(path string) []claude.Session
	IsAlive(pid int) bool
	IsDescendantOf(pid int, hostPIDs map[int]bool) bool
	CustomTitleFor(projectPath, sessionID string) string
	LastMessages(projectPath, sessionID string, count int) []claude.SessionMessage
	RecentSessions(projectPath string, maxResults int) []claude.RecentSession
	ProjectsDirForPath(projectPath string) string
}
