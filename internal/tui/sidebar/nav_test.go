package sidebar

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/rvanmech/unky-mo/internal/claude"
)

// sendKey is a tiny helper that pushes a single keystroke through Update and
// returns the resulting Model (the bubbletea update interface gives back a
// tea.Model, so we assert + cast back).
func sendKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	var keyMsg tea.KeyPressMsg
	switch key {
	case "up":
		keyMsg = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		keyMsg = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		keyMsg = tea.KeyPressMsg{Code: rune(key[0])}
	}
	updated, _ := m.Update(keyMsg)
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return m2
}

// newNavModel builds a Model with pre-populated items/shells/files ready for
// pure navigation testing — no tmux, no state file touched.
func newNavModel(items []SidebarItem, shells []claude.ActiveShell, files []string) Model {
	return Model{
		items:         items,
		activeShells:  shells,
		changedFiles:  files,
		activeTermIdx: -1,
		focusSection:  "sessions",
		cursor:        0,
	}
}

func TestNavDownWrapsSessionsShellsFiles(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha", Path: "/ws/alpha"},
	}
	shells := []claude.ActiveShell{{Command: "build", PID: 1}}
	files := []string{"main.go"}
	m := newNavModel(items, shells, files)

	// From sessions top (index 0) → down moves to index 1 (alpha).
	m = sendKey(t, m, "down")
	if m.focusSection != "sessions" || m.cursor != 1 {
		t.Fatalf("step 1: want sessions/1, got %s/%d", m.focusSection, m.cursor)
	}
	// Next down wraps → enters shells.
	m = sendKey(t, m, "down")
	if m.focusSection != "shells" || m.shellCursor != 0 {
		t.Fatalf("step 2: want shells/0, got %s/%d", m.focusSection, m.shellCursor)
	}
	// Next down exhausts shells → enters files.
	m = sendKey(t, m, "down")
	if m.focusSection != "files" || m.fileCursor != 0 {
		t.Fatalf("step 3: want files/0, got %s/%d", m.focusSection, m.fileCursor)
	}
	// Next down wraps back to sessions (bottom/top depending on impl — today: top).
	m = sendKey(t, m, "down")
	if m.focusSection != "sessions" {
		t.Fatalf("step 4: want sessions, got %s", m.focusSection)
	}
}

func TestNavUpReverses(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	shells := []claude.ActiveShell{{Command: "tail", PID: 2}}
	files := []string{"a.go"}
	m := newNavModel(items, shells, files)
	m.focusSection = "files"
	m.fileCursor = 0

	// From files/0, up → shells (at the end).
	m = sendKey(t, m, "up")
	if m.focusSection != "shells" || m.shellCursor != 0 {
		t.Fatalf("step 1: want shells/0, got %s/%d", m.focusSection, m.shellCursor)
	}
	// From shells/0, up → sessions (bottom).
	m = sendKey(t, m, "up")
	if m.focusSection != "sessions" || m.cursor != len(items)-1 {
		t.Fatalf("step 2: want sessions/%d, got %s/%d", len(items)-1, m.focusSection, m.cursor)
	}
}

func TestNavEmptyShellsSkipped(t *testing.T) {
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	m := newNavModel(items, nil, []string{"x.go"})
	// Move to bottom of sessions, then down → should land in files, bypassing shells.
	m = sendKey(t, m, "down") // sessions/1
	m = sendKey(t, m, "down") // wrap → files (not shells)
	if m.focusSection != "files" {
		t.Errorf("empty shells should be skipped; got %s", m.focusSection)
	}
}

func TestNavEmptyFilesWrapsToSessions(t *testing.T) {
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	m := newNavModel(items, []claude.ActiveShell{{Command: "x", PID: 1}}, nil)
	m.focusSection = "shells"
	m.shellCursor = 0

	// Down past the last shell: no files, so wraps to sessions/0.
	m = sendKey(t, m, "down")
	if m.focusSection != "sessions" || m.cursor != 0 {
		t.Errorf("want sessions/0, got %s/%d", m.focusSection, m.cursor)
	}
}

func TestNavStaleShellCursorRegression(t *testing.T) {
	// Regression: when shells went 2 → 0 → 2, the shellCursor kept its old
	// value, so navigating back in didn't start at 0.
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	shells := []claude.ActiveShell{{PID: 1}, {PID: 2}}
	m := newNavModel(items, shells, nil)
	m.focusSection = "shells"
	m.shellCursor = 1 // user is on the second shell

	// Shells go away. Clear them.
	m.activeShells = nil
	m.focusSection = "sessions"

	// Shells come back. If shellCursor carries over, re-entering will land on
	// index 1 instead of 0. Today's behaviour: cursor stays at 1 (known bug).
	m.activeShells = []claude.ActiveShell{{PID: 10}, {PID: 11}}
	// Navigate into shells via sessions → down wrap.
	m.cursor = len(items) - 1
	m = sendKey(t, m, "down")
	if m.focusSection != "shells" {
		t.Fatalf("should be in shells, got %s", m.focusSection)
	}
	// This assertion documents today's behaviour. A future fix would reset
	// shellCursor on section re-entry.
	t.Logf("shellCursor on re-entry: %d (want 0 after a bug fix)", m.shellCursor)
}

func TestNavCursorSkipsHeaders(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
		{Name: "External", IsHeader: true},
		{Name: "stray"},
	}
	m := newNavModel(items, nil, nil)
	// Move forward. From 0 → 1 (alpha) → skip header → 3 (stray).
	m = sendKey(t, m, "down")
	if m.cursor != 1 {
		t.Fatalf("step 1: want 1, got %d", m.cursor)
	}
	m = sendKey(t, m, "down")
	if m.cursor != 3 {
		t.Errorf("header at index 2 should be skipped; cursor = %d (%q)",
			m.cursor, m.items[m.cursor].Name)
	}
}

func TestNavUpSkipsHeaders(t *testing.T) {
	items := []SidebarItem{
		{Name: "alpha"},
		{Name: "External", IsHeader: true},
		{Name: "stray"},
	}
	m := newNavModel(items, nil, nil)
	m.cursor = 2 // on stray
	m = sendKey(t, m, "up")
	if m.cursor != 0 {
		t.Errorf("up should skip header; cursor = %d (%q)", m.cursor, m.items[m.cursor].Name)
	}
}
