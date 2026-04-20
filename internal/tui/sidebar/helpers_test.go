package sidebar

import (
	"strings"
	"testing"

	"github.com/rvanmech/unky-mo/internal/claude"
)

func TestRenderDotKnownStatuses(t *testing.T) {
	// Each mapped status renders a filled dot; anything else falls to an open circle.
	cases := []struct {
		status string
		want   string // the literal rune expected in the output
	}{
		{"active", "●"},
		{"idle", "●"},
		{"permission", "●"},
		{"external", "●"},
		{"", "○"},
		{"unknown", "○"},
	}
	for _, c := range cases {
		got := renderDot(c.status)
		if !strings.Contains(got, c.want) {
			t.Errorf("renderDot(%q) should contain %q, got %q", c.status, c.want, got)
		}
	}
}

func TestTruncateName(t *testing.T) {
	cases := []struct {
		in     string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"just-right", 10, "just-right"},
		{"a-bit-too-long", 10, "a-bit-t..."},
		// maxLen <= 3: hard cut, no ellipsis.
		{"abcdef", 3, "abc"},
		{"abcdef", 2, "ab"},
		{"abcdef", 1, "a"},
	}
	for _, c := range cases {
		got := truncateName(c.in, c.maxLen)
		if got != c.want {
			t.Errorf("truncateName(%q, %d) = %q, want %q", c.in, c.maxLen, got, c.want)
		}
	}
}

func TestRenderSectionHeader(t *testing.T) {
	// The header renders with the label embedded. We don't lock the exact
	// rune counts since the style wrapper may vary; just assert presence.
	out := renderSectionHeader("Sessions", 40)
	if !strings.Contains(out, "Sessions") {
		t.Errorf("header missing label: %q", out)
	}
	// Narrow-width fallback renders with -- around the label.
	out = renderSectionHeader("Files", 5)
	if !strings.Contains(out, "Files") {
		t.Errorf("narrow fallback missing label: %q", out)
	}
}

func TestBuildFileTreeLinesSingleFile(t *testing.T) {
	lines := buildFileTreeLines([]string{"main.go"})
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	if lines[0].display != "main.go" || lines[0].fileIndex != 0 {
		t.Errorf("line wrong: %+v", lines[0])
	}
}

func TestBuildFileTreeLinesCollapsesSingleChildDir(t *testing.T) {
	// internal/tui/app.go — "internal" has only "tui" as child, which has only "app.go".
	// The single-child dir collapse merges "internal/tui" into one node.
	lines := buildFileTreeLines([]string{"internal/tui/app.go"})
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (dir + file), got %d: %+v", len(lines), lines)
	}
	if !strings.Contains(lines[0].display, "internal") || !strings.Contains(lines[0].display, "tui") {
		t.Errorf("dir node should be collapsed, got %q", lines[0].display)
	}
	if lines[1].display != "app.go" || lines[1].fileIndex != 0 {
		t.Errorf("file line wrong: %+v", lines[1])
	}
}

func TestBuildFileTreeLinesSiblings(t *testing.T) {
	// Two files in same dir: dir is NOT collapsed (has multiple children).
	lines := buildFileTreeLines([]string{"pkg/a.go", "pkg/b.go"})
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (dir + 2 files), got %d: %+v", len(lines), lines)
	}
	if lines[0].display != "pkg" {
		t.Errorf("first line should be 'pkg', got %q", lines[0].display)
	}
	// File indexes preserve input order.
	if lines[1].fileIndex != 0 || lines[2].fileIndex != 1 {
		t.Errorf("file indexes lost: %+v / %+v", lines[1], lines[2])
	}
}

func TestRenderFileTreeEmptyReturnsEmpty(t *testing.T) {
	if got := renderFileTree(nil, 40, 10); got != "" {
		t.Errorf("empty file list should render empty string, got %q", got)
	}
}

