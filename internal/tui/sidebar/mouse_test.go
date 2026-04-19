package sidebar

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rvanmech/unky-mo/internal/claude"
	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// sendMouseClick pushes a left-press mouse event at (x=0, y) through Update
// and returns the resulting Model. The returned cmd is discarded — we only
// check state transitions, not what the click would do once dispatched.
func sendMouseClick(t *testing.T, m Model, y int) (Model, tea.Cmd) {
	t.Helper()
	msg := tea.MouseMsg{
		X:      0,
		Y:      y,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	updated, cmd := m.Update(msg)
	m2, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", updated)
	}
	return m2, cmd
}

// newMouseModel builds a Model wired for pure mouse-routing tests — no tmux,
// no state file, no resolver. cursor starts at 0, viewportStart at 0.
// width/height are generous so computeLayout renders every item.
func newMouseModel(items []SidebarItem) Model {
	return Model{
		items:         items,
		activeTermIdx: -1,
		focusSection:  "sessions",
		cursor:        0,
		width:         42,
		height:        200,
	}
}

// Top row (Y=0) is the "Sessions" section header — not a clickable item.
func TestMouseClickOnHeaderRowIsNoop(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	m := newMouseModel(items)
	m.cursor = 1

	m, cmd := sendMouseClick(t, m, 0)

	if m.cursor != 1 {
		t.Errorf("Y=0 click should not move cursor; got %d, want 1", m.cursor)
	}
	if cmd != nil {
		t.Errorf("Y=0 click should not return a cmd; got %T", cmd)
	}
}

// Y=1 maps to items[0] when viewportStart=0 (the first visible item sits
// right below the "Sessions" header).
func TestMouseClickFirstItemMovesCursor(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha", Path: "/ws/alpha"},
	}
	m := newMouseModel(items)
	m.cursor = 1 // start on alpha

	m, cmd := sendMouseClick(t, m, 1)

	if m.cursor != 0 {
		t.Errorf("click on Y=1 should land on items[0]; got cursor=%d", m.cursor)
	}
	if cmd == nil {
		t.Errorf("click on a real item should return a handleEnter cmd")
	}
}

// When the list has scrolled (viewportStart > 0), Y=1 maps to
// items[viewportStart], not items[0].
func TestMouseClickRespectsViewportStart(t *testing.T) {
	items := make([]SidebarItem, 10)
	for i := range items {
		items[i] = SidebarItem{Name: "row", Path: "/ws/row"}
	}
	m := newMouseModel(items)
	m.viewportStart = 3

	m, _ = sendMouseClick(t, m, 1)

	if m.cursor != 3 {
		t.Errorf("viewportStart=3, click Y=1 → cursor should be 3; got %d", m.cursor)
	}
}

// Inline section headers (the "External" row for strays, the "Terminals"
// row the drawer adds) are in m.items with IsHeader=true. Clicks on their
// row must not move the cursor onto a non-selectable row.
func TestMouseClickOnInlineHeaderIsNoop(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
		{Name: "External", IsHeader: true, Section: "external"},
		{Name: "stray"},
	}
	m := newMouseModel(items)
	m.cursor = 1

	// Y=3 → items[2], the "External" header.
	m, cmd := sendMouseClick(t, m, 3)

	if m.cursor != 1 {
		t.Errorf("click on inline header should not move cursor; got %d, want 1", m.cursor)
	}
	if cmd != nil {
		t.Errorf("click on inline header should not return a cmd")
	}
}

// Clicks past the rendered items fall on the shells/files/footer region,
// which the current handler doesn't route. They must be no-ops, not OOB
// index reads.
func TestMouseClickPastItemsIsNoop(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	m := newMouseModel(items)
	m.cursor = 1

	// Y=5 is well past the two items + header.
	m, cmd := sendMouseClick(t, m, 5)

	if m.cursor != 1 {
		t.Errorf("click past items should not move cursor; got %d, want 1", m.cursor)
	}
	if cmd != nil {
		t.Errorf("click past items should not return a cmd")
	}
}

