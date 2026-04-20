package sidebar

import (
	"testing"

	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// Phase A6: Characterization tests for terminal drawer state survival across
// sidebar restarts. When ctrl+alt+r respawns the sidebar, the new process
// must rediscover terminals from the mo-terms session via refreshTerminals.

func TestRefreshTerminals_PrunesDeadPanes(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	m := &Model{
		tmux:     tmux,
		windowID: "@1",
		terminals: []TerminalTab{
			{PaneID: "%10", Name: "term-1"},
			{PaneID: "%11", Name: "term-2"},
			{PaneID: "%12", Name: "term-3"},
		},
		activeTermIdx: 1,
		drawerOpen:    true,
	}

	// %10 alive, %11 dead, %12 alive
	tmux.EXPECT().IsPaneAlive("%10").Return(true)
	tmux.EXPECT().IsPaneAlive("%11").Return(false)
	tmux.EXPECT().IsPaneAlive("%12").Return(true)

	m.refreshTerminals()

	if len(m.terminals) != 2 {
		t.Fatalf("want 2 terminals after pruning, got %d", len(m.terminals))
	}
	if m.terminals[0].PaneID != "%10" || m.terminals[1].PaneID != "%12" {
		t.Errorf("wrong survivors: %+v", m.terminals)
	}
}

func TestRefreshTerminals_ActiveTermPrunedClosesDrawer(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	m := &Model{
		tmux:     tmux,
		windowID: "@1",
		terminals: []TerminalTab{
			{PaneID: "%10", Name: "term-1"},
			{PaneID: "%11", Name: "term-2"},
		},
		activeTermIdx: 1, // %11 is active
		drawerOpen:    true,
	}

	tmux.EXPECT().IsPaneAlive("%10").Return(true)
	tmux.EXPECT().IsPaneAlive("%11").Return(false) // active terminal dies

	m.refreshTerminals()

	if m.drawerOpen {
		t.Error("drawer should close when the active terminal is pruned")
	}
	if m.activeTermIdx != 0 {
		t.Errorf("activeTermIdx should reset to 0, got %d", m.activeTermIdx)
	}
}

func TestRefreshTerminals_AllDeadClosesDrawer(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	m := &Model{
		tmux:     tmux,
		windowID: "@1",
		terminals: []TerminalTab{
			{PaneID: "%10", Name: "term-1"},
		},
		activeTermIdx: 0,
		drawerOpen:    true,
	}

	tmux.EXPECT().IsPaneAlive("%10").Return(false)

	m.refreshTerminals()

	if len(m.terminals) != 0 {
		t.Errorf("want 0 terminals, got %d", len(m.terminals))
	}
	if m.drawerOpen {
		t.Error("drawer should close when all terminals die")
	}
	if m.activeTermIdx != -1 {
		t.Errorf("activeTermIdx should be -1, got %d", m.activeTermIdx)
	}
}

func TestRefreshTerminals_NoTerminalsIsNoop(t *testing.T) {
	m := &Model{
		windowID:      "@1",
		activeTermIdx: -1,
	}

	// No tmux calls expected — terminals slice is empty so the pruning
	// loop doesn't execute.
	m.refreshTerminals()

	if m.drawerOpen {
		t.Error("drawer should stay closed")
	}
}

func TestRefreshTerminals_AppendsTerminalItemsToSidebar(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	m := &Model{
		tmux:     tmux,
		windowID: "@1",
		items:    []SidebarItem{{Name: "Home", IsHome: true}},
		terminals: []TerminalTab{
			{PaneID: "%10", Name: "term-1"},
			{PaneID: "%11", Name: "term-2"},
		},
		activeTermIdx: 0,
		drawerOpen:    true,
	}

	tmux.EXPECT().IsPaneAlive("%10").Return(true)
	tmux.EXPECT().IsPaneAlive("%11").Return(true)

	m.refreshTerminals()

	// items: Home + Terminals header + 2 terminal rows = 4
	if len(m.items) != 4 {
		t.Fatalf("want 4 items, got %d: %+v", len(m.items), m.items)
	}
	if !m.items[1].IsHeader || m.items[1].Name != "Terminals" {
		t.Errorf("item[1] should be Terminals header, got %+v", m.items[1])
	}
	if !m.items[2].IsTerminal || m.items[2].PaneID != "%10" {
		t.Errorf("item[2] should be terminal %%10, got %+v", m.items[2])
	}
	if !m.items[2].IsActive {
		t.Error("item[2] should be active (activeTermIdx=0)")
	}
	if m.items[3].IsActive {
		t.Error("item[3] should not be active")
	}
}
