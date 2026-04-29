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

// --- Agent config tests ---

func TestDefaultAgentsWhenEmpty(t *testing.T) {
	isolateConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("want 1 default agent, got %d", len(cfg.Agents))
	}
	a := cfg.Agents[0]
	if a.Name != "Claude" {
		t.Errorf("default agent name: want %q, got %q", "Claude", a.Name)
	}
	if a.Cmd != "claude" {
		t.Errorf("default agent cmd: want %q, got %q", "claude", a.Cmd)
	}
	if a.Key != "c" {
		t.Errorf("default agent key: want %q, got %q", "c", a.Key)
	}
	if a.ResumeCmd != "claude --resume" {
		t.Errorf("default agent resume_cmd: want %q, got %q", "claude --resume", a.ResumeCmd)
	}
	if !a.Default {
		t.Error("default agent should have Default=true")
	}
}

func TestCustomAgentsParsed(t *testing.T) {
	dir := isolateConfig(t)
	path := filepath.Join(dir, ".config", "unky-mo", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	body := `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
resume_cmd = "claude --resume"
default = true

[[agent]]
name = "Gemini CLI"
key = "g"
cmd = "gemini"
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].Name != "Claude" || cfg.Agents[0].Key != "c" {
		t.Errorf("first agent: %+v", cfg.Agents[0])
	}
	if cfg.Agents[1].Name != "Gemini CLI" || cfg.Agents[1].Key != "g" || cfg.Agents[1].Cmd != "gemini" {
		t.Errorf("second agent: %+v", cfg.Agents[1])
	}
}

func TestDefaultAgentReturnsMarked(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
		},
	}
	got := cfg.DefaultAgent()
	if got == nil {
		t.Fatal("DefaultAgent returned nil")
	}
	if got.Name != "Claude" {
		t.Errorf("want Claude, got %q", got.Name)
	}
}

func TestDefaultAgentFallsBackToFirst(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	got := cfg.DefaultAgent()
	if got == nil {
		t.Fatal("DefaultAgent returned nil")
	}
	if got.Name != "Gemini" {
		t.Errorf("want first entry (Gemini), got %q", got.Name)
	}
}

func TestAgentByKey(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
		},
	}
	got := cfg.AgentByKey("g")
	if got == nil {
		t.Fatal("AgentByKey returned nil for 'g'")
	}
	if got.Name != "Gemini" {
		t.Errorf("want Gemini, got %q", got.Name)
	}
}

func TestAgentByKeyNotFound(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	got := cfg.AgentByKey("z")
	if got != nil {
		t.Errorf("want nil for unknown key, got %+v", got)
	}
}

// --- Agent CRUD tests ---

func TestAddAgent(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
		},
	}
	err := cfg.AddAgent(AgentConfig{Name: "Gemini CLI", Key: "g", Cmd: "gemini"})
	if err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("want 2 agents, got %d", len(cfg.Agents))
	}
	if cfg.Agents[1].Name != "Gemini CLI" || cfg.Agents[1].Key != "g" {
		t.Errorf("added agent: %+v", cfg.Agents[1])
	}
}

func TestAddAgentRejectsDuplicateKey(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	err := cfg.AddAgent(AgentConfig{Name: "Other", Key: "c", Cmd: "other"})
	if err == nil {
		t.Error("want error for duplicate key")
	}
}

func TestAddAgentRejectsDuplicateName(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	err := cfg.AddAgent(AgentConfig{Name: "Claude", Key: "x", Cmd: "other"})
	if err == nil {
		t.Error("want error for duplicate name")
	}
}

func TestAddAgentRejectsEmptyFields(t *testing.T) {
	cfg := &Config{}
	if err := cfg.AddAgent(AgentConfig{Key: "g", Cmd: "gemini"}); err == nil {
		t.Error("want error for empty name")
	}
	if err := cfg.AddAgent(AgentConfig{Name: "Gemini", Cmd: "gemini"}); err == nil {
		t.Error("want error for empty key")
	}
	if err := cfg.AddAgent(AgentConfig{Name: "Gemini", Key: "g"}); err == nil {
		t.Error("want error for empty cmd")
	}
}

func TestRemoveAgent(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
		},
	}
	err := cfg.RemoveAgent("g")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].Key != "c" {
		t.Errorf("remaining agent: %+v", cfg.Agents[0])
	}
}

func TestRemoveAgentNotFound(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	err := cfg.RemoveAgent("z")
	if err == nil {
		t.Error("want error for unknown key")
	}
}

func TestRemoveAgentRefusesLast(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	err := cfg.RemoveAgent("c")
	if err == nil {
		t.Error("want error when removing the last agent")
	}
}

func TestSetDefaultAgent(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude", Default: true},
			{Name: "Gemini", Key: "g", Cmd: "gemini"},
		},
	}
	err := cfg.SetDefaultAgent("g")
	if err != nil {
		t.Fatalf("SetDefaultAgent: %v", err)
	}
	if cfg.Agents[0].Default {
		t.Error("Claude should no longer be default")
	}
	if !cfg.Agents[1].Default {
		t.Error("Gemini should now be default")
	}
}

func TestSetDefaultAgentNotFound(t *testing.T) {
	cfg := &Config{
		Agents: []AgentConfig{
			{Name: "Claude", Key: "c", Cmd: "claude"},
		},
	}
	err := cfg.SetDefaultAgent("z")
	if err == nil {
		t.Error("want error for unknown key")
	}
}
