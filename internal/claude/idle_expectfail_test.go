//go:build expectfail

// These tests document behaviors claimed by CLAUDE.md. Run with
// `go test -tags expectfail ./...`. Failures here flag either a code drift
// or a stale claim in the docs.

package claude

import (
	"testing"
	"time"
)

// CLAUDE.md ("Claude Session Data" section) documents: "Falls back to
// JSONL staleness >120s for permission prompt edge cases." The exact
// threshold — 120 seconds — is load-bearing and callers may depend on it.
// This test pins the boundary: 121s mtime age → idle, 119s → not idle.
func TestIdleFallbackThresholdIs120s(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/proj")

	// Synthetic user entry (slash-command shape) — the fallback the docstring
	// describes. Just below 120s → not idle.
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"<local-command-stdout>x</local-command-stdout>"}}`,
	})
	backdateFile(t, path, 119*time.Second)
	if IsSessionIdle("/workspace/proj", "sess1") {
		t.Errorf("119s stale: should NOT be idle (under threshold)")
	}

	// Just above 120s → idle.
	backdateFile(t, path, 121*time.Second)
	if !IsSessionIdle("/workspace/proj", "sess1") {
		t.Errorf("121s stale: should be idle (over threshold)")
	}
}
