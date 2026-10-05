//go:build integration

package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A terminal created parked in a mo-terms-style session is listed, can be
// typed into, and its output reads back through CapturePane.
func TestIntegrationParkedTerminalRoundTrip(t *testing.T) {
	c := newTestClient(t)
	// A plain shell, so the host's interactive-shell first-run wizards
	// (zsh-newuser, prompt configurators) don't eat the typed command.
	if err := c.runTmux("set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatalf("set default-shell: %v", err)
	}
	if _, err := c.NewDetachedSession("mo-terms-x", "/tmp"); err != nil {
		t.Fatalf("NewDetachedSession: %v", err)
	}
	pane, err := c.NewWindowInSession("mo-terms-x", "/tmp")
	if err != nil {
		t.Fatalf("NewWindowInSession: %v", err)
	}

	panes, err := c.ListSessionPanes("mo-terms-x")
	if err != nil {
		t.Fatalf("ListSessionPanes: %v", err)
	}
	if len(panes) != 2 || panes[1].ID != pane || panes[1].Cwd != "/tmp" || panes[1].Command == "" {
		t.Fatalf("want the initial pane plus %s in /tmp, got %+v", pane, panes)
	}
	if d, err := c.PaneDetails(pane); err != nil || d.ID != pane {
		t.Fatalf("PaneDetails: %+v, %v", d, err)
	}

	time.Sleep(200 * time.Millisecond) // let the shell prompt settle
	if err := c.SendLiteralText(pane, "printf 'mo-%s\\n' web"); err != nil {
		t.Fatalf("SendLiteralText: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := c.CapturePane(pane, 50)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if strings.Contains(out, "\nmo-web") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("command output never showed up:\n%s", out)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if _, err := c.ListSessionPanes("no-such-session"); err == nil {
		t.Error("ListSessionPanes on a missing session should error")
	}
}

func TestIntegrationWindowOptionRoundTrip(t *testing.T) {
	c := newTestClient(t)
	target, err := c.CreateWindow("opt", "/tmp")
	if err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	if got := c.WindowOption(target, DrawerPaneOption); got != "" {
		t.Errorf("unset option: want \"\", got %q", got)
	}
	if err := c.SetWindowOption(target, DrawerPaneOption, "%42"); err != nil {
		t.Fatalf("SetWindowOption: %v", err)
	}
	if got := c.WindowOption(target, DrawerPaneOption); got != "%42" {
		t.Errorf("want %%42, got %q", got)
	}
	if err := c.SetWindowOption(target, DrawerPaneOption, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := c.WindowOption(target, DrawerPaneOption); got != "" {
		t.Errorf("cleared option: want \"\", got %q", got)
	}
}

// bracketedCat opens a window whose program switches on bracketed paste and
// writes what it receives, made visible by cat -v, to the returned file.
func bracketedCat(t *testing.T, c *Client) (pane, out string) {
	t.Helper()
	out = filepath.Join(t.TempDir(), "received")
	script := `printf '\033[?2004h'; stty raw -echo; exec cat -v > ` + out
	id, err := c.tmuxCmd("new-window", "-d", "-t", c.SessionName, "-P", "-F", "#{pane_id}", "sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("new-window: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let printf and stty take effect
	return strings.TrimSpace(string(id)), out
}

// SendKeySequence types text literally and key names as keys, in order —
// including a trailing ";", which tmux would otherwise read as a command
// separator and drop — and PasteText brackets a paste without an Enter.
func TestIntegrationSendKeySequenceAndPaste(t *testing.T) {
	c := newTestClient(t)
	pane, out := bracketedCat(t, c)

	err := c.SendKeySequence(pane, []Key{{Text: "Up"}, {Name: "Tab"}, {Text: "a;"}, {Name: "C-a"}, {Name: "Up"}, {Text: ";"}})
	if err != nil {
		t.Fatalf("SendKeySequence: %v", err)
	}
	if err := c.PasteText(pane, "x;\ny;"); err != nil {
		t.Fatalf("PasteText: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "Up\ta;^A^[[A;^[[200~x;^My;^[[201~"
	if string(got) != want {
		t.Errorf("pane received %q, want %q", got, want)
	}
}

// SendLiteralText keeps trailing semicolons too.
func TestIntegrationSendLiteralTextKeepsTrailingSemicolon(t *testing.T) {
	c := newTestClient(t)
	pane, out := bracketedCat(t, c)
	if err := c.SendLiteralText(pane, `echo a\;`); err != nil {
		t.Fatalf("SendLiteralText: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	got, _ := os.ReadFile(out)
	if want := `echo a\;^M`; string(got) != want {
		t.Errorf("pane received %q, want %q", got, want)
	}
}

// CaptureScreen returns colours and puts the cursor where the typed text ends.
func TestIntegrationCaptureScreen(t *testing.T) {
	c := newTestClient(t)
	if err := c.runTmux("set-option", "-g", "default-shell", "/bin/sh"); err != nil {
		t.Fatalf("set default-shell: %v", err)
	}
	pane, err := c.NewWindowInSession(c.SessionName, "/tmp")
	if err != nil {
		t.Fatalf("NewWindowInSession: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.SendLiteralText(pane, `PS1='$ '; printf '\033[31mred\033[0m\n'`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.SendKeySequence(pane, []Key{{Text: "abc"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	sc, err := c.CaptureScreen(pane, 50)
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if !strings.Contains(sc.Text, "\x1b[31mred") {
		t.Errorf("colours missing from capture:\n%q", sc.Text)
	}
	lines := strings.Split(sc.Text, "\n")
	if sc.CursorLine >= len(lines) || lines[sc.CursorLine] != "$ abc" || sc.CursorCol != 5 || !sc.CursorVisible {
		t.Errorf("cursor at line %d col %d (visible %v), want after \"$ abc\" in:\n%q", sc.CursorLine, sc.CursorCol, sc.CursorVisible, sc.Text)
	}
}
