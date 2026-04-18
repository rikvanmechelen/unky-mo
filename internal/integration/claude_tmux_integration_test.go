//go:build integration

// Package integration exercises the seam between a real (isolated) tmux
// server and the claude session-detection code. Run with
// `go test -tags integration ./internal/integration/...`.
//
// Test scenario: spawn a fake `claude` inside a tmux pane under an isolated
// socket. Confirm that:
//   1. claude.LiveSessions() discovers the live session.
//   2. IsDescendantOf attributes the fake PID to the pane's shell PID.
//   3. Killing the window drops the session out of LiveSessions on the
//      next poll.
package integration

import (
	"os/exec"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/tmux"
)

func fakeClaudePath(t *testing.T) string {
	t.Helper()
	// Walk up from the test's working directory to find the repo root so we
	// can locate internal/tmux/testdata/fake-claude.sh.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, "internal", "tmux", "testdata", "fake-claude.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("could not locate fake-claude.sh — expected at internal/tmux/testdata/fake-claude.sh")
	return ""
}

func newIsolatedTmux(t *testing.T) *tmux.Client {
	t.Helper()
	// Force SHELL=/bin/sh so the tmux server — and every pane's default
	// shell — bypasses the user's interactive shell (zsh/fish first-run
	// wizards would otherwise swallow the SendKeys input).
	t.Setenv("SHELL", "/bin/sh")

	socket := "mo-test-claude-" + filepath.Base(t.TempDir())
	if len(socket) > 50 {
		socket = socket[len(socket)-50:]
	}
	c := &tmux.Client{SessionName: "test", SocketName: socket}
	if err := c.CreateSession(); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() {
		_ = c.KillServer()
	})
	return c
}

// waitForOk polls check until it returns true or timeout expires. Returns
// whether check succeeded.
func waitForOk(timeout time.Duration, check func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Poll waits up to `timeout` for `check` to return true. Fails the test if it
// never does.
func waitFor(t *testing.T, timeout time.Duration, name string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestClaudeSessionDetectionViaTmuxPane(t *testing.T) {
	// Isolate HOME so fake-claude's session files don't pollute the user's dir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Working directory that will be reported by fake-claude as the session's cwd.
	projectDir := filepath.Join(home, "workspace", "myproj")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	tc := newIsolatedTmux(t)
	if _, err := tc.CreateWindow("myproj", projectDir); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}

	// Give the pane's shell a moment to start before typing into it.
	time.Sleep(200 * time.Millisecond)

	// Launch fake-claude inside the window's pane. We pipe HOME + cwd through
	// env and `exec` into the fake so it replaces the shell (and keeps the
	// pane's TTY on stdin, so its `while read` loop has something to wait on).
	fake := fakeClaudePath(t)
	cmdLine := "exec env FAKE_CLAUDE_HOME=" + home + " FAKE_CLAUDE_CWD=" + projectDir + " " + fake
	if err := tc.SendKeys("test:myproj", cmdLine); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Wait for the fake's session-marker file to land.
	waited := waitForOk(5*time.Second, func() bool {
		entries, err := os.ReadDir(filepath.Join(home, ".claude", "sessions"))
		if err != nil {
			return false
		}
		return len(entries) > 0
	})
	if !waited {
		// Dump pane contents to help diagnose.
		pane, _ := exec.Command("tmux", "-L", tc.SocketName, "capture-pane", "-t", "test:myproj", "-p").CombinedOutput()
		t.Fatalf("session file never appeared.\nfake path: %s\ncmd: %s\npane:\n%s", fake, cmdLine, pane)
	}

	// LiveSessions should now find at least one live session.
	sessions, err := claude.LiveSessions()
	if err != nil {
		t.Fatalf("LiveSessions: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one live session")
	}
	// Locate the session whose CWD matches our project dir.
	var sess *claude.Session
	for i := range sessions {
		if sessions[i].CWD == projectDir {
			sess = &sessions[i]
			break
		}
	}
	if sess == nil {
		t.Fatalf("no session with cwd=%q found; got %+v", projectDir, sessions)
	}

	// IsDescendantOf the tmux pane PIDs — this is the core TUI attribution logic.
	panePIDs, err := tc.WindowPanePIDs("test:myproj")
	if err != nil {
		t.Fatalf("WindowPanePIDs: %v", err)
	}
	if len(panePIDs) == 0 {
		t.Fatal("no pane PIDs reported for the window")
	}
	if !claude.IsDescendantOf(sess.PID, panePIDs) {
		t.Errorf("fake-claude pid %d should be a descendant of pane pids %v", sess.PID, panePIDs)
	}

	// Kill the window. Fake-claude's TERM trap removes the session marker,
	// so the next LiveSessions poll should return one fewer entry.
	if err := tc.KillWindow("test:myproj"); err != nil {
		t.Fatalf("KillWindow: %v", err)
	}
	waitFor(t, 3*time.Second, "session file to disappear", func() bool {
		entries, _ := os.ReadDir(filepath.Join(home, ".claude", "sessions"))
		return len(entries) == 0
	})

	after, _ := claude.LiveSessions()
	for _, s := range after {
		if s.CWD == projectDir {
			t.Errorf("session for %s should be gone after KillWindow, still found: %+v", projectDir, s)
		}
	}
}

func TestClaudeSessionIdleDetectionOnSpawnedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectDir := filepath.Join(home, "workspace", "idletest")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	tc := newIsolatedTmux(t)
	if _, err := tc.CreateWindow("idletest", projectDir); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	fake := fakeClaudePath(t)
	cmdLine := "exec env FAKE_CLAUDE_HOME=" + home + " FAKE_CLAUDE_CWD=" + projectDir + " " + fake
	if err := tc.SendKeys("test:idletest", cmdLine); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Wait for session + JSONL to land.
	waitFor(t, 3*time.Second, "session JSONL to appear", func() bool {
		sessions, _ := claude.LiveSessions()
		for _, s := range sessions {
			if s.CWD == projectDir {
				// JSONL should exist at the encoded projects path.
				dir := claude.ProjectsDirForPath(projectDir)
				_, err := os.Stat(filepath.Join(dir, s.SessionID+".jsonl"))
				return err == nil
			}
		}
		return false
	})

	sessions, _ := claude.LiveSessions()
	var sess *claude.Session
	for i := range sessions {
		if sessions[i].CWD == projectDir {
			sess = &sessions[i]
		}
	}
	if sess == nil {
		t.Fatal("session never appeared")
	}

	// fake-claude's JSONL ends with stop_reason=end_turn → idle.
	if !claude.IsSessionIdle(projectDir, sess.SessionID) {
		t.Errorf("session with stop_reason=end_turn should be reported idle")
	}

	// Cleanup: kill the window to release the fake process.
	_ = tc.KillWindow("test:idletest")
}
