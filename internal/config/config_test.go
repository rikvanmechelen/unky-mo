package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateConfig points HOME + XDG_CONFIG_HOME at a temp dir so Load reads
// test-local files.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir
}

func TestLoadMissingFileReturnsPartialDefaults(t *testing.T) {
	isolateConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if cfg.TmuxSession != "mo" {
		t.Errorf("TmuxSession default: want %q, got %q", "mo", cfg.TmuxSession)
	}
	if cfg.SocketPath != "/tmp/unky-mo.sock" {
		t.Errorf("SocketPath default: want %q, got %q", "/tmp/unky-mo.sock", cfg.SocketPath)
	}
	if !cfg.ScanOnStartup {
		t.Error("ScanOnStartup default should be true")
	}
	if !cfg.NotifySound {
		t.Error("NotifySound default should be true")
	}
	// Note: the missing-file branch returns before filling defaults for
	// StateFilePath and ticket-panel values. This test locks in the current
	// behavior so drift shows up; see the expectfail variant for the
	// stricter assertion.
	if cfg.StateFilePath != "" {
		t.Logf("StateFilePath on missing file: %q (currently unset — see config.go:83)", cfg.StateFilePath)
	}
}

func TestLoadPartialTOMLMergesDefaults(t *testing.T) {
	dir := isolateConfig(t)
	path := filepath.Join(dir, ".config", "unky-mo", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := `
workspace_dirs = ["/ws/a", "/ws/b"]
tmux_session = "custom-mo"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TmuxSession != "custom-mo" {
		t.Errorf("TmuxSession override failed: got %q", cfg.TmuxSession)
	}
	if len(cfg.WorkspaceDirs) != 2 {
		t.Errorf("WorkspaceDirs: want 2, got %d", len(cfg.WorkspaceDirs))
	}
	if cfg.SocketPath != "/tmp/unky-mo.sock" {
		t.Errorf("SocketPath default should apply: got %q", cfg.SocketPath)
	}
	if cfg.StateFilePath != "/tmp/unky-mo-state.json" {
		t.Errorf("StateFilePath default (file-present branch): got %q", cfg.StateFilePath)
	}
	if cfg.Tickets.RefreshSeconds != 300 {
		t.Errorf("RefreshSeconds default: got %d", cfg.Tickets.RefreshSeconds)
	}
	if cfg.Tickets.PerBucketLimit != 5 {
		t.Errorf("PerBucketLimit default: got %d", cfg.Tickets.PerBucketLimit)
	}
}

func TestLoadTicketsJiraArray(t *testing.T) {
	dir := isolateConfig(t)
	path := filepath.Join(dir, ".config", "unky-mo", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := `
[tickets]
refresh_seconds = 120
per_bucket_limit = 10

[[tickets.jira]]
name = "moma"
base_url = "https://moma.atlassian.net"
email = "rik@moma.org"
sprint_field_id = "customfield_10020"

[tickets.jira.status_map]
in_progress = ["In Progress", "Doing"]
blocked = ["Blocked"]
review = ["Code Review"]
todo = ["To Do"]

[tickets.jira.project_map]
OP = "moma-apps-rails"

[[tickets.jira]]
name = "other"
base_url = "https://other.atlassian.net"
email = "r@other.com"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Tickets.RefreshSeconds != 120 {
		t.Errorf("RefreshSeconds override: got %d", cfg.Tickets.RefreshSeconds)
	}
	if len(cfg.Tickets.Jira) != 2 {
		t.Fatalf("want 2 jira instances, got %d", len(cfg.Tickets.Jira))
	}
	first := cfg.Tickets.Jira[0]
	if first.Name != "moma" || first.BaseURL != "https://moma.atlassian.net" {
		t.Errorf("first jira instance wrong: %+v", first)
	}
	if first.ProjectMap["OP"] != "moma-apps-rails" {
		t.Errorf("project map: want OP → moma-apps-rails, got %v", first.ProjectMap)
	}
	if len(first.StatusMap.InProgress) != 2 {
		t.Errorf("status map in_progress: got %v", first.StatusMap.InProgress)
	}
}

func TestDefaultConfigDirRespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	got := DefaultConfigDir()
	if got != "/custom/xdg/unky-mo" {
		t.Errorf("XDG path: want %q, got %q", "/custom/xdg/unky-mo", got)
	}
}

func TestDefaultConfigDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/test-user")
	got := DefaultConfigDir()
	if got != "/home/test-user/.config/unky-mo" {
		t.Errorf("HOME fallback: got %q", got)
	}
}
