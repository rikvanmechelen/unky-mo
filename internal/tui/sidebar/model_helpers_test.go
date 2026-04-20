package sidebar

import "testing"

// Phase 1 coverage — small pure helpers that already exist but had no
// dedicated tests. `itemMatchesOwnWindow` is the most interesting: it
// decides whether a state-file row belongs to the sidebar's own window,
// which is the foundation for the Phase 5 WindowID-over-WindowName fixes.

func TestItemMatchesOwnWindowPrefersID(t *testing.T) {
	cases := []struct {
		name          string
		item          SidebarItem
		ownInstanceID string
		ownID         string
		ownName       string
		want          bool
	}{
		{
			name:    "matching window ids → match even if names differ",
			item:    SidebarItem{WindowName: "alpha", WindowID: "@5"},
			ownID:   "@5",
			ownName: "alpha [wip]",
			want:    true,
		},
		{
			name:    "mismatched window ids → no match even if names match",
			item:    SidebarItem{WindowName: "alpha", WindowID: "@7"},
			ownID:   "@5",
			ownName: "alpha",
			want:    false,
		},
		{
			name:    "id unresolved on both sides → fall back to name match",
			item:    SidebarItem{WindowName: "alpha"},
			ownName: "alpha",
			want:    true,
		},
		{
			name:    "id on row only → fall back to name match on row",
			item:    SidebarItem{WindowName: "alpha", WindowID: "@9"},
			ownName: "alpha",
			want:    true,
		},
		{
			name:    "id on own only → fall back to name match",
			item:    SidebarItem{WindowName: "alpha"},
			ownID:   "@5",
			ownName: "alpha",
			want:    true,
		},
		{
			name:    "no id, different names → no match",
			item:    SidebarItem{WindowName: "alpha"},
			ownName: "beta",
			want:    false,
		},
		{
			name:          "instance ID match → strongest key",
			item:          SidebarItem{WindowName: "beta", WindowID: "@7", InstanceID: "a1b2c3d4e5f6"},
			ownInstanceID: "a1b2c3d4e5f6",
			ownID:         "@5",
			ownName:       "alpha",
			want:          true,
		},
		{
			name:          "instance ID mismatch → no match even if window id matches",
			item:          SidebarItem{WindowName: "alpha", WindowID: "@5", InstanceID: "aabbccddeeff"},
			ownInstanceID: "a1b2c3d4e5f6",
			ownID:         "@5",
			ownName:       "alpha",
			want:          false,
		},
		{
			name:          "instance ID on sidebar only → fall back to window id",
			item:          SidebarItem{WindowName: "alpha", WindowID: "@5"},
			ownInstanceID: "a1b2c3d4e5f6",
			ownID:         "@5",
			ownName:       "alpha",
			want:          true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := itemMatchesOwnWindow(tc.item, tc.ownInstanceID, tc.ownID, tc.ownName)
			if got != tc.want {
				t.Errorf("itemMatchesOwnWindow(%+v, %q, %q, %q) = %v, want %v",
					tc.item, tc.ownInstanceID, tc.ownID, tc.ownName, got, tc.want)
			}
		})
	}
}

func TestEnsureCursorVisibleScrollsDownToKeepCursor(t *testing.T) {
	m := &Model{
		cursor:        20,
		viewportStart: 0,
		height:        10, // headerLines=1, footerLines=5 → maxVisible=4
	}
	m.ensureCursorVisible()
	if m.viewportStart == 0 {
		t.Errorf("viewportStart should have advanced past 0, got %d", m.viewportStart)
	}
	// cursor must fall within the computed viewport window.
	if m.cursor < m.viewportStart {
		t.Errorf("cursor (%d) < viewportStart (%d) — cursor scrolled off top",
			m.cursor, m.viewportStart)
	}
}

func TestEnsureCursorVisibleScrollsUpWhenCursorMovesAboveViewport(t *testing.T) {
	m := &Model{
		cursor:        2,
		viewportStart: 10,
		height:        10,
	}
	m.ensureCursorVisible()
	if m.viewportStart > m.cursor {
		t.Errorf("viewportStart (%d) should not exceed cursor (%d)",
			m.viewportStart, m.cursor)
	}
}

func TestEnsureCursorVisibleHandlesTinyHeight(t *testing.T) {
	// If height is small or zero, maxVisible is clamped to 1 so the cursor is
	// at least placed at the viewport start. Should never panic or loop.
	m := &Model{cursor: 5, viewportStart: 0, height: 1}
	m.ensureCursorVisible()
	if m.viewportStart != m.cursor {
		t.Errorf("with tiny viewport, viewportStart should track cursor; got %d vs %d",
			m.viewportStart, m.cursor)
	}
}

// Smoke-test the fake resolver so the interface contract matches what tests
// in later phases rely on.
func TestFakeWindowResolverReturnsConfigured(t *testing.T) {
	r := FakeWindowResolver{Name: "alpha@feat", ID: "@5"}
	n, id := r.ResolveOwnWindow()
	if n != "alpha@feat" || id != "@5" {
		t.Errorf("FakeWindowResolver returned (%q, %q), want (alpha@feat, @5)", n, id)
	}
}
