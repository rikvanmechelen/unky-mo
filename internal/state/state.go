package state

import (
	"encoding/json"
	"os"
	"time"
)

// ProjectState represents a project's status in the shared state file.
// Each entry corresponds to a single tmux window. Multiple entries may
// share Name/Parent when a project has concurrent sibling sessions — they
// are distinguished by WindowName and SessionID.
type ProjectState struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	WindowName string `json:"window_name"`
	WindowID   string `json:"window_id,omitempty"` // stable tmux window id (e.g. "@5"); survives renames
	Status     string `json:"status"`               // "none", "active", "idle", "permission", "external"
	Parent     string `json:"parent,omitempty"`     // non-empty for worktree entries
	Section    string `json:"section,omitempty"`    // "projects" (default) or "external" — for stray-session grouping
	Branch     string `json:"branch,omitempty"`     // git branch (populated for git-backed strays)
	Dirty      int    `json:"dirty,omitempty"`      // dirty file count (populated for git-backed strays)
	SessionID  string `json:"session_id,omitempty"`  // Claude session ID running in this window (empty if none)
	InstanceID string `json:"instance_id,omitempty"` // mo-generated instance ID (from @mo_instance_id window option)
	AgentKey   string `json:"agent_key,omitempty"`   // coding agent mnemonic (from @mo_agent window option); empty = default
	Index      int    `json:"index,omitempty"`       // 0 = primary, 2+ = sibling ordinal; for stable sort

	// Team fields — populated when session is part of a Claude Code agent team.
	TeamName  string          `json:"team_name,omitempty"`  // team name from ~/.claude/teams/{name}/config.json
	TeamRole  string          `json:"team_role,omitempty"`  // "lead" or "teammate"
	Teammates []TeammateState `json:"teammates,omitempty"`  // only populated on the lead's row
}

// TeammateState represents a teammate pane within a team lead's window.
type TeammateState struct {
	Name   string `json:"name"`              // role name ("architect", "tester")
	Status string `json:"status"`            // "active", "idle"
	PaneID string `json:"pane_id,omitempty"` // tmux pane ID for focus switching
}

// StateFile is the shared state written by the main TUI and read by sidebar instances.
type StateFile struct {
	TmuxSession string         `json:"tmux_session"`
	Projects    []ProjectState `json:"projects"`
	UpdatedAt   time.Time      `json:"updated_at"`

	Usage *UsageState `json:"usage,omitempty"`
}

// UsageState is the Claude Code rate-limit-window view the main TUI writes
// into the shared state file. Sidebars consume it at their normal 1s tick
// (no JSONL parsing / API calls from the sidebar itself).
type UsageState struct {
	FiveHourPct      int       `json:"five_hour_pct"`
	FiveHourResetsAt time.Time `json:"five_hour_resets_at"`

	SevenDayPct      int       `json:"seven_day_pct"`
	SevenDayResetsAt time.Time `json:"seven_day_resets_at"`

	FetchedAt time.Time `json:"fetched_at"`
	Stale     bool      `json:"stale,omitempty"`
	AuthError bool      `json:"auth_error,omitempty"`
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
