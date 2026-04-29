package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/config"
)

// isolateAgentsConfig points HOME + XDG so Load/Save use a temp dir.
// Returns the dir and writes a config with the given TOML body.
func isolateAgentsConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	if body != "" {
		cfgDir := filepath.Join(dir, ".config", "unky-mo")
		if err := os.MkdirAll(cfgDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestAgentsListShowsDefaults(t *testing.T) {
	isolateAgentsConfig(t, "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents) != 1 {
		t.Fatalf("want 1 default agent, got %d", len(cfg.Agents))
	}
	if cfg.Agents[0].Name != "Claude" {
		t.Errorf("default agent: %+v", cfg.Agents[0])
	}
}

func TestAgentsAddAndPersist(t *testing.T) {
	isolateAgentsConfig(t, `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
default = true
`)
	// Load, add, save.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddAgent(config.AgentConfig{Name: "Gemini CLI", Key: "g", Cmd: "gemini"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	// Reload and verify.
	cfg2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Agents) != 2 {
		t.Fatalf("want 2 agents after reload, got %d", len(cfg2.Agents))
	}
	g := cfg2.AgentByKey("g")
	if g == nil || g.Name != "Gemini CLI" || g.Cmd != "gemini" {
		t.Errorf("reloaded gemini agent: %+v", g)
	}
}

func TestAgentsRemoveAndPersist(t *testing.T) {
	isolateAgentsConfig(t, `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
default = true

[[agent]]
name = "Gemini CLI"
key = "g"
cmd = "gemini"
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RemoveAgent("g"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	cfg2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg2.Agents) != 1 {
		t.Fatalf("want 1 agent after reload, got %d", len(cfg2.Agents))
	}
	if cfg2.Agents[0].Key != "c" {
		t.Errorf("remaining agent: %+v", cfg2.Agents[0])
	}
}

func TestAgentsDefaultAndPersist(t *testing.T) {
	isolateAgentsConfig(t, `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
default = true

[[agent]]
name = "Gemini CLI"
key = "g"
cmd = "gemini"
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetDefaultAgent("g"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	cfg2, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	def := cfg2.DefaultAgent()
	if def == nil || def.Key != "g" {
		t.Errorf("want default=g after reload, got %+v", def)
	}
}

func TestAgentsAddRejectsDuplicateKey(t *testing.T) {
	isolateAgentsConfig(t, `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
`)
	cfg, _ := config.Load()
	err := cfg.AddAgent(config.AgentConfig{Name: "Other", Key: "c", Cmd: "other"})
	if err == nil {
		t.Error("want error for duplicate key")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAgentsRemoveRefusesLast(t *testing.T) {
	isolateAgentsConfig(t, `
[[agent]]
name = "Claude"
key = "c"
cmd = "claude"
`)
	cfg, _ := config.Load()
	err := cfg.RemoveAgent("c")
	if err == nil {
		t.Error("want error when removing the last agent")
	}
	if !strings.Contains(err.Error(), "last agent") {
		t.Errorf("unexpected error: %v", err)
	}
}
