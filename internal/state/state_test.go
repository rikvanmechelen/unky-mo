package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	in := &StateFile{
		TmuxSession: "mo",
		Projects: []ProjectState{
			{Name: "alpha", Path: "/ws/alpha", WindowName: "alpha", Status: "active", Index: 0, SessionID: "s1"},
			{Name: "alpha", Path: "/ws/alpha", WindowName: "alpha [2]", Status: "idle", Index: 2, SessionID: "s2"},
			{Name: "@feat", Parent: "beta", Path: "/ws/beta.worktrees/feat", WindowName: "beta@feat", Status: "permission", Index: -1, SessionID: "s3"},
			{Name: "stray", Path: "/tmp", WindowName: "stray", Status: "external", Section: "external", Branch: "main", Dirty: 2, Index: 0, SessionID: "s4"},
		},
		Usage: &UsageState{
			FiveHourPct:      42,
			FiveHourResetsAt: now.Add(3 * time.Hour),
			SevenDayPct:      80,
			SevenDayResetsAt: now.Add(72 * time.Hour),
			FetchedAt:        now,
			Stale:            false,
		},
	}

	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if got.TmuxSession != "mo" {
		t.Errorf("TmuxSession: %q", got.TmuxSession)
	}
	if len(got.Projects) != len(in.Projects) {
		t.Fatalf("project count: want %d, got %d", len(in.Projects), len(got.Projects))
	}
	for i, p := range got.Projects {
		want := in.Projects[i]
		if p.Name != want.Name || p.Path != want.Path || p.WindowName != want.WindowName ||
			p.Status != want.Status || p.Parent != want.Parent || p.Section != want.Section ||
			p.SessionID != want.SessionID {
			t.Errorf("project %d mismatch:\ngot  %+v\nwant %+v", i, p, want)
		}
	}

	// Sibling entry has Index=2 (preserved since non-zero).
	if got.Projects[1].Index != 2 {
		t.Errorf("sibling Index: want 2, got %d", got.Projects[1].Index)
	}
	// Custom-title entry has Index=-1.
	if got.Projects[2].Index != -1 {
		t.Errorf("custom-title Index: want -1, got %d", got.Projects[2].Index)
	}

	if got.Usage == nil {
		t.Fatal("Usage should round-trip")
	}
	if got.Usage.FiveHourPct != 42 || got.Usage.SevenDayPct != 80 {
		t.Errorf("usage percentages lost: %+v", got.Usage)
	}
}

func TestWriteAtomicTempAndRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := &StateFile{TmuxSession: "x"}
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The .tmp file must not remain after a successful rename.
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("unexpected leftover tmp file: %s", e.Name())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("final state file should exist: %v", err)
	}
}

func TestWriteSetsUpdatedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &StateFile{TmuxSession: "x"}
	before := time.Now()
	if err := Write(path, s); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt outside expected window: %v (not in %v..%v)", got.UpdatedAt, before, after)
	}
}

func TestRemoveDeletesBothFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// Create both the real file and a stray .tmp.
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	Remove(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("main file should be gone, err=%v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file should be gone, err=%v", err)
	}
}

func TestReadMissingIsError(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Error("Read on missing file should error")
	}
}

func TestIndexOmitemptyStripsZero(t *testing.T) {
	// Document the observable behavior: Index=0 is stripped by the `omitempty`
	// JSON tag. A primary window therefore serializes without the field.
	s := &StateFile{
		Projects: []ProjectState{{Name: "p", Index: 0}},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if containsField(data, "index") {
		t.Errorf("Index=0 should be omitted from JSON, got %s", data)
	}
}

func containsField(b []byte, field string) bool {
	s := string(b)
	// Looking for "index" as a JSON object key (naive but sufficient for the tiny fixture).
	return contains(s, `"`+field+`"`)
}

func contains(hay, needle string) bool { return len(hay) >= len(needle) && indexOf(hay, needle) >= 0 }
func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
