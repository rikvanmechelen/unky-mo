package tui

import (
	"os"
	"path/filepath"
	"testing"

	mock_ops "github.com/rvanmech/unky-mo/internal/ops/mocks"
	ttmux "github.com/rvanmech/unky-mo/internal/tmux"
	"go.uber.org/mock/gomock"
)

func writeTeamConfig(t *testing.T, home, teamName, content string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "teams", teamName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEnrichViewsWithTeamInfo_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "review-team", `{
		"name": "review-team",
		"leadAgentId": "lead-1",
		"members": [
			{"name": "lead", "agentId": "lead-1", "agentType": "lead", "sessionId": "sess-lead"},
			{"name": "architect", "agentId": "tm-1", "agentType": "teammate"},
			{"name": "tester", "agentId": "tm-2", "agentType": "teammate"}
		]
	}`)

	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)

	// Lead window has 4 panes: lead, sidebar, architect, tester
	tmux.EXPECT().ListWindowPanes("@5").Return([]ttmux.PaneInfo{
		{ID: "%0", PID: 100}, // lead
		{ID: "%1", PID: 101}, // sidebar
		{ID: "%2", PID: 102}, // architect
		{ID: "%3", PID: 103}, // tester
	}, nil)

	views := []sessionView{
		{
			SessionID:   "sess-lead",
			PID:         100,
			CWD:         "/ws/project",
			WindowID:    "@5",
			WindowName:  "project",
			ProjectName: "project",
			Status:      StatusActive,
		},
	}

	enrichViewsWithTeamInfo(views, tmux)

	if views[0].TeamName != "review-team" {
		t.Errorf("TeamName: got %q, want %q", views[0].TeamName, "review-team")
	}
	if views[0].TeamRole != "lead" {
		t.Errorf("TeamRole: got %q, want %q", views[0].TeamRole, "lead")
	}
	if len(views[0].Teammates) != 2 {
		t.Fatalf("Teammates count: got %d, want 2", len(views[0].Teammates))
	}
	if views[0].Teammates[0].Name != "architect" || views[0].Teammates[0].PaneID != "%2" {
		t.Errorf("Teammates[0]: %+v", views[0].Teammates[0])
	}
	if views[0].Teammates[1].Name != "tester" || views[0].Teammates[1].PaneID != "%3" {
		t.Errorf("Teammates[1]: %+v", views[0].Teammates[1])
	}
}

func TestEnrichViewsWithTeamInfo_NoTeams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// No teams directory at all

	views := []sessionView{
		{SessionID: "s1", WindowID: "@1", Status: StatusActive},
	}
	enrichViewsWithTeamInfo(views, nil)

	if views[0].TeamName != "" {
		t.Errorf("TeamName should be empty when no teams exist, got %q", views[0].TeamName)
	}
}

func TestEnrichViewsWithTeamInfo_LeadNotInViews(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "orphan-team", `{
		"name": "orphan-team",
		"leadAgentId": "lead-x",
		"members": [
			{"name": "lead", "agentId": "lead-x", "agentType": "lead", "sessionId": "sess-missing"},
			{"name": "worker", "agentId": "tm-1", "agentType": "teammate"}
		]
	}`)

	views := []sessionView{
		{SessionID: "sess-other", WindowID: "@1", Status: StatusActive},
	}
	enrichViewsWithTeamInfo(views, nil)

	// Lead session not found in views — should not annotate anything
	if views[0].TeamName != "" {
		t.Errorf("TeamName should be empty when lead not found, got %q", views[0].TeamName)
	}
}

