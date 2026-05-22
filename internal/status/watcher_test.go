package status

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcher_DetectsWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess1.jsonl")
	os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0644)

	var called atomic.Int32
	var gotID, gotPath string
	w, err := NewWatcher(func(sessionID, p string) {
		gotID = sessionID
		gotPath = p
		called.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	w.WatchSession("s1", path)

	// Give fsnotify time to register, then write.
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(path, []byte(`{"type":"assistant","message":{"stop_reason":"end_turn"}}`+"\n"), 0644)

	deadline := time.After(2 * time.Second)
	for called.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for write event")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if gotID != "s1" {
		t.Errorf("sessionID: got %q, want s1", gotID)
	}
	abs, _ := filepath.Abs(path)
	if gotPath != abs {
		t.Errorf("path: got %q, want %q", gotPath, abs)
	}
}

func TestWatcher_AddRemoveSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess1.jsonl")
	os.WriteFile(path, []byte("init\n"), 0644)

	var called atomic.Int32
	w, err := NewWatcher(func(string, string) { called.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	w.WatchSession("s1", path)
	w.UnwatchSession("s1")

	time.Sleep(50 * time.Millisecond)
	os.WriteFile(path, []byte("after\n"), 0644)
	time.Sleep(200 * time.Millisecond)

	if called.Load() > 0 {
		t.Error("callback fired after UnwatchSession")
	}
}

func TestWatcher_MultipleSessionsSameDir(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "sess1.jsonl")
	path2 := filepath.Join(dir, "sess2.jsonl")
	os.WriteFile(path1, []byte("init\n"), 0644)
	os.WriteFile(path2, []byte("init\n"), 0644)

	ch := make(chan string, 16)
	w, err := NewWatcher(func(sessionID, _ string) {
		ch <- sessionID
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()

	w.WatchSession("s1", path1)
	w.WatchSession("s2", path2)

	time.Sleep(50 * time.Millisecond)
	os.WriteFile(path2, []byte("updated\n"), 0644)

	select {
	case id := <-ch:
		if id != "s2" {
			t.Errorf("expected s2, got %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for write event")
	}
}

func TestWatcher_StopCleansUp(t *testing.T) {
	w, err := NewWatcher(func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	// Stop should not hang or panic.
	w.Stop()
}

func TestWatcher_UnwatchUnknown_Noop(t *testing.T) {
	w, err := NewWatcher(func(string, string) {})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	w.UnwatchSession("nonexistent") // should not panic
}