func TestRenderFileTreeProducesNonEmpty(t *testing.T) {
	got := renderFileTree([]string{"main.go", "pkg/a.go", "pkg/b.go"}, 40, 10)
	if got == "" {
		t.Error("expected non-empty render for a few files")
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "a.go") {
		t.Errorf("rendered output missing filenames:\n%s", got)
	}
}

func TestTermSessionScopedPerWindow(t *testing.T) {
	cases := []struct {
		name       string
		windowID   string
		windowName string
		want       string
	}{
		{"windowID preferred", "@5", "moma-chatbot", "mo-terms-5"},
		{"windowName fallback", "", "moma-chatbot", "mo-terms-moma-chatbot"},
		{"sanitize bracket-and-space", "", "moma-chatbot [2]", "mo-terms-moma-chatbot-[2]"},
		{"sanitize colon", "", "foo:bar", "mo-terms-foo-bar"},
		{"sanitize dot", "", "foo.bar", "mo-terms-foo-bar"},
		{"bare fallback", "", "", "mo-terms"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{windowID: c.windowID, windowName: c.windowName}
			if got := m.termSession(); got != c.want {
				t.Errorf("termSession(id=%q, name=%q) = %q, want %q",
					c.windowID, c.windowName, got, c.want)
			}
		})
	}
}

func TestSyncProjectName(t *testing.T) {
	cases := []struct {
		name       string
		windowName string
		want       string
	}{
		{"bare project", "moma-chatbot", "moma-chatbot"},
		{"with session title", "moma-chatbot [setup-scaffold]", "moma-chatbot"},
		{"with ordinal", "moma-chatbot [2]", "moma-chatbot"},
		{"worktree bare", "unky-mo@feat-auth", "unky-mo@feat-auth"},
		{"worktree with suffix", "unky-mo@feat-auth [debug-oauth]", "unky-mo@feat-auth"},
		{"empty falls back", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Model{windowName: c.windowName}
			if got := m.syncProjectName(); got != c.want {
				t.Errorf("syncProjectName() = %q, want %q", got, c.want)
			}
		})
	}
}

// Phase A2: Characterization tests for pickOwnSession — the core
// disambiguation logic extracted from ownWindowSession.

func TestPickOwnSession_SingleCandidate(t *testing.T) {
	candidates := []claude.Session{{PID: 100, SessionID: "s1"}}
	// With a single candidate and no pane PIDs, returns it directly.
	got := pickOwnSession(candidates, nil, func(int, map[int]bool) bool { return false })
	if got == nil || got.SessionID != "s1" {
		t.Errorf("single candidate should return it, got %v", got)
	}
}

func TestPickOwnSession_TwoSiblings_PicksPaneDescendant(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true, 51: true}
	// Only PID 200 descends from the pane tree.
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return pid == 200
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s2" {
		t.Errorf("should pick s2 (PID 200 is descendant), got %v", got)
	}
}

func TestPickOwnSession_NoMatchingDescendant_ReturnsFirst(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true}
	// Neither PID descends from the pane tree.
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return false
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s1" {
		t.Errorf("no match should fall back to first candidate, got %v", got)
	}
}

func TestPickOwnSession_FirstCandidateIsDescendant(t *testing.T) {
	candidates := []claude.Session{
		{PID: 100, SessionID: "s1"},
		{PID: 200, SessionID: "s2"},
	}
	panePIDs := map[int]bool{50: true}
	isDescendant := func(pid int, hosts map[int]bool) bool {
		return pid == 100
	}
	got := pickOwnSession(candidates, panePIDs, isDescendant)
	if got == nil || got.SessionID != "s1" {
		t.Errorf("should pick s1, got %v", got)
	}
}

func TestRenderFileTreeRespectsMaxLines(t *testing.T) {
	files := []string{}
	for i := 0; i < 20; i++ {
		files = append(files, "pkg/f"+string(rune('a'+i))+".go")
	}
	got := renderFileTree(files, 40, 5)
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) > 6 {
		// Some implementations add a "…" overflow line on top of maxLines; allow +1.
		t.Errorf("maxLines=5 but got %d lines", len(lines))
	}
}
