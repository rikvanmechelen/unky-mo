package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// TeamConfig represents ~/.claude/teams/{team-name}/config.json
type TeamConfig struct {
	Name        string       `json:"name"`
	Members     []TeamMember `json:"members"`
	LeadAgentID string       `json:"leadAgentId"`
}

// TeamMember represents a single member of an agent team.
type TeamMember struct {
	Name      string `json:"name"`
	AgentID   string `json:"agentId"`
	AgentType string `json:"agentType"` // "lead" or "teammate"
	SessionID string `json:"sessionId,omitempty"`
	PaneID    string `json:"paneId,omitempty"`
}

// LeadMember returns the team member with agentType "lead", or nil.
func (tc TeamConfig) LeadMember() *TeamMember {
	for i := range tc.Members {
		if tc.Members[i].AgentType == "lead" {
			return &tc.Members[i]
		}
	}
	return nil
}

// Teammates returns all members that are not the lead.
func (tc TeamConfig) Teammates() []TeamMember {
	var out []TeamMember
	for _, m := range tc.Members {
		if m.AgentType != "lead" {
			out = append(out, m)
		}
	}
	return out
}

// TeamsDir returns the path to Claude's teams directory.
func TeamsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "teams")
}

// ReadTeamConfigs reads all team configurations from ~/.claude/teams/*/config.json.
// Malformed configs are skipped. Returns nil, nil when no teams directory exists.
func ReadTeamConfigs() ([]TeamConfig, error) {
	dir := TeamsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var configs []TeamConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		configPath := filepath.Join(dir, entry.Name(), "config.json")
		data, err := os.ReadFile(configPath)
		if err != nil {
			continue // missing config.json — skip
		}
		var tc TeamConfig
		if err := json.Unmarshal(data, &tc); err != nil {
			continue // malformed JSON — skip
		}
		configs = append(configs, tc)
	}
	return configs, nil
}
