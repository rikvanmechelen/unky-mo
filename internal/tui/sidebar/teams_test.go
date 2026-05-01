package sidebar

import (
	"testing"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

func TestRefreshState_WithTeammates(t *testing.T) {
	m, _, _, sf := newTestModel(t, "my-project", "@5")
	writeStateFile(t, sf, &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{
				Name:       "my-project",
				Path:       "/ws/my-project",
				WindowName: "my-project",
				WindowID:   "@5",
				Status:     "active",
				SessionID:  "sess-lead",
				TeamName:   "review-team",
				TeamRole:   "lead",
				Teammates: []state.TeammateState{
					{Name: "architect", Status: "active", PaneID: "%3"},
					{Name: "tester", Status: "idle", PaneID: "%4"},
				},
			},
			{
				Name:       "other",
				Path:       "/ws/other",
				WindowName: "other",
				WindowID:   "@6",
				Status:     "idle",
			},
		},
	})
	m.refreshState()

	// items: Home + my-project (lead) + architect (teammate) + tester (teammate) + other
	if len(m.items) != 5 {
		t.Fatalf("want 5 items (Home + lead + 2 teammates + other), got %d: %+v", len(m.items), itemNames(m.items))
	}

	lead := m.items[1]
	if !lead.IsTeamLead {
		t.Errorf("lead item should have IsTeamLead=true")
	}
	if lead.Name != "my-project" {
		t.Errorf("lead Name: got %q", lead.Name)
	}

	arch := m.items[2]
	if !arch.IsTeammate {
		t.Error("architect should be IsTeammate")
	}
	if arch.TeammateName != "architect" {
		t.Errorf("TeammateName: got %q", arch.TeammateName)
	}
	if arch.TeamPaneID != "%3" {
		t.Errorf("TeamPaneID: got %q", arch.TeamPaneID)
	}
	if arch.Status != "active" {
		t.Errorf("Status: got %q", arch.Status)
	}
	if arch.Parent != "my-project" {
		t.Errorf("Parent should be lead name for indent, got %q", arch.Parent)
	}
	if arch.WindowID != "@5" {
		t.Errorf("WindowID should match lead, got %q", arch.WindowID)
	}

	tester := m.items[3]
	if !tester.IsTeammate || tester.TeammateName != "tester" || tester.TeamPaneID != "%4" {
		t.Errorf("tester item: %+v", tester)
	}
	if tester.Status != "idle" {
		t.Errorf("tester Status: got %q", tester.Status)
	}

	other := m.items[4]
	if other.IsTeamLead || other.IsTeammate {
		t.Error("other project should not have team flags")
	}
}

func TestRefreshState_NoTeammates_BackwardCompat(t *testing.T) {
	m, _, _, sf := newTestModel(t, "proj", "@1")
	writeStateFile(t, sf, &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{Name: "proj", Path: "/ws/proj", WindowName: "proj", WindowID: "@1", Status: "active"},
		},
	})
	m.refreshState()

	// items: Home + proj (no teammate sub-items)
	if len(m.items) != 2 {
		t.Fatalf("want 2 items, got %d", len(m.items))
	}
	if m.items[1].IsTeamLead || m.items[1].IsTeammate {
		t.Error("non-team project should not have team flags")
	}
}

func TestSwitchToSelected_Teammate(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	tmux.EXPECT().SelectPane("%3").Return(nil)

	m := &Model{
		tmux:         tmux,
		cursor:       0,
		focusSection: "sessions",
		items: []SidebarItem{
			{
				IsTeammate:   true,
				TeammateName: "architect",
				TeamPaneID:   "%3",
				WindowID:     "@5",
			},
		},
	}

	cmd := m.switchToSelected()
	msg := cmd()
	if msg == nil {
		t.Fatal("expected a status message")
	}
	status, ok := msg.(sidebarStatusMsg)
	if !ok {
		t.Fatalf("expected sidebarStatusMsg, got %T", msg)
	}
	if string(status) != "focused architect" {
		t.Errorf("status message: got %q", string(status))
	}
}

func TestSwitchToSelected_TeamLead_NormalSwitch(t *testing.T) {
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	// Team lead should use normal window switch via WindowID (not pane focus)
	tmux.EXPECT().SwitchToWindow("@5").Return(nil)

	m := &Model{
		tmux:         tmux,
		cursor:       0,
		focusSection: "sessions",
		items: []SidebarItem{
			{
				Name:       "my-project",
				WindowID:   "@5",
				IsTeamLead: true,
			},
		},
	}

	cmd := m.switchToSelected()
	cmd() // just verify no panic; SwitchToWindow expectation ensures correct call
}

func TestTeammateIndent(t *testing.T) {
	// Verify that teammate items get Parent set, which triggers the indent
	// logic in View() (the same mechanism used for worktrees).
	m, _, _, sf := newTestModel(t, "proj", "@5")
	writeStateFile(t, sf, &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{
				Name: "proj", Path: "/ws/proj", WindowName: "proj", WindowID: "@5",
				Status: "active", TeamRole: "lead",
				Teammates: []state.TeammateState{
					{Name: "worker", Status: "active", PaneID: "%2"},
				},
			},
		},
	})
	m.refreshState()

	// items: Home + proj + worker
	if len(m.items) != 3 {
		t.Fatalf("want 3 items, got %d", len(m.items))
	}
	worker := m.items[2]
	if worker.Parent == "" {
		t.Error("teammate item should have Parent set for indent rendering")
	}
	if worker.Parent != "proj" {
		t.Errorf("teammate Parent: got %q, want %q", worker.Parent, "proj")
	}
}

func TestTeammateNoPaneID_NoSelectPane(t *testing.T) {
	// When a teammate has no pane ID (in-process mode), switchToSelected
	// should fall through to window switching via WindowID.
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)

	// Should try window switch as fallback since TeamPaneID is empty
	tmux.EXPECT().SwitchToWindow("@5").Return(nil)

	m := &Model{
		tmux:         tmux,
		cursor:       0,
		focusSection: "sessions",
		items: []SidebarItem{
			{
				IsTeammate:   true,
				TeammateName: "worker",
				TeamPaneID:   "", // no pane ID
				WindowID:     "@5",
			},
		},
	}

	cmd := m.switchToSelected()
	cmd() // verify it falls through to SwitchToWindow
}

// itemNames is a test helper that returns item names for debug output.
func itemNames(items []SidebarItem) []string {
	names := make([]string, len(items))
	for i, item := range items {
		switch {
		case item.IsHome:
			names[i] = "[Home]"
		case item.IsHeader:
			names[i] = "[" + item.Name + "]"
		case item.IsTeammate:
			names[i] = "  (tm:" + item.TeammateName + ")"
		default:
			names[i] = item.Name
		}
	}
	return names
}
