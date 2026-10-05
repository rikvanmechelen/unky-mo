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

// Screen is a pane's scrollback and screen with its colours, plus where
// the cursor is, for a live terminal view.
type Screen struct {
	// Text holds the lines with tmux's SGR escapes (capture-pane -e),
	// wrapped lines not joined, so they line up with the cursor.
	Text string
	// CursorLine is the cursor's 0-based line in Text, CursorCol its
	// column in cells.
	CursorLine, CursorCol int
	CursorVisible         bool
}

// screenMarker starts the display-message line that follows the capture.
const screenMarker = "mo-screen "

// CaptureScreen returns the last `lines` lines of history plus the visible
// screen, with colours and the cursor position. The capture and the cursor
// are read in one tmux command, so they match. Like CapturePane, nothing
// attaches or resizes.
func (c *Client) CaptureScreen(paneID string, lines int) (Screen, error) {
	out, err := c.tmuxCmd(
		"capture-pane", "-p", "-e", "-t", paneID, "-S", fmt.Sprintf("-%d", lines), ";",
		"display-message", "-p", "-t", paneID, screenMarker+"#{cursor_x} #{cursor_y} #{pane_height} #{cursor_flag}",
	).CombinedOutput()
	if err != nil {
		return Screen{}, fmt.Errorf("capture-pane: %s", strings.TrimSpace(string(out)))
	}
	return parseScreen(string(out))
}

// parseScreen splits CaptureScreen's output into the capture and the
// cursor line that ends it.
func parseScreen(out string) (Screen, error) {
	out = strings.TrimSuffix(out, "\n")
	i := strings.LastIndex(out, "\n"+screenMarker)
	if i < 0 {
		return Screen{}, fmt.Errorf("capture-pane: no cursor in output")
	}
	var x, y, height, flag int
	if _, err := fmt.Sscanf(out[i+1+len(screenMarker):], "%d %d %d %d", &x, &y, &height, &flag); err != nil {
		return Screen{}, fmt.Errorf("capture-pane: bad cursor line: %w", err)
	}
	text := out[:i]
	n := strings.Count(text, "\n") + 1
	return Screen{Text: text, CursorLine: max(n-height+y, 0), CursorCol: x, CursorVisible: flag == 1}, nil
}

// Key is one step of what a live terminal typed: literal Text, or a tmux
// key Name such as "Tab" or "C-r". Exactly one is set.
type Key struct {
	Text string
	Name string
}

// SendKeySequence types keys into target in order: Text literally (no
// key-name interpretation), runs of Names in one send-keys. Callers must
// only pass Names they've checked against a fixed list — a Name is handed
// to tmux as a key name.
func (c *Client) SendKeySequence(target string, keys []Key) error {
	var names []string
	flush := func() error {
		if len(names) == 0 {
			return nil
		}
		err := c.runTmux(append([]string{"send-keys", "-t", target}, names...)...)
		names = names[:0]
		return err
	}
	for _, k := range keys {
		if k.Name != "" {
			names = append(names, k.Name)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := c.sendLiteral(target, k.Text); err != nil {
			return err
		}
	}
	return flush()
}

// PasteText pastes text into target as a bracketed paste (if the app asked
// for bracketed paste), without pressing Enter — a shell then shows a
// multi-line paste for editing instead of running each line.
func (c *Client) PasteText(target, text string) error {
	return c.paste(target, text)
}
