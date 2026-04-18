package config

import (
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
