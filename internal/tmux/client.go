package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Client wraps tmux commands for session/window management.
type Client struct {
	SessionName string
}

func NewClient(sessionName string) *Client {
	return &Client{SessionName: sessionName}
}

// SessionExists checks if the tmux session exists.
func (c *Client) SessionExists() bool {
	cmd := exec.Command("tmux", "has-session", "-t", c.SessionName)
	return cmd.Run() == nil
}

// CreateSession creates a new tmux session (detached) with mouse support enabled.
func (c *Client) CreateSession() error {
	if err := runTmux("new-session", "-d", "-s", c.SessionName); err != nil {
		return err
	}
	c.EnableMouse()
	return nil
}

// EnableMouse turns on mouse support for the session (scroll, click panes/windows,
// drag borders). Idempotent — safe to call on every launch. Silently no-ops if the
// session does not exist.
func (c *Client) EnableMouse() {
	runTmux("set-option", "-t", c.SessionName, "mouse", "on")
}

// CreateWindow creates a new window in the session.
func (c *Client) CreateWindow(name, cwd string) (string, error) {
	target := c.SessionName + ":" + name
	// Use -a to append after the current window, avoiding index conflicts
	if err := runTmux("new-window", "-a", "-t", c.SessionName, "-n", name, "-c", cwd); err != nil {
		return "", fmt.Errorf("creating window %q: %w", name, err)
	}
	return target, nil
}

// SendKeys sends keystrokes to a tmux target (window or pane), followed by Enter.
func (c *Client) SendKeys(target, keys string) error {
	return runTmux("send-keys", "-t", target, keys, "Enter")
}

// SendRawKeys sends keystrokes to a tmux target without appending Enter.
func (c *Client) SendRawKeys(target, keys string) error {
	return runTmux("send-keys", "-t", target, keys)
}

// SwitchToWindow switches the client to the specified window.
func (c *Client) SwitchToWindow(target string) error {
	return runTmux("select-window", "-t", target)
}

// DetachClient detaches every client currently attached to the tmux session.
// All processes running inside the session keep running; users can resume
// by running `mo` (or `tmux attach`) again.
func (c *Client) DetachClient() error {
	return runTmux("detach-client", "-s", c.SessionName)
}

// ListWindows returns the names of windows in the session.
// window_id is placed first so SplitN(4) safely preserves any ':' characters
// that happen to appear in pane_current_path.
func (c *Client) ListWindows() ([]Window, error) {
	cmd := exec.Command("tmux", "list-windows", "-t", c.SessionName, "-F", "#{window_id}:#{window_index}:#{window_name}:#{pane_current_path}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 4 {
			continue
		}
		windows = append(windows, Window{
			ID:    parts[0],
			Index: parts[1],
			Name:  parts[2],
			CWD:   parts[3],
		})
	}
	return windows, nil
}

// KillWindow kills a specific window.
func (c *Client) KillWindow(target string) error {
	return runTmux("kill-window", "-t", target)
}

// RenameWindow renames an existing tmux window. Target can be any tmux
// target (window id @N, session:name, etc.).
func (c *Client) RenameWindow(target, newName string) error {
	return runTmux("rename-window", "-t", target, newName)
}

// PanePIDs returns the set of shell PIDs running in this tmux session's panes.
// Used to distinguish Claude processes spawned under mo (descendants of one of
// these PIDs) from orphans running elsewhere on the host.
func (c *Client) PanePIDs() (map[int]bool, error) {
	cmd := exec.Command("tmux", "list-panes", "-s", "-t", c.SessionName, "-F", "#{pane_pid}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	pids := make(map[int]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil {
			continue
		}
		pids[pid] = true
	}
	return pids, nil
}

