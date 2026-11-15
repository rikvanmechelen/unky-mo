package sidebar

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	moexec "github.com/rvanmech/unky-mo/internal/exec"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/gitfiles"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
)

// TmuxClient is the subset of *tmux.Client the sidebar reaches for. Kept as
// an interface so tests can inject a mock without spinning up a real tmux
// server.
//
//go:generate mockgen -destination=mocks/mock_deps.go -package=mock_sidebar github.com/rvanmech/unky-mo/internal/tui/sidebar TmuxClient,ClaudeReader,WindowResolver,GitReader,Opener
type TmuxClient interface {
	SessionName() string
	BindKey(table, key, command string, args ...string) error
	UnbindKey(table, key string) error
	BreakPane(paneID string) error
	BreakPaneToSession(paneID, session string) error
	ConfigureStatusFormat()
	DisplayPopupAttach(session, cwd, title string) error
	IsPaneAlive(paneID string) bool
	JoinPaneConsolidate(srcPaneID, dstPaneID string) error
	JoinPaneVertical(srcPaneID, dstTarget string) error
	KillPane(paneID string) error
	NewDetachedSession(name, cwd string) (string, error)
	SelectPane(target string) error
	SelectWindowByPane(paneID string) error
	SessionExistsNamed(name string) bool
	SetSessionOption(session, option, value string) error
	SetWindowOption(target, option, value string) error
	SplitWindowHorizontal(target, cwd string) (string, error)
	SwitchToWindow(target string) error
	WindowPanePIDs(target string) (map[int]bool, error)
	ListWindowPanes(target string) ([]ttmux.PaneInfo, error)
	ListSessionPanes(session string) ([]ttmux.TermPane, error)
	WindowOption(target, option string) string
}

// ClaudeReader is the subset of internal/claude the sidebar reads from.
type ClaudeReader interface {
	// ActiveShells returns the Bash-tool subprocesses for a specific Claude
	// PID. The sidebar must pass its OWN session's PID — not any session
	// matching its CWD — because two concurrent sessions sharing a CWD would
	// otherwise both display the same shell list.
	ActiveShells(claudePID int) []claude.ActiveShell
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
func (a *tmuxClientAdapter) UnbindKey(table, key string) error {
	return a.c.UnbindKey(table, key)
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
func (a *tmuxClientAdapter) NewDetachedSession(name, cwd string) (string, error) {
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
func (a *tmuxClientAdapter) ListSessionPanes(session string) ([]ttmux.TermPane, error) {
	return a.c.ListSessionPanes(session)
}
func (a *tmuxClientAdapter) WindowOption(target, option string) string {
	return a.c.WindowOption(target, option)
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
func (a *tmuxClientAdapter) ListWindowPanes(target string) ([]ttmux.PaneInfo, error) {
	return a.c.ListWindowPanes(target)
}

// defaultClaudeReader delegates every method to the package-level function.
type defaultClaudeReader struct{}

func (defaultClaudeReader) ActiveShells(claudePID int) []claude.ActiveShell {
	return claude.ActiveShells(claudePID)
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

// GitReader reads the window's checkout: its changes (the Files section)
// and every file in it (the full tree). Both list paths from the repo root.
type GitReader interface {
	Changes(dir string) (*gitfiles.Changes, error)
	Tree(dir string) ([]string, error)
}

// gitTimeout bounds one read of the checkout on the sidebar's tick.
const gitTimeout = 5 * time.Second

type defaultGitReader struct{}

func (defaultGitReader) Changes(dir string) (*gitfiles.Changes, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return gitfiles.GetChanges(ctx, moexec.DefaultCommander, dir)
}

func (defaultGitReader) Tree(dir string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	_, paths, err := gitfiles.Tree(ctx, moexec.DefaultCommander, dir)
	return paths, err
}

// Opener opens a checkout in an editor outside the terminal (o): VS Code
// with the file focused, else the system's default.
type Opener interface {
	// Open opens dir, focusing file (relative to dir) when it isn't "".
	// It says where it opened, or returns an error when nothing could.
	Open(dir, file string) (string, error)
}

type defaultOpener struct{}

func (defaultOpener) Open(dir, file string) (string, error) {
	target := dir
	codeArgs := []string{dir}
	if file != "" {
		target = filepath.Join(dir, file)
		codeArgs = append(codeArgs, "--goto", target+":1:1")
	}
	if err := exec.Command("code", codeArgs...).Run(); err == nil {
		return "VS Code", nil
	}
	if err := exec.Command("open", target).Run(); err == nil {
		if file == "" {
			return "Finder", nil
		}
		return "default editor", nil
	}
	return "", errors.New("no external editor found")
}
