package tui

import (
	"github.com/rvanmech/unky-mo/internal/claude"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// TmuxClient is the subset of *tmux.Client that the TUI actually uses. Kept
// as an interface so tests can inject a gomock fake and avoid spinning up a
// real tmux server.
//
//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_tui github.com/rvanmech/unky-mo/internal/tui TmuxClient,ClaudeReader
type TmuxClient interface {
	SessionName() string
	CreateWindow(name, cwd string) (string, error)
	DetachClient() error
	KillWindow(target string) error
	ListWindows() ([]ttmux.Window, error)
	PaneID(target string) (string, error)
	PanePIDs() (map[int]bool, error)
	RenameWindow(target, newName string) error
	SelectPane(target string) error
	SendKeys(target, keys string) error
	SendRawKeys(target, keys string) error
	SetWindowHook(target, hookName, command string)
	SplitWindow(target string, cols int, cwd, command string) (string, error)
	SwitchToWindow(target string) error
	WindowExists(name string) bool
	WindowPanePIDs(target string) (map[int]bool, error)
}

// ClaudeReader is the subset of the internal/claude package the TUI reads
// from. Wrapping the package-level functions behind this interface lets
// tests substitute canned session data and skip the filesystem.
type ClaudeReader interface {
	LiveSessions() ([]claude.Session, error)
	SessionForPath(path string) *claude.Session
	SessionsForPath(path string) []claude.Session
	IsAlive(pid int) bool
	IsDescendantOf(pid int, hostPIDs map[int]bool) bool
	IsSessionIdle(projectPath, sessionID string) bool
	CustomTitleFor(projectPath, sessionID string) string
	LastMessages(projectPath, sessionID string, count int) []claude.SessionMessage
	RecentSessions(projectPath string, maxResults int) []claude.RecentSession
	ProjectsDirForPath(projectPath string) string
}

// tmuxClientAdapter wraps *ttmux.Client to satisfy TmuxClient. Only the
// field→method adapter for SessionName is non-trivial; the rest is
// passthrough (Go structural typing would handle these if the interface
// declared a method with the same name as a field, but it can't, so we
// wrap explicitly).
type tmuxClientAdapter struct {
	c *ttmux.Client
}

func newTmuxClientAdapter(c *ttmux.Client) TmuxClient {
	if c == nil {
		return nil
	}
	return &tmuxClientAdapter{c: c}
}

func (a *tmuxClientAdapter) SessionName() string { return a.c.SessionName }

func (a *tmuxClientAdapter) CreateWindow(name, cwd string) (string, error) {
	return a.c.CreateWindow(name, cwd)
}
func (a *tmuxClientAdapter) DetachClient() error        { return a.c.DetachClient() }
func (a *tmuxClientAdapter) PanePIDs() (map[int]bool, error) {
	return a.c.PanePIDs()
}
func (a *tmuxClientAdapter) KillWindow(target string) error { return a.c.KillWindow(target) }
func (a *tmuxClientAdapter) ListWindows() ([]ttmux.Window, error) {
	return a.c.ListWindows()
}
func (a *tmuxClientAdapter) PaneID(target string) (string, error) { return a.c.PaneID(target) }
func (a *tmuxClientAdapter) RenameWindow(target, newName string) error {
	return a.c.RenameWindow(target, newName)
}
func (a *tmuxClientAdapter) SelectPane(target string) error { return a.c.SelectPane(target) }
func (a *tmuxClientAdapter) SendKeys(target, keys string) error {
	return a.c.SendKeys(target, keys)
}
func (a *tmuxClientAdapter) SendRawKeys(target, keys string) error {
	return a.c.SendRawKeys(target, keys)
}
func (a *tmuxClientAdapter) SetWindowHook(target, hookName, command string) {
	a.c.SetWindowHook(target, hookName, command)
}
func (a *tmuxClientAdapter) SplitWindow(target string, cols int, cwd, command string) (string, error) {
	return a.c.SplitWindow(target, cols, cwd, command)
}
func (a *tmuxClientAdapter) SwitchToWindow(target string) error {
	return a.c.SwitchToWindow(target)
}
func (a *tmuxClientAdapter) WindowExists(name string) bool { return a.c.WindowExists(name) }
func (a *tmuxClientAdapter) WindowPanePIDs(target string) (map[int]bool, error) {
	return a.c.WindowPanePIDs(target)
}

// defaultClaudeReader delegates every method to the package-level function
// of the same name in internal/claude.
type defaultClaudeReader struct{}

func (defaultClaudeReader) LiveSessions() ([]claude.Session, error) {
	return claude.LiveSessions()
}
func (defaultClaudeReader) SessionForPath(path string) *claude.Session {
	return claude.SessionForPath(path)
}
func (defaultClaudeReader) SessionsForPath(path string) []claude.Session {
	return claude.SessionsForPath(path)
}
func (defaultClaudeReader) IsAlive(pid int) bool { return claude.IsAlive(pid) }
func (defaultClaudeReader) IsDescendantOf(pid int, hostPIDs map[int]bool) bool {
	return claude.IsDescendantOf(pid, hostPIDs)
}
func (defaultClaudeReader) IsSessionIdle(projectPath, sessionID string) bool {
	return claude.IsSessionIdle(projectPath, sessionID)
}
func (defaultClaudeReader) CustomTitleFor(projectPath, sessionID string) string {
	return claude.CustomTitleFor(projectPath, sessionID)
}
func (defaultClaudeReader) LastMessages(projectPath, sessionID string, count int) []claude.SessionMessage {
	return claude.LastMessages(projectPath, sessionID, count)
}
func (defaultClaudeReader) RecentSessions(projectPath string, maxResults int) []claude.RecentSession {
	return claude.RecentSessions(projectPath, maxResults)
}
func (defaultClaudeReader) ProjectsDirForPath(projectPath string) string {
	return claude.ProjectsDirForPath(projectPath)
}

// NewDefaultClaudeReader returns the production ClaudeReader implementation.
// Exposed so callers can construct a Model without reaching into the tui
// package internals.
func NewDefaultClaudeReader() ClaudeReader { return defaultClaudeReader{} }
