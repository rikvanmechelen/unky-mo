package sidebar

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvanmech/unky-mo/internal/claude"
	"github.com/rvanmech/unky-mo/internal/state"
	"go.uber.org/mock/gomock"
)

// The golden tests pin what the sidebar draws so its split into files
// (docs/plans/review-remediation.md, phase 3) can show it changes nothing.
// Regenerate with `go test ./internal/tui/sidebar -run Golden -update` only
// for an intended change.
var updateGolden = flag.Bool("update", false, "rewrite the golden files")

func goldenSidebar(t *testing.T) *Model {
	t.Helper()
	m, _, cr, sf := newTestModel(t, "alpha", "@1")
	cr.EXPECT().FormatShellCommand(gomock.Any(), gomock.Any()).DoAndReturn(
		func(cmd string, width int) string { return cmd }).AnyTimes()
	now := time.Now()
	writeStateFile(t, sf, &state.StateFile{
		TmuxSession: "mo",
		Projects: []state.ProjectState{
			{Name: "alpha", Path: "/ws/alpha", WindowName: "alpha", WindowID: "@1", Status: "active"},
			{Name: "alpha@feat", Path: "/ws/alpha.worktrees/feat", WindowName: "alpha@feat", WindowID: "@2", Status: "permission", Parent: "alpha"},
			{Name: "beta", Path: "/ws/beta", WindowName: "beta", WindowID: "@3", Status: "idle", AgentKey: "g"},
			{Name: "scratch", Path: "/tmp/scratch", WindowName: "scratch", WindowID: "@4", Status: "active", Section: "external"},
		},
		Usage: &state.UsageState{
			FiveHourPct: 42, FiveHourResetsAt: now.Add(2*time.Hour + 30*time.Minute + 30*time.Second),
			SevenDayPct: 87, SevenDayResetsAt: now.Add(72 * time.Hour),
			FetchedAt: now,
		},
	})
	m.refreshState()
	m.width, m.height = 40, 50
	m.changedFiles = []string{"internal/tui/app.go", "internal/tui/golden_test.go", "README.md"}
	m.gitStatusMap = map[string]string{"internal/tui/app.go": "M", "internal/tui/golden_test.go": "?", "README.md": "M"}
	m.changedAdded, m.changedRemoved = 120, 7
	m.fileTreeMode = fileTreeModeChanged
	return m
}

func TestGoldenSidebar(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *Model)
	}{
		{"sidebar", func(m *Model) {}},
		{"sidebar_cursor", func(m *Model) { m.cursor = 3 }},
		{"sidebar_files_focus", func(m *Model) {
			m.focusSection = "files"
			m.fileCursor = 1
		}},
		{"sidebar_full_tree", func(m *Model) {
			m.fileTreeMode = fileTreeModeFull
			m.dirTree = buildDirTree([]string{"README.md", "go.mod", "internal/tui/app.go", "internal/tui/golden_test.go", "internal/web/server.go"})
		}},
		{"sidebar_shells", func(m *Model) {
			m.activeShells = []claude.ActiveShell{{Command: "go test ./..."}, {Command: "npm run dev"}}
		}},
		{"sidebar_drawer", func(m *Model) {
			m.terminals = []TerminalTab{{PaneID: "%5", Name: "term-1"}, {PaneID: "%6", Name: "term-2"}}
			m.activeTermIdx = 1
			m.drawerOpen = true
		}},
		{"sidebar_tokens_sync", func(m *Model) {
			m.sessionTokens = 123456
			m.syncStatus = "synced"
		}},
		{"sidebar_status", func(m *Model) { m.statusMsg = "pushed alpha" }},
		{"sidebar_narrow", func(m *Model) { m.width, m.height = 24, 20 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := goldenSidebar(t)
			tc.setup(m)
			checkGolden(t, tc.name, m.View().Content)
		})
	}
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file (run with -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want\n%s\n--- got\n%s", name, path, plain(string(want)), plain(got))
	}
}

func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
