//go:build integration

// Integration tests that spin up a real (isolated) tmux server via -L. Run
// with `go test -tags integration ./internal/tmux/...`. Skipped by default so
// the normal suite doesn't depend on tmux being installed.

package tmux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// newTestClient starts an isolated tmux server with a random -L name and an
// empty session. Returns a Client pointed at that server plus a cleanup hook
// (registered via t.Cleanup) that kills the server.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	// Random-ish socket name — go test gives us a unique TempDir, so reuse its basename.
	socket := "mo-test-" + strings.ReplaceAll(t.TempDir(), "/", "-")
	// Socket names on tmux are limited in length; truncate aggressively.
	if len(socket) > 50 {
		socket = socket[len(socket)-50:]
	}

	c := &Client{SessionName: "test", SocketName: socket}
	if err := c.CreateSession(); err != nil {
		t.Fatalf("CreateSession on isolated socket: %v", err)
	}
	t.Cleanup(func() {
		_ = c.KillServer()
	})
	return c
}

func TestIntegrationSessionLifecycle(t *testing.T) {
	c := newTestClient(t)

	if !c.SessionExists() {
		t.Error("SessionExists should report true right after CreateSession")
	}

	if _, err := c.CreateWindow("myproj", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	windows, err := c.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	var found bool
	for _, w := range windows {
		if w.Name == "myproj" {
			found = true
			if w.ID == "" || !strings.HasPrefix(w.ID, "@") {
				t.Errorf("window ID should start with @, got %q", w.ID)
			}
		}
	}
	if !found {
		t.Errorf("created window missing from list: %+v", windows)
	}

	if !c.WindowExists("myproj") {
		t.Error("WindowExists('myproj') should be true")
	}

	if err := c.KillWindow("test:myproj"); err != nil {
		t.Errorf("KillWindow: %v", err)
	}
	// Give tmux a beat to update its state.
	time.Sleep(50 * time.Millisecond)
	if c.WindowExists("myproj") {
		t.Error("WindowExists should be false after KillWindow")
	}
}

func TestIntegrationPanePIDsIncludesCreatedPane(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.CreateWindow("panes", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	// Give the shell a moment to spawn.
	time.Sleep(100 * time.Millisecond)

	pids, err := c.PanePIDs()
	if err != nil {
		t.Fatalf("PanePIDs: %v", err)
	}
	if len(pids) == 0 {
		t.Fatal("expected at least one pane PID")
	}
	// At least one PID should be alive (sanity: we just created it).
	found := false
	for pid := range pids {
		if pid > 1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no live PIDs in pane set: %v", pids)
	}
}

func TestIntegrationRenameWindow(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.CreateWindow("before", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	if err := c.RenameWindow("test:before", "after"); err != nil {
		t.Fatalf("RenameWindow: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if !c.WindowExists("after") {
		t.Error("renamed window should be found under new name")
	}
	if c.WindowExists("before") {
		t.Error("old name should no longer exist")
	}
}

func TestIntegrationBreakPaneToSession(t *testing.T) {
	c := newTestClient(t)

	// Create a window on the default "test" session so we have a pane to
	// move, and a second session (mo-terms-ish) to move it into.
	if _, err := c.CreateWindow("donor", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	// Split that window so we can break a pane off without killing the window.
	paneID, err := c.SplitWindowHorizontal("test:donor", "/tmp")
	if err != nil {
		t.Fatalf("SplitWindowHorizontal: %v", err)
	}

	ghost, err := c.NewDetachedSession("mo-terms", "/tmp")
	if err != nil {
		t.Fatalf("NewDetachedSession: %v", err)
	}
	if ghost == "" || !strings.HasPrefix(ghost, "%") {
		t.Errorf("ghost pane id should be a tmux pane id; got %q", ghost)
	}
	if !c.SessionExistsNamed("mo-terms") {
		t.Fatal("mo-terms should exist after NewDetachedSession")
	}

	if err := c.BreakPaneToSession(paneID, "mo-terms"); err != nil {
		t.Fatalf("BreakPaneToSession: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Verify the pane now lives in mo-terms.
	out, err := c.tmuxCmd("list-panes", "-s", "-t", "mo-terms", "-F", "#{pane_id}").CombinedOutput()
	if err != nil {
		t.Fatalf("list-panes -s -t mo-terms: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), paneID) {
		t.Errorf("pane %q not found under mo-terms:\n%s", paneID, out)
	}
}

func TestIntegrationPopupKeyTableBindings(t *testing.T) {
	c := newTestClient(t)

	// Create mo-terms, flip its key-table to popup-keys, and bind the three
	// popup shortcuts. A client that later attaches to this session will
	// look up key presses in popup-keys — but we don't need a real client
	// here, tmux records the bindings server-side regardless.
	if _, err := c.NewDetachedSession("mo-terms", "/tmp"); err != nil {
		t.Fatalf("NewDetachedSession: %v", err)
	}
	if err := c.SetSessionOption("mo-terms", "key-table", "popup-keys"); err != nil {
		t.Fatalf("SetSessionOption: %v", err)
	}
	if err := c.BindKey("popup-keys", "`", "detach-client"); err != nil {
		t.Fatalf("BindKey backtick: %v", err)
	}
	if err := c.BindKey("popup-keys", "Tab", "next-window"); err != nil {
		t.Fatalf("BindKey Tab: %v", err)
	}
	if err := c.BindKey("popup-keys", "BTab", "previous-window"); err != nil {
		t.Fatalf("BindKey BTab: %v", err)
	}

	// Verify the session option landed.
	out, err := c.tmuxCmd("show-option", "-v", "-t", "mo-terms", "key-table").CombinedOutput()
	if err != nil {
		t.Fatalf("show-option: %v (%s)", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "popup-keys" {
		t.Errorf("key-table: got %q, want popup-keys", got)
	}

	// Verify the three bindings are registered in the popup-keys table.
	listOut, err := c.tmuxCmd("list-keys", "-T", "popup-keys").CombinedOutput()
	if err != nil {
		t.Fatalf("list-keys: %v (%s)", err, listOut)
	}
	body := string(listOut)
	for _, snippet := range []string{"detach-client", "next-window", "previous-window"} {
		if !strings.Contains(body, snippet) {
			t.Errorf("popup-keys missing %q:\n%s", snippet, body)
		}
	}

	// The session must still exist — binding the key table should not have
	// killed it.
	if !c.SessionExistsNamed("mo-terms") {
		t.Error("mo-terms should survive key-table setup")
	}
}

// Docstring check: a test that runs with no SocketName (SocketName="") and
// therefore would touch the host tmux server is intentionally absent —
// tests that need real tmux state MUST use newTestClient.
func TestIntegrationDefaultClientBuildsBareArgs(t *testing.T) {
	c := &Client{SessionName: "mo"}
	args := c.tmuxCmd("has-session", "-t", "mo").Args
	if len(args) < 1 || args[0] != "tmux" {
		t.Fatalf("args[0] should be tmux, got %v", args)
	}
	for _, a := range args {
		if a == "-L" {
			t.Errorf("default client must NOT include -L socket flag, got %v", args)
		}
	}
}
