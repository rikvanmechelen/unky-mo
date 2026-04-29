package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// MoTermsSession is the dedicated tmux session that holds the sidebar
// terminal tabs when they are not displayed in a project window's drawer.
// A tmux popup can attach to it to show the shells floating, and backtick
// inside that popup is bound (in the popup-keys key table, see
// NewDetachedSession callers) to detach-client — so closing the popup
// preserves the shells.
const MoTermsSession = "mo-terms"

// Client wraps tmux commands for session/window management.
type Client struct {
	SessionName string
	// SocketName — when non-empty, every tmux command runs with `-L <name>`.
	// Leave empty in production to use the default socket; tests set a
	// unique socket name so they can spin up an isolated tmux server.
	SocketName string
}

func NewClient(sessionName string) *Client {
	return &Client{SessionName: sessionName}
}

// tmuxCmd builds an *exec.Cmd for tmux, prepending -L <socket> when the
// Client has a SocketName configured. All methods route through this helper
// so isolated-socket tests stay isolated.
func (c *Client) tmuxCmd(args ...string) *exec.Cmd {
	if c.SocketName != "" {
		full := make([]string, 0, len(args)+2)
		full = append(full, "-L", c.SocketName)
		full = append(full, args...)
		return exec.Command("tmux", full...)
	}
	return exec.Command("tmux", args...)
}

