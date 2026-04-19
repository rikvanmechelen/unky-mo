//go:build integration

// Real-tmux integration tests for the sidebar. Spin up an isolated tmux
// server, drive sidebar state against it, verify both behaviour and
// graceful degradation.
//
// Run: go test -tags integration ./internal/tui/sidebar/...
package sidebar

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

func newSidebarIntegTmux(t *testing.T) *ttmux.Client {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("SHELL", "/bin/sh")
	socket := "mo-test-sidebar-" + filepath.Base(t.TempDir())
	if len(socket) > 50 {
		socket = socket[len(socket)-50:]
	}
	c := &ttmux.Client{SessionName: "test", SocketName: socket}
	// CreateSession can race on server startup right after another test's
	// KillServer — retry a few times before giving up.
	var err error
	for i := 0; i < 5; i++ {
		if err = c.CreateSession(); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	t.Cleanup(func() { _ = c.KillServer() })
	return c
}

// TestIntegrationRefreshTerminalsPrunesRealPane — creates a real tmux pane,
// seeds the sidebar with it, kills it externally, calls refreshTerminals,
// asserts the pane was pruned and the drawer auto-closed.
func TestIntegrationRefreshTerminalsPrunesRealPane(t *testing.T) {
	tc := newSidebarIntegTmux(t)

	if _, err := tc.CreateWindow("alpha", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	paneID, err := tc.SplitWindowHorizontal("test:alpha.0", "/tmp")
	if err != nil {
		t.Fatalf("SplitWindowHorizontal: %v", err)
	}
	if !tc.IsPaneAlive(paneID) {
		t.Fatalf("created pane %s reported not alive", paneID)
	}

	// Build a minimal Model backed by the real tmux adapter + a no-op claude mock.
	ctrl := gomock.NewController(t)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	claude.EXPECT().ActiveShellsForSession(gomock.Any()).Return(nil).AnyTimes()

	tmuxAdapter := newTmuxClientAdapter(tc)
	m := &Model{
		tmux:          tmuxAdapter,
		claude:        claude,
		resolver:      FakeWindowResolver{Name: "alpha", ID: "@1"},
		windowName:    "alpha",
		windowPath:    "/tmp",
		terminals:     []TerminalTab{{PaneID: paneID, Name: "term-1"}},
		activeTermIdx: 0,
		drawerOpen:    true,
		items:         []SidebarItem{{Name: "Unky Mo Home", IsHome: true}},
	}

	// External kill — simulates the user running `tmux kill-pane` from
	// another window.
	if err := tc.KillPane(paneID); err != nil {
		t.Fatalf("KillPane: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if tc.IsPaneAlive(paneID) {
		t.Fatal("pane should be dead after external KillPane")
	}

	// Refresh — dead pane must be pruned, drawer must auto-close.
	m.refreshTerminals()
	if len(m.terminals) != 0 {
		t.Errorf("dead pane not pruned; terminals = %+v", m.terminals)
	}
	if m.drawerOpen {
		t.Error("drawer should auto-close when its only terminal died")
	}
	if m.activeTermIdx != -1 {
		t.Errorf("activeTermIdx should be -1, got %d", m.activeTermIdx)
	}
}

// TestIntegrationWindowIDStableAcrossRename — tmux window IDs don't change
// on rename. This verifies the sidebar's WindowID-based own-window match
// strategy is actually sound against a real tmux server.
func TestIntegrationWindowIDStableAcrossRename(t *testing.T) {
	tc := newSidebarIntegTmux(t)
	if _, err := tc.CreateWindow("alpha", "/tmp"); err != nil {
		t.Fatalf("CreateWindow: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	windows, err := tc.ListWindows()
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	var alphaID string
	for _, w := range windows {
		if w.Name == "alpha" {
			alphaID = w.ID
		}
	}
	if alphaID == "" {
		t.Fatal("alpha window not found")
	}

	// Rename via raw tmux — simulates /rename from inside claude.
	renameCmd := exec.Command("tmux", "-L", tc.SocketName, "rename-window", "-t", "test:alpha", "alpha [wip]")
	if out, err := renameCmd.CombinedOutput(); err != nil {
		t.Fatalf("rename: %v\n%s", err, out)
	}
	time.Sleep(100 * time.Millisecond)

	windows, _ = tc.ListWindows()
	var renamedID, renamedName string
	for _, w := range windows {
		if w.ID == alphaID {
			renamedID = w.ID
			renamedName = w.Name
		}
	}
	if renamedID != alphaID {
		t.Errorf("WindowID should be stable across rename; got %q, want %q", renamedID, alphaID)
	}
	if renamedName != "alpha [wip]" {
		t.Errorf("rename didn't take; got %q", renamedName)
	}

	// Now drive the sidebar's matcher: with ownID=alphaID, itemMatchesOwnWindow
	// should still match a row that carries the same WindowID regardless of
	// the stale WindowName.
	item := SidebarItem{
		Name:       "alpha", // stale name
		WindowName: "alpha",
		WindowID:   alphaID,
	}
	if !itemMatchesOwnWindow(item, alphaID, "alpha [wip]") {
		t.Errorf("itemMatchesOwnWindow should match by WindowID after rename")
	}
}
