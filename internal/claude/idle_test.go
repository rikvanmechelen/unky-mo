package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJSONL helper is defined in title_test.go (same package).

func backdateFile(t *testing.T, path string, by time.Duration) {
	t.Helper()
	when := time.Now().Add(-by)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestLastMessagesRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"reply 1","stop_reason":"end_turn"}}`,
		`{"type":"user","message":{"role":"user","content":"second"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"reply 2","stop_reason":"end_turn"}}`,
	})
	msgs := LastMessages("/workspace/myproj", "sess1", 3)
	if len(msgs) == 0 {
		t.Fatal("expected at least one message")
	}
	// Should be in chronological order; last entry must be an assistant reply.
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" {
		t.Errorf("last message role: want assistant, got %q", last.Role)
	}
}

func TestIsAliveSelf(t *testing.T) {
	if !IsAlive(os.Getpid()) {
		t.Errorf("IsAlive(self) should be true")
	}
	// PID 1 (init) always exists on Unix; a very large PID almost certainly does not.
	if IsAlive(2147483640) {
		t.Errorf("IsAlive(huge-pid) should be false")
	}
}

// Sanity-check the helper path resolution used everywhere else in these tests.
func TestJSONLPathResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/a/b")
	writeJSONL(t, dir, "sess1", []string{`{"type":"custom-title","customTitle":"t"}`})
	if _, err := os.Stat(filepath.Join(dir, "sess1.jsonl")); err != nil {
		t.Fatalf("fixture not in resolved dir: %v", err)
	}
}
