package ops

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

func TestListTeams_HappyPath(t *testing.T) {
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
	tmux.EXPECT().ListWindowPanes("@5").Return([]ttmux.PaneInfo{
		{ID: "%0", PID: 100},
		{ID: "%1", PID: 101},
		{ID: "%2", PID: 102},
		{ID: "%3", PID: 103},
	}, nil)

	sessionWindows := map[string]string{"sess-lead": "@5"}
	teams, err := ListTeams(tmux, sessionWindows)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("want 1 team, got %d", len(teams))
	}

	team := teams[0]
	if team.Name != "review-team" {
		t.Errorf("Name: got %q", team.Name)
	}
	if team.LeadSession != "sess-lead" {
		t.Errorf("LeadSession: got %q", team.LeadSession)
	}
	if team.LeadWindow != "@5" {
		t.Errorf("LeadWindow: got %q", team.LeadWindow)
	}
	if len(team.Teammates) != 2 {
		t.Fatalf("Teammates: got %d", len(team.Teammates))
	}
	if team.Teammates[0].Name != "architect" || team.Teammates[0].PaneID != "%2" {
		t.Errorf("Teammates[0]: %+v", team.Teammates[0])
	}
	if team.Teammates[1].Name != "tester" || team.Teammates[1].PaneID != "%3" {
		t.Errorf("Teammates[1]: %+v", team.Teammates[1])
	}
}

func TestListTeams_NoTeams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teams, err := ListTeams(nil, nil)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if teams != nil {
		t.Errorf("want nil, got %v", teams)
	}
}

func TestListTeams_LeadNotInWindows(t *testing.T) {
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

	// Empty sessionWindows — lead not resolved
	teams, err := ListTeams(nil, map[string]string{})
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("want 1 team (config-only), got %d", len(teams))
	}
	if teams[0].LeadWindow != "" {
		t.Errorf("LeadWindow should be empty, got %q", teams[0].LeadWindow)
	}
	if len(teams[0].Teammates) != 1 || teams[0].Teammates[0].PaneID != "" {
		t.Errorf("Teammates should have no pane IDs: %+v", teams[0].Teammates)
	}
}

func TestListTeams_NilTmuxClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "test-team", `{
		"name": "test-team",
		"leadAgentId": "lead-1",
		"members": [
			{"name": "lead", "agentId": "lead-1", "agentType": "lead", "sessionId": "sess-lead"},
			{"name": "worker", "agentId": "tm-1", "agentType": "teammate"}
		]
	}`)

	sessionWindows := map[string]string{"sess-lead": "@5"}
	teams, err := ListTeams(nil, sessionWindows)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("want 1 team, got %d", len(teams))
	}
	// Should populate teammates from config without pane IDs
	if teams[0].Teammates[0].PaneID != "" {
		t.Errorf("PaneID should be empty without tmux client, got %q", teams[0].Teammates[0].PaneID)
	}
}

func TestListTeams_MultipleTeams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeTeamConfig(t, home, "team-a", `{
		"name": "team-a",
		"leadAgentId": "lead-a",
		"members": [
			{"name": "lead", "agentId": "lead-a", "agentType": "lead", "sessionId": "sess-a"},
			{"name": "worker-a", "agentId": "tm-a", "agentType": "teammate"}
		]
	}`)
	writeTeamConfig(t, home, "team-b", `{
		"name": "team-b",
		"leadAgentId": "lead-b",
		"members": [
			{"name": "lead", "agentId": "lead-b", "agentType": "lead", "sessionId": "sess-b"},
			{"name": "worker-b", "agentId": "tm-b", "agentType": "teammate"}
		]
	}`)

	teams, err := ListTeams(nil, nil)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams) != 2 {
		t.Fatalf("want 2 teams, got %d", len(teams))
	}
	names := map[string]bool{}
	for _, ts := range teams {
		names[ts.Name] = true
	}
	if !names["team-a"] || !names["team-b"] {
		t.Errorf("missing team: %v", names)
	}
}

func TestListTeams_FewerPanesThanTeammates(t *testing.T) {
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
	// Only 3 panes (lead + sidebar + 1 teammate)
	tmux.EXPECT().ListWindowPanes("@5").Return([]ttmux.PaneInfo{
		{ID: "%0", PID: 100},
		{ID: "%1", PID: 101},
		{ID: "%2", PID: 102},
	}, nil)

	sessionWindows := map[string]string{"sess-lead": "@5"}
	teams, err := ListTeams(tmux, sessionWindows)
	if err != nil {
		t.Fatalf("ListTeams: %v", err)
	}
	if len(teams[0].Teammates) != 2 {
		t.Fatalf("Teammates: got %d", len(teams[0].Teammates))
	}
	if teams[0].Teammates[0].PaneID != "%2" {
		t.Errorf("first teammate should have pane, got %q", teams[0].Teammates[0].PaneID)
	}
	if teams[0].Teammates[1].PaneID != "" {
		t.Errorf("second teammate should have no pane, got %q", teams[0].Teammates[1].PaneID)
	}
}
