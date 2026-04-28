package sidebar

import (
	"errors"
	"testing"

	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// drawerModelTermSession is the mo-terms session the newDrawerModel test
// helper expects — derived from the model's windowID ("@1" → "mo-terms-1")
// so the per-window scoping logic is exercised end-to-end.
const drawerModelTermSession = "mo-terms-1"

// expectEnsureMoTermsFirstTime sets up the full lazy-creation sequence that
// ensureMoTerms performs when mo-terms does not yet exist. ghostPaneID is
// the pane id returned by NewDetachedSession for the session's initial
// default window — callers of ensureMoTerms either kill it (hide path) or
// track it as a tab (popup path). Callers whose test already brought
// mo-terms into existence should use expectEnsureMoTermsExists instead.
func expectEnsureMoTermsFirstTime(tmux *mock_sidebar.MockTmuxClient, cwd, ghostPaneID string) {
	expectLegacyTabUnbind(tmux)
	tmux.EXPECT().SessionExistsNamed(drawerModelTermSession).Return(false)
	tmux.EXPECT().NewDetachedSession(drawerModelTermSession, cwd).Return(ghostPaneID, nil)
	tmux.EXPECT().SetSessionOption(drawerModelTermSession, "key-table", "popup-keys").Return(nil)
	tmux.EXPECT().SetSessionOption(drawerModelTermSession, "mouse", "on").Return(nil)
	tmux.EXPECT().BindKey("popup-keys", "`", "detach-client").Return(nil)
	tmux.EXPECT().BindKey("popup-keys", "WheelUpPane", "copy-mode", "-e").Return(nil)
	tmux.EXPECT().BindKey("popup-keys", "WheelDownPane", "send-keys", "-M").Return(nil)
	tmux.EXPECT().BindKey("popup-keys", "MouseDrag1Pane", "copy-mode", "-M").Return(nil)
}

// expectEnsureMoTermsExists is the fast path — mo-terms is already around,
// so ensure just confirms and returns.
func expectEnsureMoTermsExists(tmux *mock_sidebar.MockTmuxClient) {
	expectLegacyTabUnbind(tmux)
	tmux.EXPECT().SessionExistsNamed(drawerModelTermSession).Return(true)
}

// expectLegacyTabUnbind matches the Tab/BTab cleanup that ensureMoTerms
// runs on every call to purge bindings left over by older sidebar
// versions that bound Tab → next-window inside the backtick popup.
func expectLegacyTabUnbind(tmux *mock_sidebar.MockTmuxClient) {
	tmux.EXPECT().UnbindKey("popup-keys", "Tab").Return(nil)
	tmux.EXPECT().UnbindKey("popup-keys", "BTab").Return(nil)
}

// newDrawerModel builds a minimal Model for drawer-state tests — a mocked
// tmux client and just enough state to exercise openDrawer/closeDrawer/etc.
func newDrawerModel(t *testing.T) (*Model, *mock_sidebar.MockTmuxClient) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	// SessionName is called by every target-building helper.
	tmux.EXPECT().SessionName().Return("mo").AnyTimes()

	m := &Model{
		tmux:          tmux,
		windowName:    "alpha",
		windowID:      "@1",
		windowPath:    "/ws/alpha",
		activeTermIdx: -1,
		focusSection:  "sessions",
	}
	return m, tmux
}

func TestOpenDrawerCreatesFirstTerminal(t *testing.T) {
	m, tmux := newDrawerModel(t)

	// No existing terminals → openDrawer falls through to createTerminalPane.
	tmux.EXPECT().SplitWindowHorizontal("mo:alpha.0", "/ws/alpha").Return("%10", nil)
	tmux.EXPECT().SelectPane("%10").Return(nil)

	cmd := m.openDrawer()
	if cmd == nil {
		t.Fatal("openDrawer returned nil status cmd")
	}
	if !m.drawerOpen {
		t.Error("drawerOpen should be true")
	}
	if len(m.terminals) != 1 || m.terminals[0].PaneID != "%10" {
		t.Errorf("terminals: %+v", m.terminals)
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx: got %d want 0", m.activeTermIdx)
	}
}

