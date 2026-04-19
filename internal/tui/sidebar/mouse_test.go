package sidebar

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
func newMouseModel(items []SidebarItem) Model {
	return Model{
		items:         items,
		activeTermIdx: -1,
		focusSection:  "sessions",
		cursor:        0,
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