// Clicks on terminal-tab rows must move the cursor onto the tab and dispatch
// the switch handler. switchTerminalIdx does its tmux work eagerly so we need
// a mock here; the specific tmux calls are an implementation detail, so they
// use AnyTimes gomock expectations.
func TestMouseClickOnTerminalTabMovesCursor(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()
	tmux.EXPECT().JoinPaneVertical(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	tmux.EXPECT().SelectPane(gomock.Any()).Return(nil).AnyTimes()
	tmux.EXPECT().BreakPane(gomock.Any()).Return(nil).AnyTimes()

	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
		{Name: "Terminals", IsHeader: true},
		{Name: "1: term-1", IsTerminal: true, PaneID: "%5"},
		{Name: "2: term-2", IsTerminal: true, PaneID: "%6"},
	}
	m := newMouseModel(items)
	m.tmux = tmux
	m.windowName = "alpha"
	m.terminals = []TerminalTab{{PaneID: "%5", Name: "term-1"}, {PaneID: "%6", Name: "term-2"}}

	// Y=4 → items[3] = term-1 tab.
	m, cmd := sendMouseClick(t, m, 4)

	if m.cursor != 3 {
		t.Errorf("click on terminal tab row should move cursor; got %d, want 3", m.cursor)
	}
	if cmd == nil {
		t.Errorf("click on terminal tab should return a switch cmd")
	}
}

// Non-left buttons and motion events must not move the cursor.
func TestMouseNonLeftButtonIgnored(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	m := newMouseModel(items)

	for _, btn := range []tea.MouseButton{tea.MouseButtonRight, tea.MouseButtonMiddle} {
		msg := tea.MouseMsg{Y: 1, Button: btn, Action: tea.MouseActionPress}
		updated, cmd := m.Update(msg)
		got, _ := updated.(Model)
		if got.cursor != 0 {
			t.Errorf("button %v: cursor should not move; got %d", btn, got.cursor)
		}
		if cmd != nil {
			t.Errorf("button %v: should not return a cmd", btn)
		}
	}

	// Motion event also ignored.
	motion := tea.MouseMsg{Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
	updated, cmd := m.Update(motion)
	got, _ := updated.(Model)
	if got.cursor != 0 {
		t.Errorf("motion: cursor should not move; got %d", got.cursor)
	}
	if cmd != nil {
		t.Errorf("motion: should not return a cmd")
	}
}

// Release events (e.g. after a press) must not re-trigger handleEnter.
func TestMouseReleaseIgnored(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	m := newMouseModel(items)
	m.cursor = 1

	msg := tea.MouseMsg{Y: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease}
	updated, cmd := m.Update(msg)
	got, _ := updated.(Model)

	if got.cursor != 1 {
		t.Errorf("release: cursor should not move; got %d", got.cursor)
	}
	if cmd != nil {
		t.Errorf("release: should not return a cmd")
	}
}

// Regression guard: when the window pane narrows to width=0 or items list
// is empty, a click must not panic.
func TestMouseClickEmptyItemsNoPanic(t *testing.T) {
	m := newMouseModel(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("click on empty-items sidebar panicked: %v", r)
		}
	}()
	_, _ = sendMouseClick(t, m, 1)
}

// Click on a shell row focuses the shells section and fires showShellOutput.
// OutputFile is left empty so the cmd short-circuits to a statusCmd, avoiding
// the tmux display-popup exec path.
func TestMouseClickOnShellRowFocusesShells(t *testing.T) {
	items := []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha"},
	}
	m := newMouseModel(items)
	m.activeShells = []claude.ActiveShell{
		{Command: "go build", PID: 1},
		{Command: "npm run dev", PID: 2},
	}

	// Layout with 2 items (Y=1..2), no scroll indicators, then blank Y=3,
	// shells header Y=4, shell rows Y=5, Y=6.
	l := m.computeLayout()
	if l.shellsY0 < 0 {
		t.Fatalf("expected shells region visible; layout=%+v", l)
	}

	m, cmd := sendMouseClick(t, m, l.shellsY0+1) // second shell

	if m.focusSection != "shells" {
		t.Errorf("focus should be shells, got %q", m.focusSection)
	}
	if m.shellCursor != 1 {
		t.Errorf("shellCursor should be 1, got %d", m.shellCursor)
	}
	if cmd == nil {
		t.Errorf("click on shell row should return a cmd")
	}
}

// Click on the "Shells (N)" header is a no-op — no focus shift, no cmd.
func TestMouseClickOnShellsHeaderIsNoop(t *testing.T) {
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	m := newMouseModel(items)
	m.activeShells = []claude.ActiveShell{{Command: "go build", PID: 1}}
	l := m.computeLayout()

	m, cmd := sendMouseClick(t, m, l.shellsHdrY)

	if m.focusSection != "sessions" {
		t.Errorf("focus should stay on sessions, got %q", m.focusSection)
	}
	if cmd != nil {
		t.Errorf("click on shells header should not return a cmd")
	}
}