func TestOpenDrawerRejoinsHiddenTerminal(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0 // "last active" hint, drawer currently closed
	m.drawerOpen = false

	tmux.EXPECT().JoinPaneVertical("%10", "mo:alpha.0").Return(nil)
	tmux.EXPECT().SelectPane("%10").Return(nil)

	m.openDrawer()
	if !m.drawerOpen {
		t.Error("drawer should be open after re-join")
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx: %d", m.activeTermIdx)
	}
}

func TestOpenDrawerJoinFailureSurfacesError(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0

	tmux.EXPECT().JoinPaneVertical("%10", "mo:alpha.0").Return(errors.New("no session"))

	cmd := m.openDrawer()
	if cmd == nil {
		t.Fatal("want status cmd surfacing the error")
	}
	if m.drawerOpen {
		t.Error("drawer should NOT be open on join failure")
	}
}

func TestCloseDrawerHidesActivePane(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0
	m.drawerOpen = true

	// First hide — mo-terms is lazily created with ghost pane %99, then
	// %10 is parked there and the ghost is killed so only real tabs remain.
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%99")
	tmux.EXPECT().BreakPaneToSession("%10", drawerModelTermSession).Return(nil)
	tmux.EXPECT().KillPane("%99").Return(nil)

	m.closeDrawer()
	if m.drawerOpen {
		t.Error("drawerOpen should be false after closeDrawer")
	}
	// activeTermIdx is preserved so reopen targets the same terminal.
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx should be preserved; got %d", m.activeTermIdx)
	}
}

func TestNewTerminalHidesCurrentAndCreatesFresh(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0
	m.drawerOpen = true
	m.termCounter = 1

	// Hide %10 into mo-terms (lazy-create session on first call, ghost %99
	// gets killed after the park).
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%99")
	tmux.EXPECT().BreakPaneToSession("%10", drawerModelTermSession).Return(nil)
	tmux.EXPECT().KillPane("%99").Return(nil)
	// Then create new.
	tmux.EXPECT().SplitWindowHorizontal("mo:alpha.0", "/ws/alpha").Return("%11", nil)
	tmux.EXPECT().SelectPane("%11").Return(nil)

	m.newTerminal()
	if len(m.terminals) != 2 {
		t.Fatalf("want 2 terminals, got %d", len(m.terminals))
	}
	if m.activeTermIdx != 1 {
		t.Errorf("new terminal should be active; got idx %d", m.activeTermIdx)
	}
	if !m.drawerOpen {
		t.Error("drawer should be open")
	}
}

func TestCloseTerminalLastOneAutoClosesDrawer(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0
	m.drawerOpen = true

	tmux.EXPECT().KillPane("%10").Return(nil)

	m.closeTerminal()
	if len(m.terminals) != 0 {
		t.Errorf("terminals not cleaned: %+v", m.terminals)
	}
	if m.drawerOpen {
		t.Error("drawer should auto-close when last terminal killed")
	}
	if m.activeTermIdx != -1 {
		t.Errorf("activeTermIdx should be -1; got %d", m.activeTermIdx)
	}
}

func TestCloseTerminalPromotesNext(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"},
		{PaneID: "%11", Name: "term-2"},
	}
	m.activeTermIdx = 0
	m.drawerOpen = true

	tmux.EXPECT().KillPane("%10").Return(nil)
	// After removing %10, list is [%11]; the code joins %11 into the window.
	tmux.EXPECT().JoinPaneVertical("%11", "mo:alpha.0").Return(nil)
	tmux.EXPECT().SelectPane("%11").Return(nil)

	m.closeTerminal()
	if len(m.terminals) != 1 || m.terminals[0].PaneID != "%11" {
		t.Errorf("remaining terminals: %+v", m.terminals)
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx should point to remaining terminal; got %d", m.activeTermIdx)
	}
	if !m.drawerOpen {
		t.Error("drawer should stay open with surviving terminal")
	}
}

