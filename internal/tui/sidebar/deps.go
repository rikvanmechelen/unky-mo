package sidebar

import (
	"github.com/rvanmech/unky-mo/internal/claude"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// TmuxClient is the subset of *tmux.Client the sidebar reaches for. Kept as
// an interface so tests can inject a mock without spinning up a real tmux
// server.
//
//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_sidebar github.com/rvanmech/unky-mo/internal/tui/sidebar TmuxClient,ClaudeReader,WindowResolver
type TmuxClient interface {
	SessionName() string
	BindKey(table, key, command string, args ...string) error
	BreakPane(paneID string) error
	BreakPaneToSession(paneID, session string) error
	ConfigureStatusFormat()
	DisplayPopupAttach(session, cwd, title string) error
	IsPaneAlive(paneID string) bool
	JoinPaneConsolidate(srcPaneID, dstPaneID string) error
	JoinPaneVertical(srcPaneID, dstTarget string) error
	KillPane(paneID string) error
	NewDetachedSession(name, cwd string) error
	SelectPane(target string) error
	SelectWindowByPane(paneID string) error
	SessionExistsNamed(name string) bool
	SetSessionOption(session, option, value string) error
	SetWindowOption(target, option, value string) error
	SplitWindowHorizontal(target, cwd string) (string, error)
	SwitchToWindow(target string) error
	WindowPanePIDs(target string) (map[int]bool, error)
}

// ClaudeReader is the subset of internal/claude the sidebar reads from.
type ClaudeReader interface {
	ActiveShellsForSession(projectPath string) []claude.ActiveShell
	FormatShellCommand(cmd string, maxLen int) string
	IsDescendantOf(pid int, hostPIDs map[int]bool) bool
	LiveSessions() ([]claude.Session, error)
	SessionForPath(path string) *claude.Session
	SessionsForPath(path string) []claude.Session
	ProjectsDirForPath(projectPath string) string
}

// WindowResolver answers "what tmux window is this sidebar running in?". The
// production impl reads $TMUX_PANE + shells out to `tmux display-message`.
// Tests provide a FakeWindowResolver that returns a fixed (name, id) pair so
// they don't need a live tmux server.
type WindowResolver interface {
	ResolveOwnWindow() (name, id string)
}

// defaultWindowResolver wraps the real tmux + env lookups used by
// resolveOwnWindowName and resolveOwnWindowID. It is the production impl
// wired into NewModel.
type defaultWindowResolver struct{}

// NewDefaultWindowResolver returns the production WindowResolver.
func NewDefaultWindowResolver() WindowResolver { return defaultWindowResolver{} }

func (defaultWindowResolver) ResolveOwnWindow() (name, id string) {
	return resolveOwnWindowName(), resolveOwnWindowID()
}

// FakeWindowResolver is a static implementation for tests.
type FakeWindowResolver struct {
	Name string
	ID   string
}

// ResolveOwnWindow satisfies WindowResolver.
func (f FakeWindowResolver) ResolveOwnWindow() (string, string) { return f.Name, f.ID }

// tmuxClientAdapter wraps *ttmux.Client to satisfy TmuxClient (primarily
// because SessionName is a struct field and interfaces need methods).
type tmuxClientAdapter struct {
	c *ttmux.Client
}

func newTmuxClientAdapter(c *ttmux.Client) TmuxClient { return &tmuxClientAdapter{c: c} }

func (a *tmuxClientAdapter) SessionName() string { return a.c.SessionName }
func (a *tmuxClientAdapter) BindKey(table, key, command string, args ...string) error {
	return a.c.BindKey(table, key, command, args...)
}
func (a *tmuxClientAdapter) BreakPane(paneID string) error {
	return a.c.BreakPane(paneID)
}
func (a *tmuxClientAdapter) BreakPaneToSession(paneID, session string) error {
	return a.c.BreakPaneToSession(paneID, session)
}
func (a *tmuxClientAdapter) ConfigureStatusFormat() { a.c.ConfigureStatusFormat() }
func (a *tmuxClientAdapter) DisplayPopupAttach(session, cwd, title string) error {
	return a.c.DisplayPopupAttach(session, cwd, title)
}
func (a *tmuxClientAdapter) IsPaneAlive(paneID string) bool {
	return a.c.IsPaneAlive(paneID)
}
func (a *tmuxClientAdapter) JoinPaneConsolidate(src, dst string) error {
	return a.c.JoinPaneConsolidate(src, dst)
}
func (a *tmuxClientAdapter) JoinPaneVertical(src, dst string) error {
	return a.c.JoinPaneVertical(src, dst)
}
func (a *tmuxClientAdapter) KillPane(paneID string) error {
	return a.c.KillPane(paneID)
}
func (a *tmuxClientAdapter) NewDetachedSession(name, cwd string) error {
	return a.c.NewDetachedSession(name, cwd)
}
func (a *tmuxClientAdapter) SelectPane(target string) error {
	return a.c.SelectPane(target)
}
func (a *tmuxClientAdapter) SelectWindowByPane(paneID string) error {
	return a.c.SelectWindowByPane(paneID)
}
func (a *tmuxClientAdapter) SessionExistsNamed(name string) bool {
	return a.c.SessionExistsNamed(name)
}
func (a *tmuxClientAdapter) SetSessionOption(session, option, value string) error {
	return a.c.SetSessionOption(session, option, value)
}
func (a *tmuxClientAdapter) SetWindowOption(target, option, value string) error {
	return a.c.SetWindowOption(target, option, value)
}
func (a *tmuxClientAdapter) SplitWindowHorizontal(target, cwd string) (string, error) {
	return a.c.SplitWindowHorizontal(target, cwd)
}
func (a *tmuxClientAdapter) SwitchToWindow(target string) error {
	return a.c.SwitchToWindow(target)
}
func (a *tmuxClientAdapter) WindowPanePIDs(target string) (map[int]bool, error) {
	return a.c.WindowPanePIDs(target)
}

// defaultClaudeReader delegates every method to the package-level function.
type defaultClaudeReader struct{}

func (defaultClaudeReader) ActiveShellsForSession(projectPath string) []claude.ActiveShell {
	return claude.ActiveShellsForSession(projectPath)
}
func (defaultClaudeReader) FormatShellCommand(cmd string, maxLen int) string {
	return claude.FormatShellCommand(cmd, maxLen)
}
func (defaultClaudeReader) IsDescendantOf(pid int, hostPIDs map[int]bool) bool {
	return claude.IsDescendantOf(pid, hostPIDs)
}
func (defaultClaudeReader) LiveSessions() ([]claude.Session, error) {
	return claude.LiveSessions()
}
func (defaultClaudeReader) SessionForPath(path string) *claude.Session {
	return claude.SessionForPath(path)
}
func (defaultClaudeReader) SessionsForPath(path string) []claude.Session {
	return claude.SessionsForPath(path)
}
func (defaultClaudeReader) ProjectsDirForPath(p string) string {
	return claude.ProjectsDirForPath(p)
}