// Click on a changed-file row focuses the files section and fires the diff
// popup. windowPath is empty so showDiffPopup returns a statusCmd (avoids
// spawning less).
func TestMouseClickOnFileRowFocusesFiles(t *testing.T) {
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	m := newMouseModel(items)
	m.changedFiles = []string{"a.go", "b.go"}
	l := m.computeLayout()
	if len(l.filesRows) == 0 {
		t.Fatalf("expected files region visible; layout=%+v", l)
	}

	// Find the Y of fileIndex=1 ("b.go").
	var targetY int
	found := false
	for y, idx := range l.filesRows {
		if idx == 1 {
			targetY = y
			found = true
		}
	}
	if !found {
		t.Fatalf("couldn't find fileIndex=1 in filesRows: %+v", l.filesRows)
	}

	m, cmd := sendMouseClick(t, m, targetY)

	if m.focusSection != "files" {
		t.Errorf("focus should be files, got %q", m.focusSection)
	}
	if m.fileCursor != 1 {
		t.Errorf("fileCursor should be 1, got %d", m.fileCursor)
	}
	if cmd == nil {
		t.Errorf("click on file row should return a diff cmd")
	}
}

// Files live under a tree: directory rows (fileIndex == -1) render but
// aren't selectable. Clicks on them must be no-ops.
func TestMouseClickOnDirectoryRowIsNoop(t *testing.T) {
	items := []SidebarItem{{Name: "Home", IsHome: true}, {Name: "alpha"}}
	m := newMouseModel(items)
	// Nested paths give us a directory node (internal/...).
	m.changedFiles = []string{"internal/a.go", "internal/b.go"}
	l := m.computeLayout()

	// buildFileTreeLines produces at least one directory line. Find a Y
	// within the files region that is NOT in filesRows — that's either the
	// header or a directory row; header is already separately tested, so
	// pick a Y between header and the first mapped file row.
	var dirY int
	foundDir := false
	for y := l.filesHdrY + 1; y < l.filesHdrY+10; y++ {
		if _, mapped := l.filesRows[y]; !mapped {
			dirY = y
			foundDir = true
			break
		}
	}
	if !foundDir {
		t.Skip("layout produced no directory rows — tree flattening changed")
	}

	m, cmd := sendMouseClick(t, m, dirY)

	if m.focusSection != "sessions" {
		t.Errorf("focus should stay on sessions, got %q", m.focusSection)
	}
	if cmd != nil {
		t.Errorf("click on directory row should not return a cmd")
	}
}

// Click on "▼ more" scrolls the items viewport down by one page.
func TestMouseClickScrollDownAdvancesViewport(t *testing.T) {
	items := make([]SidebarItem, 20)
	for i := range items {
		items[i] = SidebarItem{Name: "row", Path: "/ws/row"}
	}
	m := newMouseModel(items)
	m.height = 8 // tight height → many items scroll off

	l := m.computeLayout()
	if l.scrollDownY < 0 {
		t.Fatalf("expected scrollDown visible; layout=%+v", l)
	}

	origStart := m.viewportStart
	m, cmd := sendMouseClick(t, m, l.scrollDownY)

	if m.viewportStart <= origStart {
		t.Errorf("viewportStart should advance; was %d, now %d", origStart, m.viewportStart)
	}
	if cmd != nil {
		t.Errorf("scroll click should not return a cmd")
	}
}

// Click on "▲ more" scrolls the items viewport back up.
func TestMouseClickScrollUpRewindsViewport(t *testing.T) {
	items := make([]SidebarItem, 20)
	for i := range items {
		items[i] = SidebarItem{Name: "row", Path: "/ws/row"}
	}
	m := newMouseModel(items)
	m.height = 8
	m.viewportStart = 10 // already scrolled mid-list

	l := m.computeLayout()
	if l.scrollUpY < 0 {
		t.Fatalf("expected scrollUp visible; layout=%+v", l)
	}

	origStart := m.viewportStart
	m, cmd := sendMouseClick(t, m, l.scrollUpY)

	if m.viewportStart >= origStart {
		t.Errorf("viewportStart should rewind; was %d, now %d", origStart, m.viewportStart)
	}
	if m.viewportStart < 0 {
		t.Errorf("viewportStart should not go negative; got %d", m.viewportStart)
	}
	if cmd != nil {
		t.Errorf("scroll click should not return a cmd")
	}
}

