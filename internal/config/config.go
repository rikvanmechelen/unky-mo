package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/rvanmech/unky-mo/internal/project"
)

const defaultTmuxSession = "mo"
const defaultSocketPath = "/tmp/unky-mo.sock"
const defaultStateFilePath = "/tmp/unky-mo-state.json"
const defaultTicketsRefreshSeconds = 300
const defaultTicketsPerBucketLimit = 5

type Config struct {
	WorkspaceDirs []string          `toml:"workspace_dirs"`
	TmuxSession   string            `toml:"tmux_session"`
	SocketPath    string            `toml:"socket_path"`
	StateFilePath string            `toml:"state_file_path"`
	ScanOnStartup bool              `toml:"scan_on_startup"`
	NotifySound   bool              `toml:"notify_sound"`
	Projects      []project.Project `toml:"project"`
	Tickets       TicketsConfig     `toml:"tickets"`
	Agents        []AgentConfig     `toml:"agent"`
}

// AgentConfig describes a coding agent that can be launched in a tmux pane.
type AgentConfig struct {
	Name      string `toml:"name"`
	Key       string `toml:"key"`        // single mnemonic char for the picker menu
	Cmd       string `toml:"cmd"`        // shell command to exec (e.g. "claude", "gemini")
	ResumeCmd string `toml:"resume_cmd"` // optional resume command prefix (e.g. "claude --resume")
	Default   bool   `toml:"default"`    // at most one; used by bare enter
}

// DefaultAgents returns the built-in agent list used when no [[agent]] blocks
// are configured. Currently just Claude.
func DefaultAgents() []AgentConfig {
	return []AgentConfig{
		{Name: "Claude", Key: "c", Cmd: "claude", ResumeCmd: "claude --resume", Default: true},
	}
}

// DefaultAgent returns the agent marked Default, or the first agent if none
// is marked. Returns nil only when Agents is empty.
func (c *Config) DefaultAgent() *AgentConfig {
	for i := range c.Agents {
		if c.Agents[i].Default {
			return &c.Agents[i]
		}
	}
	if len(c.Agents) > 0 {
		return &c.Agents[0]
	}
	return nil
}

// AgentByKey looks up a configured agent by its mnemonic key. Returns nil if
// no agent has the given key.
func (c *Config) AgentByKey(key string) *AgentConfig {
	for i := range c.Agents {
		if c.Agents[i].Key == key {
			return &c.Agents[i]
		}
	}
	return nil
}

// AddAgent appends a new agent to the config. Returns an error if the key or
// name is already in use, or if required fields are empty.
func (c *Config) AddAgent(a AgentConfig) error {
	if a.Name == "" {
		return fmt.Errorf("agent name is required")
	}
	if a.Key == "" {
		return fmt.Errorf("agent key is required")
	}
	if a.Cmd == "" {
		return fmt.Errorf("agent cmd is required")
	}
	for _, existing := range c.Agents {
		if existing.Key == a.Key {
			return fmt.Errorf("agent key %q already in use by %q", a.Key, existing.Name)
		}
		if existing.Name == a.Name {
			return fmt.Errorf("agent name %q already exists", a.Name)
		}
	}
	c.Agents = append(c.Agents, a)
	return nil
}

// RemoveAgent removes the agent with the given key. Refuses to remove the
// last remaining agent.
func (c *Config) RemoveAgent(key string) error {
	if len(c.Agents) <= 1 {
		return fmt.Errorf("cannot remove the last agent")
	}
	for i, a := range c.Agents {
		if a.Key == key {
			c.Agents = append(c.Agents[:i], c.Agents[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("no agent with key %q", key)
}

// SetDefaultAgent marks the agent with the given key as default and clears
// the flag on all others.
func (c *Config) SetDefaultAgent(key string) error {
	found := false
	for i := range c.Agents {
		if c.Agents[i].Key == key {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no agent with key %q", key)
	}
	for i := range c.Agents {
		c.Agents[i].Default = c.Agents[i].Key == key
	}
	return nil
}

// TicketsConfig controls the dashboard tickets panel. Nested providers are
// arrays so multiple Jira (or future Linear / GitHub) instances can coexist.
// The panel is shown automatically whenever a token or instance is present;
// set Disabled = true to hide it even when credentials exist.
type TicketsConfig struct {
	Disabled        bool         `toml:"disabled"`
	RefreshSeconds  int          `toml:"refresh_seconds"`
	PerBucketLimit  int          `toml:"per_bucket_limit"`
	Jira            []JiraConfig `toml:"jira"`
}

// JiraConfig is one Atlassian Cloud instance.
type JiraConfig struct {
	Name          string             `toml:"name"`
	BaseURL       string             `toml:"base_url"`
	Email         string             `toml:"email"`
	SprintFieldID string             `toml:"sprint_field_id"`
	StatusMap     JiraStatusMap      `toml:"status_map"`
	// ProjectMap maps Jira project keys (e.g. "OP") to Mo project names
	// (e.g. "moma-apps-rails"). Used by the ticket-detail "start working"
	// flow. Picker-saved entries live in a companion file; this map is
	// hand-authored and takes precedence on conflict.
	ProjectMap map[string]string `toml:"project_map"`
}

// JiraStatusMap maps raw Jira statuses to Mo's four buckets. Empty slices
// fall back to tickets.DefaultStatusMap at load time.
type JiraStatusMap struct {
	InProgress []string `toml:"in_progress"`
	Blocked    []string `toml:"blocked"`
	Review     []string `toml:"review"`
	Todo       []string `toml:"todo"`
}

func DefaultConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "unky-mo")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "unky-mo")
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.toml")
}

func Load() (*Config, error) {
	cfg := &Config{
		TmuxSession:   defaultTmuxSession,
		SocketPath:    defaultSocketPath,
		ScanOnStartup: true,
		NotifySound:   true,
	}

	path := DefaultConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg.Agents = DefaultAgents()
		return cfg, nil
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}

	if cfg.TmuxSession == "" {
		cfg.TmuxSession = defaultTmuxSession
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultSocketPath
	}
	if cfg.StateFilePath == "" {
		cfg.StateFilePath = defaultStateFilePath
	}
	if cfg.Tickets.RefreshSeconds <= 0 {
		cfg.Tickets.RefreshSeconds = defaultTicketsRefreshSeconds
	}
	if cfg.Tickets.PerBucketLimit <= 0 {
		cfg.Tickets.PerBucketLimit = defaultTicketsPerBucketLimit
	}
	if len(cfg.Agents) == 0 {
		cfg.Agents = DefaultAgents()
	}

	return cfg, nil
}

func (c *Config) Save() error {
	dir := DefaultConfigDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.Create(DefaultConfigPath())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

// LoadProjects returns the merged list of auto-discovered and manually configured projects.
func (c *Config) LoadProjects() ([]project.Project, error) {
	if !c.ScanOnStartup || len(c.WorkspaceDirs) == 0 {
		return c.Projects, nil
	}
	discovered, err := project.ScanWorkspace(c.WorkspaceDirs)
	if err != nil {
		return c.Projects, err
	}
	return project.MergeWithManual(discovered, c.Projects), nil
}
