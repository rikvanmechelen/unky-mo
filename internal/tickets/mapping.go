package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// projectMapFileName is the companion file next to config.toml that stores
// picker-saved project mappings. Kept separate so we never have to mutate
// the user's hand-authored config.toml.
const projectMapFileName = "jira-project-map.toml"

// ProjectMapEntry is one row in the companion file.
type ProjectMapEntry struct {
	Provider  string `toml:"provider"`   // e.g. "jira"; future-proof for Linear, GitHub Projects
	JiraKey   string `toml:"jira_key"`
	MoProject string `toml:"mo_project"`
}

// projectMapFile is the on-disk shape of the companion file.
type projectMapFile struct {
	Entry []ProjectMapEntry `toml:"entry"`
}

// DefaultProjectMapPath returns the companion-file path, mirroring the
// other per-user files under ~/.config/unky-mo/.
func DefaultProjectMapPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "unky-mo", projectMapFileName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo", projectMapFileName)
}

// LoadCompanionProjectMap reads the companion file. Missing file is not an
// error (returns an empty map); malformed file is an error so users notice.
// Returns a map keyed by provider name → jira project key → mo project name.
func LoadCompanionProjectMap() (map[string]map[string]string, error) {
	path := DefaultProjectMapPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var file projectMapFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := map[string]map[string]string{}
	for _, e := range file.Entry {
		if e.Provider == "" || e.JiraKey == "" || e.MoProject == "" {
			continue
		}
		if out[e.Provider] == nil {
			out[e.Provider] = map[string]string{}
		}
		out[e.Provider][e.JiraKey] = e.MoProject
	}
	return out, nil
}

// SaveProjectMapEntry adds or updates a single mapping, preserving all other
// entries. Writes the whole companion file atomically (temp + rename). Fails
// if the Mo-project name is empty (that's how we detect "unset" later).
func SaveProjectMapEntry(provider, jiraKey, moProject string) error {
	if provider == "" || jiraKey == "" || moProject == "" {
		return fmt.Errorf("saveProjectMapEntry: all fields required")
	}

	path := DefaultProjectMapPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	var file projectMapFile
	if data, err := os.ReadFile(path); err == nil {
		_ = toml.Unmarshal(data, &file)
	}

	updated := false
	for i, e := range file.Entry {
		if e.Provider == provider && e.JiraKey == jiraKey {
			file.Entry[i].MoProject = moProject
			updated = true
			break
		}
	}
	if !updated {
		file.Entry = append(file.Entry, ProjectMapEntry{
			Provider:  provider,
			JiraKey:   jiraKey,
			MoProject: moProject,
		})
	}

	sort.SliceStable(file.Entry, func(i, j int) bool {
		if file.Entry[i].Provider != file.Entry[j].Provider {
			return file.Entry[i].Provider < file.Entry[j].Provider
		}
		return file.Entry[i].JiraKey < file.Entry[j].JiraKey
	})

	tmp, err := os.CreateTemp(filepath.Dir(path), projectMapFileName+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString("# Auto-managed by mo. Hand-editing is fine.\n# Entries here supplement [tickets.jira.project_map] in config.toml.\n\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := toml.NewEncoder(tmp).Encode(file); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// MergeProjectMaps returns the effective mapping for a provider: config map
// wins over companion-file entries on key conflict. Both inputs may be nil.
func MergeProjectMaps(configMap map[string]string, companion map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range companion {
		if v != "" {
			out[k] = v
		}
	}
	for k, v := range configMap {
		if v != "" {
			out[k] = v
		}
	}
	return out
}
