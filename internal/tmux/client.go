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

// CreateSession creates a new tmux session (detached).
func (c *Client) CreateSession() error {
	return runTmux("new-session", "-d", "-s", c.SessionName)
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

// SendKeys sends keystrokes to a tmux target (window or pane).
func (c *Client) SendKeys(target, keys string) error {
	return runTmux("send-keys", "-t", target, keys, "Enter")
}

// SwitchToWindow switches the client to the specified window.
func (c *Client) SwitchToWindow(target string) error {
	return runTmux("select-window", "-t", target)
}

// ListWindows returns the names of windows in the session.
func (c *Client) ListWindows() ([]Window, error) {
	cmd := exec.Command("tmux", "list-windows", "-t", c.SessionName, "-F", "#{window_index}:#{window_name}:#{pane_current_path}")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	var windows []Window
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		windows = append(windows, Window{
			Index: parts[0],
			Name:  parts[1],
			CWD:   parts[2],
		})
	}
	return windows, nil
}

// KillWindow kills a specific window.
func (c *Client) KillWindow(target string) error {
	return runTmux("kill-window", "-t", target)
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

// SplitWindow creates a vertical split pane in the target window.
// The new pane is created on the right with the specified width in columns.
// If command is non-empty, it is run in the new pane.
func (c *Client) SplitWindow(target string, cols int, command string) (string, error) {
	args := []string{"split-window", "-h", "-t", target, "-l", fmt.Sprintf("%d", cols), "-P", "-F", "#{pane_id}"}
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

// SelectPane focuses a specific pane.
func (c *Client) SelectPane(target string) error {
	return runTmux("select-pane", "-t", target)
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
	Index string
	Name  string
	CWD   string
}
