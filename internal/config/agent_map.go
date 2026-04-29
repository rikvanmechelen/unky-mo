package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// AgentChoice records a per-project/branch agent preference.
type AgentChoice struct {
	Project  string `toml:"project"`
	Branch   string `toml:"branch"`
	AgentKey string `toml:"agent_key"`
}

type agentChoiceFile struct {
	Choices []AgentChoice `toml:"choice"`
}

func agentChoicesPath() string {
	return filepath.Join(DefaultConfigDir(), "agent-choices.toml")
}

// LoadAgentChoices reads the companion file. Returns nil map (not error) if
// the file doesn't exist.
func LoadAgentChoices() (map[string]string, error) {
	path := agentChoicesPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	var f agentChoiceFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, err
	}
	m := make(map[string]string, len(f.Choices))
	for _, c := range f.Choices {
		m[c.Project+":"+c.Branch] = c.AgentKey
	}
	return m, nil
}

// SaveAgentChoice persists a project+branch → agent preference.
func SaveAgentChoice(project, branch, agentKey string) error {
	choices, _ := LoadAgentChoices()
	if choices == nil {
		choices = map[string]string{}
	}
	choices[project+":"+branch] = agentKey

	// Rebuild the file.
	var f agentChoiceFile
	for k, v := range choices {
		proj, br := splitChoiceKey(k)
		f.Choices = append(f.Choices, AgentChoice{Project: proj, Branch: br, AgentKey: v})
	}

	dir := DefaultConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	file, err := os.Create(agentChoicesPath())
	if err != nil {
		return err
	}
	defer file.Close()
	return toml.NewEncoder(file).Encode(f)
}

// LookupAgentChoice returns the saved agent key for a project+branch, or "".
func LookupAgentChoice(choices map[string]string, project, branch string) string {
	if choices == nil {
		return ""
	}
	return choices[project+":"+branch]
}

func splitChoiceKey(key string) (string, string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
