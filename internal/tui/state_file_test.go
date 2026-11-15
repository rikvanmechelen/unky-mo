package tui

import (
	"testing"
)

// buildStateFile lists, in the dashboard's order: each project's main
// checkout sessions (or a placeholder row), then its worktrees' sessions
// under it, then git-backed strays, then externals; usage once known.
func TestBuildStateFile(t *testing.T) {
	m := goldenModel(t)
	sf := m.buildStateFile()
	type row struct{ name, parent, status, section string }
	var got []row
	for _, p := range sf.Projects {
		got = append(got, row{p.Name, p.Parent, p.Status, p.Section})
	}
	want := []row{
		{"alpha", "", "active", "projects"},
		{"@feat", "alpha", "permission", "projects"},
		{"beta", "", "idle", "projects"},
		{"gamma", "", "none", "projects"},
		{"other", "", "idle", "projects"},
		{"scratch", "", "question", "external"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if sf.Usage == nil || sf.Usage.FiveHourPct != 42 || sf.Usage.SevenDayPct != 87 {
		t.Errorf("usage %+v", sf.Usage)
	}
}
