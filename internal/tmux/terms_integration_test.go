//go:build integration

package tmux

import (
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
