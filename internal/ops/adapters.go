package ops

import (
	"os"

	"github.com/rvanmech/unky-mo/internal/claude"
	moexec "github.com/rvanmech/unky-mo/internal/exec"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// NewContext builds a production Context using real tmux / claude / exec
// seams. Callers (cmd/mo, tui.NewModel) pass a constructed *ttmux.Client.
// MoBinaryPath defaults to os.Executable(); callers can override.
func NewContext(tc *ttmux.Client) *Context {
	ctx := &Context{
		Tmux:         NewTmuxClientAdapter(tc),
		Claude:       NewDefaultClaudeReader(),
		Cmd:          moexec.DefaultCommander,
		SidebarWidth: 42,
	}
	if p, err := os.Executable(); err == nil {
		ctx.MoBinaryPath = p
	}
	return ctx
}

// NewTmuxClientAdapter wraps *ttmux.Client to satisfy TmuxClient. Exported
// so tests constructing a Context from a real tmux client can wrap it too.
func NewTmuxClientAdapter(c *ttmux.Client) TmuxClient {
	if c == nil {
		return nil
	}
	return &tmuxClientAdapter{c: c}
}

type tmuxClientAdapter struct {
	c *ttmux.Client
}

func (a *tmuxClientAdapter) SessionName() string { return a.c.SessionName }
func (a *tmuxClientAdapter) CreateWindow(name, cwd string) (string, error) {
	return a.c.CreateWindow(name, cwd)
}
func (a *tmuxClientAdapter) DetachClient() error              { return a.c.DetachClient() }
func (a *tmuxClientAdapter) KillWindow(target string) error   { return a.c.KillWindow(target) }
func (a *tmuxClientAdapter) ListWindows() ([]ttmux.Window, error) {
	return a.c.ListWindows()
}
func (a *tmuxClientAdapter) PaneID(target string) (string, error) { return a.c.PaneID(target) }
func (a *tmuxClientAdapter) PanePIDs() (map[int]bool, error)      { return a.c.PanePIDs() }
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

// NewDefaultClaudeReader returns the production ClaudeReader implementation.
func NewDefaultClaudeReader() ClaudeReader { return defaultClaudeReader{} }

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
