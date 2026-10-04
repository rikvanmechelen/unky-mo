//go:build integration

// Integration tests that spin up a real (isolated) tmux server via -L. Run
// with `go test -tags integration ./internal/tmux/...`. Skipped by default so
// the normal suite doesn't depend on tmux being installed.

package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
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

// TestIntegrationSendLiteralTextTypesLiteralNotControlKey verifies that
// SendLiteralText's -l (literal) mode actually suppresses tmux's key-name
// interpretation — sending the text "Up" must type the two characters, not
// invoke the Up-arrow key, which is the entire point of the method existing
// (SendKeys/SendRawKeys would send the Up-arrow keypress for this same
// input). Only a real tmux exercise can verify this safety property.
func TestIntegrationSendLiteralTextTypesLiteralNotControlKey(t *testing.T) {
	c := newTestClient(t)
	target, err := c.CreateWindow("prompttest", "/tmp")
	if err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the shell prompt settle

	if err := c.SendLiteralText(target+".0", "Up"); err != nil {
		t.Fatalf("SendLiteralText: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	out, err := c.tmuxCmd("capture-pane", "-p", "-t", target+".0").Output()
	if err != nil {
		t.Fatalf("capture-pane: %v", err)
	}
	if !strings.Contains(string(out), "Up") {
		t.Fatalf("expected literal \"Up\" typed into the pane, got:\n%s", out)
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

// TestIntegrationPersistentSessionSurvivesInstantExitAndRespawns covers the
// mo-web lifecycle: a command that dies immediately (e.g. `mo web` on a busy
// port) must leave a dead pane behind rather than a vanished session, and
// RespawnPane must bring it back with a new command.
func TestIntegrationPersistentSessionSurvivesInstantExitAndRespawns(t *testing.T) {
	c := newTestClient(t)

	if err := c.StartPersistentSession("web", "", "exit 3"); err != nil {
		t.Fatalf("StartPersistentSession: %v", err)
	}
	paneField := func(format string) string {
		out, _ := c.tmuxCmd("display-message", "-p", "-t", "web:", format).Output()
		return strings.TrimSpace(string(out))
	}
	deadline := time.Now().Add(2 * time.Second)
	for paneField("#{pane_dead}") != "1" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !c.SessionExistsNamed("web") || paneField("#{pane_dead}") != "1" {
		t.Fatal("session should survive its command exiting, with a dead pane")
	}

	if err := c.RespawnPane("web:", "sleep 30"); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	if got := paneField("#{pane_dead}"); got != "0" {
		t.Errorf("pane_dead after respawn = %q, want 0", got)
	}
}

// CreateWindow must not move the session's current window — launches from the
// web dashboard (and restore-after-suspend) happen in the background, and
// focus only moves via an explicit SwitchToWindow.
func TestIntegrationCreateWindowKeepsCurrentWindow(t *testing.T) {
	c := newTestClient(t)
	activeID := func() string {
		t.Helper()
		out, err := c.tmuxCmd("display-message", "-p", "-t", c.SessionName, "#{window_id}").Output()
		if err != nil {
			t.Fatalf("display-message: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	before := activeID()
	target, err := c.CreateWindow("background", "/tmp")
	if err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	if got := activeID(); got != before {
		t.Fatalf("CreateWindow moved the current window from %s to %s", before, got)
	}

	if err := c.SwitchToWindow(target); err != nil {
		t.Fatalf("SwitchToWindow: %v", err)
	}
	if got := activeID(); !strings.HasSuffix(target, ":"+got) {
		t.Fatalf("SwitchToWindow: current window %s, want %s", got, target)
	}
}

// TestIntegrationSendPastedTextBracketsThePaste verifies that SendPastedText
// delivers multi-line text as one bracketed paste (ESC[200~ … ESC[201~) to an
// app that enabled bracketed-paste mode — as Claude Code does — followed by
// Enter, rather than as typed lines.
func TestIntegrationSendPastedTextBracketsThePaste(t *testing.T) {
	c := newTestClient(t)
	out := filepath.Join(t.TempDir(), "pasted")
	script := `printf '\033[?2004h'; exec cat -v > ` + out
	pane, err := c.tmuxCmd("new-window", "-d", "-t", c.SessionName, "-P", "-F", "#{pane_id}", "sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("new-window: %v", err)
	}
	target := strings.TrimSpace(string(pane))
	time.Sleep(300 * time.Millisecond) // let printf switch the mode on

	if err := c.SendPastedText(target, "-first line\nsecond line"); err != nil {
		t.Fatalf("SendPastedText: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "^[[200~-first line\nsecond line^[[201~\n"
	if string(got) != want {
		t.Errorf("pane received %q, want %q", got, want)
	}
	if bufs, _ := c.tmuxCmd("list-buffers").Output(); strings.Contains(string(bufs), "mo-paste-") {
		t.Errorf("paste buffer left behind: %s", bufs)
	}
}
