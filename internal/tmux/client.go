package tmux

import (
	"fmt"
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
	cmd := exec.Command("tmux", "new-session", "-d", "-s", c.SessionName)
	return cmd.Run()
}

// CreateWindow creates a new window in the session.
func (c *Client) CreateWindow(name, cwd string) (string, error) {
	target := c.SessionName + ":" + name
	cmd := exec.Command("tmux", "new-window", "-t", c.SessionName, "-n", name, "-c", cwd)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("creating window %q: %w", name, err)
	}
	return target, nil
}

// SendKeys sends keystrokes to a tmux target (window or pane).
func (c *Client) SendKeys(target, keys string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", target, keys, "Enter")
	return cmd.Run()
}

// SwitchToWindow switches the client to the specified window.
// Only works if we're inside the same tmux session.
func (c *Client) SwitchToWindow(target string) error {
	cmd := exec.Command("tmux", "select-window", "-t", target)
	return cmd.Run()
}

// ListWindows returns the names of windows in the session.
func (c *Client) ListWindows() ([]Window, error) {
	cmd := exec.Command("tmux", "list-windows", "-t", c.SessionName, "-F", "#{window_index}:#{window_name}:#{pane_current_path}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
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
	cmd := exec.Command("tmux", "kill-window", "-t", target)
	return cmd.Run()
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

// IsInsideTmux checks if we're running inside a tmux session.
func IsInsideTmux() bool {
	cmd := exec.Command("tmux", "display-message", "-p", "#{session_name}")
	return cmd.Run() == nil
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
	Index string
	Name  string
	CWD   string
}