// WindowPanePIDs returns the set of shell PIDs in the panes of a specific
// window. Target can be any tmux window target (window id @N, name, etc.).
// Used to attribute a Claude process (via PPID chain) to its tmux window.
func (c *Client) WindowPanePIDs(target string) (map[int]bool, error) {
	cmd := exec.Command("tmux", "list-panes", "-t", target, "-F", "#{pane_pid}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	pids := make(map[int]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil {
			continue
		}
		pids[pid] = true
	}
	return pids, nil
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
	cmd := exec.Command("tmux", args...)
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
	cmd := exec.Command("tmux", args...)
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
	return runTmux("break-pane", "-d", "-s", paneID)
}

// GetPaneWindowID returns the tmux window ID (e.g. "@3") that contains the given pane.
func (c *Client) GetPaneWindowID(paneID string) (string, error) {
	cmd := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{window_id}")
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

// SetWindowOption sets a user option on a tmux window. The target can be a
// window ID (@N), window name, or pane ID (%N) — tmux resolves pane targets
// to the containing window.
func (c *Client) SetWindowOption(target, option, value string) error {
	return runTmux("set-window-option", "-t", target, option, value)
}

// ConfigureStatusFormat sets the global window-status-format to hide windows
// marked with the @mo_hidden user option (used for terminal drawer tab storage).
// Uses -g because session-level window options are not inherited by windows
// created later (e.g. by break-pane).
func (c *Client) ConfigureStatusFormat() {
	runTmux("set-option", "-g",
		"window-status-format", "#{?#{@mo_hidden},,#I:#W#{window_flags} }")
	runTmux("set-option", "-g",
		"window-status-current-format", "#{?#{@mo_hidden},,#I:#W#{window_flags}*}")
}

// JoinPaneVertical moves srcPaneID into dstTarget's window, splitting dstTarget
// vertically (new pane below) at 30% height.
func (c *Client) JoinPaneVertical(srcPaneID, dstTarget string) error {
	return runTmux("join-pane", "-v", "-l", "30%", "-s", srcPaneID, "-t", dstTarget)
}

// JoinPaneConsolidate moves srcPaneID into dstPaneID's window for hidden storage.
// Uses -d to prevent tmux from switching focus to the hidden window.
func (c *Client) JoinPaneConsolidate(srcPaneID, dstPaneID string) error {
	return runTmux("join-pane", "-d", "-s", srcPaneID, "-t", dstPaneID)
}

// KillPane kills a specific pane by its ID.
func (c *Client) KillPane(paneID string) error {
	return runTmux("kill-pane", "-t", paneID)
}

// IsPaneAlive checks if a pane exists and its process is still running.
// Returns false for panes that don't exist or whose process has exited
// (e.g. remain-on-exit keeps the pane around but the shell is dead).
func (c *Client) IsPaneAlive(paneID string) bool {
	cmd := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{pane_dead}")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "0"
}

// Popup opens a floating popup terminal in the given directory.
func (c *Client) Popup(cwd, title string) error {
	return runTmux("display-popup", "-E", "-d", cwd, "-w", "80%", "-h", "80%", "-T", title)
}

// SelectPane focuses a specific pane.
// SetWindowHook sets a tmux hook on a window. For example,
// SetWindowHook("mo:rails", "pane-exited", "kill-window") will kill the
// window when any pane in it exits.
func (c *Client) SetWindowHook(target, hookName, command string) {
	runTmux("set-hook", "-t", target, hookName, command)
}

func (c *Client) SelectPane(target string) error {
	return runTmux("select-pane", "-t", target)
}

// PaneCommand returns the current command running in a pane.
func (c *Client) PaneCommand(target string) string {
	cmd := exec.Command("tmux", "display-message", "-t", target, "-p", "#{pane_current_command}")
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// CurrentWindowName returns the name of the current tmux window, if any.
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

// runTmux executes a tmux command and returns a descriptive error on failure.
func runTmux(args ...string) error {
	cmd := exec.Command("tmux", args...)
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

// Window represents a tmux window.
type Window struct {
	ID    string // stable tmux window id, e.g. "@3"
	Index string
	Name  string
	CWD   string
}
