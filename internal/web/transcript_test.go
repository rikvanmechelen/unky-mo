package web

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestTranscriptCursorFiltersToUserAndAssistant(t *testing.T) {
	dir := t.TempDir()
	userLine := `{"type":"user","uuid":"u1","message":{"role":"user","content":"hi"}}`
	assistantLine := `{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`
	excludedLine := `{"type":"file-history-snapshot","uuid":"f1"}`
	path := writeFile(t, dir, "session.jsonl", userLine+"\n"+assistantLine+"\n"+excludedLine+"\n")

	c := newTranscriptCursor(path)
	msgs, err := c.readNew()
	if err != nil {
		t.Fatalf("readNew: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (user+assistant), got %d: %v", len(msgs), msgs)
	}
	if string(msgs[0]) != userLine {
		t.Errorf("message 0: want %q, got %q", userLine, string(msgs[0]))
	}
	if string(msgs[1]) != assistantLine {
		t.Errorf("message 1: want %q, got %q", assistantLine, string(msgs[1]))
	}
}

func TestTranscriptCursorBuffersPartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	full := `{"type":"user","uuid":"u1","message":{"role":"user","content":"hi"}}`

	// Write the line WITHOUT a trailing newline — simulates a write landing mid-line.
	if err := os.WriteFile(path, []byte(full), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c := newTranscriptCursor(path)
	msgs, err := c.readNew()
	if err != nil {
		t.Fatalf("readNew (partial): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages while line is incomplete, got %d: %v", len(msgs), msgs)
	}

	// Now "finish" the line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	msgs, err = c.readNew()
	if err != nil {
		t.Fatalf("readNew (completed): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected exactly 1 message once the line completes, got %d: %v", len(msgs), msgs)
	}
	if string(msgs[0]) != full {
		t.Errorf("want %q, got %q", full, string(msgs[0]))
	}
}

func TestTranscriptCursorReturnsNothingWhenNoNewBytes(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "session.jsonl", `{"type":"user","uuid":"u1","message":{"content":"hi"}}`+"\n")

	c := newTranscriptCursor(path)
	if _, err := c.readNew(); err != nil {
		t.Fatalf("readNew (first): %v", err)
	}
	msgs, err := c.readNew()
	if err != nil {
		t.Fatalf("readNew (second): %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no new messages on second call, got %d", len(msgs))
	}
}

func TestTranscriptCursorHandlesTruncation(t *testing.T) {
	dir := t.TempDir()
	line1 := `{"type":"user","uuid":"u1","message":{"content":"first"}}`
	path := writeFile(t, dir, "session.jsonl", line1+"\n")

	c := newTranscriptCursor(path)
	if _, err := c.readNew(); err != nil {
		t.Fatalf("readNew (first): %v", err)
	}

	// Simulate truncation+rewrite shorter than the tracked offset (line2 must
	// be strictly shorter than line1+"\n" for the truncation branch to fire).
	line2 := `{"type":"assistant","uuid":"a1"}`
	if err := os.WriteFile(path, []byte(line2+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile (truncate): %v", err)
	}

	msgs, err := c.readNew()
	if err != nil {
		t.Fatalf("readNew (after truncation): %v", err)
	}
	if len(msgs) != 1 || string(msgs[0]) != line2 {
		t.Fatalf("expected recovery to re-read from scratch and return %q, got %v", line2, msgs)
	}
}

func TestTranscriptCursorMissingFileIsEmpty(t *testing.T) {
	c := newTranscriptCursor(t.TempDir() + "/missing.jsonl")
	msgs, err := c.readNew()
	if err != nil || len(msgs) != 0 {
		t.Fatalf("want no messages and no error, got %v, %v", msgs, err)
	}
}
