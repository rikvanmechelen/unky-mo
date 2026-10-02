package web

import (
	"os"
	"testing"
	"time"
)

func TestTranscriptHubBacklogThenLive(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "session.jsonl", "")

	h := newTranscriptHub()
	backlog, ch, unsubscribe, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()
	if len(backlog) != 0 {
		t.Fatalf("expected empty backlog for an empty file, got %d", len(backlog))
	}

	line := `{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"hi"}]}}`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-ch:
			if string(msg) != line {
				t.Fatalf("want %q, got %q", line, string(msg))
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the live-tailed message")
		}
	}
}

func TestTranscriptHubLateSubscriberGetsFullBacklog(t *testing.T) {
	dir := t.TempDir()
	line := `{"type":"user","uuid":"u1","message":{"content":"hi"}}`
	path := writeFile(t, dir, "session.jsonl", line+"\n")

	h := newTranscriptHub()
	_, ch1, unsub1, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe 1: %v", err)
	}
	defer unsub1()
	_ = ch1

	backlog2, ch2, unsub2, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe 2: %v", err)
	}
	defer unsub2()
	_ = ch2

	if len(backlog2) != 1 || string(backlog2[0]) != line {
		t.Fatalf("expected the second subscriber to see the full existing backlog, got %v", backlog2)
	}
}

func TestTranscriptHubTeardownOnLastUnsubscribe(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "session.jsonl", "")

	h := newTranscriptHub()
	_, _, unsub1, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe 1: %v", err)
	}
	_, ch2, unsub2, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe 2: %v", err)
	}

	unsub1()

	// The remaining subscriber must still get live updates.
	line := `{"type":"user","uuid":"u1","message":{"content":"still here"}}`
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(line + "\n")
	f.Close()

	select {
	case msg := <-ch2:
		if string(msg) != line {
			t.Fatalf("want %q, got %q", line, string(msg))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remaining subscriber never got the live update")
	}

	unsub2()

	// A fresh subscribe afterward must still work cleanly (correct backlog,
	// still gets live updates) — proving teardown-then-recreate is clean.
	backlog3, ch3, unsub3, err := h.subscribe("sess1", path)
	if err != nil {
		t.Fatalf("subscribe 3 (after full teardown): %v", err)
	}
	defer unsub3()
	if len(backlog3) != 1 {
		t.Fatalf("expected fresh subscribe to see 1 backlog message, got %d", len(backlog3))
	}

	line2 := `{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"again"}]}}`
	f2, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f2.WriteString(line2 + "\n")
	f2.Close()

	select {
	case msg := <-ch3:
		if string(msg) != line2 {
			t.Fatalf("want %q, got %q", line2, string(msg))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fresh subscriber never got a live update after teardown+recreate")
	}
}