// runTmux executes a tmux command and returns a descriptive error on failure.
func (c *Client) runTmux(args ...string) error {
	cmd := c.tmuxCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// SessionExists checks if the tmux session exists.
func (c *Client) SessionExists() bool {
	return c.tmuxCmd("has-session", "-t", c.SessionName).Run() == nil
}

// SessionExistsNamed checks whether an arbitrary tmux session exists on the
// same socket as this client. SessionExists hardcodes c.SessionName (always
// "mo"); this variant is used for auxiliary sessions like mo-terms.
func (c *Client) SessionExistsNamed(name string) bool {
	return c.tmuxCmd("has-session", "-t", name).Run() == nil
}

// NewDetachedSession creates a new detached tmux session with the given
// name and starting cwd, and returns the pane ID (%N) of its initial
// window. Callers that don't care about that initial pane can ignore the
// returned id. Unlike CreateSession it does not enable mouse or any other
// mo-session-specific options — the caller configures whatever the session
// needs.
func (c *Client) NewDetachedSession(name, cwd string) (string, error) {
	args := []string{"new-session", "-d", "-s", name}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	args = append(args, "-P", "-F", "#{pane_id}")
	cmd := c.tmuxCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("new-session: %s", msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetSessionOption sets a tmux session option (set-option -t <session>).
func (c *Client) SetSessionOption(session, option, value string) error {
	return c.runTmux("set-option", "-t", session, option, value)
}

// BindKey binds a key in the given key table to a tmux command. key is
// passed raw so callers can use tmux notation like "Tab", "BTab", or a
// literal backtick. Extra args go through unchanged.
func (c *Client) BindKey(table, key, command string, args ...string) error {
	full := []string{"bind-key", "-T", table, key, command}
	full = append(full, args...)
	return c.runTmux(full...)
}

// UnbindKey removes a key binding from the given key table. Idempotent —
// unbinding a key that was never bound is a no-op. Used to clean up
// legacy bindings left over by older sidebar versions.
func (c *Client) UnbindKey(table, key string) error {
	return c.runTmux("unbind-key", "-T", table, key)
}

// BreakPaneToSession moves a pane into a new window in the named session,
// without switching focus. Used by the sidebar to park terminal tabs in
// mo-terms when the drawer closes.
func (c *Client) BreakPaneToSession(paneID, sessionName string) error {
	return c.runTmux("break-pane", "-d", "-s", paneID, "-t", sessionName+":")
}

// SelectWindowByPane selects the window containing the given pane. tmux
// resolves pane targets (%N) to the containing window automatically.
func (c *Client) SelectWindowByPane(paneID string) error {
	return c.runTmux("select-window", "-t", paneID)
}

// DisplayPopupAttach opens a floating popup that attaches a nested tmux
// client to the named session. Used by the sidebar's backtick popup so the
// popup renders the persistent mo-terms shells instead of spawning a fresh
// one. The inner tmux invocation inherits this client's SocketName so
// isolated-socket tests stay isolated.
func (c *Client) DisplayPopupAttach(session, cwd, title string) error {
	args := []string{"display-popup", "-E", "-w", "80%", "-h", "80%"}
	if cwd != "" {
		args = append(args, "-d", cwd)
	}
	if title != "" {
		args = append(args, "-T", title)
	}
	args = append(args, "tmux")
	if c.SocketName != "" {
		args = append(args, "-L", c.SocketName)
	}
	args = append(args, "attach-session", "-t", session)
	return c.runTmux(args...)
}

// CreateSession creates a new tmux session (detached) with mouse support enabled.
func (c *Client) CreateSession() error {
	if err := c.runTmux("new-session", "-d", "-s", c.SessionName); err != nil {
		return err
	}
	c.EnableMouse()
	return nil
}

// EnableMouse turns on mouse support for the session (scroll, click panes/windows,
// drag borders). Idempotent — safe to call on every launch. Silently no-ops if the
// session does not exist.
func (c *Client) EnableMouse() {
	c.runTmux("set-option", "-t", c.SessionName, "mouse", "on")
}

// CreateWindow creates a new window in the session. The returned target uses
// the window's stable ID (@N) so that dotted window names (e.g.
// "moma.org.cubed") are never misinterpreted by tmux as pane separators.
func (c *Client) CreateWindow(name, cwd string) (string, error) {
	// Use -a to append after the current window, avoiding index conflicts.
	// -P -F captures the new window's stable ID so the target is unambiguous.
	args := []string{
		"new-window", "-a", "-t", c.SessionName, "-n", name, "-c", cwd,
		"-P", "-F", "#{window_id}",
	}
	cmd := c.tmuxCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("creating window %q: %s", name, msg)
		}
		return "", fmt.Errorf("creating window %q: %w", name, err)
	}
	windowID := strings.TrimSpace(string(out))
	return c.SessionName + ":" + windowID, nil
}

// SendKeys sends keystrokes to a tmux target (window or pane), followed by Enter.
func (c *Client) SendKeys(target, keys string) error {
	return c.runTmux("send-keys", "-t", target, keys, "Enter")
}

// SendRawKeys sends keystrokes to a tmux target without appending Enter.
func (c *Client) SendRawKeys(target, keys string) error {
	return c.runTmux("send-keys", "-t", target, keys)
}

// SwitchToWindow switches the client to the specified window.
func (c *Client) SwitchToWindow(target string) error {
	return c.runTmux("select-window", "-t", target)
}

// DetachClient detaches every client currently attached to the tmux session.
// All processes running inside the session keep running; users can resume
// by running `mo` (or `tmux attach`) again.
func (c *Client) DetachClient() error {
	return c.runTmux("detach-client", "-s", c.SessionName)
}

// ListWindows returns the names of windows in the session.
func (c *Client) ListWindows() ([]Window, error) {
	cmd := c.tmuxCmd("list-windows", "-t", c.SessionName, "-F", "#{window_id}:#{window_index}:#{window_name}:#{@mo_instance_id}:#{@mo_agent}:#{pane_current_path}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return parseWindowList(out), nil
}

// KillWindow kills a specific window.
func (c *Client) KillWindow(target string) error {
	return c.runTmux("kill-window", "-t", target)
}

// ListSessions returns the names of all tmux sessions on this client's
// socket. Used by the main TUI to sweep orphaned mo-terms-* sessions
// whose paired project window no longer exists.
func (c *Client) ListSessions() ([]string, error) {
	cmd := c.tmuxCmd("list-sessions", "-F", "#{session_name}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "no server running on "+c.SocketName || strings.Contains(msg, "no server running") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// KillSession kills a tmux session by name. Returns nil if the session
// doesn't exist (tmux prints to stderr but the caller shouldn't care).
func (c *Client) KillSession(name string) error {
	return c.runTmux("kill-session", "-t", name)
}

// RenameWindow renames an existing tmux window. Target can be any tmux
// target (window id @N, session:name, etc.).
func (c *Client) RenameWindow(target, newName string) error {
	return c.runTmux("rename-window", "-t", target, newName)
}

// PanePIDs returns the set of shell PIDs running in this tmux session's panes.
// Used to distinguish Claude processes spawned under mo (descendants of one of
// these PIDs) from orphans running elsewhere on the host.
func (c *Client) PanePIDs() (map[int]bool, error) {
	cmd := c.tmuxCmd("list-panes", "-s", "-t", c.SessionName, "-F", "#{pane_pid}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return parsePIDSet(out), nil
}

// WindowPanePIDs returns the set of shell PIDs in the panes of a specific
// window. Target can be any tmux window target (window id @N, name, etc.).
// Used to attribute a Claude process (via PPID chain) to its tmux window.
func (c *Client) WindowPanePIDs(target string) (map[int]bool, error) {
	cmd := c.tmuxCmd("list-panes", "-t", target, "-F", "#{pane_pid}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return parsePIDSet(out), nil
}

// WindowExists checks if a window with the given name exists.
func (c *Client) WindowExists(name string) bool {
	windows, err := c.ListWindows()
	if err != nil {
		return false
	}
	for _, w := range windows {
		if w.Name == name {
			return true
		}
	}
	return false
}

// EnsureSession creates the session if it doesn't exist.
func (c *Client) EnsureSession() error {
	if c.SessionExists() {
		return nil
	}
	return c.CreateSession()
}

// SplitWindow creates a vertical split pane in the target window with the
// given cwd. The new pane is created on the right with the specified width
// in columns. If command is non-empty, it is run in the new pane.
func (c *Client) SplitWindow(target string, cols int, cwd, command string) (string, error) {
	args := []string{
		"split-window", "-h",
		"-t", target,
		"-l", fmt.Sprintf("%d", cols),
		"-c", cwd,
		"-P", "-F", "#{pane_id}",
	}
	if command != "" {
		args = append(args, command)
	}
	cmd := c.tmuxCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("split-window: %s", msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SplitWindowHorizontal creates a horizontal split (panes stacked vertically)
// below the target pane, with the given working directory.
func (c *Client) SplitWindowHorizontal(target, cwd string) (string, error) {
	args := []string{"split-window", "-v", "-t", target, "-c", cwd, "-P", "-F", "#{pane_id}"}
	cmd := c.tmuxCmd(args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("split-window: %s", msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BreakPane moves a pane into its own hidden window without switching focus.
func (c *Client) BreakPane(paneID string) error {
	return c.runTmux("break-pane", "-d", "-s", paneID)
}

// GetPaneWindowID returns the tmux window ID (e.g. "@3") that contains the given pane.
func (c *Client) GetPaneWindowID(paneID string) (string, error) {
	cmd := c.tmuxCmd("display-message", "-t", paneID, "-p", "#{window_id}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("get window for pane %s: %s", paneID, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// PaneID returns the tmux pane ID (e.g. "%7") of the given target.
func (c *Client) PaneID(target string) (string, error) {
	cmd := c.tmuxCmd("display-message", "-t", target, "-p", "#{pane_id}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("get pane id for %s: %s", target, msg)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SetWindowOption sets a user option on a tmux window. The target can be a
// window ID (@N), window name, or pane ID (%N) — tmux resolves pane targets
// to the containing window.
func (c *Client) SetWindowOption(target, option, value string) error {
	return c.runTmux("set-window-option", "-t", target, option, value)
}

// ConfigureStatusFormat sets the global window-status-format to hide windows
// marked with the @mo_hidden user option (used for terminal drawer tab storage).
// Uses -g because session-level window options are not inherited by windows
// created later (e.g. by break-pane).
func (c *Client) ConfigureStatusFormat() {
	c.runTmux("set-option", "-g",
		"window-status-format", "#{?#{@mo_hidden},,#I:#W#{window_flags} }")
	c.runTmux("set-option", "-g",
		"window-status-current-format", "#{?#{@mo_hidden},,#I:#W#{window_flags}*}")
}

// JoinPaneVertical moves srcPaneID into dstTarget's window, splitting dstTarget
// vertically (new pane below) at 30% height.
func (c *Client) JoinPaneVertical(srcPaneID, dstTarget string) error {
	return c.runTmux("join-pane", "-v", "-l", "30%", "-s", srcPaneID, "-t", dstTarget)
}

// JoinPaneConsolidate moves srcPaneID into dstPaneID's window for hidden storage.
// Uses -d to prevent tmux from switching focus to the hidden window.
func (c *Client) JoinPaneConsolidate(srcPaneID, dstPaneID string) error {
	return c.runTmux("join-pane", "-d", "-s", srcPaneID, "-t", dstPaneID)
}

// KillPane kills a specific pane by its ID.
func (c *Client) KillPane(paneID string) error {
	return c.runTmux("kill-pane", "-t", paneID)
}

// IsPaneAlive checks if a pane exists and its process is still running.
// Returns false for panes that don't exist or whose process has exited
// (e.g. remain-on-exit keeps the pane around but the shell is dead).
func (c *Client) IsPaneAlive(paneID string) bool {
	cmd := c.tmuxCmd("display-message", "-t", paneID, "-p", "#{pane_dead}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

// Popup opens a floating popup terminal in the given directory.
func (c *Client) Popup(cwd, title string) error {
	return c.runTmux("display-popup", "-E", "-d", cwd, "-w", "80%", "-h", "80%", "-T", title)
}

// SelectPane focuses a specific pane.
// SetWindowHook sets a tmux hook on a window. For example,
// SetWindowHook("mo:rails", "pane-exited", "kill-window") will kill the
// window when any pane in it exits.
func (c *Client) SetWindowHook(target, hookName, command string) {
	c.runTmux("set-hook", "-t", target, hookName, command)
}

func (c *Client) SelectPane(target string) error {
	return c.runTmux("select-pane", "-t", target)
}

// PaneCommand returns the current command running in a pane.
func (c *Client) PaneCommand(target string) string {
	cmd := c.tmuxCmd("display-message", "-t", target, "-p", "#{pane_current_command}")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// KillServer terminates the tmux server associated with this Client's socket.
// Useful for test cleanup so an isolated -L socket doesn't linger. In
// production the default socket is never killed; mo leaves that to tmux.
func (c *Client) KillServer() error {
	return c.runTmux("kill-server")
}

// CurrentWindowName returns the name of the current tmux window, if any.
// Runs against the default tmux socket — intended for use from an
// mo-in-tmux startup path, not from tests.
func CurrentWindowName() string {
	cmd := exec.Command("tmux", "display-message", "-p", "#{window_name}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// IsInsideTmux checks if we're running inside a tmux session.
func IsInsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// CurrentSessionName returns the name of the current tmux session, if any.
func CurrentSessionName() string {
	cmd := exec.Command("tmux", "display-message", "-p", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Window represents a tmux window.
type Window struct {
	ID         string // stable tmux window id, e.g. "@3"
	Index      string
	Name       string
	InstanceID string // mo-generated instance ID (from @mo_instance_id window option); empty for pre-refactor windows
	AgentKey   string // coding agent mnemonic key (from @mo_agent window option); empty = Claude (default)
	CWD        string
}
