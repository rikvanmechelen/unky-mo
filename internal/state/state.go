package state

import (
	"encoding/json"
	"os"
	"time"
)

// ProjectState represents a project's status in the shared state file.
type ProjectState struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	WindowName string `json:"window_name"`
	Status     string `json:"status"`           // "none", "active", "idle", "permission"
	Parent     string `json:"parent,omitempty"` // non-empty for worktree entries
}

// StateFile is the shared state written by the main TUI and read by sidebar instances.
type StateFile struct {
	TmuxSession string         `json:"tmux_session"`
	Projects    []ProjectState `json:"projects"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Write atomically writes the state file (write to temp, then rename).
func Write(path string, s *StateFile) error {
	s.UpdatedAt = time.Now()
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read reads and parses the state file.
func Read(path string) (*StateFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s StateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Remove deletes the state file.
func Remove(path string) {
	os.Remove(path)
	os.Remove(path + ".tmp")
}
