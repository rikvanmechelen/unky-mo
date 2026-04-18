//go:build expectfail

// These tests encode CLAUDE.md claims we suspect the current code does not
// satisfy. Run them with `go test -tags expectfail ./...`. A failure here
// means either the code drifted or the docs did — someone needs to decide.

package config

import "testing"

// CLAUDE.md ("State File Schema" section) documents the state file path
// default as `/tmp/unky-mo-state.json`, with Load returning fully-defaulted
// values when the file is missing. In practice, Load's missing-file branch
// returns early and leaves StateFilePath empty.
func TestLoadMissingFileShouldDefaultStateFilePath(t *testing.T) {
	isolateConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StateFilePath != "/tmp/unky-mo-state.json" {
		t.Fatalf("StateFilePath on missing file: want %q, got %q — Load's no-file branch skips defaults (config.go:83)",
			"/tmp/unky-mo-state.json", cfg.StateFilePath)
	}
}

// Same drift for the tickets panel defaults. Documented defaults are
// RefreshSeconds=300, PerBucketLimit=5 — currently only applied when a
// file exists.
func TestLoadMissingFileShouldDefaultTicketsFields(t *testing.T) {
	isolateConfig(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tickets.RefreshSeconds != 300 {
		t.Errorf("Tickets.RefreshSeconds on missing file: want 300, got %d", cfg.Tickets.RefreshSeconds)
	}
	if cfg.Tickets.PerBucketLimit != 5 {
		t.Errorf("Tickets.PerBucketLimit on missing file: want 5, got %d", cfg.Tickets.PerBucketLimit)
	}
}
