package sidebar

import (
	"github.com/rvanmech/unky-mo/internal/claude"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// TmuxClient is the subset of *tmux.Client the sidebar reaches for. Kept as
// an interface so tests can inject a mock without spinning up a real tmux
// server.
//
//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_sidebar github.com/rvanmech/unky-mo/internal/tui/sidebar TmuxClient,ClaudeReader
type TmuxClient interface {
	SessionName() string
	BreakPane(paneID string) error
	ConfigureStatusFormat()
	IsPaneAlive(paneID string) bool
	JoinPaneConsolidate(srcPaneID, dstPaneID string) error
	JoinPaneVertical(srcPaneID, dstTarget string) error
	KillPane(paneID string) error
	SelectPane(target string) error
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

// tmuxClientAdapter wraps *ttmux.Client to satisfy TmuxClient (primarily
// because SessionName is a struct field and interfaces need methods).
type tmuxClientAdapter struct {
	c *ttmux.Client
}

func newTmuxClientAdapter(c *ttmux.Client) TmuxClient { return &tmuxClientAdapter{c: c} }

func (a *tmuxClientAdapter) SessionName() string { return a.c.SessionName }
func (a *tmuxClientAdapter) BreakPane(paneID string) error {
	return a.c.BreakPane(paneID)
}
func (a *tmuxClientAdapter) ConfigureStatusFormat() { a.c.ConfigureStatusFormat() }
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
func (a *tmuxClientAdapter) SelectPane(target string) error {
	return a.c.SelectPane(target)
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
