package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSONL(t *testing.T, dir, sessionID string, lines []string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionTitleLastWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"custom-title","customTitle":"first-title"}`,
		`{"type":"assistant","message":{"role":"assistant","content":"hi","stop_reason":"end_turn"}}`,
		`{"type":"custom-title","customTitle":"second-title"}`,
	})

	if got := SessionTitle(path); got != "second-title" {
		t.Errorf("SessionTitle: want %q, got %q", "second-title", got)
	}
	if got := CustomTitleFor("/workspace/myproj", "sess1"); got != "second-title" {
		t.Errorf("CustomTitleFor: want %q, got %q", "second-title", got)
	}
}

func TestSessionTitleMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"hello","stop_reason":"end_turn"}}`,
	})
	if got := SessionTitle(path); got != "" {
		t.Errorf("SessionTitle: want empty, got %q", got)
	}
}

func TestSessionTitleSkipsMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`not valid json at all`,
		`{"type":"custom-title","customTitle":"only-good"}`,
		`{broken`,
	})
	if got := SessionTitle(path); got != "only-good" {
		t.Errorf("SessionTitle: want %q, got %q", "only-good", got)
	}
}

func TestSessionTitleEmptyCustomTitleIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"custom-title","customTitle":"real-title"}`,
		`{"type":"custom-title","customTitle":""}`,
	})
	// Empty custom titles must not overwrite a set one (code guard: CustomTitle != "").
	if got := SessionTitle(path); got != "real-title" {
		t.Errorf("SessionTitle: empty title shouldn't overwrite, got %q", got)
	}
}
