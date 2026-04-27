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

func TestIsSessionIdleEndTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"done","stop_reason":"end_turn"}}`,
	})
	_ = path
	if !IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("stop_reason=end_turn should be idle")
	}
}

func TestIsSessionIdleToolUse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"tool","stop_reason":"tool_use"}}`,
	})
	if IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("stop_reason=tool_use should NOT be idle (Claude still working)")
	}
}

// A tool_use entry older than 120s means the tool is almost certainly
// blocked on the user (AskUserQuestion, permission prompt the hook missed) —
// the session should light up idle instead of staying active.
func TestIsSessionIdleToolUseStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"go"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"tool","stop_reason":"tool_use"}}`,
	})
	backdateFile(t, path, 130*time.Second)
	if !IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("stop_reason=tool_use with mtime > 120s should be idle (stuck on user input)")
	}
}

func TestIsSessionIdleStaleFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	// File with no assistant/user entries — only skipped types. Fallback path triggers
	// based on file mtime > 120s.
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"file-history-snapshot"}`,
		`{"type":"attachment"}`,
	})
	backdateFile(t, path, 130*time.Second)
	if !IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("mtime > 120s with no assistant message should fall back to idle")
	}
}

func TestIsSessionIdleRecentNoAssistant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	// Recent file, no assistant/user entries → fallback says "not stale yet".
	writeJSONL(t, dir, "sess1", []string{
		`{"type":"file-history-snapshot"}`,
	})
	if IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("recent file with no assistant msg should NOT be idle")
	}
}

func TestIsSessionIdleSlashCommandStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	// Slash-command synthetic user entry with stale mtime — idle (the docstring
	// explicitly covers this case).
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"user","message":{"role":"user","content":"<local-command-stdout>result</local-command-stdout>"}}`,
	})
	backdateFile(t, path, 130*time.Second)
	if !IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("user entry with stale mtime should be idle")
	}
}

// A regular user prompt means Claude is thinking about the response. Even if
// the JSONL hasn't been written to in a while (complex tasks can take minutes),
// the session is still active — not idle.
func TestIsSessionIdleUserPromptStaleNotIdle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"assistant","message":{"role":"assistant","content":"done","stop_reason":"end_turn"}}`,
		`{"type":"user","message":{"role":"user","content":"please refactor the auth module"}}`,
	})
	backdateFile(t, path, 200*time.Second)
	if IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("regular user prompt should NOT be idle — Claude is still thinking")
	}
}

// A tool_result means Claude is mid-turn processing tool output. Never idle.
func TestIsSessionIdleToolResultNotIdle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ProjectsDirForPath("/workspace/myproj")
	path := writeJSONL(t, dir, "sess1", []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use"}],"stop_reason":"tool_use"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
	})
	backdateFile(t, path, 200*time.Second)
	if IsSessionIdle("/workspace/myproj", "sess1") {
		t.Errorf("tool_result should NOT be idle — Claude is mid-turn")
	}
}

func TestIsSessionIdleMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if IsSessionIdle("/workspace/myproj", "does-not-exist") {
		t.Errorf("missing JSONL should not be reported as idle")
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