func TestEnrichViewsWithTeamInfo_NilTmuxClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "headless-team", `{
		"name": "headless-team",
		"leadAgentId": "lead-1",
		"members": [
			{"name": "lead", "agentId": "lead-1", "agentType": "lead", "sessionId": "sess-lead"},
			{"name": "worker", "agentId": "tm-1", "agentType": "teammate"}
		]
	}`)

	views := []sessionView{
		{SessionID: "sess-lead", WindowID: "@5", Status: StatusActive},
	}
	enrichViewsWithTeamInfo(views, nil)

	// Should still populate from config, just without pane IDs
	if views[0].TeamName != "headless-team" {
		t.Errorf("TeamName: got %q, want %q", views[0].TeamName, "headless-team")
	}
	if len(views[0].Teammates) != 1 {
		t.Fatalf("Teammates count: got %d, want 1", len(views[0].Teammates))
	}
	if views[0].Teammates[0].Name != "worker" {
		t.Errorf("Teammates[0].Name: got %q, want %q", views[0].Teammates[0].Name, "worker")
	}
	if views[0].Teammates[0].PaneID != "" {
		t.Errorf("Teammates[0].PaneID should be empty without tmux client, got %q", views[0].Teammates[0].PaneID)
	}
}

func TestEnrichViewsWithTeamInfo_FewerPanesThanTeammates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "partial-team", `{
		"name": "partial-team",
		"leadAgentId": "lead-1",
		"members": [
			{"name": "lead", "agentId": "lead-1", "agentType": "lead", "sessionId": "sess-lead"},
			{"name": "arch", "agentId": "tm-1", "agentType": "teammate"},
			{"name": "test", "agentId": "tm-2", "agentType": "teammate"}
		]
	}`)

	ctrl := gomock.NewController(t)
	tmux := mock_ops.NewMockTmuxClient(ctrl)

	// Only 3 panes (lead + sidebar + 1 teammate) — fewer than 2 teammates
	tmux.EXPECT().ListWindowPanes("@5").Return([]ttmux.PaneInfo{
		{ID: "%0", PID: 100},
		{ID: "%1", PID: 101},
		{ID: "%2", PID: 102},
	}, nil)

	views := []sessionView{
		{SessionID: "sess-lead", WindowID: "@5", Status: StatusActive},
	}
	enrichViewsWithTeamInfo(views, tmux)

	if len(views[0].Teammates) != 2 {
		t.Fatalf("Teammates count: got %d, want 2", len(views[0].Teammates))
	}
	// First teammate gets a pane ID, second doesn't
	if views[0].Teammates[0].PaneID != "%2" {
		t.Errorf("Teammates[0].PaneID: got %q, want %%2", views[0].Teammates[0].PaneID)
	}
	if views[0].Teammates[1].PaneID != "" {
		t.Errorf("Teammates[1].PaneID should be empty, got %q", views[0].Teammates[1].PaneID)
	}
}

func TestViewToProjectState_WithTeam(t *testing.T) {
	v := sessionView{
		SessionID:   "sess-lead",
		CWD:         "/ws/project",
		WindowName:  "project",
		WindowID:    "@5",
		Status:      StatusActive,
		TeamName:    "my-team",
		TeamRole:    "lead",
		Teammates: []teammateView{
			{Name: "arch", Status: "active", PaneID: "%2"},
			{Name: "test", Status: "idle", PaneID: "%3"},
		},
	}

	ps := viewToProjectState(v, "", "project")
	if ps.TeamName != "my-team" {
		t.Errorf("TeamName: got %q", ps.TeamName)
	}
	if ps.TeamRole != "lead" {
		t.Errorf("TeamRole: got %q", ps.TeamRole)
	}
	if len(ps.Teammates) != 2 {
		t.Fatalf("Teammates: got %d", len(ps.Teammates))
	}
	if ps.Teammates[0].Name != "arch" || ps.Teammates[0].PaneID != "%2" {
		t.Errorf("Teammates[0]: %+v", ps.Teammates[0])
	}
}

func TestViewToProjectState_NoTeam(t *testing.T) {
	v := sessionView{
		SessionID:  "s1",
		CWD:        "/ws/project",
		WindowName: "project",
		Status:     StatusActive,
	}
	ps := viewToProjectState(v, "", "project")
	if ps.TeamName != "" || ps.TeamRole != "" || ps.Teammates != nil {
		t.Errorf("non-team session should have empty team fields: %+v", ps)
	}
}
