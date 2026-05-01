package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTeamsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := TeamsDir()
	want := filepath.Join(home, ".claude", "teams")
	if got != want {
		t.Errorf("TeamsDir: got %q, want %q", got, want)
	}
}

func TestReadTeamConfigs_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teamDir := filepath.Join(home, ".claude", "teams", "my-team")
	if err := os.MkdirAll(teamDir, 0755); err != nil {
		t.Fatal(err)
	}
	config := `{
		"name": "my-team",
		"leadAgentId": "lead-abc",
		"members": [
			{"name": "architect", "agentId": "agent-1", "agentType": "lead"},
			{"name": "tester", "agentId": "agent-2", "agentType": "teammate"}
		]
	}`
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("want 1 config, got %d", len(configs))
	}
	c := configs[0]
	if c.Name != "my-team" {
		t.Errorf("Name: got %q, want %q", c.Name, "my-team")
	}
	if c.LeadAgentID != "lead-abc" {
		t.Errorf("LeadAgentID: got %q, want %q", c.LeadAgentID, "lead-abc")
	}
	if len(c.Members) != 2 {
		t.Fatalf("Members: want 2, got %d", len(c.Members))
	}
	if c.Members[0].Name != "architect" || c.Members[0].AgentType != "lead" {
		t.Errorf("Members[0]: got %+v", c.Members[0])
	}
	if c.Members[1].Name != "tester" || c.Members[1].AgentType != "teammate" {
		t.Errorf("Members[1]: got %+v", c.Members[1])
	}
}

func TestReadTeamConfigs_EmptyDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// No teams dir at all
	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if configs != nil {
		t.Errorf("want nil, got %v", configs)
	}
}

func TestReadTeamConfigs_EmptyTeamsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Teams dir exists but is empty
	if err := os.MkdirAll(filepath.Join(home, ".claude", "teams"), 0755); err != nil {
		t.Fatal(err)
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if configs != nil {
		t.Errorf("want nil, got %v", configs)
	}
}

func TestReadTeamConfigs_MalformedJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teamDir := filepath.Join(home, ".claude", "teams", "bad-team")
	if err := os.MkdirAll(teamDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), []byte(`{broken json`), 0644); err != nil {
		t.Fatal(err)
	}

	// Should skip malformed configs gracefully
	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs should not error on malformed JSON: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("want 0 configs (malformed skipped), got %d", len(configs))
	}
}

func TestReadTeamConfigs_MultipleTeams(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, name := range []string{"team-alpha", "team-beta"} {
		dir := filepath.Join(home, ".claude", "teams", name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		config := `{"name": "` + name + `", "leadAgentId": "lead-` + name + `", "members": []}`
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0644); err != nil {
			t.Fatal(err)
		}
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("want 2 configs, got %d", len(configs))
	}

	names := map[string]bool{}
	for _, c := range configs {
		names[c.Name] = true
	}
	if !names["team-alpha"] || !names["team-beta"] {
		t.Errorf("missing expected team names: %v", names)
	}
}

func TestReadTeamConfigs_SkipsNonDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teamsDir := filepath.Join(home, ".claude", "teams")
	if err := os.MkdirAll(teamsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A stray file in the teams directory should be skipped
	if err := os.WriteFile(filepath.Join(teamsDir, "stray.txt"), []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("want 0 configs, got %d", len(configs))
	}
}

func TestReadTeamConfigs_MissingConfigJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Team directory exists but has no config.json
	teamDir := filepath.Join(home, ".claude", "teams", "empty-team")
	if err := os.MkdirAll(teamDir, 0755); err != nil {
		t.Fatal(err)
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if len(configs) != 0 {
		t.Errorf("want 0 configs, got %d", len(configs))
	}
}

func TestReadTeamConfigs_UnknownFieldsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	teamDir := filepath.Join(home, ".claude", "teams", "future-team")
	if err := os.MkdirAll(teamDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Config with extra fields we don't know about
	config := `{
		"name": "future-team",
		"leadAgentId": "lead-x",
		"members": [{"name": "worker", "agentId": "a1", "agentType": "teammate", "futureField": 42}],
		"unknownTopLevel": true
	}`
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := ReadTeamConfigs()
	if err != nil {
		t.Fatalf("ReadTeamConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("want 1 config, got %d", len(configs))
	}
	if configs[0].Members[0].Name != "worker" {
		t.Errorf("expected member name 'worker', got %q", configs[0].Members[0].Name)
	}
}

func TestTeamLeadMember(t *testing.T) {
	tc := TeamConfig{
		Name:        "test-team",
		LeadAgentID: "lead-1",
		Members: []TeamMember{
			{Name: "lead", AgentID: "lead-1", AgentType: "lead"},
			{Name: "worker", AgentID: "worker-1", AgentType: "teammate"},
		},
	}

	lead := tc.LeadMember()
	if lead == nil {
		t.Fatal("LeadMember returned nil")
	}
	if lead.Name != "lead" {
		t.Errorf("LeadMember name: got %q, want %q", lead.Name, "lead")
	}
}

func TestTeamLeadMember_NoLead(t *testing.T) {
	tc := TeamConfig{
		Name:    "headless-team",
		Members: []TeamMember{{Name: "solo", AgentType: "teammate"}},
	}

	if lead := tc.LeadMember(); lead != nil {
		t.Errorf("LeadMember should be nil for headless team, got %+v", lead)
	}
}

func TestTeamTeammates(t *testing.T) {
	tc := TeamConfig{
		Members: []TeamMember{
			{Name: "lead", AgentType: "lead"},
			{Name: "arch", AgentType: "teammate"},
			{Name: "test", AgentType: "teammate"},
		},
	}

	teammates := tc.Teammates()
	if len(teammates) != 2 {
		t.Fatalf("Teammates: want 2, got %d", len(teammates))
	}
	if teammates[0].Name != "arch" || teammates[1].Name != "test" {
		t.Errorf("unexpected teammates: %+v", teammates)
	}
}
