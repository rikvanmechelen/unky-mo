package sidebar

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/state"
	mock_sidebar "github.com/rvanmech/unky-mo/internal/tui/sidebar/mocks"
	"go.uber.org/mock/gomock"
)

// newTestModel builds a Model ready for refreshState() tests. The caller
// supplies a fake resolver so the sidebar "thinks" it's in a specific
// window without any tmux calls. The returned Model has an empty items
// slice; call m.refreshState() to exercise the reconciler.
func newTestModel(t *testing.T, name, id string) (*Model, *mock_sidebar.MockTmuxClient, *mock_sidebar.MockClaudeReader, string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	tmux := mock_sidebar.NewMockTmuxClient(ctrl)
	claude := mock_sidebar.NewMockClaudeReader(ctrl)
	stateFile := filepath.Join(t.TempDir(), "state.json")

	// Default expectations for things refreshState always calls:
	//   - ClaudeReader.SessionsForPath (via ownWindowSession) → zero
	//   - ClaudeReader.ActiveShells is only reached when ownWindowSession
	//     finds a session, so no default expectation is needed.
	claude.EXPECT().SessionsForPath(gomock.Any()).Return(nil).AnyTimes()
	claude.EXPECT().ActiveShells(gomock.Any()).Return(nil).AnyTimes()

	m := &Model{
		tmux:          tmux,
		claude:        claude,
		resolver:      FakeWindowResolver{Name: name, ID: id},
		stateFile:     stateFile,
		windowName:    name,
		windowID:      id,
		activeTermIdx: -1,
		focusSection:  "sessions",
	}
	return m, tmux, claude, stateFile
}

// writeStateFile writes an atomic StateFile to path for the sidebar to read.
func writeStateFile(t *testing.T, path string, sf *state.StateFile) {
	t.Helper()
	if sf.UpdatedAt.IsZero() {
		sf.UpdatedAt = time.Now()
	}
	if err := state.Write(path, sf); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

func TestRefreshStateHappyPath(t *testing.T) {
	m, _, _, sf := newTestModel(t, "alpha", "@1")
	writeStateFile(t, sf, &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{Name: "alpha", Path: "/ws/alpha", WindowName: "alpha", WindowID: "@1", Status: "active"},
			{Name: "beta", Path: "/ws/beta", WindowName: "beta", WindowID: "@2", Status: "idle"},
			{Name: "gamma", Path: "/ws/gamma", WindowName: "gamma", WindowID: "@3", Status: "none"}, // skipped
		},
	})
	m.refreshState()

	// items: Home + alpha + beta (gamma filtered).
	if len(m.items) != 3 {
		t.Fatalf("want 3 items (Home + alpha + beta), got %d: %+v", len(m.items), m.items)
	}
	if !m.items[0].IsHome {
		t.Errorf("first item should be Home")
	}
	if m.items[1].Name != "alpha" || m.items[2].Name != "beta" {
		t.Errorf("project order wrong: %q, %q", m.items[1].Name, m.items[2].Name)
	}
	// Cursor should land on the own window (alpha, index 1) on first load.
	if m.cursor != 1 {
		t.Errorf("cursor should land on own window (alpha); got %d", m.cursor)
	}
	if !m.cursorSetOnce {
		t.Error("cursorSetOnce should be true after first refresh")
	}
}

func TestRefreshStateSessionAppearsKeepsCursorStable(t *testing.T) {
	m, _, _, sf := newTestModel(t, "beta", "@2")

	// Tick 1: two projects, cursor lands on beta (index 2).
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
			{Name: "beta", WindowID: "@2", Status: "active"},
		},
	})
	m.refreshState()
	if m.cursor != 2 {
		t.Fatalf("cursor didn't land on own window (beta) initially; got %d", m.cursor)
	}

	// Tick 2: new project added at top. Cursor must still point at beta,
	// not at the new row that slid into its old index.
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "aardvark", WindowID: "@0", Status: "active"},
			{Name: "alpha", WindowID: "@1", Status: "active"},
			{Name: "beta", WindowID: "@2", Status: "active"},
		},
	})
	m.refreshState()
	// After tick 2, cursorSetOnce is true so cursor is NOT re-placed. It
	// remains at 2 — which is now "alpha" not "beta". This documents today's
	// behavior (the duplicate-names regression case). A future fix would
	// track cursor identity by WindowID and re-resolve.
	if m.cursor != 2 {
		t.Errorf("cursor changed unexpectedly to %d", m.cursor)
	}
	if m.items[m.cursor].Name == "beta" {
		t.Log("cursor follows beta — identity-tracking is in place")
	} else {
		t.Logf("cursor at index %d is %q (not beta) — documents current index-based tracking",
			m.cursor, m.items[m.cursor].Name)
	}
}

func TestRefreshStateSessionDisappearsClampsCursor(t *testing.T) {
	m, _, _, sf := newTestModel(t, "beta", "@2")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
			{Name: "beta", WindowID: "@2", Status: "active"},
			{Name: "gamma", WindowID: "@3", Status: "active"},
		},
	})
	m.refreshState()
	m.cursor = 3 // simulate user navigated to gamma

	// Tick 2: gamma removed. Cursor is now past the end of items.
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
			{Name: "beta", WindowID: "@2", Status: "active"},
		},
	})
	m.refreshState()
	// items should be Home + alpha + beta = 3, cursor must be < 3.
	if m.cursor >= len(m.items) {
		t.Errorf("cursor %d out of bounds for items len %d", m.cursor, len(m.items))
	}
}