func TestCycleTerminalMovesToNext(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"},
		{PaneID: "%11", Name: "term-2"},
	}
	m.activeTermIdx = 0
	m.drawerOpen = true

	// Hide %10 by parking it in mo-terms (lazy-create on first call, ghost
	// %99 killed afterwards).
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%99")
	tmux.EXPECT().BreakPaneToSession("%10", drawerModelTermSession).Return(nil)
	tmux.EXPECT().KillPane("%99").Return(nil)
	// Then join %11.
	tmux.EXPECT().JoinPaneVertical("%11", "mo:alpha.0").Return(nil)
	tmux.EXPECT().SelectPane("%11").Return(nil)

	m.cycleTerminal(1)
	if m.activeTermIdx != 1 {
		t.Errorf("cycleTerminal(+1): activeTermIdx = %d, want 1", m.activeTermIdx)
	}
}

func TestCycleTerminalIsNoopWhenLessThanTwo(t *testing.T) {
	m, _ := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10"}}
	m.activeTermIdx = 0
	if cmd := m.cycleTerminal(1); cmd != nil {
		t.Error("cycleTerminal with <2 terminals should be nil")
	}
}

func TestRefreshTerminalsPrunesDeadPanes(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"},
		{PaneID: "%11", Name: "term-2"}, // this one is dead
	}
	m.activeTermIdx = 0
	m.drawerOpen = true
	// items must already have the non-terminal rows for refreshTerminals to append.
	m.items = []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}

	tmux.EXPECT().IsPaneAlive("%10").Return(true)
	tmux.EXPECT().IsPaneAlive("%11").Return(false)

	m.refreshTerminals()
	if len(m.terminals) != 1 || m.terminals[0].PaneID != "%10" {
		t.Errorf("dead pane not pruned: %+v", m.terminals)
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx should stay on surviving pane; got %d", m.activeTermIdx)
	}
	if !m.drawerOpen {
		t.Error("drawer should stay open — active pane is still alive")
	}
}

func TestRefreshTerminalsActivePaneDiedClosesDrawer(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"}, // this one is dead AND active
		{PaneID: "%11", Name: "term-2"},
	}
	m.activeTermIdx = 0
	m.drawerOpen = true
	m.items = []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}

	tmux.EXPECT().IsPaneAlive("%10").Return(false)
	tmux.EXPECT().IsPaneAlive("%11").Return(true)

	m.refreshTerminals()
	if len(m.terminals) != 1 || m.terminals[0].PaneID != "%11" {
		t.Errorf("terminals: %+v", m.terminals)
	}
	if m.drawerOpen {
		t.Error("drawer should close when the active pane was pruned")
	}
}

func TestRefreshTerminalsAllGoneDrawerCloses(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"},
	}
	m.activeTermIdx = 0
	m.drawerOpen = true
	m.items = []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}

	tmux.EXPECT().IsPaneAlive("%10").Return(false)

	m.refreshTerminals()
	if len(m.terminals) != 0 {
		t.Errorf("terminals should be empty, got %+v", m.terminals)
	}
	if m.drawerOpen {
		t.Error("drawer should close when every terminal is dead")
	}
	if m.activeTermIdx != -1 {
		t.Errorf("activeTermIdx should be -1, got %d", m.activeTermIdx)
	}
}

func TestCreateTerminalPaneSplitFailure(t *testing.T) {
	m, tmux := newDrawerModel(t)
	tmux.EXPECT().SplitWindowHorizontal(gomock.Any(), gomock.Any()).Return("", errors.New("no session"))

	cmd := m.createTerminalPane()
	if cmd == nil {
		t.Fatal("want error status cmd")
	}
	if len(m.terminals) != 0 {
		t.Error("no terminal should be recorded on split failure")
	}
	if m.drawerOpen {
		t.Error("drawer should not be marked open on split failure")
	}
}

func TestEnsureMoTermsFirstTimeCreatesSession(t *testing.T) {
	m, tmux := newDrawerModel(t)
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%99")

	ghost, err := m.ensureMoTerms()
	if err != nil {
		t.Fatalf("ensureMoTerms: %v", err)
	}
	if ghost != "%99" {
		t.Errorf("ghost pane id: got %q, want %%99", ghost)
	}
}

func TestEnsureMoTermsIdempotent(t *testing.T) {
	m, tmux := newDrawerModel(t)
	// Second call with an existing session must not re-create or re-bind.
	expectEnsureMoTermsExists(tmux)

	ghost, err := m.ensureMoTerms()
	if err != nil {
		t.Fatalf("ensureMoTerms: %v", err)
	}
	if ghost != "" {
		t.Errorf("idempotent path must return empty ghost id, got %q", ghost)
	}
}

