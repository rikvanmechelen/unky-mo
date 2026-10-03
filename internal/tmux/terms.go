package tmux

import (
	"fmt"
	"strings"
)

// DrawerPaneOption is the window option the sidebar sets to the pane ID of
// the terminal currently shown in its drawer ("" when the drawer is
// closed). Together with the panes parked in the window's mo-terms session
// it lets other processes (mo web, a restarted sidebar) find every
// terminal of a window. A window option rather than a pane option, because
// pane options need tmux 3.1 and mo supports 3.0.
const DrawerPaneOption = "@mo_drawer_pane"

// TermPane describes a terminal pane for display: what it's running and
// where.
type TermPane struct {
	ID      string // "%N"
	Command string // pane_current_command, e.g. "fish"
	Cwd     string // pane_current_path
}

const termPaneFormat = "#{pane_id}\t#{pane_current_command}\t#{pane_current_path}"

// parseTermPanes parses lines of termPaneFormat. Malformed lines are dropped.
func parseTermPanes(out string) []TermPane {
	var panes []TermPane
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 || !strings.HasPrefix(f[0], "%") {
			continue
		}
		panes = append(panes, TermPane{ID: f[0], Command: f[1], Cwd: f[2]})
	}
	return panes
}

// ListSessionPanes lists every pane in every window of the named session
// (e.g. a mo-terms parking session). A missing session is an error.
func (c *Client) ListSessionPanes(session string) ([]TermPane, error) {
	out, err := c.tmuxCmd("list-panes", "-s", "-t", session+":", "-F", termPaneFormat).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list-panes: %s", strings.TrimSpace(string(out)))
	}
	return parseTermPanes(string(out)), nil
}

// PaneDetails describes a single pane.
func (c *Client) PaneDetails(paneID string) (TermPane, error) {
	out, err := c.tmuxCmd("display-message", "-p", "-t", paneID, termPaneFormat).CombinedOutput()
	if err != nil {
		return TermPane{}, fmt.Errorf("display-message: %s", strings.TrimSpace(string(out)))
	}
	panes := parseTermPanes(string(out))
	if len(panes) != 1 {
		return TermPane{}, fmt.Errorf("pane %s not found", paneID)
	}
	return panes[0], nil
}

// WindowOption reads a window user option ("" when unset or on error).
func (c *Client) WindowOption(target, option string) string {
	out, err := c.tmuxCmd("show-options", "-wqv", "-t", target, option).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NewWindowInSession opens a detached window (a shell in cwd) in the named
// session and returns its pane ID. Used to create terminals directly in a
// mo-terms parking session, without touching any visible layout.
func (c *Client) NewWindowInSession(session, cwd string) (string, error) {
	args := []string{"new-window", "-d", "-t", session + ":", "-P", "-F", "#{pane_id}"}
	if cwd != "" {
		args = append(args, "-c", cwd)
	}
	out, err := c.tmuxCmd(args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("new-window: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// CapturePane returns the last `lines` lines of a pane's scrollback and
// screen as plain text (wrapped lines joined, trailing blank lines kept
// out). It reads the pane's buffer only — no client attaches and nothing
// is resized.
func (c *Client) CapturePane(paneID string, lines int) (string, error) {
	out, err := c.tmuxCmd("capture-pane", "-p", "-J", "-t", paneID, "-S", fmt.Sprintf("-%d", lines)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("capture-pane: %s", strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil
}