func TestRefreshStateStatusTransition(t *testing.T) {
	m, _, _, sf := newTestModel(t, "alpha", "@1")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
		},
	})
	m.refreshState()
	if m.items[1].Status != "active" {
		t.Fatalf("tick 1: got %q", m.items[1].Status)
	}

	// Tick 2: status changes to permission.
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "permission"},
		},
	})
	m.refreshState()
	if m.items[1].Status != "permission" {
		t.Errorf("status didn't propagate: got %q, want permission", m.items[1].Status)
	}
}

func TestRefreshStateWindowRenamePreservesOwnMatch(t *testing.T) {
	// Own window: windowID=@5, any name. Main TUI renamed it to "alpha [wip]".
	m, _, _, sf := newTestModel(t, "alpha", "@5")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha [wip]", WindowID: "@5", WindowName: "alpha [wip]", Status: "active"},
		},
	})
	m.refreshState()
	// The single project row IS the own window because WindowID matches.
	if m.cursor != 1 {
		t.Errorf("cursor should be on own window (by id @5); got %d (%q)",
			m.cursor, m.items[m.cursor].Name)
	}
}

func TestRefreshStateExternalSection(t *testing.T) {
	m, _, _, sf := newTestModel(t, "alpha", "@1")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
			{Name: "stray", WindowID: "@9", Status: "external", Section: "external"},
		},
	})
	m.refreshState()
	// items: Home + alpha + "External" header + stray = 4
	if len(m.items) != 4 {
		t.Fatalf("want 4 items, got %d: %+v", len(m.items), m.items)
	}
	if !m.items[2].IsHeader || m.items[2].Name != "External" {
		t.Errorf("want External header at index 2, got %+v", m.items[2])
	}
	if m.items[3].Name != "stray" || m.items[3].Section != "external" {
		t.Errorf("stray row misplaced: %+v", m.items[3])
	}
}

func TestRefreshStateExternalHeaderHiddenWhenNoStrays(t *testing.T) {
	m, _, _, sf := newTestModel(t, "alpha", "@1")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
		},
	})
	m.refreshState()
	for _, item := range m.items {
		if item.IsHeader && item.Name == "External" {
			t.Error("External header should not appear when no external projects exist")
		}
	}
}

func TestRefreshStateStaleFileFallsBackToLiveSessions(t *testing.T) {
	m, _, claude, sf := newTestModel(t, "alpha", "@1")
	// state.Write always bumps UpdatedAt=now, so write the JSON by hand with
	// an old UpdatedAt.
	old := time.Now().Add(-60 * time.Second)
	raw := `{"tmux_session":"mo","projects":[{"name":"alpha","path":"/ws/alpha","window_name":"alpha","window_id":"@1","status":"active"}],"updated_at":"` + old.UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(sf, []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-populate items so refreshFromSessions has something to update.
	m.items = []SidebarItem{
		{Name: "Home", IsHome: true},
		{Name: "alpha", Path: "/ws/alpha"},
	}
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	m.refreshState()
	// Fallback path: existing items kept, statuses re-written from live sessions.
	if len(m.items) != 2 {
		t.Errorf("fallback should preserve items; got %d", len(m.items))
	}
	// Usage should NOT be populated from a stale file.
	if m.usage != nil {
		t.Errorf("usage should stay nil on stale fallback, got %+v", m.usage)
	}
}

func TestRefreshStateMissingFileDoesNotPanic(t *testing.T) {
	m, _, claude, _ := newTestModel(t, "alpha", "@1")
	// Point at a non-existent file.
	m.stateFile = filepath.Join(t.TempDir(), "does-not-exist.json")
	claude.EXPECT().LiveSessions().Return(nil, nil).AnyTimes()

	// Must not panic.
	m.refreshState()
}

func TestRefreshStateDuplicateNamesDocumentsIndexTracking(t *testing.T) {
	// Two projects named "api" with different Paths. This is a pathological
	// case the WindowID migration is meant to address.
	m, _, _, sf := newTestModel(t, "api", "@2")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "api", Path: "/ws/a/api", WindowID: "@1", Status: "active"},
			{Name: "api", Path: "/ws/b/api", WindowID: "@2", Status: "active"},
		},
	})
	m.refreshState()
	// Own window is @2 → cursor should land on the *second* api row (index 2).
	if m.cursor != 2 {
		t.Errorf("with duplicate names, cursor should resolve via WindowID to index 2; got %d", m.cursor)
	}
	if m.items[m.cursor].Path != "/ws/b/api" {
		t.Errorf("cursor landed on wrong duplicate: %q", m.items[m.cursor].Path)
	}
}

func TestRefreshStateUsagePopulated(t *testing.T) {
	m, _, _, sf := newTestModel(t, "alpha", "@1")
	writeStateFile(t, sf, &state.StateFile{
		Projects: []state.ProjectState{
			{Name: "alpha", WindowID: "@1", Status: "active"},
		},
		Usage: &state.UsageState{
			FiveHourPct:      42,
			SevenDayPct:      80,
			FiveHourResetsAt: time.Now().Add(2 * time.Hour),
		},
	})
	m.refreshState()
	if m.usage == nil {
		t.Fatal("usage should be populated from state file")
	}
	if m.usage.FiveHourPct != 42 || m.usage.SevenDayPct != 80 {
		t.Errorf("usage percentages dropped: %+v", m.usage)
	}
}