func TestOpenPopupClosesDrawerFirst(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0
	m.drawerOpen = true

	// hidePane on the active tab creates mo-terms with ghost %99, parks
	// %10, and kills the ghost so only the real tab remains.
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%99")
	tmux.EXPECT().BreakPaneToSession("%10", drawerModelTermSession).Return(nil)
	tmux.EXPECT().KillPane("%99").Return(nil)
	// openPopup's own ensureMoTerms call is now the fast path.
	expectEnsureMoTermsExists(tmux)
	// Pre-select the active tab's window inside mo-terms.
	tmux.EXPECT().SelectWindowByPane("%10").Return(nil)

	cmd := m.openPopup()
	if cmd == nil {
		t.Fatal("openPopup returned nil")
	}
	if m.drawerOpen {
		t.Error("openPopup should have auto-closed the drawer")
	}
	if len(m.terminals) != 1 {
		t.Errorf("tab list should still have one entry, got %d", len(m.terminals))
	}
	// The returned tea.Cmd would exec tmux display-popup — we deliberately
	// don't invoke it here so tests don't shell out.
}

func TestOpenPopupFirstTimeTracksGhostAsTab(t *testing.T) {
	m, tmux := newDrawerModel(t)
	// Drawer closed, no terminals tracked. The first `` ` `` creates
	// mo-terms — its default shell must be tracked as term-1 so the
	// sidebar's terminal list shows it.
	expectEnsureMoTermsFirstTime(tmux, "/ws/alpha", "%50")
	tmux.EXPECT().SelectWindowByPane("%50").Return(nil)

	cmd := m.openPopup()
	if cmd == nil {
		t.Fatal("openPopup returned nil")
	}
	if len(m.terminals) != 1 || m.terminals[0].PaneID != "%50" {
		t.Fatalf("ghost pane not tracked as tab: %+v", m.terminals)
	}
	if m.terminals[0].Name != "term-1" {
		t.Errorf("tab name: got %q, want term-1", m.terminals[0].Name)
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx should point at the new tab; got %d", m.activeTermIdx)
	}
}

func TestOpenPopupWithoutDrawerExistingTerminals(t *testing.T) {
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{{PaneID: "%10", Name: "term-1"}}
	m.activeTermIdx = 0
	m.drawerOpen = false

	// mo-terms already exists — no ghost to track. Pre-select the active
	// tab so the popup lands on it.
	expectEnsureMoTermsExists(tmux)
	tmux.EXPECT().SelectWindowByPane("%10").Return(nil)

	cmd := m.openPopup()
	if cmd == nil {
		t.Fatal("openPopup returned nil")
	}
	if m.drawerOpen {
		t.Error("drawerOpen should remain false")
	}
	if len(m.terminals) != 1 {
		t.Errorf("existing tab list should not change, got %+v", m.terminals)
	}
}

func TestRefreshTerminalsAppendsItems(t *testing.T) {
	// refreshTerminals appends "Terminals" header + per-terminal rows to items.
	m, tmux := newDrawerModel(t)
	m.terminals = []TerminalTab{
		{PaneID: "%10", Name: "term-1"},
		{PaneID: "%11", Name: "term-2"},
	}
	m.activeTermIdx = 0
	m.drawerOpen = true
	m.items = []SidebarItem{{Name: "Unky Mo Home", IsHome: true}}

	tmux.EXPECT().IsPaneAlive("%10").Return(true)
	tmux.EXPECT().IsPaneAlive("%11").Return(true)

	m.refreshTerminals()
	// Expect: Home + "Terminals" header + 2 rows.
	if len(m.items) != 4 {
		t.Fatalf("want 4 items, got %d: %+v", len(m.items), m.items)
	}
	if !m.items[1].IsHeader || m.items[1].Name != "Terminals" {
		t.Errorf("expected Terminals header at index 1, got %+v", m.items[1])
	}
	if !m.items[2].IsActive {
		t.Error("first terminal should be marked IsActive when drawerOpen+idx=0")
	}
	if m.items[3].IsActive {
		t.Error("second terminal should NOT be active")
	}
}
